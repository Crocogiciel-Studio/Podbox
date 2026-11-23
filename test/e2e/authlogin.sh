#!/usr/bin/env bash
# podbox auth login: the opener records the URL, podbox relays the browser's
# callback from the host loopback into the sandbox, and nothing listens after.
# A fake login (node, in the workspace) stands in for kiro-cli: it opens a
# sign-in URL whose redirect_uri is a loopback port, then answers the callback.
. "$(dirname "$0")/lib.sh"

NAME=e2e-auth
PROJ=$(mktemp -d)
OUT=$(mktemp -d)

cleanup() {
	[ -n "${HOLDER:-}" ] && kill "$HOLDER" 2>/dev/null
	pb rm "$NAME" --volumes >/dev/null 2>&1
	rm -rf "$PROJ" "$OUT" "$PODBOX_HOME"
}
trap cleanup EXIT

free_port() {
	local p
	for _ in $(seq 50); do
		p=$((20000 + RANDOM % 20000))
		ss -ltn "sport = :$p" | grep -q LISTEN || {
			echo "$p"
			return 0
		}
	done
	return 1
}

cat >"$PROJ/fake-login.js" <<'EOF'
const http = require("http"), { execFileSync } = require("child_process");
const port = +process.argv[2];
const s = http.createServer((q, r) => {
  const code = new URL(q.url, "http://x").searchParams.get("code");
  r.writeHead(302, { Location: "https://example.org/done" });
  r.end();
  console.log("got " + code);
  s.close();
});
s.listen(port, "127.0.0.1", () =>
  execFileSync("xdg-open", ["https://example.org/signin?state=s&redirect_uri=" + encodeURIComponent("http://localhost:" + port + "/cb")]));
EOF

pb image build proxy shell >/dev/null || { echo "image build failed"; exit 1; }
up "$NAME" "$PROJ" || { echo "sandbox failed"; exit 1; }
running "$NAME"

echo "== opener"
check "xdg-open is podbox-open, root-owned" sx "$NAME" 'test "$(readlink /usr/local/bin/xdg-open)" = podbox-open && test "$(stat -c %u /usr/local/bin/podbox-open)" = 0'
denied "the agent cannot replace the opener" "$NAME" 'echo x > /usr/local/bin/podbox-open'
sx "$NAME" "xdg-open 'https://example.org/a?b=1' >/dev/null 2>&1"
check "the opener records the URL" test "$(sx "$NAME" 'cat /tmp/podbox-open/urls')" = "https://example.org/a?b=1"

echo "== relay"
PORT=$(free_port) || { echo "no free port"; exit 1; }
pb -o json auth login "$NAME" -- node "$PROJ/fake-login.js" "$PORT" >"$OUT/stdout" 2>"$OUT/stderr" </dev/null &
PID=$!
check "the relay listens on the host loopback" eventually 30 curl -s -o /dev/null "http://127.0.0.1:$PORT/favicon.ico"
check "only on loopback" sh -c "! ss -ltnH 'sport = :$PORT' | grep -Ev '127\.0\.0\.1|\[::1\]' | grep -q ."
check "POST is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$PORT/cb")" = 405
check "a foreign Host is refused" test "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.example' "http://127.0.0.1:$PORT/cb?code=x")" = 403
check "another path is refused" test "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/other")" = 404
check "the browser is sent where the login answered" test "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://localhost:$PORT/cb?code=abc123&state=s")" = "302 https://example.org/done"
wait "$PID"
check "auth login exits with the login's code" test $? -eq 0
check "the login got the code" grep -q 'got abc123' "$OUT/stdout"
check "the JSON summary counts one callback" sh -c "sed -n '/^{/,\$p' '$OUT/stdout' | jq -e '.callbacks == 1 and .urls == 1 and .exit_code == 0' >/dev/null"
check "no output holds the query" sh -c "! grep -q 'code=abc123' '$OUT/stdout' '$OUT/stderr'"
check "nothing listens after the login" sh -c "! curl -s -o /dev/null 'http://127.0.0.1:$PORT/'"

echo "== busy port"
PORT=$(free_port) || { echo "no free port"; exit 1; }
python3 -c "import socket,time; s=socket.socket(); s.bind(('127.0.0.1', $PORT)); s.listen(); time.sleep(120)" &
HOLDER=$!
eventually 10 sh -c "ss -ltnH 'sport = :$PORT' | grep -q ."
err=$(timeout 60 "$PODBOX" auth login "$NAME" -- node "$PROJ/fake-login.js" "$PORT" 2>&1 </dev/null >/dev/null)
check "a port held on the host fails the login" test $? -eq 1
check "the error names the port" grep -q "port $PORT is in use" <<<"$err"
kill "$HOLDER" 2>/dev/null
HOLDER=

echo "== usage"
pb auth login "$NAME" >/dev/null 2>&1 </dev/null
check "an agent with no login command needs one" test $? -eq 2

echo "== teardown"
check "rm --volumes" pb rm "$NAME" --volumes
check "nothing left behind" no_objects_left

summary
