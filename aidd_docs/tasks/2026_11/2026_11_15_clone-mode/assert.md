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

## Round 6 (phase 3)

- Head: `4bada2e`
- Uncommitted changes: no
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint test` | 0 | |
| `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66, clonediff 115, cloneexport 141 passed, 0 failed) | 0 | |
| `git ls-files -s test/e2e` | seven scripts 100755 | |
| cloneexport.sh against a build without the five fixes | 22 failed, 119 passed | expected: shows the safety cases were real |

## Round 7 (phase 3 repair)

- Head: `8aa8171`
- Uncommitted changes: no (this file and run.md aside)
- Verdict: PASS

| Check | Exit | Failure |
| --- | --- | --- |
| `make lint test` | 0 | |
| `make cross` | 0 | |
| `make e2e` (evasion 187, lifecycle 24, agentctx 43, clone 66, clonediff 115, cloneexport 158 passed, 0 failed) | 0 | |
| new cloneexport.sh against a 779a2ed build | 9 failed, 149 passed | expected: shows the review findings were real |
| `git ls-files -s test/e2e` | seven scripts 100755 | |
