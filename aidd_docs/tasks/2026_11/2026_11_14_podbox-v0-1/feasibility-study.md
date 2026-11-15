# Podman Agent Sandbox — spec & feasibility study

Nov 14, 2026 · @Guillaume

## Context and goals

Goal: an open-source CLI that reproduces the Docker Sandboxes (sbx) experience on **rootless Podman**, to run coding agents in autonomous mode (YOLO) without risk to the host machine. It must be usable on its own, then integrated later into Sandy (the author's open-source ADE, currently based on sbx), which must keep working with both.

Working name used in this document: `podsbox` (to be replaced).

### Target environment constraints (enterprise)

- No usable Docker account, so no sbx and no Docker Desktop.
- Stack: Python, Java, TypeScript; **Podman** containers; **Kubernetes** deployments (possibly OpenShift, to be checked).
- The team's main coding agent: **Kiro** (IDE + CLI). Claude Code is used too.
- Likely internal registry (Artifactory/Nexus); Docker Hub possibly blocked. Corporate proxy possibly doing TLS interception.
- Workstation: OS to be confirmed (Linux, macOS or Windows/WSL2).

### Requested features

1. A proxy that injects secrets (the agent never sees the real keys).
2. Network policy (allowlist, deny by default), with **interactive approval built into the agent's flow**.
3. Mounting the project directory in **shared** or **isolated** mode (equivalent of sbx `--clone`), with **diff and export** of the agent's work.
4. An exclusion file like `.sandboxignore` so that `.env`, keys, etc. are never exposed.
5. Kits: connect MCPs, install skills, compose environments (like sbx kits).

### Non-goals (v1)

- No microVM isolation at first (optional backend later).
- No Kubernetes backend at first.
- No OAuth handling by the proxy at first (API keys only).

## Target architecture

Two repos: the **sandboxing CLI** (this project, open source, standalone) and **Sandy**, which consumes it through a provider interface. The "ADE" logic (diff, review, approval UI) lives in Sandy; the CLI stays an engine.

&#91;embedded content: sandbox architecture · agent, proxy, host CLI\]

The agent has only one way out: the proxy, the only container on both networks. Decisions go through the control API, reachable only from the host.

### Per-sandbox components

- **Agent container**: rootless, on a Podman `--internal` network (no route to the outside), workspace mounted, no real secrets.
- **Proxy container** (one per sandbox): the only container on two networks (internal + egress). Data plane = allowlist, secret injection, logs. Control plane = local API reachable **only from the host** (unix socket).
- Filtered **DNS resolver** (sinkholing unauthorized domains), built into the proxy or as a sidecar.
- **`sandbox-control` MCP** in the agent container: lets the agent read the policy and request access (never approve).
- **Host CLI**: lifecycle, secrets (OS keychain), policy, JSON event stream for Sandy.

### Technical choices

- **Language: Go.** Podman bindings, client-go for a future K8s backend, single binary, good proxy libs (goproxy, martian). Also usable as a library by Sandy.
- **Pluggable runtime from the start** (`Runtime` interface): `podman` (v1), `podman --runtime krun` / Kata (microVM, later), `k8s` (later). The isolation gap with sbx becomes an option, not a product limitation.
- **Podman API** via the socket (Docker-compatible): potentially allows supporting rootless Docker Engine at low cost.
- **CLI surface modeled on sbx** (`run`, `exec`, `cp`, `ls`, `rm`, `policy`, `secret`, `kit`) so that the Sandy adapter is nearly identical between the two providers.
- **`--output json` on every command** from v0.1.

### Contract with Sandy (to freeze early)

```
SandboxProvider
  create / start / stop / rm / ls
  exec / attach (PTY)
  cp in/out, ports, logs
  policy (get / allow / deny / watch events), secrets
  (diff/export: implemented on the Sandy side via exec + cp)
├── SbxProvider     → sbx CLI
└── PodsboxProvider → this CLI (or its Go lib)
```

Recommendation: write the interface and the `SbxProvider` first in Sandy; the CLI then only has to fulfill the same contract.

## Isolation model

Rootless Podman isolates the whole agent with its own namespaces, but shares the host kernel: it is weaker than an sbx microVM, and far above an agent run directly on the workstation. The agents' native sandboxes (Kiro `/sandbox`, Claude Code `/sandbox`) are only guardrails: they restrict the commands the agent launches, not the agent itself.

| Level | Kernel | Whole agent isolated | Verdict |
| --- | --- | --- | --- |
| Kiro / Claude Code native sandbox (bubblewrap, Seatbelt, AppContainer) | host | no, only its commands; the built-in file tools stay outside the sandbox | complementary guardrail |
| Rootless Podman (v1) | shared | yes | good |
| Hardened K8s pod | shared with the node | yes | good, if well configured |
| gVisor pod | user-space kernel | yes | very good |
| Podman `--runtime krun` / Kata | microVM, own kernel | yes | equivalent to sbx |

### Default hardening of the agent container

- Rootless, `--userns=keep-id`, non-root user in the image.
- `--cap-drop=ALL`, `--security-opt no-new-privileges`, default seccomp.
- Never `--privileged` except in rare rootless cases. Never a mounted Podman or Docker socket.
- No host mounts besides the workspace and the agent's state volumes. Refuse `$HOME` dot-dirs (`.ssh`, `.aws`, `.kube`, `.config`…).
- CPU, RAM, PID limits.
- `--internal` network: no direct route to the Internet or to the host's loopback services.
- Read-only rootfs when possible, with tmpfs for `/tmp`.

### Why never a rootful privileged container

`--privileged` grants all capabilities, disables seccomp and AppArmor/SELinux and exposes the host's devices: in rootful mode, this is equivalent to root on the machine (mounting the host disk, kernel modules). In K8s, add service account tokens, kubelet credentials and the secrets of other pods on the node. In **rootless** mode, the user namespace limits `--privileged` to the user's rights, which makes Podman-in-Podman (Testcontainers) possible without opening up the host.

### Kubernetes backend (later)

- Hardened pod: `automountServiceAccountToken: false`, `runAsNonRoot`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, resource limits, dedicated namespace without secrets, no `hostPath`.
- Deny-all egress NetworkPolicy, except towards the **proxy pod** and DNS. Caution: a NetworkPolicy applies to the whole pod, so **a proxy sidecar in the same pod cannot be the only one allowed out**. Proxy = separate pod (recommended), or sidecar with iptables redirection by UID (requires `NET_ADMIN` in an init container).
- Block the cloud metadata endpoint (`169.254.169.254`).
- Stronger isolation: gVisor or Kata `RuntimeClass` if the platform provides them.
- To give the agent a cluster: **vcluster** (virtual cluster in a namespace, without privileges), rather than kind/k3d which require a privileged pod.

## Egress proxy and secret injection

Same model as sbx: an HTTPS proxy with TLS interception, whose CA is baked into the agent image, replaces sentinel values with the real secrets, only for authorized hosts. The agent only sees placeholders.

### Non-negotiable rules

1. The agent has **no direct route** to the outside (`--internal` network). Without this, the proxy can be bypassed.
2. Real secrets exist **only in the proxy** (OS keychain on the host side → Podman secret mounted in the proxy container only).
3. Injection **per destination host**: a GitLab key only goes to GitLab.
4. The control plane (approval, policy changes) is **never reachable from the sandbox**.

### Two injection modes

| Mode | How it works | Advantages | Constraints |
| --- | --- | --- | --- |
| A. Forward proxy with TLS interception | `HTTPS_PROXY=http://proxy:8080`; the proxy presents a certificate signed by its CA, decrypts, injects, re-encrypts | works for all tools without reconfiguration | the CA must be accepted by every trust store; impossible with certificate pinning |
| B. Reverse proxy | the tool points to `http://proxy:8080/<service>` (e.g. `ANTHROPIC_BASE_URL`, Maven mirror, pip `index-url`); the proxy opens the real HTTPS connection | no CA to distribute | only for tools whose base URL is configurable |

In practice: B for package registries and LLM APIs, A for the rest.

### Why the CA is the tricky part

To add a header to an HTTPS request, the proxy has to decrypt it, and so impersonate the server with a certificate signed by its own CA. Each ecosystem has its own trust store:

| Tool | Trust store | Setting |
| --- | --- | --- |
| curl, git, most binaries | system | `update-ca-certificates` / `update-ca-trust` |
| Python (requests, pip) | certifi | `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE` |
| Node, npm | internal | `NODE_EXTRA_CA_CERTS` |
| Java, Maven, Gradle | JVM `cacerts` | `keytool -importcert -cacerts` (on RHEL/UBI, `update-ca-trust` often updates it) |
| Java (proxy) | ignores `HTTPS_PROXY` | `JAVA_TOOL_OPTIONS=-Dhttps.proxyHost=… -Dhttps.proxyPort=…` |
| Containers launched in the sandbox | their own trust stores | install the CA in the build images |

This is a cost paid once in the base image (Images, kits section). Recommendation: **one ephemeral CA per sandbox** (like Modal, Cloudflare, Lens), or at minimum a dedicated CA whose private key stays only in the proxy.

### Reference implementation (Podman prototype)

```bash
podman network create --internal sbx-internal
podman network create sbx-egress
printf '%s' "$ANTHROPIC_KEY" | podman secret create anthropic_key -

podman run -d --name proxy --network sbx-internal \
  --secret anthropic_key,type=env,target=ANTHROPIC_API_KEY \
  -v ./mitmproxy-ca.pem:/home/mitmproxy/.mitmproxy/mitmproxy-ca.pem:ro,Z \
  -v ./addon.py:/addon.py:ro,Z \
  docker.io/mitmproxy/mitmproxy mitmdump -s /addon.py --listen-port 8080
podman network connect sbx-egress proxy

podman run -it --rm --network sbx-internal --userns=keep-id \
  -e ANTHROPIC_API_KEY=placeholder \
  -v "$(pwd)":/workspace:Z -w /workspace \
  sbx/claude:polyglot
```

Minimal mitmproxy addon (prototype; the final version may be rewritten in Go):

```python
import os
from mitmproxy import http

INJECT = {"api.anthropic.com": ("x-api-key", os.environ["ANTHROPIC_API_KEY"])}
ALLOW = set(INJECT) | {"pypi.org", "files.pythonhosted.org"}

def http_connect(flow: http.HTTPFlow):
    if flow.request.pretty_host not in ALLOW:  # block before TLS
        flow.response = http.Response.make(403, b"blocked by sandbox proxy")

def request(flow: http.HTTPFlow):
    host = flow.request.pretty_host
    if host in INJECT:
        header, value = INJECT[host]
        flow.request.headers[header] = value
```

### Planned improvements

- **Redact responses**: replace any real secret returned by the upstream with its placeholder (done by agentcage).
- **Random, rotating placeholders** rather than a fixed string.
- **Chaining to the corporate proxy** (`--mode upstream:…`): the proxy container is the one that trusts the corporate CA; the agent only knows the sandbox CA.
- **Git**: HTTPS with an injected token (SSH does not go through an HTTP proxy).
- **OAuth** (Claude Code with a subscription, Kiro with SSO): refreshed tokens, hence intercepting the refresh flow agent by agent. Out of v1: the OAuth token lives in a dedicated sandbox volume, with minimal rights and lifetime.

## Network policy and interactive approval

Key differentiator versus sbx: the agent knows it is in a sandbox with a network policy, and requests access **within its own flow**; a human decides, never the agent.

### Policy model

Layers from general to specific; a deny always wins:

`global defaults → kit → project (.sandy/policy.yaml, versioned) → session grants`

```yaml
mode: ask            # strict | ask | learn
default: deny
rules:
  - host: "*.pythonhosted.org"
    allow: true
  - host: "api.anthropic.com"
    allow: true
    inject: { header: x-api-key, secret: anthropic }
  - host: "gitlab.example.internal"
    allow: true
    methods: [GET, POST]          # optional fine-grained rules: method, path, query
    paths: ["/api/v4/projects/123/"]
  - host: "*.pastebin.com"
    allow: false     # explicit deny: never a request
approval:
  hold_timeout: 30s
  scopes: [once, session, project]   # "project" rewrites policy.yaml
```

| Mode | Behavior | Use |
| --- | --- | --- |
| `strict` | anything not allowed is refused, without asking | CI, autonomous tasks |
| `ask` | unknown request put on hold, event emitted, human decision | interactive work (Sandy, TUI) |
| `learn` | everything passes and everything is logged; at the end of the session, the CLI proposes a `policy.yaml` generated from the traffic | onboarding a repo, then switching to `strict` |

### Approval flow

1. **Reactive**: a command reaches an unknown host. In `ask` mode, the proxy holds the request (bounded `hold_timeout`, since pip, Maven, etc. have timeouts and retry) and emits an event. Refusal or timeout: 403 with a body **readable by the agent**:

   ```
   Blocked by sandbox network policy: host "foo.com" is not allowed.
   To request access, call the MCP tool `request_network_access` with the host and your reason.
   ```
2. **Proactive**: the `sandbox-control` MCP exposes `get_network_policy()` and `request_network_access(host, reason)`. The call blocks until the decision, then returns `allowed` or `denied`. The request shows up in the agent's thread (Claude, Kiro) with its justification.
3. A base **skill / steering** explains the model to the agent (proxy, where to read the policy, what to do when blocked). It is installed by the base kit for each agent.
4. The MCP reaches `policy.sandbox.local`, a virtual host served by the proxy, which only accepts **requests**. Decisions go through the host control API (unix socket).

### Approval security

- The agent and its justification are not trustworthy (prompt injection). The UI highlights the **facts**: host, port, method, path, body size; the agent's reason comes second. The proxy can decrypt the held request to display `POST /api/upload (2.3 MB)` rather than `foo.com`.
- The sandbox can request, **never approve**.
- Against approval fatigue: rate limiting, grouping requests, silent explicit denies.
- The proxy knows which sandbox issued the request, not which command. Only the agent can specify it via the MCP, and that is declarative.
- Difference from agentcage: there, an LLM "Decider" agent can grant access without a human. Here, **human in the loop by default**.

### Beyond HTTP

- **DNS**: an `--internal` network can still resolve external names via aardvark-dns, which opens an exfiltration channel. Provide a filtered resolver (dnsmasq sinkholing unauthorized domains) and test it.
- **Non-HTTP** (git over SSH, databases): blocked by default; explicit TCP forwards in the policy for real needs.
- Block private IPs and metadata endpoints, with an anti DNS-rebinding check.
- The allowlist limits **destinations**, not **content**: an allowed host remains a possible exfiltration channel. The proxy's audit logs are the safety net.

### Control API events (format to freeze)

```json
{"type":"network.request.pending","sandbox":"sbx-42","id":"req-7f3",
 "host":"adoptium.net","port":443,"method":"GET","path":"/download/jdk21.tar.gz",
 "bodyBytes":0,"agentReason":"Download the JDK to build","source":"mcp|proxy",
 "expiresAt":"2026-11-14T19:30:00Z"}
```

Related commands: `podsbox policy watch --json` (stream), `podsbox policy allow <sbx> <host> --scope once|session|project`, `podsbox policy deny …`, `podsbox policy log`.

## Workspace: shared, clone, exclusions, diff

Clone mode is the only one that guarantees nothing changes on the host disk without consent: the agent's work becomes a **proposal** that you review, then accept in whole or in part.

| Mode | Mount | Exclusions | Review gate | Limits |
| --- | --- | --- | --- | --- |
| `shared` | bind mount of the repo, `--userns=keep-id`, `:Z` under SELinux | by masking (see below) | no: writes hit the host | changes mixed with yours; slow on macOS (VM) |
| `clone` | copy of the working tree (including uncommitted) into a named volume or a copy-on-write snapshot | trivial at copy time | yes | repo size, initial copy |

### `.sandboxignore`

- gitignore syntax. Sensitive patterns **always excluded and non-overridable**: `.env*`, `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.npmrc`, `.pypirc`, `.netrc`, `.docker/config.json` (to be completed). Inspiration: brood-box's `.broodboxignore`.
- Warning at launch if a file that looks like a secret is not covered (heuristic: name + entropy).
- **Clone mode**: exclusions applied at copy time (`rsync --exclude-from` logic).
- **Shared mode**: a bind mount cannot exclude. We mount the repo, then **mask** each ignored path with an empty file (`/dev/null`) or a tmpfs mounted on top. Limits: globs resolved at startup only; impossible for thousands of paths (reserve it for secrets); whatever the agent writes into a masked path goes into the tmpfs and disappears.
- Also mask `.git/hooks/` by default (code execution on the host side); this is what agentcage does.

### Diffing the agent's work (clone mode)

1. At creation: copy of the working tree, then a **base commit** (exact snapshot) on a `sandy/<id>` branch; its SHA is recorded.
2. The diff = base commit compared with the current state, whether the agent committed or not. To include untracked files without touching the agent's index:

   ```bash
   GIT_INDEX_FILE=/tmp/sandy.idx git add -A
   GIT_INDEX_FILE=/tmp/sandy.idx git diff --cached --stat <base>
   ```
3. Export, as you choose:
   - **Branch**: `git bundle create` in the sandbox, `cp` to the host, then `git fetch <bundle> sandy/<id>`.
   - **Patch**: `git apply --3way` on the host working tree.
   - **PR**: push to the remote via the proxy (injected token).
4. The base SHA enables a clean 3-way merge or rebase if the host has moved on in the meantime.
5. Alternative inspired by brood-box: copy-on-write snapshot (FICLONE on Linux, `clonefile` on macOS), file-by-file diff, accepted files written after re-checking hashes, setuid/setgid bits stripped.

In shared mode, a diff is still possible: `git stash create` at launch produces a snapshot without modifying anything. But there is no review gate.

### What this enables on the Sandy side (later)

- Live diff while the agent works; acceptance per file or per hunk.
- Automatic **checkpoints** (at each agent turn via a hook, or every N minutes) and rollback.
- Several sandboxes on the same base (Kiro versus Claude, two prompts) and diff comparison.

**Design point**: the diff logic lives **in Sandy**, not in the CLI. It only needs `exec` and `cp`, which sbx and the CLI both provide, so it works with both providers. The CLI can expose a `podsbox diff <id> --json` shortcut for use without Sandy.

## Images, kits, MCP and skills

One image per agent, built in layers so each thing is maintained in only one place. A kit = an image (or a layer) + a network policy + a runtime config (MCP, skills, env, auth volumes).

### Image layers

```
sbx-base          ← sandbox CA, proxy variables, user 1000, git, base tools
   └── sbx-polyglot   ← JDK + Maven/Gradle, Python + uv, Node + pnpm
         ├── sbx-kiro     ← kiro-cli + config
         ├── sbx-claude   ← Claude Code + config
         └── sbx-codex    ← …
```

- The base rarely changes; the toolchain is heavy and shared; the agent layer is small and changes every week.
- The agent takes its base as a parameter (`ARG BASE`), which allows toolchain variants (`sbx/kiro:java`, `sbx/kiro:polyglot`) without touching the agent.

Base image (excerpt):

```dockerfile
FROM registry.example.internal/ubi9/openjdk-21
USER root
COPY sbx-ca.crt /etc/pki/ca-trust/source/anchors/sbx-ca.crt
RUN update-ca-trust \
 && keytool -importcert -noprompt -cacerts -storepass changeit \
      -alias sbx-ca -file /etc/pki/ca-trust/source/anchors/sbx-ca.crt || true
ENV HTTPS_PROXY=http://proxy:8080 HTTP_PROXY=http://proxy:8080 \
    NO_PROXY=localhost,127.0.0.1 \
    SSL_CERT_FILE=/etc/pki/tls/certs/ca-bundle.crt \
    REQUESTS_CA_BUNDLE=/etc/pki/tls/certs/ca-bundle.crt \
    NODE_EXTRA_CA_CERTS=/etc/pki/ca-trust/source/anchors/sbx-ca.crt \
    JAVA_TOOL_OPTIONS="-Dhttps.proxyHost=proxy -Dhttps.proxyPort=8080 -Dhttp.proxyHost=proxy -Dhttp.proxyPort=8080 -Dhttp.nonProxyHosts=localhost"
USER 1000
```

Agent layer (Kiro example):

```dockerfile
ARG BASE=registry.example.internal/sbx/polyglot:2026.11
FROM ${BASE}
ARG KIRO_VERSION=2.31.0
USER root
RUN curl -fsSL https://cli.kiro.dev/install | bash   # ideally: pinned version, binary from the internal registry
USER 1000
LABEL sbx.agent="kiro" sbx.agent.version="${KIRO_VERSION}"
ENTRYPOINT ["kiro-cli", "chat", "--v3"]
```

If the CA is ephemeral per sandbox, it is injected at startup (read-only volume + entrypoint script that updates the trust stores) instead of being baked into the image.

### Kit format (neutral, to be specified)

```yaml
name: kiro-java
image: { base: sbx/polyglot, agent: kiro }   # or a layer of packages/files
env: { KIRO_LOG: info }
network: { extends: base, rules: [ { host: "*.kiro.dev", allow: true } ] }
secrets: [ { name: gitlab, host: gitlab.example.internal, header: PRIVATE-TOKEN } ]
mcp:
  - { name: sandbox-control, transport: stdio, builtin: true }
  - { name: jira, transport: http, url: https://mcp.example.internal/jira, secret: jira }
skills: [ { name: operating-in-sandbox, builtin: true }, { path: ./skills/java-conventions } ]
volumes: { auth: ~/.local/share/kiro-cli }
```

- **Two-part compilation**: an image layer cached by hash (fast startup) and a runtime config applied at launch.
- **Per-agent adapters**: the kit describes MCPs and skills once; each adapter generates the right format (`.mcp.json` / `~/.claude.json` and `.claude/skills` for Claude Code; `~/.kiro/settings/mcp.json`, steering and powers for Kiro; `config.toml` for Codex). This is the most expensive item to maintain, since these formats change often.
- **Open question**: read the sbx kit format directly if it is documented and stable (free "one kit for both tools" compatibility), otherwise translate the neutral format to sbx.

### Three MCP cases

| Case | How it works | Secrets |
| --- | --- | --- |
| stdio, inside the sandbox | installed by the kit in the image or at launch | via the proxy for its outgoing HTTP calls |
| remote HTTP | goes through the proxy, host in the allowlist | **token injected by the proxy**: the agent does not see it |
| on the host | bridge to `host.containers.internal`, explicitly allowed | stay on the host |

Alternative to study (kuza/agentbox): an **MCP sidecar** per service, which holds the token and exposes a secret-free MCP endpoint to the agent.

### Maintenance

- Dedicated repo: `base/`, `toolchains/`, `agents/`, `kits/`, `policies/`.
- CI: weekly rebuild (CVEs, agent updates), push to the registry (internal in enterprise, GHCR in open source), dated tags + `latest`, pinned versions, image scanning (Trivy or equivalent).

## CLI specification

Verbs modeled on sbx; every command accepts `--output json` and returns a stable exit code. Proposed first surface, to be validated:

| Command | Role | Version |
| --- | --- | --- |
| `podsbox run <agent\|kit> [path] [--mode shared\|clone] [--name] [--policy strict\|ask\|learn]` | creates and attaches a sandbox (interactive agent) | v0.1 |
| `podsbox create` / `start` / `stop` / `rm` / `ls` | lifecycle, persistent sandboxes | v0.1 |
| `podsbox exec <id> -- <cmd>` / `attach <id>` | command or PTY in the sandbox | v0.1 |
| `podsbox doctor` | checks rootless Podman, subuid/subgid, networks, DNS, SELinux | v0.1 |
| `podsbox policy show\|allow\|deny\|log\|watch\|reload` | network policy, approvals, audit | v0.1 (allowlist), v0.4 (approvals) |
| `podsbox secret set\|ls\|rm` | secrets in the OS keychain | v0.2 |
| `podsbox cp <src> <dst>` | file copy (screenshots, artifacts) | v0.3 |
| `podsbox port <id> <port>` | exposes a sandbox port on `127.0.0.1` | v0.3 |
| `podsbox diff <id>` / `export <id> --as branch\|patch\|push` | the agent's work in clone mode | v0.3 |
| `podsbox kit ls\|add\|build` | kits: images, MCP, skills, policy | v0.3 / v0.4 |

### Configuration files

- Global: `~/.config/podsbox/config.yaml`, `~/.config/podsbox/policy.yaml`.
- Project: `.sandy/policy.yaml` (versioned), `.sandboxignore`, optional `.sandy/kit.yaml`.
- State: `~/.local/share/podsbox/<id>/` (metadata, base SHA, audit logs).

### Cross-cutting requirements

- Fail closed by default: a missing or invalid file = no additional access, never the opposite.
- A project policy **cannot widen** the global policy (brood-box rule).
- Every proxy action is logged (decision, host, method, path, size) in a format Sandy can consume.
- Automated escape tests in CI: reading the host `~/.ssh`, direct network egress, external DNS, access to the Podman socket, exfiltration of a fake secret (inspiration: thenaithan/agent-sandbox-podman's test script).

## MVP roadmap

Four sequential versions, then later workstreams; each is usable on its own and ends with a verifiable exit criterion.

### v0.1 — Closed-network sandbox

- [ ] Go skeleton, `Runtime` interface, rootless Podman implementation via the socket
- [ ] `run` / `exec` / `attach` / `ls` / `rm` / `doctor`, `--output json` everywhere
- [ ] Default hardening of the agent container (Isolation section)
- [ ] `--internal` network + allowlist proxy container (without injection) + filtered DNS
- [ ] `shared` mode + `.sandboxignore` by masking, with forced sensitive patterns
- [ ] `sbx-base`, `sbx-polyglot`, `sbx-claude`, `sbx-kiro` images

Exit criterion: Claude Code and Kiro run in autonomous mode; the escape tests (direct network, external DNS, host `~/.ssh`, Podman socket) all fail.

### v0.2 — Secrets

- [ ] `secret set|ls|rm` (OS keychain, encrypted file fallback)
- [ ] Per-host injection with random placeholders; secret redaction in responses
- [ ] Ephemeral per-sandbox CA, distributed to all trust stores (system, Python, Node, Java)
- [ ] Proxy audit logs

Exit criterion: `printenv` and reading the disk inside the sandbox reveal no real secret; API calls work.

### v0.3 — Clone, diff, kits v1

- [ ] `clone` mode (copy with exclusions, base commit, `sandy/<id>` branch)
- [ ] `diff` / `export --as branch|patch|push`
- [ ] `cp`, `port`
- [ ] Kits v1: image + env + policy

Exit criterion: an agent's work comes back as a reviewable branch, without any host file having changed before the export.

### v0.4 — Approvals and agents

- [ ] `ask` mode: holding, `policy watch --json` events, `policy allow|deny --scope`
- [ ] `sandbox-control` MCP + `operating-in-sandbox` skill for each agent
- [ ] `learn` mode + `policy.yaml` generation
- [ ] MCP and skills adapters for Claude Code and Kiro
- [ ] Minimal TUI for approvals without Sandy

Exit criterion: a blocked agent requests access via the MCP, a human approves, the agent resumes without manual intervention.

### Later

- microVM runtime (`podman --runtime krun`, Kata; look at brood-box's `go-microvm`)
- Kubernetes backend (hardened pod, proxy in a separate pod, optional vcluster)
- OAuth handled by the proxy, agent by agent
- Rootless Podman-in-Podman (Testcontainers)
- Sandy integration (`PodsboxProvider`)

## Prior art and inspirations

No open-source project covers the whole list, but every feature already exists somewhere, often in Go and under an MIT or Apache-2.0 license. Chosen strategy: a clean CLI in Go that takes the best ideas; run agentcage and mattolson/agent-sandbox locally before freezing the architecture. Check licenses before reusing any code and keep the copyright notices.

| Project | Tech / license | What we take | What is missing |
| --- | --- | --- | --- |
| [agentcage](https://github.com/agentcage/agentcage) | probably Python, MIT, \~22 stars | rootless Podman + quadlets, egress deny by default, random placeholders and response redaction, mitmdump + inspector chain, DNS sinkhole (dnsmasq), blocking private IPs and metadata, **Policy API** `agentcage.local` where the agent requests access with a justification, tmpfs masks on `.git/hooks/` | MCP, skills, kits, clone mode; decision by an LLM "Decider" rather than a human; small project with no release |
| [mattolson/agent-sandbox](https://github.com/mattolson/agent-sandbox) | Go + mitmproxy addon (`enforcer.py`), MIT, \~212 stars | layered YAML policy (user, per agent) with rules by scheme / method / path / query, hot reload, secrets mounted only in the proxy, iptables firewall, `operating-in-agent-sandbox` skill and policy readable in `/run/agentbox/policy.yaml` | designed for Docker / compose, no MCP, early stage |
| [brood-box (Stacklok)](https://awesome.ecosyste.ms/projects/github.com%2Fstacklok%2Fbrood-box) | pure Go, Apache-2.0, \~20 stars | libkrun microVM, copy-on-write workspace snapshot, file-by-file diff review, `.broodboxignore` with forced sensitive patterns, egress profiles, a project config cannot widen the global one | no injection proxy (secrets passed as env into the VM), requires KVM, experimental |
| [container-use (Dagger)](https://docsearch.algolia.com/mcp/docs/repo/dagger/container-use) | Go, \~3.6k stars | one container + one git branch per agent, history in git notes, exposed as MCP, review via `git checkout` | depends on Dagger / Docker, no secrets proxy |
| [kuza/agentbox](https://github.com/kuza/agentbox) | rootless Podman | `--internal` network, Squid as allowlist, **MCP sidecars** that hold the tokens outside the box | no injection, Claude Code and pi only |
| [devgugga/agent-sandbox](https://github.com/devgugga/agent-sandbox) | rootless Podman + systemd | clone per workspace, per-project `.agent-sandbox.toml` (domains, ports, nested Podman, Postgres / Redis services), `reload-allowlist` without restarting the agent | no secret injection |
| [thomaspeklak/agent-sandbox (ags)](https://github.com/thomaspeklak/agent-sandbox) | Rust, rootless Podman | multi-agent (pi, claude, codex, gemini, opencode), ordered secret sources (env, secret store, host command), allow / deny prompts via zenity / kdialog | young; approvals as desktop popups |
| [h5i](https://medium.com/@Koukyosyumei/h5i-a-lightweight-rootless-container-sandbox-for-claude-code-and-codex-e9a9eb0cf71e) | rootless Podman | all capabilities dropped, read-only rootfs, disposable git workspace, host-side credentials proxy, patch + execution traces for review | poorly documented |
| [thenaithan/agent-sandbox-podman](https://github.com/thenaithan/agent-sandbox-podman) | rootless Podman | **script that tests the boundaries** on the machine (mounts, sockets, host loopback via pasta `--no-map-gw`) | open egress |
| [ftnilsson/dev-containers](https://github.com/ftnilsson/dev-containers) | Podman + devcontainer | Squid sidecar doing CONNECT without decryption (no CA): a good model for an "allowlist without injection" mode | no injection |
| [microsandbox](https://docs.vultr.com/how-to-set-up-agent-sandboxing-on-vultr-cloud-compute) | libkrun, Python / TS / Rust / Go SDK | microVM from OCI images, inter-VM traffic denied by default, lifecycle MCP server | not specific to interactive coding agents |

### Reusable credential proxy building blocks

| Project | Model |
| --- | --- |
| [Infisical Agent Vault](https://infisical.com/blog/agent-vault-the-open-source-credential-proxy-and-vault-for-agents) | MITM forward proxy via `HTTPS_PROXY`, built-in vault, research preview |
| [agent-airlock](https://github.com/es617/agent-airlock) | small host-side reverse proxy, revocable proxy token, can listen on a unix socket, errors tagged `X-Agent-Airlock` |
| [OneGate](https://onegate.178-104-9-163.sslip.io/) | MIT gateway, root CA to trust, placeholders replaced by the real secrets, per-agent policy |
| Aegis (Product Hunt) | Apache-2.0 local proxy, encrypted vault, per-domain allowlist, audit, MCP server |

### Product references (not open source, or platforms)

- **Docker Sandboxes (sbx)**: microVM, host proxy `gateway.docker.internal:3128`, CA in the trust store, `JAVA_TOOL_OPTIONS` for Java, secrets in the OS keychain replaced by sentinels. Reference model to reproduce.
- **Modal, Cloudflare Sandboxes, Lens agents**: one ephemeral CA per sandbox, installed at creation, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS` variables.
- **kubernetes-sigs/agent-sandbox**: `Sandbox`, `SandboxTemplate`, `SandboxWarmPool`, `SandboxClaim` CRDs, gVisor / Kata isolation; reference for the future K8s backend.
- **Native sandboxes**: Claude Code `/sandbox` (bubblewrap + socat on Linux, Seatbelt on macOS) and Kiro `/sandbox` (IDE 1.3+, CLI 2.31+ in V3 sessions; Seatbelt, bubblewrap, AppContainer): complementary guardrails, not agent sandboxes.

## Risks, pitfalls and open questions

The main risk is not the sandboxing but the maintenance of the per-agent adapters (MCP formats, skills, auth) and the scope: sbx is the product of an entire team.

### Known technical pitfalls

| Pitfall | Mitigation |
| --- | --- |
| Rootless volumes: unexpected uids on the host side | `--userns=keep-id` |
| SELinux (Fedora, RHEL): permission denied on volumes | `:Z` suffix |
| Short image names refused or ambiguous | fully qualified names (`docker.io/library/…`) or internal registry in `registries.conf` |
| Ports < 1024 forbidden in rootless | ports ≥ 1024 |
| Tools that expect the Docker socket (Testcontainers) | `systemctl --user enable --now podman.socket`, `DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock`, often `TESTCONTAINERS_RYUK_DISABLED=true`; inside the sandbox, rootless Podman-in-Podman |
| Java ignores `HTTPS_PROXY` and has its own truststore | `JAVA_TOOL_OPTIONS` + `keytool` |
| Certificate pinning | mode B (reverse proxy) or passthrough without injection |
| DNS resolved despite `--internal` | filtered resolver + dedicated test |
| `--dangerously-skip-permissions` refused as root (Claude Code) | non-root user in the image |
| Native sandboxes (Kiro, Claude Code) silently fall back if the dependency is missing | `sandbox.failIfUnavailable: true` (Claude Code); check the status on the Kiro side |
| Slow bind mounts on macOS (`podman machine` VM) | prefer clone mode on macOS |

### Accepted v1 limitations

- Shared kernel: a kernel flaw allows an escape. Future mitigation: microVM runtime.
- OAuth tokens (Claude subscription, Kiro SSO) present in the sandbox.
- An allowed host remains an exfiltration channel; the allowlist limits destinations, not content.
- The injected key keeps its full scope: the proxy controls where the request goes, not what it does. Use minimally scoped tokens.

### Open questions

- [ ] Workstation OS in the enterprise (Linux, macOS, Windows/WSL2)? Determines the shared or clone default and the feasibility of krun.
- [ ] Is the sbx kit format documented and stable, for direct reading?
- [ ] Does sbx expose its network requests and decisions in its CLI, so the `sandbox-control` MCP can work with the `SbxProvider`?
- [ ] How exactly does Kiro CLI authenticate (SQLite session file, refresh), and can that token be kept outside the sandbox?
- [ ] Does the corporate proxy do TLS interception (corporate CA to integrate into the proxy container)?
- [ ] Does the K8s platform offer a gVisor or Kata `RuntimeClass`? Is it OpenShift (Dev Spaces supports Kiro over remote SSH)?
- [ ] Final proxy in native Go (goproxy, martian), or keep mitmproxy?
- [ ] Final project name.

### Non-technical

- **Intellectual property**: check the employment contract (IP clause, personal projects) before developing or publishing as open source a tool used at work, especially if it is developed partly on work time.
- **Security validation**: present the tool to security / IT as a proposal rather than a workaround; image scanning and escape tests in CI are good arguments.

## References

### Docker Sandboxes (sbx), reference model

- [Docker Sandboxes — Manage credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/)
- [Docker Sandboxes — Troubleshooting (CA, forward / transparent paths)](https://docs.docker.com/ai/sandboxes/troubleshooting/)
- [Docker Sandboxes — Kiro](https://docs.docker.com/ai/sandboxes/agents/kiro/)
- [Varlock — Docker Sandboxes (gateway, CA, sbx secret)](https://varlock.dev/sandboxes/docker-sandboxes/)
- [DZone — Docker Sandbox isolation story (JAVA\_TOOL\_OPTIONS)](https://dzone.com/articles/docker-sandbox-isolation-story)
- [Docker Labs — Network control and proxy-managed credentials](https://dockerlabs.collabnix.com/docker-workshop/ai-governance/kits/network-and-creds)

### Native agent sandboxes

- [Kiro — Local command sandbox](https://kiro.dev/docs/sandbox/)
- [Kiro blog — Local sandboxing](https://kiro.dev/blog/local-sandboxing/)
- [Kiro — Tool permissions (`/tools trust-all`)](https://kiro.dev/docs/cli/chat/permissions/)
- [Classmethod — Testing Kiro CLI's `/sandbox` (mcpSandboxing, sandbox\_network)](https://dev.classmethod.jp/en/articles/kiro-cli-local-command-sandbox/)
- [Claude Code — Sandboxing](https://code.claude.com/docs/en/sandboxing)
- [Claude Code — Development containers](https://docs.claude.com/en/docs/claude-code/devcontainer)

### Credential injection and per-sandbox CA

- [Modal — Sandbox secret injection](https://modal.com/docs/guide/sandbox-secret-injection)
- [Cloudflare — Credential injection and egress policies for Sandboxes](https://developers.cloudflare.com/changelog/post/2026-04-13-sandbox-outbound-workers-tls-auth/index.md)
- [Lens — Credential bindings (ephemeral CA)](https://docs.k8slens.dev/lens-agents/platform-admin/credential-bindings/)
- [Infisical — Agent Vault](https://infisical.com/blog/agent-vault-the-open-source-credential-proxy-and-vault-for-agents)
- [agent-airlock](https://github.com/es617/agent-airlock)
- [StackOne — Why a proxy does not limit what the key can do](https://www.stackone.com/blog/ai-agent-sandbox-api-keys/)

### Open-source projects (see Prior art)

- [agentcage](https://github.com/agentcage/agentcage) · [mattolson/agent-sandbox](https://github.com/mattolson/agent-sandbox) · [kuza/agentbox](https://github.com/kuza/agentbox) · [devgugga/agent-sandbox](https://github.com/devgugga/agent-sandbox) · [thomaspeklak/agent-sandbox](https://github.com/thomaspeklak/agent-sandbox) · [thenaithan/agent-sandbox-podman](https://github.com/thenaithan/agent-sandbox-podman) · [ftnilsson/dev-containers](https://github.com/ftnilsson/dev-containers) · [con/yolo](https://github.com/con/yolo) · [statox/claude-devcontainer](https://github.com/statox/claude-devcontainer) · [textcortex/claude-code-sandbox](https://github.com/textcortex/claude-code-sandbox)
- [brood-box (Stacklok)](https://awesome.ecosyste.ms/projects/github.com%2Fstacklok%2Fbrood-box) · [container-use (Dagger)](https://container-use.com/) · [h5i](https://medium.com/@Koukyosyumei/h5i-a-lightweight-rootless-container-sandbox-for-claude-code-and-codex-e9a9eb0cf71e) · [microsandbox (Vultr guide)](https://docs.vultr.com/how-to-set-up-agent-sandboxing-on-vultr-cloud-compute) · [libkrun for agent sandboxes (iii)](https://iii.dev/blog/why-agent-sandboxes-are-converging-on-libkrun-not-firecracker/)

### Kubernetes

- [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) · [Kubernetes blog — Running agents with Agent Sandbox](https://kubernetes.io/blog/2026/03/20/running-agents-on-kubernetes-with-agent-sandbox) · [Pulumi — Agent Sandbox (gVisor, Kata)](https://www.pulumi.com/blog/kubernetes-agent-sandbox)
- [Red Hat OpenShift Dev Spaces — Kiro SSH support](https://docs.redhat.com/en/documentation/red_hat_openshift_dev_spaces/3.26/html/release_notes_and_known_issues/technology-preview) · [Coder — Kiro CLI in workspaces](https://coder.com/blog/using-kiro-cli-with-coder-workspaces)
