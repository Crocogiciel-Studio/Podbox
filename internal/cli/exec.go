package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
)

func (a *app) execCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec <sandbox> -- <command>...",
		Short: "Run a command in a running sandbox; exits with its code",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 1 {
				return output.Errorf(output.ExitUsage, "usage: podbox exec <sandbox> -- <command>...")
			}
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			code, err := m.Exec(cmd.Context(), args[0], args[1:], terminal())
			a.gitCheck(m, args[0])
			return exitWith(code, err)
		},
	}
}

func (a *app) attachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <sandbox>",
		Short: "Open a new agent session in a running sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			start := time.Now()
			code, err := m.Attach(cmd.Context(), args[0], terminal())
			a.gitCheck(m, args[0])
			a.blockedNotice(cmd.Context(), m, args[0], start)
			return exitWith(code, err)
		},
	}
}
