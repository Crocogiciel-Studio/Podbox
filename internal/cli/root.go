// Package cli wires podbox commands.
package cli

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
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
	if !a.started {
		a.out.Mode = modeFromArgs(args)
	}
	return a.out.PrintError(classify(err, a.started))
}

// classify gives err its exit code from the sentinel or type it carries;
// before a command body runs (cobra argument errors), untyped errors are
// usage errors.
func classify(err error, started bool) error {
	var typed *output.Error
	var status output.ExitStatus
	var cmdErr *runtime.CommandError
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	var sysErr *os.SyscallError
	code := output.ExitGeneric
	switch {
	case errors.As(err, &typed), errors.As(err, &status):
		return err
	case !started:
		code = output.ExitUsage
	case errors.Is(err, sandbox.ErrNotFound):
		code = output.ExitNotFound
	case errors.Is(err, workspace.ErrMasks):
		code = output.ExitMasks
	case errors.Is(err, workspace.ErrUnsafe), errors.Is(err, policy.ErrInvalid), errors.Is(err, sandbox.ErrBadWorkspace):
		code = output.ExitUsage
	case errors.Is(err, hostgit.ErrMissing), errors.Is(err, hostgit.ErrTooOld):
		code = output.ExitPrereq
	case errors.Is(err, hostgit.ErrNotRepo), errors.Is(err, hostgit.ErrNotTopLevel), errors.Is(err, hostgit.ErrEmptyRepo):
		code = output.ExitUsage
	case errors.Is(err, sandbox.ErrNotClone):
		code = output.ExitUsage
	case errors.Is(err, sandbox.ErrCloneExists), errors.Is(err, sandbox.ErrModeMismatch):
		code = output.ExitGeneric
	case errors.As(err, &cmdErr), errors.As(err, &pathErr), errors.As(err, &linkErr), errors.As(err, &sysErr):
		code = output.ExitRuntime // podman or a host file operation failed
	}
	return &output.Error{Code: code, Err: err}
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
	root.AddCommand(a.runCmd(), a.createCmd(), a.lsCmd(), a.execCmd(), a.attachCmd(), a.diffCmd(), a.policyCmd(), a.imageCmd(), a.doctorCmd(), a.versionCmd(), a.proxyCmd())
	root.AddCommand(a.lifecycleCmds()...)
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
