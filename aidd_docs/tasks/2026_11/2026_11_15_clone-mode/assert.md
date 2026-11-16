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

## Round 2 (phase 2)

- Head: `4ea98d2`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint test` and `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66, clonediff 54 passed, 0 failed) | 0 | |
| `git ls-files -s test/e2e` | six scripts 100755 | |

## Round 3 (phase 2 repair)

- Head: `6e549e4`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint test` and `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66, clonediff 69 passed, 0 failed) | 0 | |
| `git ls-files -s test/e2e` | six scripts 100755 | |

## Round 4 (phase 2, second repair)

- Head: `3f1a772`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint test` and `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66, clonediff 93 passed, 0 failed) | 0 | |
| new clonediff.sh against a build of 3adfe9e | 26 failed, 67 passed | expected: shows the three deal breakers were real |
| `git ls-files -s test/e2e` | six scripts 100755 | |

## Round 5 (phase 2, third repair)

- Head: `d48635e`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint test` and `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66, clonediff 115 passed, 0 failed) | 0 | |
| new clonediff.sh against a build of 8d4f142 | 20 failed, 95 passed | expected: shows the deal breakers were real |
| `git ls-files -s test/e2e` | six scripts 100755 | |
