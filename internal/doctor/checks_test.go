package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

type charDev struct{}

func (charDev) Name() string       { return "tun" }
func (charDev) Size() int64        { return 0 }
func (charDev) Mode() fs.FileMode  { return fs.ModeDevice | fs.ModeCharDevice }
func (charDev) ModTime() time.Time { return time.Time{} }
func (charDev) IsDir() bool        { return false }
func (charDev) Sys() any           { return nil }

func healthy() (Env, *runtime.Info) {
	info := &runtime.Info{Version: "5.7.0", Rootless: true, NetworkBackend: "netavark", CgroupVersion: "v2", StorageDriver: "overlay"}
	return Env{
		GOOS: "linux", User: "dev", UID: 1000,
		LocatePodman:    func() (string, error) { return "/usr/bin/podman", nil },
		Info:            func(context.Context) (runtime.Info, error) { return *info, nil },
		ReadFile:        func(string) ([]byte, error) { return []byte("dev:100000:65536\n"), nil },
		Stat:            func(string) (os.FileInfo, error) { return charDev{}, nil },
		LimitsDelegated: func(runtime.Info) bool { return true },
	}, info
}

func find(t *testing.T, cs []Check, name string) Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing in %+v", name, cs)
	return Check{}
}

func TestHealthy(t *testing.T) {
	e, _ := healthy()
	cs := Run(context.Background(), e)
	if Failed(cs) {
		t.Fatalf("%+v", cs)
	}
	for _, c := range cs {
		if c.Status != OK {
			t.Errorf("%s: %s", c.Name, c.Status)
		}
	}
}

func TestGitIsOptional(t *testing.T) {
	cases := []struct {
		name   string
		locate func() (string, string, error)
		want   string
	}{
		{"present", func() (string, string, error) { return "/usr/bin/git", "2.45.0", nil }, OK},
		{"missing", func() (string, string, error) { return "", "", hostgit.ErrMissing }, Warn},
		{"too old", func() (string, string, error) { return "/usr/bin/git", "2.20.1", hostgit.ErrTooOld }, Warn},
	}
	for _, c := range cases {
		e, _ := healthy()
		e.LocateGit = c.locate
		cs := Run(context.Background(), e)
		if got := find(t, cs, "git"); got.Status != c.want {
			t.Errorf("%s: git is %s, want %s", c.name, got.Status, c.want)
		}
		if Failed(cs) {
			t.Errorf("%s: git alone must not fail doctor: %+v", c.name, cs)
		}
	}
	e, _ := healthy()
	for _, c := range Run(context.Background(), e) {
		if c.Name == "git" {
			t.Error("git check runs without LocateGit")
		}
	}
}

func TestPodmanMissing(t *testing.T) {
	e, _ := healthy()
	e.LocatePodman = func() (string, error) { return "", errors.New("not in PATH") }
	cs := Run(context.Background(), e)
	if c := find(t, cs, "podman"); c.Status != Fail || c.Fix == "" || !Failed(cs) {
		t.Fatalf("%+v", c)
	}
}

func TestRootful(t *testing.T) {
	e, info := healthy()
	info.Rootless = false
	if c := find(t, Run(context.Background(), e), "rootless"); c.Status != Fail || c.Detail != "rootless required: podman runs as root" {
		t.Fatalf("%+v", c)
	}
}

func TestNoDelegation(t *testing.T) {
	e, _ := healthy()
	e.LimitsDelegated = func(runtime.Info) bool { return false }
	cs := Run(context.Background(), e)
	if c := find(t, cs, "cgroups"); c.Status != Warn {
		t.Fatalf("%+v", c)
	}
	if Failed(cs) {
		t.Fatal("a warning must not fail doctor")
	}
}

func TestOldVersionAndMissingSubID(t *testing.T) {
	e, info := healthy()
	info.Version = "4.3.1"
	e.ReadFile = func(string) ([]byte, error) { return []byte("other:1:2\n"), nil }
	cs := Run(context.Background(), e)
	if find(t, cs, "podman").Status != Fail || find(t, cs, "subuid").Status != Fail {
		t.Fatalf("%+v", cs)
	}
}

func TestVersionAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"4.4.0": true, "5.0": true, "4.3.9": false, "3.9": false, "x": false} {
		if versionAtLeast(v, 4, 4) != want {
			t.Errorf("%s", v)
		}
	}
}

func TestBaseImage(t *testing.T) {
	pullErr := errors.New("pull docker.io/x: connection refused")
	cases := []struct {
		name    string
		exists  bool
		pull    func(context.Context, string) error
		status  string
		pulled  bool
		wantFix bool
	}{
		{"present", true, nil, OK, false, false},
		{"absent offline", false, nil, Warn, false, true},
		{"absent pulled", false, func(context.Context, string) error { return nil }, OK, true, false},
		{"absent unpullable", false, func(context.Context, string) error { return pullErr }, Fail, true, true},
	}
	for _, c := range cases {
		e, _ := healthy()
		pulled := false
		e.BaseImage = "docker.io/x"
		e.ImageExists = func(_ context.Context, ref string) (bool, error) { return c.exists && ref == "docker.io/x", nil }
		if c.pull != nil {
			e.Pull = func(ctx context.Context, ref string) error { pulled = true; return c.pull(ctx, ref) }
		}
		got := find(t, Run(context.Background(), e), "base-image")
		if got.Status != c.status || pulled != c.pulled || (got.Fix != "") != c.wantFix {
			t.Errorf("%s: %+v pulled=%v", c.name, got, pulled)
		}
		if c.status == Fail && !strings.Contains(got.Fix, "images.base") {
			t.Errorf("%s: fix lacks images.base: %q", c.name, got.Fix)
		}
	}
}
