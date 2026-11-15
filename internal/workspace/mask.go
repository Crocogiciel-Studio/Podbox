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
// symbolic links, and returns what to mask. It works in two passes so the
// walk order never matters: the walk collects every entry, then a path is
// masked when a pattern matches it, it is a recorded path, or its identity
// (device, inode) is in the masked set: the identities of every recorded
// file, of every file a pattern or a recorded path designates, and of every
// file inside a masked directory. Every hard link of a masked file is thus
// masked, and recorded. ignore is the .sandboxignore content to apply (the
// host-side snapshot, never the live workspace file).
// Any walk error outside a masked directory fails the resolution (wrapping
// ErrMasks): a directory podbox cannot read may hold secrets the agent could
// read after a chmod.
func Resolve(root string, ignore []byte, tracked []Tracked) (Result, error) {
	res := Result{Tracked: append([]Tracked(nil), tracked...)}
	r, err := os.OpenRoot(root)
	if err != nil {
		return res, err
	}
	defer r.Close()
	w, err := scan(r, root, Matcher(ignore), res.Tracked)
	if err != nil {
		return res, err
	}
	w.place(&res)
	w.mask(&res)
	if err := w.verify(res); err != nil {
		return res, err
	}
	if len(res.Masks) > MaxMasks {
		return res, fmt.Errorf("%d paths to mask exceed the limit of %d: mask parent directories in %s", len(res.Masks), MaxMasks, IgnoreFile)
	}
	sort.Slice(res.Masks, func(i, j int) bool { return res.Masks[i].Rel < res.Masks[j].Rel })
	return res, nil
}

// entry is a workspace path seen by the walk.
type entry struct {
	rel    string
	d      fs.DirEntry
	dir    bool
	id     fileKey
	hasID  bool
	hidden bool // inside a masked directory: the agent cannot see it
}

type walked struct {
	r       *os.Root
	masked  func(rel string, dir bool) bool
	entries []*entry // walk order
	byRel   map[string]*entry
	byID    map[fileKey][]*entry
	links   map[string]bool  // visible symbolic links, never mounted over
	hidden  map[string]bool  // masked directories and the directories below them
	recRel  map[string]bool  // recorded paths
	maskID  map[fileKey]bool // identities masked wherever they are
}

// scan is the first pass: it walks the workspace, decides the masked
// directories (from patterns and records only) and collects the masked set.
func scan(r *os.Root, root string, masked func(string, bool) bool, tracked []Tracked) (*walked, error) {
	w := &walked{r: r, masked: masked, byRel: map[string]*entry{}, byID: map[fileKey][]*entry{},
		links: map[string]bool{}, hidden: map[string]bool{}, recRel: map[string]bool{}, maskID: map[fileKey]bool{}}
	for _, t := range tracked {
		w.recRel[t.Rel] = true
		if t.Ino != 0 {
			w.maskID[fileKey{t.Dev, t.Ino}] = true
		}
	}
	err := fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		hidden := rel != "." && w.hidden[path.Dir(rel)]
		if err != nil {
			if hidden || w.hidden[rel] {
				return nil // inside a masked directory: best effort
			}
			return walkError(root, rel, err)
		}
		if rel == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if !hidden {
				w.links[rel] = true // a link carries no content; its target is masked or absent
			}
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir // protected by read-only binds and the integrity check
		}
		fi, err := d.Info()
		if err != nil {
			if hidden {
				return nil
			}
			return walkError(root, rel, err)
		}
		e := &entry{rel: rel, d: d, dir: d.IsDir(), hidden: hidden}
		dev, ino, ok := fileID(fi)
		e.id, e.hasID = fileKey{dev, ino}, ok
		w.entries = append(w.entries, e)
		w.byRel[rel] = e
		if ok {
			w.byID[e.id] = append(w.byID[e.id], e)
		}
		if e.dir && (hidden || masked(rel, true) || w.recRel[rel] || ok && w.maskID[e.id]) {
			w.hidden[rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, e := range w.entries {
		if !e.dir && e.hasID && (e.hidden || masked(e.rel, false) || w.recRel[e.rel]) {
			w.maskID[e.id] = true
		}
	}
	return w, nil
}

// masks reports whether a visible entry is masked. A FIFO, socket or device
// is masked like a file.
func (w *walked) masks(e *entry) bool {
	if e.dir {
		return w.hidden[e.rel]
	}
	return w.masked(e.rel, false) || w.recRel[e.rel] || e.hasID && w.maskID[e.id]
}

// place finds each record: at its recorded path with its identity, else by
// identity elsewhere, else at its recorded path with a new identity
// (replaced on the host, e.g. an editor's save, or by another file type),
// else inside a masked directory; otherwise it is missing.
func (w *walked) place(res *Result) {
	for i := range res.Tracked {
		t := &res.Tracked[i]
		t.Now = ""
		key := fileKey{t.Dev, t.Ino}
		at := w.byRel[t.Rel]
		moved := w.pick(*t, key)
		switch {
		case at != nil && t.Ino != 0 && at.hasID && at.id == key:
			t.Now = t.Rel
		case moved != nil:
			t.Now = moved.rel
		case at != nil:
			t.Now, t.Dir, t.Dev, t.Ino = t.Rel, at.dir, 0, 0
			if at.hasID {
				t.Dev, t.Ino = at.id.dev, at.id.ino
			}
		case covered(t.Rel, w.hidden):
			t.Now = t.Rel // inside a masked directory
		}
		switch {
		case t.Now == "" && w.links[t.Rel]:
			res.Missing = append(res.Missing, t.Rel+" (now a symbolic link, which podbox never mounts over)")
		case t.Now == "":
			res.Missing = append(res.Missing, t.Rel)
		case t.Now != t.Rel:
			res.Moved = append(res.Moved, Move{From: t.Rel, To: t.Now})
		}
	}
}

// pick chooses where a record with a known identity lives among the paths
// with that identity: the one with its base name (a renamed parent), else
// the first; nil when none.
func (w *walked) pick(t Tracked, key fileKey) *entry {
	if t.Ino == 0 {
		return nil
	}
	var first *entry
	for _, e := range w.byID[key] {
		if e.dir != t.Dir {
			continue
		}
		if path.Base(e.rel) == path.Base(t.Rel) {
			return e
		}
		if first == nil {
			first = e
		}
	}
	return first
}

// mask is the second pass: it masks every visible entry the masked set
// designates, records the new ones and warns about likely secrets left.
func (w *walked) mask(res *Result) {
	now := map[string]bool{}
	for _, t := range res.Tracked {
		now[t.Now] = true
	}
	for _, e := range w.entries {
		switch {
		case e.hidden:
		case w.masks(e):
			res.Masks = append(res.Masks, Mask{Rel: e.rel, Dir: e.dir})
			if !w.recRel[e.rel] && !now[e.rel] {
				t := Tracked{Rel: e.rel, Dir: e.dir, Now: e.rel}
				if e.hasID {
					t.Dev, t.Ino = e.id.dev, e.id.ino
				}
				res.Tracked = append(res.Tracked, t)
			}
		case !e.dir && e.d.Type().IsRegular():
			if why := looksSecret(w.r, e.rel, e.d); why != "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s and is not masked; add it to %s and run podbox policy reload to hide it", e.rel, why, IgnoreFile))
			}
		}
	}
}

// verify re-checks the result against the masked set, so an internal
// inconsistency refuses the start (ErrMasks, exit 7) instead of leaking.
func (w *walked) verify(res Result) error {
	got := map[string]bool{}
	for _, m := range res.Masks {
		got[m.Rel] = true
	}
	for _, e := range w.entries {
		if !e.hidden && !e.dir && e.hasID && w.maskID[e.id] && !got[e.rel] {
			return fmt.Errorf("%w: internal error: %s shares a masked file's identity and is not masked", ErrMasks, e.rel)
		}
	}
	for _, t := range res.Tracked {
		if t.Now != "" && !got[t.Now] && !covered(t.Now, w.hidden) {
			return fmt.Errorf("%w: internal error: recorded %s found at %s is not masked", ErrMasks, t.Rel, t.Now)
		}
	}
	return nil
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
	fmt.Fprintf(&sb, "%v: %d masked path(s) of %s are missing, or now symbolic links, and were not found elsewhere in the workspace:\n", ErrMasks, len(missing), name)
	for _, p := range missing {
		fmt.Fprintf(&sb, "  %s\n", p)
	}
	fmt.Fprintf(&sb, "Restore them on the host, or stop masking them with: podbox policy reload %s --forget-missing", name)
	return &missingError{sb.String()}
}

type missingError struct{ msg string }

func (e *missingError) Error() string { return e.msg }
func (e *missingError) Unwrap() error { return ErrMasks }

// Shrink drops, on an explicit policy reload only, the records still at
// their recorded path that no pattern matches any more and, with
// forgetMissing, the missing ones. A record found at another path (a
// renamed parent) is never dropped: it stays masked where it is now. Nor is
// a record the next resolution of root masks anyway, such as a hard link of
// a kept record or of a file inside a masked directory: dropped lists only
// what stops being masked. It returns the kept records and a description of
// each dropped one.
func Shrink(root string, res Result, ignore []byte, forgetMissing bool) (kept []Tracked, dropped []string, err error) {
	masked := Matcher(ignore)
	var gone []Tracked
	for _, t := range res.Tracked {
		switch {
		case t.Now == "" && !forgetMissing, t.Now != "" && (t.Now != t.Rel || masked(t.Rel, t.Dir)):
			kept = append(kept, t)
		default:
			gone = append(gone, t)
		}
	}
	if len(gone) == 0 {
		return kept, nil, nil
	}
	// The masks without the dropped records; it re-records what they still cover.
	again, err := Resolve(root, ignore, kept)
	if err != nil {
		return nil, nil, err
	}
	still, dirs := map[string]bool{}, map[string]bool{}
	for _, m := range again.Masks {
		still[m.Rel] = true
		dirs[m.Rel] = m.Dir
	}
	for _, t := range gone {
		switch {
		case t.Now == "":
			dropped = append(dropped, t.Rel+" (missing)")
		case !still[t.Now] && !covered(t.Now, dirs):
			dropped = append(dropped, t.Rel)
		}
	}
	return again.Tracked, dropped, nil
}
