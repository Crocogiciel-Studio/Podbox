package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	goruntime "runtime"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/doctor"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check host prerequisites and explain how to fix them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cs := doctor.Run(cmd.Context(), hostEnv())
			if err := a.out.Print(cs, func(w io.Writer) { printChecks(w, cs) }); err != nil {
				return err
			}
			if doctor.Failed(cs) {
				return output.ExitStatus(output.ExitPrereq)
			}
			return nil
		},
	}
}

func hostEnv() doctor.Env {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return doctor.Env{
		GOOS: goruntime.GOOS, User: name, UID: os.Getuid(),
		LocatePodman: runtime.LocatePodman,
		Info: func(ctx context.Context) (runtime.Info, error) {
			p, err := runtime.NewPodman()
			if err != nil {
				return runtime.Info{}, err
			}
			return p.Info(ctx)
		},
		ReadFile:        os.ReadFile,
		Stat:            os.Stat,
		LimitsDelegated: runtime.LimitsDelegated,
	}
}

func printChecks(w io.Writer, cs []doctor.Check) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, c := range cs {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Status, c.Name, c.Detail)
		if c.Fix != "" && c.Status != doctor.OK {
			fmt.Fprintf(tw, "\t\t  fix: %s\n", c.Fix)
		}
	}
	tw.Flush()
}
