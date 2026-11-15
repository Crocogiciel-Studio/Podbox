package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// LocatePodman finds the podman binary: PODBOX_PODMAN, then PATH.
func LocatePodman() (string, error) {
	if p := os.Getenv("PODBOX_PODMAN"); p != "" {
		return p, nil
	}
	return exec.LookPath("podman")
}

type streams struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

type runner interface {
	// output runs and returns stdout; a non-zero exit is an error with stderr.
	output(ctx context.Context, bin string, args []string) ([]byte, error)
	// stream runs with the given streams; a non-zero exit is an error.
	stream(ctx context.Context, bin string, args []string, s streams) error
	// interactive runs with the given streams and returns the exit code.
	interactive(ctx context.Context, bin string, args []string, s streams) (int, error)
}

type execRunner struct{}

// CommandError is a failed podman invocation.
type CommandError struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.Code)
	}
	return fmt.Sprintf("podman %s: %s", firstWords(e.Args), msg)
}

// Is maps engine "no such ..." messages to ErrNotFound.
func (e *CommandError) Is(target error) bool {
	if target != ErrNotFound {
		return false
	}
	s := strings.ToLower(e.Stderr)
	return strings.Contains(s, "no such") || strings.Contains(s, "not found")
}

func firstWords(args []string) string {
	if len(args) > 2 {
		args = args[:2]
	}
	return strings.Join(args, " ")
}

func (execRunner) output(ctx context.Context, bin string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, commandError(args, err, stderr.String())
	}
	return out, nil
}

func (execRunner) stream(ctx context.Context, bin string, args []string, s streams) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout = s.Stdin, s.Stdout
	var tail bytes.Buffer
	cmd.Stderr = &tail
	if s.Stderr != nil {
		cmd.Stderr = io.MultiWriter(s.Stderr, &tail)
	}
	if err := cmd.Run(); err != nil {
		return commandError(args, err, lastLines(tail.String(), 5))
	}
	return nil
}

func (execRunner) interactive(ctx context.Context, bin string, args []string, s streams) (int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.Stdin, s.Stdout, s.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func commandError(args []string, err error, stderr string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &CommandError{Args: args, Code: ee.ExitCode(), Stderr: stderr}
	}
	return err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
