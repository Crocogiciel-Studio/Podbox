---
status: done
---

# Instruction: podbox export

## Architecture projection

> Tree of the final files from the project root, each folder ending in `/`. ✅ create · ✏️ modify · ❌ delete

```txt
.
├── CONTRIBUTING.md                ✏️
├── README.md                      ✏️
├── docs/
│   └── security-model.md          ✏️
├── internal/
│   ├── hostgit/
│   │   ├── hostgit.go             ✏️
│   │   └── hostgit_test.go        ✏️
│   ├── sandbox/
│   │   ├── inspect.go             ✏️
│   │   ├── export.go              ✅
│   │   └── export_test.go         ✅
│   └── cli/
│       ├── export.go              ✅
│       ├── export_test.go         ✅
│       ├── root.go                ✏️
│       └── exitcode_test.go       ✏️
└── test/
    └── e2e/
        └── clone.sh               ✏️
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| -------- | -------- | ------------------ | ----- |
| Setup | a running clone sandbox whose agent made two commits on its branch, plus one uncommitted edit and one untracked file; host repository hashed (`.git/config`, index, HEAD, working tree, `git status`) | agent work to export, host baseline recorded | cli |
| Happy path | `podbox export <name> --as branch` | exit 0; the branch `podbox/<name>/<agent branch>` exists in the host repository with the two commits; the output names it and reports 1 uncommitted and 1 untracked file as not exported | cli |
| Happy path | compare the host repository with the baseline | working tree, index, HEAD, current branch, `.git/config`, hooks and every other ref unchanged; no `FETCH_HEAD`, no temporary ref, no stray temporary directory | cli |
| Happy path | `export --as branch --branch-name review/x -o json` | JSON with `as`, `branch`, `base`, `commits`, `head`, and `not_exported` counts | cli |
| Happy path | `export --as patch` then `git apply` on a fresh checkout of the base commit | `<name>.patch` in the current directory applies cleanly and the resulting tree equals the agent's committed tree | cli |
| Happy path | `export --as patch --out other.patch -o json` | the file is written at that path and the JSON reports it and the not exported counts | cli |
| Edge case | export the same branch name twice, and a name that is a branch of the host | exit 1, the existing branch is untouched, the message suggests `--branch-name` | cli |
| Edge case | export the current branch name of the host as the target | refused like any existing branch, the checkout is untouched | cli |
| Edge case | export the patch to an existing file | exit 1, the file is byte-identical afterwards | cli |
| Edge case | the agent made no commit | exit 0, nothing created, "nothing to export" with the uncommitted counts | cli |
| Edge case | detached HEAD in the clone; the agent branch name carries a leading dash, spaces or control characters | detached is refused with a hint to create a branch; an invalid name is refused by `check-ref-format` and never reaches an argv unquoted | cli |
| Edge case | the base commit no longer exists on the host (gc or a rewritten history) | `git bundle verify` fails, exit 1, nothing created, message names the missing prerequisite | cli |
| Edge case | a corrupt or truncated bundle stream, and a stream over the cap | nothing created in the host repository and no temporary file left | cli |
| Edge case | the agent commits an emptied or edited file that a `.sandboxignore` pattern of the host masks | `export` exits 1 naming the masked path and exports nothing; with `--include-masked` it proceeds and `masked_touched` lists the path | cli |
| Edge case | the sandbox is stopped, shared, or unknown; `--as` missing or invalid; `--out` with branch or `--branch-name` with patch | exit 1, 2, 4, 2, 2 respectively | cli |
| Teardown | `rm --volumes`, delete the exported branch and the patch files, remove the temporary directories | no podman object, no clone, host repository as at setup | cli |

## Tasks to do

### `1)` Host side verification and import

> The host ingests only an inert bundle, verified, into a ref that cannot overwrite.

1. In `hostgit` add `CheckRefName(ctx, ws, name)` (`git check-ref-format refs/heads/<name>`), `RefExists`, `BundleVerify(ctx, ws, file)`, `BundleHeads(ctx, ws, file)` (`git bundle list-heads`), and `ImportBranch(ctx, ws, file, srcRef, target)`.
2. `ImportBranch` fetches with `-c transfer.fsckObjects=true -c fetch.fsckObjects=true --no-tags --no-write-fetch-head --no-recurse-submodules` into a temporary ref `refs/podbox/tmp/<random>`, creates `refs/heads/<target>` with `git update-ref refs/heads/<target> <sha> 0000000000000000000000000000000000000000`, then deletes the temporary ref, also on failure; it never writes the index, the working tree or the config.
3. Create files for bundle and patch streams in a private directory made by `os.MkdirTemp` with mode 0700, always removed.

### `2)` Bundle and patch from inside

> `Manager` produces the bundle or the patch in the sandbox and reports what is left out.

1. In `export.go` add `Manager.ExportBranch(ctx, name, opts)`: through `runIn`, read the agent branch (`symbolic-ref --short HEAD`; detached is refused), validate it and the target with `CheckRefName`, read the commit count (`rev-list --count <base>..<branch>`), return a no-op result when it is zero, stream `git bundle create - <branch> ^<base>` into the private file with the size cap, then `BundleVerify`, `BundleHeads` (the head must equal the sandbox's reported head), `ImportBranch`.
2. Add `Manager.ExportPatch(ctx, name, out)`: stream `git format-patch --stdout --binary --full-index --no-signature <base>..<branch>` and write it to `out` created with `O_CREATE|O_EXCL|O_WRONLY` (0644), removing a partial file on failure; the in-container commands use the same hardened `-c` options as `Diff`.
3. In `inspect.go` factor the worktree status listing of phase 2 into a helper that returns the uncommitted and untracked counts; both exports report them as `NotExported`.
4. Default branch target `podbox/<sandbox>/<agent branch>`; the default patch file `<sandbox>.patch` in the current directory.

### `3)` The command

> `podbox export <sandbox> --as branch|patch [--branch-name] [--out]`.

1. Create `cli/export.go`: `--as` required (`branch` or `patch`), `--branch-name` only with branch, `--out` only with patch, other combinations are usage errors (2); text output names what was created and says "not exported: N uncommitted, M untracked"; `-o json` returns `as`, `branch` or `file`, `base`, `head`, `commits`, `exported`, and `not_exported`.
2. Register it in `root.go`; map branch-exists, file-exists and verify failures to generic (1) with the suggestion; git missing stays exit 3; the host never merges, checks out or pushes.
3. Print names from the sandbox through `termsafe.CleanName`.
4. Refuse or flag every exported path that is masked in the sandbox (`Store.ResolveMasks` on the clone tree, host `.sandboxignore` snapshot): the agent sees a masked tracked file as empty and can commit it emptied, so an export would otherwise overwrite the real secret on the host. List the masked paths the commits touch (from `diff --name-status <base>..<branch>` run in the sandbox); `export` exits 1 naming them unless `--include-masked` is passed, and reports them in JSON (`masked_touched`).

### `4)` Tests

> The git invocations, the refusals and the JSON contract are unit tested.

1. `hostgit_test.go`: `ImportBranch` on a temporary repository with a real bundle: new ref created, existing ref refused and untouched, the current branch refused, no `FETCH_HEAD`, temporary ref removed, working tree and index hashes unchanged; verify failure on a truncated bundle and on a missing prerequisite.
2. `export_test.go` (sandbox and cli): argument combinations, detached refusal, no-commit no-op, cap, `O_EXCL` refusal, JSON shape, exit codes.

### `5)` e2e, docs and contributor checks

> The clone suite covers export; the docs close the loop; the gates pass.

1. Extend `test/e2e/clone.sh` with an "export" section from the Test Scope; keep 100755.
2. README: `export <name> --as branch|patch` in the commands table with its flags and refusals, the workflow "run, diff, export, review on the host, merge yourself", the not-exported rule, exit codes unchanged, known limit (submodule and LFS content is not exported); security model: the bundle is verified and fetched with fsck into a never-overwritten ref, git runs in the sandbox, the host never runs git in the clone; CONTRIBUTING: the e2e line lists the clone suite with diff and export.
3. Run `make lint test`, `make cross` and `make e2e`; confirm no `sed -i` was used and `test/e2e` scripts are 100755.

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | a bundle is imported only after `git bundle verify`; the new branch is created atomically and never overwrites; the host working tree, index, config and other refs are unchanged; temporary files and refs are removed on success and on failure |
| 2 | the bundle holds only commits after the base; a patch file is written once and never replaced; uncommitted and untracked work is reported as not exported; no commit means nothing created |
| 3 | `export --as branch` and `--as patch` work with their flags and defaults; invalid combinations exit 2; an existing branch or file exits 1 with a suggestion; JSON reports what was created; an export whose commits touch a masked path is refused (exit 1, the paths named) unless `--include-masked` is passed, and the paths are in the JSON |
| 4 | `make test` passes with the new tests |
| 5 | `clone.sh` prints no FAIL for the export section, the patch applies with `git apply` on the base commit, the docs describe the workflow and limits, and `make lint test`, `make cross` and `make e2e` pass |
