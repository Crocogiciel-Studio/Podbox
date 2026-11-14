package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// The agent can write the workspace, so podbox never reads the project's
// policy or ignore file live: it snapshots them into the state directory at
// creation and on an explicit `podbox policy reload`.

// ProjectPolicyRel is the project policy file inside the workspace.
const ProjectPolicyRel = ".podbox/policy.yaml"

// projectDirs are the workspace paths mounted read-only in the agent.
var projectDirs = []string{".podbox", workspace.IgnoreFile}

func (s Store) projectDir(name string) string { return filepath.Join(s.Dir(name), "project") }
func (s Store) ProjectPolicyPath(name string) string {
	return filepath.Join(s.projectDir(name), "policy.yaml")
}
func (s Store) ProjectIgnorePath(name string) string {
	return filepath.Join(s.projectDir(name), "sandboxignore")
}

type projectFile struct{ rel, snap string }

func (s Store) projectFiles(name string) []projectFile {
	return []projectFile{{ProjectPolicyRel, s.ProjectPolicyPath(name)}, {workspace.IgnoreFile, s.ProjectIgnorePath(name)}}
}

// readLive reads a workspace file, refusing symlinks anywhere below the
// workspace so the agent cannot point podbox at another host file.
func readLive(ws, rel string) ([]byte, bool, error) {
	p := ws
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("%s is a symbolic link; ignored", rel)
		}
	}
	b, err := os.ReadFile(p)
	return b, err == nil, err
}

func readSnap(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}

// Changes is what a snapshot refresh changed.
type Changes struct {
	RulesAdded    []string `json:"rules_added"`
	RulesRemoved  []string `json:"rules_removed"`
	IgnoreAdded   []string `json:"ignore_added"`
	IgnoreRemoved []string `json:"ignore_removed"`
	// MasksDropped lists the recorded masks policy reload forgot.
	MasksDropped []string `json:"masks_dropped,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// Empty reports no change.
func (c Changes) Empty() bool {
	return len(c.RulesAdded)+len(c.RulesRemoved)+len(c.IgnoreAdded)+len(c.IgnoreRemoved)+len(c.MasksDropped) == 0
}

// Snapshot copies the workspace's project files into the state directory and
// reports the difference with the previous snapshot. An unreadable file is
// snapshotted as absent, with a warning.
func (s Store) Snapshot(m Meta) (Changes, error) {
	var c Changes
	for _, f := range s.projectFiles(m.Name) {
		live, ok, err := readLive(m.Workspace, f.rel)
		if err != nil {
			c.Warnings = append(c.Warnings, err.Error())
			ok = false
		}
		old, _, err := readSnap(f.snap)
		if err != nil {
			return c, err
		}
		if f.rel == ProjectPolicyRel {
			c.RulesAdded, c.RulesRemoved = diff(ruleKeys(old), ruleKeys(live))
		} else {
			c.IgnoreAdded, c.IgnoreRemoved = diff(ignoreLines(old), ignoreLines(live))
		}
		if !ok {
			if err := os.Remove(f.snap); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return c, err
			}
			continue
		}
		if err := policy.WriteAtomic(f.snap, live); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Drift lists the project files whose workspace copy differs from the
// snapshot in use.
func (s Store) Drift(m Meta) []string {
	var out []string
	for _, f := range s.projectFiles(m.Name) {
		live, lok, lerr := readLive(m.Workspace, f.rel)
		snap, sok, serr := readSnap(f.snap)
		if lerr != nil || serr != nil || lok != sok || !bytes.Equal(live, snap) {
			out = append(out, f.rel)
		}
	}
	return out
}

// DriftWarning is the advice printed when Drift is not empty.
func DriftWarning(name string, files []string) string {
	return fmt.Sprintf("%s: %s changed in the workspace since podbox last read it (by you or the agent); review it, then run podbox policy reload %s",
		name, strings.Join(files, " and "), name)
}

// ruleKeys renders a policy file's rules; an invalid file has none (fail closed).
func ruleKeys(b []byte) []string {
	f, _, err := policy.Parse(b)
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range f.Rules {
		k := "deny " + policy.NormalizeHost(r.Host)
		if *r.Allow {
			k = "allow " + policy.NormalizeHost(r.Host)
		}
		if len(r.Ports) > 0 {
			k += " ports " + policy.JoinPorts(r.Ports)
		}
		if r.AllowPrivate {
			k += " (allow_private)"
		}
		out = append(out, k)
	}
	return out
}

func ignoreLines(b []byte) []string {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// diff returns the sorted entries only in b (added) and only in a (removed).
func diff(a, b []string) (added, removed []string) {
	added, removed = []string{}, []string{}
	in := func(xs []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	for x := range mb {
		if !ma[x] {
			added = append(added, x)
		}
	}
	for x := range ma {
		if !mb[x] {
			removed = append(removed, x)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// readOnlyProject lists the existing project paths to mount read-only in the
// agent; symlinks are skipped so nothing outside the workspace is exposed.
func readOnlyProject(ws string) []string {
	var out []string
	for _, rel := range projectDirs {
		if fi, err := os.Lstat(filepath.Join(ws, rel)); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
			out = append(out, rel)
		}
	}
	// .git/config can name hooks, fsmonitor or filter commands that the
	// host's git would run; objects, refs, index and HEAD stay writable.
	if gitDir(ws) {
		if fi, err := os.Lstat(filepath.Join(ws, gitConfig)); err == nil && fi.Mode().IsRegular() {
			out = append(out, gitConfig)
		}
		out = append(out, hookDirs(ws)...)
	}
	return out
}

// hookDirs lists the existing hooks directories of .git and of the submodule
// git directories under .git/modules, so hooks run for the agent's commits
// but cannot be changed. Absent ones are not created: one the agent creates
// is reported by the integrity check.
func hookDirs(ws string) []string {
	var out []string
	isDir := func(p string) bool { fi, err := os.Lstat(p); return err == nil && fi.IsDir() }
	if isDir(filepath.Join(ws, ".git", "hooks")) {
		out = append(out, ".git/hooks")
	}
	_ = filepath.WalkDir(filepath.Join(ws, ".git", "modules"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if fi, err := os.Lstat(filepath.Join(filepath.Dir(p), "HEAD")); err != nil || !fi.Mode().IsRegular() {
			return nil // not inside a git directory: a path segment of a submodule name
		}
		switch d.Name() { // inside a submodule git directory: only hooks and nested modules matter
		case "hooks":
			out = append(out, filepath.ToSlash(mustRel(ws, p)))
			return fs.SkipDir
		case "modules":
			return nil
		}
		return fs.SkipDir
	})
	return out
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

const gitConfig = ".git/config"

// gitDir reports a real .git directory (not a symlink).
func gitDir(ws string) bool {
	fi, err := os.Lstat(filepath.Join(ws, ".git"))
	return err == nil && fi.IsDir()
}

// gitPointer reports a .git file (worktree or submodule): its git directory
// lies outside the workspace and is not protected.
func gitPointer(ws string) bool {
	fi, err := os.Lstat(filepath.Join(ws, ".git"))
	return err == nil && fi.Mode().IsRegular()
}
