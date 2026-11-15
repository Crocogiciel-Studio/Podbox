package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const cgroupRoot = "/sys/fs/cgroup"

func hostDelegated(manager string) bool {
	if manager == "systemd" {
		uid := os.Getuid()
		b, err := os.ReadFile(fmt.Sprintf("%s/user.slice/user-%d.slice/user@%d.service/cgroup.controllers", cgroupRoot, uid, uid))
		return err == nil && hasControllers(string(b))
	}
	// cgroupfs: podman creates the container cgroup under our own cgroup.
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			dir := filepath.Join(cgroupRoot, rel)
			c, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
			return err == nil && hasControllers(string(c)) && syscall.Access(dir, 2) == nil
		}
	}
	return false
}
