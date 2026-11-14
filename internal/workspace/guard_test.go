package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	state := filepath.Join(home, ".local", "state", "podbox")
	for _, d := range []string{filepath.Join(home, "proj"), state, filepath.Join(base, "podbox-work"), filepath.Join(base, "real")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(name, target string) string {
		p := filepath.Join(base, name)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	homeLink := link("home-link", home)
	stateLink := link("state-link", state)
	realLink := link("real-link", filepath.Join(base, "real"))
	owned := []string{state, filepath.Join(home, ".config", "podbox"), filepath.Join(base, "podbox"), filepath.Join(realLink, "cache", "absent")}

	cases := []struct {
		name, ws string
		unsafe   bool
	}{
		{"project under home", filepath.Join(home, "proj"), false},
		{"home", home, true},
		{"home with trailing slash", home + "/", true},
		{"parent of home", base, true},
		{"filesystem root", "/", true},
		{"symlink to home", homeLink, true},
		{"state dir", state, true},
		{"inside state dir", filepath.Join(state, "sandboxes", "x"), true},
		{"contains state dir", filepath.Join(home, ".local"), true},
		{"contains absent config dir", filepath.Join(home, ".config"), true},
		{"symlink to state dir", stateLink, true},
		{"contains owned dir reached through a symlink", filepath.Join(base, "real"), true},
		{"sibling sharing a name prefix", filepath.Join(base, "podbox-work"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckRoot(c.ws, home, owned)
			if got := errors.Is(err, ErrUnsafe); got != c.unsafe {
				t.Fatalf("CheckRoot(%s) = %v, want unsafe=%v", c.ws, err, c.unsafe)
			}
		})
	}
}
