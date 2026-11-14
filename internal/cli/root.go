// Package cli wires podbox commands.
package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
)

type app struct {
	outputFlag string
	out        *output.Printer
	started    bool // true once flags parsed and the command body runs
}

// Execute runs podbox with os.Args and returns the process exit code.
func Execute() int {
	return run(os.Args[1:])
}

func run(args []string) int {
	a := &app{out: &output.Printer{Mode: output.Text, Out: os.Stdout, Err: os.Stderr}}
	root := a.newRoot()
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return output.ExitOK
	}
	var typed *output.Error
	var status output.ExitStatus
	if !a.started {
		if !errors.As(err, &typed) && !errors.As(err, &status) {
			err = &output.Error{Code: output.ExitUsage, Err: err}
		}
		a.out.Mode = modeFromArgs(args)
	}
	return a.out.PrintError(err)
}

func (a *app) newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "podbox",
		Short:         "Run coding agents in a closed-network rootless Podman sandbox",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			m, err := output.ParseMode(a.outputFlag)
			if err != nil {
				return err
			}
			a.out.Mode = m
			a.started = true
			return nil
		},
	}
	root.PersistentFlags().StringVarP(&a.outputFlag, "output", "o", "text", "output format: text or json")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &output.Error{Code: output.ExitUsage, Err: err}
	})
	root.AddCommand(a.versionCmd(), a.doctorCmd(), a.policyCmd())
	return root
}

// modeFromArgs finds --output when cobra failed before parsing flags.
func modeFromArgs(args []string) output.Mode {
	for i, arg := range args {
		v, ok := "", false
		switch {
		case arg == "--output" || arg == "-o":
			if i+1 < len(args) {
				v, ok = args[i+1], true
			}
		case strings.HasPrefix(arg, "--output="):
			v, ok = strings.TrimPrefix(arg, "--output="), true
		case strings.HasPrefix(arg, "-o="):
			v, ok = strings.TrimPrefix(arg, "-o="), true
		}
		if ok && v == string(output.JSON) {
			return output.JSON
		}
	}
	return output.Text
}
