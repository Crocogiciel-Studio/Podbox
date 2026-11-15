---
name: podbox-sandbox
description: How to work inside a podbox sandbox. Use when a request fails, returns 403 or "Blocked by podbox", a host or port seems unreachable, a file such as .env or a key is empty, or git push is needed.
---

# Working in a podbox sandbox

You run in a podbox sandbox. The sandbox name is `$PODBOX_SANDBOX`. Your workspace and your effective network policy are in `/run/podbox/policy.json`: the sandbox, the workspace, and per rule the host, whether it is allowed, and its ports. The file is read-only and always current, also right after the human changes the policy.

## Reading a refusal

- HTTPS: the proxy answers HTTP 403 to the CONNECT. HTTP: the body starts with "Blocked by podbox network policy" and names the command to ask for.
- A refusal is policy, not an outage. Do not retry, and never try another proxy, a tunnel, another route, or editing policy files.
- Check `/run/podbox/policy.json` to see whether the host has a rule, and for which ports. A deny rule wins over an allow rule. A rule covers one host name; a subdomain needs its own rule unless the rule is a wildcard.

## Asking for a host

You cannot run `podbox`. Give the human the exact commands, with the real sandbox name (`$PODBOX_SANDBOX`) and the refused host:

- `podbox policy allow <sandbox> <host>` opens ports 80 and 443.
- `podbox policy allow <sandbox> <host> --port N` opens another port (repeat `--port` for several).
- `podbox policy log <sandbox> --denied` lists what was refused, to find the host.

Say which host you need and why, and wait for the human.

## Masked files

Files such as `.env`, private keys and credential folders are empty on purpose: podbox hides them from the sandbox. Do not look for a way around it; ask the human for the value you need, or for a harmless placeholder.

## git push

`git push` is not possible from the sandbox. Commit your work, then ask the human to run on the host: `git -C <workspace> push`, with `<workspace>` read from `/run/podbox/policy.json`.
