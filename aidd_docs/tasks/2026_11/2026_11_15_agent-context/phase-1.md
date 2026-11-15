---
status: done
---

# Instruction: Agent policy view, read-only at /run/podbox

## Architecture projection

> Tree of the final files from the project root, each folder ending in `/`. ✅ create · ✏️ modify · ❌ delete

```txt
.
└── internal/
    ├── policy/
    │   ├── agentview.go        ✅
    │   └── agentview_test.go   ✅
    └── sandbox/
        ├── state.go            ✏️
        ├── project.go          ✏️
        ├── agentspec.go        ✏️
        ├── sandbox.go          ✏️
        └── sandbox_test.go     ✏️
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| -------- | ------ | ------------------ | ----- |
| Setup | an effective policy with an allow without ports, an allow with ports, a deny, and a warning | compiled policy in memory | system |
| Happy path | build the agent view and refresh a sandbox policy | `agent/policy.json` holds host, allow and ports as `"80,443"` or `"all"`, nothing else | system |
| Happy path | build the agent spec | the argv has a read-only bind of the agent directory on `/run/podbox` and no bind of the state directory | system |
| Edge case | `RefreshPolicy` twice with a changed rule | the file is replaced atomically and shows the new rule | system |
| Edge case | `ReloadProject` with a changed project rule | the agent view shows it too | system |
| Edge case | sandbox created before this feature, then started | the agent directory exists after start | system |
| Teardown | remove the temporary state directory | baseline restored | system |

## Tasks to do

### `1)` Agent view of the policy

> A pure function turns an effective policy into the minimal document the agent may read.

1. Add `AgentView` (version, sandbox, workspace, rules of `host`, `allow`, `ports` as a string) and `Effective.AgentView(workspace string)` in `internal/policy/agentview.go`.
2. Render ports with `Rule.PortList()` so an allow without ports reads `80,443` and a deny without ports reads `all`.
3. Drop layers, warnings and `allow_private`.

### `2)` Write the view with the policy

> The view is refreshed everywhere the effective policy is.

1. Add `Store.AgentDir(name)` and `Store.AgentPolicyPath(name)`.
2. Add `Store.writeEffective(m, e)` writing the effective file and then `agent/policy.json` through `fsutil.WriteAtomic`.
3. Use it in `Store.RefreshPolicy` (state.go:116) and in the reload path (project.go:223) so `policy allow|deny|reload`, `create` and `Start` all refresh the view.

### `3)` Mount it on the agent

> The agent sees the view at `/run/podbox`, read-only.

1. Add `AgentDir` to `agentInput` and one `runtime.Mount` (bind, read-only, `Relabel: in.SELinux`) on `/run/podbox` in `agentSpec` (agentspec.go; `PODBOX_SANDBOX` is already set there).
2. In `createAgent` (sandbox.go), pass `m.Store.AgentDir(meta.Name)` and create it when missing.

## Test acceptance criteria

| Task | Acceptance criteria |
| ---- | ------------------- |
| 1 | the view of a policy lists each rule once with host, allow and ports, and contains no layer or warning text |
| 2 | after `RefreshPolicy` and after a project reload the file parses back to the view; a second refresh with another rule shows it |
| 3 | `TestAgentSpecHardening` asserts the `/run/podbox:ro` bind and still finds no state directory bind |
