#!/usr/bin/env bash
# podbox export: the agent's commits come back as a new host branch (a
# verified bundle) or a patch file. The host repository must stay
# byte-identical but for the new branch, no host hook may fire, and a hostile
# clone (rewritten history, replace refs, planted config, hostile names,
# corrupt objects) must never reach the host.
. "$(dirname "$0")/lib.sh"

NAME=e2e-ce
HOSTILE=e2e-cehostile
SHARED=e2e-ceshared
REPO=$(mktemp -d)
WORK=$(mktemp -d)   # current directory of the exports, patches land here
XTMP=$(mktemp -d)   # TMPDIR of every export: nothing may be left in it
MARK=$(mktemp -u)   # written by any host hook that fires
OUT=$(mktemp)
ERR=$(mktemp)

cleanup() {
	for n in "$NAME" "$HOSTILE" "$SHARED"; do pb rm "$n" --volumes >/dev/null 2>&1; done
	rm -rf "$REPO" "$WORK" "$XTMP" "$MARK" "$OUT" "$ERR" "$PODBOX_HOME"
}
trap cleanup EXIT

G() { git -C "$REPO" -c user.name=host -c user.email=host@example.org -c commit.gpgsign=false "$@"; }
json() { jq -r "$1" "$OUT"; }
# px: podbox export from the work directory with a private TMPDIR.
px() { (cd "$WORK" && TMPDIR=$XTMP "$PODBOX" export "$@"); }
# hostprint: the working tree and everything of .git that an export must not
# change: HEAD, index, config, hooks, info, and every ref but the ones named in $SKIP.
SKIP='refs/heads/(podbox/|review/|masked|warned|replaced|planted|rewritten|big|corrupt|dotgit|merged|reverted|sigterm)'
hostprint() {
	(
		cd "$REPO" || exit 1
		find . -path ./.git -prune -o -type f -print | sort | xargs sha256sum
		sha256sum .git/HEAD .git/index .git/config .git/hooks/* .git/info/* 2>/dev/null
		G for-each-ref --format='%(refname) %(objectname)' | grep -Ev "$SKIP"
		G status --porcelain
	) | sha256sum
}
agent() { sx "$1" "export GIT_AUTHOR_NAME=a GIT_AUTHOR_EMAIL=a@example.org GIT_COMMITTER_NAME=a GIT_COMMITTER_EMAIL=a@example.org
$2"; }
leftovers() { [ -z "$(ls -A "$XTMP")" ] && [ -z "$(G for-each-ref refs/podbox)" ] && [ ! -e "$REPO/.git/FETCH_HEAD" ] && [ ! -e "$MARK" ]; }
nobranch() { [ -z "$(G for-each-ref "refs/heads/$1")" ]; }

pb image build proxy shell >/dev/null || { echo "image build failed"; exit 1; }

echo "== setup"
G init -q -b main
printf 'SECRET=real\n' >"$REPO/.env"
printf '.sandboxignore\n' >"$REPO/.gitignore"
printf 'one\ntwo\n' >"$REPO/a.txt"
printf 'x\n' >"$REPO/del.txt"
G add . && G commit -q -m base
BASE=$(G rev-parse HEAD)
mkdir -p "$REPO/.git/hooks"
for h in reference-transaction post-checkout post-merge post-update update pre-auto-gc post-commit post-fetch; do
	printf '#!/bin/sh\necho %s >> %s\n' "$h" "$MARK" >"$REPO/.git/hooks/$h"
	chmod +x "$REPO/.git/hooks/$h"
done
G config podbox.canary keep
printf 'host edit\n' >>"$REPO/a.txt"
printf 'host untracked\n' >"$REPO/untracked-host.txt"
G status --porcelain >/dev/null
BEFORE=$(hostprint)
HOSTBRANCH=$(G symbolic-ref HEAD)
check "create clone sandbox" q pb create shell "$REPO" --name "$NAME" --clone
check "start" q pb start "$NAME"
running "$NAME"
check "the agent sees .env empty (masked)" test "$(sx "$NAME" 'wc -c < .env')" -eq 0

echo "== the agent commits twice and leaves work undone"
agent "$NAME" 'echo three >> a.txt && git add a.txt && git commit -qm first
mkdir sub && echo c > sub/c.txt && git add sub && git commit -qm second
echo dirty >> a.txt; echo u1 > untracked1.txt'
AGENT_HEAD=$(sx "$NAME" 'git rev-parse HEAD')

echo "== export --as branch"
px "$NAME" --as branch >"$OUT" 2>"$ERR"; rc=$?
check "exit 0" test $rc -eq 0
check "the branch exists at the agent's tip" test "$(G rev-parse "podbox/$NAME/main")" = "$AGENT_HEAD"
check "it holds exactly the agent's two commits" test "$(G rev-list --count "$BASE..podbox/$NAME/main")" = 2
check "the output names the branch" grep -q "created branch podbox/$NAME/main with 2 commit" "$OUT"
check "the output lists the commits" grep -q ' second$' "$OUT"
check "uncommitted and untracked work is reported not exported" grep -q 'not exported: 1 uncommitted, 1 untracked' "$OUT"
check "the file names are listed" grep -q 'untracked1.txt' "$OUT"
check "nothing was merged or checked out" grep -q 'nothing was checked out or merged' "$OUT"
check "host working tree, index, HEAD, config, hooks, other refs unchanged (hash)" test "$(hostprint)" = "$BEFORE"
check "the host is still on its branch" test "$(G symbolic-ref HEAD)" = "$HOSTBRANCH"
check "the host's uncommitted edit is still there" grep -q 'host edit' "$REPO/a.txt"
check "no host hook fired, no FETCH_HEAD, no temporary ref, no temporary file" leftovers
check "the branch's tree has the agent's file" test "$(G show "podbox/$NAME/main:sub/c.txt")" = c

echo "== JSON"
px "$NAME" --as branch --branch-name review/x -o json >"$OUT" 2>/dev/null; rc=$?
check "exit 0" test $rc -eq 0
check "JSON: as, branch, base, head, commits" test "$(json '[.as, .branch, .base, .head, .commits, .exported, .host_verified] | join(" ")')" = "branch review/x $BASE $AGENT_HEAD 2 true true"
check "JSON: not_exported counts and names" test "$(json '[.not_exported.uncommitted, .not_exported.untracked, .not_exported.untracked_files[0]] | join(" ")')" = "1 1 untracked1.txt"
check "JSON: files, commit list, empty warnings and masked" test "$(json '[.files_changed, (.commit_list | length), (.warnings | length), (.masked_touched | length)] | join(" ")')" = "2 2 0 0"
check "JSON schema" test "$(json .schema)" = 1
check "the named branch exists" test "$(G rev-parse review/x)" = "$AGENT_HEAD"
check "host unchanged but for the branches" test "$(hostprint)" = "$BEFORE"

echo "== a branch is never overwritten"
TIP=$(G rev-parse "podbox/$NAME/main")
px "$NAME" --as branch >"$OUT" 2>"$ERR"; rc=$?
check "same name again: exit 1" test $rc -eq 1
check "the message suggests --branch-name" grep -q -- '--branch-name' "$ERR"
check "the existing branch is untouched" test "$(G rev-parse "podbox/$NAME/main")" = "$TIP"
px "$NAME" --as branch --branch-name main >"$OUT" 2>"$ERR"; rc=$?
check "the host's current branch name: exit 1" test $rc -eq 1
check "the current branch is untouched" test "$(G rev-parse main)" = "$BASE"
check "the checkout is untouched" test "$(hostprint)" = "$BEFORE"
check "nothing left behind" leftovers

echo "== export --as patch"
px "$NAME" --as patch >"$OUT" 2>"$ERR"; rc=$?
check "exit 0" test $rc -eq 0
check "default file is <sandbox>.patch in the current directory" test -s "$WORK/$NAME.patch"
check "the output names it and the not exported work" grep -q "wrote $WORK/$NAME.patch with 2 commit" "$OUT" && grep -q 'not exported: 1 uncommitted, 1 untracked' "$OUT"
FRESH=$(mktemp -d)
git clone -q "$REPO" "$FRESH/c" && git -C "$FRESH/c" checkout -q --detach "$BASE"
check "the patch applies with git apply on a fresh checkout of the base" git -C "$FRESH/c" apply --index "$WORK/$NAME.patch"
check "the result is the agent's committed tree" test "$(git -C "$FRESH/c" write-tree)" = "$(sx "$NAME" 'git rev-parse HEAD^{tree}')"
rm -rf "$FRESH"
SUM=$(sha256sum "$WORK/$NAME.patch")
px "$NAME" --as patch >"$OUT" 2>"$ERR"; rc=$?
check "an existing patch file is refused: exit 1" test $rc -eq 1
check "the message suggests --out" grep -q -- '--out' "$ERR"
check "the file is byte-identical" test "$(sha256sum "$WORK/$NAME.patch")" = "$SUM"
px "$NAME" --as patch --out other.patch -o json >"$OUT" 2>/dev/null; rc=$?
check "--out: exit 0" test $rc -eq 0
check "JSON reports the file, not a branch, not host-verified" test "$(json '[.file, has("branch"), .commits, .host_verified, .not_exported.untracked] | join(" ")')" = "$WORK/other.patch false 2 false 1"
ln -s "$WORK/elsewhere" "$WORK/link.patch"
px "$NAME" --as patch --out link.patch >/dev/null 2>&1
check "a symlink at the target is not followed" test $? -eq 1 -a ! -e "$WORK/elsewhere"
check "host unchanged" test "$(hostprint)" = "$BEFORE"
check "nothing left behind" leftovers

echo "== a masked path touched by the commits"
agent "$NAME" 'E=$(git hash-object -w /dev/null); git update-index --cacheinfo 100644,$E,.env; git commit -qm "empty the secret"'
check "the agent committed the emptied secret" test "$(sx "$NAME" 'git show HEAD:.env | wc -c')" -eq 0
px "$NAME" --as branch --branch-name masked >"$OUT" 2>"$ERR"; rc=$?
check "branch: exit 1" test $rc -eq 1
check "the masked path is named" grep -q '\.env' "$ERR"
check "the message names --include-masked" grep -q -- '--include-masked' "$ERR"
check "no branch was created" nobranch masked
px "$NAME" --as patch --out masked.patch >/dev/null 2>"$ERR"
check "patch: exit 1, no file" test $? -eq 1 -a ! -e "$WORK/masked.patch"
px "$NAME" --as branch --branch-name masked -o json >"$OUT" 2>/dev/null
check "JSON error is generic" test "$(json .error.kind)" != ""
px "$NAME" --as branch --branch-name masked --include-masked -o json >"$OUT" 2>/dev/null; rc=$?
check "--include-masked: exit 0" test $rc -eq 0
check "JSON lists the masked path (and its base64)" test "$(json '[.masked_touched[], (.masked_touched_b64[0] | @base64d)] | join(" ")')" = ".env .env"
check "the real secret is intact on the host" grep -q 'SECRET=real' "$REPO/.env"
check "host unchanged" test "$(hostprint)" = "$BEFORE"
check "nothing left behind" leftovers

echo "== warnings for what can act on checkout"
agent "$NAME" 'ln -s /etc/passwd evil-link; printf "*.x filter=evil\n" > .gitattributes; mkdir -p .husky && echo "echo hi" > .husky/pre-commit
printf "echo ok\n\000rm -rf ~\n" > nul.sh; printf "x\n" > "$(printf "caf\303\251.txt")"; printf "1\n" > Case.TXT; printf "2\n" > case.txt
git add -f -- evil-link .gitattributes .husky nul.sh Case.TXT case.txt caf*.txt; git commit -qm danger'
px "$NAME" --as branch --branch-name warned --include-masked -o json >"$OUT" 2>/dev/null; rc=$?
check "exit 0" test $rc -eq 0
check "JSON warns: symlink, attributes, hook-like, nul, case, non-ascii" test "$(json '[.warnings[].kind] | sort | join(" ")')" = "attributes case-collision hook-like non-ascii nul symlink"
px "$NAME" --as patch --out warned.patch --include-masked >"$OUT" 2>/dev/null
check "text output warns loudly and points to podbox diff and git diff" grep -q '^WARNING: the commits contain things' "$OUT" && grep -q 'podbox diff and git diff' "$OUT"
check "the warning names the hook-like file" grep -q 'hook-like (1)' "$OUT" && grep -q '\.husky/pre-commit' "$OUT"
check "the patch is not sanitized: the NUL file is a binary patch for git apply" grep -q '^GIT binary patch' "$WORK/warned.patch"
check "the symlink is in the patch for git apply" grep -q '^new file mode 120000' "$WORK/warned.patch"
check "host unchanged" test "$(hostprint)" = "$BEFORE"

echo "== refusals"
px "$NAME" >/dev/null 2>"$ERR"
check "no --as: exit 2" test $? -eq 2
px "$NAME" --as zip >/dev/null 2>&1
check "invalid --as: exit 2" test $? -eq 2
px "$NAME" --as branch --out x >/dev/null 2>&1
check "--out with branch: exit 2" test $? -eq 2
px "$NAME" --as patch --branch-name x >/dev/null 2>&1
check "--branch-name with patch: exit 2" test $? -eq 2
for bad in -x a..b 'a b' x.lock '@{-1}' HEAD "$(printf 'a\001b')" "$(printf 'a%.0s' $(seq 1 300))/\$x/../.."; do
	px "$NAME" --as branch --branch-name "$bad" >/dev/null 2>&1; rc=$?
	[ $rc -eq 2 ] || [ $rc -eq 1 ] && pass "branch name refused (exit $rc): $(printf %s "$bad" | cut -c1-20 | tr -c '[:print:]' '?')" || fail "branch name accepted: $bad"
done
check "no bad branch was created" test "$(G for-each-ref --format='%(refname)' refs/heads | grep -c .)" -le 5
pb create shell "$REPO" --name "$SHARED" >/dev/null 2>&1
px "$SHARED" --as branch >/dev/null 2>"$ERR"
check "shared sandbox: exit 2" test $? -eq 2
check "shared sandbox: the message names --clone" grep -q -- '--clone' "$ERR"
pb rm "$SHARED" >/dev/null 2>&1
px e2e-cenone --as branch >/dev/null 2>&1
check "unknown sandbox: exit 4" test $? -eq 4
pb stop "$NAME" >/dev/null
px "$NAME" --as branch --branch-name stopped >/dev/null 2>"$ERR"
check "stopped sandbox: exit 1" test $? -eq 1
check "stopped sandbox: the message names podbox start" grep -q "podbox start $NAME" "$ERR"
check "start again" q pb start "$NAME"
check "nothing left behind" leftovers

echo "== nothing to export"
check "create a second clone sandbox" q pb create shell "$REPO" --name "$HOSTILE" --clone
check "start it" q pb start "$HOSTILE"
running "$HOSTILE"
sx "$HOSTILE" 'echo dirty >> a.txt; echo u > u.txt'
px "$HOSTILE" --as branch >"$OUT" 2>"$ERR"; rc=$?
check "exit 0" test $rc -eq 0
check "says nothing to export, with the uncommitted counts" grep -q 'nothing to export' "$OUT" && grep -q 'not exported: 1 uncommitted, 1 untracked' "$OUT"
check "no branch" nobranch "podbox/$HOSTILE/main"
px "$HOSTILE" --as patch -o json >"$OUT" 2>/dev/null; rc=$?
check "patch: exit 0, exported false, no file" test $rc -eq 0 -a "$(json .exported)" = false -a ! -e "$WORK/$HOSTILE.patch"
sx "$HOSTILE" 'git checkout -q -- a.txt; rm u.txt'
check "host unchanged" test "$(hostprint)" = "$BEFORE"

echo "== a replace ref cannot change what is exported"
agent "$HOSTILE" 'echo real > r.txt && git add r.txt && git commit -qm real'
REAL=$(sx "$HOSTILE" 'git rev-parse HEAD')
agent "$HOSTILE" 'echo evil > evil.txt; git add evil.txt; T=$(git write-tree); F=$(git commit-tree $T -p HEAD~1 -m fake); git reset -q; rm evil.txt; git replace HEAD $F'
check "the replace ref is in effect for the agent's own git (the attack is real)" test "$(sx "$HOSTILE" 'git show HEAD:evil.txt 2>/dev/null')" = evil
px "$HOSTILE" --as branch --branch-name replaced -o json >"$OUT" 2>"$ERR"; rc=$?
check "exit 0" test $rc -eq 0
check "the exported tip is the real commit" test "$(G rev-parse replaced)" = "$REAL"
check "the evil file is not on the branch" test -z "$(G ls-tree -r --name-only replaced | grep evil)"
px "$HOSTILE" --as patch --out replaced.patch >/dev/null 2>&1
check "the patch has the real content" grep -q '^+real$' "$WORK/replaced.patch"
check "the patch has not the fake's content" test -z "$(grep evil "$WORK/replaced.patch")"
sx "$HOSTILE" 'git for-each-ref --format="%(refname)" refs/replace | xargs -r -n1 git update-ref -d'
check "host unchanged but for the branches" test "$(hostprint)" = "$BEFORE"

echo "== a planted config, attributes and hooks never run"
sx "$HOSTILE" 'printf "[filter \"x\"]\n\tclean = touch /tmp/pwn-filter\n\tsmudge = touch /tmp/pwn-filter\n\tprocess = touch /tmp/pwn-filter\n\trequired = true\n[core]\n\tfsmonitor = touch /tmp/pwn-fsmonitor\n\tpager = touch /tmp/pwn-pager\n\thooksPath = /tmp/pwnhooks\n\tsshCommand = touch /tmp/pwn-ssh\n[diff]\n\texternal = touch /tmp/pwn-diff\n[alias]\n\tbundle = !touch /tmp/pwn-alias\n[uploadpack]\n\tpackObjectsHook = touch /tmp/pwn-hook\n[format]\n\theaders = X-Evil: yes\n\tsignature = EVIL\n" >> .git/config
printf "*.txt filter=x\n" > .gitattributes; printf "*.txt diff=x\n" > .git/info/attributes
mkdir -p /tmp/pwnhooks .git/hooks; for h in reference-transaction post-index-change pre-auto-gc post-checkout; do printf "#!/bin/sh\ntouch /tmp/pwn-$h\n" > /tmp/pwnhooks/$h; cp /tmp/pwnhooks/$h .git/hooks/$h; chmod +x /tmp/pwnhooks/$h .git/hooks/$h; done'
px "$HOSTILE" --as branch --branch-name planted >"$OUT" 2>"$ERR"; rc=$?
check "branch export still works: exit 0" test $rc -eq 0
px "$HOSTILE" --as patch --out planted.patch >/dev/null 2>&1
check "patch export still works" test $? -eq 0 -a -s "$WORK/planted.patch"
check "no program named by the clone's configuration, attributes or hooks ran" sx "$HOSTILE" '! ls /tmp/pwn-* >/dev/null 2>&1'
check "the exported bytes are the committed ones" test "$(G show planted:r.txt)" = real
check "the patch has no injected header or signature" test -z "$(grep -E 'X-Evil|EVIL' "$WORK/planted.patch")"
sx "$HOSTILE" 'git config --remove-section filter.x; git config --remove-section core; git config --remove-section diff; git config --remove-section alias; git config --remove-section uploadpack; git config --remove-section format; rm -f .gitattributes .git/info/attributes .git/hooks/*; git config core.repositoryformatversion 0; git config core.bare false'
check "host unchanged but for the branches" test "$(hostprint)" = "$BEFORE"
check "nothing left behind" leftovers

echo "== the patch is one combined diff of what was reviewed"
GOOD=$(sx "$HOSTILE" 'git rev-parse HEAD')
agent "$HOSTILE" "printf 'hello\n\ndiff --git a/.env b/.env\n--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-SECRET=real\n+SECRET=pwned\n' > /tmp/msg; echo smuggler >> r.txt; git add r.txt; git commit -q -F /tmp/msg"
px "$HOSTILE" --as patch --out msg.patch >"$OUT" 2>"$ERR"; rc=$?
check "a commit message that holds a diff of the masked file: exit 0 (the file is not in the reviewed changes)" test $rc -eq 0
check "the patch holds no commit message" test -z "$(grep -E 'pwned|^hello' "$WORK/msg.patch")"
FRESH=$(mktemp -d)
git clone -q "$REPO" "$FRESH/c" && git -C "$FRESH/c" checkout -q --detach "$BASE"
check "git apply on the base" git -C "$FRESH/c" apply --index "$WORK/msg.patch"
check "the real secret is not changed by it" grep -q 'SECRET=real' "$FRESH/c/.env"
check "the result is the agent's tree" test "$(git -C "$FRESH/c" write-tree)" = "$(sx "$HOSTILE" 'git rev-parse HEAD^{tree}')"
rm -rf "$FRESH"

echo "== a merge commit, and its own changes"
# git merge itself cannot run next to a masked file (it is a device), so the merge is built with plumbing
agent "$HOSTILE" 'M=$(git rev-parse HEAD); trap "rm -f /tmp/ix1 /tmp/ix2 /tmp/ix3" EXIT
echo s > s.txt; echo m > m.txt
GIT_INDEX_FILE=/tmp/ix1 git read-tree $M; GIT_INDEX_FILE=/tmp/ix1 git update-index --add s.txt; C1=$(GIT_INDEX_FILE=/tmp/ix1 git write-tree | xargs -I{} git commit-tree {} -p $M -m side)
GIT_INDEX_FILE=/tmp/ix2 git read-tree $M; GIT_INDEX_FILE=/tmp/ix2 git update-index --add m.txt; C2=$(GIT_INDEX_FILE=/tmp/ix2 git write-tree | xargs -I{} git commit-tree {} -p $M -m main2)
echo evil >> r.txt
GIT_INDEX_FILE=/tmp/ix3 git read-tree $C2; GIT_INDEX_FILE=/tmp/ix3 git update-index --add s.txt r.txt; C3=$(GIT_INDEX_FILE=/tmp/ix3 git write-tree | xargs -I{} git commit-tree {} -p $C2 -p $C1 -m merge)
git update-ref refs/heads/main $C3; git read-tree HEAD'
check "the agent made a merge commit" test "$(sx "$HOSTILE" 'git rev-list --merges --count HEAD')" -ge 1
px "$HOSTILE" --as patch --out merge.patch >"$OUT" 2>"$ERR"; rc=$?
check "exit 0" test $rc -eq 0
FRESH=$(mktemp -d)
git clone -q "$REPO" "$FRESH/c" && git -C "$FRESH/c" checkout -q --detach "$BASE"
check "git apply on the base" git -C "$FRESH/c" apply --index "$WORK/merge.patch"
check "the merge's own change is in the patch: same tree as the agent's" test "$(git -C "$FRESH/c" write-tree)" = "$(sx "$HOSTILE" 'git rev-parse HEAD^{tree}')"
rm -rf "$FRESH"
px "$HOSTILE" --as branch --branch-name merged >"$OUT" 2>"$ERR"
check "branch export of the merge: same tree" test "$(G rev-parse merged^{tree})" = "$(sx "$HOSTILE" 'git rev-parse HEAD^{tree}')"

echo "== a masked path touched and put back in history"
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD; git read-tree HEAD; rm -f s.txt m.txt; git checkout -q -- r.txt 2>/dev/null
E=\$(git hash-object -w /dev/null); git update-index --cacheinfo 100644,\$E,.env; git commit -qm 'empty the secret'
git update-index --cacheinfo 100644,\$(git rev-parse $BASE:.env),.env; git commit -qm 'put it back'"
check "the net change does not touch .env" test -z "$(sx "$HOSTILE" "git diff --name-only $BASE HEAD | grep '^.env'")"
px "$HOSTILE" --as branch --branch-name reverted >"$OUT" 2>"$ERR"; rc=$?
check "refused although the net change is clean: exit 1" test $rc -eq 1
check "the masked path is named" grep -q '\.env' "$ERR"
check "no branch" nobranch reverted
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD; git read-tree HEAD"

echo "== hostile history"
GOOD=$(sx "$HOSTILE" 'git rev-parse HEAD')
agent "$HOSTILE" 'E=$(git mktree </dev/null); R=$(git commit-tree $E -m unrelated); git update-ref refs/heads/main $R'
px "$HOSTILE" --as branch --branch-name rewritten >"$OUT" 2>"$ERR"; rc=$?
check "an unrelated root commit: exit 1" test $rc -eq 1
check "the refusal says history does not descend from the base" grep -q 'does not descend from the base' "$ERR"
check "no branch" nobranch rewritten
px "$HOSTILE" --as patch --out rewritten.patch >/dev/null 2>&1
check "patch: exit 1, no file" test $? -eq 1 -a ! -e "$WORK/rewritten.patch"
# a graft makes the agent's own git claim the root descends from the base; the export ignores replace refs
agent "$HOSTILE" 'git replace --graft HEAD '"$BASE"
px "$HOSTILE" --as branch --branch-name rewritten >"$OUT" 2>"$ERR"; rc=$?
check "with a graft in the clone: still exit 1" test $rc -eq 1
check "still no branch" nobranch rewritten
sx "$HOSTILE" 'git for-each-ref --format="%(refname)" refs/replace | xargs -r -n1 git update-ref -d'
agent "$HOSTILE" "git update-ref refs/heads/main $BASE"
px "$HOSTILE" --as branch --branch-name rewritten >"$OUT" 2>"$ERR"
check "history reset to the base: nothing to export, exit 0" test $? -eq 0
check "and nothing created" nobranch rewritten
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD"
check "nothing left behind" leftovers

echo "== hostile branch names"
agent "$HOSTILE" 'git update-ref refs/heads/-evil HEAD && git symbolic-ref HEAD refs/heads/-evil'
px "$HOSTILE" --as branch -o json >"$OUT" 2>"$ERR"; rc=$?
check "a leading dash in the agent's branch: exit 0, safe name" test $rc -eq 0 -a "$(json .branch)" = "podbox/$HOSTILE/-evil"
agent "$HOSTILE" 'L=$(printf "l%.0s" $(seq 1 200)); git update-ref "refs/heads/$L/x" HEAD && git symbolic-ref HEAD "refs/heads/$L/x"'
px "$HOSTILE" --as branch -o json >"$OUT" 2>"$ERR"; rc=$?
check "a very long agent branch name: handled, name bounded" test $rc -eq 0 -a "$(json '.branch | length')" -le 130 -a "$(json .branch_sanitized)" = true
agent "$HOSTILE" "git symbolic-ref HEAD refs/heads/main"
sx "$HOSTILE" 'printf "ref: refs/heads/a b\033[31mc..d\n" > .git/HEAD'
px "$HOSTILE" --as branch >"$OUT" 2>"$ERR"; rc=$?
check "a HEAD with spaces, ESC and '..': refused cleanly (exit 1)" test $rc -eq 1
check "the error shows no raw control character" test "$(LC_ALL=C tr -d '\n\t' <"$ERR" | LC_ALL=C grep -c '[^ -~]')" = 0
sx "$HOSTILE" 'echo "ref: refs/heads/main" > .git/HEAD'
agent "$HOSTILE" 'git checkout -q --detach'
px "$HOSTILE" --as branch >"$OUT" 2>"$ERR"; rc=$?
check "a detached HEAD: exit 1" test $rc -eq 1
check "a detached HEAD: the hint names a branch" grep -q 'branch' "$ERR"
agent "$HOSTILE" 'git checkout -q main'
check "nothing left behind" leftovers

echo "== a bundle the host refuses"
agent "$HOSTILE" 'B=$(echo x | git hash-object -w --stdin)
T=$(printf "100644 blob %s\t.git\n" $B | git mktree); C=$(git commit-tree $T -p HEAD -m dotgit); git update-ref refs/heads/main $C'
px "$HOSTILE" --as branch --branch-name dotgit >"$OUT" 2>"$ERR"; rc=$?
check "a tree entry named .git: exit 1 (object checks on)" test $rc -eq 1
check "the refusal comes from the bundle check" grep -q 'bundle rejected' "$ERR"
check "no branch, no temporary ref" nobranch dotgit
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD"
agent "$HOSTILE" 'echo more > m.txt; git add m.txt; git commit -qm more; o=.git/objects/$(git rev-parse HEAD | cut -c1-2)/$(git rev-parse HEAD | cut -c3-); chmod u+w $o; printf junk > $o'
px "$HOSTILE" --as branch --branch-name corrupt >"$OUT" 2>"$ERR"; rc=$?
check "a corrupt object store: exit 1" test $rc -eq 1
check "no branch, no leftover" nobranch corrupt
px "$HOSTILE" --as patch --out corrupt.patch >/dev/null 2>&1
check "patch from a corrupt store: exit 1, no file" test $? -eq 1 -a ! -e "$WORK/corrupt.patch"
check "nothing left behind" leftovers

echo "== a bundle over the cap"
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD; git read-tree HEAD; head -c 300000 /dev/urandom | od -An -tx1 > big.txt; git add big.txt; git commit -qm big"
PODBOX_EXPORT_MAX_BYTES=20000 px "$HOSTILE" --as branch --branch-name big >"$OUT" 2>"$ERR"; rc=$?
check "oversized bundle: exit 1" test $rc -eq 1
check "the message says too large" grep -q 'too large' "$ERR"
check "no branch" nobranch big
PODBOX_EXPORT_MAX_BYTES=20000 px "$HOSTILE" --as patch --out big.patch >/dev/null 2>&1
check "oversized patch: exit 1, no partial file" test $? -eq 1 -a ! -e "$WORK/big.patch"
check "host unchanged" test "$(hostprint)" = "$BEFORE"
check "nothing left behind" leftovers

echo "== an interrupt leaves nothing behind"
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD; git read-tree HEAD; head -c 150000000 /dev/urandom > huge.bin; git add huge.bin; git commit -qm huge"
( cd "$WORK" && TMPDIR=$XTMP exec "$PODBOX" export "$HOSTILE" --as branch --branch-name sigterm >/dev/null 2>&1 ) &
PID=$!
sleep 2
kill -TERM $PID 2>/dev/null
wait $PID; rc=$?
if [ $rc -eq 0 ]; then
	pass "the export finished before the signal (too fast to interrupt here)"
	G update-ref -d refs/heads/sigterm; rm -f "$MARK"
else
	check "interrupted: non-zero exit" test $rc -ne 0
	check "no branch" nobranch sigterm
	check "no temporary file or ref, no FETCH_HEAD" leftovers
fi
agent "$HOSTILE" "git update-ref refs/heads/main $GOOD; git read-tree HEAD; rm -f huge.bin"

echo "== teardown"
check "rm keeps the clone" q pb rm "$NAME"
check "rm hostile --volumes" q pb rm "$HOSTILE" --volumes
check "rm --volumes finds the orphan clone" q pb rm "$NAME" --volumes
check "no podman object left" no_objects_left
check "no clone directory left" test -z "$(ls -A "$PODBOX_HOME/state/clones" 2>/dev/null)"
check "no temporary directory left by the exports" test -z "$(ls -A "$XTMP")"
check "the host's secret and branch are intact" grep -q 'SECRET=real' "$REPO/.env" && test "$(G rev-parse main)" = "$BASE"

summary
