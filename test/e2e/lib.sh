# Shared helpers for the e2e suites. Source it; needs podman, curl-free host, jq.
set -u

PODBOX=${PODBOX:-$(cd "$(dirname "$0")/../.." && pwd)/bin/podbox}
PASS=0
FAIL=0

# Isolate config and state from the developer's own sandboxes.
export PODBOX_HOME=${PODBOX_HOME:-$(mktemp -d)}

pass() { PASS=$((PASS + 1)); printf 'ok    %s\n' "$*"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL  %s\n' "$*"; }

# check <description> <command...>: passes when the command succeeds.
check() {
	local what=$1
	shift
	if "$@"; then pass "$what"; else fail "$what"; fi
}

# blocked <description> <command...>: passes when the command fails.
blocked() {
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then fail "$what (escape succeeded)"; else pass "$what"; fi
}

pb() { "$PODBOX" "$@"; }

# jqe <jq args...>: jq -e on stdin, quiet (a redirect on check would hide its result).
jqe() { jq -e "$@" >/dev/null; }

# sx <sandbox> <shell snippet>: run a snippet in the sandbox, stdin closed.
sx() {
	local name=$1
	shift
	"$PODBOX" exec "$name" -- sh -c "$*" </dev/null
}

# eventually <seconds> <command...>
eventually() {
	local deadline=$(($(date +%s) + $1))
	shift
	until "$@" >/dev/null 2>&1; do
		[ "$(date +%s)" -ge "$deadline" ] && return 1
		sleep 0.5
	done
}

no_objects_left() {
	[ -z "$(podman ps -a -q --filter label=io.podbox.sandbox)" ] &&
		[ -z "$(podman network ls -q --filter label=io.podbox.sandbox)" ] &&
		! podman network exists podbox-egress
}

summary() {
	printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
	[ "$FAIL" -eq 0 ]
}
