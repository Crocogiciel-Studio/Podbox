package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

func TestMaskRecordFollowsRenamesUntilReload(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("no inode tracking on Windows")
	}
	ws := t.TempDir()
	st := Store{Root: t.TempDir(), ConfigDir: t.TempDir()}
	m := Meta{Name: "s", Agent: "shell", Workspace: ws}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, ".sandboxignore"), "/config/prod.yaml\n")
	write(t, filepath.Join(ws, "config/prod.yaml"), "secret")
	if _, err := st.Snapshot(m); err != nil {
		t.Fatal(err)
	}
	if res, err := st.ResolveMasks(m); err != nil || len(res.Masks) != 1 || MaskWarning(m, res) != "" {
		t.Fatalf("create: %+v %v", res, err)
	}
	if err := os.Rename(filepath.Join(ws, "config"), filepath.Join(ws, "config2")); err != nil {
		t.Fatal(err)
	}
	res, err := st.ResolveMasks(m) // the next start
	if err != nil || len(res.Masks) != 1 || res.Masks[0].Rel != "config2/prod.yaml" {
		t.Fatalf("after rename: %+v %v", res.Masks, err)
	}
	if w := MaskWarning(m, res); !strings.Contains(w, "config/prod.yaml -> config2/prod.yaml (still masked)") {
		t.Fatalf("warning: %q", w)
	}
	// The user removes the pattern and reloads: the moved record is kept
	// and still masked where it is now.
	write(t, filepath.Join(ws, ".sandboxignore"), "")
	if c, _, err := st.Reload(m, false); err != nil || len(c.MasksDropped) != 0 {
		t.Fatalf("reload after rename: %+v %v", c, err)
	}
	if res, err := st.ResolveMasks(m); err != nil || len(res.Masks) != 1 || res.Masks[0].Rel != "config2/prod.yaml" {
		t.Fatalf("after reload: %+v %v", res.Masks, err)
	}
	// Moved back to its recorded path, it is dropped, loudly.
	if err := os.Rename(filepath.Join(ws, "config2"), filepath.Join(ws, "config")); err != nil {
		t.Fatal(err)
	}
	if c, _, err := st.Reload(m, false); err != nil || strings.Join(c.MasksDropped, " ") != "config/prod.yaml" {
		t.Fatalf("reload after the move back: %+v %v", c, err)
	}
	if res, err := st.ResolveMasks(m); err != nil || len(res.Masks) != 0 {
		t.Fatalf("after reload: %+v %v", res.Masks, err)
	}
}

// TestReloadIsTransactional: a reload refused by an unreadable directory
// writes neither the snapshot nor the record nor the effective policy.
func TestReloadIsTransactional(t *testing.T) {
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced")
	}
	ws := t.TempDir()
	st := Store{Root: t.TempDir(), ConfigDir: t.TempDir()}
	m := Meta{Name: "s", Agent: "shell", Workspace: ws}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, "sub/x.txt"), "x")
	if _, err := st.Snapshot(m); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RefreshPolicy(m); err != nil {
		t.Fatal(err)
	}
	before := stateFiles(t, st.Dir(m.Name))
	write(t, filepath.Join(ws, ".sandboxignore"), "notes.txt\n")
	write(t, filepath.Join(ws, ".podbox/policy.yaml"), "rules:\n  - host: example.com\n    allow: true\n")
	if err := os.Chmod(filepath.Join(ws, "sub"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(ws, "sub"), 0o755) })
	if _, _, err := st.Reload(m, false); !errors.Is(err, workspace.ErrMasks) {
		t.Fatalf("got %v, want ErrMasks", err)
	}
	if after := stateFiles(t, st.Dir(m.Name)); after != before {
		t.Fatalf("state changed by a refused reload:\n%s\n---\n%s", before, after)
	}
	_ = os.Chmod(filepath.Join(ws, "sub"), 0o755)
	c, e, err := st.Reload(m, false)
	if err != nil || len(c.RulesAdded) != 1 || len(c.IgnoreAdded) != 1 || len(e.Rules) == 0 {
		t.Fatalf("retry: %+v %v", c, err)
	}
	if d := st.Drift(m); len(d) != 0 {
		t.Fatalf("drift after reload: %v", d)
	}
}

// stateFiles renders the files of a state directory with their content.
func stateFiles(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			fmt.Fprintf(&sb, "%s\n%s\n", p, b)
		}
		return err
	})
	return sb.String()
}

func TestMissingMaskRefusesStartUntilForgotten(t *testing.T) {
	ws := t.TempDir()
	st := Store{Root: t.TempDir(), ConfigDir: t.TempDir()}
	m := Meta{Name: "s", Agent: "shell", Workspace: ws}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, ".sandboxignore"), "/config/prod.yaml\n")
	write(t, filepath.Join(ws, "config/prod.yaml"), "secret")
	if _, err := st.Snapshot(m); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveMasks(m); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(ws, "config")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveMasks(m); !errors.Is(err, workspace.ErrMasks) || !strings.Contains(err.Error(), "  config/prod.yaml\n") {
		t.Fatalf("start with a missing mask: %v", err)
	}
	// A plain reload keeps the record, so the next start still refuses.
	if c, _, err := st.Reload(m, false); err != nil || len(c.MasksDropped) != 0 || len(c.MasksMissing) != 1 {
		t.Fatalf("plain reload: %+v %v", c, err)
	}
	if _, err := st.ResolveMasks(m); !errors.Is(err, workspace.ErrMasks) {
		t.Fatalf("start after a plain reload: %v", err)
	}
	c, _, err := st.Reload(m, true)
	if err != nil || len(c.MasksMissing) != 0 || strings.Join(c.MasksDropped, " ") != "config/prod.yaml (missing)" {
		t.Fatalf("forget-missing: %+v %v", c, err)
	}
	if _, err := st.ResolveMasks(m); err != nil {
		t.Fatalf("start after forget-missing: %v", err)
	}
}
