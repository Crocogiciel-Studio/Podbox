package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// fakePodman writes a script that records its argv and behaves per $FAKE_*.
func fakePodman(t *testing.T) (*Podman, string) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + log + `"
[ -n "$FAKE_STDOUT" ] && printf '%s' "$FAKE_STDOUT"
[ -n "$FAKE_STDERR" ] && printf '%s' "$FAKE_STDERR" >&2
exit ${FAKE_EXIT:-0}
`
	bin := filepath.Join(dir, "podman")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PODBOX_PODMAN", bin)
	p, err := NewPodman()
	if err != nil {
		t.Fatal(err)
	}
	return p, log
}

func recorded(t *testing.T, log string) []string {
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestCreateArgsOrder(t *testing.T) {
	s := ContainerSpec{
		Name: "a", Image: "img", Command: []string{"sleep", "infinity"},
		Userns: "keep-id:uid=1000,gid=1000", User: "1000:1000",
		CapDrop: []string{"ALL"}, NoNewPrivileges: true,
		Limits:   &Limits{CPUs: "4", Memory: "8g", PIDs: 2048},
		Networks: []NetworkAttachment{{Name: "int"}, {Name: "eg", IP: "10.0.0.2"}},
		DNS:      []string{"127.0.0.1"},
		Hosts:    map[string]string{"proxy": "10.0.0.2"},
		Mounts: []Mount{
			{Type: Bind, Source: "/p", Target: "/p", Relabel: true},
			{Type: Bind, Source: "/dev/null", Target: "/p/.env", ReadOnly: true},
			{Type: Tmpfs, Target: "/tmp", Options: "rw,nosuid"},
		},
		Env:    map[string]string{"B": "2", "A": "1"},
		Labels: map[string]string{"io.podbox.sandbox": "a"},
	}
	want := "create --name a --userns=keep-id:uid=1000,gid=1000 --user 1000:1000 --cap-drop=ALL " +
		"--security-opt no-new-privileges --http-proxy=false --cpus 4 --memory 8g --pids-limit 2048 " +
		"--network int --network eg:ip=10.0.0.2 --dns 127.0.0.1 --add-host proxy:10.0.0.2 " +
		"--volume /p:/p:z --volume /dev/null:/p/.env:ro --tmpfs /tmp:rw,nosuid " +
		"--env A=1 --env B=2 --label io.podbox.sandbox=a img sleep infinity"
	for i := 0; i < 3; i++ {
		if got := strings.Join(CreateArgs(s), " "); got != want {
			t.Fatalf("argv\n got %s\nwant %s", got, want)
		}
	}
}

func TestContainerCreateRunsPodman(t *testing.T) {
	p, log := fakePodman(t)
	if err := p.ContainerCreate(context.Background(), ContainerSpec{Name: "x", Image: "i", InheritProxyEnv: true}); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, log); got[0] != "create --name x i" {
		t.Fatalf("%q", got)
	}
}

func TestExecPropagatesExitCode(t *testing.T) {
	p, log := fakePodman(t)
	t.Setenv("FAKE_EXIT", "42")
	code, err := p.Exec(context.Background(), "x", ExecSpec{Command: []string{"false"}, Interactive: true, Workdir: "/w"})
	if err != nil || code != 42 {
		t.Fatalf("code %d err %v", code, err)
	}
	if got := recorded(t, log); got[0] != "exec --interactive --workdir /w x false" {
		t.Fatalf("%q", got)
	}
}

func TestNotFound(t *testing.T) {
	p, _ := fakePodman(t)
	t.Setenv("FAKE_EXIT", "125")
	t.Setenv("FAKE_STDERR", "Error: no such container x")
	_, err := p.ContainerInspect(context.Background(), "x")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestExists(t *testing.T) {
	p, _ := fakePodman(t)
	t.Setenv("FAKE_EXIT", "1")
	if ok, err := p.VolumeExists(context.Background(), "v"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestInfoParse(t *testing.T) {
	p, _ := fakePodman(t)
	t.Setenv("FAKE_STDOUT", `{"host":{"arch":"arm64","os":"linux","cgroupVersion":"v2","cgroupManager":"cgroupfs",
"cgroupControllers":["cpu"],"networkBackend":"netavark","security":{"rootless":true,"selinuxEnabled":false}},
"store":{"graphDriverName":"vfs"},"version":{"Version":"5.7.0"}}`)
	info, err := p.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "5.7.0" || !info.Rootless || info.NetworkBackend != "netavark" || info.StorageDriver != "vfs" {
		t.Fatalf("%+v", info)
	}
}

func TestLimitsDelegated(t *testing.T) {
	if !LimitsDelegated(Info{Remote: true}) || !LimitsDelegated(Info{Rootless: false}) {
		t.Fatal("remote or rootful engines manage limits")
	}
	if LimitsDelegated(Info{Rootless: true, CgroupVersion: "v1"}) {
		t.Fatal("cgroup v1 rootless has no delegation")
	}
	if !hasControllers("cpuset cpu io memory pids\n") || hasControllers("cpu memory") {
		t.Fatal("controller parsing")
	}
}
