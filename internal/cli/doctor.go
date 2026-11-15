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

	"github.com/Crocogiciel-Studio/podbox/internal/config"
	"github.com/Crocogiciel-Studio/podbox/internal/doctor"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

func (a *app) doctorCmd() *cobra.Command {
	var pull bool
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Check host prerequisites and explain how to fix them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, paths, err := a.store()
			if err != nil {
				return err
			}
			cfg, err := config.Load(paths.Config)
			if err != nil {
				return output.Wrap(output.ExitUsage, err, "config")
			}
			cs := doctor.Run(cmd.Context(), hostEnv(cfg.Images.Base, pull))
			if err := a.out.Print(cs, func(w io.Writer) { printChecks(w, cs) }); err != nil {
				return err
			}
			if doctor.Failed(cs) {
				return output.ExitStatus(output.ExitPrereq)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&pull, "pull", false, "also pull the base image when it is not present (needs the network)")
	return c
}

func hostEnv(base string, pull bool) doctor.Env {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	e := doctor.Env{
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
		BaseImage:       base,
	}
	if p, err := runtime.NewPodman(); err == nil {
		e.ImageExists = p.ImageExists
		if pull {
			e.Pull = p.ImagePull
		}
	}
	return e
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
