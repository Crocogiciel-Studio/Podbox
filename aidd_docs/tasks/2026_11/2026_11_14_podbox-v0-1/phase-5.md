---
status: done
---

# Instruction: Images — proxy, base, Claude, Kiro

## Architecture projection

```txt
.
├── Makefile ✏️
└── internal/
    ├── cli/
    │   └── image.go ✅
    └── images/
        ├── images.go ✅
        ├── proxybin.go ✅
        ├── proxybin_embed.go ✅
        ├── images_test.go ✅
        └── defs/
            ├── proxy.Containerfile ✅
            ├── base.Containerfile ✅
            ├── claude.Containerfile ✅
            ├── kiro.Containerfile ✅
            ├── shell.Containerfile ✅
            └── entrypoint.sh ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | podman available | — | system |
| Happy path | `podbox image build proxy` on Linux | `localhost/podbox-proxy:<version>` from `scratch`, no registry pull | cli |
| Happy path | `podbox image build claude` | image runs `claude --version` as uid 1000 user `agent` | cli |
| Happy path | `podbox image build shell` | base image with bash, curl, git as uid 1000 | cli |
| Edge case | host is macOS without embedded linux binary (dev build) | clear error naming `make cross-embed` | cli |
| Edge case | `ca_file` configured | CA present in system trust store and `NODE_EXTRA_CA_CERTS` | cli |
| Edge case | `images.base` overridden to an internal registry | build uses it | cli |

## Tasks to do

### `1)` Proxy image without registry

> `FROM scratch` + the podbox linux binary, entrypoint `podbox proxy serve`.

1. Binary source: running executable when host is linux and arch matches the podman machine; else binary embedded under build tag `embedproxy` (linux/amd64 + arm64, produced by `make cross-embed`).
2. Image tag includes version so upgrades rebuild.

### `2)` Agent images from embedded Containerfiles

> base (user `agent` uid 1000, home `/home/agent`, git, curl, ca-certificates, bash) → claude (Node + npm `@anthropic-ai/claude-code`) / kiro (installer) / shell.

1. `ARG BASE`, default `docker.io/library/node:22-bookworm-slim` for base; config `images.base` overrides.
2. Optional corporate CA copied into the build context and trusted (system store, `NODE_EXTRA_CA_CERTS`, Java via `JAVA_TOOL_OPTIONS` truststore when a JDK exists).
3. Labels `io.podbox.agent`, `io.podbox.version`; entrypoint script sets proxy env defaults.

### `3)` `podbox image build|ls`

> Build on demand; `run` builds missing images automatically.

### `4)` Kiro verification

> Build the Kiro image once `cli.kiro.dev` and its download hosts are reachable; record the hosts in Kiro's default policy and the autonomous flag in the agent table. Until then the Kiro image is marked experimental.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | proxy image builds offline from any registry; `podman run` it answers on 3128 |
| 2 | `claude --version` runs as non-root in the Claude image |
| 3 | `podbox image ls --output json` lists built images with agent labels |
| 4 | Kiro image builds and `kiro-cli --version` runs, or the gap is reported as unverified |
