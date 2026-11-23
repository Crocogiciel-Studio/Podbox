---
status: done
---

# Instruction: Manual check of the callback relay (go / no-go)

Result (2026-11-20): go. Both legs relayed by hand completed an Entra ID login on kiro-cli 3.5.0, once `login.microsoftonline.com` was allowed. Pasting a URL into zsh adds backslashes (`url-quote-magic`) that break the path: relay with `"$(pbpaste | tr -d '\\\n')"` instead of a pasted literal.

Done by the human on the Mac, with the current image, before any code. About 10 minutes. It answers one question: does the Entra step redirect to `localhost`, so that a relay can finish the login?

Never paste a callback URL (it holds a one-time code) into a chat, a ticket or a log.

## Step 1: variables and a running sandbox

```sh
NAME=kiro-brain-system          # see: podbox ls
podbox start "$NAME"            # no-op when already running
lsof -nP -iTCP:3128 -sTCP:LISTEN   # must print nothing: no host program may catch the callback
```

## Step 2: install a temporary URL opener in the agent home

```sh
podbox exec "$NAME" -- sh -c 'mkdir -p ~/bin && cat > ~/bin/xdg-open <<"EOF"
#!/bin/sh
printf "%s\n" "$1" >> "$HOME/login-urls.txt"
printf "\n>>> Open this URL in your Mac browser:\n%s\n\n" "$1" > /dev/tty 2>/dev/null || printf "%s\n" "$1" >&2
EOF
chmod +x ~/bin/xdg-open && : > ~/login-urls.txt'
```

## Step 3: terminal 1, start the login

```sh
podbox exec "$NAME" -- env DISPLAY=:0 BROWSER=/home/agent/bin/xdg-open \
  PATH=/home/agent/bin:/usr/local/bin:/usr/bin:/bin kiro-cli login
```

Expected: `Opening auth portal and logging in...` and a URL `https://app.kiro.dev/signin?...redirect_uri=http%3A%2F%2Flocalhost%3A<port>...`. Leave it running.

## Step 4: first leg, the Kiro portal

1. Open the URL in the Mac browser, enter the work email.
2. The browser ends on a page that cannot load `http://localhost:<port>/...`. Copy the full address.
3. Terminal 2:

```sh
podbox exec "$NAME" -- curl -sS --noproxy '*' -o /dev/null \
  -w 'HTTP %{http_code} -> %{redirect_url}\n' 'PASTE-THE-LOCALHOST-URL'
```

Expected: `HTTP 302` to a URL without `auth_status=error`. A 302 alone proves only that the listener answered: `-> https://app.kiro.dev/signin?auth_status=error&...&error_message=Invalid%20callback%20path` means it refused the path (observed on 2026-11-20). The listener of the portal leg answers on `http://localhost:<port>` with a `login_option` query (from the binary's strings); compare the path of the copied URL with that before relaying.

## Step 5: second leg, Entra

```sh
podbox exec "$NAME" -- cat /home/agent/login-urls.txt
```

Look only at the last line's host and its `redirect_uri=` value (URL-decoded).

| What you see | Meaning | Next |
| ------------ | ------- | ---- |
| a `login.microsoftonline.com` URL with `redirect_uri=http://localhost...` or `http://127.0.0.1...` | relay can work | open it, sign in, relay the failed localhost address with the step 4 command, go to step 6 |
| Microsoft shows `AADSTS50011` after sign-in | the Amadeus app registration lacks a localhost reply URL | send IT the request below; phase 1 can proceed meanwhile |
| a `redirect_uri=kiro://...` | the relay cannot work | stop; report to the plan owner |
| only the portal URL, and terminal 1 keeps spinning or fails | no second leg is offered | stop; copy terminal 1's last lines (no URL) to the plan owner |

Request for IT when `AADSTS50011` shows (fill the path from the `redirect_uri` shown by Microsoft):

> Please add the redirect URI `http://localhost/<path>` (platform: Mobile and desktop applications) to the Entra app registration used by Kiro (client ID shown in the sign-in URL). Entra ignores the port of localhost redirect URIs, so one entry covers kiro-cli's local callback.

## Step 6: confirm

```sh
podbox exec "$NAME" -- kiro-cli whoami
podbox run kiro                 # chat starts without a login prompt
```

## Step 7: clean up

```sh
podbox exec "$NAME" -- rm -f /home/agent/bin/xdg-open /home/agent/login-urls.txt
```

## Outcome

| Result | Decision |
| ------ | -------- |
| `whoami` shows the work account | go: phase 2 |
| `AADSTS50011` | go once IT adds the reply URL; repeat steps 3 to 6 |
| `kiro://` or no second leg | no-go: phase 2 is dropped, phase 1 is kept, the upstream issue is commented |
