# podbox

Run coding agents (Claude Code, Kiro) autonomously in a hardened rootless Podman sandbox whose only network egress is an allowlisting proxy.

- The agent container has no route out, no DNS, and no access to the host or its loopback services; its only exit is the sandbox proxy.
- The proxy allows only listed hosts (deny by default) and answers refusals with a message the agent can read and relay.
- The project is mounted read-write; secrets (`.env*`, keys, `.ssh/`, ...) are always hidden. `.git/config` and `.git/hooks/` are read-only (project hooks still run for the agent's commits); other `.git` changes that git on the host could execute, and edits to worktree hook files (`.husky/`, `lefthook.yml`, `.pre-commit-config.yaml`, `core.hooksPath`, ...), are reported after each session. Your home directory and podbox's own config/state directories are refused as workspaces.
- The agent login persists in a per-agent volume, never in your `$HOME`.

Status: v0.1. See [security model](docs/security-model.md) and [policy reference](docs/policy.md).

## Install

Requirements: rootless Podman 4.4+ (on macOS/Windows, a running `podman machine`). No Docker, no root.

```sh
make build                 # bin/podbox (static, CGO_ENABLED=0)
sudo install bin/podbox /usr/local/bin/
podbox doctor              # every check ok or warn; each failure prints its fix
```

Release builds for macOS and Windows embed the Linux proxy binary (`make cross-embed`) so the proxy image builds without any registry.

## Quick start

```sh
cd ~/code/my-project
podbox run claude          # builds images on first use, then opens Claude Code
```

`run` creates the sandbox `claude-my-project` if needed, starts it and opens `claude --dangerously-skip-permissions` in the project directory. Log in once; the next `run` reuses the session.

Other agents: `podbox run kiro` (experimental, unverified), `podbox run shell` (bash, for inspection).

When the agent hits a host outside the allowlist it receives:

```
Blocked by podbox network policy: host "pypi.example.org" is not allowed.
Ask the user to run: podbox policy allow claude-my-project pypi.example.org
```

From another terminal on the host:

```sh
podbox policy allow claude-my-project pypi.example.org            # this sandbox, applied within a second
podbox policy allow claude-my-project pypi.example.org --scope project   # writes .podbox/policy.yaml
podbox policy reload claude-my-project      # apply hand edits to .podbox/policy.yaml and .sandboxignore
podbox policy show claude-my-project
```

## Commands

| Command | Role |
| --- | --- |
| `run <agent> [path] [--name]` | create if absent, start, open an agent session |
| `create <agent> [path] [--name]` | create a sandbox without starting it |
| `start <name>` / `stop <name>` | start (recreating stopped containers with fresh masks) / stop |
| `rm <name> [--volumes]` | remove containers, network and state; `--volumes` also drops the agent login |
| `ls` | list sandboxes, from podman labels and the state directory |
| `exec <name> -- <cmd>...` | run a command in the workspace, exit with its code |
| `attach <name>` | open a new agent session in a running sandbox |
| `git-check <name> [--accept]` | compare `.git` with the fingerprint taken at creation, exit 6 on suspicious changes; `--accept` re-records it |
| `policy show\|allow\|deny\|reload <name> [host]` | inspect and change the allowlist from the host; `reload --forget-missing` also drops recorded masks found nowhere |
| `image build <image>... [--force]` / `image ls` | build `proxy`, `base`, `claude`, `kiro`, `shell` |
| `doctor` | check prerequisites |
| `version` | build information |

Every command accepts `--output json` (`-o json`).

## Configuration

`~/.config/podbox/config.yaml` (`PODBOX_HOME` relocates config and state):

```yaml
images:
  base: registry.corp.example/node:22-bookworm-slim   # Debian-based; default docker.io/library/node:22-bookworm-slim
ca_file: corp-ca.pem          # trusted in agent images when your proxy intercepts TLS
upstream_proxy: http://user:pass@proxy.corp:3128   # default: host HTTPS_PROXY; "none" disables
limits: { cpus: "4", memory: 8g, pids: 2048 }      # applied when cgroup controllers are delegated
```

Project files: `.podbox/policy.yaml` (allowlist layer, versioned) and `.sandboxignore` (gitignore syntax, extra masks). podbox reads them once at `create`, printing each imported rule and pattern, and keeps a copy in the sandbox state; later edits, by you or the agent, apply only after you review them and run `podbox policy reload <name>`. Inside the sandbox they are read-only.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | generic error (sandbox exists, sandbox not running, ...) |
| 2 | usage error (bad flag, argument, agent, `--output` value or policy rule, unsafe workspace) |
| 3 | prerequisite missing (see `podbox doctor`) |
| 4 | not found (sandbox) |
| 5 | runtime error (podman or a host file operation failed, e.g. an unwritable state directory) |
| 6 | `git-check` found suspicious `.git` changes |
| 7 | masks cannot be verified: the workspace or one of its directories is unreadable (`create`, `start`, `policy reload`), a recorded masked path is missing or now a symbolic link (`start`; restore it or `policy reload --forget-missing`), or the mask resolution is internally inconsistent; see [workspace masking](docs/security-model.md#workspace-masking) |

`exec`, `attach` and `run` exit with the inner command's code. A suspicious `.git` change only prints a warning on these and on `start`, `stop`, `rm` and `ls` (and adds `git_integrity` to their JSON); see [git directory integrity](docs/security-model.md#git-directory-integrity). With `--output json`, errors are printed on stdout as `{"error":{"code":<exit code>,"kind":"...","message":"..."}}`.

## Known limits

- Containers share the host kernel; a kernel exploit escapes the sandbox.
- Allowlisted hosts are an exfiltration channel: allow only what the task needs.
- HTTPS is not decrypted in v0.1: rules apply to host names only, and refusals of `CONNECT` requests are a bare `403` for most clients (plain-HTTP requests show the message).
- Behind a TLS-intercepting corporate proxy, the agent sees and trusts the corporate CA.
- Masks are resolved when the agent container is (re)created; files created later are not masked until `stop` + `start`. Writes into masked directories are refused. Masks follow their files when a parent directory is renamed (inode tracking; not on Windows hosts).
- An allowlisted host is reachable on ports 80 and 443 only; list other ports with `ports:` on the rule or `podbox policy allow <name> <host> --port N`.
- Only HTTP(S) leaves the sandbox: no SSH git remotes, no databases.
- macOS and Windows are designed for but not tested in CI.

## License

Apache-2.0
