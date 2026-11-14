package config

import (
	"path/filepath"
	"testing"
)

func fixed(s string) func() (string, error) { return func() (string, error) { return s, nil } }

func TestResolve(t *testing.T) {
	cases := []struct {
		name  string
		goos  string
		env   map[string]string
		state string
		cfg   string
	}{
		{"linux defaults", "linux", nil, "/home/u/.local/state/podbox", "/home/u/.config/podbox"},
		{"linux xdg", "linux", map[string]string{"XDG_STATE_HOME": "/s", "XDG_CONFIG_HOME": "/c"}, "/s/podbox", "/c/podbox"},
		{"relative xdg ignored", "linux", map[string]string{"XDG_STATE_HOME": "rel"}, "/home/u/.local/state/podbox", "/home/u/.config/podbox"},
		{"podbox home wins", "linux", map[string]string{"PODBOX_HOME": "/p", "XDG_STATE_HOME": "/s"}, "/p/state", "/p/config"},
		{"darwin", "darwin", nil, "/lib/podbox/state", "/lib/podbox"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := func(k string) string { return c.env[k] }
			p, err := resolve(c.goos, env, fixed("/home/u"), fixed("/lib"), fixed("/cache"))
			if err != nil {
				t.Fatal(err)
			}
			if p.State != filepath.FromSlash(c.state) || p.Config != filepath.FromSlash(c.cfg) {
				t.Fatalf("got %+v", p)
			}
		})
	}
}
