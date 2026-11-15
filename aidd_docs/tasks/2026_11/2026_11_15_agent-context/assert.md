# Assert: Agent context

## Round 1

- Head: `dcb201a`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint` | 0 | |
| `go test -count=1 ./...` (`make test`) | 0 | |
| `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 35 passed) | 0 | |
