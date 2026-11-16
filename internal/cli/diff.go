package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

// diffResult is the JSON of podbox diff; Patch is absent with --stat.
type diffResult struct {
	Name  string               `json:"name"`
	Base  string               `json:"base"`
	Files []sandbox.FileChange `json:"files"`
	Patch *string              `json:"patch,omitempty"`
	// Ignored counts ignored paths not listed in Files.
	Ignored       int  `json:"ignored"`
	IgnoredCapped bool `json:"ignored_capped,omitempty"`
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
			if v, err := strconv.Atoi(os.Getenv("PODBOX_DIFF_TIMEOUT")); err == nil && v > 0 { // test knob
				m.GitTimeout = time.Duration(v) * time.Second
			}
			d, err := m.Diff(cmd.Context(), args[0], !stat)
			if errors.Is(err, sandbox.ErrTooLarge) {
				err = fmt.Errorf("%w (nothing printed; list the files only with: podbox diff %s --stat)", err, args[0])
			}
			if err != nil {
				return err
			}
			res := diffResult{Name: args[0], Base: d.Base, Files: d.Files, Ignored: d.Ignored, IgnoredCapped: d.IgnoredCapped}
			if !stat {
				res.Patch = &d.Patch
			}
			return a.out.Print(res, func(w io.Writer) { writeDiff(w, res) })
		},
	}
	cmd.Flags().BoolVar(&stat, "stat", false, "list the changed files only, no patch")
	return cmd
}

// writeDiff prints the file summary, a total, the count of ignored paths,
// then the patch. Everything in res has already been through termsafe.
func writeDiff(w io.Writer, res diffResult) {
	if len(res.Files) == 0 {
		fmt.Fprintf(w, "no changes since %s\n", short(res.Base))
	} else {
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
			if f.Gitlink {
				p += " [nested repository: only its commit is compared]"
			}
			added, deleted = added+f.Added, deleted+f.Deleted
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", st, a, d, p)
		}
		tw.Flush()
		fmt.Fprintf(w, "%d file(s) changed, +%d -%d since %s\n", len(res.Files), added, deleted, short(res.Base))
	}
	if res.Ignored > 0 {
		more := ""
		if res.IgnoredCapped {
			more = " or more"
		}
		fmt.Fprintf(w, "%d%s ignored path(s) not shown (git ignores them, so the agent can keep files there)\n", res.Ignored, more)
	}
	if res.Patch != nil && *res.Patch != "" {
		fmt.Fprintf(w, "\n%s", *res.Patch)
	}
}
