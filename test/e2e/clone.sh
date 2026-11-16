#!/usr/bin/env bash
# Clone mode: a sandbox whose workspace is the agent's own git clone of the
# committed HEAD. The host repository, its working tree and its hooks must
# stay byte-identical whatever the agent does in the clone.
. "$(dirname "$0")/lib.sh"

NAME=e2e-cl
DET=e2e-cldet
SHARED=e2e-clshared
REPO=$(mktemp -d)
REPO2=$(mktemp -d)
EMPTY=$(mktemp -d)
PLAIN=$(mktemp -d)
NOGIT=$(mktemp -d)
ERR=$(mktemp)

cleanup() {
	for n in "$NAME" "$DET" "$SHARED"; do pb rm "$n" --volumes >/dev/null 2>&1; done
	rm -rf "$REPO" "$REPO2" "$EMPTY" "$PLAIN" "$NOGIT" "$ERR" "$PODBOX_HOME"
}
trap cleanup EXIT

# Every host git command carries its identity, never the user's.
G() { git -C "$REPO" -c user.name=host -c user.email=host@example.org -c commit.gpgsign=false "$@"; }

# hostprint: the working tree and the whole .git of the host repository.
hostprint() {
	(
		cd "$REPO" || exit 1
		find . -path ./.git -prune -o -type f -print | sort | xargs sha256sum
		find .git -type f | sort | xargs sha256sum
	) | sha256sum
}

json() { jq -r "$1" "$OUT"; }
OUT=$(mktemp)

pb image build proxy shell >/dev/null || { echo "image build failed"; exit 1; }

echo "== host repository"
G init -q -b main
printf 'SECRET=committed\n' >"$REPO/.env"
# .sandboxignore is gitignored: the host's file masks the tracked secret, the clone has none.
printf '.sandboxignore\n' >"$REPO/.gitignore"
printf '/.env\n' >"$REPO/.sandboxignore"
printf 'one\n' >"$REPO/a.txt"
mkdir "$REPO/.husky"
printf '#!/bin/sh\nexit 0\n' >"$REPO/.husky/pre-commit"
G add . && G commit -q -m one
printf 'two\n' >>"$REPO/a.txt" && G commit -q -am two
G remote add origin https://example.org/host.git
G config user.email host-secret@example.org
mkdir -p "$REPO/.git/hooks"
printf '#!/bin/sh\ntouch /tmp/podbox-e2e-host-hook-ran\n' >"$REPO/.git/hooks/pre-commit"
chmod +x "$REPO/.git/hooks/pre-commit"
BLOB=$(printf 'dangling secret\n' | G hash-object -w --stdin)
printf 'uncommitted\n' >>"$REPO/a.txt"
printf 'new\n' >"$REPO/new.txt"
printf 'TOKEN=untracked\n' >"$REPO/.env.local"
HEAD=$(G rev-parse HEAD)
G status --porcelain >/dev/null
BEFORE=$(hostprint)
STATUS=$(G status --porcelain)

echo "== create --clone"
pb create shell "$REPO" --name "$NAME" --clone -o json >"$OUT" 2>"$ERR"
check "create exits 0 and prints the clone" test "$(json .clone.base)" = "$HEAD"
check "create reports 1 uncommitted and 2 untracked" test "$(json '.clone.uncommitted')-$(json '.clone.untracked')" = 1-2
check "create says what the clone leaves out" grep -q '1 uncommitted and 2 untracked file(s) are not in the clone' "$ERR"
CLONE=$(json .clone.path)
check "the clone is outside the sandbox directory" test -d "$CLONE" -a -d "$(dirname "$CLONE")/../clones"
check "no hook notice for a clone" test "$(json '.info // ""')" = ""
check "start" pb start "$NAME" -o json
running "$NAME"

echo "== what the agent sees"
check "cwd is the project path" test "$(sx "$NAME" pwd)" = "$REPO"
check "history is HEAD's" test "$(sx "$NAME" 'git log --oneline | wc -l')" = 2
check "only committed content: the edit is absent" test "$(sx "$NAME" 'tail -1 a.txt')" = two
check "untracked file absent" sx "$NAME" '! test -e new.txt'
check "untracked secret absent" sx "$NAME" '! test -e .env.local'
check "tracked masked file is empty" test -z "$(sx "$NAME" 'cat .env')"
check "the gitignored .sandboxignore is not in the clone" sx "$NAME" '! test -e .sandboxignore'
check "no remote" test -z "$(sx "$NAME" 'git remote')"
check "no host configuration value" sx "$NAME" '! git config --list | grep -q host'
check "no hook" test "$(sx "$NAME" 'find .git/hooks -type f 2>/dev/null | wc -l')" = 0
check "no alternates" sx "$NAME" '! test -e .git/objects/info/alternates'
check "every object has one link" test "$(sx "$NAME" 'find .git/objects -type f -exec stat -c %h {} + | sort -u')" = 1
check "the dangling blob is absent" sx "$NAME" "! git cat-file -e $BLOB"
check "git status works with no ownership error" sx "$NAME" 'git status >/dev/null'
check "git commit without identity fails on the identity" sx "$NAME" 'out=$(git commit --allow-empty -qm x 2>&1); [ $? -ne 0 ] && echo "$out" | grep -qi "identity\|tell me who you are"'

echo "== the agent works in the clone"
check "agent commit with an identity in the environment" sx "$NAME" 'export GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@example.org GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@example.org; echo work > agent.txt && git add agent.txt && git commit -qm agent'
sx "$NAME" 'echo "[core]
	hooksPath = /tmp/x" >> .git/config; mkdir -p .git/hooks; printf "#!/bin/sh\n" > .git/hooks/x; true'
check "host repository, working tree and hooks are unchanged" test "$(hostprint)" = "$BEFORE"
check "host git status is unchanged" test "$(G status --porcelain)" = "$STATUS"
check "the host hook never ran" test ! -e /tmp/podbox-e2e-host-hook-ran

echo "== git-check, ls, restart"
pb git-check "$NAME" >"$OUT" 2>&1
check "git-check exits 0 and says it does not apply" grep -q 'does not apply' "$OUT"
pb git-check "$NAME" --accept >/dev/null 2>&1
check "git-check --accept exits 0 on a clone" test $? -eq 0
pb git-check "$NAME" -o json >"$OUT"
check "git-check JSON: not applicable, no integrity" test "$(json '[.applicable, .git_integrity] | tostring')" = '[false,null]'
pb start "$NAME" -o json >"$OUT" 2>&1
check "start prints no hook notice" test "$(jq -r '.info // ""' "$OUT")" = ""
pb ls >"$OUT"
check "ls has a MODE column showing clone" grep -Eq "^NAME +AGENT +MODE " "$OUT"
check "ls shows the clone sandbox in mode clone" grep -Eq "^$NAME +shell +clone " "$OUT"
pb ls -o json >"$OUT"
check "ls JSON has mode and clone path" test "$(json ".[] | select(.name == \"$NAME\") | .mode + \"|\" + .clone")" = "clone|$CLONE"
sx "$NAME" ': > .sandboxignore; mkdir -p .podbox; echo "rules: []" > .podbox/policy.yaml'
check "restart" restart "$NAME"
check "an emptied .sandboxignore written by the agent unmasks nothing" test -z "$(sx "$NAME" 'cat .env')"
check "still masked after the restart" test -z "$(sx "$NAME" 'cat .env')"
check "the agent's commit survives the restart" test "$(sx "$NAME" 'git log --oneline | wc -l')" = 3
check "host repository still unchanged after the restart" test "$(hostprint)" = "$BEFORE"

echo "== run honors the mode"
# stdout is a pipe so no terminal is allocated and the shell reads EOF.
pb run shell "$REPO" --name "$NAME" </dev/null 2>"$ERR" | cat >/dev/null
check "run without --clone attaches to the clone" test "${PIPESTATUS[0]}" -eq 0
check "run says it attached to the clone" grep -q 'clone sandbox' "$ERR"

echo "== detached HEAD"
git -C "$REPO2" init -q -b main
git -C "$REPO2" -c user.name=h -c user.email=h@example.org commit -q --allow-empty -m a
git -C "$REPO2" -c user.name=h -c user.email=h@example.org commit -q --allow-empty -m b
git -C "$REPO2" checkout -q --detach HEAD~1
DETHEAD=$(git -C "$REPO2" rev-parse HEAD)
pb create shell "$REPO2" --name "$DET" --clone -o json >"$OUT" 2>/dev/null
check "detached HEAD: cloned at that commit" test "$(json .clone.base)" = "$DETHEAD"
check "detached HEAD: no branch reported" test "$(json '.clone.branch // ""')" = ""
pb rm "$DET" --volumes >/dev/null 2>&1

echo "== refusals"
pb create shell "$PLAIN" --name "$DET" --clone >/dev/null 2>"$ERR"
check "not a repository: exit 2" test $? -eq 2
git -C "$EMPTY" init -q
pb create shell "$EMPTY" --name "$DET" --clone >/dev/null 2>"$ERR"
check "empty repository: exit 2" test $? -eq 2
mkdir "$REPO/sub"
pb create shell "$REPO/sub" --name "$DET" --clone >/dev/null 2>"$ERR"
check "subdirectory: exit 2" test $? -eq 2
check "the refusal names the cause" grep -q 'top level' "$ERR"
check "a refusal leaves no state and no clone" test "$(pb ls -o json | jq "[.[] | select(.name == \"$DET\")] | length")" = 0
rmdir "$REPO/sub"
# git missing: a PATH with every tool of /usr/bin but git
for f in /usr/bin/*; do [ "${f##*/}" = git ] || ln -s "$f" "$NOGIT/${f##*/}"; done
PATH=$NOGIT pb create shell "$REPO" --name "$DET" --clone >/dev/null 2>"$ERR"
check "git missing: exit 3 with an install hint" test $? -eq 3 && grep -q 'git not found' "$ERR"
check "git missing leaves nothing behind" test "$(pb ls -o json | jq "[.[] | select(.name == \"$DET\")] | length")" = 0

echo "== mode is fixed at creation"
pb create shell "$REPO" --name "$SHARED" >/dev/null 2>&1
pb run shell "$REPO" --name "$SHARED" --clone </dev/null >/dev/null 2>"$ERR"
check "run --clone on a shared sandbox: exit 1 naming the mode" test $? -eq 1 && grep -q 'shared sandbox' "$ERR"
pb rm "$SHARED" >/dev/null 2>&1

echo "== rm keeps the clone"
pb rm "$NAME" -o json >"$OUT" 2>"$ERR"
check "rm exits 0 and reports the kept clone" test "$(json '.info')" != "" && grep -q "$CLONE" "$ERR"
check "the clone remains with the agent's work" test -f "$CLONE/agent.txt"
pb create shell "$REPO" --name "$NAME" --clone >/dev/null 2>"$ERR"
check "create on a name whose clone remains: exit 1" test $? -eq 1
check "the refusal names podbox rm --volumes" grep -q "podbox rm $NAME --volumes" "$ERR"
pb ls -o json >"$OUT"
check "ls shows the orphan clone" test "$(json ".[] | select(.name == \"$NAME\") | .state + \"|\" + .mode")" = "missing|clone"
check "rm --volumes removes the clone" pb rm "$NAME" --volumes
check "the clone directory is gone" test ! -e "$CLONE"
check "rm of an unknown name is exit 4" test "$(pb rm "$NAME" >/dev/null 2>&1; echo $?)" = 4

echo "== shared mode is unchanged"
check "shared create and start" up "$SHARED" "$REPO"
pb git-check "$SHARED" -o json >"$OUT"
check "shared git-check applies" test "$(json '.applicable')" = true
check "shared sandbox mounts the project: untracked file visible" sx "$SHARED" 'test -e new.txt'
denied "shared .git/config stays read-only" "$SHARED" 'echo x >> .git/config'
pb ls -o json >"$OUT"
check "ls shows shared" test "$(json ".[] | select(.name == \"$SHARED\") | .mode")" = shared
check "rm of a shared sandbox keeps nothing" pb rm "$SHARED" --volumes

echo "== teardown"
check "no podman object left" no_objects_left
check "no clone directory left" test -z "$(ls -A "$PODBOX_HOME/state/clones" 2>/dev/null)"

summary
