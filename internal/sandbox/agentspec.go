package sandbox

import (
	"path"
	"path/filepath"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// Labels on podbox objects.
const (
	LabelSandbox   = "io.podbox.sandbox"
	LabelRole      = "io.podbox.role"
	LabelAgent     = "io.podbox.agent"
	LabelWorkspace = "io.podbox.workspace"
	LabelHome      = "io.podbox.home"
	LabelShared    = "io.podbox.shared"
)

// AgentUser is the uid:gid of the agent inside the container.
const AgentUser = "1000:1000"

// policyMount is where the proxy reads the sandbox state directory.
const policyMount = "/etc/podbox"

func agentContainer(name string) string { return "podbox-" + name + "-agent" }
func proxyContainer(name string) string { return "podbox-" + name + "-proxy" }

// HomeVolume persists one agent's home (and its auth) across sandboxes.
func HomeVolume(agent string) string { return "podbox-home-" + agent }

// WorkspaceTarget is the workspace path inside the container: the same
// absolute path, except on Windows hosts.
func WorkspaceTarget(goos, hostPath string) string {
	if goos == "windows" {
		return "/workspace"
	}
	return filepath.ToSlash(hostPath)
}

func labels(m Meta, role string) map[string]string {
	return map[string]string{LabelSandbox: m.Name, LabelRole: role, LabelAgent: m.Agent, LabelWorkspace: m.Workspace}
}

// proxySpec is the per-sandbox proxy: egress + internal networks, policy
// directory read-only, nothing else from the host.
func proxySpec(m Meta, image, stateDir, ip, upstream string, selinux bool) runtime.ContainerSpec {
	s := runtime.ContainerSpec{
		Name:            proxyContainer(m.Name),
		Image:           image,
		Hostname:        "proxy",
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		ReadOnlyRoot:    true,
		Networks:        []runtime.NetworkAttachment{{Name: EgressNetwork}, {Name: internalNetwork(m.Name), IP: ip}},
		Mounts:          []runtime.Mount{{Type: runtime.Bind, Source: stateDir, Target: policyMount, ReadOnly: true, Relabel: selinux}},
		Labels:          labels(m, "proxy"),
	}
	if upstream != "" {
		s.Env = map[string]string{"PODBOX_UPSTREAM_PROXY": upstream}
	}
	return s
}

type agentInput struct {
	Meta     Meta
	Image    string
	ProxyIP  string
	ReadOnly []string // workspace paths the agent may read but not edit
	Masks    []workspace.Mask
	EmptyDir string
	Limits   *runtime.Limits
	SELinux  bool
	GOOS     string
}

// agentSpec is the hardened agent container: internal network only, no
// DNS, no inherited proxy variables, non-root, no capabilities.
func agentSpec(in agentInput) runtime.ContainerSpec {
	m := in.Meta
	target := WorkspaceTarget(in.GOOS, m.Workspace)
	proxyURL := "http://proxy:" + ProxyPort
	mounts := []runtime.Mount{
		{Type: runtime.Bind, Source: m.Workspace, Target: target, Relabel: in.SELinux},
		{Type: runtime.Volume, Source: HomeVolume(m.Agent), Target: "/home/agent"},
		{Type: runtime.Tmpfs, Target: "/tmp", Options: "rw,nosuid,nodev,size=1g"},
	}
	for _, rel := range in.ReadOnly {
		p := path.Join(target, rel)
		mounts = append(mounts, runtime.Mount{Type: runtime.Bind, Source: filepath.Join(m.Workspace, rel), Target: p, ReadOnly: true, Relabel: in.SELinux})
	}
	for _, mk := range in.Masks {
		dst := path.Join(target, mk.Rel)
		if mk.Dir {
			mounts = append(mounts, runtime.Mount{Type: runtime.Bind, Source: in.EmptyDir, Target: dst, ReadOnly: true, Relabel: in.SELinux})
		} else {
			mounts = append(mounts, runtime.Mount{Type: runtime.Bind, Source: "/dev/null", Target: dst, ReadOnly: true})
		}
	}
	return runtime.ContainerSpec{
		Name:            agentContainer(m.Name),
		Image:           in.Image,
		Hostname:        m.Name,
		Userns:          "keep-id:uid=1000,gid=1000",
		User:            AgentUser,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Limits:          in.Limits,
		Networks:        []runtime.NetworkAttachment{{Name: internalNetwork(m.Name)}},
		DNS:             []string{"127.0.0.1"},
		Hosts:           map[string]string{"proxy": in.ProxyIP},
		Mounts:          mounts,
		Workdir:         target,
		Env: map[string]string{
			"HTTPS_PROXY": proxyURL, "HTTP_PROXY": proxyURL, "https_proxy": proxyURL, "http_proxy": proxyURL,
			"NO_PROXY": "localhost,127.0.0.1", "no_proxy": "localhost,127.0.0.1",
			"JAVA_TOOL_OPTIONS": "-Dhttp.proxyHost=proxy -Dhttp.proxyPort=" + ProxyPort + " -Dhttps.proxyHost=proxy -Dhttps.proxyPort=" + ProxyPort,
			"PODBOX_SANDBOX":    m.Name,
		},
		Labels: labels(m, "agent"),
	}
}
