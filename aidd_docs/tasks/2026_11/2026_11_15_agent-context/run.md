# Run: Agent context

## Run 1

- Head: `dcb201a`
- Left: live Claude and Kiro sessions not run (no logged-in agent); the read of the file in step 1.2 uses exit code 3 for an absent file instead of any non-zero exit

| Task | Validation | Result |
| ---- | ---------- | ------ |
| 1.1 | `go test ./internal/policy` (view lists host, allow, ports; no layer or warning) | pass |
| 1.2 | `go test ./internal/sandbox` (refresh twice, reload) | view follows both writers |
| 1.3 | `TestAgentSpecHardening` | `/run/podbox:ro` bind present, no state directory bind |
| 2.1 | `go test ./internal/agentctx` (templates static and clean) | pass |
| 2.2 | `go test ./internal/agentctx` (merge scenarios, unterminated marker) | pass |
| 3.1 | `go test ./internal/sandbox` and `test/e2e/agentctx.sh` | block and skill installed, warn on failure |
| 3.2 | `make e2e` | 3 suites green, e2e files 100755 |
| 3.3 | review of README, security-model, policy docs | none claims enforcement |
| 3.4 | `make lint test`, `make cross`, `make e2e` | all exit 0; live Kiro and Claude unverified |
