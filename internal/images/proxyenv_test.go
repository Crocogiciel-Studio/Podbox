package images

import (
	"maps"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

func TestBuildProxy(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	loop := map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:3128", "http_proxy": "localhost:9000",
		"NO_PROXY": "localhost,127.0.0.1",
	}
	corp := map[string]string{"HTTPS_PROXY": "http://corp:3128"}
	cases := []struct {
		goos    string
		env     map[string]string
		args    map[string]string
		network string
		noEnv   bool
	}{
		{"linux", loop, nil, "host", false},
		{"linux", corp, nil, "", false},
		{"linux", map[string]string{"NO_PROXY": "127.0.0.1"}, nil, "", false},
		{"darwin", loop, map[string]string{
			"HTTPS_PROXY": "http://host.containers.internal:3128", "http_proxy": "host.containers.internal:9000",
			"NO_PROXY": "localhost,127.0.0.1",
		}, "", true},
		{"windows", corp, corp, "", true},
		{"darwin", nil, nil, "", true},
	}
	for _, c := range cases {
		var s runtime.BuildSpec
		buildProxy(&s, c.goos, env(c.env))
		if !maps.Equal(s.BuildArgs, c.args) || s.Network != c.network || s.NoProxyEnv != c.noEnv {
			t.Errorf("%s %v: got args %v network %q noEnv %v", c.goos, c.env, s.BuildArgs, s.Network, s.NoProxyEnv)
		}
	}
	var s runtime.BuildSpec
	buildProxy(&s, "darwin", nil)
	if s.NoProxyEnv || s.BuildArgs != nil {
		t.Errorf("nil getenv changed the spec: %+v", s)
	}
}
