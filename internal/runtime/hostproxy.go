package runtime

import (
	"net/url"
	"strings"
)

// HostAlias names the host from inside containers and podman machine builds.
const HostAlias = "host.containers.internal"

// ToHost rewrites a proxy URL (scheme optional) whose host is loopback to
// HostAlias, keeping scheme, credentials and port, and reports whether it
// did. Any other value is returned unchanged.
func ToHost(v string) (string, bool) {
	raw := v
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return v, false
	}
	h := u.Hostname()
	if h != "localhost" && !strings.HasPrefix(h, "127.") && h != "::1" {
		return v, false
	}
	host := HostAlias
	if p := u.Port(); p != "" {
		host += ":" + p
	}
	u.Host = host
	if !strings.Contains(v, "://") {
		return strings.TrimPrefix(u.String(), "http://"), true
	}
	return u.String(), true
}
