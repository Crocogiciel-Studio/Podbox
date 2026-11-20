# Podbox — Agent context

## Target

An agent running in a podbox sandbox (Claude Code or Kiro) knows from its first message that it is sandboxed, why a request can be refused, and what to tell the human to do about it, without any file added to the project.

## Hard constraints

- The agent is told, in an always-loaded short context, that: it runs in podbox sandbox `<name>` on workspace `<path>`; its network goes only through an allowlist proxy; a refused request (HTTP 403 on CONNECT, or a plain-HTTP refusal naming podbox) is a policy decision, not an outage; it must never try to bypass the policy (alternative proxy, tunnel, other route, editing policy files); it cannot run `podbox` itself.
- That context gives the agent the exact commands for the human, copy-pasteable with the real sandbox name and the refused host: `podbox policy allow <name> <host>` and `podbox policy log <name> --denied`.
- A detailed on-demand guide (more than the short context can hold) is available to the agent: how to read a refusal, how to ask for a host with its ports, why masked files (`.env`, keys) are empty on purpose, and that `git push` is not possible from the sandbox in v0.1 (the human runs `git -C <path> push` on the host).
- The sandbox name is exposed to the agent as `PODBOX_SANDBOX`.
- The agent can read its effective network policy, and what it reads is current after `podbox policy allow|deny` without restarting the sandbox.
- The agent cannot modify, replace or delete the policy it reads, nor the files podbox maintains for it; nothing the agent does changes what the proxy enforces.
- The context lives only in the agent's persistent home and the sandbox's runtime; podbox never writes it in the project, the host's `$HOME` or the host's agent configuration.
- The context is rewritten at every sandbox start from podbox's current version, so an upgrade of podbox reaches existing sandboxes and an edit by the agent does not outlive a restart.
- Content the human or the agent already keeps in the agent's own instruction files is preserved; only podbox's part is replaced, and repeated starts never duplicate it.
- The context is identical for a given sandbox on Linux, macOS and Windows hosts, and is written in English.
- The `shell` agent receives no context.
- A failure to write the context does not prevent the sandbox from starting; the human is warned.
- The context adds no secret, host path outside the workspace, upstream proxy address or credential.

## Non-goals

- Control tools for the agent (a sandbox-control MCP server, an `ask` mode, holding a request until the human decides): v0.3.
- `--clone` mode, `diff`, `export`: separate cycle.
- Any change to the project's own `CLAUDE.md`, `AGENTS.md` or steering files.
- Making the agent obey: the context informs, enforcement stays the proxy's and the container's.
- Context for agents other than `claude` and `kiro`.
- Localization of the context.

## Done-when

- After `podbox run claude`, the agent's first turn can state that it is in a podbox sandbox, name it, and name the command that allows a blocked host, with no prompt from the human about it.
- After `podbox run kiro`, the same holds for Kiro, once the location Kiro loads global instructions from is confirmed.
- A host outside the allowlist is refused; the agent, asked why, answers from the context and gives `podbox policy allow <name> <host>` with the real name and host.
- The agent can read its effective policy; after `podbox policy allow <name> <host>` it shows the new host without a restart.
- From inside the sandbox, writing, replacing or removing the policy it reads, or podbox's context files, either fails or is undone at the next start without changing what the proxy enforces.
- A pre-existing instruction file with user content keeps that content after one and after several starts, with exactly one podbox part.
- The project directory has no file added or changed by the feature, and the host's `$HOME` is untouched.
- `podbox run shell` leaves no podbox context in the shell sandbox's home.
- A start where the context cannot be written still starts the sandbox and prints a warning.
- The above is verified by the Linux e2e suite on every change.

## Open Questions

- TBD: Where does Kiro load global (cross-project) instructions from in the shipped image, and in which format?
- TBD: May the policy the agent reads show the layer each rule comes from (defaults, global, project, session), or only host, ports and allow/deny?
- TBD: Does Claude Code load a global instruction file and a global skill from the agent's home on the version pinned in the image, with `--dangerously-skip-permissions`?

## Context

- Previous spec: `aidd_docs/tasks/2026_11/2026_11_14_podbox-v0-1/spec.md`, which keeps `operating-in-sandbox` and the `sandbox-control` MCP for v0.3; this cycle delivers the passive half only.
- Modelled on what Docker `sbx` injects into its agents.
