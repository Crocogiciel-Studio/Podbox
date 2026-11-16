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

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/termsafe"
)

// ErrNotClone reports a command that needs a clone sandbox.
var ErrNotClone = errors.New("not a clone sandbox")

// ErrTooLarge reports output past the cap.
var ErrTooLarge = errors.New("output too large")

// maxOutput caps what one command run in a sandbox may return.
const maxOutput = 32 << 20

// maxStderr caps the error text kept from a failed command.
const maxStderr = 64 << 10

var validBase = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// FileChange is one file that differs from the clone's base commit.
type FileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added, modified, deleted, renamed, typechange
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary"`
	Untracked bool   `json:"untracked"`
}

// DiffResult is what the agent changed in its clone since the base commit.
// Every string in it has been through termsafe.
type DiffResult struct {
	Base  string       `json:"base"`
	Files []FileChange `json:"files"`
	Patch string       `json:"patch,omitempty"`
}

// capWriter buffers up to max bytes; past it, it fails (or, when quiet,
// drops the rest).
type capWriter struct {
	buf   bytes.Buffer
	max   int
	quiet bool
	over  bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		if w.quiet {
			return len(p), nil
		}
		w.over = true
		return 0, ErrTooLarge
	}
	return w.buf.Write(p)
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
// returns its stdout, capped. Stdin is closed; the stderr of a failure is
// returned cleaned.
func (m *Manager) runIn(ctx context.Context, meta Meta, argv []string, env map[string]string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out, errw := &capWriter{max: maxOutput}, &capWriter{max: maxStderr, quiet: true}
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: argv, Workdir: WorkspaceTarget(m.GOOS, meta.Workspace), User: AgentUser, Env: env,
		Stdout: out, Stderr: errw,
	})
	switch {
	case out.over:
		return nil, fmt.Errorf("%w: more than %d MiB", ErrTooLarge, maxOutput>>20)
	case err != nil:
		return nil, err
	case code != 0:
		return nil, fmt.Errorf("%s exited %d: %s", argv[0], code, strings.TrimSpace(termsafe.CleanName(errw.buf.String())))
	}
	return out.buf.Bytes(), nil
}

// gitArgs is a git command line that ignores the clone's configuration for
// everything that could run a program or change the output format.
func gitArgs(project string, sub ...string) []string {
	return append([]string{"git",
		"-c", "safe.directory=" + project, "-c", "core.pager=cat", "-c", "core.fsmonitor=false",
		"-c", "diff.external=", "-c", "core.quotepath=off", "-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
	}, sub...)
}

// Diff reports what the agent changed in its clone since the base commit:
// commits, uncommitted changes to tracked files and untracked files. Git runs
// in the agent container on a temporary index, so the agent's index, config
// and objects are not touched. The patch is left out unless withPatch.
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
	base := map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_OPTIONAL_LOCKS": "0"}
	temp := map[string]string{"GIT_INDEX_FILE": idx}
	for k, v := range base {
		temp[k] = v
	}
	project := WorkspaceTarget(m.GOOS, meta.Workspace)
	git := func(env map[string]string, sub ...string) ([]byte, error) {
		b, err := m.runIn(ctx, meta, gitArgs(project, sub...), env)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", sub[0], err)
		}
		return b, nil
	}
	diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "-M"}
	for _, step := range [][]string{{"read-tree", meta.Base}, {"add", "-N", "."}} {
		if _, err := git(temp, step...); err != nil {
			return DiffResult{}, err
		}
	}
	status, err := git(temp, append(diff, "-z", "--name-status", meta.Base)...)
	if err != nil {
		return DiffResult{}, err
	}
	numstat, err := git(temp, append(diff, "-z", "--numstat", meta.Base)...)
	if err != nil {
		return DiffResult{}, err
	}
	others, err := git(base, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return DiffResult{}, err
	}
	res := DiffResult{Base: meta.Base, Files: parseChanges(status, numstat, others)}
	if withPatch {
		patch, err := git(temp, append(diff, "--binary", meta.Base)...)
		if err != nil {
			return DiffResult{}, err
		}
		res.Patch = string(termsafe.Clean(patch))
	}
	return res, nil
}

var statusNames = map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "typechange"}

type counts struct {
	added, deleted int
	binary         bool
}

// parseChanges joins the -z outputs of git diff --name-status and --numstat
// and of ls-files --others into file changes; every name is cleaned.
func parseChanges(nameStatus, numStat, others []byte) []FileChange {
	nums := map[string]counts{}
	toks := splitNUL(numStat)
	for i := 0; i < len(toks); i++ {
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			continue
		}
		path := f[2]
		if path == "" { // rename: the next two tokens are the old and new names
			if i+2 >= len(toks) {
				break
			}
			path = toks[i+2]
			i += 2
		}
		c := counts{binary: f[0] == "-" && f[1] == "-"}
		c.added, _ = strconv.Atoi(f[0])
		c.deleted, _ = strconv.Atoi(f[1])
		nums[path] = c
	}
	untracked := map[string]bool{}
	for _, p := range splitNUL(others) {
		untracked[p] = true
	}
	files := []FileChange{}
	toks = splitNUL(nameStatus)
	for i := 0; i < len(toks); i++ {
		code := toks[i]
		if code == "" || i+1 >= len(toks) {
			break
		}
		f := FileChange{Status: statusNames[code[0]]}
		if f.Status == "" {
			f.Status = strings.ToLower(code[:1])
		}
		path := toks[i+1]
		i++
		if code[0] == 'R' || code[0] == 'C' {
			if i+1 >= len(toks) {
				break
			}
			f.OldPath = termsafe.CleanName(path)
			path = toks[i+1]
			i++
		}
		c := nums[path]
		f.Path = termsafe.CleanName(path)
		f.Added, f.Deleted, f.Binary = c.added, c.deleted, c.binary
		f.Untracked = untracked[path] && f.Status == "added"
		files = append(files, f)
	}
	return files
}

func splitNUL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}
