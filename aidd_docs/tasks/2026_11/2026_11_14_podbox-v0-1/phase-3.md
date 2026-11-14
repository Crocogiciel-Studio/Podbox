---
status: pending
---

# Instruction: Network policy model and policy command

## Architecture projection

```txt
.
└── internal/
    ├── cli/
    │   └── policy.go ✅
    └── policy/
        ├── policy.go ✅
        ├── match.go ✅
        ├── load.go ✅
        ├── defaults.go ✅
        └── policy_test.go ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | fixture global, project and session files | loaded | system |
| Happy path | `*.pythonhosted.org` allowed, request `files.pythonhosted.org` | allow | system |
| Edge case | host allowed by project but denied globally | deny | system |
| Edge case | project file invalid YAML | load error reported, project layer contributes nothing | system |
| Edge case | rule without `allow_private`, host resolves to 10.x | deny (checked by the proxy in phase 4, flag carried here) | system |
| Happy path | `podbox policy allow <sbx> pypi.org` | session grant written; `policy show <sbx>` lists it with its layer | cli |
| Happy path | `podbox policy deny <sbx> pypi.org` | grant removed / explicit session deny | cli |

## Tasks to do

### `1)` Model and matching

> One decision function: host → allow/deny, with the rule and layer that decided.

1. YAML schema: `default: deny`, `rules: [{host, allow, allow_private}]`; `mode` field accepted, only `strict` honoured in v0.1.
2. Host patterns: exact and `*.suffix` wildcard, case-insensitive, port-agnostic.
3. Precedence: any deny in any layer wins; otherwise any allow; otherwise default deny.

### `2)` Layers and fail-closed loading

> defaults → global (`<config>/policy.yaml`) → project (`.podbox/policy.yaml`) → session grants.

1. Built-in defaults per agent (Claude: `api.anthropic.com`, `*.anthropic.com`, `claude.ai`, `*.claude.ai`, `claude.com`, `*.claude.com`, `statsig.anthropic.com`, `sentry.io`; Kiro: TBD in phase 5 from observed traffic; common: `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `repo.maven.apache.org`, `github.com`, `*.githubusercontent.com`).
2. Invalid layer file ⇒ warning + layer ignored, never widening.
3. Compile layers into one effective JSON document written atomically to the sandbox state dir.

### `3)` `podbox policy show|allow|deny`

> Inspect and change a sandbox's grants from the host.

1. `allow`/`deny` write the session layer and recompile the effective file; `--scope session|project` (project writes `.podbox/policy.yaml`).
2. `show` prints rules with their layer, text or JSON.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | table-driven tests cover exact, wildcard, deny-wins, default deny |
| 2 | tests prove an invalid or missing file never adds an allow |
| 3 | allow then show lists the grant; effective file changes atomically |
