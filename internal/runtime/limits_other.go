//go:build !linux

package runtime

// hostDelegated is only reached for a local rootless engine, i.e. Linux.
func hostDelegated(string) bool { return true }
