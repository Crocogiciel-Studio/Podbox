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
		d := e.Decide(c.host)
		_, layer := d.Reason()
		if d.Allow != c.allow || layer != c.layer {
			t.Errorf("%s: allow=%v layer=%s, want %v %s", c.host, d.Allow, layer, c.allow, c.layer)
		}
	}
	if d := e.Decide("internal.corp"); !d.AllowPrivate {
		t.Error("allow_private not carried")
	}
	if d := e.Decide("pypi.org"); d.AllowPrivate {
		t.Error("allow_private leaked")
	}
	var nilPolicy *Effective
	if nilPolicy.Decide("pypi.org").Allow {
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
	if d := e.Decide("pastebin.com"); d.Allow {
		t.Fatalf("global deny overridden: %+v", d.Rule)
	}
	if !e.Decide("api.anthropic.com").Allow {
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
		if e.Decide("x.com").Allow {
			t.Errorf("case %d: invalid layer allowed x.com", i)
		}
		if len(e.Warnings) == 0 {
			t.Errorf("case %d: no warning", i)
		}
	}
	e := Compile(Sources{Agent: "shell", Project: filepath.Join(dir, "missing.yaml")})
	if len(e.Warnings) != 0 || e.Decide("x.com").Allow {
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
	if err := SetRule(session, "PyPI.org", true); err != nil {
		t.Fatal(err)
	}
	if err := SetRule(session, "pypi.org", false); err != nil {
		t.Fatal(err)
	}
	if err := SetRule(session, "*.example.com", true); err != nil {
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
	if err != nil || e.Sandbox != "s" || e.Decide("pypi.org").Allow || !e.Decide("a.example.com").Allow {
		t.Fatalf("%+v %v", e, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temp files left: %v", entries)
	}
}

func TestSetRuleRefusesInvalidFile(t *testing.T) {
	p := write(t, t.TempDir(), "p.yaml", "rules: [")
	if err := SetRule(p, "x.com", true); err == nil {
		t.Fatal("overwrote an invalid file")
	}
}
