package policy

import (
	"fmt"
	"net"
	"strings"
)

// NormalizeHost lowercases, drops a port and a trailing dot.
func NormalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// Match reports whether host matches pattern: exact, or "*.suffix" for any
// subdomain of suffix (not suffix itself).
func Match(pattern, host string) bool {
	p := NormalizeHost(pattern)
	if suffix, ok := strings.CutPrefix(p, "*."); ok {
		return strings.HasSuffix(host, "."+suffix)
	}
	return p == host
}

// ValidatePattern rejects patterns the matcher cannot honour.
func ValidatePattern(p string) error {
	n := NormalizeHost(p)
	if n == "" {
		return fmt.Errorf("empty host")
	}
	body := strings.TrimPrefix(n, "*.")
	if body == "" || strings.ContainsAny(body, "*/ ") {
		return fmt.Errorf("invalid host pattern %q: use an exact host or *.suffix", p)
	}
	if strings.Contains(n, "*") && !strings.HasPrefix(n, "*.") {
		return fmt.Errorf("invalid host pattern %q: wildcard only as leading *.", p)
	}
	return nil
}
