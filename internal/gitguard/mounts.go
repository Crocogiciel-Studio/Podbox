package gitguard

import (
	"io/fs"
	"os"
	"path"

	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// ConfigRel is the workspace's git configuration, bound read-only.
const ConfigRel = ".git/config"

// HasDir reports a real .git directory (not a symlink).
func HasDir(r *os.Root) bool {
	fi, err := r.Lstat(".git")
	return err == nil && fi.IsDir()
}

// IsPointer reports a .git file (worktree or submodule): its git directory
// lies outside the workspace and is not protected.
func IsPointer(r *os.Root) bool {
	fi, err := r.Lstat(".git")
	return err == nil && fi.Mode().IsRegular()
}

// ReadOnly lists the existing git paths of workspace r to bind read-only in
// the agent: .git/config, which can name hooks, fsmonitor or filter commands
// the host's git would run, and the hooks directories. Objects, refs, index
// and HEAD stay writable. Nothing when .git is not a directory.
func ReadOnly(r *os.Root) []string {
	if !HasDir(r) {
		return nil
	}
	var out []string
	if fi, err := workspace.Lstat(r, ConfigRel); err == nil && fi.Mode().IsRegular() {
		out = append(out, ConfigRel)
	}
	return append(out, hookDirs(r)...)
}

// hookDirs lists the existing hooks directories of .git and of the submodule
// git directories under .git/modules, so hooks run for the agent's commits
// but cannot be changed. Absent ones are not created: one the agent creates
// is reported by the integrity check.
func hookDirs(r *os.Root) []string {
	var out []string
	if fi, err := workspace.Lstat(r, ".git/hooks"); err == nil && fi.IsDir() {
		out = append(out, ".git/hooks")
	}
	mods, err := workspace.OpenDir(r, ".git/modules")
	if err != nil {
		return out
	}
	defer mods.Close()
	_ = fs.WalkDir(mods.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == "." {
			return nil
		}
		if !isGitDir(mods, path.Dir(p)) {
			return nil // not inside a git directory: a path segment of a submodule name
		}
		switch d.Name() { // inside a submodule git directory: only hooks and nested modules matter
		case "hooks":
			out = append(out, ".git/modules/"+p)
			return fs.SkipDir
		case "modules":
			return nil
		}
		return fs.SkipDir
	})
	return out
}
