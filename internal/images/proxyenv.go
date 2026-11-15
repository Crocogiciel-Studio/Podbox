package images

import "github.com/Crocogiciel-Studio/podbox/internal/runtime"

// proxyVars are the host proxy variables a build needs; podman passes the
// same names as predefined build arguments, kept out of the image history.
var proxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

// buildProxy adapts spec so the build's RUN steps reach the host proxy.
//
// macOS and Windows build inside the podman machine VM, where the host's
// loopback is the VM's own: podbox passes the host variables as build
// arguments, loopback rewritten to runtime.HostAlias (NO_PROXY kept), and
// stops podman forwarding the VM's own variables.
//
// On Linux podman forwards the host variables itself, but rootless RUN
// steps still get their own network namespace (pasta), where 127.0.0.1 is
// the build container and host.containers.internal reaches only services
// listening beyond loopback. A loopback proxy therefore needs the host
// network for the build.
func buildProxy(spec *runtime.BuildSpec, goos string, getenv func(string) string) {
	if getenv == nil {
		return
	}
	if goos == "linux" {
		for _, k := range proxyVars {
			if _, loop := runtime.ToHost(getenv(k)); loop && k != "NO_PROXY" && k != "no_proxy" {
				spec.Network = "host"
				return
			}
		}
		return
	}
	for _, k := range proxyVars {
		v := getenv(k)
		if v == "" {
			continue
		}
		if k != "NO_PROXY" && k != "no_proxy" {
			v, _ = runtime.ToHost(v)
		}
		if spec.BuildArgs == nil {
			spec.BuildArgs = map[string]string{}
		}
		spec.BuildArgs[k] = v
	}
	spec.NoProxyEnv = true
}
