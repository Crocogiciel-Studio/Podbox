package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentView(t *testing.T) {
	e := Effective{
		Version: EffectiveVersion, Sandbox: "s1",
		Rules: []Rule{
			{Host: "a.example", Allow: true, Layer: LayerGlobal, AllowPrivate: true},
			{Host: "b.example", Allow: true, Ports: []int{8080, 443}, Layer: LayerProject},
			{Host: "c.example", Allow: false, Layer: LayerSession},
		},
		Warnings: []string{"secret warning /home/u/x"},
	}
	v := e.AgentView("/w")
	if v.Sandbox != "s1" || v.Workspace != "/w" || len(v.Rules) != 3 {
		t.Fatalf("%+v", v)
	}
	for i, want := range []string{"80,443", "8080,443", "all"} {
		if v.Rules[i].Ports != want {
			t.Errorf("rule %d ports %q, want %q", i, v.Rules[i].Ports, want)
		}
	}
	b, _ := json.Marshal(v)
	for _, bad := range []string{"layer", "global", "project", "session", "warning", "secret", "allow_private"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("view leaks %q: %s", bad, b)
		}
	}
}
