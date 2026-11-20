package agentctx

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestFilesClaude(t *testing.T) {
	fs := Files("claude")
	if len(fs) != 2 || fs[0].Path != ".claude/CLAUDE.md" || fs[0].Mode != Block ||
		fs[1].Path != ".claude/skills/podbox-sandbox/SKILL.md" || fs[1].Mode != Owned {
		t.Fatalf("%+v", fs)
	}
	for _, f := range fs {
		for _, want := range []string{"$PODBOX_SANDBOX", "/run/podbox/policy.json"} {
			if !strings.Contains(f.Content, want) {
				t.Errorf("%s lacks %q", f.Path, want)
			}
		}
	}
	if !strings.Contains(fs[0].Content, "podbox policy allow <sandbox> <host>") ||
		!strings.Contains(fs[0].Content, "podbox policy log <sandbox> --denied") {
		t.Error("block lacks the commands for the human")
	}
	if !strings.HasPrefix(fs[1].Content, "---\nname: podbox-sandbox\ndescription: ") {
		t.Error("skill front matter")
	}
}

func TestFilesKiroAndShell(t *testing.T) {
	fs := Files("kiro")
	if len(fs) != 1 || fs[0].Path != ".kiro/steering/podbox.md" || fs[0].Mode != Owned {
		t.Fatalf("%+v", fs)
	}
	if strings.HasPrefix(fs[0].Content, "---") {
		t.Error("steering must have no front matter")
	}
	if got := Files("shell"); len(got) != 0 {
		t.Fatalf("shell: %+v", got)
	}
}

func TestTemplatesAreStaticAndClean(t *testing.T) {
	bad := regexp.MustCompile(`/home/|/Users/|/workspace|https?://|\{\{|token|password|secret`)
	for _, agent := range []string{"claude", "kiro"} {
		for _, f := range Files(agent) {
			if m := bad.FindString(f.Content); m != "" {
				t.Errorf("%s contains %q", f.Path, m)
			}
		}
	}
}

func TestMergeBlock(t *testing.T) {
	const blk = "hello\n"
	const region = beginMarker + "\nhello\n" + endMarker + "\n"
	cases := []struct{ name, in, want string }{
		{"empty", "", region},
		{"user text", "mine\n", "mine\n\n" + region},
		{"user text no newline", "mine", "mine\n\n" + region},
		{"old block in place", "top\n\n" + beginMarker + "\nold\n" + endMarker + "\n\nbottom\n", "top\n\n" + region + "\nbottom\n"},
	}
	for _, c := range cases {
		got, err := MergeBlock(c.in, blk)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
		again, err := MergeBlock(got, blk)
		if err != nil || again != got {
			t.Errorf("%s: second merge changed %q -> %q", c.name, got, again)
		}
	}
	got, _ := MergeBlock("x\n", "one\n")
	got, _ = MergeBlock(got, "two\n")
	if strings.Count(got, beginMarker) != 1 || strings.Contains(got, "one") || !strings.HasPrefix(got, "x\n") {
		t.Errorf("replace: %q", got)
	}
}

func TestMergeBlockUnterminated(t *testing.T) {
	in := "a\n" + beginMarker + "\nb\n"
	got, err := MergeBlock(in, "x\n")
	if !errors.Is(err, ErrUnterminated) || got != "" {
		t.Fatalf("%q %v", got, err)
	}
}
