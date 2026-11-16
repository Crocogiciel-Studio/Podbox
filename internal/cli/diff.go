package cli

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

// diffResult is the JSON of podbox diff; Patch is absent with --stat.
type diffResult struct {
	Name  string               `json:"name"`
	Base  string               `json:"base"`
	Files []sandbox.FileChange `json:"files"`
	Patch *string              `json:"patch,omitempty"`
}

func (a *app) diffCmd() *cobra.Command {
	var stat bool
	cmd := &cobra.Command{
		Use:   "diff <sandbox> [--stat]",
		Short: "Show what the agent changed in a clone sandbox since its base commit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			d, err := m.Diff(cmd.Context(), args[0], !stat)
			if errors.Is(err, sandbox.ErrTooLarge) {
				err = fmt.Errorf("%w (nothing printed; list the files only with: podbox diff %s --stat)", err, args[0])
			}
			if err != nil {
				return err
			}
			res := diffResult{Name: args[0], Base: d.Base, Files: d.Files}
			if !stat {
				res.Patch = &d.Patch
			}
			return a.out.Print(res, func(w io.Writer) { writeDiff(w, res) })
		},
	}
	cmd.Flags().BoolVar(&stat, "stat", false, "list the changed files only, no patch")
	return cmd
}

// writeDiff prints the file summary, a total, then the patch. Everything in
// res has already been through termsafe.
func writeDiff(w io.Writer, res diffResult) {
	if len(res.Files) == 0 {
		fmt.Fprintf(w, "no changes since %s\n", short(res.Base))
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tADDED\tDELETED\tPATH")
	added, deleted := 0, 0
	for _, f := range res.Files {
		st, a, d, p := f.Status, fmt.Sprint(f.Added), fmt.Sprint(f.Deleted), f.Path
		if f.Untracked {
			st = "untracked"
		}
		if f.Binary {
			a, d = "-", "-"
		}
		if f.OldPath != "" {
			p = f.OldPath + " -> " + f.Path
		}
		added, deleted = added+f.Added, deleted+f.Deleted
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", st, a, d, p)
	}
	tw.Flush()
	fmt.Fprintf(w, "%d file(s) changed, +%d -%d since %s\n", len(res.Files), added, deleted, short(res.Base))
	if res.Patch != nil && *res.Patch != "" {
		fmt.Fprintf(w, "\n%s", *res.Patch)
	}
}
