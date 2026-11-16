//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
	"github.com/Crocogiciel-Studio/podbox/internal/output"
	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// locked returns a directory with the given mode, restored at cleanup.
func locked(t *testing.T, mode os.FileMode) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	d := t.TempDir()
	if err := os.Chmod(d, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(d, 0o755) })
	return d
}

// TestExitCodes asserts the documented exit code (README) of each failure,
// from the error its source produces.
func TestExitCodes(t *testing.T) {
	home := t.TempDir()
	_, notFound := sandbox.Store{Root: t.TempDir()}.Load("s")
	_, unreadable := workspace.Resolve(locked(t, 0), nil, nil)
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"untyped", errors.New("boom"), output.ExitGeneric},
		{"sandbox exists", fmt.Errorf("%w: s", sandbox.ErrExists), output.ExitGeneric},
		{"sandbox not running", fmt.Errorf("%w: s", sandbox.ErrNotRunning), output.ExitGeneric},
		{"unsafe workspace", workspace.CheckRoot(home, home, nil), output.ExitUsage},
		{"workspace not a directory", fmt.Errorf("%w: /nope", sandbox.ErrBadWorkspace), output.ExitUsage},
		{"invalid rule", policy.SetRule(filepath.Join(t.TempDir(), "s.yaml"), "a*b", true, nil), output.ExitUsage},
		{"invalid port", policy.SetRule(filepath.Join(t.TempDir(), "s.yaml"), "example.com", true, []int{0}), output.ExitUsage},
		{"prerequisite", output.Wrap(output.ExitPrereq, errors.New("no podman"), "podman not found"), output.ExitPrereq},
		{"sandbox not found", notFound, output.ExitNotFound},
		{"podman failed", &runtime.CommandError{Args: []string{"run"}, Code: 125}, output.ExitRuntime},
		{"state write failed", policy.SetRule(filepath.Join(locked(t, 0o500), "s.yaml"), "example.com", true, nil), output.ExitRuntime},
		{"git-check findings", output.ExitStatus(output.ExitGitIntegrity), output.ExitGitIntegrity},
		{"unreadable workspace", unreadable, output.ExitMasks},
		{"missing masked path", workspace.MissingError("s", []string{"a.key"}), output.ExitMasks},
		{"git missing", hostgit.ErrMissing, output.ExitPrereq},
		{"git too old", fmt.Errorf("%w (found 2.20.0)", hostgit.ErrTooOld), output.ExitPrereq},
		{"not a repository", fmt.Errorf("%w: /x", hostgit.ErrNotRepo), output.ExitUsage},
		{"not the top level", fmt.Errorf("%w: /x/y", hostgit.ErrNotTopLevel), output.ExitUsage},
		{"empty repository", fmt.Errorf("%w: /x", hostgit.ErrEmptyRepo), output.ExitUsage},
		{"clone exists", fmt.Errorf("%w for s", sandbox.ErrCloneExists), output.ExitGeneric},
		{"not a clone sandbox", fmt.Errorf("%w: s", sandbox.ErrNotClone), output.ExitUsage},
		{"output too large", fmt.Errorf("%w: s", sandbox.ErrTooLarge), output.ExitGeneric},
		{"git timed out", fmt.Errorf("%w after 120s", sandbox.ErrTimeout), output.ExitGeneric},
		{"mode mismatch", fmt.Errorf("%w: s", sandbox.ErrModeMismatch), output.ExitGeneric},
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s: no error produced", c.name)
			continue
		}
		if got := output.CodeOf(classify(c.err, true)); got != c.want {
			t.Errorf("%s: exit %d, want %d (%v)", c.name, got, c.want, c.err)
		}
	}
}

// TestCommandExitCodes runs commands that need no podman.
func TestCommandExitCodes(t *testing.T) {
	h := t.TempDir()
	t.Setenv("PODBOX_HOME", h)
	st := sandbox.Store{Root: filepath.Join(h, "state", "sandboxes")}
	if err := st.Save(sandbox.Meta{Name: "s", Agent: "shell", Workspace: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"run", "nosuchagent", t.TempDir()}, output.ExitUsage},
		{[]string{"policy", "show", "nosuch"}, output.ExitNotFound},
		{[]string{"diff"}, output.ExitUsage},
		{[]string{"diff", "s", "--stat", "--show-binary"}, output.ExitUsage},
		{[]string{"policy", "allow", "nosuch", "example.com"}, output.ExitNotFound},
		{[]string{"policy", "allow", "s", "a*b"}, output.ExitUsage},
		{[]string{"policy", "allow", "s", "example.com", "--scope", "nowhere"}, output.ExitUsage},
		{[]string{"policy", "show", "s", "-o", "json"}, output.ExitOK},
	}
	for _, c := range cases {
		if got := run(c.args); got != c.want {
			t.Errorf("%v: exit %d, want %d", c.args, got, c.want)
		}
	}
	// A state directory podbox cannot write is a runtime error, not usage.
	if err := os.Chmod(st.Dir("s"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(st.Dir("s"), 0o755) })
	if got := run([]string{"policy", "allow", "s", "example.com"}); got != output.ExitRuntime {
		t.Errorf("unwritable state: exit %d, want %d", got, output.ExitRuntime)
	}
}
