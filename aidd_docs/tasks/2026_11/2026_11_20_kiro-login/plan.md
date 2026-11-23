---
objective: "A developer whose Kiro organization signs in through an external identity provider (Microsoft Entra ID, Okta) can log kiro-cli in inside a podbox sandbox with one command, without copying tokens from the host, without opening a port into the sandbox and without decrypting any traffic."
status: implemented
---

# Plan: Kiro login through a loopback callback relay

## Problem

Inside a sandbox, kiro-cli sees no display and falls back to the device flow (Builder ID or IAM Identity Center). An organization bound to an external IdP has no device flow: its login is a browser flow whose callback is `http://localhost:<port>/...` on the machine running kiro-cli. The browser runs on the host, kiro-cli in the sandbox, and the sandbox has no reachable address by design. API keys are not available to the user's organization, and copying the host's `data.sqlite3` is refused.

## Approach

1. Make kiro-cli choose the browser flow in the sandbox (`DISPLAY`), and give it an opener that records the URL instead of opening a browser.
2. `podbox auth login <name>` runs `kiro-cli login`, prints each sign-in URL, listens on the host loopback on the callback port, and relays each browser callback into the sandbox with `podman exec ... curl`. The `redirect_uri` is never changed, so PKCE and the token exchange are untouched.

```
sandbox kiro-cli ─▶ opener (URL file) ─▶ podbox prints URL ─▶ host browser ─▶ Kiro portal / Entra
                                                                                  │ redirect
                                                         http://localhost:<port>/... (host loopback)
                                                                                  │
                                                       podbox auth login (127.0.0.1 and ::1 listener)
                                                                                  │ podman exec curl
                                                               kiro-cli listener inside the sandbox
```

## Phases

| #   | Phase | File | Depends on |
| --- | ----- | ---- | ---------- |
| 0   | Manual check that the relay completes an Entra login (go / no-go) | [`phase-0.md`](./phase-0.md) | none |
| 1   | Kiro image: pinned and checksummed kiro-cli, URL opener, browser flow on | [`phase-1.md`](./phase-1.md) | none (useful even if phase 0 fails) |
| 2   | `podbox auth login`: URL watch, loopback relay, tests, docs | [`phase-2.md`](./phase-2.md) | 0 (go), 1 |

## External sources

| URL | What it settled |
| --- | --------------- |
| https://learn.microsoft.com/en-us/entra/identity-platform/reply-url | Entra ignores the port when matching a `localhost` redirect URI; an unregistered one fails with `AADSTS50011` |
| https://kiro.dev/changelog/cli/external-identity-provider-support-for-kiro-cli/ | kiro-cli supports Entra ID and Okta through a browser OAuth flow |
| https://github.com/kirodotdev/Kiro/issues/12424 | the external IdP gap in headless environments is known upstream |
| https://docs.docker.com/ai/sandboxes/agents/kiro/ | Docker sbx only documents the device flow for Kiro: no prior art for external IdP |
| `https://desktop-release.q.us-east-1.amazonaws.com/latest/manifest.json` | versioned zips exist at `<version>/kirocli-<arch>-linux.zip` with a SHA-256 per package (3.5.0 checked on 2026-11-20); `<version>/manifest.json` is not served (403) |

## Findings this plan relies on

| Finding | How it was obtained | Confidence |
| ------- | ------------------- | ---------- |
| `DISPLAY` set makes kiro-cli use the portal flow ("Opening auth portal and logging in...") | observed in a sandbox | verified |
| The portal callback is `http://localhost:3128` and a relayed `curl --noproxy '*'` gets `302` | observed in a sandbox | verified |
| The Linux kiro-cli 3.5.0 has no `kiro://` string and an external IdP module with OIDC discovery and a code exchange | `strings` on the binary | inferred |
| The portal returns to `http://localhost:<port>/signin/callback?login_option=external_idp&issuer_url=…&client_id=…`; kiro-cli then opens the Entra authorize URL itself and takes its callback on a loopback `/oauth/callback` | observed in phase 0 | verified |
| kiro-cli fetches the issuer's OIDC discovery and tokens from inside the sandbox, so `login.microsoftonline.com` must be allowed | observed in phase 0 (`Failed to fetch OIDC discovery` until allowed) | verified |
| A callback whose path carries shell escapes (`\?`, `\&` pasted by zsh) is refused with "Invalid callback path" | observed in phase 0 | verified; the relay takes the browser's request, so no paste |

## Impact

| Category | Verdict and evidence | Handled in |
| -------- | -------------------- | ---------- |
| security | a host loopback listener is open only while `auth login` runs, GET only, on the callback path only; the code and state never reach logs or argv (curl reads its URL from stdin); no inbound route to the sandbox is created; podbox never opens a browser on the host (a sandbox-chosen URL opened on the host is a phishing vector); the Kiro token ends up in the agent home volume, as with every agent login | 2 |
| runtime | `curl` is in the base image (`base.Containerfile` line 8); `/tmp` is a per-container tmpfs (`agentspec.go`), so the URL file never reaches the host or another sandbox | 1, 2 |
| regression | `agents.Agent` gains a field; `images.inputs` gains files for the kiro image; covered by `internal/images/images_test.go` and existing sandbox tests | 1, 2 |
| documentation | README (commands table, Kiro login section, known limits), `docs/security-model.md` (login relay), `CONTRIBUTING.md` (e2e line) | 2 |
| contributor checks | `make lint test`, `make cross`, `make e2e` with a new `test/e2e/authlogin.sh` | 2 |

## Decisions

| Decision | Why |
| -------- | --- |
| Relay the callback over `podman exec`, not a published port | the sandbox keeps "no route except the proxy"; the host already trusts `exec` |
| Never rewrite `redirect_uri`, never decrypt traffic, never reverse engineer kiro-cli storage | the token exchange must match the authorize request; kiro-cli is closed source and changes often |
| `DISPLAY=:0` and `BROWSER` set in the kiro image, not per command | `podbox run kiro` starts `kiro-cli chat`, which offers to log in itself; any other GUI attempt fails harmlessly (no X server) |
| The opener lives in the base image at `/usr/local/bin/podbox-open`, linked as `xdg-open` | root-owned, so the agent cannot replace it; other agents (Claude Code) benefit from a URL that is shown rather than lost |
| kiro-cli is pinned in source with a SHA-256 per architecture, bumped by hand | `latest` changes the login behaviour on any rebuild; no versioned manifest is served, so checksums cannot be fetched at build time from a pinned URL |
| `auth login` prints URLs, it does not open them | the URL is chosen by code in the sandbox |
| `auth login <name> [-- <cmd>...]` runs the agent's login command by default and accepts an override | lets a user pass kiro-cli flags, and lets the e2e suite test the relay with a fake login server without a Kiro account |
| Exit code: the login command's own code, as `exec` does; 2 for an agent with no login command and no override; 1 for a busy callback port | no new exit code |
