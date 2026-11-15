---
status: pending
---

# Instruction: Clone sandboxes

## Architecture projection

> Tree of the final files from the project root, each folder ending in `/`. ✅ create · ✏️ modify · ❌ delete

```txt
.
├── .github/
│   └── workflows/
│       └── ci.yml                 ✏️
├── CONTRIBUTING.md                ✏️
├── Makefile                       ✏️
├── README.md                      ✏️
├── docs/
│   └── security-model.md          ✏️
├── internal/
│   ├── hostgit/
│   │   ├── hostgit.go             ✅
│   │   └── hostgit_test.go        ✅
│   ├── gitguard/
│   │   ├── guard.go               ✅
│   │   ├── guard_test.go          ✅
│   │   ├── report.go              ✏️
│   │   └── hooks_test.go          ✏️
│   ├── sandbox/
│   │   ├── clone.go               ✅
│   │   ├── clone_test.go          ✅
│   │   ├── state.go               ✏️
│   │   ├── sandbox.go             ✏️
│   │   ├── agentspec.go           ✏️
│   │   ├── masks.go               ✏️
│   │   ├── project.go             ✏️
│   │   ├── gitcheck.go            ✏️
│   │   ├── sandbox_test.go        ✏️
│   │   └── project_test.go        ✏️
│   ├── workspace/
│   │   └── guard_test.go          ✏️
│   ├── doctor/
│   │   ├── checks.go              ✏️
│   │   └── checks_test.go         ✏️
│   └── cli/
│       ├── create.go              ✏️
│       ├── lifecycle.go           ✏️
│       ├── ls.go                  ✏️
│       ├── gitcheck.go            ✏️
│       ├── policy.go              ✏️
│       ├── doctor.go              ✏️
│       ├── env.go                 ✏️
│       ├── root.go                ✏️
│       └── exitcode_test.go       ✏️
└── test/
    └── e2e/
        ├── lib.sh                 ✏️
        └── clone.sh               ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| -------- | ------ | ------------------ | ----- |
| Setup | a host repository with a committed tracked `.env`, a committed file, an uncommitted edit, an untracked file, an untracked `.env.local`, a host remote, a host `user.email` in `.git/config`, a `.git/hooks/pre-commit`, and a dangling blob | repository ready, its `.git` hashed and `git status` saved | system |
| Happy path | `podbox create shell <repo> --clone`, then `start` | exit 0; the create output reports the base commit, the clone path and the counts left out (1 uncommitted, 2 untracked) | cli |
| Happy path | inside the sandbox: `pwd`, `git log`, `ls`, `git remote`, `git config --list`, `ls .git/hooks`, `cat .env` | cwd is the project path; one commit history of HEAD; only committed files; no remote; no host config value; no hook; the tracked `.env` is empty (masked); the untracked `.env.local` and the uncommitted edit are absent | cli |
| Happy path | inside the sandbox: `stat -c %h` on every pack and loose object, `ls .git/objects/info/alternates`, `git cat-file -e <dangling blob>` | link count 1, no alternates, the dangling blob is absent | cli |
| Happy path | the agent commits in the clone and writes `.git/config` and `.git/hooks/x`; host `.git` hash and `git status` compared | host repository, working tree and hooks byte-identical | cli |
| Happy path | `git-check <name>`, `git-check <name> -o json`, `start`, `stop` | exit 0, text says it does not apply, JSON `applicable` is false and `git_integrity` is null; no hook notice on create or start | cli |
| Happy path | `ls`, `ls -o json` | a MODE column and a `mode` field with `clone`; the clone path in JSON | cli |
| Happy path | `rm <name>`, then `create` the same name | `rm` prints the kept clone path and the clone remains with the agent's commit; `create` is refused with exit 1 and a message holding `podbox rm <name> --volumes`; `ls` shows the orphan clone | cli |
| Happy path | `rm <name> --volumes` | the clone directory is gone and no podman object is left | cli |
| Edge case | `--clone` on a directory that is not a git repository, on a repository with no commit, on a subdirectory of a repository | exit 2 each, the message names the cause, no state and no clone left | cli |
| Edge case | `--clone` with git missing from PATH | exit 3 with an install hint, nothing left behind | cli |
| Edge case | git inside the clone as the agent user, with and without an identity in the environment | `git status` works with no ownership error; `git commit` succeeds when `GIT_AUTHOR_*` and `GIT_COMMITTER_*` are set and fails with git's own identity message otherwise | cli |
| Edge case | detached HEAD on the host | the clone is made at that commit, `create` reports it | cli |
| Edge case | a tracked file masked by `.sandboxignore` committed in the repo; restart | still masked after `stop` and `start`; the agent's commit survives the restart | cli |
| Edge case | `run <agent> <repo> --clone --name x` when `x` is a shared sandbox, and `run` without the flag when `x` is a clone | the first is refused with exit 1 naming the mode, the second attaches to the clone | cli |
| Edge case | a shared sandbox created in the same run | the shared behaviors are unchanged: `git-check` compares, hooks and config are read-only, existing suites pass | cli |
| Edge case | `doctor` with and without git in PATH (unit, injected) | `git` check is ok or warn, never fail, and the exit code is 0 when it is the only warning | system |
| Teardown | `rm --volumes` for every sandbox, remove the temporary directories | no podman object, no clone directory left | cli |

## Tasks to do

### `1)` Host git package

> One package runs git on the host, with a controlled environment, and nothing else does.

1. Create `internal/hostgit` with `Run(ctx, dir, stdin, args...)`: environment filtered of every `GIT_*` variable, `GIT_TERMINAL_PROMPT=0`, `LC_ALL=C`, leading `-c core.fsmonitor=false -c gc.auto=0 -c protocol.ext.allow=never -c core.hooksPath=/dev/null`, stdout capped (error past the cap), stderr trimmed into the error.
2. Add `Available() (path, version string, err error)` returning `ErrMissing` when git is not in PATH and `ErrTooOld` below 2.29 (both prerequisite, exit 3).
3. Add `Inspect(ctx, ws)` returning the repository top level (compared to the project path after resolving links), the HEAD commit, the branch name or empty when detached, and the counts of uncommitted tracked files and untracked files from `status --porcelain=v1 -z --untracked-files=all` (ignored files are not counted). Sentinels `ErrNotRepo`, `ErrNotTopLevel`, `ErrEmptyRepo`.
4. Add `Clone(ctx, ws, dest, base, branch)` that clones into a sibling `.partial` directory then renames it to `dest`: `file://` URL, `--no-hardlinks --single-branch --no-tags --template=` (an empty directory), `--branch <name>` when on a branch, otherwise `--no-checkout` then `checkout --detach <base>` run once in the fresh directory podbox just made; afterwards remove `origin` and branch tracking with `git remote remove origin` and `git config --unset-all` of the branch section, and verify `.git/config` holds no `url`, `remote` or host key. A failure removes the partial directory.

### `2)` Git guard, one switch

> The shared-mode git protections are asked of one object that is inactive for a clone.

1. Create `gitguard.Guard{Clone bool}` with `Active()`, and methods `ReadOnly(r)`, `GitDir(r)` (the `.git` self-bind), `Pointer(r)`, `Record(ws)`, `Check(ws, old)` and `Notice(name, ws)` that delegate to the existing functions when active and return the empty value when not.
2. Build the Guard in one `sandbox` helper from `Meta.IsClone()`; no other code reads the mode to decide a git protection.
3. Change the text of `gitguard.Notice` from "(coming soon)" to point at `--clone`, and update its test in `hooks_test.go`.

### `3)` Mode and clone state

> A sandbox knows its mode and where its clone is; the clone survives `rm`.

1. In `state.go` add to `Meta` the fields `Mode` (`shared` when empty, `clone`; an unknown value makes `Load` fail) and `Base`; add `Meta.IsClone()`.
2. Add `Store.ClonesRoot` (set to `<state>/clones` in `cli/env.go`), `Store.CloneDir(name)`, `Store.Source(meta)` (the clone directory for a clone, `Workspace` otherwise), `Store.HasClone(name)` and `Store.RemoveClone(name)`.
3. Add `clone.go` in `sandbox`: `CreateOptions{Clone bool}`, `CloneReport{Path, Base, Branch, Uncommitted, Untracked}`, the sentinels `ErrCloneExists` (message names `podbox rm <name> --volumes`), `ErrModeMismatch`, and a function `prepareClone` that runs the checks and the clone.
4. Add `LabelMode` to the container labels and `Status.Mode` and `Status.Clone` (JSON `mode`, `clone`), `shared` for containers without the label.

### `4)` Create and start a clone sandbox

> `Manager.Create` takes the mode and returns what the human must know; start mounts the clone.

1. Change `Create` to take `CreateOptions` and return `(meta, imp, report, err)`. For a clone, in this order: refuse an existing clone directory (before anything is made), require git, `Inspect`, ensure images, save the meta with `Mode` and `Base`, `hostgit.Clone`, then the existing snapshot, policy and container steps. The failure cleanup removes the clone this call made and nothing older.
2. In `createAgent` and everywhere the workspace is read on the host (`ResolveMasks`, `readProject`, `Reload`, `Drift`, `Shrink`, `OpenRoot`, `checkMounts`, `MaskWarning`) use `Store.Source(meta)`; the bind source is the clone for a clone and the target stays `WorkspaceTarget(meta.Workspace)`.
3. In `Start` keep `CheckRoot` on `meta.Workspace` only; refuse with a clear error when a clone sandbox's clone directory is missing; skip `RecordGit` for a clone; skip the pointer warning.
4. In `agentSpec` take the `.git` self-bind and the read-only git binds from the Guard (inactive for a clone); keep `.podbox` and `.sandboxignore` read-only binds for both modes.
5. In `Store.RecordGit` and `CheckGit` return nothing to compare for a clone (through the Guard).

### `5)` Remove, list, orphans

> `rm` keeps the clone, says where it is, and can still remove it later.

1. Change `Remove` to return a `Removed{ClonePath string, CloneKept bool}`; for a clone it deletes the clone only with `volumes`, otherwise keeps it; `status` counts an existing clone directory as found so `rm <name> --volumes` works after the sandbox is gone.
2. In `List` add orphan clones (a directory in `ClonesRoot` with no sandbox) with state `missing` and mode `clone`.
3. Make the Create failure cleanup call the new `Remove` in a way that removes the clone this call made.

### `6)` CLI surface

> `--clone` on create and run, mode in ls, advice on rm, git-check not applicable.

1. Add the `--clone` flag to `createFlags.bind`; pass it to `Create`; in `ensureSandbox` refuse `--clone` on an existing shared sandbox (exit 1, names the mode) and attach to an existing clone without the flag, printing an info line.
2. After create, print through `a.out.Info` the base commit (short), the clone path, and "N uncommitted and M untracked file(s) are not in the clone"; add a `clone` object to the create JSON (`path`, `base`, `branch`, `uncommitted`, `untracked`).
3. `hookNotice` and `gitCheck` return nothing for a clone; `git-check` prints "does not apply to a clone sandbox" and JSON `{"name", "applicable": false, "git_integrity": null}` with exit 0, also with `--accept`.
4. `rm` prints, in text and in the JSON `info`, the kept clone path and that `--volumes` removes it.
5. `ls` gains a MODE column and the JSON `mode` and `clone` fields.
6. `policy allow|deny --scope project` writes through `Store.Source`.
7. In `root.go` `classify`: `ErrNotRepo`, `ErrNotTopLevel`, `ErrEmptyRepo` map to usage (2), `hostgit.ErrMissing` to prerequisite (3), `ErrCloneExists` and `ErrModeMismatch` to generic (1).

### `7)` Doctor

> Git is reported as optional.

1. Add `Env.LocateGit` and a `git` check in `doctor.Run`: ok with the version, or `warn` (also for a version below 2.29) "git not found: `--clone` and `export` are unavailable" with an install fix; skipped when `LocateGit` is nil so the healthy fixture is unchanged; wire it in `cli/doctor.go`.

### `8)` Tests

> Unit tests lock the new rules and the unchanged shared behavior.

1. `hostgit_test.go`: environment filtering, the cap, `Inspect` counts and sentinels, `Clone` result (no origin, no template hook, no alternates, no dangling object).
2. `guard_test.go`: an inactive guard returns no read-only path, no self-bind, no record, no notice; an active one equals the old functions.
3. `sandbox_test.go` and `project_test.go`: `agentSpec` for a clone mounts the clone on the project path with no git bind; shared specs are unchanged; masks resolved on a clone directory under a fake state directory work while `workspace.CheckRoot` on that directory is refused (pin in `workspace/guard_test.go`: the clone directory must never be given to `CheckRoot`).
4. `clone_test.go`: `Meta` round trip and the legacy meta without mode; `Remove` keeps and then removes the clone; the orphan listing; `ErrCloneExists`.
5. `exitcode_test.go`: the new classifications; `checks_test.go`: git ok and warn, never fail.

### `9)` e2e suite and CI

> `test/e2e/clone.sh` verifies the done-when of this phase on every change.

1. Create `test/e2e/clone.sh` (mode 100755) from the scenarios of the Test Scope, with its own helper `upclone` added to `test/e2e/lib.sh`, a `cleanup` trap that removes sandboxes with `--volumes` and the temporary directories, and git identity passed with `-c` on every host git command.
2. Add the suite to the `e2e` target of the `Makefile` and a "Clone suite" step to `.github/workflows/ci.yml`.

### `10)` Documentation

> The README and the security model describe clone mode, its limits and when to use it.

1. README: `--clone` in the commands table for `create` and `run`; the `ls` mode; `rm` keeping the clone; `git-check` on a clone; `doctor` git line; a "Clone mode" paragraph (committed HEAD only, counts reported, lives in the state directory, not carried: uncommitted and untracked files, host remote, config and hooks); known limits (submodules and LFS only as a plain clone gives them, shared-mode git protections do not apply, no way back to the host yet in this phase, a full copy of the repository history on disk per clone, no git identity set by podbox); exit codes unchanged.
2. `docs/security-model.md`: a "Clone mode" section (what the agent can reach, why `file://`, why no host object is shared, why `.git` is writable and safe, mask behavior, the host never runs git in the clone); replace the "upcoming `--clone`" sentences in Git hooks and Git directory integrity.
3. `CONTRIBUTING.md`: the e2e line names the clone suite; a convention line: host-side git only through `internal/hostgit`, never inside a clone.

### `11)` Contributor checks

> The phase passes the repository's own gates.

1. Run `make lint test`, `make cross` and `make e2e`; confirm `git ls-files -s test/e2e` shows mode 100755 for every script and that no `sed -i` was used.

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | git runs on the host only through `hostgit.Run`, with no inherited `GIT_*` variable; non-repo, empty repo and subdirectory return their sentinels; the clone has no `origin`, no hook, no alternate, no unreachable object, and no hard link to the host |
| 2 | with a clone the Guard returns no read-only path, no self-bind, no fingerprint, no notice; with shared mode its answers equal the previous functions |
| 3 | an old `meta.json` without `mode` loads as shared; an unknown mode is refused; `Source` returns the clone only for a clone; the clone directory is outside `sandboxes/<name>` |
| 4 | `create --clone` leaves a clone with only committed content mounted at the project path, with masks applied and `CheckRoot` never called on it; a failed create leaves no state and no clone it made; an existing clone directory refuses before any object is made |
| 5 | `rm` keeps the clone and returns its path; `rm --volumes` removes it, also when no sandbox exists; `ls` lists an orphan clone |
| 6 | `--clone` works on create and run; `run` honors the mode rules; create prints the base, the path and both counts in text and JSON; `git-check` on a clone exits 0 and says it does not apply; `ls` shows the mode in text and JSON; usage and prerequisite failures use exits 2 and 3 |
| 7 | `doctor` shows git as ok or warn and exits 0 with only that warning |
| 8 | `make test` passes with the new and the unchanged tests |
| 9 | `test/e2e/clone.sh` prints no FAIL and the CI job runs it |
| 10 | README and `docs/security-model.md` describe the mode, its limits and when to use it, and no "upcoming" sentence remains |
| 11 | `make lint test`, `make cross` and `make e2e` pass and every e2e script is 100755 |
