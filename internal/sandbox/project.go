package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
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

// maxProjectFile caps the project files podbox reads.
const maxProjectFile = 1 << 20

// readLive reads a workspace file through an os.Root, refusing symbolic
// links anywhere below the workspace so the agent cannot point podbox at
// another host file.
func readLive(ws, rel string) ([]byte, bool, error) {
	r, err := os.OpenRoot(ws)
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	b, err := workspace.ReadFile(r, rel, maxProjectFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s ignored: %w", rel, err)
	}
	return b, true, nil
}

// SetProjectRule records a rule in the workspace's .podbox/policy.yaml
// through an os.Root: .podbox and policy.yaml must be a plain directory and
// a regular file (or absent), never a symbolic link, and the file is
// replaced atomically inside the workspace. It edits only that file.
func SetProjectRule(ws, host string, allow bool, ports []int) error {
	r, err := os.OpenRoot(ws)
	if err != nil {
		return err
	}
	defer r.Close()
	b, err := workspace.ReadFile(r, ProjectPolicyRel, maxProjectFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("refusing to write %s: %w", ProjectPolicyRel, err)
	}
	out, err := policy.EditRule(b, host, allow, ports)
	if err != nil {
		return fmt.Errorf("%s: %w", ProjectPolicyRel, err)
	}
	if err := workspace.WriteFile(r, ProjectPolicyRel, out); err != nil {
		return fmt.Errorf("refusing to write %s: %w", ProjectPolicyRel, err)
	}
	return nil
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
	// MasksMissing lists the recorded masks found nowhere, kept without
	// --forget-missing; start refuses until they are restored or forgotten.
	MasksMissing []string `json:"masks_missing,omitempty"`
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
	c, live, err := s.readProject(m)
	if err != nil {
		return c, err
	}
	return c, s.writeProject(live)
}

// liveFile is a project file as read from the workspace; ok is false when
// absent or unreadable.
type liveFile struct {
	projectFile
	b  []byte
	ok bool
}

// readProject reads the project files and diffs them with the snapshot,
// writing nothing.
func (s Store) readProject(m Meta) (Changes, []liveFile, error) {
	var c Changes
	var out []liveFile
	for _, f := range s.projectFiles(m.Name) {
		live, ok, err := readLive(m.Workspace, f.rel)
		if err != nil {
			c.Warnings = append(c.Warnings, err.Error())
			ok = false
		}
		old, _, err := readSnap(f.snap)
		if err != nil {
			return c, nil, err
		}
		if f.rel == ProjectPolicyRel {
			c.RulesAdded, c.RulesRemoved = diff(ruleKeys(old), ruleKeys(live))
		} else {
			c.IgnoreAdded, c.IgnoreRemoved = diff(ignoreLines(old), ignoreLines(live))
		}
		out = append(out, liveFile{f, live, ok})
	}
	return c, out, nil
}

// writeProject replaces the snapshot with what readProject read.
func (s Store) writeProject(files []liveFile) error {
	for _, f := range files {
		if !f.ok {
			if err := os.Remove(f.snap); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := policy.WriteAtomic(f.snap, f.b); err != nil {
			return err
		}
	}
	return nil
}

// Reload is podbox policy reload, all or nothing: it reads the project
// files, resolves the masks with the new .sandboxignore and compiles the
// policy with the new project rules, and writes the snapshot, the mask
// record and the effective policy only when all of that succeeded, so a
// refused reload (e.g. an unreadable workspace directory) changes nothing.
func (s Store) Reload(m Meta, forgetMissing bool) (Changes, policy.Effective, error) {
	c, live, err := s.readProject(m)
	if err != nil {
		return c, policy.Effective{}, err
	}
	var ignore, rules []byte
	var hasRules bool
	for _, f := range live {
		switch {
		case f.rel == workspace.IgnoreFile && f.ok:
			ignore = f.b
		case f.rel == ProjectPolicyRel:
			rules, hasRules = f.b, f.ok
		}
	}
	ts, err := s.loadMasks(m.Name)
	if err != nil {
		return c, policy.Effective{}, err
	}
	res, err := workspace.Resolve(m.Workspace, ignore, ts)
	if err != nil {
		return c, policy.Effective{}, err
	}
	kept, dropped := workspace.Shrink(res, ignore, forgetMissing)
	c.MasksDropped = dropped
	if !forgetMissing {
		c.MasksMissing = res.Missing
	}
	e, err := s.compileWith(m, rules, hasRules)
	if err != nil {
		return c, e, err
	}
	if err := s.writeProject(live); err != nil {
		return c, e, err
	}
	if err := s.saveMasks(m.Name, kept); err != nil {
		return c, e, err
	}
	return c, e, policy.WriteEffective(s.EffectivePath(m.Name), e)
}

// compileWith compiles the policy with the given project rules in place of
// the snapshot, through a temporary file removed afterwards.
func (s Store) compileWith(m Meta, rules []byte, ok bool) (policy.Effective, error) {
	src := s.Sources(m)
	src.Project = ""
	if ok {
		f, err := os.CreateTemp(s.Dir(m.Name), ".reload-policy-*.yaml")
		if err != nil {
			return policy.Effective{}, err
		}
		defer os.Remove(f.Name())
		_, werr := f.Write(rules)
		if err := errors.Join(werr, f.Close()); err != nil {
			return policy.Effective{}, err
		}
		src.Project = f.Name()
	}
	e := policy.Compile(src)
	for i, w := range e.Warnings { // name the snapshot the policy is read from
		if src.Project != "" {
			e.Warnings[i] = strings.ReplaceAll(w, src.Project, s.ProjectPolicyPath(m.Name))
		}
	}
	return e, nil
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
func readOnlyProject(r *os.Root) []string {
	var out []string
	for _, rel := range projectDirs {
		if fi, err := workspace.Lstat(r, rel); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
			out = append(out, rel)
		}
	}
	// .git/config can name hooks, fsmonitor or filter commands that the
	// host's git would run; objects, refs, index and HEAD stay writable.
	if gitDir(r) {
		if fi, err := workspace.Lstat(r, gitConfig); err == nil && fi.Mode().IsRegular() {
			out = append(out, gitConfig)
		}
		out = append(out, hookDirs(r)...)
	}
	return out
}

// hookDirs lists the existing hooks directories of .git and of the submodule
// git directories under .git/modules, so hooks run for the agent's commits
// but cannot be changed. Absent ones are not created: one the agent creates
// is reported by the integrity check.
func hookDirs(r *os.Root) []string {
	var out []string
	isDir := func(rel string) bool { fi, err := workspace.Lstat(r, rel); return err == nil && fi.IsDir() }
	if isDir(".git/hooks") {
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
		if fi, err := workspace.Lstat(mods, path.Join(path.Dir(p), "HEAD")); err != nil || !fi.Mode().IsRegular() {
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

const gitConfig = ".git/config"

// gitDir reports a real .git directory (not a symlink).
func gitDir(r *os.Root) bool {
	fi, err := r.Lstat(".git")
	return err == nil && fi.IsDir()
}

// gitPointer reports a .git file (worktree or submodule): its git directory
// lies outside the workspace and is not protected.
func gitPointer(r *os.Root) bool {
	fi, err := r.Lstat(".git")
	return err == nil && fi.Mode().IsRegular()
}

// checkMounts re-validates, right before the container is created, every
// bind source inside the workspace: each component below the workspace must
// be a plain directory, and the source a directory or regular file, never a
// symbolic link (podman would follow it on the host).
func checkMounts(r *os.Root, ws string, ms []runtime.Mount) error {
	for _, mt := range ms {
		if mt.Type != runtime.Bind {
			continue
		}
		rel, err := filepath.Rel(ws, mt.Source)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue // outside the workspace: podbox's own state, /dev/null
		}
		if err := workspace.Check(r, filepath.ToSlash(rel)); err != nil {
			return fmt.Errorf("refusing to mount %s: %w", mt.Source, err)
		}
	}
	return nil
}
