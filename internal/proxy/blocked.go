package proxy

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
)

// BlockedHeader marks responses produced by a podbox refusal.
const BlockedHeader = "X-Podbox-Blocked"

// NotAllowedBody is the agent-readable refusal for a host and port outside
// the allowlist.
func NotAllowedBody(sandbox, host string, port int) string {
	if slices.Contains(policy.DefaultPorts, port) {
		return fmt.Sprintf("Blocked by podbox network policy: host %q is not allowed.\nAsk the user to run: podbox policy allow %s %s\n", host, sandbox, host)
	}
	return fmt.Sprintf("Blocked by podbox network policy: host %q is not allowed on port %d.\nAsk the user to run: podbox policy allow %s %s --port %d\n", host, port, sandbox, host, port)
}

func writeNotAllowed(w http.ResponseWriter, sandbox, host string, port int) {
	writeBlocked(w, NotAllowedBody(sandbox, host, port))
}

func writeBlockedDestination(w http.ResponseWriter, host string, be *blockedError) {
	body := fmt.Sprintf("Blocked by podbox network policy: host %q resolves to a blocked destination (%s).\n", host, be)
	if be.private {
		body += "To reach an internal service, ask the user to add allow_private: true to its rule in .podbox/policy.yaml.\n"
	}
	writeBlocked(w, body)
}

func writeBlocked(w http.ResponseWriter, body string) {
	w.Header().Set(BlockedHeader, "1")
	writeError(w, http.StatusForbidden, body)
}

func writeError(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Connection", "close")
	w.WriteHeader(code)
	_, _ = fmt.Fprint(w, body)
}
