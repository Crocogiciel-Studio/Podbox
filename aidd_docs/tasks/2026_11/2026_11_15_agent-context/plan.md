---
objective: "An agent in a podbox sandbox (claude, kiro) is told at every start that it is sandboxed and what to ask the human when a request is refused, and can read its live effective policy, with nothing written in the project."
status: pending
---

# Plan: Agent context

Source: aidd_docs/tasks/2026_11/2026_11_15_agent-context/spec.md

## Phases

| #   | Phase | File | Depends on |
| --- | ----- | ---- | ---------- |
| 1   | Agent policy view, read-only at /run/podbox | [`phase-1.md`](./phase-1.md) | none |
| 2   | Context content and merge logic | [`phase-2.md`](./phase-2.md) | none |
| 3   | Install at start, e2e, docs | [`phase-3.md`](./phase-3.md) | 1, 2 |

## External sources

| URL | What it settled |
| --- | --------------- |
| https://code.claude.com/docs/en/memory | `~/.claude/CLAUDE.md` is a user-scope instruction file loaded in every session, for every project |
| https://code.claude.com/docs/en/skills | personal skills live in `~/.claude/skills/<name>/SKILL.md`; only the name and description are listed at session start, the body loads when the description matches; no field is required |
| https://kiro.dev/docs/steering/ | global steering files live in `~/.kiro/steering/` and apply to all workspaces (found through search; the CLI steering page answered 403 from the sandbox) |

## Impact

| Category | Verdict and evidence | Handled in |
| -------- | -------------------- | ---------- |
| runtime | Claude Code loading of `~/.claude/CLAUDE.md` and personal skills: validated by the official docs, not run end to end here (needs a logged-in Claude in the image). Kiro CLI loading of `~/.kiro/steering/`: documented, unverified in the shipped image (installer unreachable from this sandbox, image marked experimental) | 3 |
| regression | `agentSpec` (agentspec.go, TestAgentSpecHardening), `createAgent`, `Start`, `RefreshPolicy`, `policy reload` (project.go) and `policy allow|deny` change; covered by internal/sandbox tests, e2e lifecycle and evasion suites; risk low; no safety-net phase | 1, 3 |
| architecture | one new package `internal/agentctx` (pure: templates, rendering, block merge); `sandbox` owns the exec into the container and the policy view mount; `runtime.Runtime` unchanged | 2, 3 |
| documentation | README (commands, known limits), docs/security-model.md (new "Agent context" section: what the agent can read, that the context informs and does not enforce), docs/policy.md (the agent's view) | 3 |

## Rules

| Rule | Why it applies |
| ---- | -------------- |
| Repo content in English | open source repository |
| Commits without Co-Authored-By or Claude-Session lines | user rule, also for executors |
| Never `sed -i`; `test/e2e/*.sh` stay 100755 | `sed -i` drops the exec bit; the new e2e script is created executable |
| podbox state files go through `fsutil.WriteAtomic` | the policy view is a state file read live by the agent |
| Conventional Commits | CONTRIBUTING.md |

## Decisions

| Decision | Why |
| -------- | --- |
| The agent mounts a dedicated `<state>/sandboxes/<name>/agent/` directory read-only at `/run/podbox`, never the state directory | the state directory holds meta, session policy and the git fingerprint; a directory mount (not a file mount) keeps atomic renames visible without a restart |
| The agent view carries the sandbox name, the workspace path, and per rule only host, ports, allow/deny (no layer, no warnings, no `allow_private`) | decided with the user: warnings can carry host paths, layers are not the agent's business; name and workspace are what the guide needs |
| The context text is identical for every sandbox of an agent and names no sandbox: it tells the agent to take the name from `$PODBOX_SANDBOX` and the workspace from `/run/podbox/policy.json` | the home volume is per agent, not per sandbox: two sandboxes of one agent share `~/.claude/CLAUDE.md`, and a rendered name would be wrong for one of them |
| Both writers of the effective policy (`RefreshPolicy` and `policy reload`) go through one `Store.writeEffective` that also writes the agent view | `reload` wrote the effective file directly, so the view would go stale |
| The context is written by exec into the running agent container at every start, merged in Go | works with the home volume as is, needs no image rebuild, makes an upgrade reach existing sandboxes, and keeps the merge unit-testable; a bind-mounted overlay would shadow the user's own `CLAUDE.md` |
| A failed write only warns | the context is advice; a sandbox that cannot start for it would be worse than one without it |
