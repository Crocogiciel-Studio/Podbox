package cli

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/proxy"
)

func (a *app) proxyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "proxy", Short: "Egress proxy (runs inside the sandbox proxy container)", Hidden: true}
	var listen, policyPath, upstream, upstreamCA, logFormat, auth string
	var allowAll bool
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the allowlisting HTTP proxy",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if logFormat != "json" {
				return output.Errorf(output.ExitUsage, "--log supports only json")
			}
			if policyPath == "" && !allowAll {
				return output.Errorf(output.ExitUsage, "--policy is required")
			}
			if upstream == "" {
				upstream = os.Getenv("PODBOX_UPSTREAM_PROXY")
			}
			if auth == "" {
				auth = os.Getenv("PODBOX_PROXY_AUTH")
			}
			cfg := proxy.Config{PolicyPath: policyPath, AllowAll: allowAll, Auth: auth, Log: os.Stdout}
			if upstream != "" {
				u, err := proxy.ParseUpstream(upstream)
				if err != nil {
					return output.Wrap(output.ExitUsage, err, "--upstream")
				}
				cfg.Upstream = u
			}
			if upstreamCA != "" {
				pool, err := proxy.LoadCA(upstreamCA)
				if err != nil {
					return output.Wrap(output.ExitUsage, err, "--upstream-ca")
				}
				cfg.UpstreamCA = pool
			}
			return serveProxy(listen, cfg)
		},
	}
	f := serve.Flags()
	f.StringVar(&listen, "listen", ":3128", "listen address")
	f.StringVar(&policyPath, "policy", "", "effective policy file, polled every second")
	f.StringVar(&upstream, "upstream", "", "parent proxy URL (default $PODBOX_UPSTREAM_PROXY)")
	f.StringVar(&upstreamCA, "upstream-ca", "", "PEM bundle trusted for an https:// upstream")
	f.StringVar(&logFormat, "log", "json", "decision log format (json)")
	f.BoolVar(&allowAll, "allow-all", false, "allow every destination (test upstream only)")
	f.StringVar(&auth, "auth", "", "require basic auth user:pass from clients (default $PODBOX_PROXY_AUTH)")
	cmd.AddCommand(serve)
	return cmd
}

func serveProxy(listen string, cfg proxy.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := proxy.New(cfg)
	go s.Watch(ctx)
	srv := &http.Server{Addr: listen, Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return output.Wrap(output.ExitRuntime, err, "proxy")
	}
	return nil
}
