# Run: Clone mode

## Run 1

- Head: `4432342`
- Phase: 1 only (phases 2 and 3 are planned on other branches)
- Left: no Mac or Windows check; the Windows `file://` form of a drive path is covered by a unit test of the URL only; git 2.29 (the floor) not exercised, the suite ran on git 2.53; the host-side `GIT_CONFIG_GLOBAL` variable is ignored by git before 2.32, so `HOME` and `XDG_CONFIG_HOME` are pointed at an empty directory too
- Deviations from the plan:
  - the clone is staged in `clones/.partial-<name>`, not `<dest>.partial`: a sandbox named `foo.partial` is valid and would have lost its clone
  - `.sandboxignore` and `.podbox/policy.yaml` (snapshot, drift, reload, `policy allow --scope project`) are read from the host project, not from the clone, which the agent can rewrite; only the tree the masks are resolved against is the clone (`Store.Source`); the plan's decision on `Store.Source` for project files is superseded
  - `hostgit.Clone` ignores the host's global and system git configuration; `Inspect` keeps it
  - `Images.Ensure` runs before the second `Inspect`, so the base commit and counts are current; a first `Inspect` still fails fast before any image build
  - `git-check` asks `Guard.Active()`; no new method
  - `Remove` also removes a leftover staging directory with `--volumes`
  - `lib.sh` gained `upclone`, unused by `clone.sh` which creates and starts through `pb` to keep the create output; kept for the next suites
  - phase 3 gained a task and a criterion: refuse or flag exported paths that are masked

| Task | Validation | Result |
| ---- | ---------- | ------ |
| 1 | `go test ./internal/hostgit` (environment filter, cap, `Inspect` counts and sentinels, clone without origin, hook, alternate, dangling object or hard link, awkward path, host config ignored, failure leaves nothing) | pass |
| 2 | `go test ./internal/gitguard ./internal/sandbox` (inactive guard protects nothing, active equals the old functions, notice text) | pass |
| 3 | `go test ./internal/sandbox` (legacy meta loads as shared, unknown mode refused, `Source`, clone outside the sandbox directory) | pass |
| 4 | `TestAgentSpecOfAClone`, `TestCloneMasksComeFromTheHostProjectConfig`, `test/e2e/clone.sh` create and start sections | clone mounted on the project path, no git bind, host `.sandboxignore` masks a tracked secret, `CheckRoot` pinned off the clone |
| 5 | `TestRemoveKeepsTheCloneUntilVolumes`, `TestStagingNeverCollidesWithAClone`, e2e "rm keeps the clone" | kept, removable after the sandbox is gone, orphan listed, `foo` and `foo.partial` independent |
| 6 | `go test ./internal/cli` and the e2e refusals, run, ls, git-check sections | exits 1, 2, 3, 4 as planned, JSON `mode`, `clone`, `applicable` |
| 7 | `go test ./internal/doctor` | git ok, warn when missing or too old, never fail |
| 8 | `make test` | pass |
| 9 | `test/e2e/clone.sh` | 66 passed, 0 failed |
| 10 | review of README, `docs/security-model.md`, CONTRIBUTING | no "upcoming" sentence left, downgrade and Windows limits stated |
| 11 | `make lint test`, `make cross`, `make e2e`, `git ls-files -s test/e2e` | all exit 0, five scripts 100755, no `sed -i` |

## Run 2

- Head: `4ea98d2`
- Phase: 2 only (`podbox diff`); phase 3 is planned on another branch
- Left: no Mac or Windows check; the suite ran on git 2.53 on the host and the agent image's git; a stopped-container diff and a hostile-config diff are covered, a diff of a repository with submodules is not; the planted `filter` driver case is documented as running inside the sandbox, not tested
- Deviations from the plan:
  - the e2e section is a new suite `test/e2e/clonediff.sh` (wired into `make e2e` and CI), not an addition to `clone.sh`
  - `Manager.Diff(ctx, name, withPatch)` takes a flag so `--stat` never fetches or caps the patch; `runIn` takes the already loaded `Meta` after `cloneMeta` checked clone and running once
  - an escape sequence ends at the end of its line at the latest, instead of running to the end of the input, so an unterminated OSC cannot hide the rest of a patch from the reviewer
  - bidirectional controls become a visible `<U+XXXX>` and invalid UTF-8 becomes U+FFFD, rather than being silently removed
  - extra git neutralizing: `core.fsmonitor=false`, `diff.noprefix` and `diff.mnemonicPrefix` reset, explicit `--src-prefix` and `--dst-prefix`, `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1` in the container
  - untracked files come from `ls-files --others` on the agent's own index (read only); the temporary index is used for the diff
  - the exit-code unit table gained the two sentinels; the `diff <shared|unknown>` exits are asserted by the sandbox tests and the e2e suite, since the CLI table cannot run a command that needs the engine
  - the "stopped sandbox" e2e uses `stop` then `diff`; the "agent changed nothing" case runs first

| Task | Validation | Result |
| ---- | ---------- | ------ |
| 1 | `go test ./internal/termsafe` (every sequence class, truncated and line-bounded sequences, UTF-8 kept, C1 as UTF-8 and raw bytes, bidi, names) | pass |
| 2 | `go test ./internal/sandbox` (`-z` parsers with renames, spaces, newline and ESC in names, binary; argv, env, user, workdir, temporary index removed last; cap; stopped, shared, unknown, invalid base; stderr cleaned) | pass |
| 3 | `go test ./internal/cli` (text, `--stat`, empty, JSON shape, `ErrNotClone` exit 2, `ErrTooLarge` exit 1) | pass |
| 4 | `make test` | pass |
| 5 | `test/e2e/clonediff.sh` and README and security model review | 54 passed, 0 failed |
| 6 | `make lint test`, `make cross`, `make e2e`, `git ls-files -s test/e2e`, no `sed -i` | all exit 0, six scripts 100755 |

## Run 3 (phase 2 repair)

- Head: `6e549e4`
- Phase: 2 repair after review
- Left: no Mac or Windows check; no submodule with a populated checkout; the "rewritten loose base object" case is documented as undetectable here, not tested
- Deviations from the plan and the review:
  - `--` follows the base in `git diff <base> --` (the form that separates revisions from paths); `read-tree` takes the validated hex base without it
  - the timeout knob is the environment variable `PODBOX_DIFF_TIMEOUT` (seconds) read by `podbox diff`; the default stays 120 s
  - timeout exit codes 124 and 137 map to `ErrTimeout` (generic, exit 1)
  - ignored files are listed with `--directory`, so a fully ignored directory counts once, and the list is read up to 1 MiB (`ignored_capped`)
  - C1 controls are now visible `<U+XXXX>` instead of removed; escape sequences are no longer skipped: ESC becomes a marker and the payload stays as text
  - the `C` status stays parsed with two names for safety but is shown as `unknown` (no `-C` is asked)

| Item | Validation | Result |
| ---- | ---------- | ------ |
| 1 replace objects | unit env test; e2e plants a replace ref to a commit equal to the worktree, diff unchanged | pass |
| 2 hooks, timeout | unit argv test (`timeout -k 5 120`, hooksPath), 124 and 137 mapping; e2e post-index-change hook not run, hanging clean filter gives exit 1 "timed out after 5s", no sleep left | pass |
| 3 termsafe | table tests with markers, invisible code points, hidden payload | pass |
| 4 completeness | ignored count, gitlink flag, single raw+numstat call with mismatch errors, sparse and submodule settings; e2e nested repo and ignored file | pass |
| 5 status | unknown mapping test | pass |
| 6 errors | stderr truncation test, error wording test | pass |
