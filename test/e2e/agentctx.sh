#!/usr/bin/env bash
# Agent context: what podbox writes in the agent's home at every start, the
# read-only policy view at /run/podbox, and that the project stays untouched.
. "$(dirname "$0")/lib.sh"

NAME=e2e-ctx
NAME2=e2e-ctx2
SH=e2e-ctxsh
PROJ=$(mktemp -d)
PROJ2=$(mktemp -d)
PROJ3=$(mktemp -d)

cleanup() {
	pb rm "$NAME" >/dev/null 2>&1
	pb rm "$NAME2" >/dev/null 2>&1
	pb rm "$SH" --volumes >/dev/null 2>&1
	podman volume rm -f podbox-home-claude >/dev/null 2>&1
	rm -rf "$PROJ" "$PROJ2" "$PROJ3" "$PODBOX_HOME"
}
trap cleanup EXIT

CLAUDE_MD=/home/agent/.claude/CLAUDE.md
SKILL=/home/agent/.claude/skills/podbox-sandbox/SKILL.md
POLICY=/run/podbox/policy.json

blocks() { sx "$1" "grep -c 'podbox:begin' $CLAUDE_MD"; }

pb image build proxy claude shell >/dev/null || { echo "image build failed"; exit 1; }
git -C "$PROJ" init -q && git -C "$PROJ" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
git -C "$PROJ" status --porcelain >"$PROJ.before"

echo "== first start"
pb create claude "$PROJ" --name "$NAME" >/dev/null 2>&1 && pb start "$NAME" >/dev/null 2>&1
running "$NAME"
sx "$NAME" "printf 'user text\n' > $CLAUDE_MD"
check "restart keeps user text" restart "$NAME"
check "one podbox block after the restart" test "$(blocks "$NAME")" = 1
check "user text intact" test "$(sx "$NAME" "head -1 $CLAUDE_MD")" = "user text"
check "skill present" sx "$NAME" "grep -q 'name: podbox-sandbox' $SKILL"
check "files owned by uid 1000" test "$(sx "$NAME" "stat -c %u $CLAUDE_MD $SKILL" | sort -u)" = 1000
check "PODBOX_SANDBOX is the sandbox name" test "$(sx "$NAME" 'echo $PODBOX_SANDBOX')" = "$NAME"
check "context names no sandbox" sx "$NAME" "! grep -q '$NAME' $CLAUDE_MD $SKILL"

echo "== policy view"
check "/run/podbox is a read-only mount" sx "$NAME" "grep -Eq ' /run/podbox ro[, ]' /proc/self/mountinfo"
check "policy.json names the sandbox and the workspace" test "$(sx "$NAME" "cat $POLICY" | jq -r '.sandbox + "|" + .workspace')" = "$NAME|$PROJ"
check "view has no layer or warning" sx "$NAME" "! grep -Eq 'layer|warnings|allow_private' $POLICY"
check "example.org is not allowed yet" test "$(sx "$NAME" "cat $POLICY" | jq '[.rules[] | select(.host == "example.org")] | length')" = 0
pb policy allow "$NAME" example.org >/dev/null 2>&1
check "policy allow shows without a restart" eventually 10 sh -c "$PODBOX exec $NAME -- cat $POLICY </dev/null | jq -e '.rules[] | select(.host == \"example.org\" and .allow)'"
running "$NAME"
denied "cannot write policy.json" "$NAME" "echo x > $POLICY"
denied "cannot replace policy.json" "$NAME" "echo x > /run/podbox/new && mv /run/podbox/new $POLICY"
denied "cannot delete policy.json" "$NAME" "rm -f $POLICY"

echo "== second sandbox of the same agent"
pb create claude "$PROJ2" --name "$NAME2" >/dev/null 2>&1 && pb start "$NAME2" >/dev/null 2>&1
check "shared CLAUDE.md still has one block" test "$(blocks "$NAME2")" = 1
check "shared CLAUDE.md names neither sandbox" sx "$NAME2" "! grep -q 'e2e-ctx' $CLAUDE_MD"
check "second sandbox reads its own policy" test "$(sx "$NAME2" "cat $POLICY" | jq -r .sandbox)" = "$NAME2"
pb rm "$NAME2" >/dev/null 2>&1

echo "== restarts and tampering"
restart "$NAME" && restart "$NAME"
check "still one block after more starts" test "$(blocks "$NAME")" = 1
sx "$NAME" "echo tampered > $SKILL; printf 'user text\n<!-- podbox:begin -->\ntampered\n<!-- podbox:end -->\nuser line\n' > $CLAUDE_MD"
check "restart" restart "$NAME"
check "skill back to podbox content" sx "$NAME" "grep -q 'name: podbox-sandbox' $SKILL && ! grep -q tampered $SKILL"
check "block back to podbox content" sx "$NAME" "! grep -q tampered $CLAUDE_MD"
check "user text kept" sx "$NAME" "grep -q 'user text' $CLAUDE_MD && grep -q 'user line' $CLAUDE_MD"
check "still one block" test "$(blocks "$NAME")" = 1

echo "== project untouched"
git -C "$PROJ" status --porcelain >"$PROJ.after"
check "git status unchanged by the feature" cmp -s "$PROJ.before" "$PROJ.after"
check "no podbox file in the project" test -z "$(find "$PROJ" -path "$PROJ/.git" -prune -o -type f -print)"
rm -f "$PROJ.before" "$PROJ.after"

echo "== symlinked CLAUDE.md"
sx "$NAME" "rm -f $CLAUDE_MD; printf 'linked\n' > /home/agent/real-claude.md; chmod 600 /home/agent/real-claude.md; ln -s /home/agent/real-claude.md $CLAUDE_MD"
check "restart" restart "$NAME"
check "CLAUDE.md is still a symbolic link" sx "$NAME" "test -L $CLAUDE_MD"
check "the block went to the link target, text and mode kept" sx "$NAME" "grep -q linked /home/agent/real-claude.md && grep -q 'podbox:begin' /home/agent/real-claude.md && test \"\$(stat -c %a /home/agent/real-claude.md)\" = 600"
sx "$NAME" "rm -f $CLAUDE_MD /home/agent/real-claude.md"

echo "== FIFO CLAUDE.md and directory skill"
sx "$NAME" "rm -f $CLAUDE_MD $SKILL; mkfifo $CLAUDE_MD; mkdir $SKILL"
pb stop "$NAME" >/dev/null 2>&1
err=$(timeout 120 "$PODBOX" start "$NAME" 2>&1 >/dev/null)
check "start returns with a FIFO CLAUDE.md" test $? -eq 0
check "start warns about CLAUDE.md" grep -q 'CLAUDE.md' <<<"$err"
check "start warns about the skill" grep -q 'SKILL.md' <<<"$err"
check "the FIFO is untouched" sx "$NAME" "test -p $CLAUDE_MD"
check "the skill directory is untouched and empty" sx "$NAME" "test -d $SKILL && test -z \"\$(ls -A $SKILL)\""
sx "$NAME" "rm -rf $CLAUDE_MD $SKILL"

echo "== unmergeable CLAUDE.md"
sx "$NAME" "printf 'x\n<!-- podbox:begin -->\nhalf\n' > $CLAUDE_MD"
sx "$NAME" "cat $CLAUDE_MD" >"$PROJ3/before"
pb stop "$NAME" >/dev/null 2>&1
err=$(pb start "$NAME" 2>&1 >/dev/null)
check "start succeeds" test "$(pb ls -o json | jq -r --arg n "$NAME" '.[] | select(.name == $n) | .state')" = running
check "start warns" grep -q 'agent context' <<<"$err"
sx "$NAME" "cat $CLAUDE_MD" >"$PROJ3/after"
check "file untouched" cmp -s "$PROJ3/before" "$PROJ3/after"

echo "== shell agent"
pb create shell "$PROJ3" --name "$SH" >/dev/null 2>&1 && pb start "$SH" >/dev/null 2>&1
running "$SH"
check "no .claude and no .kiro in the shell home" sx "$SH" "test ! -e ~/.claude && test ! -e ~/.kiro"

echo "== teardown"
check "rm --volumes" pb rm "$NAME" --volumes
pb rm "$SH" --volumes >/dev/null 2>&1
check "nothing left behind" no_objects_left

summary
