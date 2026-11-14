// Package agents describes the agents podbox can run.
package agents

import (
	"fmt"
	"sort"

	"github.com/Crocogiciel-Studio/podbox/internal/images"
)

// Agent is a runnable agent.
type Agent struct {
	Name         string
	Image        images.Kind
	Command      []string
	Experimental bool
}

var table = map[string]Agent{
	"claude": {Name: "claude", Image: images.Claude, Command: []string{"claude", "--dangerously-skip-permissions"}},
	// Unverified: the autonomous flag could not be confirmed against a real Kiro install.
	"kiro":  {Name: "kiro", Image: images.Kiro, Command: []string{"kiro-cli", "chat", "--trust-all-tools"}, Experimental: true},
	"shell": {Name: "shell", Image: images.Shell, Command: []string{"bash", "-l"}},
}

// Get returns an agent by name.
func Get(name string) (Agent, error) {
	a, ok := table[name]
	if !ok {
		return Agent{}, fmt.Errorf("unknown agent %q: want one of %v", name, Names())
	}
	return a, nil
}

// Names lists the agents.
func Names() []string {
	var n []string
	for k := range table {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}
