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
- **Hardening of the agent**: uid 1000 mapped to your user (`--userns=keep-id`), `--cap-drop=ALL`, `no-new-privileges`, default seccomp, never privileged, no engine socket, `/tmp` as tmpfs, CPU/RAM/PID limits when cgroup controllers are delegated (otherwise a warning). Host mounts: the project, its `.podbox/`, `.sandboxignore`, `.git/config` and `.git/hooks/` read-only when they exist, the masks, and nothing else; the home is a named volume.

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

Forced, non-overridable patterns: `.env*`, `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.gnupg/`, `.npmrc`, `.pypirc`, `.netrc`, `.git-credentials`, `.docker/config.json`, and `.git/config` mounted read-only (it can name `core.hooksPath`, `core.fsmonitor` or filter commands that git on the host would run). `.git` is bound on itself so it cannot be renamed and replaced. `.git/hooks/` is read-only too (see [git hooks](#git-hooks)). Objects, refs, index and `HEAD` stay writable, so the agent can still commit; `git config` fails inside the sandbox. `.sandboxignore` adds patterns, read from the snapshot taken at `create` or `podbox policy reload`, so the agent cannot drop its own masks. Files are covered by a read-only bind of `/dev/null`, directories by a read-only bind of an empty directory (a tmpfs over a path inside a bind mount does not hide it). Uncovered files that look like secrets (name and entropy heuristic) produce a warning at start.

## Git hooks

Hooks run inside the sandbox: `.git/hooks/` and every `.git/modules/*/hooks/` that exists is bound read-only on itself, so the project's hooks run for the agent's commits but the agent cannot add, change or replace them. An absent `.git/hooks/` is not created on the host; one the agent creates (it can, since `.git` is writable) is reported by the [integrity check](#git-directory-integrity), like a new submodule hook.

The residual risk is hook managers that read worktree files: `core.hooksPath` pointing inside the project, `.husky/`, `.githooks/`, `lefthook.yml` (`lefthook.yaml`, `.lefthook.yml`) and `.pre-commit-config.yaml`. The agent can edit them like any project file, and your next commit on the host runs what it wrote. This is the same risk class as `package.json` scripts or a `Makefile`, and podbox detects it rather than prevents it:

- At `create`, `start` and `run`, when the project has such files, podbox prints one `info:` line naming them (and an `info` field in the JSON output of `create` and `start`).
- Their sha256 (recursively for directories, bounded to 1000 files; symbolic links by target) is part of the git fingerprint. Each changed, added or removed file is reported after the session under `GIT HOOK FILES CHANGED`, with the other findings: review the diff before committing on the host, then `podbox git-check <name> --accept`.

For untrusted code, use the upcoming `--clone` mode. Hook installers fail inside the sandbox: `husky` cannot set `core.hooksPath` because `.git/config` is read-only, and `lefthook install` or `pre-commit install` cannot write the read-only `.git/hooks/`. Run the install once on the host.

## Git directory integrity

The agent must write `.git` to commit, and git on the host trusts what it finds there. Some writes make host git run commands or read another repository, and cannot be masked without breaking git: `.git/commondir` pointing at a config the agent controls (its `core.fsmonitor` runs on the next `git status`), `config.worktree`, `objects/info/alternates`, `info/*`, symbolic links (including `HEAD` or refs) pointing outside `.git`, unexpected new top-level entries, hooks created in an absent `.git/hooks/`, submodule configs and hooks under `.git/modules/`, a pending rebase with `exec` lines, or a `.git` created or repointed in the sandbox.

podbox detects these, it does not prevent them. At `create` (or the first `start` of an older sandbox) it records a fingerprint of `.git` in the sandbox state: its kind, top-level entries, the sha256 of the files above and every symbolic link. Normal churn (objects, refs, logs, index, `HEAD` content, `ORIG_HEAD`, `FETCH_HEAD`, `packed-refs`, worktrees, ...) is left out, so commits, branches, checkouts, rebases, stashes and gc raise nothing.

The live `.git` is compared with the fingerprint after `run`, `attach` and `exec` return, and on `start`, `stop`, `rm` and `ls`. Any difference prints a warning on stderr listing each path and why it matters, and adds `git_integrity` (`ok`, `findings`) to the JSON output; exit codes are unchanged. `podbox git-check <name>` runs the comparison alone and exits 6 on findings. On a warning, do not run git in that repository until you have reviewed the listed paths; remove what you did not create, then `podbox git-check <name> --accept` re-records the fingerprint. A change you made yourself on the host (for example `git remote add`, which rewrites `.git/config`) is reported too and needs the same acceptance.

The check runs when podbox does: a git command you run on the host before any podbox command is not protected. For untrusted work, prefer the upcoming `--clone` mode, which keeps the agent away from the host repository's git directory.

## What it does not protect against

- Kernel or runtime exploits: containers share the host kernel.
- Exfiltration through allowlisted hosts: anything the agent can reach can receive data.
- A TLS-intercepting corporate proxy: its CA is visible to and trusted by the agent (hiding it needs TLS interception in the sandbox, planned for v0.2).
- Files created in the project after the agent container was created are not masked until it is recreated (`stop` + `start`).
- Code the agent writes into the project runs on the host when you run it; review changes before executing them. This includes worktree hook files run by your next commit (see [git hooks](#git-hooks)).
- Other git directory files stay writable: an agent can write `.git/commondir` and similar files that make host git run its commands. podbox detects them after the session (see [git directory integrity](#git-directory-integrity)) but does not block them.
- When `.git` is a file (a worktree or a submodule), the git directory it points to is outside the workspace and its hooks and config are not protected; podbox warns at start. Its `.git` pointer itself stays agent-writable; a change to it is reported by the git integrity check.
