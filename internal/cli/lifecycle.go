package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/gitguard"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) manager(ctx context.Context) (*sandbox.Manager, error) {
	e, err := a.engine(ctx)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir() // no home: nothing to protect beyond podbox's directories
	return &sandbox.Manager{
		RT: e.rt, Store: e.store, Images: e.builder, Config: e.cfg, Info: e.info,
		GOOS: goruntime.GOOS, Getenv: os.Getenv, Warn: a.out.Warn,
		Home: home, Owned: e.paths.Owned(),
	}, nil
}

type actionResult struct {
	Name         string              `json:"name"`
	Action       string              `json:"action"`
	GitIntegrity *gitguard.Integrity `json:"git_integrity,omitempty"`
	Info         string              `json:"info,omitempty"`
	// Imported lists what create took from the project files.
	Imported *sandbox.Imported `json:"imported,omitempty"`
}

func (a *app) done(name, action string, g *gitguard.Integrity, info string) error {
	return a.out.Print(actionResult{Name: name, Action: action, GitIntegrity: g, Info: info}, func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", name, action) })
}

// hookNotice prints, once per launch, which worktree files define git
// hooks the agent can modify.
func (a *app) hookNotice(m *sandbox.Manager, name string) string {
	meta, err := m.Store.Load(name)
	if err != nil {
		return ""
	}
	n := gitguard.Notice(meta.Name, meta.Workspace)
	if n != "" {
		a.out.Info("info: %s", n)
	}
	return n
}

// gitCheck compares the sandbox's .git with its fingerprint and warns on
// findings; nil when there is nothing to compare. It never fails the command.
func (a *app) gitCheck(m *sandbox.Manager, name string) *gitguard.Integrity {
	meta, err := m.Store.Load(name)
	if err != nil {
		return nil
	}
	g, err := m.Store.CheckGit(meta)
	if err != nil {
		a.out.Warn("%s: git integrity check failed: %v", name, err)
		return nil
	}
	if g != nil && !g.OK {
		a.out.Warn("%s", gitguard.Warning(meta.Workspace, *g, meta.Name))
	}
	return g
}

// simple builds start/stop: one sandbox name, one manager call; launch
// adds the hook notice.
func (a *app) simple(use, short, action string, launch bool, do func(*sandbox.Manager, context.Context, string) error) *cobra.Command {
	return &cobra.Command{
		Use: use + " <sandbox>", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			if err := do(m, cmd.Context(), args[0]); err != nil {
				return err
			}
			info := ""
			if launch {
				info = a.hookNotice(m, args[0])
			}
			return a.done(args[0], action, a.gitCheck(m, args[0]), info)
		},
	}
}

func (a *app) lifecycleCmds() []*cobra.Command {
	start := a.simple("start", "Start a sandbox (recreating stopped containers with fresh masks)", "started", true, (*sandbox.Manager).Start)
	stop := a.simple("stop", "Stop a sandbox's agent and proxy", "stopped", false, (*sandbox.Manager).Stop)
	var volumes bool
	rm := &cobra.Command{
		Use: "rm <sandbox>", Short: "Remove a sandbox's containers, network and state", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			g := a.gitCheck(m, args[0]) // before the fingerprint is deleted
			if err := m.Remove(cmd.Context(), args[0], volumes); err != nil {
				return err
			}
			return a.done(args[0], "removed", g, "")
		},
	}
	rm.Flags().BoolVar(&volumes, "volumes", false, "also remove the agent home volume (loses agent login)")
	return []*cobra.Command{start, stop, rm, a.gitCheckCmd()}
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
		return err
	}
	if code != 0 {
		return output.ExitStatus(code)
	}
	return nil
}
