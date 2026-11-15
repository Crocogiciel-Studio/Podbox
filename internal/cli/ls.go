package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/gitguard"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			list, err := m.List(cmd.Context())
			if err != nil {
				return err
			}
			// Sandboxes sharing a workspace and its findings get one warning.
			type group struct {
				ws    string
				g     gitguard.Integrity
				names []string
			}
			var groups []*group
			byKey := map[string]*group{}
			for _, s := range list {
				if len(s.ProjectChanged) > 0 {
					a.out.Warn("%s", sandbox.DriftWarning(s.Name, s.ProjectChanged))
				}
				if s.GitIntegrity == nil || s.GitIntegrity.OK {
					continue
				}
				key := fmt.Sprintf("%s\x00%v", s.Workspace, s.GitIntegrity.Findings)
				if byKey[key] == nil {
					byKey[key] = &group{ws: s.Workspace, g: *s.GitIntegrity}
					groups = append(groups, byKey[key])
				}
				byKey[key].names = append(byKey[key].names, s.Name)
			}
			for _, g := range groups {
				a.out.Warn("%s", gitguard.Warning(g.ws, g.g, g.names...))
			}
			return a.out.Print(list, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tAGENT\tSTATE\tPROXY\tWORKSPACE")
				for _, s := range list {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Agent, s.State, s.Proxy, s.Workspace)
				}
				tw.Flush()
			})
		},
	}
}
