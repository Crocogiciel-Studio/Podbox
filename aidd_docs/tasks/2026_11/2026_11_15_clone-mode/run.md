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

## Run 4 (phase 2, second repair)

- Head: `3f1a772`
- Left: no Mac or Windows check; a file under a populated submodule checkout; the agent's git is the image's 2.39 (`GIT_ATTR_SOURCE` unavailable)
- Design changes:
  - the temporary index is built as: `read-tree base`, then `add -f -N` of every path the agent's index or HEAD tracks that the base lacks and that exists on disk (paths on stdin with `--pathspec-from-file=- --pathspec-file-nul`, `GIT_LITERAL_PATHSPECS=1`), then the untracked list (`ls-files --others --exclude-standard` on the temporary index), then `add -N .`; "untracked" and "ignored" come from the temporary index, the agent's real index is only read to learn what is tracked; a path in HEAD but dropped from the index is kept only if it is still on disk (`ls-files --others`)
  - `ls-files --cached --deleted` lists every cached file, so the deleted list uses `--deleted` alone (a unit test panics on the combined form)
  - attributes: the diff always uses `--text`; `--binary` is never passed; a file is binary when its patch section holds a NUL byte; a second plain `--numstat` call gives git's own verdict, used for `binary_attr` and for files whose patch has no content (rename, mode change); with `--stat` the patch is still read, cut quietly at the cap
  - termsafe: every C0 control and DEL becomes `<U+XXXX>`, except the CR of CRLF (documented); forged markers are written `<U+003C>...`; invisible set is unicode Cf, Cc, Co plus an explicit list
  - JSON: one `sandbox.DiffResult` (also used by the CLI), `schema`, `path_b64`, `mode`, `old_mode`, `symlink`, `exec`, `id`, `old_id`, `binary_attr`, case-collision and non-ASCII notes
  - code split: `inspect.go` (exec helpers), `diff.go`, `diffparse.go`; `Manager.requireRunning` is shared by `Exec`, `ProxyLog` and the clone checks
- Deviations: ignored paths are listed with `--directory` (one per directory); a patch section is matched to a file by position; `PODBOX_DIFF_TIMEOUT` left as is

| Deal breaker | New e2e cases | Fail on the previous binary (3adfe9e) |
| ------------ | ------------- | ------------------------------------- |
| 1 ignored tracked file | committed, staged-with--f and dropped-from-index files listed, content in the patch, JSON untracked false, ignored.log only counted | yes: ran the new script against a build of 3adfe9e, 26 of 93 checks failed, these among them |
| 2 attributes | `* binary` plus `.git/info/attributes` `code.py -diff`: text readable, `binary_attr`, real binary still binary, same under `--stat` | yes, same run |
| 3 silent C0 | BS and CR and BEL visible as `<U+0008>`, `<U+000D>`, `<U+0007>` in content and name | yes, same run; plus table tests over every C0, DEL and C1 |

## Run 5 (phase 2, third repair)

- Head: `d48635e`
- Left: no Mac or Windows check; the e2e no longer has a case that makes git time out (nothing the diff runs can hang any more: filters, hooks, pager and fsmonitor are neutralised), so the timeout mapping (exit 124 and 137 to `ErrTimeout`) is covered by unit tests only; `text` and `eol` attribute conversions are not flagged and still apply to what git compares
- Design:
  - filters: the config is read with `git config --null --name-only --get-regexp '^filter\..*\.(clean|smudge|process|required)$'` (every source and include), then every name gets `clean=/bin/cat`, `smudge=/bin/cat`, `process=` (empty) and `required=false` through `GIT_CONFIG_COUNT`/`GIT_CONFIG_KEY_n`/`GIT_CONFIG_VALUE_n` (no "=" to split a name); a local experiment showed `required=false` is needed (an empty process with required=true fails) and that the result is the raw bytes; more than 100 filters refuses the diff
  - `git check-attr -z --stdin filter ident working-tree-encoding` on the changed paths gives per-file `conversions`; JSON `filters` is the configured and attribute-named filters; text gets a WARNING line and per-file notes
  - `--show-binary` (exclusive with `--stat`, exit 2): NUL files print cleaned, up to 256 KiB each with a "truncated, N bytes not shown" line; the default stub gives size, first bytes and the flag; JSON `binary_hidden`
  - per-file cap: a streaming `sectionWriter` keeps at most 4 MiB of each file's patch (8 KiB for `--stat`), counts all bytes and looks for NUL in all of them; a text file past the cap becomes a "Too large" stub (`too_large`); the overall 32 MiB cap on retained output stays (10 files over 4 MiB hit it)
  - HEAD-only paths are checked with `ls-files --others -- <200 paths>` and literal pathspecs, so a huge untracked tree cannot fail the diff
  - look-alike spaces (Zs other than U+0020) are marked `<U+XXXX>` in names and content
  - docs: shared-mode wording for the read-only `.git/config`, raw bytes and filter flags, `--show-binary`, per-file cap
- Deviations: the stub of a NUL file shows its first 16 bytes with newline and tab as `?`; termsafe, the diff and its tests went into one commit

| Deal breaker | New e2e cases | Fail on the previous build (8d4f142) |
| ------------ | ------------- | ------------------------------------ |
| 1 filter falsifies the diff | `filter.x.clean = echo harmless` with `evil.sh filter=x` and a required process filter: raw bytes in the patch, no `+harmless`, WARNING line, per-file conversion, JSON filters; a hanging clean and process filter is never run | yes: ran the new script against a build of 8d4f142, 20 of 115 checks failed, these among them |
| 2 one NUL hides a file | script with one NUL: hidden by default (stub with size, first bytes, flag), `binary_hidden`, visible with `--show-binary`, real binary printed only on request, `--stat` plus `--show-binary` exit 2 | yes, same run |
| per-file cap | one 36 MB file gives a stub and `too_large` while other files stay; 10 files past 4 MiB exit 1 | yes, same run |

## Run 6 (phase 3)

- Head: `4bada2e`
- Phase: 3 (`podbox export`)
- Left: no Mac or Windows check; git 2.29 (the floor) not exercised, the suite ran on git 2.53 on the host and 2.39 in the agent image; the Windows paths of the patch file are not tried; a bundle produced by a hostile git binary in the sandbox (as opposed to a hostile repository) is covered by unit tests with a fake stream only, since the agent cannot replace `git` in the image
- Design:
  - `hostgit.Host` (methods, not the package functions the plan names) runs every host command with the host's global and system configuration ignored, `HOME` in a private directory, hooks off, `maintenance.auto=false`; `ImportBranch` takes a `check` callback that sees the fetched tip before the branch is created, so the ancestry, masks and warnings are decided on objects the host holds
  - the bundle is `git bundle create - HEAD ^<base>`, so no agent-chosen ref name reaches an argument; the host requires one head, `HEAD`, equal to the HEAD read just before
  - the file list, NUL scan and commit list of a branch export come from the host (`diff-tree`, `diff --text` into the diff's `sectionWriter`, `log`); for a patch they come from the sandbox and `host_verified` is false
  - masks: the forced and `.sandboxignore` patterns, parent directories and the recorded masks, also lower-cased; raw paths only
  - not-exported counts come from `git status` in the sandbox on the hardened session; masked paths are left out because a masked tracked file is a device in the clone and always reads as changed
  - the diff and export share `gitSession` (refactor commit)
  - test knobs: `PODBOX_EXPORT_MAX_BYTES` (like `PODBOX_DIFF_TIMEOUT`), `Manager.MaxExport`
- Deviations: the e2e is a new suite `test/e2e/cloneexport.sh` (wired into `make e2e` and CI), not a section of `clone.sh`; a detached HEAD is accepted with `--branch-name`; an agent that cannot `git add` a masked file (it is a device) commits the emptied secret with `update-index --cacheinfo`, which the suite does; one `sed -i` was run on a Go test file by mistake and reverted by hand (no script touched, all seven `test/e2e` scripts stay 100755)

| Task | Validation | Result |
| ---- | ---------- | ------ |
| 1 | `go test ./internal/hostgit` (new ref, existing and current branch refused and untouched, no `FETCH_HEAD`, temporary ref gone, hook not fired, junk, truncated and missing-prerequisite bundles, name table) | pass |
| 2 | `go test ./internal/sandbox` (real git as the sandbox: branch, patch applies, `O_EXCL`, symlink target, masked, rewritten history, replace graft, hostile config, `.git` tree entry, junk bundle, cap, refusals, argv hardening) | pass |
| 3 | `go test ./internal/cli` (text, JSON shape, flag combinations and exit codes) | pass |
| 4 | `make test` | pass |
| 5 | `test/e2e/cloneexport.sh`, README, security model, CONTRIBUTING | 141 passed, 0 failed |
| 6 | `make lint test`, `make cross`, `make e2e`, `git ls-files -s test/e2e` | all exit 0 |

| Safety case | Fails without its fix |
| ----------- | --------------------- |
| replace objects ignored | yes: unit test and e2e (patch holds the fake) against a build without `GIT_NO_REPLACE_OBJECTS` |
| object checks on fetch | yes: unit and e2e (`.git` tree entry accepted) against a build without the fsck options |
| host hooks off | yes: unit (hook fired) and e2e (host hook marker) against a build without `core.hooksPath=/dev/null` |
| masked refusal | yes: e2e against a build without it (7 checks) |
| patch formatting overrides | yes: e2e (injected header and signature) against a build without `--no-add-header` and `--no-signature` |
| combined mutant build of all of the above | e2e: 22 of 141 checks failed |

## Run 7 (phase 3 repair)

- Head: `8aa8171` (code at `2db11d0` and the three commits after it)
- Left: no Mac or Windows check; the SIGTERM e2e depends on timing (it passes by design when the export beats the signal; it was interrupted for real in the run recorded here); NTFS trailing dot and short names, and Unicode normalisation, are not handled by the mask match (documented); a decompression bomb is bounded by the timeout and disk only, as git has no inflate limit reachable from `fetch`
- Design changes:
  - the patch is one `git diff --binary --full-index <base> <tip>` with no commit message; the host runs `git apply --numstat -z` on the finished file and removes it when its file set differs from the reviewed net change; history is kept by `--as branch`
  - the review covers the net change and every commit (`log --raw --no-abbrev -m` and `log -p -m`, merges against each parent), one entry per path, mode and blob; masks and warnings use the union, so a change a later commit undoes is seen
  - the bundle is fetched into a quarantine bare repository (alternates to the host objects, none of its configuration) where the object checks and the review run; only then are the reviewed objects fetched into a temporary ref of the host; a refused bundle leaves no host object; the errors say `bundle verify failed` or `object check failed`
  - mask matching lower-cases the patterns, the recorded paths and the candidate (`workspace.MatcherFold`)
  - the review does not read a blob over 64 MiB (`core.bigFileThreshold`, no `--text`); such a file and a "Binary files differ" section count as hidden
  - `podbox` cancels the command context on SIGINT and SIGTERM
  - `ExportOptions.Validate` is the only place of the flag-combination rules; `subName` names the real git subcommand in errors; a default branch name is re-sanitized after the 100 byte cut and the sandbox name is sanitized too
- Deviations from the review: `--as patch` still checks git and the base commit on the host (the brief for phase 3 asks for the base check in both forms), so it still makes the temporary directory; `--max-input-size` is not reachable through `fetch`, so the size limit is a timeout and a documented residual

| Item | Test | Fails on 779a2ed |
| ---- | ---- | ---------------- |
| 1 message diff smuggled into the patch, merge's own changes | unit `TestPatchHoldsNoCommitMessage`, `TestExportMerge`; e2e message and merge sections | yes (unit and e2e) |
| 2 interrupt | e2e SIGTERM section; unit cancel during the bundle and a refused import leaves no object or ref | e2e: yes (3 checks); the unit cleanup tests pass on both, they pin the defers |
| 3 case folding | `TestMaskCaseFolding`, `TestMatcherFold` | yes (the pattern case; the recorded case follows from the first refusal missing) |
| 4 size | `TestBigFileIsNotInflatedByTheReview` | yes |
| 5 history | `TestMaskedTouchedThenReverted`, e2e reverted section | yes (unit and e2e) |
| 6 layers, fsck | `TestExportObjectChecks` (ten cases), `TestExportObjectChecksIgnoreLocalSettings`, layer asserts in `TestExportBadBundles` | new behaviour, not run on 779a2ed |
| 7 cleanups | `TestDefaultBranchIsAlwaysAValidName`, `TestOptionsValidate`, `TestSubName` | the name test yes |

Verified against 779a2ed in a temporary worktree: the new repair unit tests (without the two tests that need new APIs) failed on 6 tests, and the new e2e script run with a 779a2ed binary failed 9 of 158 checks.
