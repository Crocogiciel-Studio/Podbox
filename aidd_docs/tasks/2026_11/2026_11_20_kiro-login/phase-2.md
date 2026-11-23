---
status: implemented
---

# Instruction: `podbox auth login`

Implemented. Differences from the tasks below: a redirect_uri with no path (the portal's `http://localhost:3128`) accepts any callback path but `/favicon.ico`, since the portal appends `/signin/callback`; a callback whose `issuer_url` host the policy refuses is held (fixed page, `needs_host` event with the `podbox policy allow` command) and relayed on reload; URLs are shown by `podbox-open`, not reprinted. Pending: `make e2e` on Linux and a real login with the built binary.

## Architecture projection

```txt
.
├── .github/workflows/ci.yml           ✏️  "Auth login suite" step
├── CONTRIBUTING.md                    ✏️  e2e line
├── Makefile                           ✏️  e2e target runs authlogin.sh
├── README.md                          ✏️
├── docs/security-model.md             ✏️  "Login relay" section
├── internal/
│   ├── agents/agents.go               ✏️  Agent.Login
│   ├── sandbox/
│   │   ├── login.go                   ✅  URL watch + loopback relay
│   │   └── login_test.go              ✅
│   └── cli/
│       ├── auth.go                    ✅  auth login command
│       ├── root.go                    ✏️  register, classify ErrNoLogin / ErrPortBusy
│       └── exitcode_test.go           ✏️
└── test/e2e/authlogin.sh              ✅  100755
```

## Command surface

```sh
podbox auth login <sandbox> [--timeout 10m] [-- <command>...]
```

- Default command: the agent's `Login` (`kiro-cli login` for kiro); none for claude and shell, which need `-- <command>`.
- Text: info lines for each URL to open and each callback delivered; the login command keeps the terminal.
- JSON (`-o json`): at the end, `{"name","command","urls","callbacks","exit_code"}`; no URL, code or state in it.
- Exit: the login command's code; 2 no login command; 4 unknown sandbox; 1 not running or a callback port busy on the host.

## Tasks to do

### `1)` Agent login command

1. Add `Login []string` to `agents.Agent`; set `{"kiro-cli", "login"}` for kiro.

### `2)` URL watch

> `sandbox/login.go`, `Manager.Login(ctx, name, LoginOptions{Command, Timeout}, tio IO, events func(LoginEvent)) (int, error)`.

1. Load the meta, `requireRunning`, resolve the command (override, else `Agent.Login`, else `ErrNoLogin`).
2. Reset the URL file with `runIn`: `rm -f /tmp/podbox-open/urls`.
3. Start the command with `m.Exec` in a goroutine, on the caller's terminal; derive a context bounded by `Timeout`.
4. Every 500 ms read new lines of `/tmp/podbox-open/urls` with `runIn` (`tail -c +<offset+1>`, capped by `capWriter`). For each line: parse as an absolute `https` URL (else ignore with a warning), emit `LoginEvent{Kind: "url"}` with the URL cleaned by `termsafe`, then read its `redirect_uri` query value.
5. When `redirect_uri` is `http://localhost:<port>[/path]` or `http://127.0.0.1:<port>[/path]`, start one relay for that port (task 3) unless already running. Any other scheme or host: warn that the callback cannot be relayed.
6. When the command exits or the context ends, close every listener and return the exit code.

### `3)` Loopback relay

1. Listen on `127.0.0.1:<port>` (required: failure is `ErrPortBusy`, message `port N is in use on this machine; find it with: lsof -nP -iTCP:N -sTCP:LISTEN`) and `[::1]:<port>` (best effort: browsers may resolve `localhost` to either).
2. Accept only: method `GET` (405 otherwise), path equal to the redirect path (`/` when none; 404 otherwise), `Host` header equal to `localhost:<port>` or `127.0.0.1:<port>` (403 otherwise, against DNS rebinding). One request at a time.
3. Deliver with `runInto`, URL on stdin so it never appears in `ps`:

```sh
curl -sS --noproxy '*' --max-time 30 -o /dev/null -w '%{http_code}\n%{redirect_url}' --config -
# stdin: url = "http://<redirect host>:<port><path>?<raw query>"   (\ and " escaped)
```

4. Answer the browser: when curl reports a 3xx and an `https` `redirect_url`, `302` to it; otherwise `200` with a fixed page "Login callback delivered to sandbox <name>. You can close this tab." A failed delivery is `502` with a fixed page; the detail goes to the terminal. Never reflect the query.
5. Emit `LoginEvent{Kind: "callback", Status}`; never log the path or the query.

### `4)` CLI

1. `cli/auth.go`: `auth` group with `login`, flags `--timeout` (default 10m); `--` splits the override command, as in `exec`; JSON summary at the end.
2. `root.go`: register `a.authCmd()`; `classify`: `ErrNoLogin` → 2, `ErrPortBusy` → 1.
3. Ctrl-C closes the listeners before exiting (same path as `interrupt_test.go`).

### `5)` Unit tests (`login_test.go`, fake `runtime.Runtime` as in `inspect_test.go`)

1. A URL with `redirect_uri=http://localhost:<free port>/cb` starts a relay; a GET on it runs curl with the URL on stdin and none in argv, and the browser gets `302` to the `https` Location curl reported.
2. POST → 405, other path → 404, `Host: evil.example` → 403, `http://` Location from the sandbox → 200 page, not a redirect.
3. Port held by a test listener → `ErrPortBusy`.
4. A `redirect_uri=kiro://...` → warning, no listener.
5. The command exits → listeners closed (dial fails).
6. No event, warning or error text holds the query string.
7. `exitcode_test.go`: `ErrNoLogin` → 2, `ErrPortBusy` → 1.

### `6)` e2e suite `test/e2e/authlogin.sh` (100755)

A shell sandbox and a fake login written into the test workspace, so no Kiro account is needed:

```sh
# fake-login.js, run in the sandbox: opener + local callback server, like kiro-cli
node -e '
const http=require("http"),{execFileSync}=require("child_process");const port=+process.argv[1];
const s=http.createServer((q,r)=>{r.writeHead(302,{Location:"https://example.org/done"});r.end();
  console.log("got", new URL(q.url,"http://x").searchParams.get("code"));s.close();});
s.listen(port,"127.0.0.1",()=>execFileSync("xdg-open",["https://example.org/signin?redirect_uri="+encodeURIComponent("http://localhost:"+port+"/cb")]));
' "$PORT"
```

Scenarios:

| Action | Observable outcome |
| ------ | ------------------ |
| `podbox auth login "$N" -- node fake-login.js` in the background; host `curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://127.0.0.1:$PORT/cb?code=abc"` | `302 https://example.org/done`; podbox exits 0; its output has `got abc` and no `code=abc` |
| same, then `curl -X POST`, `/other`, `-H 'Host: evil.example'` | 405, 404, 403 |
| a host process listening on `$PORT` first | `auth login` exits 1 naming the port |
| `podbox auth login "$N"` on a shell sandbox | exit 2 |
| after exit | nothing listens on `$PORT` |

Add the suite to the `Makefile` `e2e` target and a CI step.

### `7)` Documentation

1. README: `auth login` in the commands table; a "Kiro login" paragraph (external IdP orgs; the flow; open the printed URL; podbox never opens it; the token lives in the agent home volume; `podbox rm --volumes` drops it); known limits (needs a `localhost` redirect; one login at a time per port; on timeout the login process in the sandbox may linger until `stop`).
2. `docs/security-model.md`: "Login relay": what is listened on, for how long, what is filtered, why no port is published, why URLs are printed not opened, where the token ends up.
3. `CONTRIBUTING.md`: e2e line names the auth login suite.

## Commands

```sh
make lint test
make cross-embed
sudo install dist/podbox-darwin-arm64 /usr/local/bin/podbox

# real login (Mac)
podbox auth login kiro-brain-system         # open each printed URL in the Mac browser
podbox exec kiro-brain-system -- kiro-cli whoami
podbox run kiro                             # no login prompt

# relay checks (Mac, during a login)
lsof -nP -iTCP -sTCP:LISTEN | grep podbox   # only 127.0.0.1 / [::1] on the callback port
lsof -nP -iTCP -sTCP:LISTEN | grep podbox   # after exit: nothing

# Linux CI box
make e2e
git ls-files -s test/e2e                    # every script 100755
```

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | kiro has a login command, claude and shell do not |
| 2, 3 | unit tests of task 5 pass; a real Entra login completes on the Mac (phase 0 account) |
| 4 | exit codes as documented; JSON summary holds no URL |
| 6 | `test/e2e/authlogin.sh` prints no FAIL and CI runs it |
| 7 | README and security model describe the relay and its limits |
