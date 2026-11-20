// Package doctor checks the host prerequisites of podbox.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

// Status of a check.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Check is one prerequisite result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Env is what the checks observe; tests replace it.
type Env struct {
	GOOS            string
	User            string
	UID             int
	LocatePodman    func() (string, error)
	Info            func(context.Context) (runtime.Info, error)
	ReadFile        func(string) ([]byte, error)
	Stat            func(string) (os.FileInfo, error)
	LimitsDelegated func(runtime.Info) bool
	// BaseImage is images.base; ImageExists looks it up locally and Pull,
	// set only by doctor --pull, fetches it.
	BaseImage   string
	ImageExists func(context.Context, string) (bool, error)
	Pull        func(context.Context, string) error
	// LocateGit reports git and its version; nil skips the git check.
	// git is optional: only --clone and export need it.
	LocateGit func() (path, version string, err error)
}

const minMajor, minMinor = 4, 4

// Run executes every check in a fixed order.
func Run(ctx context.Context, e Env) []Check {
	var cs []Check
	add := func(name, status, detail, fix string) { cs = append(cs, Check{name, status, detail, fix}) }

	bin, err := e.LocatePodman()
	if err != nil {
		add("podman", Fail, "podman not found", "install Podman 4.4+: https://podman.io/docs/installation")
		return cs
	}
	info, err := e.Info(ctx)
	if err != nil {
		fix := "check `podman info` output"
		if e.GOOS != "linux" {
			fix = "podman machine init && podman machine start"
		}
		add("podman", Fail, fmt.Sprintf("%s unusable: %v", bin, firstLine(err.Error())), fix)
		return cs
	}
	if versionAtLeast(info.Version, minMajor, minMinor) {
		add("podman", OK, fmt.Sprintf("%s %s", bin, info.Version), "")
	} else {
		add("podman", Fail, "version "+info.Version+" is older than 4.4", "upgrade Podman to 4.4 or newer")
	}
	if e.GOOS != "linux" {
		if info.Remote {
			add("machine", OK, "podman machine reachable", "")
		} else {
			add("machine", Warn, "engine is not a podman machine", "podman machine init && podman machine start")
		}
	}
	if info.Rootless {
		add("rootless", OK, "running rootless", "")
	} else {
		fix := "run podbox as a regular user, not root"
		if e.GOOS != "linux" {
			fix = "podman machine set --rootful=false && podman machine stop && podman machine start"
		}
		add("rootless", Fail, "rootless required: podman runs as root", fix)
	}
	if e.GOOS == "linux" && !info.Remote {
		for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
			if hasSubID(e.ReadFile, f, e.User, e.UID) {
				add(strings.TrimPrefix(f, "/etc/"), OK, "entry for "+e.User, "")
			} else {
				add(strings.TrimPrefix(f, "/etc/"), Fail, "no entry for "+e.User+" in "+f,
					"sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "+e.User+" && podman system migrate")
			}
		}
		if fi, err := e.Stat("/dev/net/tun"); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			add("tun", OK, "/dev/net/tun present", "")
		} else {
			add("tun", Fail, "/dev/net/tun missing: rootless networking cannot start",
				"sudo mkdir -p /dev/net && sudo mknod /dev/net/tun c 10 200 && sudo chmod 666 /dev/net/tun")
		}
	}
	if info.NetworkBackend == "netavark" {
		add("network", OK, "netavark backend", "")
	} else {
		add("network", Fail, "network backend "+info.NetworkBackend+": internal networks need netavark",
			"set network_backend=\"netavark\" in containers.conf and run podman system reset")
	}
	if e.LimitsDelegated(info) {
		add("cgroups", OK, "cpu, memory and pids controllers delegated", "")
	} else {
		add("cgroups", Warn, "resource limits unavailable: cgroup v2 cpu/memory/pids not delegated; sandboxes run without CPU/RAM/PID limits",
			"enable systemd user delegation (Delegate=cpu cpuset io memory pids) or use the systemd cgroup manager")
	}
	if info.SELinux {
		add("selinux", OK, "enforcing or permissive: workspace mounts are relabelled", "")
	} else {
		add("selinux", OK, "disabled", "")
	}
	baseImage(ctx, e, add)
	switch info.StorageDriver {
	case "overlay":
		add("storage", OK, "overlay", "")
	default:
		add("storage", Warn, info.StorageDriver+" storage driver: slow image builds and high disk usage",
			"make /dev/fuse available or install fuse-overlayfs, then podman system reset")
	}
	gitCheck(e, add)
	return cs
}

// gitCheck reports git, needed only by --clone and export: never a failure.
func gitCheck(e Env, add func(name, status, detail, fix string)) {
	if e.LocateGit == nil {
		return
	}
	path, version, err := e.LocateGit()
	switch {
	case err == nil:
		add("git", OK, path+" "+version, "")
	case errors.Is(err, hostgit.ErrTooOld):
		add("git", Warn, "git "+version+" is older than 2.29: --clone and export are unavailable", "upgrade git to 2.29 or newer")
	default:
		add("git", Warn, "git not found: --clone and export are unavailable", "install git 2.29 or newer: https://git-scm.com/downloads")
	}
}

const baseFix = "set images.base in the podbox config.yaml to an image your registry serves, " +
	"or add a mirror for its registry in registries.conf (inside the VM with podman machine)"

func baseImage(ctx context.Context, e Env, add func(name, status, detail, fix string)) {
	if e.BaseImage == "" || e.ImageExists == nil {
		return
	}
	ok, err := e.ImageExists(ctx, e.BaseImage)
	switch {
	case err != nil:
		add("base-image", Warn, "cannot look up "+e.BaseImage+": "+firstLine(err.Error()), "check `podman image exists "+e.BaseImage+"`")
	case ok:
		add("base-image", OK, e.BaseImage+" present", "")
	case e.Pull == nil:
		add("base-image", Warn, e.BaseImage+" not present: the first image build pulls it",
			"run podbox doctor --pull to check it is pullable; if not, "+baseFix)
	default:
		if err := e.Pull(ctx, e.BaseImage); err != nil {
			add("base-image", Fail, "cannot pull "+e.BaseImage+": "+firstLine(err.Error()), baseFix)
		} else {
			add("base-image", OK, e.BaseImage+" pulled", "")
		}
	}
}

// Failed reports whether any check failed.
func Failed(cs []Check) bool {
	for _, c := range cs {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

func hasSubID(read func(string) ([]byte, error), file, user string, uid int) bool {
	b, err := read(file)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && (name == user || name == strconv.Itoa(uid)) {
			return true
		}
	}
	return false
}

func versionAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
