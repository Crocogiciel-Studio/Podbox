---
status: done
---

# Instruction: Context content and merge logic

## Architecture projection

> Tree of the final files from the project root, each folder ending in `/`. ✅ create · ✏️ modify · ❌ delete

```txt
.
└── internal/
    └── agentctx/
        ├── agentctx.go            ✅
        ├── agentctx_test.go       ✅
        └── templates/
            ├── claude-block.md    ✅
            ├── claude-skill.md    ✅
            └── kiro-steering.md   ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| -------- | ------ | ------------------ | ----- |
| Setup | agent `claude` | rendered files in memory | system |
| Happy path | render the files for claude | a block for `CLAUDE.md` (merge) and a skill file (owned), both naming `$PODBOX_SANDBOX`, `/run/podbox/policy.json`, `podbox policy allow <sandbox> <host>` and `podbox policy log <sandbox> --denied` | system |
| Happy path | render for kiro | one owned steering file under `.kiro/steering/` | system |
| Edge case | render for shell | no file | system |
| Edge case | merge into empty, into user text, twice, into text with an old block | one block, user text intact, replaced in place, no duplicate, no blank-line growth | system |
| Edge case | existing file with a begin marker and no end marker | the file is left as is and an error is returned | system |
| Teardown | none | baseline restored | system |

## Tasks to do

### `1)` Templates

> The words the agent reads, in English, short where always loaded.

1. Write `claude-block.md`: the sandbox name is the value of `$PODBOX_SANDBOX` and the workspace is in `/run/podbox/policy.json`, and the text names no sandbox because the home is shared by every sandbox of the agent; network only through the allowlist proxy; a refused request (403 on CONNECT or an HTTP refusal naming podbox) is policy, not an outage; the exact commands for the human with the real name; never bypass (no other proxy, tunnel, route, no editing policy); the policy is readable at `/run/podbox/policy.json`; you cannot run `podbox`; point to the `podbox-sandbox` skill.
2. Write `claude-skill.md` with `name: podbox-sandbox` and a `description` that fires on failed requests, 403, blocked hosts, push and masked files; body: reading a refusal, asking for a host with ports (`--port N`), masked `.env` and keys are empty on purpose, `git push` is not possible in v0.1 so ask the human to run `git -C {{workspace}} push`.
3. Write `kiro-steering.md` as the same short block plus the guide, in one file, with no front matter so Kiro loads it in every interaction.

### `2)` Render and merge

> Pure functions, no I/O.

1. Embed the templates with `embed`; no placeholder, the text is static.
2. `Files(agent string) []File` returns `File{Path, Content, Mode}` with `Path` relative to the home, `Mode` `Block` or `Owned`; claude gives `.claude/CLAUDE.md` (Block) and `.claude/skills/podbox-sandbox/SKILL.md` (Owned); kiro gives `.kiro/steering/podbox.md` (Owned); shell gives none.
3. `MergeBlock(existing, block string) (string, error)` wraps the block between a `podbox:begin` and a `podbox:end` marker, each written as an HTML comment line, replaces an existing marked region in place, appends otherwise, and errors on an unterminated marker.

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | no template contains a host path, a sandbox name, an upstream proxy address or a credential placeholder |
| 2 | the table of scenarios above passes; merging the same block twice yields identical bytes |
