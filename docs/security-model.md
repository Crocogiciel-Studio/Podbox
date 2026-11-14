# Security model

## What a sandbox is

```
 host ──── podman (rootless) ─────────────────────────────────────────────
             │
             ├─ podbox-egress (bridge) ── podbox-<name>-proxy ── upstream / internet
             │                                   │
             └─ podbox-<name>-int (internal, no DNS)
                                                 │
                                         podbox-<name>-agent
```

- **Agent container**: joins only the internal network, which has no gateway and no DNS. `/etc/resolv.conf` points at `127.0.0.1`, so no name resolves. The only reachable address is the proxy, named `proxy` in `/etc/hosts`. Host proxy variables are not forwarded (`--http-proxy=false`); podbox sets `HTTP(S)_PROXY=http://proxy:3128`.
- **Proxy container**: the podbox binary itself (`podbox proxy serve`) in a `FROM scratch` image, read-only root, no capabilities, uid 65534. It reads the effective policy from the sandbox state directory mounted read-only and polls it every second. Nothing in the sandbox can write it: the only writer is `podbox policy` on the host, and it compiles the project layer from a host-side snapshot of `.podbox/policy.yaml`, never from the agent-writable project (see [policy](policy.md#project-files-are-snapshotted)).
- **Hardening of the agent**: uid 1000 mapped to your user (`--userns=keep-id`), `--cap-drop=ALL`, `no-new-privileges`, default seccomp, never privileged, no engine socket, `/tmp` as tmpfs, CPU/RAM/PID limits when cgroup controllers are delegated (otherwise a warning). Host mounts: the project, its `.podbox/`, `.sandboxignore` and `.git/config` read-only when they exist, the masks, and nothing else; the home is a named volume.

## Proxy decisions

1. Policy: any matching deny wins, then any matching allow, otherwise deny. An invalid or missing layer contributes nothing.
2. Destination guard: the proxy resolves the name once, checks every address and dials the checked address (no DNS rebinding). Loopback, link-local, cloud metadata (`169.254.169.254`, `fd00:ec2::254`), unspecified, multicast and reserved ranges are always refused; private ranges (RFC 1918, ULA, CGNAT) only with `allow_private` on the matching rule.
3. Upstream: with a corporate proxy, the name is still resolved and checked locally; the CONNECT sent upstream carries the name so the corporate proxy applies its own rules. When the name does not resolve locally (DNS-less networks), it is forwarded by name. The agent never learns the upstream address or credentials: they are passed to the proxy container only.

Each decision is logged as one JSON line on the proxy's stdout (`podman logs podbox-<name>-proxy`).

## Refused workspaces

`create`, `run` and `start` refuse (exit code 2, `unsafe workspace`) a workspace that:

- is your home directory or one of its parents (including `/`), which would expose every dot-directory of your home;
- equals, contains or lies inside a podbox directory: config (`~/.config/podbox`), state (`~/.local/state/podbox`), cache (`~/.cache/podbox`) or `PODBOX_HOME`. The state holds the effective policy the proxy reads; an agent able to write it could allowlist any host.

Both sides are compared after resolving symbolic links, so a link to a refused directory is refused too. `start` checks again, so a workspace path later replaced by such a link is caught.

## Workspace masking

Forced, non-overridable patterns: `.env*`, `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.gnupg/`, `.npmrc`, `.pypirc`, `.netrc`, `.git-credentials`, `.docker/config.json`, `.git/hooks/`, and `.git/config` mounted read-only (it can name `core.hooksPath`, `core.fsmonitor` or filter commands that git on the host would run). `.git` is bound on itself so it cannot be renamed and replaced. Objects, refs, index and `HEAD` stay writable, so the agent can still commit; `git config` fails inside the sandbox. `.sandboxignore` adds patterns, read from the snapshot taken at `create` or `podbox policy reload`, so the agent cannot drop its own masks. Files are covered by a read-only bind of `/dev/null`, directories by a read-only bind of an empty directory (a tmpfs over a path inside a bind mount does not hide it). Uncovered files that look like secrets (name and entropy heuristic) produce a warning at start.

## What it does not protect against

- Kernel or runtime exploits: containers share the host kernel.
- Exfiltration through allowlisted hosts: anything the agent can reach can receive data.
- A TLS-intercepting corporate proxy: its CA is visible to and trusted by the agent (hiding it needs TLS interception in the sandbox, planned for v0.2).
- Files created in the project after the agent container was created are not masked until it is recreated (`stop` + `start`).
- Code the agent writes into the project runs on the host when you run it; review changes before executing them.
- Other git directory files stay writable: an agent can write `.git/commondir` to point git at a config it controls, which host git would then run (fsmonitor, hooks path). Masking it would break git; before running git on the host after a session, check that `.git/commondir` does not exist in a repository that is not a worktree.
- When `.git` is a file (a worktree or a submodule), the git directory it points to is outside the workspace and its hooks and config are not protected; podbox warns at start. Its `.git` pointer itself stays agent-writable, so review it before running git on the host.
