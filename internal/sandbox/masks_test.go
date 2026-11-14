package sandbox

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
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
	dropped, err := st.ReloadMasks(m)
	if err != nil || len(dropped) != 1 || dropped[0] != "config/prod.yaml (now config2/prod.yaml)" {
		t.Fatalf("dropped %v %v", dropped, err)
	}
	if res, err := st.ResolveMasks(m); err != nil || len(res.Masks) != 0 {
		t.Fatalf("after reload: %+v %v", res.Masks, err)
	}
}
