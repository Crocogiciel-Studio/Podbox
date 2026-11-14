package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDecide(t *testing.T) {
	e := &Effective{Rules: []Rule{
		{Host: "*.pythonhosted.org", Allow: true, Layer: LayerGlobal},
		{Host: "pypi.org", Allow: true, Layer: LayerProject},
		{Host: "evil.pypi.org", Allow: false, Layer: LayerGlobal},
		{Host: "*.pypi.org", Allow: true, Layer: LayerProject},
		{Host: "internal.corp", Allow: true, AllowPrivate: true, Layer: LayerProject},
	}}
	cases := []struct {
		host  string
		allow bool
		layer string
	}{
		{"files.pythonhosted.org", true, LayerGlobal},
		{"FILES.PythonHosted.org:443", true, LayerGlobal},
		{"pythonhosted.org", false, LayerDefaults},
		{"pypi.org", true, LayerProject},
		{"pypi.org.", true, LayerProject},
		{"evil.pypi.org", false, LayerGlobal},
		{"good.pypi.org", true, LayerProject},
		{"example.com", false, LayerDefaults},
		{"notpypi.org", false, LayerDefaults},
	}
	for _, c := range cases {
		d := e.Decide(c.host, 443)
		_, layer := d.Reason()
		if d.Allow != c.allow || layer != c.layer {
			t.Errorf("%s: allow=%v layer=%s, want %v %s", c.host, d.Allow, layer, c.allow, c.layer)
		}
	}
	if d := e.Decide("internal.corp", 443); !d.AllowPrivate {
		t.Error("allow_private not carried")
	}
	if d := e.Decide("pypi.org", 443); d.AllowPrivate {
		t.Error("allow_private leaked")
	}
	var nilPolicy *Effective
	if nilPolicy.Decide("pypi.org", 443).Allow {
		t.Error("nil policy must deny")
	}
}

func TestProjectCannotWidenGlobalDeny(t *testing.T) {
	dir := t.TempDir()
	s := Sources{
		Agent:   "claude",
		Global:  write(t, dir, "global.yaml", "rules:\n  - host: pastebin.com\n    allow: false\n"),
		Project: write(t, dir, "project.yaml", "rules:\n  - host: pastebin.com\n    allow: true\n"),
		Session: write(t, dir, "session.yaml", "rules:\n  - host: pastebin.com\n    allow: true\n"),
	}
	e := Compile(s)
	if d := e.Decide("pastebin.com", 443); d.Allow {
		t.Fatalf("global deny overridden: %+v", d.Rule)
	}
	if !e.Decide("api.anthropic.com", 443).Allow {
		t.Fatal("claude defaults missing")
	}
}

func TestInvalidOrMissingNeverAllows(t *testing.T) {
	dir := t.TempDir()
	bad := []string{
		"rules: [ {host: x.com, allow: true",                       // broken yaml
		"rules:\n  - host: x.com\n",                                // allow missing
		"default: allow\nrules:\n  - host: x.com\n    allow: true", // widening default
		"rules:\n  - host: x.com\n    allow: true\n    alow: true", // unknown key
		"rules:\n  - host: 'x*.com'\n    allow: true",              // bad wildcard
		"mode: yolo\nrules:\n  - host: x.com\n    allow: true",
	}
	for i, body := range bad {
		p := write(t, dir, "p.yaml", body)
		e := Compile(Sources{Agent: "shell", Project: p})
		if e.Decide("x.com", 443).Allow {
			t.Errorf("case %d: invalid layer allowed x.com", i)
		}
		if len(e.Warnings) == 0 {
			t.Errorf("case %d: no warning", i)
		}
	}
	e := Compile(Sources{Agent: "shell", Project: filepath.Join(dir, "missing.yaml")})
	if len(e.Warnings) != 0 || e.Decide("x.com", 443).Allow {
		t.Fatalf("missing file: %+v", e)
	}
	if len(e.Rules) != len(Defaults("shell")) {
		t.Fatal("missing file added rules")
	}
}

func TestModeWarns(t *testing.T) {
	_, warns, err := Parse([]byte("mode: ask\n"))
	if err != nil || len(warns) != 1 {
		t.Fatal(warns, err)
	}
}

func TestSetRuleAndEffectiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	session := filepath.Join(dir, "session.yaml")
	if err := SetRule(session, "PyPI.org", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := SetRule(session, "pypi.org", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := SetRule(session, "*.example.com", true, nil); err != nil {
		t.Fatal(err)
	}
	rs, warns := LoadLayer(session, LayerSession)
	if len(warns) != 0 || len(rs) != 2 || rs[0].Host != "pypi.org" || rs[0].Allow {
		t.Fatalf("%+v %v", rs, warns)
	}
	eff := filepath.Join(dir, "policy.effective.json")
	if err := WriteEffective(eff, Compile(Sources{Sandbox: "s", Agent: "shell", Session: session})); err != nil {
		t.Fatal(err)
	}
	e, err := ReadEffective(eff)
	if err != nil || e.Sandbox != "s" || e.Decide("pypi.org", 443).Allow || !e.Decide("a.example.com", 443).Allow {
		t.Fatalf("%+v %v", e, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temp files left: %v", entries)
	}
}

func TestSetRuleRefusesInvalidFile(t *testing.T) {
	p := write(t, t.TempDir(), "p.yaml", "rules: [")
	if err := SetRule(p, "x.com", true, nil); err == nil {
		t.Fatal("overwrote an invalid file")
	}
}

func TestPorts(t *testing.T) {
	f, _, err := Parse([]byte("rules:\n  - host: git.corp\n    allow: true\n    ports: [22, 443]\n  - host: npm.test\n    allow: true\n  - host: evil.test\n    allow: false\n  - host: evil.test\n    allow: true\n    ports: [8080]\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Effective{Rules: f.rules(LayerProject)}
	cases := []struct {
		host  string
		port  int
		allow bool
	}{
		{"npm.test", 443, true}, {"npm.test", 80, true}, {"npm.test", 22, false}, {"npm.test", 8443, false},
		{"git.corp", 22, true}, {"git.corp", 443, true}, {"git.corp", 80, false},
		{"evil.test", 8080, false}, {"evil.test", 443, false}, // a deny without ports covers every port
	}
	for _, c := range cases {
		if got := e.Decide(c.host, c.port).Allow; got != c.allow {
			t.Errorf("%s:%d allow=%v, want %v", c.host, c.port, got, c.allow)
		}
	}
	for _, bad := range []string{"ports: [0]", "ports: [70000]", "ports: [x]"} {
		if _, _, err := Parse([]byte("rules:\n  - host: a.test\n    allow: true\n    " + bad + "\n")); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	p := filepath.Join(t.TempDir(), "s.yaml")
	if err := SetRule(p, "git.corp", true, []int{22}); err != nil {
		t.Fatal(err)
	}
	rs, _ := LoadLayer(p, LayerSession)
	if len(rs) != 1 || rs[0].PortList() != "22" {
		t.Fatalf("%+v", rs)
	}
	if err := SetRule(p, "git.corp", true, []int{0}); err == nil {
		t.Fatal("port 0 accepted")
	}
}
