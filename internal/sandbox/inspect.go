package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/termsafe"
)

// ErrNotClone reports a command that needs a clone sandbox.
var ErrNotClone = errors.New("not a clone sandbox")

// ErrTooLarge reports output past the cap.
var ErrTooLarge = errors.New("output too large")

// ErrTimeout reports a git command killed for running too long.
var ErrTimeout = errors.New("git timed out")

// DefaultGitTimeout bounds each git command run in a sandbox.
const DefaultGitTimeout = 120 * time.Second

// maxIgnoredList caps what is read of the ignored paths, which only get counted.
const maxIgnoredList = 1 << 20

// maxOutput caps what one command run in a sandbox may return.
const maxOutput = 32 << 20

// maxStderr caps the error text kept from a failed command.
const maxStderr = 64 << 10

var validBase = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// FileChange is one file that differs from the clone's base commit.
type FileChange struct {
	// Path and OldPath are for display: cleaning them is lossy, so never use
	// them to look a file up.
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added, modified, deleted, renamed, typechange
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary"`
	Untracked bool   `json:"untracked"`
	// Gitlink marks a nested repository or submodule (mode 160000): only its
	// commit is compared, not its files.
	Gitlink bool `json:"gitlink"`
}

// DiffResult is what the agent changed in its clone since the base commit.
// Every string in it has been through termsafe.
type DiffResult struct {
	Base  string       `json:"base"`
	Files []FileChange `json:"files"`
	Patch string       `json:"patch,omitempty"`
	// Ignored counts the ignored paths (directories count once) that are not
	// listed in Files; IgnoredCapped says the count stopped at a limit.
	Ignored       int  `json:"ignored"`
	IgnoredCapped bool `json:"ignored_capped,omitempty"`
}

// capWriter buffers up to max bytes. Past it, it fails; when quiet it keeps
// the first max bytes and drops the rest without failing.
type capWriter struct {
	buf   bytes.Buffer
	max   int
	quiet bool
	over  bool // a non-quiet writer was refused
	cut   bool // a quiet writer dropped bytes
}

func (w *capWriter) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if len(p) <= room {
		return w.buf.Write(p)
	}
	if !w.quiet {
		w.over = true
		return 0, ErrTooLarge
	}
	w.buf.Write(p[:room])
	w.cut = true
	return len(p), nil
}

// ExecError is a command that ran in the sandbox and failed.
type ExecError struct {
	Code   int
	Stderr string // cleaned
}

func (e *ExecError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("exit status %d: %s", e.Code, e.Stderr)
}

// cloneMeta loads a sandbox and requires a running clone sandbox.
func (m *Manager) cloneMeta(ctx context.Context, name string) (Meta, error) {
	meta, err := m.Store.Load(name)
	if err != nil {
		return meta, err
	}
	if !meta.IsClone() {
		return meta, fmt.Errorf("%w: %s is a shared sandbox; this command needs one created with --clone", ErrNotClone, name)
	}
	c, err := m.RT.ContainerInspect(ctx, agentContainer(name))
	if errors.Is(err, runtime.ErrNotFound) || err == nil && !c.Running() {
		return meta, fmt.Errorf("%w: %s (run podbox start %s)", ErrNotRunning, name, name)
	}
	return meta, err
}

// runIn runs argv in the agent container of meta at the workspace path and
// returns its stdout, capped at maxOutput. Stdin is closed. A failure is an
// *ExecError with the stderr cleaned.
func (m *Manager) runIn(ctx context.Context, meta Meta, argv []string, env map[string]string) ([]byte, error) {
	out := &capWriter{max: maxOutput}
	if err := m.runInto(ctx, meta, argv, env, out); err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

func (m *Manager) runInto(ctx context.Context, meta Meta, argv []string, env map[string]string, out *capWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errw := &capWriter{max: maxStderr, quiet: true}
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: argv, Workdir: WorkspaceTarget(m.GOOS, meta.Workspace), User: AgentUser, Env: env,
		Stdout: out, Stderr: errw,
	})
	switch {
	case out.over:
		return fmt.Errorf("%w: more than %d MiB", ErrTooLarge, maxOutput>>20)
	case err != nil:
		return err
	case code != 0:
		return &ExecError{Code: code, Stderr: strings.TrimSpace(termsafe.CleanName(errw.buf.String()))}
	}
	return nil
}

// gitArgs is a git command line, bounded by timeout inside the container (so
// a killed podman client leaves nothing running), that ignores the clone's
// configuration for everything that could run a program or change the format.
func gitArgs(project string, limit time.Duration, sub ...string) []string {
	return append([]string{"timeout", "-k", "5", strconv.Itoa(int(limit.Seconds())), "git",
		"-c", "safe.directory=" + project, "-c", "core.pager=cat", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"-c", "core.sparseCheckout=false", "-c", "diff.external=", "-c", "diff.ignoreSubmodules=none",
		"-c", "core.quotepath=off", "-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
	}, sub...)
}

// Diff reports what the agent changed in its clone since the base commit:
// commits, uncommitted changes to tracked files and untracked files, as of the
// moment it runs (the worktree is live; this is not a snapshot of what an
// export would bring). Git runs in the agent container on a temporary index,
// so the agent's index, config and objects are not written. The patch is left
// out unless withPatch.
func (m *Manager) Diff(ctx context.Context, name string, withPatch bool) (DiffResult, error) {
	meta, err := m.cloneMeta(ctx, name)
	if err != nil {
		return DiffResult{}, err
	}
	if !validBase.MatchString(meta.Base) {
		return DiffResult{}, fmt.Errorf("sandbox %s has no valid base commit (%q)", name, meta.Base)
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return DiffResult{}, err
	}
	idx := "/tmp/podbox-diff-" + hex.EncodeToString(rnd[:])
	defer m.runIn(context.WithoutCancel(ctx), meta, []string{"rm", "-f", idx, idx + ".lock"}, nil)
	limit := m.GitTimeout
	if limit <= 0 {
		limit = DefaultGitTimeout
	}
	base := map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_OPTIONAL_LOCKS": "0",
		"GIT_NO_REPLACE_OBJECTS": "1", "GIT_ATTR_NOSYSTEM": "1",
	}
	temp := map[string]string{"GIT_INDEX_FILE": idx}
	for k, v := range base {
		temp[k] = v
	}
	project := WorkspaceTarget(m.GOOS, meta.Workspace)
	fail := func(sub string, err error) error {
		var ee *ExecError
		if errors.As(err, &ee) && (ee.Code == 124 || ee.Code == 137) {
			return fmt.Errorf("%w after %ds; the sandbox may contain a hostile filter or hook", ErrTimeout, int(limit.Seconds()))
		}
		return fmt.Errorf("git %s: %w", sub, err)
	}
	git := func(env map[string]string, sub ...string) ([]byte, error) {
		b, err := m.runIn(ctx, meta, gitArgs(project, limit, sub...), env)
		if err != nil {
			return nil, fail(sub[0], err)
		}
		return b, nil
	}
	diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", "-M"}
	for _, step := range [][]string{{"read-tree", meta.Base}, {"add", "-N", "."}} {
		if _, err := git(temp, step...); err != nil {
			return DiffResult{}, err
		}
	}
	// One call gives the list and the counts, so they cannot disagree.
	raw, err := git(temp, append(diff, "-z", "--raw", "--numstat", meta.Base, "--")...)
	if err != nil {
		return DiffResult{}, err
	}
	others, err := git(base, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return DiffResult{}, err
	}
	files, err := parseChanges(raw, others)
	if err != nil {
		return DiffResult{}, err
	}
	res := DiffResult{Base: meta.Base, Files: files}
	ign := &capWriter{max: maxIgnoredList, quiet: true}
	if err := m.runInto(ctx, meta, gitArgs(project, limit, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"), base, ign); err != nil {
		return DiffResult{}, fail("ls-files", err)
	}
	list := splitNUL(ign.buf.Bytes())
	if ign.cut && len(list) > 0 {
		list = list[:len(list)-1] // the last name may be cut
	}
	res.Ignored, res.IgnoredCapped = len(list), ign.cut
	if withPatch {
		patch, err := git(temp, append(diff, "--binary", meta.Base, "--")...)
		if err != nil {
			return DiffResult{}, err
		}
		res.Patch = string(termsafe.Clean(patch))
	}
	return res, nil
}

var statusNames = map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'T': "typechange", 'U': "unmerged"}

type rawEntry struct {
	status        byte
	oldMode, mode string
	oldPath, path string
}

type numEntry struct {
	added, deleted int
	binary         bool
	path           string
}

// parseChanges reads the -z output of git diff --raw --numstat (the raw
// records, which start with ':', then the numstat records) and of ls-files
// --others, and returns file changes with cleaned names. A list and counts
// that do not match are an error, never a zero.
func parseChanges(diffOut, others []byte) ([]FileChange, error) {
	toks := splitNUL(diffOut)
	var raws []rawEntry
	i := 0
	for i < len(toks) && strings.HasPrefix(toks[i], ":") {
		f := strings.Fields(toks[i])
		if len(f) != 5 {
			return nil, fmt.Errorf("unexpected git diff output %q", toks[i])
		}
		e := rawEntry{status: f[4][0], oldMode: strings.TrimPrefix(f[0], ":"), mode: f[1]}
		n := 1
		if e.status == 'R' || e.status == 'C' { // C needs -C, which is not asked; both carry two names
			n = 2
		}
		if i+n >= len(toks) {
			return nil, errors.New("truncated git diff output")
		}
		e.path = toks[i+n]
		if n == 2 {
			e.oldPath = toks[i+1]
		}
		raws = append(raws, e)
		i += 1 + n
	}
	var nums []numEntry
	for ; i < len(toks); i++ {
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected git numstat output %q", toks[i])
		}
		e := numEntry{path: f[2], binary: f[0] == "-" && f[1] == "-"}
		e.added, _ = strconv.Atoi(f[0])
		e.deleted, _ = strconv.Atoi(f[1])
		if e.path == "" { // rename: the next two names are the old and the new
			if i+2 >= len(toks) {
				return nil, errors.New("truncated git numstat output")
			}
			e.path = toks[i+2]
			i += 2
		}
		nums = append(nums, e)
	}
	if len(nums) != len(raws) {
		return nil, fmt.Errorf("git listed %d files but counted %d", len(raws), len(nums))
	}
	untracked := map[string]bool{}
	for _, p := range splitNUL(others) {
		untracked[p] = true
	}
	files := []FileChange{}
	for k, r := range raws {
		if nums[k].path != r.path {
			return nil, fmt.Errorf("git listed %q but counted %q", r.path, nums[k].path)
		}
		st := statusNames[r.status]
		if st == "" {
			st = "unknown"
		}
		f := FileChange{
			Path: termsafe.CleanName(r.path), Status: st, Added: nums[k].added, Deleted: nums[k].deleted, Binary: nums[k].binary,
			Untracked: untracked[r.path] && st == "added", Gitlink: r.mode == "160000" || r.oldMode == "160000",
		}
		if r.oldPath != "" {
			f.OldPath = termsafe.CleanName(r.oldPath)
		}
		files = append(files, f)
	}
	return files, nil
}

func splitNUL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}
