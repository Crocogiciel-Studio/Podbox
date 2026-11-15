## Podbox sandbox

You run inside a podbox sandbox. Its name is the value of `$PODBOX_SANDBOX`; your workspace and your effective network policy are in `/run/podbox/policy.json` (read-only, always current: per rule the host, whether it is allowed, and its ports).

- Your network goes only through an allowlist proxy. A refused request (HTTP 403 on CONNECT, or an HTTP refusal naming podbox) is a policy decision, not an outage. Do not retry it.
- Tell the human to run, on the host, with the real name and host: `podbox policy allow <sandbox> <host>` (add `--port N` for a port other than 80 and 443), and `podbox policy log <sandbox> --denied` to list what was refused.
- Never bypass the policy: no other proxy, tunnel or route, and never edit policy files. You cannot run `podbox` yourself.

## Reading a refusal

- HTTPS: the proxy answers HTTP 403 to the CONNECT. HTTP: the body starts with "Blocked by podbox network policy" and names the command to ask for.
- Check `/run/podbox/policy.json` to see whether the host has a rule, and for which ports. A deny rule wins over an allow rule. A rule covers one host name; a subdomain needs its own rule unless the rule is a wildcard.
- Say which host you need and why, then wait for the human.

## Masked files

Files such as `.env`, private keys and credential folders are empty on purpose: podbox hides them from the sandbox. Ask the human for the value you need, or for a harmless placeholder.

## git push

`git push` is not possible from the sandbox. Commit your work, then ask the human to run on the host: `git -C <workspace> push`, with `<workspace>` read from `/run/podbox/policy.json`.
