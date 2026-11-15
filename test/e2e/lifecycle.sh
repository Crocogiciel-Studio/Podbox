#!/usr/bin/env bash
# create / ls / exec / stop / start / rm round trip with JSON assertions, and
# agent home persistence across sandbox recreation.
. "$(dirname "$0")/lib.sh"

NAME=e2e-lc
PROJ=$(mktemp -d)

cleanup() {
	pb rm "$NAME" --volumes >/dev/null 2>&1
	rm -rf "$PROJ" "$PODBOX_HOME"
}
trap cleanup EXIT

field() { pb ls -o json | jq -r --arg n "$NAME" ".[] | select(.name == \$n) | .$1"; }

pb image build proxy shell >/dev/null || { echo "image build failed"; exit 1; }

echo "== create"
out=$(pb create shell "$PROJ" --name "$NAME" -o json 2>/dev/null)
check "create prints JSON" test "$(jq -r .action <<<"$out")" = created
check "ls shows created state" test "$(field state)" = created
check "ls shows agent and workspace" test "$(field agent):$(field workspace)" = "shell:$PROJ"
pb create shell "$PROJ" --name "$NAME" >/dev/null 2>&1
check "second create is refused with exit 1" test $? -eq 1

echo "== start / exec"
check "start" pb start "$NAME" -o json
check "ls shows running agent and proxy" test "$(field state):$(field proxy)" = running:running
check "exec runs as uid 1000" test "$(pb exec "$NAME" -- id -u </dev/null)" = 1000
pb exec "$NAME" -- sh -c 'exit 42' </dev/null
check "exec propagates the inner exit code" test $? -eq 42
check "exec runs in the workspace" test "$(pb exec "$NAME" -- pwd </dev/null)" = "$PROJ"

echo "== stop / start keeps the home"
pb exec "$NAME" -- sh -c 'echo persisted >~/marker' </dev/null
check "stop" pb stop "$NAME"
check "ls shows exited" test "$(field state)" = exited
pb exec "$NAME" -- true </dev/null 2>/dev/null
check "exec on a stopped sandbox fails with exit 1" test $? -eq 1
check "restart" pb start "$NAME"
check "home marker survives stop/start" test "$(pb exec "$NAME" -- cat /home/agent/marker </dev/null)" = persisted

echo "== rm and recreate keeps the home volume"
check "rm" pb rm "$NAME"
check "no labelled podman object left after rm" no_objects_left
check "home volume kept without --volumes" podman volume exists podbox-home-shell
pb create shell "$PROJ" --name "$NAME" >/dev/null 2>&1 && pb start "$NAME" >/dev/null 2>&1
check "home marker survives rm + create" test "$(pb exec "$NAME" -- cat /home/agent/marker </dev/null)" = persisted

echo "== errors"
out=$(pb stop no-such-sandbox -o json)
code=$?
check "unknown sandbox exits 4" test "$code" -eq 4
check "unknown sandbox JSON error" test "$(jq -r .error.kind <<<"$out")" = not_found
pb run nosuchagent "$PROJ" >/dev/null 2>&1
check "unknown agent exits 2" test $? -eq 2

echo "== teardown"
check "rm --volumes" pb rm "$NAME" --volumes
check "nothing left behind" no_objects_left
check "home volume removed" sh -c '! podman volume exists podbox-home-shell'

summary
