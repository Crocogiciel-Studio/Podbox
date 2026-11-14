// Package policy compiles layered egress allowlists into one effective policy.
package policy

import "fmt"

// Layer names, from general to specific.
const (
	LayerDefaults = "defaults"
	LayerGlobal   = "global"
	LayerProject  = "project"
	LayerSession  = "session"
)

// Rule is one compiled rule with the layer it came from.
type Rule struct {
	Host         string `json:"host"`
	Allow        bool   `json:"allow"`
	AllowPrivate bool   `json:"allow_private,omitempty"`
	Layer        string `json:"layer"`
}

// Effective is the document the proxy enforces.
type Effective struct {
	Version  int      `json:"version"`
	Sandbox  string   `json:"sandbox"`
	Rules    []Rule   `json:"rules"`
	Warnings []string `json:"warnings,omitempty"`
}

// EffectiveVersion is the schema version of Effective.
const EffectiveVersion = 1

// Decision is the verdict for one host.
type Decision struct {
	Allow        bool
	AllowPrivate bool
	Rule         *Rule // nil when the default decided
}

// Reason names what decided, for logs.
func (d Decision) Reason() (rule, layer string) {
	if d.Rule == nil {
		return "default", LayerDefaults
	}
	return d.Rule.Host, d.Rule.Layer
}

// Decide applies the precedence: any matching deny wins, then any matching
// allow, otherwise deny. A nil policy denies everything.
func (e *Effective) Decide(host string) Decision {
	if e == nil {
		return Decision{}
	}
	h := NormalizeHost(host)
	var d Decision
	for i := range e.Rules {
		r := &e.Rules[i]
		if !Match(r.Host, h) {
			continue
		}
		if !r.Allow {
			return Decision{Rule: r}
		}
		if d.Rule == nil {
			d = Decision{Allow: true, Rule: r}
		}
		d.AllowPrivate = d.AllowPrivate || r.AllowPrivate
	}
	return d
}

func (r Rule) String() string {
	verb := "deny"
	if r.Allow {
		verb = "allow"
	}
	return fmt.Sprintf("%s %s (%s)", verb, r.Host, r.Layer)
}
