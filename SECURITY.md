# Security policy

podbox is a security tool: its job is to keep an agent that runs on your machine inside a sandbox. A flaw that lets an agent leave it matters to every user, so please report it privately.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting: open the **Security** tab of this repository and choose **Report a vulnerability**. Please do not open a public issue or pull request for it.

Include what you can of:

- the podbox version (`podbox version`), the host OS and the Podman version (`podbox doctor` output helps);
- the agent and the steps that reproduce it, ideally as a script or a short `test/e2e`-style case;
- what the agent could do that the [security model](docs/security-model.md) says it cannot.

You will get an acknowledgement within a few days. There is no bounty. Fixes are released as soon as they are ready, and reporters are credited unless they prefer not to be.

## What is in scope

Anything that breaks a guarantee stated in the [security model](docs/security-model.md) and the [policy reference](docs/policy.md), for example:

- reaching a host outside the allowlist, resolving external names, or reaching the host, its loopback services or a container-engine socket from the agent container;
- reading a masked file (`.env`, keys, `.ssh/`, ...) or an unmasked path the model says is hidden;
- making the agent change what the proxy enforces, or the host's git run agent-chosen code (hooks, config, filters), in `shared` or `--clone` mode;
- `podbox diff` or `podbox export` writing anything to the host repository beyond a new branch or a new file, or running agent-controlled code on the host, or a path where the agent can hide changes from them;
- the agent escaping the hardening of its container (capabilities, privileges, user namespace).

## What is not

The model lists its own limits ("Known limits", "What it does not protect against"): the host kernel is shared, allowlisted hosts are an exfiltration channel, secrets ever committed on a cloned branch are readable from its history, and so on. Those are documented trade-offs, not vulnerabilities. Reports about a documented limit are still welcome as ideas, in a public issue.

## Supported versions

Only the latest release, and `main`.
