---
status: pending
---

# Instruction: Evasion suite, e2e CI, docs

## Architecture projection

```txt
.
├── .github/
│   └── workflows/
│       └── ci.yml ✏️
├── README.md ✏️
├── docs/
│   ├── security-model.md ✅
│   └── policy.md ✅
└── test/
    └── e2e/
        ├── evasion.sh ✅
        └── lifecycle.sh ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | build podbox, `podbox image build proxy shell`, fixture project with `.env` and fake `~/.ssh` | sandbox `e2e` running | system |
| Happy path | curl an allowed host through the proxy | HTTP 2xx/3xx/4xx from the origin | cli |
| Edge case | direct IP egress, external DNS, host loopback service, host gateway | all fail | cli |
| Edge case | read host `~/.ssh`, project `.env`, `.git/hooks` | absent or empty | cli |
| Edge case | find a podman/docker socket anywhere | none | cli |
| Edge case | non-allowlisted host | 403 with readable body; after `policy allow`, success without restart | cli |
| Edge case | capabilities, uid, no_new_privs | CapEff 0, uid 1000, NoNewPrivs 1 | cli |
| Teardown | `podbox rm e2e --volumes` | no labelled podman objects left | cli |

## Tasks to do

### `1)` Evasion script

> `test/e2e/evasion.sh` asserts every Done-when isolation condition, curl only, non-zero exit on any escape.

### `2)` Lifecycle script

> create / ls / exec / stop / start / rm round trip with JSON assertions.

### `3)` CI e2e job

> Ubuntu runner, install podman, run `podbox doctor` and print it, then run the scripts on every push and PR.

### `3b)` Corporate proxy and auth persistence e2e

> `evasion.sh` also runs with an upstream proxy (a podbox proxy on the runner, allow-all, basic auth) and checks an allowed host still answers; `lifecycle.sh` writes a marker in the agent home, recreates the sandbox and finds it again.

### `4)` Documentation

> README quick start (install, doctor, run claude/kiro, policy allow), security model and known limits (shared kernel, allowlisted hosts as exfiltration channel, corporate CA visible to agent), policy reference, exit-code table.

### `5)` Contributor checks

> `make lint test` and `make e2e` pass before the pull request.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | `evasion.sh` exits 0 locally with every check reported blocked |
| 2 | `lifecycle.sh` exits 0 and leaves nothing behind |
| 3 | CI workflow defines the e2e job running doctor and both scripts |
| 3b | allowed host reachable through the upstream; home marker survives `rm` + `run` |
| 4 | README walks from install to a running agent |
| 5 | contributor checks pass |
