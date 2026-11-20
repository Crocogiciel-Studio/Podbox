package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/gitguard"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
)

type gitCheckResult struct {
	Name         string              `json:"name"`
	Accepted     bool                `json:"accepted,omitempty"`
	GitIntegrity *gitguard.Integrity `json:"git_integrity"`
	// Applicable is false for a clone sandbox: its git directory is the
	// agent's own, not one the host's git trusts.
	Applicable bool `json:"applicable"`
}

func (a *app) gitCheckCmd() *cobra.Command {
	var accept bool
	c := &cobra.Command{
		Use:   "git-check <sandbox>",
		Short: "Check the workspace's .git for changes git on the host could execute (exit 6 on findings)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			meta, err := m.Store.Load(args[0])
			if err != nil {
				return err
			}
			if !meta.Guard().Active() {
				return a.out.Print(gitCheckResult{Name: meta.Name, GitIntegrity: nil, Applicable: false},
					func(w io.Writer) { fmt.Fprintf(w, "%s: git-check does not apply to a clone sandbox\n", meta.Name) })
			}
			if accept || !m.Store.HasGitPrint(meta.Name) { // never recorded: nothing to compare yet
				if err := m.Store.RecordGit(meta); err != nil {
					return err
				}
			}
			g, err := m.Store.CheckGit(meta)
			if err != nil {
				return err
			}
			if !g.OK {
				a.out.Warn("%s", gitguard.Warning(meta.Workspace, *g, meta.Name))
			}
			err = a.out.Print(gitCheckResult{meta.Name, accept, g, true}, func(w io.Writer) {
				switch {
				case accept:
					fmt.Fprintf(w, "%s: .git fingerprint re-recorded\n", meta.Name)
				case g.OK:
					fmt.Fprintf(w, "%s: .git unchanged\n", meta.Name)
				default:
					fmt.Fprintf(w, "%s: %d suspicious .git change(s)\n", meta.Name, len(g.Findings))
				}
			})
			if err == nil && !g.OK {
				return output.ExitStatus(output.ExitGitIntegrity)
			}
			return err
		},
	}
	c.Flags().BoolVar(&accept, "accept", false, "re-record the fingerprint after you reviewed the changes")
	return c
}
