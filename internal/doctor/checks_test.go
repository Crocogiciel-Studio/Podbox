package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

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
