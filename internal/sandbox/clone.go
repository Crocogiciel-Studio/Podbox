package sandbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
)

// ErrCloneExists reports a clone directory still on disk for a name.
var ErrCloneExists = errors.New("a clone already exists")

// ErrModeMismatch reports a sandbox whose mode differs from the one asked.
var ErrModeMismatch = errors.New("sandbox mode mismatch")

// CreateOptions are the choices of Create beyond the agent and the project.
type CreateOptions struct {
	// Clone makes the workspace the agent's own clone of the project's
	// committed HEAD instead of the project directory.
	Clone bool
}

// CloneReport tells the human what a clone holds and what it leaves out.
type CloneReport struct {
	Path        string `json:"path"`
	Base        string `json:"base"`
	Branch      string `json:"branch,omitempty"`
	Uncommitted int    `json:"uncommitted"`
	Untracked   int    `json:"untracked"`
}

// Removed is what Remove left on the host.
type Removed struct {
	ClonePath string `json:"clone_path,omitempty"`
	CloneKept bool   `json:"clone_kept,omitempty"`
}

func (s Store) cloneExists(name string) error {
	if s.HasClone(name) {
		return fmt.Errorf("%w for %s at %s (it holds the agent's work); to delete it run: podbox rm %s --volumes", ErrCloneExists, name, s.CloneDir(name), name)
	}
	return nil
}

// checkClone runs what a clone needs before anything is made: a clone
// directory of its own, a usable git and a repository with a commit whose
// top level is ws.
func (m *Manager) checkClone(ctx context.Context, ws string) (hostgit.Repo, error) {
	if m.Store.ClonesRoot == "" {
		return hostgit.Repo{}, errors.New("no clones directory configured")
	}
	if _, _, err := hostgit.Available(); err != nil {
		return hostgit.Repo{}, err
	}
	return hostgit.Inspect(ctx, ws)
}

// makeClone clones the committed HEAD that checkClone saw.
func (m *Manager) makeClone(ctx context.Context, meta Meta, repo hostgit.Repo) (*CloneReport, error) {
	dest := m.Store.CloneDir(meta.Name)
	if err := hostgit.Clone(ctx, meta.Workspace, dest, repo.Head, repo.Branch); err != nil {
		return nil, fmt.Errorf("clone %s: %w", meta.Workspace, err)
	}
	return &CloneReport{Path: dest, Base: repo.Head, Branch: repo.Branch, Uncommitted: repo.Uncommitted, Untracked: repo.Untracked}, nil
}
