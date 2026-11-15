package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/agents"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

type createFlags struct{ name string }

func (f *createFlags) bind(c *cobra.Command) {
	c.Flags().StringVar(&f.name, "name", "", "sandbox name (default <agent>-<directory>)")
}

// target validates the agent, workspace and name from arguments.
func (f *createFlags) target(args []string) (agent, ws, name string, err error) {
	agent, ws = args[0], "."
	if len(args) > 1 {
		ws = args[1]
	}
	if _, err := agents.Get(agent); err != nil {
		return "", "", "", output.Wrap(output.ExitUsage, err, "")
	}
	if ws, err = filepath.Abs(ws); err != nil {
		return "", "", "", err
	}
	name = f.name
	if name == "" {
		name = sandbox.DefaultName(agent, ws)
	}
	if err := sandbox.ValidateName(name); err != nil {
		return "", "", "", output.Wrap(output.ExitUsage, err, "")
	}
	return agent, ws, name, nil
}

func (a *app) createCmd() *cobra.Command {
	var f createFlags
	c := &cobra.Command{
		Use:   "create <agent> [path]",
		Short: "Create a sandbox for an agent on a project directory",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, ws, name, err := f.target(args)
			if err != nil {
				return err
			}
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			_, imp, err := m.Create(cmd.Context(), name, agent, ws)
			if err != nil {
				return err
			}
			a.printImported(name, imp)
			return a.out.Print(actionResult{Name: name, Action: "created", Info: a.hookNotice(m, name), Imported: &imp},
				func(w io.Writer) { fmt.Fprintf(w, "%s created\n", name) })
		},
	}
	f.bind(c)
	return c
}

func (a *app) runCmd() *cobra.Command {
	var f createFlags
	c := &cobra.Command{
		Use:   "run <agent> [path]",
		Short: "Create if needed, start, and open an agent session (claude, kiro, shell)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, ws, name, err := f.target(args)
			if err != nil {
				return err
			}
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.ensureSandbox(cmd.Context(), m, name, agent, ws); err != nil {
				return err
			}
			if err := m.Start(cmd.Context(), name); err != nil {
				return err
			}
			a.hookNotice(m, name)
			start := time.Now()
			code, err := m.Attach(cmd.Context(), name, terminal())
			a.gitCheck(m, name)
			a.blockedNotice(cmd.Context(), m, name, start)
			return exitWith(code, err)
		},
	}
	f.bind(c)
	return c
}

// printImported shows, one line per rule, what create took from the project
// files: the agent may have written them in an earlier sandbox.
func (a *app) printImported(name string, imp sandbox.Imported) {
	for _, r := range imp.Rules {
		a.out.Info("%s: imported rule from %s: %s", name, sandbox.ProjectPolicyRel, r)
	}
	for _, p := range imp.Ignore {
		a.out.Info("%s: imported %s pattern: %s", name, workspace.IgnoreFile, p)
	}
}

func (a *app) ensureSandbox(ctx context.Context, m *sandbox.Manager, name, agent, ws string) error {
	meta, err := m.Store.Load(name)
	switch {
	case errors.Is(err, sandbox.ErrNotFound):
		var imp sandbox.Imported
		if _, imp, err = m.Create(ctx, name, agent, ws); err == nil {
			a.printImported(name, imp)
		}
		return err
	case err != nil:
		return err
	case meta.Agent != agent || meta.Workspace != ws:
		return output.Errorf(output.ExitGeneric, "sandbox %s already exists for %s on %s; pick another --name", name, meta.Agent, meta.Workspace)
	}
	return nil
}
