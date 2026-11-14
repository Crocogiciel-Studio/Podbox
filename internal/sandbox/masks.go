package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// Masks are recorded with their file identity in the state directory so
// they follow a file whose parent directory the agent renamed (see
// workspace.Tracked). The record grows at every start and shrinks only on
// podbox policy reload.

func (s Store) masksPath(name string) string { return filepath.Join(s.Dir(name), "masks.json") }

func (s Store) loadMasks(name string) ([]workspace.Tracked, error) {
	b, err := os.ReadFile(s.masksPath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ts []workspace.Tracked
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("mask record of %s: %w", name, err)
	}
	return ts, nil
}

func (s Store) saveMasks(name string, ts []workspace.Tracked) error {
	if ts == nil {
		ts = []workspace.Tracked{}
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return policy.WriteAtomic(s.masksPath(name), append(b, '\n'))
}

// ResolveMasks computes the masks of a sandbox from the .sandboxignore
// snapshot and the record, and saves the grown record.
func (s Store) ResolveMasks(m Meta) (workspace.Result, error) {
	ignore, _, err := readSnap(s.ProjectIgnorePath(m.Name))
	if err != nil {
		return workspace.Result{}, err
	}
	ts, err := s.loadMasks(m.Name)
	if err != nil {
		return workspace.Result{}, err
	}
	res, err := workspace.Resolve(m.Workspace, ignore, ts)
	if err != nil {
		return res, err
	}
	return res, s.saveMasks(m.Name, res.Tracked)
}

// ReloadMasks re-records the masks after a snapshot refresh, dropping the
// records no pattern matches any more and the missing ones; it returns the
// dropped paths.
func (s Store) ReloadMasks(m Meta) ([]string, error) {
	ignore, _, err := readSnap(s.ProjectIgnorePath(m.Name))
	if err != nil {
		return nil, err
	}
	ts, err := s.loadMasks(m.Name)
	if err != nil {
		return nil, err
	}
	res, err := workspace.Resolve(m.Workspace, ignore, ts)
	if err != nil {
		return nil, err
	}
	kept, dropped := workspace.Shrink(res, ignore)
	return dropped, s.saveMasks(m.Name, kept)
}

// MaskWarning is the loud advice printed when recorded masked paths moved
// or vanished; "" when none did.
func MaskWarning(m Meta, res workspace.Result) string {
	if len(res.Moved)+len(res.Missing) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: MASKED PATHS MOVED OR MISSING in %s (a parent directory was renamed or the file removed, by you or the agent)\n", m.Name, m.Workspace)
	for _, mv := range res.Moved {
		fmt.Fprintf(&sb, "  %s -> %s (still masked)\n", mv.From, mv.To)
	}
	for _, p := range res.Missing {
		fmt.Fprintf(&sb, "  %s -> missing\n", p)
	}
	fmt.Fprintf(&sb, "Review the workspace; podbox keeps masking them. To forget a record, fix %s and run podbox policy reload %s", workspace.IgnoreFile, m.Name)
	return sb.String()
}
