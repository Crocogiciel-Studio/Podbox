// Package workspace decides which project paths are hidden from the agent.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// Forced patterns are always masked; .sandboxignore cannot unmask them.
var Forced = []string{
	".env*", "*.pem", "*.key", "id_*",
	".ssh/", ".aws/", ".kube/", ".gnupg/",
	".npmrc", ".pypirc", ".netrc", ".git-credentials",
	".docker/config.json",
}

// IgnoreFile adds masks in gitignore syntax.
const IgnoreFile = ".sandboxignore"

// MaxMasks bounds the mounts added to one container.
const MaxMasks = 512

// Mask is a host path to hide.
type Mask struct {
	Rel string `json:"path"` // slash-separated, relative to the workspace
	Dir bool   `json:"dir"`
}

// Tracked is a masked path recorded with its file identity. The agent
// cannot move a masked file (its mount is busy) but can rename a parent
// directory, after which a pattern with a slash no longer matches: the
// identity, which a rename keeps, lets the mask follow the file.
type Tracked struct {
	Rel string `json:"path"` // where it was first masked
	Dir bool   `json:"dir"`
	Dev uint64 `json:"dev,omitempty"` // 0 with Ino when unknown (Windows)
	Ino uint64 `json:"ino,omitempty"`
	// Now is where it is found by this resolution, "" when missing.
	Now string `json:"-"`
}

// Move is a recorded masked path found elsewhere.
type Move struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Result lists masks, the updated record and warnings.
type Result struct {
	Masks []Mask
	// Tracked is the previous record, with current locations, plus the new
	// pattern matches. It never shrinks here (see Shrink).
	Tracked  []Tracked
	Moved    []Move   // recorded paths found at another path, still masked
	Missing  []string // recorded paths found nowhere
	Warnings []string // files that look secret and are not masked
}

// Matcher reports whether a slash-separated workspace path is masked by
// the forced patterns or the .sandboxignore content ignore.
func Matcher(ignore []byte) func(rel string, dir bool) bool {
	forced := gitignore.CompileIgnoreLines(Forced...)
	user := gitignore.CompileIgnoreLines(strings.Split(string(ignore), "\n")...)
	return func(rel string, dir bool) bool {
		if dir {
			rel += "/"
		}
		return forced.MatchesPath(rel) || user.MatchesPath(rel)
	}
}

type fileKey struct{ dev, ino uint64 }

// ErrMasks marks a mask set podbox cannot trust: a workspace path it could
// not read, or a recorded masked path found nowhere. The agent is not started.
var ErrMasks = errors.New("masks cannot be verified")

// Resolve walks root once, through an os.Root and without following
// symbolic links, and returns what to mask: what the patterns match
// now, and every recorded path wherever it now lives (same device and
// inode) or still at its recorded path. ignore is the .sandboxignore
// content to apply (the host-side snapshot, never the live workspace file).
// Any walk error fails the resolution (wrapping ErrMasks): a directory podbox
// cannot read may hold secrets the agent could read after a chmod.
func Resolve(root string, ignore []byte, tracked []Tracked) (Result, error) {
	res := Result{Tracked: append([]Tracked(nil), tracked...)}
	masked := Matcher(ignore)
	byID := map[fileKey]int{}
	byRel := map[string]int{}
	for i, t := range res.Tracked {
		res.Tracked[i].Now = ""
		if t.Ino != 0 {
			byID[fileKey{t.Dev, t.Ino}] = i
		}
		byRel[t.Rel] = i
	}
	seen := map[string]bool{}
	mask := func(rel string, dir bool) {
		if !seen[rel] {
			seen[rel] = true
			res.Masks = append(res.Masks, Mask{Rel: rel, Dir: dir})
		}
	}
	// A record is placed by identity first; at its recorded path with a new
	// identity (replaced on the host, e.g. an editor's save) only otherwise.
	byIDPlaced := make([]bool, len(res.Tracked))
	newID := make([]fileKey, len(res.Tracked))
	r, err := os.OpenRoot(root)
	if err != nil {
		return res, err
	}
	defer r.Close()
	err = fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return walkError(root, rel, err)
		}
		if rel == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // a link carries no content; its target is masked or absent
		}
		dir := d.IsDir()
		if dir && d.Name() == ".git" {
			return fs.SkipDir // protected by read-only binds and the integrity check
		}
		if !dir && !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return walkError(root, rel, err)
		}
		dev, ino, hasID := fileID(fi)
		switch i, ok := byID[fileKey{dev, ino}]; {
		case hasID && ok && res.Tracked[i].Dir == dir:
			if t := &res.Tracked[i]; !byIDPlaced[i] || rel == t.Rel {
				t.Now, byIDPlaced[i] = rel, true
			}
		case byRelMatch(byRel, rel):
			// Still at its recorded path, possibly replaced: masked there.
			if j := byRel[rel]; j < len(byIDPlaced) && !byIDPlaced[j] {
				res.Tracked[j].Now, res.Tracked[j].Dir, newID[j] = rel, dir, fileKey{dev, ino}
			}
		case masked(rel, dir):
			t := Tracked{Rel: rel, Dir: dir, Now: rel}
			if hasID {
				t.Dev, t.Ino = dev, ino
				byID[fileKey{dev, ino}] = len(res.Tracked)
			}
			byRel[rel] = len(res.Tracked)
			res.Tracked = append(res.Tracked, t)
		default:
			if !dir {
				if why := looksSecret(r, rel, d); why != "" {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s and is not masked; add it to %s and run podbox policy reload to hide it", rel, why, IgnoreFile))
				}
			}
			return nil
		}
		mask(rel, dir)
		if dir {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	for i := range res.Tracked {
		t := &res.Tracked[i]
		if i < len(byIDPlaced) && !byIDPlaced[i] && t.Now != "" && newID[i] != (fileKey{}) {
			t.Dev, t.Ino = newID[i].dev, newID[i].ino
		}
		switch {
		case t.Now == "" && covered(t.Rel, seen):
			t.Now = t.Rel // inside a masked directory: never visited
		case t.Now == "":
			res.Missing = append(res.Missing, t.Rel)
		case t.Now != t.Rel:
			res.Moved = append(res.Moved, Move{From: t.Rel, To: t.Now})
		}
	}
	if len(res.Masks) > MaxMasks {
		return res, fmt.Errorf("%d paths to mask exceed the limit of %d: mask parent directories in %s", len(res.Masks), MaxMasks, IgnoreFile)
	}
	sort.Slice(res.Masks, func(i, j int) bool { return res.Masks[i].Rel < res.Masks[j].Rel })
	return res, nil
}

func byRelMatch(byRel map[string]int, rel string) bool {
	_, ok := byRel[rel]
	return ok
}

func walkError(root, rel string, err error) error {
	p := filepath.Join(root, filepath.FromSlash(rel))
	return fmt.Errorf("%w: cannot read %s (%w); podbox refuses to start the agent with a partial mask set. Fix it on the host, e.g. chmod u+rx %q, then retry", ErrMasks, p, err, p)
}

// covered reports rel inside a masked directory.
func covered(rel string, masked map[string]bool) bool {
	for p := path.Dir(rel); p != "." && p != "/"; p = path.Dir(p) {
		if masked[p] {
			return true
		}
	}
	return false
}

// MissingError is the refusal to start while recorded masked paths are
// found nowhere; nil when none are.
func MissingError(name string, missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%v: %d masked path(s) of %s are missing and were not found elsewhere in the workspace:\n", ErrMasks, len(missing), name)
	for _, p := range missing {
		fmt.Fprintf(&sb, "  %s\n", p)
	}
	fmt.Fprintf(&sb, "Restore them on the host, or stop masking them with: podbox policy reload %s --forget-missing", name)
	return &missingError{sb.String()}
}

type missingError struct{ msg string }

func (e *missingError) Error() string { return e.msg }
func (e *missingError) Unwrap() error { return ErrMasks }

// Shrink drops the records of a resolution that no pattern matches any
// more, at their recorded or current path, and, with forgetMissing, the
// missing ones; it is applied only on an explicit policy reload. It returns
// the kept records and a description of each dropped one.
func Shrink(res Result, ignore []byte, forgetMissing bool) (kept []Tracked, dropped []string) {
	masked := Matcher(ignore)
	for _, t := range res.Tracked {
		switch {
		case t.Now == "" && !forgetMissing:
			kept = append(kept, t)
		case t.Now == "":
			dropped = append(dropped, t.Rel+" (missing)")
		case masked(t.Rel, t.Dir) || masked(t.Now, t.Dir):
			kept = append(kept, t)
		case t.Now != t.Rel:
			dropped = append(dropped, t.Rel+" (now "+t.Now+")")
		default:
			dropped = append(dropped, t.Rel)
		}
	}
	return kept, dropped
}
