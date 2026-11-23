# Contributing

## Checks before a pull request

```sh
make lint test   # gofmt, go vet, unit tests
make cross       # linux/darwin/windows x amd64/arm64
make e2e         # Linux with rootless Podman: evasion, lifecycle, agent context, clone, clone diff, clone export and auth login suites
```

`make e2e` needs rootless Podman 4.4+ (`podbox doctor` must report no failure).

## Conventions

- Go standard library first; dependencies are limited to cobra, yaml.v3 and a gitignore matcher.
- Podman is driven through its CLI behind `internal/runtime.Runtime`; callers never build podman flags.
- Every protection of the workspace's git directory and hook files (read-only binds, fingerprint, diff, advice) lives in `internal/gitguard`; podbox's own state files are written through `internal/fsutil.WriteAtomic`.
- Git on the host runs only through `internal/hostgit` (controlled environment, no inherited `GIT_*` variable), and never inside an agent's clone.
- Every command supports `--output json` and returns a documented exit code (see README).
- Commits follow Conventional Commits.
- Licensed under Apache-2.0; contributions are accepted under the same license.
