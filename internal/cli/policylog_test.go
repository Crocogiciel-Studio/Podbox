package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScanEntries(t *testing.T) {
	log := strings.Join([]string{
		`{"time":"t","event":"policy loaded"}`,
		`{"time":"t","method":"CONNECT","host":"ok.test","port":"443","decision":"allow","rule":"ok.test","layer":"defaults"}`,
		`{"time":"t","method":"CONNECT","host":"x.test","port":"443","decision":"deny","reason":"policy","rule":"default","layer":"defaults"}`,
		`garbage`,
	}, "\n")
	var all, denied []logEntry
	collect := func(dst *[]logEntry) func(logEntry) error {
		return func(e logEntry) error { *dst = append(*dst, e); return nil }
	}
	if err := scanEntries(strings.NewReader(log), "s", false, collect(&all)); err != nil || len(all) != 2 {
		t.Fatalf("all: %v %+v", err, all)
	}
	if err := scanEntries(strings.NewReader(log), "s", true, collect(&denied)); err != nil || len(denied) != 1 {
		t.Fatalf("denied: %v %+v", err, denied)
	}
	b, _ := json.Marshal(denied[0])
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["host"] != "x.test" || m["decision"] != "deny" || m["fix"] != "podbox policy allow s x.test" {
		t.Errorf("json %s", b)
	}
	if b, _ := json.Marshal(all[0]); strings.Contains(string(b), `"fix"`) {
		t.Errorf("allowed entry has a fix: %s", b)
	}
}
