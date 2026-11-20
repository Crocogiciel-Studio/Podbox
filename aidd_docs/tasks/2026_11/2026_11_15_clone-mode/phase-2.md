---
status: done
---

# Instruction: podbox diff

## Architecture projection

> Tree of the final files from the project root, each folder ending in `/`. ✅ create · ✏️ modify · ❌ delete

```txt
.
├── README.md                      ✏️
├── docs/
│   └── security-model.md          ✏️
├── internal/
│   ├── termsafe/
│   │   ├── termsafe.go            ✅
│   │   └── termsafe_test.go       ✅
│   ├── sandbox/
│   │   ├── inspect.go             ✅
│   │   ├── inspect_test.go        ✅
│   │   └── clone.go               ✏️
│   └── cli/
│       ├── diff.go                ✅
│       ├── diff_test.go           ✅
│       ├── root.go                ✏️
│       └── exitcode_test.go       ✏️
└── test/
    └── e2e/
        └── clone.sh               ✏️
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| -------- | ------ | ------------------ | ----- |
| Setup | a running clone sandbox; the agent commits a change, edits a tracked file without committing, adds an untracked file, deletes a file, renames a file, adds a binary file, and creates a file whose name and content carry escape sequences | agent work in the clone, host repository untouched | cli |
| Happy path | `podbox diff <name>` | a file summary (status, added, deleted, path) with a total, then a patch that holds the committed, the uncommitted and the untracked changes | cli |
| Happy path | `podbox diff <name> --stat` | only the summary, no patch | cli |
| Happy path | `podbox diff <name> -o json` | `base`, a `files` array with `path`, `old_path`, `status`, `added`, `deleted`, `binary`, `untracked`, and a `patch` string; with `--stat` no `patch` | cli |
| Happy path | after the diff, `git status` and the object count inside the sandbox | the agent's index and object count are unchanged by the diff | cli |
| Edge case | the file name and content carry ESC, CSI, OSC, BEL, CR, a C1 byte and a newline in the name | the text output and the JSON strings hold none of them; ordinary text, tabs and newlines of the patch are kept | cli |
| Edge case | the agent changed nothing | "no changes since <base>" and an empty `files` array, exit 0 | cli |
| Edge case | the sandbox is stopped | exit 1 and a message naming `podbox start <name>` | cli |
| Edge case | a shared sandbox, an unknown sandbox | exit 2 naming the clone requirement, exit 4 | cli |
| Edge case | a patch larger than the cap | exit 1 and a message suggesting `--stat`, nothing partial printed | cli |
| Edge case | the agent plants `diff.external`, `core.pager` and a textconv driver in the clone config | none is run, output is plain git diff | cli |
| Teardown | `rm --volumes` and remove the temporary directories | no podman object, no clone left | cli |

## Tasks to do

### `1)` Terminal sanitizer

> Everything the agent wrote is made inert before it reaches the terminal.

1. Create `internal/termsafe` with `Clean(b []byte) []byte` (keeps `\n` and `\t`, removes ESC introduced sequences: CSI, OSC and DCS, APC, PM, SOS up to BEL or ST, and two-character escapes; removes C0 controls, DEL, CR and the C1 range U+0080 to U+009F) and `CleanName(s string) string` (also replaces newline and tab with `?`).
2. Make it a pure function with no allocation of unbounded state: an unterminated sequence is dropped to the end of the input.

### `2)` Read the clone from inside the sandbox

> `Manager` runs git in the agent container and returns the data without trusting it.

1. Add `inspect.go` with an unexported `runIn(ctx, name, argv, env)` that checks the sandbox is a clone and running (`ErrNotClone`, `ErrNotRunning`), runs through `RT.Exec` with `User` and `Workdir` as `Exec` does, captures stdout into a buffer capped at 32 MiB (`ErrTooLarge`), and discards stdin.
2. Add `Manager.Diff(ctx, name)` returning `DiffResult{Base, Files, Patch}`: validates `Meta.Base` as a hex object name, runs `git -c safe.directory=<project path> -c core.pager=cat -c diff.external= -c core.quotepath=off` commands with `--no-ext-diff --no-textconv --no-color`, using a temporary index file (`GIT_INDEX_FILE=/tmp/podbox-diff-<rand>`: `read-tree <base>`, `add -N .`, then `diff -z --name-status -M <base>`, `diff -z --numstat -M <base>`, `diff --binary -M <base>`, and `ls-files -z --others --exclude-standard`), and removes the temporary index.
3. Parse the `-z` outputs into `FileChange{Path, OldPath, Status, Added, Deleted, Binary, Untracked}`; names are cleaned with `CleanName` and the patch with `Clean` before the result is returned.
4. Add the sentinel `ErrNotClone`; `Exec`'s existing not-running error is reused.

### `3)` The command

> `podbox diff <sandbox> [--stat]` in text and JSON.

1. Create `cli/diff.go`: the summary table, the total, the patch; `--stat` stops after the summary; `-o json` returns `name`, `base`, `files` and, without `--stat`, `patch`.
2. Register it in `root.go`; map `ErrNotClone` to usage (2) in `classify`; `ErrTooLarge` stays generic (1) with the `--stat` hint.
3. Never print or return a byte that did not pass through `termsafe`.

### `4)` Tests

> Parsing, sanitizing and the command contract are unit tested.

1. `termsafe_test.go`: each sequence class, truncated sequences, UTF-8 text kept, a table of hostile inputs.
2. `inspect_test.go`: the `-z` parsers (renames, spaces, newlines in names, binary `-	-`), the command construction with a fake `Runtime` (argv, env, no index file left), the cap, the not-clone and not-running refusals.
3. `diff_test.go` and `exitcode_test.go`: JSON shape, `--stat` omits the patch, exit codes.

### `5)` e2e and docs

> The clone suite covers diff; the docs describe what the output means and why it is sanitized.

1. Extend `test/e2e/clone.sh` with a "diff" section from the Test Scope; keep the file mode 100755.
2. README: `diff <name> [--stat]` in the commands table, the JSON shape, exit codes unchanged; security model: the data comes from git inside the sandbox and describes what the agent says it changed, terminal sequences are removed, review the patch before running anything.

### `6)` Contributor checks

> The phase passes the repository's own gates.

1. Run `make lint test`, `make cross` and `make e2e`; confirm no `sed -i` was used and `test/e2e` scripts are 100755.

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | no ESC, C0 (except newline and tab), DEL, CR or C1 character survives; ordinary UTF-8 and the structure of a patch survive |
| 2 | the diff covers commits, uncommitted tracked changes and untracked files; the agent's index, config and objects are not changed; the output is capped; a shared or stopped sandbox is refused |
| 3 | `diff` prints summary then patch, `--stat` only the summary, JSON has files and the sanitized patch; exit codes follow the table |
| 4 | `make test` passes with the new tests |
| 5 | `clone.sh` prints no FAIL for the diff section; README and the security model describe the command |
| 6 | `make lint test`, `make cross` and `make e2e` pass |
