package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

// EgressNetwork is shared by every proxy container; agents never join it.
const EgressNetwork = "podbox-egress"

// ProxyPort is where the sandbox proxy listens.
const ProxyPort = "3128"

func internalNetwork(name string) string { return "podbox-" + name + "-int" }

func ensureNetwork(ctx context.Context, rt runtime.Runtime, spec runtime.NetworkSpec) (runtime.Network, error) {
	n, err := rt.NetworkInspect(ctx, spec.Name)
	if err == nil {
		return n, nil
	}
	if !errors.Is(err, runtime.ErrNotFound) {
		return n, err
	}
	if err := rt.NetworkCreate(ctx, spec); err != nil {
		return n, err
	}
	return rt.NetworkInspect(ctx, spec.Name)
}

// proxyIP picks a fixed address in the internal subnet so the agent's
// /etc/hosts entry stays valid across restarts.
func proxyIP(n runtime.Network) (string, error) {
	for _, s := range n.Subnets {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() {
			continue
		}
		return p.Masked().Addr().Next().Next().String(), nil
	}
	return "", fmt.Errorf("network %s has no IPv4 subnet", n.Name)
}

// Upstream returns the parent proxy for the sandbox proxy: config first,
// then the host proxy variables. Host loopback addresses are rewritten to
// the host as seen from containers.
func Upstream(configured string, getenv func(string) string) string {
	v := configured
	if v == "none" {
		return ""
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v != "" {
			break
		}
		v = getenv(k)
	}
	if v == "" {
		return ""
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return v
	}
	switch h := u.Hostname(); {
	case h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1":
		host := "host.containers.internal"
		if p := u.Port(); p != "" {
			host += ":" + p
		}
		u.Host = host
	}
	return u.String()
}
