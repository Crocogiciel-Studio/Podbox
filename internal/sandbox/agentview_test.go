package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
)

func readView(t *testing.T, st Store, name string) policy.AgentView {
	t.Helper()
	b, err := os.ReadFile(st.AgentPolicyPath(name))
	if err != nil {
		t.Fatal(err)
	}
	var v policy.AgentView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func hasRule(v policy.AgentView, host string) bool {
	for _, r := range v.Rules {
		if r.Host == host {
			return true
		}
	}
	return false
}

func TestAgentViewFollowsRefreshAndReload(t *testing.T) {
	ws := t.TempDir()
	st := Store{Root: t.TempDir(), ConfigDir: t.TempDir()}
	m := Meta{Name: "s", Agent: "shell", Workspace: ws}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, ProjectPolicyRel), "rules:\n  - host: one.test\n    allow: true\n    ports: [8080]\n")
	if _, err := st.Snapshot(m); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RefreshPolicy(m); err != nil {
		t.Fatal(err)
	}
	v := readView(t, st, "s")
	if v.Sandbox != "s" || v.Workspace != ws || !hasRule(v, "one.test") {
		t.Fatalf("%+v", v)
	}
	write(t, st.SessionPath("s"), "rules:\n  - host: two.test\n    allow: true\n")
	if _, err := st.RefreshPolicy(m); err != nil {
		t.Fatal(err)
	}
	if v := readView(t, st, "s"); !hasRule(v, "two.test") {
		t.Fatalf("second refresh not shown: %+v", v)
	}
	write(t, filepath.Join(ws, ProjectPolicyRel), "rules:\n  - host: three.test\n    allow: true\n")
	if _, _, err := st.Reload(m, false); err != nil {
		t.Fatal(err)
	}
	if v := readView(t, st, "s"); !hasRule(v, "three.test") || hasRule(v, "one.test") {
		t.Fatalf("reload not shown: %+v", v)
	}
}
