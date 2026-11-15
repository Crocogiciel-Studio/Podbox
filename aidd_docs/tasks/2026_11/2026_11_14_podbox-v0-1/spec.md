# Podbox — M0 + v0.1: closed-network sandbox usable every day

## Target

A developer runs Claude Code or Kiro in autonomous mode inside a rootless Podman sandbox, on their project directory, with a single `podbox` command, without the agent being able to reach a host outside the allowlist, read a sensitive file of the host or the project, or reach the container engine.

## Hard constraints

- The container engine is rootless Podman; no Docker account, Docker Desktop or root privilege is required.
- Shipped as a single `podbox` binary per OS and architecture (Linux, macOS, Windows; amd64 and arm64), with no runtime dependency other than Podman.
- The agent container has no direct network route to the outside, to the host or to its loopback services; its only way out is its sandbox's proxy.
- The agent container cannot resolve any external domain name.
- Egress denied by default: only allowlisted hosts (global defaults + global file + project file) are reachable; an explicit deny always wins; a project policy cannot widen a global deny.
- Fail closed: a missing or invalid policy file opens no additional access.
- Allowed HTTPS traffic is not decrypted in v0.1; no CA is installed in the agent.
- A blocked request is explained in a way the agent and the human can act on: it names the refused host and the command the human runs to allow it (response body for plain HTTP; `podbox policy log` for HTTPS, whose clients do not show the proxy's CONNECT response).
- The allowlist of a running sandbox is changed from the host without restarting the agent; nothing in the sandbox can change it.
- The proxy can chain to a corporate proxy without the agent knowing its address or credentials.
- If the corporate proxy intercepts TLS, the provided corporate CA is added to the agent's trust stores (without decryption on the sandbox side, the agent sees the corporate certificates; hiding it requires v0.2).
- Destinations resolving to loopback, link-local or cloud metadata are always refused; private IPs are refused unless a rule explicitly allows them (internal corporate services).
- The host's proxy variables are never passed to the agent container.
- Hardened agent container: non-root user, all capabilities dropped, no privilege escalation, default seccomp, CPU/RAM/PID limits, never privileged, never a mounted Podman or Docker socket, no host mount other than the workspace and the auth volume.
- `shared` mode: the project is mounted read-write; sensitive paths (`.env`, `.env.*` and `.envrc`, except the templates `.env.example`, `.env.sample`, `.env.template`, `.env.dist`, which stay readable by the agent; `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.npmrc`, `.pypirc`, `.netrc`, `.docker/config.json`) are always masked, with no way to undo it, including after a rename, a hard link or a permission change by the agent (when in doubt, the sandbox refuses to start); `.sandboxignore` (gitignore syntax) adds masks.
- The project's git hooks also run for the agent's commits; the agent can neither modify nor add a hook in `.git/hooks/`, nor modify `.git/config`.
- What the agent changes in `.git` that could make the host's git run code (`commondir` redirect, worktrees, alternates, project hook files) is detected and reported to the human, not blocked.
- The project configuration (`.podbox/policy.yaml`, `.sandboxignore`) is only taken into account at creation or on an explicit reload by the human; podbox never follows a workspace symbolic link on the host side.
- A workspace equal to the user's home, one of its parents, or containing podbox's configuration or state, is refused.
- An allowlist rule opens only ports 80 and 443, unless ports are listed explicitly.
- The agent's authentication (Claude Code session, Kiro session) persists between runs in a dedicated volume per agent, never in the host's `$HOME`.
- Agent images (Claude Code, Kiro) are built on the workstation from definitions shipped with `podbox`, on a configurable base image (internal registry); the proxy depends on no registry.
- Every command accepts `--output json` and returns a stable, documented exit code.
- Open source license compatible with reusing ideas from the cited projects (MIT or Apache-2.0).

## Non-goals

- Secret injection, TLS interception, ephemeral CA, `podbox secret` (v0.2).
- `learn` mode, structured audit logs (v0.2).
- `ask` mode, holding requests, `sandbox-control` MCP, `operating-in-sandbox` skill (v0.3).
- `clone` mode, `diff`, `export`, `cp`, `port`, kits (v0.4).
- Rules per method, path or query (they require decryption).
- Non-HTTP(S) traffic (git over SSH, databases).
- microVM runtime, Kubernetes backend, Podman-in-Podman, Docker Engine support.
- Sandy integration (`PodboxProvider`).
- Automated verification on macOS and Windows: support is designed in, CI verifies Linux only.
- Publishing images to a public registry.

## Done-when

- `podbox run claude [path]` and `podbox run kiro [path]` open an interactive agent session on the project, the agent being able to reach its API and read/write the project.
- After a first login, a second `run` of the same agent does not ask for authentication again.
- `podbox ls`, `exec`, `attach`, `stop`, `rm` manage the created sandboxes, in text and JSON output.
- `podbox doctor` reports each missing prerequisite (rootless Podman, subuid/subgid, network, SELinux) with a remediation.
- From the sandbox, all of these fail: a direct connection to the Internet, an external DNS resolution, a connection to a host loopback service, reading the host's `~/.ssh`, reading a project `.env`, reaching a Podman or Docker socket, a request to a host outside the allowlist.
- A request to a host outside the allowlist is explained as above; after `podbox policy allow <sandbox> <host>`, the same request succeeds without a restart.
- Behind a mandatory corporate proxy, the agent reaches an allowed host.
- The escape tests above run in Linux CI on every change and pass.

## Open Questions

None

## Stakeholders

- Decider: Guillaume (author)
- Owner: Crocogiciel Studio (repo `Crocogiciel-Studio/Podbox`)
- Consumer: developers in companies without Docker; Sandy (later integration)

## Context

- Full feasibility study: [feasibility-study.md](./feasibility-study.md).
- Revised roadmap and trade-offs (allowlist without decryption, agent without DNS, embedded proxy, corporate chaining brought forward to v0.1): decided in the onboarding session on 2026-11-14.
- Amendments of 2026-11-15 (user decisions taken during the validation rounds): git hooks run read-only instead of being masked; `.git` tampering detected rather than blocked (`podbox git-check`, exit codes 6 and 7); project configuration frozen at creation; dangerous workspaces refused; ports 80/443 by default; env templates readable by the agent; HTTPS refusals explained through `podbox policy log`. Details: [run.md](./run.md) and `docs/security-model.md`.
- M0 findings (Podman 5.7, rootless, Linux, behind a mandatory proxy): Podman passes the host's proxy variables to containers by default; an `--internal --disable-dns` network still inherits the host's `resolv.conf`; once both are neutralised, the internal container has no direct egress, no DNS, no access to the host's loopback or gateway, and reaches the proxy container, which goes out through the upstream proxy.
