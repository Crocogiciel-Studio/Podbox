#!/bin/sh
# podbox-open is the sandbox's xdg-open and BROWSER. No browser runs in a
# sandbox: the URL is shown to the human, who opens it on the host, and
# recorded for `podbox auth login`, which relays the login callback back in.
# It never starts a program and never reads stdin.
url=$(printf '%s' "${1-}" | tr -d '\000-\037\177')
[ -n "$url" ] || exit 0
dir=/tmp/podbox-open
mkdir -p -m 0700 "$dir" 2>/dev/null
printf '%s\n' "$url" >> "$dir/urls" 2>/dev/null
msg=$(printf '\n>>> Open this URL in a browser on your machine:\n%s\n' "$url")
{ printf '%s\n\n' "$msg" > /dev/tty; } 2>/dev/null || printf '%s\n\n' "$msg" >&2
exit 0
