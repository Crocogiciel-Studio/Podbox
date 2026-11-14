# Policy reference

## Layers

From general to specific, all merged into one effective policy per sandbox:

| Layer | Source |
| --- | --- |
| defaults | built into podbox, per agent (below) |
| global | `~/.config/podbox/policy.yaml` |
| project | `<project>/.podbox/policy.yaml`, as snapshotted (below) |
| session | `podbox policy allow\|deny <sandbox> <host>` (sandbox state directory) |

Precedence: a deny in any layer wins; otherwise an allow in any layer; otherwise deny. A project or session allow therefore never overrides a global deny.

## File format

```yaml
mode: strict          # only strict is honoured in v0.1; ask/learn warn and act as strict
default: deny         # the only accepted value
rules:
  - host: "*.pythonhosted.org"   # exact host or *.suffix (subdomains only), case-insensitive
    allow: true                  # required: true or false
  - host: git.corp.example
    allow: true
    ports: [22, 443]             # optional; default 80 and 443 for an allow, every port for a deny
  - host: gitlab.corp.internal
    allow: true
    allow_private: true          # permit private addresses (RFC 1918, ULA, CGNAT) for this rule
  - host: "*.pastebin.com"
    allow: false
```

Ports: an allow rule opens ports 80 and 443 only, so an allowlisted host is not reachable on 22 or any other port unless a rule lists it in `ports`. A deny rule without `ports` covers every port; with `ports`, only those. The proxy logs a refused port as `"decision":"deny","reason":"policy"` with the `port`, and tells the agent the `--port` fix.

Fail closed: a file that does not parse, has an unknown key, a bad pattern or port (outside 1-65535), a rule without `allow`, or `default` other than `deny` is ignored entirely with a warning; a missing file is an empty layer.

## Commands

```sh
podbox policy show <sandbox>                       # rules with their layer, warnings
podbox policy allow <sandbox> <host> [--port N]... [--scope session|project]
podbox policy deny <sandbox> <host> [--port N]... [--scope session|project]
podbox policy reload <sandbox>                     # re-read project files, print and apply the changes
```

Changes rewrite the effective file atomically; the running proxy applies them within about a second. Edits to the global file by hand are picked up at the next `policy` change or `start`.

## Project files are snapshotted

The agent can write the project, so podbox never trusts the live `.podbox/policy.yaml` or `.sandboxignore`. `create` copies both into the sandbox state directory; every later compilation (`policy allow|deny`, `start`) and mask computation uses that copy only. In the agent container both paths, when they exist, are mounted read-only.

- `create` prints each rule and pattern it imports (`<name>: imported rule from .podbox/policy.yaml: allow example.com`, `<name>: imported .sandboxignore pattern: secrets/`) on stderr and lists them in `imported` (`rules`, `ignore`) in its JSON output: the agent of an earlier sandbox may have written these files.
- `--scope project` writes the rule to the project file and to the snapshot, so the commit and the sandbox agree. It edits only `.podbox/policy.yaml` in the project, replacing it atomically, and is refused (exit code 2, `unsafe workspace`, nothing written) when `.podbox` or `.podbox/policy.yaml` is a symbolic link or not a plain directory and regular file: an agent link to another directory (for example the podbox config) cannot make it rewrite another layer. A project allow never overrides a global deny.
- After editing a project file by hand (or noticing the agent did), review it and run `podbox policy reload <sandbox>`. It prints added and removed rules (`+ rule allow example.com`) and ignore patterns (`- ignore secrets/`), then applies the policy at once; mask changes take effect at the next `stop` + `start`. It also forgets the recorded masks (see [masks follow their files](security-model.md#workspace-masking)) that no pattern matches any more, printing each (`- mask config/prod.yaml (now config2/prod.yaml)`). It never drops a recorded mask found nowhere in the workspace: it keeps it and warns, and `start` refuses (exit 7) until you restore the file or run `podbox policy reload <sandbox> --forget-missing`, which drops and prints each one (`- mask sub2/.env (missing)`). An unreadable workspace directory refuses the reload (exit 7, see [workspace masking](security-model.md#workspace-masking)). `--output json` returns `rules_added`, `rules_removed`, `ignore_added`, `ignore_removed`, `masks_dropped`, `masks_missing`.
- `podbox ls` and `policy show` warn when a project file differs from the snapshot; `ls -o json` lists them in `project_changed`.
- A project file reached through a symbolic link (at any component) is ignored with a warning; podbox never follows workspace links on the host (see [workspace symbolic links](security-model.md#workspace-symbolic-links)).

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
