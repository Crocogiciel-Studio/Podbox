package policy

var commonHosts = []string{
	"registry.npmjs.org",
	"pypi.org",
	"files.pythonhosted.org",
	"repo.maven.apache.org",
	"github.com",
	"*.githubusercontent.com",
}

var agentHosts = map[string][]string{
	"claude": {
		"api.anthropic.com",
		"*.anthropic.com",
		"claude.ai",
		"*.claude.ai",
		"claude.com",
		"*.claude.com",
		"statsig.anthropic.com",
		"sentry.io",
	},
	// Unverified: Kiro's hosts could not be observed (installer blocked in
	// the build environment). Taken from Kiro/Amazon Q CLI documentation.
	"kiro": {
		"kiro.dev",
		"*.kiro.dev",
		"q.us-east-1.amazonaws.com",
		"codewhisperer.us-east-1.amazonaws.com",
		"oidc.us-east-1.amazonaws.com",
		"*.awsapps.com",
	},
}

// Defaults returns the built-in allow rules for an agent.
func Defaults(agent string) []Rule {
	var rs []Rule
	for _, h := range append(append([]string{}, agentHosts[agent]...), commonHosts...) {
		rs = append(rs, Rule{Host: h, Allow: true, Layer: LayerDefaults})
	}
	return rs
}
