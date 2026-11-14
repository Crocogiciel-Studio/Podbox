package sandbox

import (
	"errors"
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
	// The user removes the pattern and reloads: the record is dropped, loudly.
	write(t, filepath.Join(ws, ".sandboxignore"), "")
	if _, err := st.Snapshot(m); err != nil {
		t.Fatal(err)
	}
	dropped, _, err := st.ReloadMasks(m, false)
	if err != nil || len(dropped) != 1 || dropped[0] != "config/prod.yaml (now config2/prod.yaml)" {
		t.Fatalf("dropped %v %v", dropped, err)
	}
	if res, err := st.ResolveMasks(m); err != nil || len(res.Masks) != 0 {
		t.Fatalf("after reload: %+v %v", res.Masks, err)
	}
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
	if dropped, missing, err := st.ReloadMasks(m, false); err != nil || len(dropped) != 0 || len(missing) != 1 {
		t.Fatalf("plain reload: dropped %v missing %v %v", dropped, missing, err)
	}
	if _, err := st.ResolveMasks(m); !errors.Is(err, workspace.ErrMasks) {
		t.Fatalf("start after a plain reload: %v", err)
	}
	dropped, missing, err := st.ReloadMasks(m, true)
	if err != nil || len(missing) != 0 || strings.Join(dropped, " ") != "config/prod.yaml (missing)" {
		t.Fatalf("forget-missing: dropped %v missing %v %v", dropped, missing, err)
	}
	if _, err := st.ResolveMasks(m); err != nil {
		t.Fatalf("start after forget-missing: %v", err)
	}
}
