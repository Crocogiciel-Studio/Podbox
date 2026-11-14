# Policy reference

## Layers

From general to specific, all merged into one effective policy per sandbox:

| Layer | Source |
| --- | --- |
| defaults | built into podbox, per agent (below) |
| global | `~/.config/podbox/policy.yaml` |
| project | `<project>/.podbox/policy.yaml` |
| session | `podbox policy allow\|deny <sandbox> <host>` (sandbox state directory) |

Precedence: a deny in any layer wins; otherwise an allow in any layer; otherwise deny. A project or session allow therefore never overrides a global deny.

## File format

```yaml
mode: strict          # only strict is honoured in v0.1; ask/learn warn and act as strict
default: deny         # the only accepted value
rules:
  - host: "*.pythonhosted.org"   # exact host or *.suffix (subdomains only), case-insensitive, any port
    allow: true                  # required: true or false
  - host: gitlab.corp.internal
    allow: true
    allow_private: true          # permit private addresses (RFC 1918, ULA, CGNAT) for this rule
  - host: "*.pastebin.com"
    allow: false
```

Fail closed: a file that does not parse, has an unknown key, a bad pattern, a rule without `allow`, or `default` other than `deny` is ignored entirely with a warning; a missing file is an empty layer.

## Commands

```sh
podbox policy show <sandbox>                       # rules with their layer, warnings
podbox policy allow <sandbox> <host> [--scope session|project]
podbox policy deny <sandbox> <host> [--scope session|project]
```

Changes rewrite the effective file atomically; the running proxy applies them within about a second. Edits to the global or project file by hand are picked up at the next `policy` change or `start`.

## Defaults

Common to every agent: `registry.npmjs.org`, `pypi.org`, `files.pythonhosted.org`, `repo.maven.apache.org`, `github.com`, `*.githubusercontent.com`.

| Agent | Extra hosts |
| --- | --- |
| claude | `api.anthropic.com`, `*.anthropic.com`, `claude.ai`, `*.claude.ai`, `claude.com`, `*.claude.com`, `statsig.anthropic.com`, `sentry.io` |
| kiro (unverified) | `kiro.dev`, `*.kiro.dev`, `q.us-east-1.amazonaws.com`, `codewhisperer.us-east-1.amazonaws.com`, `oidc.us-east-1.amazonaws.com`, `*.awsapps.com` |
| shell | none |

Observed Claude Code 2.1.325 startup traffic: `api.anthropic.com`, `platform.claude.com`, `raw.githubusercontent.com`. The login flow itself has not been observed.

## Always refused

Whatever the policy: `localhost`, loopback, link-local, cloud metadata (`169.254.169.254`, `fd00:ec2::254`), unspecified, multicast and reserved addresses. Private addresses need `allow_private`.
