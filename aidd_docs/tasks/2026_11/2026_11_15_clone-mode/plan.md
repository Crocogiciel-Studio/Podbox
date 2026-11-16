---
objective: "A developer can run an agent on untrusted code in a podbox clone sandbox, review what it changed with podbox diff, and bring its commits back to the host as a new branch or a patch without the agent ever touching the host repository."
status: in-progress
---

# Plan: Clone mode

Source: aidd_docs/tasks/2026_11/2026_11_15_clone-mode/spec.md

## Phases

| #   | Phase | File | Depends on |
| --- | ----- | ---- | ---------- |
| 1   | Clone sandboxes: --clone, state, mounts, git switch, ls, rm, doctor, docs, e2e suite | [`phase-1.md`](./phase-1.md) | none |
| 2   | podbox diff: review the clone's changes, sanitized | [`phase-2.md`](./phase-2.md) | 1 |
| 3   | podbox export: verified bundle to a new branch, or a patch file | [`phase-3.md`](./phase-3.md) | 2 |

## External sources

| URL | What it settled |
| --- | --------------- |
| none | the behaviors of `git clone`, `git bundle`, `git fetch` and `git update-ref` the plan relies on are checked by the e2e suite on the CI git, not assumed from the manuals |

## Impact

| Category | Verdict and evidence | Handled in |
| -------- | -------------------- | ---------- |
| runtime | git on the host is new and needed only for `--clone` and `export` (`diff` runs git in the sandbox only); the agent image already ships git (`internal/images/defs/base.Containerfile` line 8, observed); the host git version behaviors (`--no-write-fetch-head`, `git bundle create -` to stdout) are unverified here and asserted by `test/e2e/clone.sh` on the CI runner; bundle and `format-patch` output sizes are unbounded, so host reads are capped; three prerequisites are unverified here and settled by a task each: a host git version floor of 2.29 (`--no-write-fetch-head`; `hostgit.Available` refuses an older git and `doctor` warns, phase 1 tasks 1 and 7), git ownership trust inside the container (the clone is owned by the host user mapped to uid 1000 exactly like a shared workspace, so "dubious ownership" is not expected; the e2e suite asserts git works in the clone and every in-container git call passes `safe.directory` for the project path as a safeguard, phases 1 and 2), and the agent's git identity (no host config is carried, so podbox sets none; the e2e commits with an identity passed in the environment and the README says the agent or the human configures it) | 1, 2, 3 |
| regression | `Meta`, `Store`, `Manager.Create/Remove/List/Start/createAgent`, `agentSpec`, `ResolveMasks`, `Snapshot/Reload/Drift`, `gitcheck`, `readOnlyProject`, `cli` create/run/rm/ls/git-check/policy, doctor change; covered by `internal/sandbox/*_test.go`, `internal/gitguard/*_test.go`, `internal/doctor/checks_test.go`, `internal/cli/exitcode_test.go` and the e2e suites evasion, lifecycle, agentctx; risk medium (the mount source and every host-side workspace read now go through one accessor); no separate safety-net phase: phase 1 adds the accessor first with shared behavior pinned by the existing tests, which must pass unchanged | 1 |
| architecture | starts from: `Meta.Workspace` is both the host directory mounted and the in-container path, and `gitguard` functions are called directly by `sandbox` and `cli`. Intends: `Meta.Workspace` stays the project path (label, container path, agent view, `Exec` workdir); `Store.Source(meta)` returns the host directory that is mounted and scanned (the project for shared, the clone for clone); `gitguard.Guard` is the single switch; `internal/hostgit` is the only place that runs git on the host; `diff` and `export` data comes from `Manager` execs inside the agent container | 1, 2, 3 |
| mask and refusal rules | observed: `workspace.CheckRoot` refuses any directory equal to, inside or containing a podbox-owned directory (`guard.go`, the `within` test in both directions), and is called only on the project path (`Create`, `Start`), never on a mount source; `workspace.OpenRoot`, `Resolve`, `Shrink` and `checkMounts` take a plain directory and never call `CheckRoot`, so a clone under the state directory is scanned and mounted without rejection as long as no new code calls `CheckRoot` on it; the clone lives in `<state>/clones/<name>`, outside `<state>/sandboxes/<name>` which the proxy container mounts, so the proxy never sees agent-written files; a unit test pins both facts | 1 |
| documentation | README (commands table, ls columns, rm behavior, exit code notes, known limits), `docs/security-model.md` (new Clone mode section; the two "upcoming `--clone`" sentences; git-check not applicable; diff and export trust model), `CONTRIBUTING.md` (e2e line, host git rule), `gitguard.Notice` text | 1, 2, 3 |
| contributor checks | `make lint test`, `make cross`, `make e2e` (CONTRIBUTING.md "Checks before a pull request"; `Makefile`; CI in `.github/workflows/ci.yml`); `make e2e` and CI must run the new `test/e2e/clone.sh` | 3 |

## Rules

| Rule | Why it applies |
| ---- | -------------- |
| Repo content in English | open source repository: code, docs, specs, commits, PRs |
| Commits without Co-Authored-By or Claude-Session lines | user rule, also when briefing an executor |
| Never `sed -i`; `test/e2e/*.sh` stay 100755 | `sed -i` drops the exec bit; `clone.sh` is created executable |
| Conventional Commits, one stacked pull request per phase | CONTRIBUTING.md and the spec's delivery section |
| Every git protection lives in `internal/gitguard`; podbox state files go through `fsutil.WriteAtomic` | CONTRIBUTING.md conventions |
| Every command supports `--output json` and uses the documented exit codes | CONTRIBUTING.md; the spec adds no exit code |
| Tests on the Mac go through `dist/` | user working style: `make cross` builds `dist/` for the Mac check of the CLI-only parts |

## Decisions

| Decision | Why |
| -------- | --- |
| The clone lives in `<state>/clones/<name>`, a sibling of `<state>/sandboxes`, not inside `<state>/sandboxes/<name>` | `Store.Remove` deletes the sandbox directory, so the clone survives `rm` without special casing; a sandbox named `x.clone` cannot collide with a directory scan; the proxy mounts `sandboxes/<name>` and must not see agent-written files; it is still under the state directory, so the mask engine and `os.Root` walk it like a workspace |
| `Meta.Workspace` stays the host project path; `Store.Source(meta)` is the host directory mounted and scanned; every host-side read or write of workspace content (masks, project file snapshots, drift, `policy allow --scope project`, mount checks) goes through it | the container path, labels, agent view and `exec` workdir must match shared mode; one accessor means a missed call site fails to compile in review, not at runtime; project files are read from the clone, so `.sandboxignore` and `.podbox/policy.yaml` are the committed ones, and `policy allow --scope project` writes into the clone, never the host project |
| `CheckRoot` is applied to the project path only, never to the clone directory | the clone directory is inside the owned state directory, which `CheckRoot` refuses by design; the agent still cannot reach `sandboxes/<name>` (policy, fingerprint, masks record) because only the clone is mounted |
| `gitguard.Guard` is the only switch: one `Active()` decision, with the read-only binds, the `.git` self-bind, the fingerprint record and check, the pointer warning and the hook notice all asked of the Guard | the spec requires one place; callers never test the mode themselves |
| The clone is made with `git clone --no-hardlinks --single-branch --no-tags` from a `file://` URL with an empty template directory, then `origin` and branch tracking are removed | a plain-path clone copies the whole object directory, including dangling and unreachable objects (a deleted secret), and the agent could read them; the `file://` transport sends only what the cloned branch reaches; the empty template brings no host hook; removing `origin` leaves no host remote; the flag the spec names is kept |
| All host-side git goes through `internal/hostgit`: an environment stripped of every `GIT_*` variable, no prompt, `core.fsmonitor=false`, `gc.auto=0`, `protocol.ext.allow=never`, capped output, never run inside a clone | the host's git must not be steered by the environment or by the agent-writable directory; one package makes the rule reviewable |
| `diff` and `export` read the sandbox through execs in the running agent container and fail with the existing not-running error (exit 1, `run podbox start <name>`) instead of starting it | starting recreates containers and rewrites the agent context, which a read command must not do; the spec allows either |
| `diff` computes its file list in a temporary index (`GIT_INDEX_FILE` in `/tmp`: `read-tree <base>`, `add -N .`, then `diff <base>`) | commits, uncommitted tracked changes and untracked files appear in one diff and the agent's own index is never touched |
| `export --as branch` streams `git bundle create -` from the sandbox to a private temporary file, runs `git bundle verify`, fetches into a temporary ref with `transfer.fsckObjects`, then creates the branch with `git update-ref <ref> <sha> <zero-oid>` and deletes the temporary ref | the zero old value makes "never overwrite" atomic, a fetch straight into `refs/heads/...` would fast-forward an existing branch and refuses the checked-out one; the working tree, index and config are never written |
| `export --as patch` streams `git format-patch --stdout --binary --full-index <base>..<branch>` and writes it with `O_EXCL` | one file, applies with `git apply` on the base commit, never overwrites |
| `create --clone` requires the project path to be the repository top level and HEAD to exist; `run` on an existing sandbox with `--clone` set on a shared sandbox is refused, and without the flag on an existing clone sandbox it attaches to the clone | a subdirectory would give an ambiguous clone path; the mode is fixed at creation, and the refusal only goes in the direction where silently running shared would weaken what the user asked for |
| `rm` keeps the clone and reports its path, `rm <name> --volumes` removes it, and `rm` also finds a clone with no sandbox left; `ls` lists such orphan clones with state `missing` | the refusal message of `create` names `podbox rm <name> --volumes`, which must work after the sandbox is gone |
| podbox sets no git identity in the clone and carries no host git configuration | the spec forbids carrying host configuration; an identity is a host value and the agent home volume is shared by sandboxes of one agent; the documentation tells the user how to give the agent one |
| No exit code is added: not a git repository, empty repository, wrong directory and not a clone sandbox are usage (2); git missing on the host is prerequisite (3); an existing clone, branch or file, a failed bundle verify and an oversized stream are generic (1); an unknown sandbox is 4 | the spec allows an addition only when unavoidable, and each case maps to an existing meaning |
