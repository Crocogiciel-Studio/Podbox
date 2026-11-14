package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafe reports a workspace that would expose the user's home or
// podbox's own files to the agent.
var ErrUnsafe = errors.New("unsafe workspace")

// CheckRoot refuses a workspace that is the home directory or one of its
// parents, or that contains, equals or lies inside a podbox-owned directory
// (config, state, cache): the agent could otherwise rewrite its own policy.
// Paths are compared after resolving symbolic links.
func CheckRoot(ws, home string, owned []string) error {
	w, err := realPath(ws)
	if err != nil {
		return err
	}
	if home != "" {
		h, err := realPath(home)
		if err != nil {
			return err
		}
		if within(h, w) {
			return fmt.Errorf("%w: %s is your home directory or one of its parents; pick a project directory", ErrUnsafe, ws)
		}
	}
	for _, o := range owned {
		r, err := realPath(o)
		if err != nil {
			return err
		}
		if within(w, r) || within(r, w) {
			return fmt.Errorf("%w: %s overlaps the podbox directory %s (config, state or cache), which the agent could rewrite; pick a directory outside it", ErrUnsafe, ws, o)
		}
	}
	return nil
}

// within reports whether p equals dir or lies inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// realPath resolves symbolic links in the longest existing prefix of p.
func realPath(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var rest []string
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{r}, rest...)...), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(append([]string{p}, rest...)...), nil
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}
