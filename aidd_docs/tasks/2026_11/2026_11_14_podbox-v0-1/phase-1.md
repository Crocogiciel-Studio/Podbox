---
status: pending
---

# Instruction: Go skeleton, output contract, CI

## Architecture projection

```txt
.
├── .github/
│   └── workflows/
│       └── ci.yml ✅
├── .gitignore ✅
├── CONTRIBUTING.md ✅
├── LICENSE ✅
├── Makefile ✅
├── README.md ✅
├── cmd/
│   └── podbox/
│       └── main.go ✅
├── go.mod ✅
└── internal/
    ├── cli/
    │   ├── root.go ✅
    │   └── version.go ✅
    ├── config/
    │   ├── paths.go ✅
    │   └── paths_test.go ✅
    └── output/
        ├── output.go ✅
        ├── exitcode.go ✅
        └── output_test.go ✅
```

## Test Scope

| Scenario | Action | Observable outcome | Actor |
| --- | --- | --- | --- |
| Setup | `make build` | `bin/podbox` exists | cli |
| Happy path | `podbox version --output json` | JSON object with `version`, `commit`, `os`, `arch`; exit 0 | cli |
| Edge case | `podbox --output yaml version` | usage error on stderr, exit 2 | cli |
| Edge case | error raised by a command in JSON mode | `{"error":{"code":...,"message":...}}` on stdout, documented exit code | cli |
| Happy path | `make cross` | binaries for linux/darwin/windows × amd64/arm64 | system |

## Tasks to do

### `1)` Module and entrypoint

> A buildable `podbox` binary.

1. `go mod init github.com/Crocogiciel-Studio/podbox`; add cobra.
2. `cmd/podbox/main.go` calls `cli.Execute()` and exits with its code.

### `2)` Output and exit-code contract

> Every command prints text or JSON the same way and exits with a stable code.

1. `output.Printer` with `Text`/`JSON` modes, global `--output text|json` flag.
2. Exit codes: 0 ok, 1 generic, 2 usage, 3 prerequisite missing, 4 not found, 5 runtime error; exec-like commands propagate the inner exit code.
3. Typed error carrying its exit code, rendered as JSON in JSON mode.

### `3)` Config paths

> One place resolving config, state and cache directories per OS.

1. Global config `$XDG_CONFIG_HOME/podbox` (macOS/Windows: `os.UserConfigDir()`), state `$XDG_STATE_HOME/podbox` (fallback `~/.local/state/podbox`), `PODBOX_HOME` override.

### `4)` `version` command

> Build metadata injected by ldflags.

### `5)` Repo hygiene and CI

> Lint, test and cross-build on every push.

1. Makefile: `build`, `test`, `lint` (gofmt check + go vet), `cross`.
2. GitHub Actions `ci.yml`: lint, unit tests, cross-build matrix.
3. Apache-2.0 LICENSE, README stub, CONTRIBUTING with the contributor checks, `.gitignore`.

## Test acceptance criteria

| Task | Acceptance criteria |
| --- | --- |
| 1 | `go build ./...` succeeds |
| 2 | unit tests show text vs JSON rendering and error→exit-code mapping |
| 3 | unit tests resolve paths with and without `PODBOX_HOME` / XDG vars |
| 4 | `podbox version --output json` parses as JSON |
| 5 | `make lint test cross` passes locally |
