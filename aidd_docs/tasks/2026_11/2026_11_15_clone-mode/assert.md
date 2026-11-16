# Assert: Clone mode

## Round 1

- Head: `4432342`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint` and `make test` (one run) | 0 | |
| `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66 passed, 0 failed) | 0 | |
