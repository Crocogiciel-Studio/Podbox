// Package workspace decides which project paths are hidden from the agent.
package workspace

import (
	"fmt"
	"io/fs"
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

// Result lists masks and warnings for uncovered secret-looking files.
type Result struct {
	Masks    []Mask
	Warnings []string
}

// Resolve walks root once and returns what to mask. ignore is the
// .sandboxignore content to apply (the host-side snapshot, never the live
// workspace file).
func Resolve(root string, ignore []byte) (Result, error) {
	var res Result
	forced := gitignore.CompileIgnoreLines(Forced...)
	user := gitignore.CompileIgnoreLines(strings.Split(string(ignore), "\n")...)
	masked := func(rel string) bool { return forced.MatchesPath(rel) || user.MatchesPath(rel) }

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable entries are invisible to the agent too
		}
		if p == root {
			return nil
		}
		rel := filepath.ToSlash(mustRel(root, p))
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // a link carries no content; its target is masked or absent
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir // protected by read-only binds and the integrity check
			}
			if masked(rel + "/") {
				res.Masks = append(res.Masks, Mask{Rel: rel, Dir: true})
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if masked(rel) {
			res.Masks = append(res.Masks, Mask{Rel: rel})
			return nil
		}
		if why := looksSecret(p, d); why != "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s and is not masked; add it to %s and run podbox policy reload to hide it", rel, why, IgnoreFile))
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if len(res.Masks) > MaxMasks {
		return res, fmt.Errorf("%d paths to mask exceed the limit of %d: mask parent directories in %s", len(res.Masks), MaxMasks, IgnoreFile)
	}
	sort.Slice(res.Masks, func(i, j int) bool { return res.Masks[i].Rel < res.Masks[j].Rel })
	return res, nil
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}
