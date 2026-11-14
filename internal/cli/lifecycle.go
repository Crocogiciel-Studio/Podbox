package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) manager(ctx context.Context) (*sandbox.Manager, error) {
	e, err := a.engine(ctx)
	if err != nil {
		return nil, err
	}
	return &sandbox.Manager{
		RT: e.rt, Store: e.store, Images: e.builder, Config: e.cfg, Info: e.info,
		GOOS: goruntime.GOOS, Getenv: os.Getenv, Warn: a.out.Warn,
	}, nil
}

// classify maps sandbox errors to exit codes.
func classify(err error) error {
	var typed *output.Error
	var cmdErr *runtime.CommandError
	switch {
	case err == nil || errors.As(err, &typed):
		return err
	case errors.Is(err, sandbox.ErrNotFound):
		return &output.Error{Code: output.ExitNotFound, Err: err}
	case errors.Is(err, sandbox.ErrExists), errors.Is(err, sandbox.ErrNotRunning):
		return &output.Error{Code: output.ExitGeneric, Err: err}
	case errors.As(err, &cmdErr):
		return &output.Error{Code: output.ExitRuntime, Err: err}
	}
	return err
}

type actionResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

func (a *app) done(name, action string) error {
	return a.out.Print(actionResult{name, action}, func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", name, action) })
}

// simple builds start/stop: one sandbox name, one manager call.
func (a *app) simple(use, short, action string, do func(*sandbox.Manager, context.Context, string) error) *cobra.Command {
	return &cobra.Command{
		Use: use + " <sandbox>", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			if err := do(m, cmd.Context(), args[0]); err != nil {
				return classify(err)
			}
			return a.done(args[0], action)
		},
	}
}

func (a *app) lifecycleCmds() []*cobra.Command {
	start := a.simple("start", "Start a sandbox (recreating stopped containers with fresh masks)", "started", (*sandbox.Manager).Start)
	stop := a.simple("stop", "Stop a sandbox's agent and proxy", "stopped", (*sandbox.Manager).Stop)
	var volumes bool
	rm := &cobra.Command{
		Use: "rm <sandbox>", Short: "Remove a sandbox's containers, network and state", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			if err := m.Remove(cmd.Context(), args[0], volumes); err != nil {
				return classify(err)
			}
			return a.done(args[0], "removed")
		},
	}
	rm.Flags().BoolVar(&volumes, "volumes", false, "also remove the agent home volume (loses agent login)")
	return []*cobra.Command{start, stop, rm}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// terminal wires the process stdio; stdin is passed only from a terminal
// or a pipe so scripts do not lose their input.
func terminal() sandbox.IO {
	tio := sandbox.IO{Out: os.Stdout, Err: os.Stderr, TTY: isTerminal(os.Stdin) && isTerminal(os.Stdout)}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&(os.ModeCharDevice|os.ModeNamedPipe) != 0 {
		tio.In = os.Stdin
	}
	return tio
}

func exitWith(code int, err error) error {
	if err != nil {
		return classify(err)
	}
	if code != 0 {
		return output.ExitStatus(code)
	}
	return nil
}
