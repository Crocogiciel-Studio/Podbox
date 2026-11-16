package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) diffCmd() *cobra.Command {
	var stat, showBinary bool
	cmd := &cobra.Command{
		Use:   "diff <sandbox> [--stat | --show-binary]",
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
			res, err := m.Diff(cmd.Context(), args[0], sandbox.DiffOptions{Patch: !stat, ShowBinary: showBinary})
			if errors.Is(err, sandbox.ErrTooLarge) {
				err = fmt.Errorf("%w (nothing printed; list the files only with: podbox diff %s --stat)", err, args[0])
			}
			if err != nil {
				return err
			}
			return a.out.Print(res, func(w io.Writer) { writeDiff(w, res) })
		},
	}
	cmd.Flags().BoolVar(&stat, "stat", false, "list the changed files only, no patch")
	cmd.Flags().BoolVar(&showBinary, "show-binary", false, "print the content of files that hold NUL bytes (bounded per file), which is otherwise summarised")
	cmd.MarkFlagsMutuallyExclusive("stat", "show-binary")
	return cmd
}

// fileNote says what the summary line of a file needs a reviewer to know.
func fileNote(f sandbox.FileChange) string {
	var n []string
	if f.Gitlink {
		n = append(n, "nested repository: only its commit is compared")
	}
	if f.Symlink {
		n = append(n, "symlink")
	}
	if f.Exec {
		n = append(n, "executable")
	}
	if f.Binary && f.BinaryHidden {
		n = append(n, "binary: contains NUL bytes, not shown; see --show-binary")
	} else if f.Binary {
		n = append(n, "binary: contains NUL bytes")
	}
	if f.TooLarge {
		n = append(n, "too large: patch not shown")
	}
	if len(f.Conversions) > 0 {
		n = append(n, "conversion: "+strings.Join(f.Conversions, ", "))
	}
	if f.BinaryAttr {
		n = append(n, "git attributes mark it binary; shown as text")
	}
	if len(n) == 0 {
		return ""
	}
	return " [" + strings.Join(n, "; ") + "]"
}

// writeDiff prints the file summary, a total, what else a reviewer should
// know, then the patch. Everything in res has already been through termsafe.
func writeDiff(w io.Writer, res sandbox.DiffResult) {
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
			added, deleted = added+f.Added, deleted+f.Deleted
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s%s\n", st, a, d, p, fileNote(f))
		}
		tw.Flush()
		fmt.Fprintf(w, "%d file(s) changed, +%d -%d since %s\n", len(res.Files), added, deleted, short(res.Base))
	}
	conv := len(res.Filters) > 0
	for _, f := range res.Files {
		conv = conv || len(f.Conversions) > 0
	}
	if conv {
		fmt.Fprintf(w, "WARNING: git filters or conversions are in play (filters: %s). The bytes below are the raw bytes of each file; what the agent's own git would commit may differ.\n", strings.Join(res.Filters, ", "))
	}
	if res.Ignored > 0 {
		more := ""
		if res.IgnoredCapped {
			more = " or more"
		}
		fmt.Fprintf(w, "%d%s ignored untracked path(s) not shown (git ignores them, so the agent can keep files there)\n", res.Ignored, more)
	}
	if res.CaseCollisions > 0 {
		fmt.Fprintf(w, "%d path(s) differ only by case and collide on a case-insensitive file system: %s\n", res.CaseCollisions, strings.Join(res.CaseExamples, ", "))
	}
	if res.NonASCII > 0 {
		fmt.Fprintf(w, "%d path(s) have non-ASCII characters, which can imitate ASCII ones: %s\n", res.NonASCII, strings.Join(res.NonASCIIExamples, ", "))
	}
	if res.Patch != nil && *res.Patch != "" {
		fmt.Fprintf(w, "\n%s", *res.Patch)
	}
}
