package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// maxOutput caps what one command run in a sandbox may return.
const maxOutput = 32 << 20

// maxStderr caps the error text kept from a failed command.
const maxStderr = 64 << 10

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
	return meta, m.requireRunning(ctx, agentContainer(name), name)
}

// runIn runs argv in the agent container of meta at the workspace path and
// returns its stdout, capped at maxOutput. Stdin is closed. A failure is an
// *ExecError with the stderr cleaned.
func (m *Manager) runIn(ctx context.Context, meta Meta, argv []string, env map[string]string) ([]byte, error) {
	out := &capWriter{max: maxOutput}
	if err := m.runInto(ctx, meta, argv, env, nil, out); err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// limitedWriter is stdout of a command with a cap: exceeded says it refused bytes.
type limitedWriter interface {
	io.Writer
	exceeded() bool
}

func (w *capWriter) exceeded() bool { return w.over }

// runInto is runIn with a stdin (nil for none) and the caller's writer.
func (m *Manager) runInto(ctx context.Context, meta Meta, argv []string, env map[string]string, stdin io.Reader, out limitedWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errw := &capWriter{max: maxStderr, quiet: true}
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: argv, Workdir: WorkspaceTarget(m.GOOS, meta.Workspace), User: AgentUser, Env: env,
		Interactive: stdin != nil, Stdin: stdin, Stdout: out, Stderr: errw,
	})
	switch {
	case out.exceeded():
		return fmt.Errorf("%w: more than %d MiB", ErrTooLarge, maxOutput>>20)
	case err != nil:
		return err
	case code != 0:
		return &ExecError{Code: code, Stderr: strings.TrimSpace(termsafe.CleanName(errw.buf.String()))}
	}
	return nil
}

// gitSession runs git in the agent container of one clone sandbox: bounded by
// a timeout, with the agent's configuration neutralised as far as git allows.
type gitSession struct {
	m       *Manager
	ctx     context.Context
	meta    Meta
	project string
	limit   time.Duration
	// agent is the environment of every call: no global or system config, no
	// replace objects, no optional locks (the agent's index is only read).
	agent map[string]string
}

func (m *Manager) newGitSession(ctx context.Context, meta Meta) *gitSession {
	limit := m.GitTimeout
	if limit <= 0 {
		limit = DefaultGitTimeout
	}
	return &gitSession{m: m, ctx: ctx, meta: meta, limit: limit, project: WorkspaceTarget(m.GOOS, meta.Workspace),
		agent: map[string]string{
			"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_OPTIONAL_LOCKS": "0",
			"GIT_NO_REPLACE_OBJECTS": "1", "GIT_ATTR_NOSYSTEM": "1",
		}}
}

func (s *gitSession) fail(sub string, err error) error {
	var ee *ExecError
	if errors.As(err, &ee) && (ee.Code == 124 || ee.Code == 137) {
		return fmt.Errorf("%w after %ds; the sandbox may contain a hostile program", ErrTimeout, int(s.limit.Seconds()))
	}
	return fmt.Errorf("git %s: %w", sub, err)
}

// subName is the git subcommand of an argument list: the first argument that
// is not a -c option or its value.
func subName(sub []string) string {
	for i := 0; i < len(sub); i++ {
		if sub[i] == "-c" {
			i++
			continue
		}
		return sub[i]
	}
	return sub[0]
}

// into runs git with env, an optional stdin and the caller's stdout.
func (s *gitSession) into(env map[string]string, stdin []byte, out limitedWriter, sub ...string) error {
	var in io.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	if err := s.m.runInto(s.ctx, s.meta, gitArgs(s.project, s.limit, sub...), env, in, out); err != nil {
		return s.fail(subName(sub), err)
	}
	return nil
}

// git runs git with env and returns its capped stdout.
func (s *gitSession) git(env map[string]string, sub ...string) ([]byte, error) {
	out := &capWriter{max: maxOutput}
	if err := s.into(env, nil, out, sub...); err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}

// harden turns every git filter the agent configured into cat, for all later calls.
func (s *gitSession) harden() ([]string, error) {
	names, err := s.m.configuredFilters(s.git, s.agent)
	if err != nil {
		return nil, err
	}
	over, err := filterOverrides(names)
	if err != nil {
		return nil, err
	}
	s.agent = merge(s.agent, over)
	return names, nil
}

func merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
