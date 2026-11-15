package sandbox

import (
	"context"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/agentctx"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

// homeRT is a fake agent container: a map of home files behind Exec.
type homeRT struct {
	runtime.Runtime
	files  map[string]string
	specs  []runtime.ExecSpec
	fail   bool
	noView bool
}

func (h *homeRT) Exec(_ context.Context, _ string, s runtime.ExecSpec) (int, error) {
	h.specs = append(h.specs, s)
	if s.Command[0] == "test" {
		if h.noView {
			return 1, nil
		}
		return 0, nil
	}
	target := s.Command[len(s.Command)-1]
	switch {
	case h.fail:
		return 1, nil
	case strings.Contains(s.Command[2], "cat >"):
		b, _ := io.ReadAll(s.Stdin)
		h.files[target] = string(b)
	default:
		c, ok := h.files[target]
		if !ok {
			return exitAbsent, nil
		}
		io.WriteString(s.Stdout, c)
	}
	return 0, nil
}

func newCtxManager(files map[string]string) (*Manager, *homeRT, *[]string) {
	rt := &homeRT{files: files}
	var warns []string
	m := &Manager{RT: rt, Warn: func(f string, a ...any) { warns = append(warns, f) }}
	return m, rt, &warns
}

func TestInstallContextAbsentFiles(t *testing.T) {
	m, rt, warns := newCtxManager(map[string]string{})
	m.installContext(context.Background(), Meta{Name: "s", Agent: "claude"})
	m.installContext(context.Background(), Meta{Name: "s", Agent: "claude"}) // a second start
	if len(*warns) != 0 {
		t.Fatalf("warned %v", *warns)
	}
	claude := rt.files[path.Join(agentHome, ".claude/CLAUDE.md")]
	if strings.Count(claude, "podbox:begin") != 1 || !strings.Contains(claude, "$PODBOX_SANDBOX") {
		t.Fatalf("CLAUDE.md %q", claude)
	}
	if !strings.Contains(rt.files[path.Join(agentHome, ".claude/skills/podbox-sandbox/SKILL.md")], "name: podbox-sandbox") {
		t.Fatal("skill missing")
	}
	for _, s := range rt.specs {
		if s.User != AgentUser {
			t.Errorf("exec as %q", s.User)
		}
		if strings.Contains(s.Command[2], "cat >") && !s.Interactive {
			t.Error("write without Interactive")
		}
	}
}

func TestInstallContextKeepsUserContent(t *testing.T) {
	target := path.Join(agentHome, ".claude/CLAUDE.md")
	m, rt, warns := newCtxManager(map[string]string{target: "my notes\n"})
	m.installContext(context.Background(), Meta{Name: "s", Agent: "claude"})
	if len(*warns) != 0 || !strings.HasPrefix(rt.files[target], "my notes\n\n") || !strings.Contains(rt.files[target], "podbox:end") {
		t.Fatalf("%q %v", rt.files[target], *warns)
	}
}

func TestInstallContextUnmergeableWarns(t *testing.T) {
	target := path.Join(agentHome, ".claude/CLAUDE.md")
	bad := "x\n<!-- podbox:begin -->\nhalf\n"
	m, rt, warns := newCtxManager(map[string]string{target: bad})
	m.installContext(context.Background(), Meta{Name: "s", Agent: "claude"})
	if rt.files[target] != bad || len(*warns) != 1 {
		t.Fatalf("%q %v", rt.files[target], *warns)
	}
	if _, ok := rt.files[path.Join(agentHome, ".claude/skills/podbox-sandbox/SKILL.md")]; !ok {
		t.Fatal("the skill is independent of the unmergeable file")
	}
}

func TestInstallContextExecFailureWarns(t *testing.T) {
	m, rt, warns := newCtxManager(map[string]string{})
	rt.fail = true
	m.installContext(context.Background(), Meta{Name: "s", Agent: "kiro"})
	if len(*warns) != 1 {
		t.Fatalf("%v", *warns)
	}
}

func TestInstallContextOverCapLeavesFileAndWarns(t *testing.T) {
	target := path.Join(agentHome, ".claude/CLAUDE.md")
	big := strings.Repeat("a", maxRead+1)
	m, rt, warns := newCtxManager(map[string]string{target: big})
	m.installContext(context.Background(), Meta{Name: "s", Agent: "claude"})
	if rt.files[target] != big || len(*warns) != 1 || !strings.Contains((*warns)[0], "agent context") {
		t.Fatalf("warns %v, file changed: %v", *warns, rt.files[target] != big)
	}
}

func TestInstallContextWarnsWithoutPolicyView(t *testing.T) {
	m, rt, warns := newCtxManager(map[string]string{})
	rt.noView = true
	m.installContext(context.Background(), Meta{Name: "s", Agent: "claude"})
	if len(*warns) != 1 || !strings.Contains((*warns)[0], "restart") {
		t.Fatalf("%v", *warns)
	}
	if _, ok := rt.files[path.Join(agentHome, ".claude/CLAUDE.md")]; !ok {
		t.Fatal("context not installed")
	}
}

func TestInstallContextShellDoesNothing(t *testing.T) {
	m, rt, _ := newCtxManager(map[string]string{})
	m.installContext(context.Background(), Meta{Name: "s", Agent: "shell"})
	if len(rt.specs) != 0 || len(agentctx.Files("shell")) != 0 {
		t.Fatal("shell got context")
	}
}
