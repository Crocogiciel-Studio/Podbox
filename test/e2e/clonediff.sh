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
printf "line\033]52;c;aGVsbG8=\007\033[2J\033[31mred\033[0m\015overwrite\302\233x end\010\n" > "$(printf "evil\033[31mname\033]0;pwned\007.txt")"
printf "x\n" > "$(printf "new\nline.txt")"
printf "ignored.log\nhidden.txt\nstaged.txt\ndropped.txt\n" > .gitignore; echo hidden > ignored.log
echo hidden > hidden.txt; git add -f hidden.txt; git commit -qm hidden
echo staged > staged.txt; git add -f staged.txt
echo dropped > dropped.txt; git add -f dropped.txt; git commit -qm dropped; git rm --cached -q dropped.txt
echo run > run.sh; chmod +x run.sh; ln -s a.txt link
mkdir nested && git -C nested init -q && git -C nested -c user.name=n -c user.email=n@example.org commit -q --allow-empty -m nested'
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
check "a binary file is summarised, its content not printed" grep -q '^Binary file: contains NUL bytes, content not shown' "$OUT"
check "no base85 binary patch is printed" test "$(grep -c '^GIT binary patch' "$OUT")" = 0
check "a binary file is flagged in the summary" grep -Eq '^untracked +- +- +bin.dat .*contains NUL bytes' "$OUT"
check "a file the agent committed although .gitignore lists it is in the diff" grep -Eq '^added +1 +0 +hidden.txt' "$OUT"
check "a listed file only staged with -f is in the diff" grep -Eq '^added +1 +0 +staged.txt' "$OUT"
check "a listed file dropped from the index but committed is in the diff" grep -Eq '^added +1 +0 +dropped.txt' "$OUT"
check "the patch holds the hidden file's content" grep -q '^+hidden$' "$OUT"
check "an ignored untracked file is not listed" test "$(grep -Ec '^[a-z]+ +[0-9-]+ +[0-9-]+ +ignored.log' "$OUT")" = 0
check "a symlink and an executable are flagged" grep -q 'link .*symlink' "$OUT" && grep -q 'run.sh .*executable' "$OUT"
check "C0 controls in content are visible" grep -q 'x end<U+0008>' "$OUT"
check "text output is printable: no ESC, CR, BEL or C1" printable "$OUT"
check "the hostile content keeps its payload visible" grep -q 'line<OSC>52;c;aGVsbG8=<U+0007><CSI>2J<CSI>31mred<CSI>0m<U+000D>overwrite<U+009B>x end<U+0008>' "$OUT"
check "the hostile name keeps its payload visible" grep -q 'evil<CSI>31mname<OSC>0;pwned<U+0007>.txt' "$OUT"
check "ignored files are counted, not hidden" grep -Eq '^[0-9]+ ignored untracked path\(s\) not shown' "$OUT"
check "a nested repository is flagged" grep -q 'nested.*\[nested repository' "$OUT"
check "a newline in a name stays on one line" grep -q 'new?line.txt' "$OUT"

pb diff "$NAME" --stat >"$OUT"
check "--stat prints the summary" grep -q 'a.txt' "$OUT"
check "--stat prints no patch" test "$(grep -c '^diff --git' "$OUT")" = 0

pb diff "$NAME" -o json >"$OUT"
check "JSON files: path, status, counts" test "$(json '.files[] | select(.path == "a.txt") | [.status, .added, .deleted, .binary, .untracked] | join(" ")')" = "modified 1 0 false false"
check "JSON rename has old_path" test "$(json '.files[] | select(.status == "renamed") | .old_path + ">" + .path')" = "ren.txt>ren2.txt"
check "JSON untracked and binary flags" test "$(json '.files[] | select(.path == "bin.dat") | [.binary, .untracked] | join(" ")')" = "true true"
check "JSON committed file is not untracked" test "$(json '.files[] | select(.path == "committed.txt") | .untracked')" = false
check "JSON counts the ignored paths" test "$(json '.ignored >= 1')" = true
check "JSON flags the nested repository" test "$(json '.files[] | select(.path == "nested") | .gitlink')" = true
check "JSON flags no other gitlink" test "$(json '[.files[] | select(.gitlink)] | length')" = 1
check "JSON schema and symlink, exec, mode" test "$(json '[.schema, (.files[] | select(.path == "link") | .symlink), (.files[] | select(.path == "run.sh") | [.exec, .mode] | join(":"))] | join(" ")')" = "1 true true:100755"
check "JSON path_b64 is the raw path" test "$(json '.files[] | select(.path == "a.txt") | .path_b64' | base64 -d)" = a.txt
check "JSON lists the hidden tracked files, not untracked" test "$(json '[.files[] | select(.path == "hidden.txt" or .path == "staged.txt" or .path == "dropped.txt") | .untracked] | join(" ")')" = "false false false"
check "JSON has the sanitized patch" test "$(json '.patch | contains("+uncommitted line")')" = true
check "JSON raw bytes hold no control byte" printable "$OUT"
check "JSON strings hold no control byte" test "$(jq -r '[.patch, (.files[] | .path, .old_path // "")] | join("")' "$OUT" | LC_ALL=C tr -d '\n\t' | LC_ALL=C grep -c '[^ -~]')" = 0
pb diff "$NAME" --stat -o json >"$OUT"
check "JSON --stat has no patch" test "$(json 'has("patch")')" = false

echo "== nothing of the agent changed, host untouched"
check "agent index, config, objects and status unchanged by the diff" test "$(agentstate)" = "$STATE"
check "no temporary index left" sx "$NAME" '! ls /tmp/podbox-diff-* >/dev/null 2>&1'
check "host repository unchanged" test "$(hostprint)" = "$BEFORE"

echo "== attributes cannot make text unreadable"
sx "$NAME" 'printf "* binary\n" > .gitattributes; mkdir -p .git/info; printf "code.py -diff\n" >> .git/info/attributes; printf "print(1)\n" > code.py'
pb diff "$NAME" >"$OUT"
check "a text file under '* binary' and an info/attributes -diff is readable" grep -q '^+print(1)$' "$OUT"
check "an edited text file is readable too" grep -q '^+uncommitted line$' "$OUT"
check "no base85 patch and no 'Binary files differ' for text" test "$(grep -c '^GIT binary patch\|^Binary files' "$OUT")" = 0
check "the summary says attributes flagged it" grep -q 'code.py .*git attributes mark it binary; shown as text' "$OUT"
check "a real binary stays summarised" grep -q '^Binary file: contains NUL bytes' "$OUT"
pb diff "$NAME" -o json >"$OUT"
check "JSON: text file binary=false, binary_attr=true" test "$(json '.files[] | select(.path == "code.py") | [.binary, .binary_attr] | join(" ")')" = "false true"
check "JSON: real binary binary=true" test "$(json '.files[] | select(.path == "bin.dat") | .binary')" = true
pb diff "$NAME" --stat -o json >"$OUT"
check "--stat reaches the same verdicts" test "$(json '[.files[] | select(.path == "code.py" or .path == "bin.dat") | "\(.path)=\(.binary)"] | join(" ")')" = "bin.dat=true code.py=false"
sx "$NAME" 'rm -f .gitattributes .git/info/attributes code.py'

echo "== names a reviewer can misread"
sx "$NAME" 'echo 1 > Case.TXT; echo 2 > case.txt; f=$(printf "caf\303\251.txt"); echo 3 > "$f"'
pb diff "$NAME" >"$OUT"
check "case collisions are reported" grep -q '2 path(s) differ only by case' "$OUT"
check "non-ASCII paths are reported" grep -q '1 path(s) have non-ASCII' "$OUT"
pb diff "$NAME" -o json >"$OUT"
check "JSON counts them" test "$(json '[.case_collisions, .non_ascii] | join(" ")')" = "2 1"
check "path_b64 of a non-ASCII name is lossless" test "$(json '.files[] | select(.non_ascii_x // (.path | test("caf"))) | .path_b64' | base64 -d | od -An -tx1 | tr -d ' \n')" = "636166c3a92e747874"
sx "$NAME" 'rm -f Case.TXT case.txt caf*.txt'

echo "== git replace cannot fake the base"
PATHS=$(pb diff "$NAME" --stat -o json | jq -c '[.files[].path]')
sx "$NAME" "export GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@example.org GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@example.org GIT_INDEX_FILE=/tmp/e2e-ix
git add -A >/dev/null 2>&1; T=\$(git write-tree); C=\$(git commit-tree \$T -m fake); rm -f /tmp/e2e-ix; git replace $HEAD \$C"
check "the agent planted a replace ref for the base" sx "$NAME" "git for-each-ref refs/replace | grep -q $HEAD"
pb diff "$NAME" -o json >"$OUT"
check "the diff still lists the same files" test "$(json '[.files[].path]' | jq -c .)" = "$PATHS"
check "the diff still shows the modification" test "$(json '.files[] | select(.path == "a.txt") | .status')" = modified
check "the patch is still there" test "$(json '.patch | contains("+uncommitted line")')" = true

echo "== a hostile clone configuration"
sx "$NAME" 'printf "[core]\n\tpager = touch /tmp/pager-ran\n[diff]\n\texternal = touch /tmp/external-ran\n[diff \"x\"]\n\ttextconv = touch /tmp/textconv-ran\n\tcommand = touch /tmp/driver-ran\n" >> .git/config; echo "*.txt diff=x" > .gitattributes'
pb diff "$NAME" >"$OUT" 2>/dev/null
check "plain patch still printed" grep -q '^+uncommitted line$' "$OUT"
check "no pager, external diff or textconv ran" sx "$NAME" '! ls /tmp/pager-ran /tmp/external-ran /tmp/textconv-ran /tmp/driver-ran >/dev/null 2>&1'
sx "$NAME" 'rm .gitattributes'
sx "$NAME" 'mkdir -p .git/hooks; printf "#!/bin/sh\ntouch /tmp/hook-ran\nsleep 600\n" > .git/hooks/post-index-change; chmod +x .git/hooks/post-index-change'
pb diff "$NAME" --stat >"$OUT" 2>"$ERR"
check "a post-index-change hook does not run and does not hang the diff" test $? -eq 0 && sx "$NAME" '! test -e /tmp/hook-ran'
sx "$NAME" 'rm .git/hooks/post-index-change'
sx "$NAME" 'printf "[filter \"slow\"]\n\tclean = sleep 600\n" >> .git/config; printf "a.txt filter=slow\nhang.txt filter=slow\n" > .gitattributes; echo x > hang.txt'
PODBOX_DIFF_TIMEOUT=5 pb diff "$NAME" >"$OUT" 2>"$ERR"
check "a hanging clean filter: exit 1" test $? -eq 1
check "a hanging clean filter: nothing printed" test ! -s "$OUT"
check "a hanging clean filter: the timeout is named" grep -q 'timed out after 5s' "$ERR"
check "the hung filter does not outlive the diff" eventually 15 sx "$NAME" '! grep -l "sleep 6[0]0" /proc/[0-9]*/cmdline 2>/dev/null | grep -q .'
sx "$NAME" 'rm -f .gitattributes hang.txt; git config --remove-section filter.slow'
pb diff "$NAME" --stat >"$OUT" 2>/dev/null
check "the diff works again once the filter is gone" grep -q 'a.txt' "$OUT"

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
