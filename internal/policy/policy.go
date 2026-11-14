// Package policy compiles layered egress allowlists into one effective policy.
package policy

import (
	"fmt"
	"strconv"
	"strings"
)

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
	// Ports limits the rule to these ports. Empty means DefaultPorts for an
	// allow and every port for a deny.
	Ports []int  `json:"ports,omitempty"`
	Layer string `json:"layer"`
}

// DefaultPorts are the ports an allow rule without ports opens.
var DefaultPorts = []int{80, 443}

// Covers reports whether the rule applies to port.
func (r Rule) Covers(port int) bool {
	ps := r.Ports
	if len(ps) == 0 {
		if !r.Allow {
			return true
		}
		ps = DefaultPorts
	}
	for _, p := range ps {
		if p == port {
			return true
		}
	}
	return false
}

// PortList renders the ports a rule applies to.
func (r Rule) PortList() string {
	ps := r.Ports
	if len(ps) == 0 {
		if !r.Allow {
			return "all"
		}
		ps = DefaultPorts
	}
	return JoinPorts(ps)
}

// JoinPorts renders ports as "80,443".
func JoinPorts(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
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

// Decision is the verdict for one host and port.
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
// allow, otherwise deny. A rule matches when its host matches and it covers
// port. A nil policy denies everything.
func (e *Effective) Decide(host string, port int) Decision {
	if e == nil {
		return Decision{}
	}
	h := NormalizeHost(host)
	var d Decision
	for i := range e.Rules {
		r := &e.Rules[i]
		if !Match(r.Host, h) || !r.Covers(port) {
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
	return fmt.Sprintf("%s %s ports %s (%s)", verb, r.Host, r.PortList(), r.Layer)
}
