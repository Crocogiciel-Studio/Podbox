package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/version"
)

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build information",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			v := struct {
				Version string `json:"version"`
				Commit  string `json:"commit"`
				OS      string `json:"os"`
				Arch    string `json:"arch"`
			}{version.Version, version.Commit, runtime.GOOS, runtime.GOARCH}
			return a.out.Print(v, func(w io.Writer) {
				fmt.Fprintf(w, "podbox %s (%s) %s/%s\n", v.Version, v.Commit, v.OS, v.Arch)
			})
		},
	}
}
