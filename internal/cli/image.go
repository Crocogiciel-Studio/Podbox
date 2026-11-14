package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/images"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
)

func (a *app) imageCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "image", Short: "Build and list podbox images"}
	var force bool
	build := &cobra.Command{
		Use:   "build <image>...",
		Short: "Build images: proxy, base, claude, kiro (experimental), shell",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var kinds []images.Kind
			for _, s := range args {
				k, err := images.ParseKind(s)
				if err != nil {
					return output.Wrap(output.ExitUsage, err, "")
				}
				kinds = append(kinds, k)
			}
			e, err := a.engine(cmd.Context())
			if err != nil {
				return err
			}
			type built struct {
				Image string `json:"image"`
				Ref   string `json:"ref"`
			}
			var res []built
			for _, k := range kinds {
				if images.Experimental[k] {
					a.out.Warn("image %s is experimental: its definition is unverified", k)
				}
				ref, err := e.builder.Ensure(cmd.Context(), k, force)
				if err != nil {
					return output.Wrap(output.ExitRuntime, err, "")
				}
				res = append(res, built{string(k), ref})
			}
			return a.out.Print(res, func(w io.Writer) {
				for _, b := range res {
					fmt.Fprintf(w, "%s\t%s\n", b.Image, b.Ref)
				}
			})
		},
	}
	build.Flags().BoolVar(&force, "force", false, "rebuild even when the image exists")
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List built podbox images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := a.engine(cmd.Context())
			if err != nil {
				return err
			}
			list, err := images.List(cmd.Context(), e.rt)
			if err != nil {
				return output.Wrap(output.ExitRuntime, err, "")
			}
			if list == nil {
				list = []images.Info{}
			}
			return a.out.Print(list, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "IMAGE\tAGENT\tVERSION\tREF")
				for _, i := range list {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", i.Image, i.Agent, i.Version, i.Ref)
				}
				tw.Flush()
			})
		},
	}
	cmd.AddCommand(build, ls)
	return cmd
}
