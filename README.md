# podbox

Run coding agents (Claude Code, Kiro) autonomously in a hardened rootless Podman sandbox whose only network egress is an allowlisting proxy.

- The agent container has no route out, no DNS, and no access to the host or its loopback services; its only exit is the sandbox proxy.
- The proxy allows only listed hosts (deny by default) and answers refusals with a message the agent can read and relay.
- The project is mounted read-write; secrets (`.env`, `.env.*` except templates such as `.env.example`, keys, `.ssh/`, ...) are always hidden. `.git/config` and `.git/hooks/` are read-only (project hooks still run for the agent's commits); other `.git` changes that git on the host could execute, and edits to worktree hook files (`.husky/`, `lefthook.yml`, `.pre-commit-config.yaml`, `core.hooksPath`, ...), are reported after each session. Your home directory and podbox's own config/state directories are refused as workspaces.
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

When the agent hits a host outside the allowlist over plain HTTP it receives:

```
Blocked by podbox network policy: host "pypi.example.org" is not allowed.
Ask the user to run: podbox policy allow claude-my-project pypi.example.org
```

Over HTTPS, curl, npm or pip show only `403`. `podbox policy log` shows what was refused and the command that allows it, and `run`/`attach` print `podbox: N request(s) blocked during this session — see: podbox policy log <name> --denied` when the session had refusals.

From another terminal on the host:

```sh
podbox policy log claude-my-project --denied      # refused requests, each with its podbox policy allow command
podbox policy allow claude-my-project pypi.example.org            # this sandbox, applied within a second
podbox policy allow claude-my-project pypi.example.org --scope project   # writes .podbox/policy.yaml
podbox policy reload claude-my-project      # apply hand edits to .podbox/policy.yaml and .sandboxignore
podbox policy show claude-my-project
```

## Commands

| Command | Role |
| --- | --- |
| `run <agent> [path] [--name] [--clone]` | create if absent, start, open an agent session |
| `create <agent> [path] [--name] [--clone]` | create a sandbox without starting it; `--clone` gives the agent its own git clone of the committed HEAD instead of the project directory, see [clone mode](#clone-mode) |
| `start <name>` / `stop <name>` | start (recreating stopped containers with fresh masks, and rewriting the [agent context](docs/security-model.md#agent-context) in the agent home) / stop |
| `rm <name> [--volumes]` | remove containers, network and state; `--volumes` also drops the agent login and a clone sandbox's clone, which `rm` otherwise keeps and locates |
| `ls` | list sandboxes (name, agent, mode `shared` or `clone`, state, proxy, workspace), from podman labels and the state directory; a clone left with no sandbox shows with state `missing` |
| `exec <name> -- <cmd>...` | run a command in the workspace, exit with its code |
| `attach <name>` | open a new agent session in a running sandbox |
| `diff <name> [--stat \| --show-binary]` | what the agent changed in a running clone sandbox since its base commit: commits, uncommitted changes and untracked files, as a file summary, a total and a patch; `--stat` stops after the summary; `--show-binary` also prints the content of NUL-containing files; exit 2 for a shared sandbox or `--stat` with `--show-binary`, 4 for an unknown one, 1 when the sandbox is stopped, a git command times out (120 s) or the patch exceeds 32 MiB (use `--stat`) |
| `git-check <name> [--accept]` | compare `.git` with the fingerprint taken at creation, exit 6 on suspicious changes; `--accept` re-records it; says it does not apply (exit 0) to a clone sandbox |
| `policy show\|allow\|deny\|reload <name> [host]` | inspect and change the allowlist from the host; `reload --forget-missing` also drops recorded masks found nowhere |
| `policy log <name> [--denied] [--follow] [--since 10m]` | the proxy's decisions, with the fix command for each refusal; exit 4 for an unknown sandbox, 1 when it is not running |
| `image build <image>... [--force]` / `image ls` | build `proxy`, `base`, `claude`, `kiro`, `shell` |
| `doctor [--pull]` | check prerequisites, including that `images.base` is present; `--pull` pulls it when absent; git is reported as optional (a warning, never a failure) |
| `version` | build information |

Every command accepts `--output json` (`-o json`).

## Clone mode

```sh
cd ~/code/untrusted-project
podbox run claude --clone
```

By default the agent works on your project directory (`shared` mode). With `--clone` it works on its own git clone, made once at creation from the repository's committed HEAD, and your working tree is never mounted. Use it for code you do not trust: the agent cannot reach your `.git`, so your hooks, configuration and objects stay out of its reach. The mode is fixed at creation and shown by `ls`.

- The clone holds the committed content only. `create` prints the base commit, the clone path and how many uncommitted and untracked files were left out.
- It lives in podbox's state directory (`<state>/clones/<name>`) and appears in the agent at the project's usual absolute path, so paths and tools match shared mode.
- It shares no object with your repository and carries no remote, no git configuration, no hook and no credential. podbox sets no git identity either: the agent or you configure one (`git config` in the clone, or `GIT_AUTHOR_*` and `GIT_COMMITTER_*` in the environment).
- Masks, `.sandboxignore`, the network policy and every container guarantee apply unchanged. `.sandboxignore` and `.podbox/policy.yaml` are read from your project directory, once at creation, exactly as in shared mode (`policy reload` and `policy allow --scope project` use the project's files too); the clone's own copies, which the agent can rewrite, are ignored. So a `.sandboxignore` that is untracked or gitignored on your side still masks a tracked secret: the file is empty in the agent's working tree. An untracked secret is not in the clone at all. **The mask hides the file in the working tree only: the clone holds the cloned branch's whole history, so a secret that was ever committed on it (even one deleted since) is readable by the agent with `git show` or `git log -p`.** Rotate such secrets, or clone a branch without them.
- The git protections of shared mode (read-only hooks and config, fingerprint, `git-check`, hook notice) are off: the repository is the agent's, and the host never runs git inside it.
- `rm` keeps the clone because it holds the agent's work, and prints where; `rm <name> --volumes` deletes it. `create` refuses a name whose clone remains.
- Needs git 2.29 or newer on the host, for `--clone` only; `podbox doctor` warns when it is missing.

### Reviewing the agent's work

```sh
podbox diff claude-project          # summary, total, then the patch
podbox diff claude-project --stat   # summary only
podbox diff claude-project --show-binary   # also print files that hold NUL bytes
podbox diff claude-project -o json
```

`diff` needs a running clone sandbox (it never starts one) and compares the clone's files with the base commit `create` printed: commits the agent made, uncommitted changes, every file the agent's git tracks (even one its `.gitignore` lists) and untracked files that git does not ignore. It reads the clone with git inside the sandbox, on a temporary index, so the agent's index, configuration and objects are untouched and your repository is never read.

- Text: one line per file (`STATUS`, `ADDED`, `DELETED`, `PATH`; `untracked` for a file git does not track yet; notes in brackets for a nested repository or submodule, of which only the commit is compared, a symlink, an executable, a binary file), a total, then notes on what the list leaves out or what to look at twice, then the patch.
- JSON: `schema` (1), `name`, `base`, `files`, `ignored` (with `ignored_capped`), `case_collisions` and `non_ascii` with examples, and `patch`, which `--stat` omits. Each file has `path`, `path_b64`, `old_path` and `old_path_b64` for a rename, `status` (`added`, `modified`, `deleted`, `renamed`, `typechange`, `unmerged` or `unknown`), `added`, `deleted`, `binary`, `binary_attr`, `untracked`, `gitlink`, `symlink`, `exec`, `mode`, `old_mode`, `id` and `old_id` (git blob ids when git has them). No change gives an empty `files` array. `path` is for display only, because cleaning it is lossy: `path_b64` is the raw path, base64 encoded, and the only thing to look a file up by.
- Filters: `diff` shows the raw bytes of each file and never runs a git filter. When the agent's configuration defines filters, or its attributes give a file a `filter`, `ident` or `working-tree-encoding`, the text starts with a `WARNING` line and marks the file, and the JSON lists them (`filters`, `conversions`): what the agent's own git would commit may differ from the bytes shown.
- A file that git ignores and the agent never committed or staged is not in the diff: only the count of such paths is shown (a directory counts once), because the agent can keep files there; look in the clone when the count is not what you expect.
- Binary: a file whose content holds a NUL byte (a binary, but also a script with one NUL, or UTF-16 text) is listed as binary and its content is not printed (no base85 patch); the stub gives its size and how it starts, and `--show-binary` prints it, cleaned (NUL shows as `<U+0000>`) and cut at 256 KiB per file. `binary_hidden` is true in the JSON when the content is not in the output, so an export can refuse or ask. A file whose patch is past 4 MiB is replaced by a "too large" stub (`too_large`). Git attributes (`* binary`, `-diff` in a `.gitattributes` or in the agent's `.git/info/attributes`) cannot make text unreadable: `diff` always reads the content, and a file such attributes call binary but whose content is text is shown as text, with `binary_attr` set and a note.
- Names: paths that differ only by case, and paths with non-ASCII characters (which can imitate ASCII ones), are counted with a few examples. A file name with a newline or tab shows `?` there.
- Terminal sequences are made inert and nothing is dropped silently (look-alike spaces such as the no-break space are marked too): an escape sequence becomes a marker (`<CSI>`, `<OSC>`, `<ESC>`, ...) followed by its payload as plain text; every other control character (NUL, BEL, backspace, a lone CR, DEL, the C1 range) and every invisible code point (zero-width and joiner characters, bidirectional controls, Unicode tags, variation selectors, soft hyphen, fillers, private use, noncharacters) becomes `<U+XXXX>`. The only exception is the CR of a CRLF line ending, which reads as a newline. A literal `<CSI>` or `<U+200B>` in a file is written `<U+003C>CSI>` so a marker you read is always a real one. Emoji sequences show their joiners and selectors the same way.
- The diff is the agent's text: do not paste it into an AI assistant or another tool that follows instructions without care (it can carry a prompt injection), any more than you would run it.
- It is a point-in-time view of a live worktree, not a snapshot of what an export will bring: the agent can keep working while you read.
- Each git command is stopped after 120 seconds (exit 1, "git timed out"), and the output is capped at 32 MiB; past it `diff` exits 1 with nothing printed and suggests `--stat`.

`podbox export` comes next. Until then, read the clone at the path `create` printed.

## Configuration

`~/.config/podbox/config.yaml` (`PODBOX_HOME` relocates config and state):

```yaml
images:
  base: registry.corp.example/node:22-bookworm-slim   # Debian-based; default docker.io/library/node:22-bookworm-slim
ca_file: corp-ca.pem          # trusted in agent images when your proxy intercepts TLS
upstream_proxy: http://user:pass@proxy.corp:3128   # default: host HTTPS_PROXY; "none" disables
limits: { cpus: "4", memory: 8g, pids: 2048 }      # applied when cgroup controllers are delegated
```

Image builds use the host proxy variables. A proxy on the host loopback (px, cntlm, a Zscaler client: `HTTPS_PROXY=http://127.0.0.1:...`) is reached as `host.containers.internal` from the podman machine on macOS and Windows; on Linux such builds run on the host network, since rootless build steps otherwise get their own network namespace.

Project files: `.podbox/policy.yaml` (allowlist layer, versioned) and `.sandboxignore` (gitignore syntax, extra masks). podbox reads them once at `create`, printing each imported rule and pattern, and keeps a copy in the sandbox state; later edits, by you or the agent, apply only after you review them and run `podbox policy reload <name>`. Inside the sandbox they are read-only.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | generic error (sandbox exists, sandbox not running, a clone remains for the name, mode mismatch on `run`, ...) |
| 2 | usage error (bad flag, argument, agent, `--output` value or policy rule, unsafe or missing workspace; `--clone` on a directory that is not the top level of a git repository with a commit) |
| 3 | prerequisite missing (see `podbox doctor`; git for `--clone`) |
| 4 | not found (sandbox) |
| 5 | runtime error (podman or a host file operation failed, e.g. an unwritable state directory) |
| 6 | `git-check` found suspicious `.git` changes |
| 7 | masks cannot be verified: the workspace or one of its directories is unreadable (`create`, `start`, `policy reload`), a recorded masked path is missing or now a symbolic link (`start`; restore it or `policy reload --forget-missing`), or the mask resolution is internally inconsistent; see [workspace masking](docs/security-model.md#workspace-masking) |

`diff` uses no new code: usage (2) for a shared sandbox, not found (4), generic (1) for a stopped sandbox or an oversized patch. `exec`, `attach` and `run` exit with the inner command's code. A suspicious `.git` change only prints a warning on these and on `start`, `stop`, `rm` and `ls` (and adds `git_integrity` to their JSON); see [git directory integrity](docs/security-model.md#git-directory-integrity). With `--output json`, errors are printed on stdout as `{"error":{"code":<exit code>,"kind":"...","message":"..."}}`.

## Known limits

- Containers share the host kernel; a kernel exploit escapes the sandbox.
- Allowlisted hosts are an exfiltration channel: allow only what the task needs.
- HTTPS is not decrypted in v0.1: rules apply to host names only, and refusals of `CONNECT` requests are a bare `403` for most clients (plain-HTTP requests show the message); `podbox policy log` names them.
- Behind a TLS-intercepting corporate proxy, the agent sees and trusts the corporate CA.
- Masks are resolved when the agent container is (re)created; files created later are not masked until `stop` + `start`. Writes into masked directories are refused. Masks follow their files when a parent directory is renamed (inode tracking; not on Windows hosts).
- An allowlisted host is reachable on ports 80 and 443 only; list other ports with `ports:` on the rule or `podbox policy allow <name> <host> --port N`.
- Only HTTP(S) leaves the sandbox: no SSH git remotes, no databases.
- The agent context tells Claude and Kiro that they are sandboxed and what to ask the human; it informs and enforces nothing, the proxy and the container do. It is verified by the e2e suite, not against a live Claude or Kiro session.
- Clone mode: a podbox older than this release does not know the mode and would treat a clone sandbox as a shared one, mounting your project directory: remove clone sandboxes before downgrading. Clone mode on Windows (the `file://` URL of a drive path) is designed for but untested. A repository's submodules and LFS files are only what a plain `git clone` gives; each clone is a full copy of the repository's history on disk; a masked tracked file is empty in the clone, so a commit by the agent can record it as emptied; there is no way back to the host yet in this release.
- macOS and Windows are designed for but not tested in CI.

## License

Apache-2.0
