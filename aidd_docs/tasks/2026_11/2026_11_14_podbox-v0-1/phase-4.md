---
status: pending
---

# Instruction: Allowlisting egress proxy

## Architecture projection

```txt
.
└── internal/
    ├── cli/
    │   └── proxy.go ✅
    └── proxy/
        ├── server.go ✅
        ├── connect.go ✅
        ├── forward.go ✅
        ├── upstream.go ✅
        ├── dial.go ✅
        ├── reload.go ✅
        ├── blocked.go ✅
        └── proxy_test.go ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | proxy on a random port with a temp effective policy; local TLS and HTTP test servers | listening | system |
| Happy path | CONNECT to an allowed host | 200 Connection established, bytes tunnelled | api |
| Happy path | plain `GET http://allowed/` | forwarded response | api |
| Edge case | CONNECT to a host not allowed | 403 with the agent-readable body naming the host and `podbox policy allow` | api |
| Edge case | allowed name resolving to 127.0.0.1 / 169.254.169.254 | 403 "blocked destination" | api |
| Edge case | allowed name resolving to 10.x without `allow_private` | 403; with it, 200 | api |
| Edge case | policy file rewritten while running | next request follows the new policy within 2 s | api |
| Edge case | upstream proxy configured (with basic auth) | CONNECT chained through upstream; agent-facing response unchanged | api |
| Edge case | policy file missing or corrupt at reload | previous policy kept if valid, else deny all | api |

## Tasks to do

### `1)` `podbox proxy serve` (hidden command)

> Standard-library HTTP proxy: CONNECT tunnels and absolute-URI HTTP forwarding.

1. Flags: `--listen :3128`, `--policy <file>`, `--upstream <url>` (also `PODBOX_UPSTREAM_PROXY`), `--upstream-ca <file>`, `--log json`.
2. One JSON log line per decision (time, host, port, method, decision, rule, layer).

### `2)` Destination guard

> Resolve once, check the IP, dial that IP (anti DNS-rebinding).

1. Always block loopback, link-local, metadata (`169.254.169.254`, `fd00:ec2::254`), unspecified, multicast; block RFC1918/ULA unless the matched rule has `allow_private`.
2. With an upstream proxy: still resolve locally and apply the guard; only if local resolution fails, forward by name to the upstream (IP literals and `localhost` always checked locally).

### `3)` Hot reload

> Poll the effective policy file (mtime + size) every second; swap atomically.

### `4)` Agent-readable refusal

> Body: `Blocked by podbox network policy: host "<h>" is not allowed.` + `Ask the user to run: podbox policy allow <sandbox> <h>`; header `X-Podbox-Blocked: 1`.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | integration tests tunnel TLS and forward HTTP through the proxy |
| 2 | tests block loopback/metadata always and private ranges without opt-in |
| 3 | test rewrites the policy and observes the change without restart |
| 4 | 403 body matches the documented text |
