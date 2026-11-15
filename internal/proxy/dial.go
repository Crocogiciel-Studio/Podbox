package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

type blockedError struct {
	ip      net.IP
	private bool
	reason  string
}

func (e *blockedError) Error() string { return fmt.Sprintf("%s is %s", e.ip, e.reason) }

var (
	metadataV6 = netip.MustParseAddr("fd00:ec2::254")
	alwaysNets = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.100.100.200/32"), // alibaba metadata
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}
	privateNets = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
		netip.MustParsePrefix("198.18.0.0/15"),
	}
)

// checkIP returns why ip must not be dialled, or nil.
func checkIP(ip net.IP, allowPrivate bool) *blockedError {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return &blockedError{ip: ip, reason: "an invalid address"}
	}
	a = a.Unmap()
	switch {
	case a.IsLoopback():
		return &blockedError{ip: ip, reason: "a loopback address"}
	case a == metadataV6 || a.IsLinkLocalUnicast():
		return &blockedError{ip: ip, reason: "a link-local or cloud metadata address"}
	case a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast():
		return &blockedError{ip: ip, reason: "an unspecified or multicast address"}
	}
	for _, p := range alwaysNets {
		if p.Contains(a) {
			return &blockedError{ip: ip, reason: "a reserved address"}
		}
	}
	if allowPrivate {
		return nil
	}
	if a.IsPrivate() {
		return &blockedError{ip: ip, private: true, reason: "a private address"}
	}
	for _, p := range privateNets {
		if p.Contains(a) {
			return &blockedError{ip: ip, private: true, reason: "a private address"}
		}
	}
	return nil
}

// resolve resolves rt.host once and checks every address. The first address
// is dialled, so a later DNS answer cannot rebind the connection. With an
// upstream proxy and no local resolution, the name is forwarded as is.
func (s *Server) resolve(ctx context.Context, rt route, allowPrivate bool) (route, error) {
	if ip := net.ParseIP(rt.host); ip != nil {
		if be := checkIP(ip, allowPrivate); be != nil {
			return rt, be
		}
		return s.finish(rt, ip), nil
	}
	if rt.host == "localhost" || strings.HasSuffix(rt.host, ".localhost") {
		return rt, &blockedError{ip: net.IPv4(127, 0, 0, 1), reason: "a loopback address"}
	}
	addrs, err := s.cfg.Resolver.LookupIPAddr(ctx, rt.host)
	if err != nil || len(addrs) == 0 {
		if s.cfg.Upstream != nil {
			rt.byName = true
			return rt, nil
		}
		if err == nil {
			err = fmt.Errorf("no address")
		}
		return rt, err
	}
	for _, a := range addrs {
		if be := checkIP(a.IP, allowPrivate); be != nil {
			return rt, be
		}
	}
	return s.finish(rt, addrs[0].IP), nil
}

// finish records the dial target; with an upstream the name is still sent
// so corporate proxies can apply their own host rules.
func (s *Server) finish(rt route, ip net.IP) route {
	rt.ip = ip.String()
	rt.addr = net.JoinHostPort(rt.ip, rt.port)
	rt.byName = s.cfg.Upstream != nil
	return rt
}
