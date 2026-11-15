package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/proxy"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

// logEntry is one decision as policy log prints it in JSON.
type logEntry struct {
	proxy.Entry
	Fix string `json:"fix,omitempty"`
}

// scanEntries calls each for every decision read from r, refusals only
// when denied is set.
func scanEntries(r io.Reader, sandboxName string, denied bool, each func(logEntry) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		e, ok := proxy.ParseEntry(sc.Bytes())
		if !ok || denied && !e.Denied() {
			continue
		}
		if err := each(logEntry{e, e.Fix(sandboxName)}); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (a *app) policyLogCmd() *cobra.Command {
	var denied, follow bool
	var since string
	c := &cobra.Command{
		Use:   "log <sandbox>",
		Short: "Print the proxy's decisions, with the command that allows each refused host",
		Long: "Print the proxy's decisions. HTTPS clients show a refusal as a bare 403; " +
			"this log names the host and the command that allows it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if since != "" {
				if _, err := time.ParseDuration(since); err != nil {
					return output.Errorf(output.ExitUsage, "invalid --since %q: want a duration such as 10m or 2h", since)
				}
			}
			m, err := a.manager(cmd.Context())
			if err != nil {
				return err
			}
			if follow {
				return a.followLog(cmd.Context(), m, name, since, denied)
			}
			var buf bytes.Buffer
			if err := m.ProxyLog(cmd.Context(), name, runtime.LogsSpec{Since: since, Stdout: &buf}); err != nil {
				return err
			}
			entries := []logEntry{}
			_ = scanEntries(&buf, name, denied, func(e logEntry) error { entries = append(entries, e); return nil })
			return a.out.Print(entries, func(w io.Writer) {
				for _, e := range entries {
					fmt.Fprintln(w, e.Line(name))
				}
			})
		},
	}
	f := c.Flags()
	f.BoolVar(&denied, "denied", false, "only refused requests")
	f.BoolVarP(&follow, "follow", "f", false, "keep printing new decisions (JSON: one object per line)")
	f.StringVar(&since, "since", "", "only decisions newer than this duration (10m, 2h)")
	return c
}

// followLog streams decisions until the proxy log ends or ctx is done.
func (a *app) followLog(ctx context.Context, m *sandbox.Manager, name, since string, denied bool) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := m.ProxyLog(ctx, name, runtime.LogsSpec{Follow: true, Since: since, Stdout: pw})
		pw.CloseWithError(err)
		done <- err
	}()
	enc := json.NewEncoder(a.out.Out)
	serr := scanEntries(pr, name, denied, func(e logEntry) error {
		if a.out.Mode == output.JSON {
			return enc.Encode(e)
		}
		_, err := fmt.Fprintln(a.out.Out, e.Line(name))
		return err
	})
	pr.Close() // unblock the writer if printing failed
	if err := <-done; err != nil {
		return err
	}
	return serr
}

// blockedNotice tells, after an agent session, how many requests the proxy
// refused since it began; HTTPS clients show the agent only a bare 403.
func (a *app) blockedNotice(ctx context.Context, m *sandbox.Manager, name string, start time.Time) {
	var buf bytes.Buffer
	if m.ProxyLog(ctx, name, runtime.LogsSpec{Since: start.UTC().Format(time.RFC3339), Stdout: &buf}) != nil {
		return
	}
	n := 0
	_ = scanEntries(&buf, name, true, func(logEntry) error { n++; return nil })
	if n > 0 {
		a.out.Info("podbox: %d request(s) blocked during this session — see: podbox policy log %s --denied", n, name)
	}
}
