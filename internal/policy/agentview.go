package policy

// AgentViewVersion is the schema version of AgentView.
const AgentViewVersion = 1

// AgentView is the part of the effective policy the agent may read: no
// layer, no warning, no allow_private.
type AgentView struct {
	Version   int             `json:"version"`
	Sandbox   string          `json:"sandbox"`
	Workspace string          `json:"workspace"`
	Rules     []AgentViewRule `json:"rules"`
}

// AgentViewRule is one rule as the agent reads it.
type AgentViewRule struct {
	Host  string `json:"host"`
	Allow bool   `json:"allow"`
	Ports string `json:"ports"` // "80,443" or "all"
}

// AgentView projects e for the agent of the sandbox on workspace.
func (e Effective) AgentView(workspace string) AgentView {
	v := AgentView{Version: AgentViewVersion, Sandbox: e.Sandbox, Workspace: workspace, Rules: make([]AgentViewRule, 0, len(e.Rules))}
	for _, r := range e.Rules {
		v.Rules = append(v.Rules, AgentViewRule{Host: r.Host, Allow: r.Allow, Ports: r.PortList()})
	}
	return v
}
