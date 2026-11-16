#!/usr/bin/env bash
# podbox diff: what the agent changed in a clone sandbox, read with git inside
# the sandbox and stripped of terminal control sequences. The host repository
# must stay byte-identical and the agent's index and objects untouched.
. "$(dirname "$0")/lib.sh"

NAME=e2e-cd
SHARED=e2e-cdshared
REPO=$(mktemp -d)
OUT=$(mktemp)
ERR=$(mktemp)

cleanup() {
	for n in "$NAME" "$SHARED"; do pb rm "$n" --volumes >/dev/null 2>&1; done
	rm -rf "$REPO" "$OUT" "$ERR" "$PODBOX_HOME"
}
trap cleanup EXIT

G() { git -C "$REPO" -c user.name=host -c user.email=host@example.org -c commit.gpgsign=false "$@"; }
hostprint() {
	(
		cd "$REPO" || exit 1
		find . -path ./.git -prune -o -type f -print | sort | xargs sha256sum
		find .git -type f | sort | xargs sha256sum
	) | sha256sum
}
json() { jq -r "$1" "$OUT"; }
# printable: nothing but newline, tab and printable ASCII in the file.
printable() { ! LC_ALL=C tr -d '\n\t' <"$1" | LC_ALL=C grep -q '[^ -~]'; }
# agentstate: the agent's index, config and object counts.
agentstate() { sx "$NAME" 'sha256sum .git/index .git/config; git count-objects -v; git status --porcelain'; }

pb image build proxy shell >/dev/null || { echo "image build failed"; exit 1; }

echo "== setup"
G init -q -b main
printf 'one\ntwo\n' >"$REPO/a.txt"
printf 'to delete\n' >"$REPO/del.txt"
seq 1 40 >"$REPO/ren.txt"
G add . && G commit -q -m base
HEAD=$(G rev-parse HEAD)
BEFORE=$(hostprint)
check "create clone sandbox" q pb create shell "$REPO" --name "$NAME" --clone
check "start" q pb start "$NAME"
running "$NAME"

echo "== no changes"
pb diff "$NAME" >"$OUT"; rc=$?
check "exit 0" test $rc -eq 0
check "says no changes since the base" grep -q "no changes since ${HEAD:0:12}" "$OUT"
pb diff "$NAME" -o json >"$OUT"
check "JSON has an empty files array and the base" test "$(json '[(.files | length), .base, .name] | join(" ")')" = "0 $HEAD $NAME"

echo "== the agent works"
sx "$NAME" 'export GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@example.org GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@example.org
echo committed > committed.txt && git add committed.txt && git commit -qm agent
echo "uncommitted line" >> a.txt
rm del.txt
git mv ren.txt ren2.txt 2>/dev/null; sed "5s/.*/five/" ren2.txt > ren2.tmp && mv ren2.tmp ren2.txt
echo untracked > u.txt
printf "\000\001\002binary" > bin.dat
printf "line\033]52;c;aGVsbG8=\007\033[2J\033[31mred\033[0m\015overwrite\302\233x end\n" > "$(printf "evil\033[31mname\033]0;pwned\007.txt")"
printf "x\n" > "$(printf "new\nline.txt")"'
STATE=$(agentstate)

echo "== diff"
pb diff "$NAME" >"$OUT" 2>"$ERR"; rc=$?
check "exit 0" test $rc -eq 0
check "summary header" grep -Eq '^STATUS +ADDED +DELETED +PATH' "$OUT"
check "committed file listed" grep -Eq '^added +1 +0 +committed.txt' "$OUT"
check "modified file with its counts" grep -Eq '^modified +1 +0 +a.txt' "$OUT"
check "deleted file with its counts" grep -Eq '^deleted +0 +1 +del.txt' "$OUT"
check "rename detected" grep -Eq '^renamed .*ren.txt -> ren2.txt' "$OUT"
check "untracked file marked" grep -Eq '^untracked +1 +0 +u.txt' "$OUT"
check "binary file has dashes" grep -Eq '^untracked +- +- +bin.dat' "$OUT"
check "a total line" grep -Eq "file\(s\) changed, \+[0-9]+ -[0-9]+ since ${HEAD:0:12}" "$OUT"
check "the patch holds the committed change" grep -q '^+committed$' "$OUT"
check "the patch holds the uncommitted change" grep -q '^+uncommitted line$' "$OUT"
check "the patch holds the untracked file" grep -q '^+untracked$' "$OUT"
check "the patch holds the deletion" grep -q '^-to delete$' "$OUT"
check "the patch holds a binary patch" grep -q '^GIT binary patch' "$OUT"
check "text output is printable: no ESC, CR, BEL or C1" printable "$OUT"
check "the hostile content is reduced to its text" grep -q 'linered' "$OUT"
check "the hostile name is reduced to its text" grep -q 'evilname.txt' "$OUT"
check "a newline in a name stays on one line" grep -q 'new?line.txt' "$OUT"

pb diff "$NAME" --stat >"$OUT"
check "--stat prints the summary" grep -q 'a.txt' "$OUT"
check "--stat prints no patch" test "$(grep -c '^diff --git' "$OUT")" = 0

pb diff "$NAME" -o json >"$OUT"
check "JSON files: path, status, counts" test "$(json '.files[] | select(.path == "a.txt") | [.status, .added, .deleted, .binary, .untracked] | join(" ")')" = "modified 1 0 false false"
check "JSON rename has old_path" test "$(json '.files[] | select(.status == "renamed") | .old_path + ">" + .path')" = "ren.txt>ren2.txt"
check "JSON untracked and binary flags" test "$(json '.files[] | select(.path == "bin.dat") | [.binary, .untracked] | join(" ")')" = "true true"
check "JSON committed file is not untracked" test "$(json '.files[] | select(.path == "committed.txt") | .untracked')" = false
check "JSON has the sanitized patch" test "$(json '.patch | contains("+uncommitted line")')" = true
check "JSON raw bytes hold no control byte" printable "$OUT"
check "JSON strings hold no control byte" test "$(jq -r '[.patch, (.files[] | .path, .old_path // "")] | join("")' "$OUT" | LC_ALL=C tr -d '\n\t' | LC_ALL=C grep -c '[^ -~]')" = 0
pb diff "$NAME" --stat -o json >"$OUT"
check "JSON --stat has no patch" test "$(json 'has("patch")')" = false

echo "== nothing of the agent changed, host untouched"
check "agent index, config, objects and status unchanged by the diff" test "$(agentstate)" = "$STATE"
check "no temporary index left" sx "$NAME" '! ls /tmp/podbox-diff-* >/dev/null 2>&1'
check "host repository unchanged" test "$(hostprint)" = "$BEFORE"

echo "== a hostile clone configuration"
sx "$NAME" 'printf "[core]\n\tpager = touch /tmp/pager-ran\n[diff]\n\texternal = touch /tmp/external-ran\n[diff \"x\"]\n\ttextconv = touch /tmp/textconv-ran\n\tcommand = touch /tmp/driver-ran\n" >> .git/config; echo "*.txt diff=x" > .gitattributes'
pb diff "$NAME" >"$OUT" 2>/dev/null
check "plain patch still printed" grep -q '^+uncommitted line$' "$OUT"
check "no pager, external diff or textconv ran" sx "$NAME" '! ls /tmp/pager-ran /tmp/external-ran /tmp/textconv-ran /tmp/driver-ran >/dev/null 2>&1'
sx "$NAME" 'rm .gitattributes'

echo "== refusals"
pb stop "$NAME" >/dev/null
pb diff "$NAME" >"$OUT" 2>"$ERR"
check "stopped sandbox: exit 1" test $? -eq 1
check "stopped sandbox: the message names podbox start" grep -q "podbox start $NAME" "$ERR"
check "start again" q pb start "$NAME"
pb create shell "$REPO" --name "$SHARED" >/dev/null 2>&1
pb diff "$SHARED" >"$OUT" 2>"$ERR"
check "shared sandbox: exit 2" test $? -eq 2
check "shared sandbox: the message names --clone" grep -q -- '--clone' "$ERR"
pb diff "$SHARED" -o json >"$OUT" 2>/dev/null
check "shared sandbox: JSON error is usage" test "$(json .error.kind)" = usage
pb rm "$SHARED" >/dev/null 2>&1
pb diff e2e-cdnone >/dev/null 2>&1
check "unknown sandbox: exit 4" test $? -eq 4

echo "== a patch larger than the cap"
sx "$NAME" 'head -c 36000000 /dev/zero | tr "\000" "a" > big.txt'
pb diff "$NAME" >"$OUT" 2>"$ERR"
check "exit 1" test $? -eq 1
check "nothing printed" test ! -s "$OUT"
check "the message suggests --stat" grep -q -- '--stat' "$ERR"
pb diff "$NAME" --stat >"$OUT" 2>/dev/null
check "--stat still works" grep -q 'big.txt' "$OUT"
sx "$NAME" 'rm big.txt'
check "host repository still unchanged" test "$(hostprint)" = "$BEFORE"

echo "== teardown"
check "rm --volumes" q pb rm "$NAME" --volumes
check "no podman object left" no_objects_left
check "no clone directory left" test -z "$(ls -A "$PODBOX_HOME/state/clones" 2>/dev/null)"

summary
