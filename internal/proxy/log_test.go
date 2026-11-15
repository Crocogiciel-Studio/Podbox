package proxy

import (
	"strings"
	"testing"
	"time"
)

func TestParseEntry(t *testing.T) {
	if _, ok := ParseEntry([]byte(`{"time":"t","event":"policy reloaded"}`)); ok {
		t.Error("event line parsed as a decision")
	}
	if _, ok := ParseEntry([]byte("not json")); ok {
		t.Error("garbage parsed")
	}
	e, ok := ParseEntry([]byte(`{"time":"2026-01-02T03:04:05.5Z","sandbox":"s","method":"CONNECT","host":"x.test","port":"443","decision":"deny","reason":"policy","rule":"default","layer":"defaults"}`))
	if !ok || e.Host != "x.test" || !e.Denied() {
		t.Fatalf("%+v %v", e, ok)
	}
}

func TestEntryFix(t *testing.T) {
	cases := []struct {
		e        Entry
		why, fix string
	}{
		{Entry{Host: "x.test", Port: "443", Decision: "deny", Reason: "policy", Rule: "default", Layer: "defaults"},
			"not in the allowlist", "podbox policy allow s x.test"},
		{Entry{Host: "x.test", Port: "80", Decision: "deny", Reason: "policy", Rule: "default", Layer: "defaults"},
			"not in the allowlist", "podbox policy allow s x.test"},
		{Entry{Host: "x.test", Port: "22", Decision: "deny", Reason: "policy", Rule: "default", Layer: "defaults"},
			"not in the allowlist", "podbox policy allow s x.test --port 22"},
		{Entry{Host: "x.test", Port: "443", Decision: "deny", Reason: "policy", Rule: "x.test", Layer: "session"},
			"deny rule x.test, session layer", "podbox policy allow s x.test"},
		{Entry{Host: "a.x.test", Port: "443", Decision: "deny", Reason: "policy", Rule: "*.x.test", Layer: "global"},
			"deny rule *.x.test, global layer", "remove the deny rule *.x.test from the global policy layer"},
		{Entry{Host: "db.corp", Port: "443", Decision: "deny", Reason: "blocked_destination", IP: "10.1.2.3"},
			"blocked destination 10.1.2.3", "add allow_private: true to the rule for db.corp in .podbox/policy.yaml"},
		{Entry{Host: "evil.test", Port: "443", Decision: "deny", Reason: "blocked_destination", IP: "127.0.0.1"},
			"blocked destination 127.0.0.1", ""},
		{Entry{Host: "nx.test", Port: "443", Decision: "deny", Reason: "resolve_error"}, "name does not resolve", ""},
		{Entry{Host: "ok.test", Port: "443", Decision: "allow", Rule: "ok.test", Layer: "defaults"}, "allow rule ok.test, defaults layer", ""},
	}
	for _, c := range cases {
		if got := c.e.Why(); got != c.why {
			t.Errorf("%+v: why %q, want %q", c.e, got, c.why)
		}
		if got := c.e.Fix("s"); got != c.fix {
			t.Errorf("%+v: fix %q, want %q", c.e, got, c.fix)
		}
	}
}

func TestEntryLine(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := Entry{Time: ts.Format(time.RFC3339Nano), Method: "CONNECT", Host: "x.test", Port: "8443", Decision: "deny", Reason: "policy", Rule: "default", Layer: "defaults"}
	want := ts.Local().Format("2006-01-02 15:04:05") + "  DENY   CONNECT x.test:8443  (not in the allowlist)  → podbox policy allow s x.test --port 8443"
	if got := e.Line("s"); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	a := Entry{Time: "bad", Method: "GET", Host: "ok.test", Port: "80", Decision: "allow", Rule: "ok.test", Layer: "project"}
	if got := a.Line("s"); got != "bad  ALLOW  GET ok.test:80  (allow rule ok.test, project layer)" || strings.Contains(got, "→") {
		t.Errorf("allow line %q", got)
	}
}

func TestNotAllowedBodyUsesAllowCommand(t *testing.T) {
	if b := NotAllowedBody("s", "x.test", 22); !strings.Contains(b, "on port 22.\nAsk the user to run: podbox policy allow s x.test --port 22\n") {
		t.Errorf("%q", b)
	}
	if b := NotAllowedBody("s", "x.test", 443); b != "Blocked by podbox network policy: host \"x.test\" is not allowed.\nAsk the user to run: podbox policy allow s x.test\n" {
		t.Errorf("%q", b)
	}
}
