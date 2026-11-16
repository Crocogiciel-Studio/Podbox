package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) exportCmd() *cobra.Command {
	var as, branchName, out string
	var includeMasked bool
	cmd := &cobra.Command{
		Use:   "export <sandbox> --as branch|patch",
		Short: "Bring the agent's commits from a clone sandbox to the host as a new branch or a patch file",
		Long: `Bring the agent's commits since the clone's base out of a clone sandbox.

--as branch creates a new branch in the host repository (default
podbox/<sandbox>/<agent branch>, or --branch-name) from a bundle the host
verifies and fetches with object checks. It never checks it out and never
overwrites a branch.

--as patch writes one combined diff for git apply (default <sandbox>.patch in
the current directory, or --out), without commit messages, and never overwrites a file.

Only committed work is exported; nothing is merged. Commits that touch a masked
path are refused unless --include-masked is passed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opt := sandbox.ExportOptions{As: as, BranchName: branchName, Out: out, IncludeMasked: includeMasked}
			if err := opt.Validate(); err != nil {
				return err
			}
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			if v, err := strconv.ParseInt(os.Getenv("PODBOX_EXPORT_MAX_BYTES"), 10, 64); err == nil && v > 0 { // test knob
				m.MaxExport = v
			}
			res, err := m.Export(cmd.Context(), args[0], opt)
			if err != nil {
				return err
			}
			return a.out.Print(res, func(w io.Writer) { writeExport(w, res) })
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "what to create: branch or patch (required)")
	cmd.Flags().StringVar(&branchName, "branch-name", "", "host branch to create (with --as branch)")
	cmd.Flags().StringVar(&out, "out", "", "patch file to write (with --as patch)")
	cmd.Flags().BoolVar(&includeMasked, "include-masked", false, "export commits that touch masked paths")
	return cmd
}

// writeExport prints what an export did. Everything in res is already clean.
func writeExport(w io.Writer, res sandbox.ExportResult) {
	switch {
	case !res.Exported:
		fmt.Fprintf(w, "nothing to export: %s has no commit since %s\n", res.Name, short(res.Base))
	case res.As == sandbox.AsBranch:
		fmt.Fprintf(w, "created branch %s with %d commit(s) from %s (tip %s)\n", res.Branch, res.Commits, res.Name, short(res.Head))
	default:
		fmt.Fprintf(w, "wrote %s with %d commit(s) from %s (tip %s)\n", res.File, res.Commits, res.Name, short(res.Head))
	}
	if res.BranchSanitized {
		fmt.Fprintf(w, "the agent's branch name %q was altered to make a safe branch name\n", res.AgentBranch)
	}
	for _, c := range res.CommitList {
		fmt.Fprintf(w, "  %s %s\n", short(c.ID), c.Subject)
	}
	if res.Commits > len(res.CommitList) {
		fmt.Fprintf(w, "  ... %d more\n", res.Commits-len(res.CommitList))
	}
	if res.Exported {
		fmt.Fprintf(w, "%d file(s) changed since %s\n", res.FilesChanged, short(res.Base))
		if !res.HostVerified {
			fmt.Fprintln(w, "the patch is text the sandbox produced: only the base commit was checked on the host")
		}
	}
	if len(res.MaskedTouched) > 0 {
		fmt.Fprintf(w, "WARNING: --include-masked: the commits touch masked paths: %s\n", strings.Join(res.MaskedTouched, ", "))
	}
	if len(res.Warnings) > 0 {
		fmt.Fprintln(w, "WARNING: the commits contain things that can act when you check them out or build them. Review with podbox diff and git diff before checking anything out:")
		for _, k := range res.Warnings {
			fmt.Fprintf(w, "  %s (%d): %s: %s\n", k.Kind, k.Count, k.Detail, strings.Join(k.Examples, ", "))
		}
	}
	n := res.NotExported
	if n.Uncommitted+n.Untracked > 0 {
		fmt.Fprintf(w, "not exported: %d uncommitted, %d untracked (commit them in the sandbox to export them)\n", n.Uncommitted, n.Untracked)
		for _, p := range n.UncommittedFiles {
			fmt.Fprintf(w, "  uncommitted  %s\n", p)
		}
		for _, p := range n.UntrackedFiles {
			fmt.Fprintf(w, "  untracked    %s\n", p)
		}
	} else if res.Exported {
		fmt.Fprintln(w, "not exported: nothing uncommitted or untracked")
	}
	if res.Exported && res.As == sandbox.AsBranch {
		fmt.Fprintf(w, "nothing was checked out or merged; review with: git log %s..%s\n", short(res.Base), res.Branch)
	}
}
