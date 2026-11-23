package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) authCmd() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Log an agent in from inside its sandbox"}
	var timeout time.Duration
	login := &cobra.Command{
		Use:   "login <sandbox> [-- <command>...]",
		Short: "Run the agent's login in a running sandbox and relay its browser callback",
		Long: "Runs the agent's login command (kiro-cli login for kiro) in a running sandbox. " +
			"Open each URL it prints in a browser on this machine: podbox never opens it for you. " +
			"While the login runs, podbox listens on this machine's loopback on the login's callback port " +
			"and replays the browser's callback into the sandbox, so no port is published.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var override []string
			switch dash := cmd.ArgsLenAtDash(); {
			case dash == -1 && len(args) == 1:
			case dash == 1 && len(args) > 1:
				override = args[1:]
			default:
				return output.Errorf(output.ExitUsage, "usage: podbox auth login <sandbox> [-- <command>...]")
			}
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			tio := terminal()
			res, err := m.Login(cmd.Context(), args[0], sandbox.LoginOptions{Command: override, Timeout: timeout}, tio, a.loginEvents(args[0], tio.TTY))
			a.gitCheck(m, args[0])
			if err != nil {
				return err
			}
			if res.Code != 0 && res.URLs == 0 {
				a.out.Warn("the login opened no URL in the sandbox; if it failed to open a browser, rebuild the image (podbox image build <agent> --force), then podbox stop %s and podbox start %s", args[0], args[0])
			}
			summary := struct {
				Name      string   `json:"name"`
				Command   []string `json:"command"`
				URLs      int      `json:"urls"`
				Callbacks int      `json:"callbacks"`
				ExitCode  int      `json:"exit_code"`
			}{args[0], res.Command, res.URLs, res.Callbacks, res.Code}
			if err := a.out.Print(summary, func(w io.Writer) {
				fmt.Fprintf(w, "%s: %s exited with %d; %d callback(s) relayed\n", args[0], strings.Join(res.Command, " "), res.Code, res.Callbacks)
			}); err != nil {
				return err
			}
			return exitWith(res.Code, nil)
		},
	}
	login.Flags().DurationVar(&timeout, "timeout", sandbox.DefaultLoginTimeout, "stop the login and the relay after this long")
	auth.AddCommand(login)
	return auth
}

// loginEvents prints a login's progress on stderr. On a terminal the agent's
// session holds it in raw mode, so each line starts and ends with a carriage return.
func (a *app) loginEvents(name string, tty bool) func(sandbox.LoginEvent) {
	pre, nl := "", "\n"
	if tty {
		pre, nl = "\r", "\r\n"
	}
	say := func(format string, args ...any) {
		fmt.Fprintf(a.out.Err, pre+"podbox: "+format+nl, args...)
	}
	return func(e sandbox.LoginEvent) {
		switch e.Kind {
		case sandbox.EventListening:
			say("relaying the login callback from http://localhost:%d on this machine into the sandbox until the login ends", e.Port)
		case sandbox.EventNeedsHost:
			say("the login needs %s, which this sandbox's policy does not allow; in another terminal run: podbox policy allow %s %s and reload the browser page", e.Host, name, e.Host)
		case sandbox.EventCallback:
			say("callback delivered to the sandbox (HTTP %d)", e.Status)
		case sandbox.EventWarning:
			say("warning: %s", e.Message)
		}
	}
}
