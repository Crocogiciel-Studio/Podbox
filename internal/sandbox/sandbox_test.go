package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

func TestAgentSpecHardening(t *testing.T) {
	m := Meta{Name: "t1", Agent: "claude", Workspace: "/home/u/proj"}
	s := agentSpec(agentInput{
		Meta: m, Image: "img", ProxyIP: "10.89.0.2", EmptyDir: "/state/t1/masks/empty", GOOS: "linux",
		Masks:  []workspace.Mask{{Rel: ".env"}, {Rel: ".ssh", Dir: true}},
		Limits: &runtime.Limits{CPUs: "4", Memory: "8g", PIDs: 2048},
	})
	argv := strings.Join(runtime.CreateArgs(s), " ")
	for _, want := range []string{
		"--userns=keep-id:uid=1000,gid=1000", "--user 1000:1000", "--cap-drop=ALL",
		"--security-opt no-new-privileges", "--http-proxy=false", "--dns 127.0.0.1",
		"--add-host proxy:10.89.0.2", "--network podbox-t1-int ", "--tmpfs /tmp:",
		"--volume /home/u/proj:/home/u/proj ", "--volume podbox-home-claude:/home/agent ",
		"--volume /dev/null:/home/u/proj/.env:ro", "--volume /state/t1/masks/empty:/home/u/proj/.ssh:ro",
		"--env HTTPS_PROXY=http://proxy:3128", "--env NO_PROXY=localhost,127.0.0.1", "--cpus 4", "--pids-limit 2048",
		"--workdir /home/u/proj",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %q in\n%s", want, argv)
		}
	}
	for _, bad := range []string{"--privileged", "podman.sock", "docker.sock", EgressNetwork} {
		if strings.Contains(argv, bad) {
			t.Errorf("agent argv contains %q", bad)
		}
	}
	if w := agentSpec(agentInput{Meta: m, GOOS: "windows"}); w.Workdir != "/workspace" {
		t.Errorf("windows workdir %s", w.Workdir)
	}
}

func TestProxySpec(t *testing.T) {
	s := proxySpec(Meta{Name: "t1"}, "proxy-img", "/state/t1", "10.89.0.2", "http://corp:3128", true)
	argv := strings.Join(runtime.CreateArgs(s), " ")
	for _, want := range []string{"--network podbox-egress", "--network podbox-t1-int:ip=10.89.0.2", "--read-only",
		"--http-proxy=false", "--volume /state/t1:/etc/podbox:ro,z", "--env PODBOX_UPSTREAM_PROXY=http://corp:3128"} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %q in %s", want, argv)
		}
	}
}

func TestUpstream(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		cfg  string
		env  map[string]string
		want string
	}{
		{"", nil, ""},
		{"none", map[string]string{"HTTPS_PROXY": "http://corp:3128"}, ""},
		{"", map[string]string{"HTTPS_PROXY": "http://corp:3128"}, "http://corp:3128"},
		{"", map[string]string{"http_proxy": "corp:8080"}, "http://corp:8080"},
		{"http://u:p@cfg:1", map[string]string{"HTTPS_PROXY": "http://corp:3128"}, "http://u:p@cfg:1"},
		{"", map[string]string{"HTTPS_PROXY": "http://127.0.0.1:3128"}, "http://host.containers.internal:3128"},
	}
	for _, c := range cases {
		if got := Upstream(c.cfg, env(c.env)); got != c.want {
			t.Errorf("Upstream(%q, %v) = %q, want %q", c.cfg, c.env, got, c.want)
		}
	}
}

func TestProxyIP(t *testing.T) {
	ip, err := proxyIP(runtime.Network{Subnets: []string{"fd00::/64", "10.89.3.0/24"}})
	if err != nil || ip != "10.89.3.2" {
		t.Fatal(ip, err)
	}
}

func TestDefaultName(t *testing.T) {
	if n := DefaultName("claude", "/home/u/My Project"); n != "claude-my-project" || ValidateName(n) != nil {
		t.Fatal(n)
	}
}

// listRT reports containers from labels only.
type listRT struct {
	runtime.Runtime
	cs []runtime.Container
}

func (l listRT) ContainerList(context.Context, string) ([]runtime.Container, error) { return l.cs, nil }

func TestListFromLabelsWithStaleState(t *testing.T) {
	root := t.TempDir()
	st := Store{Root: root}
	_ = st.Save(Meta{Name: "orphan", Agent: "shell", Workspace: "/w2"})
	_ = os.MkdirAll(filepath.Join(root, "garbage"), 0o755)
	lbl := func(name, role string) map[string]string {
		return map[string]string{LabelSandbox: name, LabelRole: role, LabelAgent: "claude", LabelWorkspace: "/w"}
	}
	m := &Manager{Store: st, RT: listRT{cs: []runtime.Container{
		{Name: "podbox-live-agent", State: "running", Labels: lbl("live", "agent")},
		{Name: "podbox-live-proxy", State: "running", Labels: lbl("live", "proxy")},
	}}}
	got, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if l := got[0]; l.Name != "live" || l.State != "running" || l.Proxy != "running" || l.Agent != "claude" || l.StateDir {
		t.Fatalf("live: %+v", l)
	}
	if o := got[1]; o.Name != "orphan" || o.State != "missing" || !o.StateDir || o.Workspace != "/w2" {
		t.Fatalf("orphan: %+v", o)
	}
}

// TestStatusChecksOnlyItsSandbox: stop and rm look one sandbox up, without
// the drift and git checks List runs on every sandbox.
func TestStatusChecksOnlyItsSandbox(t *testing.T) {
	st := Store{Root: t.TempDir()}
	for _, n := range []string{"target", "other"} {
		if err := st.Save(Meta{Name: n, Agent: "shell", Workspace: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	write(t, st.gitPrintPath("other"), "not json") // List warns about it
	warned := false
	m := &Manager{Store: st, Warn: func(string, ...any) { warned = true }, RT: listRT{cs: []runtime.Container{
		{Name: "podbox-other-agent", State: "running", Labels: map[string]string{LabelSandbox: "other", LabelRole: "agent", LabelAgent: "claude"}},
	}}}
	s, err := m.status(context.Background(), "target")
	if err != nil || s.Agent != "shell" || s.State != "missing" || !s.StateDir || warned {
		t.Fatalf("%+v %v warned=%v", s, err, warned)
	}
	if _, err := m.status(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := m.List(context.Background()); err != nil || !warned {
		t.Fatalf("List should check every sandbox: %v warned=%v", err, warned)
	}
}
