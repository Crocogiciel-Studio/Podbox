---
status: pending
---

# Instruction: Install at start, e2e, docs

## Architecture projection

> Tree of the final files from the project root, each folder ending in `/`. ✅ create · ✏️ modify · ❌ delete

```txt
.
├── .github/
│   └── workflows/
│       └── ci.yml                 ✏️
├── Makefile                       ✏️
├── README.md                      ✏️
├── CONTRIBUTING.md                ✏️
├── docs/
│   ├── policy.md                  ✏️
│   └── security-model.md          ✏️
├── internal/
│   └── sandbox/
│       ├── context.go             ✅
│       ├── context_test.go        ✅
│       └── sandbox.go             ✏️
└── test/
    └── e2e/
        └── agentctx.sh            ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| -------- | ------ | ------------------ | ----- |
| Setup | `podbox create claude <proj> --name e2e-ctx` with a pre-existing `~/.claude/CLAUDE.md` holding user text, then `start` | sandbox running | cli |
| Happy path | read `~/.claude/CLAUDE.md` and the skill file in the sandbox | one podbox block, user text intact, skill present, both owned by uid 1000, and `$PODBOX_SANDBOX` is `e2e-ctx` | cli |
| Happy path | `/run/podbox` is a read-only mount and `policy.json` names `e2e-ctx` and the workspace | mount present, content right | cli |
| Edge case | a second claude sandbox on another project, started | the shared `CLAUDE.md` still holds one block and no sandbox name | cli |
| Happy path | `stop`, `start` twice more | still exactly one block | cli |
| Happy path | read `/run/podbox/policy.json`, run `podbox policy allow e2e-ctx example.org`, read it again without restart | the new host appears | cli |
| Edge case | write, replace and delete `/run/podbox/policy.json` from the sandbox | each fails | cli |
| Edge case | edit the skill file and the podbox block of `CLAUDE.md` from the sandbox, restart | both are back to podbox's content, user text kept | cli |
| Edge case | `git status` in the project after start | no file added or changed by the feature | cli |
| Edge case | shell sandbox | no `~/.claude` and no `~/.kiro` after start | cli |
| Edge case | an unmergeable `CLAUDE.md` (unterminated marker) | start succeeds and prints a warning, the file untouched | cli |
| Teardown | `podbox rm --volumes` | baseline restored | cli |

## Tasks to do

### `1)` Install the context in the container

> After the containers run, put the context in the agent's home.

1. In `internal/sandbox/context.go`, add `Manager.installContext(ctx, meta)`: skip when `agentctx.Files` is empty; for each file read the current content with exec (`cat`, a non-zero exit meaning absent), merge or replace in Go, and write it with an exec that creates the directory, writes a temporary file from stdin and renames it; every exec sets `User: AgentUser`, and the write sets `Interactive: true` so podman passes stdin.
2. Call it at the end of `Start`, also when the containers were already running, and turn any error into `m.Warn` without failing the start.
3. Unit-test it against a fake `runtime.Runtime` for: absent file, existing user content, unmergeable file warns, shell does nothing.

### `2)` End-to-end suite

> The behaviours of the Done-when, on Linux CI.

1. Create `test/e2e/agentctx.sh` mode 100755 following lifecycle.sh, covering the scenarios above with the claude image.
2. Add it to `make e2e` and to the CI e2e job.

### `3)` Documentation

> Say what the agent is told and what it can read.

1. README: one line in Commands text for `start`, and a Known limits line that the context informs and does not enforce.
2. docs/security-model.md: section "Agent context" (what is written where, the read-only policy view and what it hides, never written to the project, rewritten at each start).
3. docs/policy.md: the agent's view of the policy and its format.
4. CONTRIBUTING.md: the new e2e suite in the checks list.

### `4)` Verify what the sandbox could not

> Close the runtime questions of the spec.

1. When a kiro image can be built, run `podbox run kiro` and confirm Kiro cites the steering content; otherwise record Kiro loading as unverified in the pull request.
2. Run `podbox run claude` once logged in and confirm the first turn knows the sandbox and the allow command.
3. Contributor checks: `make lint test`, `make cross`, `make e2e`.

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | start of a claude sandbox leaves the block and the skill in the home volume; a failing write prints a warning and the sandbox runs |
| 2 | `make e2e` runs the new suite green and every file under test/e2e is mode 100755 |
| 3 | each document states the same facts as the behaviour, and none states that the context enforces anything |
| 4 | the PR states what was and was not verified for Kiro and for a live Claude session |
