---
status: pending
---

# Instruction: Sandbox lifecycle and workspace masking

## Architecture projection

```txt
.
└── internal/
    ├── agents/
    │   └── agents.go ✅
    ├── cli/
    │   ├── run.go ✅
    │   ├── create.go ✅
    │   ├── lifecycle.go ✅
    │   ├── ls.go ✅
    │   └── exec.go ✅
    ├── sandbox/
    │   ├── sandbox.go ✅
    │   ├── state.go ✅
    │   ├── network.go ✅
    │   ├── agentspec.go ✅
    │   └── sandbox_test.go ✅
    └── workspace/
        ├── mask.go ✅
        ├── sensitive.go ✅
        └── mask_test.go ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | temp project with `.env`, `.ssh/id_x`, `.git/hooks/pre-commit`, `src/` | — | system |
| Happy path | `podbox run shell <proj> --name t1` | interactive shell in the project path as uid 1000, files writable, owned by host user | cli |
| Happy path | `podbox ls --output json` | t1 with status, agent, workspace, proxy state | cli |
| Happy path | `podbox exec t1 -- id -u` | `1000`, inner exit code propagated | cli |
| Happy path | `podbox stop t1` then `podbox run shell <proj> --name t1` | same sandbox restarted, home volume kept | cli |
| Edge case | `.env` / `.ssh/` / `.git/hooks/` inside sandbox | empty file / empty read-only dirs | cli |
| Edge case | `.sandboxignore` lists `secrets/` | hidden | cli |
| Edge case | file looks like a secret but is not covered | warning at launch | cli |
| Edge case | cgroup delegation missing | sandbox starts without limits, warning printed | cli |
| Teardown | `podbox rm t1` | containers, internal network, state dir removed; home volume kept unless `--volumes` | cli |

## Tasks to do

### `1)` Agent table

> claude: image `claude`, command `claude --dangerously-skip-permissions`; kiro: image `kiro`, command `kiro-cli chat --trust-all-tools` (to confirm, phase 5 task 4); shell: `bash`.

### `2)` Sandbox state

> `<state>/sandboxes/<name>/{meta.json, policy.effective.json, masks/empty/}`; names default to `<agent>-<dir>`; all podman objects labelled `io.podbox.sandbox=<name>`.

### `3)` Network and proxy bring-up

> Shared `podbox-egress` network; per-sandbox `podbox-<name>-int` (`--internal --disable-dns`); proxy container on both, effective policy mounted read-only, upstream proxy from config or host `HTTPS_PROXY`; proxy IP read by inspect.

### `4)` Hardened agent container

> `--userns=keep-id:uid=1000,gid=1000`, `--user 1000:1000`, `--cap-drop=ALL`, `--security-opt no-new-privileges`, `--http-proxy=false`, `--dns 127.0.0.1`, `--add-host proxy:<ip>`, internal network only, `--tmpfs /tmp`, limits (cpus 4, memory 8g, pids 2048, configurable) when delegated; env `HTTPS_PROXY/HTTP_PROXY/https_proxy/http_proxy=http://proxy:3128`, `NO_PROXY=localhost,127.0.0.1`, `JAVA_TOOL_OPTIONS` proxy props; volume `podbox-home-<agent>` at `/home/agent`; project bound at the same absolute path (`/workspace` on Windows), `:z` when SELinux enforces.

### `5)` Workspace masking

> Forced patterns + `.sandboxignore` (gitignore syntax) resolved at start: files → `/dev/null` ro bind, directories → empty-dir ro bind; heuristic warning for uncovered secret-looking files (name + entropy).

### `6)` Observe real agent traffic

> Run a Claude login + one prompt through the proxy, read the decision log, and align Claude's default hosts with what was observed.

### `7)` Commands

> `run <agent> [path]` (create if absent, start, attach), `create`, `start`, `stop`, `rm [--volumes]`, `ls`, `exec <name> -- cmd`, `attach <name>`; all with `--output json` where non-interactive.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | `podbox run claude` starts Claude Code with skip-permissions as non-root |
| 2 | `ls` reflects state from labels even if the state dir is stale |
| 3 | proxy container reaches an allowed host; agent reaches only the proxy |
| 4 | `podman inspect` of the agent shows every hardening flag; no proxy env inherited from host |
| 5 | masked paths are empty inside, intact on the host |
| 6 | Claude's default policy lists every host seen in the decision log of a real login |
| 7 | lifecycle round trip leaves no podman object behind after `rm` |
