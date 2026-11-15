package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Crocogiciel-Studio/podbox/internal/fsutil"
	"github.com/Crocogiciel-Studio/podbox/internal/gitguard"
)

// The agent can write the workspace's .git, which git on the host trusts.
// podbox records a fingerprint of it at creation and compares the live
// directory against it after every session (see gitguard.Fingerprint).

func (s Store) gitPrintPath(name string) string {
	return filepath.Join(s.Dir(name), "git.fingerprint.json")
}

// RecordGit (re)records the fingerprint of the workspace's .git.
func (s Store) RecordGit(m Meta) error {
	p, err := gitguard.Fingerprint(m.Workspace)
	if err != nil {
		return fmt.Errorf("fingerprint %s/.git: %w", m.Workspace, err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(s.gitPrintPath(m.Name), append(b, '\n'))
}

// HasGitPrint reports a recorded fingerprint.
func (s Store) HasGitPrint(name string) bool {
	_, err := os.Stat(s.gitPrintPath(name))
	return err == nil
}

// CheckGit compares the workspace's .git with the recorded fingerprint; nil
// when none was recorded.
func (s Store) CheckGit(m Meta) (*gitguard.Integrity, error) {
	b, err := os.ReadFile(s.gitPrintPath(m.Name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var old gitguard.Print
	if err := json.Unmarshal(b, &old); err != nil {
		return nil, fmt.Errorf("git fingerprint of %s: %w", m.Name, err)
	}
	return gitguard.Check(m.Workspace, old)
}
