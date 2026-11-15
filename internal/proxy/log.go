package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Entry is the decision log line for one request.
type Entry struct {
	Time     string `json:"time"`
	Sandbox  string `json:"sandbox,omitempty"`
	Method   string `json:"method"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Layer    string `json:"layer,omitempty"`
	IP       string `json:"ip,omitempty"`
	Via      string `json:"via,omitempty"`
}

// ParseEntry reads one log line; ok is false for event lines and anything
// else that is not a decision.
func ParseEntry(line []byte) (e Entry, ok bool) {
	if json.Unmarshal(line, &e) != nil || e.Decision == "" || e.Host == "" {
		return Entry{}, false
	}
	return e, true
}

// Denied reports a refused request.
func (e Entry) Denied() bool { return e.Decision != "allow" }

// Why names what decided: the rule and its layer, or the refusal reason.
func (e Entry) Why() string {
	switch {
	case e.Reason == "policy" && e.Rule == "default":
		return "not in the allowlist"
	case e.Reason == "blocked_destination":
		return "blocked destination " + e.IP
	case e.Reason == "resolve_error":
		return "name does not resolve"
	case e.Reason != "" && e.Reason != "policy":
		return e.Reason
	}
	verb := "allow"
	if e.Denied() {
		verb = "deny"
	}
	return fmt.Sprintf("%s rule %s, %s layer", verb, e.Rule, e.Layer)
}

// Fix is the host command or edit that would let the request through;
// empty when there is none (an allowed request, a name that does not
// resolve).
func (e Entry) Fix(sandbox string) string {
	if !e.Denied() {
		return ""
	}
	port, _ := strconv.Atoi(e.Port)
	switch {
	case e.Reason == "policy" && (e.Rule == "default" || e.Layer == "session"):
		return AllowCommand(sandbox, e.Host, port)
	case e.Reason == "policy":
		return fmt.Sprintf("remove the deny rule %s from the %s policy layer", e.Rule, e.Layer)
	case e.Reason == "blocked_destination" && isPrivate(e.IP):
		return "add allow_private: true to the rule for " + e.Host + " in .podbox/policy.yaml"
	}
	return ""
}

// Line renders the entry as one text line:
// time  DECISION  method host:port  (why)  → fix
func (e Entry) Line(sandbox string) string {
	t := e.Time
	if pt, err := time.Parse(time.RFC3339Nano, e.Time); err == nil {
		t = pt.Local().Format("2006-01-02 15:04:05")
	}
	s := fmt.Sprintf("%s  %-5s  %s %s:%s  (%s)", t, strings.ToUpper(e.Decision), e.Method, e.Host, e.Port, e.Why())
	if f := e.Fix(sandbox); f != "" {
		s += "  → " + f
	}
	return s
}

func isPrivate(ip string) bool {
	be := checkIP(net.ParseIP(ip), false)
	return be != nil && be.private
}
