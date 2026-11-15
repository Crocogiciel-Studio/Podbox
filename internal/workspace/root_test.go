package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openRoot(t *testing.T, ws string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRootHelpersRefuseSymlinks(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "policy.yaml"), "outside\n")
	writeFile(t, filepath.Join(ws, "real", "policy.yaml"), "inside\n")
	for link, target := range map[string]string{"out": outside, "in": "real", "file": "real/policy.yaml"} {
		if err := os.Symlink(target, filepath.Join(ws, link)); err != nil {
			t.Fatal(err)
		}
	}
	r := openRoot(t, ws)
	if b, err := ReadFile(r, "real/policy.yaml", 64); err != nil || string(b) != "inside\n" {
		t.Fatalf("plain read: %q %v", b, err)
	}
	for _, rel := range []string{"out/policy.yaml", "in/policy.yaml", "file"} {
		if _, err := ReadFile(r, rel, 64); !errors.Is(err, ErrLink) {
			t.Errorf("read %s: want ErrLink, got %v", rel, err)
		}
		if err := Check(r, rel); !errors.Is(err, ErrLink) {
			t.Errorf("check %s: want ErrLink, got %v", rel, err)
		}
		if err := WriteFile(r, rel, []byte("x")); !errors.Is(err, ErrLink) {
			t.Errorf("write %s: want ErrLink, got %v", rel, err)
		}
	}
	if err := WriteFile(r, "out/new.yaml", []byte("x")); !errors.Is(err, ErrLink) {
		t.Errorf("write through a link: %v", err)
	}
	if es, _ := os.ReadDir(outside); len(es) != 1 {
		t.Fatalf("written outside the workspace: %v", es)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "policy.yaml")); string(b) != "outside\n" {
		t.Fatalf("outside file changed: %q", b)
	}
	if _, err := ReadFile(r, "real/policy.yaml", 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("cap: %v", err)
	}
	if err := WriteFile(r, "new/dir/f", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "new", "dir", "f")); string(b) != "ok" {
		t.Fatalf("write: %q", b)
	}
}

func TestMaskWalkNeverFollowsSymlinks(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "pre-commit"), "outside\n")
	if err := os.Symlink(outside, filepath.Join(ws, ".husky")); err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(ws, []byte("pre-commit\n"), nil)
	if err != nil || len(res.Masks) != 0 {
		t.Fatalf("mask walk followed a link: %+v %v", res, err)
	}
	if _, err := Lstat(openRoot(t, ws), ".husky/pre-commit"); !errors.Is(err, ErrLink) {
		t.Fatalf("lstat through a link: %v", err)
	}
}
