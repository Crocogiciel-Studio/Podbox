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

// runInto is runIn with a stdin (nil for none) and the caller's writer.
func (m *Manager) runInto(ctx context.Context, meta Meta, argv []string, env map[string]string, stdin io.Reader, out *capWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errw := &capWriter{max: maxStderr, quiet: true}
	code, err := m.RT.Exec(ctx, agentContainer(meta.Name), runtime.ExecSpec{
		Command: argv, Workdir: WorkspaceTarget(m.GOOS, meta.Workspace), User: AgentUser, Env: env,
		Interactive: stdin != nil, Stdin: stdin, Stdout: out, Stderr: errw,
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
