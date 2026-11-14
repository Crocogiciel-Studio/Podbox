---
objective: "podbox runs Claude Code or Kiro autonomously in a hardened rootless Podman sandbox whose only egress is an allowlisting proxy, with every evasion test failing in Linux CI."
status: in-progress
---

# Plan: Podbox M0 + v0.1 — closed-network sandbox

Source: [spec.md](./spec.md)

## Phases

| #   | Phase                                   | File                         | Depends on |
| --- | --------------------------------------- | ---------------------------- | ---------- |
| 1   | Go skeleton, output contract, CI        | [`phase-1.md`](./phase-1.md) | none       |
| 2   | Podman runtime and doctor               | [`phase-2.md`](./phase-2.md) | 1          |
| 3   | Network policy model and policy command | [`phase-3.md`](./phase-3.md) | 1          |
| 4   | Allowlisting egress proxy               | [`phase-4.md`](./phase-4.md) | 3          |
| 5   | Images: proxy, base, Claude, Kiro       | [`phase-5.md`](./phase-5.md) | 2, 4       |
| 6   | Sandbox lifecycle and workspace masking | [`phase-6.md`](./phase-6.md) | 2, 3, 4, 5 |
| 7   | Evasion suite, e2e CI, docs             | [`phase-7.md`](./phase-7.md) | 6          |

## External sources

| URL | What it settled |
| --- | --- |
| https://docs.podman.io/en/v5.7.0/markdown/podman-run.1.html (checked via local `podman run --help` / `podman network create --help`, Podman 5.7) | `--internal`, `--disable-dns`, `--http-proxy` (default true), `--userns`, `--add-host`, `--dns`, `--pids-limit` exist |
| https://kiro.dev/docs/cli/ (cited by the feasibility study, not fetched: host blocked here) | Kiro installer `cli.kiro.dev/install`, Kiro auth under `~/.local/share/kiro-cli`, prior-art patterns (masking `.git/hooks/`, forced sensitive patterns) |

## Impact

| Category | Verdict and evidence | Handled in |
| --- | --- | --- |
| runtime: rootless Podman | observed: Podman 5.7 rootless runs on Ubuntu 26.04 here once `/dev/net/tun` exists; vfs storage needed without `/dev/fuse` | 2 (doctor checks tun, storage, subuid) |
| runtime: network topology | observed: `--internal --disable-dns` network + `--http-proxy=false --dns 127.0.0.1` gives no direct egress (curl rc 7), no DNS (rc 28), no host loopback nor gateway (rc 7); proxy container on internal + egress networks reaches the internet through the upstream proxy (HTTP 200) | 4, 6 |
| runtime: host proxy env leak | observed: Podman forwards host `*_PROXY` variables into containers by default | 6 (`--http-proxy=false` on agent) |
| runtime: resolv.conf leak | observed: a `--disable-dns` network still gets the host `resolv.conf`; `--dns 127.0.0.1` neutralises it | 6 |
| runtime: directory masking | observed: `--tmpfs` / `--mount type=tmpfs` over a path inside a bind mount does NOT hide it; a read-only bind of an empty directory does; `/dev/null` bind hides a file | 6 (empty-dir masks), 7 (evasion test) |
| runtime: resource limits | observed: CPU/RAM/PIDs limits fail in rootless when cgroup v2 delegation is missing ("rootless needs no limits") | 2 (doctor), 6 (apply when delegated, warn otherwise) |
| runtime: Claude Code image | observed: `npm install -g @anthropic-ai/claude-code` on `node:22-bookworm-slim` builds through the proxy (2.1.325) | 5 |
| runtime: Kiro image | unverified: `cli.kiro.dev` is blocked by this environment's firewall (403); install path, download hosts and the autonomous-mode flag not observed | 5 (build verified manually once the host is allowed) |
| runtime: busybox wget | observed: busybox `wget` gets HTTP 500 through CONNECT proxies where curl gets 200; tests must use curl | 7 |
| runtime: workspace ownership | observed: with `--userns=keep-id:uid=1000,gid=1000 --user 1000:1000`, a file written in the bound project is owned by the host user (uid 1000) on the host | 6 |
| runtime: CI runner | unverified: GitHub ubuntu runner Podman version, tun, subuid, cgroup delegation not observed | 7 (CI runs `podbox doctor` first and prints it) |
| runtime: Claude Code hosts | unverified: login/telemetry hosts not observed in real traffic | 6 (record proxy decision log during a real login, adjust defaults) |
| runtime: macOS / Windows | unverified: no podman machine available here; design keeps paths and binaries portable, CI cross-compiles only | 1, 5 |
| regression | none: greenfield repository | none |
| architecture | new: CLI → `Runtime` interface → podman binary; per-sandbox proxy container running the same podbox binary (`proxy serve`); host writes the effective policy file, proxy reads it read-only (control plane never reachable from the sandbox) | 2, 3, 4, 6 |
| documentation | new README (install, quick start, threat model, limits), CONTRIBUTING, LICENSE, exit-code table | 1, 7 |

## Decisions

| Decision | Why |
| --- | --- |
| Drive the `podman` binary instead of Go bindings, behind a `Runtime` interface | bindings need cgo and storage build tags and cross-compile badly to macOS/Windows; the CLI works identically on Linux, podman machine and WSL |
| Proxy written with the Go standard library, shipped inside the podbox binary (`podbox proxy serve`) and run in a `FROM scratch` image built locally | no registry dependency for the proxy, one codebase that v0.2 extends with TLS interception |
| v0.1 allowlists on CONNECT host only, no TLS interception | no CA to distribute, standard images, all tools work unmodified |
| Agent has no DNS at all; the proxy resolves names | removes the DNS exfiltration channel and the dnsmasq component |
| Control plane = effective policy file on the host, mounted read-only into the proxy and polled | hot reload without any socket or API reachable from the sandbox |
| Persist the whole agent home in a named volume per agent | Claude and Kiro keep auth under different paths; one volume covers both and survives image rebuilds |
| Directory masks are read-only binds of an empty directory | tmpfs masks were observed not to hide paths inside a bind mount |
| With an upstream proxy, the podbox proxy still resolves names locally and applies the IP guard; only when local resolution is impossible (corporate DNS-less networks) does it forward by name to the upstream, which cannot reach the sandbox's loopback | keeps the always-refused ranges enforced wherever the proxy can see the IP, without breaking corporate networks that only resolve through their proxy |
| Kiro Done-when line is closed only by a manual run on a host where Kiro's hosts are reachable; until then the PR states it unverified | its install hosts are blocked in the build environment and cannot be observed |
| License Apache-2.0 | compatible with MIT/Apache prior art, explicit patent grant |
