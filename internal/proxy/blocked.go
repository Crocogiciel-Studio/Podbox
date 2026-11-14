package proxy

import (
	"fmt"
	"net/http"
)

// BlockedHeader marks responses produced by a podbox refusal.
const BlockedHeader = "X-Podbox-Blocked"

// NotAllowedBody is the agent-readable refusal for a host outside the allowlist.
func NotAllowedBody(sandbox, host string) string {
	return fmt.Sprintf("Blocked by podbox network policy: host %q is not allowed.\nAsk the user to run: podbox policy allow %s %s\n", host, sandbox, host)
}

func writeNotAllowed(w http.ResponseWriter, sandbox, host string) {
	writeBlocked(w, NotAllowedBody(sandbox, host))
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
