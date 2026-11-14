package cli

import (
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
			return exitWith(m.Exec(cmd.Context(), args[0], args[1:], terminal()))
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
			return exitWith(m.Attach(cmd.Context(), args[0], terminal()))
		},
	}
}
