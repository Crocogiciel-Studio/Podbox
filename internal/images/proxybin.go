package images

import (
	"debug/elf"
	"fmt"
	"os"
	goruntime "runtime"
)

// embedded holds linux proxy binaries by arch in embedproxy builds.
var embedded = map[string][]byte{}

// ProxyBinary returns a static linux podbox binary for arch: the running
// executable when it fits, else one embedded at build time.
func ProxyBinary(arch string) ([]byte, error) {
	if goruntime.GOOS == "linux" && arch == goruntime.GOARCH {
		if exe, err := os.Executable(); err == nil && isStatic(exe) {
			return os.ReadFile(exe)
		}
	}
	if b := embedded[arch]; len(b) > 0 {
		return b, nil
	}
	return nil, fmt.Errorf("no static linux/%s podbox binary available for the proxy image: "+
		"use a release build or build one with `make cross-embed` (CGO_ENABLED=0)", arch)
}

// isStatic reports whether an ELF file has no dynamic loader.
func isStatic(path string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return false
		}
	}
	return true
}
