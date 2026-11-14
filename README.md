# podbox

Run coding agents (Claude Code, Kiro) autonomously in a hardened rootless Podman sandbox whose only network egress is an allowlisting proxy.

Status: v0.1 in development.

## Build

```sh
make build        # bin/podbox
```

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | generic error |
| 2 | usage error (bad flag, argument or `--output` value) |
| 3 | prerequisite missing (see `podbox doctor`) |
| 4 | not found (sandbox, image) |
| 5 | runtime error (podman failed) |

`exec`, `attach` and `run` exit with the inner command's code. With `--output json`, errors are printed on stdout as `{"error":{"code":<exit code>,"kind":"...","message":"..."}}`.

## License

Apache-2.0
