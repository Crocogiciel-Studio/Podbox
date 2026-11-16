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

# blocked <description> <command...>: passes when the host-side command
# fails. For a probe inside a sandbox use denied: a sandbox that cannot run
# anything must not make a probe pass.
blocked() {
	local what=$1
	shift
	if "$@" >/dev/null 2>&1; then fail "$what (escape succeeded)"; else pass "$what"; fi
}

# attempt <sandbox> <shell snippet>: runs the snippet in the sandbox, output
# discarded. Returns 0 when it succeeded, 1 when it failed, 2 when the
# sandbox did not run it (not running, exec failure): the shell prints a
# sentinel after the snippet whatever its result.
attempt() {
	local out
	out=$("$PODBOX" exec "$1" -- sh -c "{ $2
} >/dev/null 2>&1 && echo podbox-e2e-succeeded; echo podbox-e2e-ran" </dev/null 2>/dev/null)
	case $out in
	*podbox-e2e-ran*) ;;
	*) return 2 ;;
	esac
	case $out in
	*podbox-e2e-succeeded*) return 0 ;;
	esac
	return 1
}

# denied <description> <sandbox> <shell snippet>: passes when the snippet
# ran in the sandbox and failed.
denied() {
	local what=$1
	attempt "$2" "$3"
	case $? in
	1) pass "$what" ;;
	0) fail "$what (escape succeeded)" ;;
	*) fail "$what (sandbox $2 did not run the probe)" ;;
	esac
}

# running <sandbox>: asserts the sandbox runs commands, before a group of
# negative probes.
running() { check "sandbox $1 is running" sx "$1" true; }

# restart <sandbox>: stop then start, stderr kept for the log on failure.
restart() {
	local err
	err=$("$PODBOX" stop "$1" 2>&1 >/dev/null) && err=$("$PODBOX" start "$1" 2>&1 >/dev/null) || {
		printf '%s\n' "$err" >&2
		return 1
	}
}

# up <sandbox> <workspace>: create and start.
up() {
	local err
	err=$("$PODBOX" create shell "$2" --name "$1" 2>&1 >/dev/null) && err=$("$PODBOX" start "$1" 2>&1 >/dev/null) || {
		printf '%s\n' "$err" >&2
		return 1
	}
}

# upclone <sandbox> <repository>: create a clone sandbox and start it.
upclone() {
	local err
	err=$("$PODBOX" create shell "$2" --name "$1" --clone 2>&1 >/dev/null) && err=$("$PODBOX" start "$1" 2>&1 >/dev/null) || {
		printf '%s\n' "$err" >&2
		return 1
	}
}

pb() { "$PODBOX" "$@"; }

# q <command...>: the command with stdout discarded (for check).
q() { "$@" >/dev/null; }

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
