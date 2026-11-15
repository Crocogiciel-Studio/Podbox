---
status: done
---

# Instruction: Podman runtime and doctor

## Architecture projection

```txt
.
└── internal/
    ├── cli/
    │   └── doctor.go ✅
    ├── doctor/
    │   ├── checks.go ✅
    │   └── checks_test.go ✅
    └── runtime/
        ├── runtime.go ✅
        ├── podman.go ✅
        ├── podman_test.go ✅
        └── exec.go ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | fake `podman` executable on PATH in unit tests | commands are recorded | system |
| Happy path | `Runtime.Run(spec)` | expected `podman run` argv, flags in a deterministic order | system |
| Happy path | `podbox doctor` on this host | each check ok/warn/fail with a remediation line; exit 0 when no fail | cli |
| Edge case | `podman` absent from PATH | doctor reports fail with install hint, exit 3 | cli |
| Edge case | rootful podman | doctor fail "rootless required" | cli |
| Edge case | cgroup controllers lack cpu/memory/pids | doctor warn "resource limits unavailable" | cli |

## Tasks to do

### `1)` Runtime interface

> Lifecycle operations independent of the engine.

1. Interface: networks (create/inspect/remove), images (exists/build), volumes (create/remove), containers (run/start/stop/remove/inspect/list by label/exec/attach), `Info()`.
2. Typed specs (`ContainerSpec`, `NetworkSpec`, `Mount`) — no podman flag leaks into callers.

### `2)` Podman implementation over the CLI

> Build argv from specs, run `podman`, parse JSON output.

1. Locate binary (`PODBOX_PODMAN`, then PATH); on macOS/Windows talk to the default podman machine transparently.
2. Interactive exec/attach wire stdin/stdout/stderr and a TTY; return the inner exit code.
3. Unit tests against a fake podman script asserting argv.

### `3)` Doctor

> Explain every missing prerequisite with a fix.

1. Checks: podman present and version ≥ 4.4, rootless, subuid/subgid entries, `/dev/net/tun` (Linux), network backend netavark, cgroup v2 delegated controllers, SELinux state, podman machine running (macOS/Windows), storage driver.
2. Text table and JSON array output; exit 3 when any check fails.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | callers compile against the interface only |
| 2 | argv unit tests pass; inner exit codes propagate |
| 3 | `podbox doctor --output json` on this host lists every check with status |
