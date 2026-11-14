package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func (a *app) policyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Inspect and change a sandbox's network allowlist"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show <sandbox>",
		Short: "Print the effective rules and the layer of each",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			st, _, err := a.store()
			if err != nil {
				return err
			}
			m, err := a.loadMeta(st, args[0])
			if err != nil {
				return err
			}
			a.warnDrift(st, m)
			e := policy.Compile(st.Sources(m))
			return a.out.Print(e, func(w io.Writer) { printPolicy(w, e) })
		},
	})
	cmd.AddCommand(a.policySetCmd("allow", true), a.policySetCmd("deny", false), a.policyReloadCmd())
	return cmd
}

func (a *app) warnDrift(st sandbox.Store, m sandbox.Meta) {
	if d := st.Drift(m); len(d) > 0 {
		a.out.Warn("%s", sandbox.DriftWarning(m.Name, d))
	}
}

func (a *app) policyReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload <sandbox>",
		Short: "Re-read .podbox/policy.yaml and .sandboxignore from the project after review, and apply them",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			st, _, err := a.store()
			if err != nil {
				return err
			}
			m, err := a.loadMeta(st, args[0])
			if err != nil {
				return err
			}
			c, err := st.Snapshot(m)
			if err != nil {
				return err
			}
			e, err := st.RefreshPolicy(m)
			if err != nil {
				return err
			}
			c.Warnings = append(c.Warnings, e.Warnings...)
			for _, w := range c.Warnings {
				a.out.Warn("%s", w)
			}
			res := struct {
				Sandbox string `json:"sandbox"`
				sandbox.Changes
			}{m.Name, c}
			return a.out.Print(res, func(w io.Writer) {
				if c.Empty() {
					fmt.Fprintf(w, "%s: project files unchanged\n", m.Name)
					return
				}
				for _, l := range []struct {
					sign, what string
					items      []string
				}{{"+", "rule", c.RulesAdded}, {"-", "rule", c.RulesRemoved}, {"+", "ignore", c.IgnoreAdded}, {"-", "ignore", c.IgnoreRemoved}} {
					for _, it := range l.items {
						fmt.Fprintf(w, "%s %s %s\n", l.sign, l.what, it)
					}
				}
				fmt.Fprintf(w, "%s: policy applied", m.Name)
				if len(c.IgnoreAdded)+len(c.IgnoreRemoved) > 0 {
					fmt.Fprintf(w, "; masks apply at the next podbox stop + start")
				}
				fmt.Fprintln(w)
			})
		},
	}
}

func (a *app) policySetCmd(verb string, allow bool) *cobra.Command {
	var scope string
	c := &cobra.Command{
		Use:   verb + " <sandbox> <host>",
		Short: verb + " a host (exact or *.suffix); applies to the running sandbox within seconds",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			st, _, err := a.store()
			if err != nil {
				return err
			}
			m, err := a.loadMeta(st, args[0])
			if err != nil {
				return err
			}
			var paths []string
			switch scope {
			case policy.LayerSession:
				paths = []string{st.SessionPath(m.Name)}
			case policy.LayerProject:
				// The project file for the repository, the snapshot for enforcement.
				paths = []string{policy.ProjectFile(m.Workspace), st.ProjectPolicyPath(m.Name)}
			default:
				return output.Errorf(output.ExitUsage, "invalid --scope %q: want session or project", scope)
			}
			for _, p := range paths {
				if err := policy.SetRule(p, args[1], allow); err != nil {
					return output.Wrap(output.ExitUsage, err, verb)
				}
			}
			e, err := st.RefreshPolicy(m)
			if err != nil {
				return err
			}
			for _, w := range e.Warnings {
				a.out.Warn("%s", w)
			}
			d := e.Decide(args[1])
			res := struct {
				Sandbox string `json:"sandbox"`
				Host    string `json:"host"`
				Scope   string `json:"scope"`
				Allowed bool   `json:"allowed"`
				Rule    string `json:"rule"`
				Layer   string `json:"layer"`
			}{m.Name, policy.NormalizeHost(args[1]), scope, d.Allow, "", ""}
			res.Rule, res.Layer = d.Reason()
			return a.out.Print(res, func(w io.Writer) {
				state := "denied"
				if d.Allow {
					state = "allowed"
				}
				fmt.Fprintf(w, "%s: %s is now %s (decided by %s rule %s)\n", m.Name, res.Host, state, res.Layer, res.Rule)
			})
		},
	}
	c.Flags().StringVar(&scope, "scope", policy.LayerSession, "where to record the rule: session or project (.podbox/policy.yaml)")
	return c
}

func printPolicy(w io.Writer, e policy.Effective) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tDECISION\tLAYER\tPRIVATE")
	for _, r := range e.Rules {
		d := "deny"
		if r.Allow {
			d = "allow"
		}
		p := ""
		if r.AllowPrivate {
			p = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Host, d, r.Layer, p)
	}
	tw.Flush()
	fmt.Fprintln(w, "default: deny")
	for _, warn := range e.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
}
