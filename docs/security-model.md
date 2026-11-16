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

1. Policy: any matching deny wins, then any matching allow, otherwise deny. A rule matches a host and a port: an allow opens ports 80 and 443 only unless it lists `ports`, a deny without `ports` covers every port. An invalid or missing layer contributes nothing.
2. Destination guard: the proxy resolves the name once, checks every address and dials the checked address (no DNS rebinding). Loopback, link-local, cloud metadata (`169.254.169.254`, `fd00:ec2::254`), unspecified, multicast and reserved ranges are always refused; private ranges (RFC 1918, ULA, CGNAT) only with `allow_private` on the matching rule.
3. Upstream: with a corporate proxy, the name is still resolved and checked locally; the CONNECT sent upstream carries the name so the corporate proxy applies its own rules. When the name does not resolve locally (DNS-less networks), it is forwarded by name. The agent never learns the upstream address or credentials: they are passed to the proxy container only.

Each decision is logged as one JSON line on the proxy's stdout, read by `podbox policy log <name>`.

The readable refusal (`Blocked by podbox network policy ... Ask the user to run: podbox policy allow ...`) reaches plain HTTP clients only. An HTTPS request is a `CONNECT`: clients such as curl, npm and pip report the proxy's `403` and drop its body, so neither the agent nor you see the host. `podbox policy log <name> --denied` is the way to find it, and `run`/`attach` point at it after a session with refusals.

## Refused workspaces

`create`, `run` and `start` refuse (exit code 2, `unsafe workspace`) a workspace that:

- is your home directory or one of its parents (including `/`), which would expose every dot-directory of your home;
- equals, contains or lies inside a podbox directory: config (`~/.config/podbox`), state (`~/.local/state/podbox`), cache (`~/.cache/podbox`) or `PODBOX_HOME`. The state holds the effective policy the proxy reads; an agent able to write it could allowlist any host.

Both sides are compared after resolving symbolic links, so a link to a refused directory is refused too. `start` checks again, so a workspace path later replaced by such a link is caught.

## Workspace symbolic links

The agent can create symbolic links anywhere in the project, pointing at any host path. podbox never follows one on the host: every host-side read, write, walk or check of project content (the project policy and `.sandboxignore`, `podbox policy allow|deny --scope project`, the mask walk and its secret scan, the git fingerprint, hook files, `.git/config`) goes through an `os.Root` opened on the workspace, which cannot resolve outside it, and refuses any symbolic link or special file on the way, so a link to another project path is refused too. Each directory step is checked to be the directory that was inspected, so a link swapped in meanwhile is refused.

- Reading a project file through a link: ignored with a warning (the snapshot treats it as absent).
- `--scope project` when `.podbox` or `.podbox/policy.yaml` is a link or not a plain directory and regular file: refused with exit code 2 (`unsafe workspace`), nothing written anywhere. The file is otherwise replaced atomically (temporary file and rename inside `.podbox`), so the write also never reaches another file through a hard link.
- Walks list a link as a link (the git fingerprint and hook files record its target) and never descend into it.
- Mount sources: right before the agent container is created, every bind source inside the workspace (the project, `.git`, `.git/config`, hook directories, `.podbox`, `.sandboxignore`) is checked again, component by component: a link or special file there makes `create` or `start` fail with exit code 2 instead of mounting it. podman resolves the source itself afterwards, so a window remains between this check and the mount (an agent in a second sandbox on the same workspace swapping a directory for a link); avoid running two sandboxes on one workspace with untrusted code.
- Watched git files (`config`, `commondir`, `gitdir`, ...) are hashed as a stream up to 1 MiB, hook files up to 16 MiB; a larger file is not read through and is reported as `oversized` at every check.

## Workspace masking

Forced, non-overridable patterns: `.env`, `.env.*` and `.envrc` (env templates `.env.example`, `.env.sample`, `.env.template` and `.env.dist` stay visible so the agent can read the expected configuration; add them to `.sandboxignore` to hide them), `*.pem`, `*.key`, `id_*`, `.ssh/`, `.aws/`, `.kube/`, `.gnupg/`, `.npmrc`, `.pypirc`, `.netrc`, `.git-credentials`, `.docker/config.json`, and `.git/config` mounted read-only (it can name `core.hooksPath`, `core.fsmonitor` or filter commands that git on the host would run). `.git` is bound on itself so it cannot be renamed and replaced. `.git/hooks/` is read-only too (see [git hooks](#git-hooks)). Objects, refs, index and `HEAD` stay writable, so the agent can still commit; `git config` fails inside the sandbox. `.sandboxignore` adds patterns, read from the snapshot taken at `create` or `podbox policy reload`, so the agent cannot drop its own masks. Files are covered by a read-only bind of `/dev/null`, directories by a read-only bind of an empty directory (a tmpfs over a path inside a bind mount does not hide it). Uncovered files that look like secrets (name and entropy heuristic) produce a warning at start.

Masks follow their files. The agent cannot move a masked file (its mount is busy), but it can rename a parent directory, after which a pattern with a slash (`/config/prod.yaml`, `.docker/config.json`) no longer matches. So every masked path is recorded with its device, inode and type in the sandbox state (`masks.json`) when it is first masked, and each (re)creation of the agent container masks what the patterns match now plus every recorded file or directory, wherever it now lives (a rename keeps the inode) and at its recorded path. A recorded path found elsewhere prints a `MASKED PATHS MOVED` warning listing `old -> new (still masked)`. A recorded path that still exists stays masked there, even replaced (a host editor that saves by rename gives the file a new inode at the same path; its record is updated) or turned into a directory. Hard links are masked too: masks are resolved in two passes, so the walk order never matters. The first collects every path the patterns match or the record names, and every file inside a masked directory; the second masks, and records, every workspace path sharing the device and inode of one of them, whether the link sorts before or after the masked file and whether you or the agent made it (`echo x >n.key; ln n.key n2` in the sandbox masks both at the next start). A FIFO, socket or device found at a recorded path is masked there like a file; a symbolic link found there is never mounted over, so `start` refuses (exit 7) and names it as `<path> (now a symbolic link, …)`. Any internal inconsistency of the resolution also refuses the start with exit code 7 (`masks`) and a JSON error under `--output json`, never a crash. The record only grows at `start`. `podbox policy reload` never drops a record found at another path (it stays masked where it is now, even once its pattern is removed: move it back on the host to unmask it); it drops, and prints as `- mask <path>`, the records still at their recorded path that no pattern matches any more and that the next start would no longer mask: a hard link of a kept record, or of a file inside a masked directory, stays recorded and is not listed.

The mask walk fails closed. Any error while walking the workspace outside a masked directory (a directory podbox cannot read, e.g. one the agent made `chmod 000`, or an I/O error) refuses `create`, `start` and `policy reload` with exit code 7, naming the path and the fix on the host (`chmod u+rx <path>`): podbox never starts the agent with a partial mask set, since the agent could restore the permissions and read what was not masked. An unreadable directory that is itself masked is not read and does not fail. A recorded path found neither at its path nor elsewhere (by inode) also refuses `start` with exit code 7, listing each missing entry: restore it, or forget it explicitly with `podbox policy reload <sandbox> --forget-missing`, which prints each dropped entry as `- mask <path> (missing)`. A plain `policy reload` keeps missing records and warns. A refused `start` removes the containers it created or started (the proxy included), so nothing of the sandbox is left running; a refused `create` removes everything it made. On Windows hosts no inode is available: masks follow the patterns and the recorded paths only, so a renamed parent directory unmasks its files at the next start.

## Git hooks

Hooks run inside the sandbox: `.git/hooks/` and every `.git/modules/*/hooks/` that exists is bound read-only on itself, so the project's hooks run for the agent's commits but the agent cannot add, change or replace them. An absent `.git/hooks/` is not created on the host; one the agent creates (it can, since `.git` is writable) is reported by the [integrity check](#git-directory-integrity), like a new submodule hook.

The residual risk is hook managers that read worktree files: `core.hooksPath` pointing inside the project, `.husky/`, `.githooks/`, `lefthook.yml` (`lefthook.yaml`, `.lefthook.yml`) and `.pre-commit-config.yaml`. The agent can edit them like any project file, and your next commit on the host runs what it wrote. This is the same risk class as `package.json` scripts or a `Makefile`, and podbox detects it rather than prevents it:

- At `create`, `start` and `run`, when the project has such files, podbox prints one `info:` line naming them (and an `info` field in the JSON output of `create` and `start`).
- Their sha256 (recursively for directories, bounded to the first 1000 files in name order; symbolic links by target) is part of the git fingerprint. Past the cap, every check reports one finding, `hook files exceed cap (N > 1000), not fully verified`, instead of per-file changes. Each changed, added or removed file is reported after the session under `GIT HOOK FILES CHANGED`, with the other findings: review the diff before committing on the host, then `podbox git-check <name> --accept`.

For untrusted code, use [clone mode](#clone-mode). Hook installers fail inside the sandbox: `husky` cannot set `core.hooksPath` because `.git/config` is read-only, and `lefthook install` or `pre-commit install` cannot write the read-only `.git/hooks/`. Run the install once on the host.

## Git directory integrity

The agent must write `.git` to commit, and git on the host trusts what it finds there. Some writes make host git run commands or read another repository, and cannot be masked without breaking git: `.git/commondir` pointing at a config the agent controls (its `core.fsmonitor` runs on the next `git status`), `config.worktree`, `objects/info/alternates`, `info/*`, symbolic links (including `HEAD` or refs) pointing outside `.git`, unexpected new top-level entries, hooks created in an absent `.git/hooks/`, submodule configs and hooks under `.git/modules/`, a pending rebase with `exec` lines, or a `.git` created or repointed in the sandbox. Linked worktrees (`git worktree add` on the host) read their git directory `.git/worktrees/<name>/` in the workspace: its `commondir` sends git in that worktree to another git directory, so an agent can plant one with `core.fsmonitor` and repoint it. For each `.git/worktrees/<name>/` (and `.git/modules/*/worktrees/<name>/`), `commondir`, `gitdir`, `config.worktree`, symbolic links, rebase todos and unknown entries are fingerprinted; `HEAD`, `ORIG_HEAD`, `index`, `logs`, `refs` and `locked` are churn. A worktree the agent adds with `git worktree add` is accepted when its files are what git writes (`commondir` = `../..`).

podbox detects these, it does not prevent them. At `create` (or the first `start` of an older sandbox) it records a fingerprint of `.git` in the sandbox state: its kind, top-level entries, the sha256 of the files above and every symbolic link. The object stores (`objects/` except `objects/info/`, in `.git` and in each submodule git directory) and `lfs/` are not walked, so the check stays fast on large repositories; only the symbolic links placed directly in them or in their `pack/` directory are recorded, and `objects/info/` is walked on its own. A link deeper inside, for example one loose object in a fan-out directory, is not seen. Normal churn (objects, refs, logs, index, `HEAD` content, `ORIG_HEAD`, `FETCH_HEAD`, `packed-refs`, worktree state, ...) is left out, so commits, branches, checkouts, rebases, stashes and gc raise nothing.

The live `.git` is compared with the fingerprint after `run`, `attach` and `exec` return, and on `start`, `stop`, `rm` and `ls`. Any difference prints a warning on stderr, once per command (`ls` groups sandboxes sharing a workspace and findings), listing each path and why it matters, and adds `git_integrity` (`ok`, `findings`) to the JSON output; exit codes are unchanged. `podbox git-check <name>` runs the comparison alone and exits 6 on findings. On a warning, do not run git in that repository until you have reviewed the listed paths; remove what you did not create, then `podbox git-check <name> --accept` re-records the fingerprint. A change you made yourself on the host (for example `git remote add`, which rewrites `.git/config`) is reported too and needs the same acceptance.

The check runs when podbox does: a git command you run on the host before any podbox command is not protected. For untrusted work, prefer [clone mode](#clone-mode), which keeps the agent away from the host repository's git directory; `git-check` does not apply to a clone sandbox.

## Clone mode

With `--clone` the agent's workspace is its own git clone of the project's committed HEAD, not the project directory. It is the mode for code you do not trust.

What the agent can reach: the clone, mounted at the project's usual path, and nothing of the host repository. It holds committed content only: no uncommitted or untracked file, no host remote, no git configuration, no hook, no credential.

How the clone is made, and why:

- podbox clones from a `file://` URL with `--no-hardlinks`. A plain-path clone copies the whole object directory, including unreachable objects such as a deleted secret; the `file://` transport sends only what the cloned branch reaches, and no object is shared or hard-linked with the host repository, so the agent cannot alter the host's object store.
- The clone starts from an empty template directory, so no host hook is installed, and `origin` and the branch tracking are removed, so no remote or URL remains. podbox checks the fresh configuration holds none.
- The clone is made with git's global and system configuration ignored, so an `insteadOf` or a protocol rule of the host cannot redirect or refuse it, and from a percent-encoded `file://` URL, so a path with spaces or `#` is read as itself. The Windows form of that URL (a drive path) is designed for but untested.
- The clone lives in `<state>/clones/<name>`, outside the sandbox's own state directory that the proxy container mounts, so the proxy never sees files the agent wrote. It is still inside podbox's state directory, so the workspace refusal (`CheckRoot`) is applied to the project path only and never to the clone, and the mask engine walks the clone like any workspace.

Why `.git` is writable in a clone: the shared-mode protections exist because git on the host trusts a git directory the agent can write. Here that directory is the agent's own, and the host never runs git inside it. Everything that runs git on the host goes through one package (`internal/hostgit`) with an environment stripped of every `GIT_*` variable and with fsmonitor, hooks and external protocols disabled, and it runs only on the host repository. All the git protections are switched off in one place (`gitguard.Guard`).

Masks, `.sandboxignore`, the network policy, the agent context and every container hardening guarantee are the same as in shared mode. The configuration files (`.sandboxignore`, `.podbox/policy.yaml`) are read from the host project, snapshotted at creation and reloaded only by `podbox policy reload`, as in shared mode: the clone's copies are ignored, because the agent can rewrite them. Only the tree the masks are resolved against is the clone. A tracked file masked by a pattern is empty in the agent's working tree even when the pattern's file is untracked or gitignored on the host. The mask does not reach the clone's history: a secret ever committed on the cloned branch, even one deleted since, is readable by the agent with `git show`, `git log -p` or the object files, exactly as in shared mode, so rotate it or clone a branch without it; the agent can still commit the file as emptied, which is why a later export must flag masked paths.

A podbox that predates this mode ignores the mode of a clone sandbox's record and would mount the project directory: remove clone sandboxes before downgrading.

### Reviewing a clone: `podbox diff`

`diff` runs git inside the agent container, as the agent user, with the project path as `safe.directory`, the host's configuration absent, `core.pager`, `diff.external` and `core.fsmonitor` neutralized and `--no-ext-diff --no-textconv`, on a temporary index file (`GIT_INDEX_FILE`) removed afterwards. The host never runs git in the clone. The agent's index, configuration and objects are not written. Read this as a limit, not a guarantee: the data describes what the agent says it changed, produced by a git it controls through the clone. Treat the patch as untrusted text, review it before running or merging anything, and note that a clean-filter or similar attribute the agent set still runs inside the sandbox, never on the host.

Everything the agent wrote is made inert on the host before it is printed or returned in JSON (`internal/termsafe`): ESC sequences (CSI, OSC such as clipboard writes and titles, DCS, APC, PM, SOS, two-character escapes), C0 controls except newline and tab, DEL, carriage return (which overwrites a line), the C1 range and invalid UTF-8 are removed or replaced, and bidirectional controls (which reorder text) are shown as `<U+XXXX>`. An escape sequence never reaches past its line, so a missing terminator cannot hide what follows. File names are cleaned the same way and a newline or tab in one becomes `?`; git's own quoting of unusual paths is switched off (`core.quotepath=off`) and the result cleaned. Output past 32 MiB is refused rather than truncated. `diff` refuses a shared sandbox (its workspace is your own repository) and a stopped one, and reads nothing else.

The commits the agent makes stay in the clone. Limits: submodules and LFS only as a plain clone gives them; no git identity is set; there is no export of the agent's work to the host in this release, only `podbox diff` to review it.

## Agent context

At every `start`, podbox tells the agent that it is sandboxed and what to ask the human when a request is refused. The text is the same for every sandbox of an agent and names none, because the agent home volume is shared by all of them: it points to `$PODBOX_SANDBOX` and to `/run/podbox/policy.json`.

- `claude`: a podbox block between `<!-- podbox:begin -->` and `<!-- podbox:end -->` markers in `~/.claude/CLAUDE.md`, and the skill `~/.claude/skills/podbox-sandbox/SKILL.md`. Text outside the markers is kept; repeated starts never duplicate the block.
- `kiro`: `~/.kiro/steering/podbox.md`, loaded in every interaction.
- `shell`: nothing.
- Files are written inside the agent container, in the agent home volume, and rewritten at each start from the running podbox version, so an upgrade reaches existing sandboxes. Only podbox's own part is restored: the owned files and the marked block. Text the agent adds outside the markers persists. A symbolic link at a target is followed and its mode kept. A file that cannot be merged (a begin marker without end marker, or larger than 1 MiB) is left as is and `start` warns; a failed write only warns.
- Nothing is written in the project, in the host `$HOME` or in the host's agent configuration.

The agent also reads its policy at `/run/podbox/policy.json`, a read-only mount of a directory podbox keeps for it (never the sandbox state directory). It is refreshed with the effective policy, so it is current after `policy allow|deny|reload` without a restart. It shows the sandbox, the workspace and, per rule, `host`, `allow` and `ports`: no layer, no warning and no `allow_private`. An agent running from before this feature needs a restart to get the mount.

The context informs; it does not enforce. An agent that ignores it is still bound by the proxy and the container.

## Known egress channels

Every allowlisted host can receive what the agent sends it. Among the defaults:

- `github.com`: `git push` and the web endpoints accept data for any account or token the agent holds (a token it finds in the project, or one it is given).
- `*.githubusercontent.com`: mostly downloads, but it is a wildcard over user-controlled content hosts.
- Package registries (`registry.npmjs.org`): `npm publish` with any token the agent holds uploads a package anyone can read. PyPI uploads go to `upload.pypi.org`, which is not allowed.
- The agent's own API hosts (`*.anthropic.com`, `*.claude.ai`, `*.claude.com`, the Kiro hosts): they carry the conversation by design.

To narrow them, deny what the task does not need. A deny wins over every allow in any layer, so a global deny in `~/.config/podbox/policy.yaml` applies to all sandboxes:

```yaml
rules:
  - host: github.com
    allow: false
  - host: "*.githubusercontent.com"
    allow: false
```

For one project, put the same rules in `.podbox/policy.yaml`, or run `podbox policy deny <sandbox> <host>` for the current sandbox only.

## What it does not protect against

- Kernel or runtime exploits: containers share the host kernel.
- Image builds on Linux behind a host-loopback proxy run on the host network, so build steps (podbox's own Containerfiles, which install the agent CLI from its registry) can reach host loopback services. Built images do not keep the proxy variables.
- Exfiltration through allowlisted hosts: anything the agent can reach can receive data.
- A TLS-intercepting corporate proxy: its CA is visible to and trusted by the agent (hiding it needs TLS interception in the sandbox, planned for v0.2).
- Files created in the project after the agent container was created are not masked until it is recreated (`stop` + `start`).
- Inode reuse: a recorded masked file deleted on the host may have its inode reused by a new file, which is then masked at the next start until `podbox policy reload` (over-masking, never under-masking), and the deleted record is not reported missing.
- Project files at `create`: `.podbox/policy.yaml` and `.sandboxignore` are imported as found, possibly written by an agent in an earlier sandbox; `create` prints each imported rule and pattern (and `imported` in its JSON output) for review.
- Code the agent writes into the project runs on the host when you run it; review changes before executing them. This includes worktree hook files run by your next commit (see [git hooks](#git-hooks)).
- Other git directory files stay writable: an agent can write `.git/commondir` and similar files that make host git run its commands. podbox detects them after the session (see [git directory integrity](#git-directory-integrity)) but does not block them.
- When `.git` is a file (a worktree or a submodule), the git directory it points to is outside the workspace and its hooks and config are not protected; podbox warns at start. Its `.git` pointer itself stays agent-writable; a change to it is reported by the git integrity check.
