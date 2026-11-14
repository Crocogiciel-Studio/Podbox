# Run: Podbox M0 + v0.1 — closed-network sandbox

## Run 1

- Head: `b7464d1`
- Left: Kiro image unverified (installer reached, download host `prod.download.cli.kiro.dev` refused 403 by the build environment's firewall); Claude login flow unverified (no interactive login possible); CI e2e job on GitHub runners not executed (no push); macOS/Windows only cross-compiled; resource limits verified only on the "not delegated" path. Deviations: Java trusts the corporate CA through the JDK cacerts store instead of a `JAVA_TOOL_OPTIONS` truststore; the agent container keeps PID 1 alive with an entrypoint loop instead of `--init` (catatonit absent); `attach` opens a new agent session via exec; base image apt falls back to HTTPS with Node's root CAs when plain-HTTP mirrors are blocked; stale podbox images are pruned after each build.

| Task | Validation | Result |
| ---- | ---------- | ------ |
| 1.1 | `go build ./...` | builds |
| 1.2 | `go test ./internal/output ./internal/cli`; `podbox --output yaml version`; `podbox -o json nosuch` | text/JSON and error mapping tests pass; exit 2; JSON error on stdout |
| 1.3 | `go test ./internal/config` | XDG, PODBOX_HOME and darwin paths resolve |
| 1.4 | `podbox version --output json` | JSON with version, commit, os, arch |
| 1.5 | `make lint test cross` | green; 6 binaries in dist/ |
| 2.1 | `go build ./...` | sandbox and cli use only `runtime.Runtime` specs |
| 2.2 | `go test ./internal/runtime` (fake podman script) | deterministic argv, exit 42 propagated, not-found mapped |
| 2.3 | `podbox doctor -o json` on this host; `PATH=/nonexistent podbox doctor` | every check listed, cgroups and storage warn, exit 0; podman missing fails with install hint, exit 3 |
| 3.1 | `go test ./internal/policy` (TestDecide) | exact, wildcard, deny-wins, default deny |
| 3.2 | TestInvalidOrMissingNeverAllows, TestProjectCannotWidenGlobalDeny | invalid/missing layers add no allow |
| 3.3 | `podbox policy allow t1 pypi.org` then `show`, `deny -o json`, `--scope project` | grant listed with layer; atomic rewrite test leaves no temp file |
| 4.1 | `go test -race ./internal/proxy` (TestTunnelAndForward) | TLS tunnelled and HTTP forwarded |
| 4.2 | TestDestinationGuard, TestCheckIP | loopback/metadata always 403, private 403 without allow_private, 200 with it |
| 4.3 | TestHotReload, TestCorruptAtStartDeniesAll | change applied < 2 s; corrupt file keeps previous; corrupt at start denies all |
| 4.4 | TestNotAllowed | 403 body equals documented text, `X-Podbox-Blocked: 1` |
| 4.x | TestUpstreamChaining, TestUpstreamAuthRequired; host smoke test chained to `$HTTPS_PROXY` | chained with basic auth, refusal unchanged; registry.npmjs.org 200 via real upstream |
| 5.1 | `podbox image build proxy`; `podman run <proxy> --allow-all` | FROM scratch, no pull, answers on 3128 |
| 5.2 | `podman run podbox-claude claude --version`; `ca_file` build | `2.1.325 (Claude Code)` as user agent; podbox CA in /etc/ssl/certs, NODE_EXTRA_CA_CERTS set |
| 5.3 | `podbox image ls --output json` | images with image/agent/version labels |
| 5.4 | `podbox image build kiro` | unverified: `prod.download.cli.kiro.dev` 403 from the environment firewall |
| 6.1 | `podbox exec c1 -- claude --version` in a claude sandbox | Claude Code runs as uid 1000; onboarding TUI reached through the proxy |
| 6.2 | TestListFromLabelsWithStaleState | ls merges labels and state dir |
| 6.3 | `test/e2e/evasion.sh` (allowed egress, network escapes) | allowed host via proxy and real upstream; no direct route, DNS or host loopback |
| 6.4 | `podman inspect podbox-t1-agent`; TestAgentSpecHardening | cap-drop ALL, no-new-privileges, user 1000, keep-id, no proxy env from host |
| 6.5 | evasion.sh filesystem checks; `go test ./internal/workspace` | .env empty, .ssh/.git/hooks/secrets empty read-only, host files intact, warning for api_token.txt |
| 6.6 | Claude TUI 45 s in sandbox, proxy decision log | api.anthropic.com, platform.claude.com, raw.githubusercontent.com: all covered by defaults; login unverified |
| 6.7 | `test/e2e/lifecycle.sh` | create/ls/exec/stop/start/rm round trip, nothing left after rm |
| 7.1 | `make e2e` (evasion.sh) | 40 passed, 0 failed, exit 0 |
| 7.2 | `make e2e` (lifecycle.sh) | 24 passed, 0 failed, exit 0 |
| 7.3 | `.github/workflows/ci.yml` e2e job | defined (doctor + both suites); not run on GitHub |
| 7.3b | evasion.sh upstream section; lifecycle.sh home marker | allowed host via allow-all basic-auth upstream chained to the real proxy; marker survives rm + create |
| 7.4 | README, docs/security-model.md, docs/policy.md | install → doctor → run → policy allow walk-through |
| 7.5 | `make lint test cross e2e` | all green |
