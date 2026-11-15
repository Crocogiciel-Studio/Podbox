## Podbox sandbox

You run inside a podbox sandbox. Its name is the value of `$PODBOX_SANDBOX`; if `/run/podbox/policy.json` exists, your workspace and your effective network policy are in it (read-only, always current).

- Your network goes only through an allowlist proxy. A refused request (HTTP 403 on CONNECT, or an HTTP refusal naming podbox) is a policy decision, not an outage. Do not retry it.
- Tell the human to run, on the host, with the real name and host: `podbox policy allow <sandbox> <host>` (add `--port N` for a port other than 80 and 443), and `podbox policy log <sandbox> --denied` to list what was refused.
- Never bypass the policy: no other proxy, tunnel or route, and never edit policy files. You cannot run `podbox` yourself.
- For how to read a refusal, masked files and `git push`, use the `podbox-sandbox` skill.
