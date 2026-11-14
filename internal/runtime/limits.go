package runtime

import "strings"

// LimitsDelegated reports whether CPU, memory and PID limits can be applied.
// Rootless podman needs the cpu, memory and pids cgroup v2 controllers
// delegated to the user; without them every limited run fails.
func LimitsDelegated(info Info) bool {
	if info.Remote || !info.Rootless {
		return true
	}
	if info.CgroupVersion != "v2" {
		return false
	}
	return hostDelegated(info.CgroupManager)
}

func hasControllers(list string) bool {
	need := map[string]bool{"cpu": true, "memory": true, "pids": true}
	for _, c := range strings.Fields(list) {
		delete(need, c)
	}
	return len(need) == 0
}
