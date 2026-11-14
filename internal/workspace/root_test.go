package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestOversizedCommondirReported(t *testing.T) {
	ws := repo(t)
	before := printOf(t, ws)
	big := strings.Repeat("../", maxGitFile/3+1)
	writeFile(t, filepath.Join(ws, ".git", "commondir"), big)
	after := printOf(t, ws)
	if after.Files["commondir"] != Oversized {
		t.Fatalf("hash %q, want %q", after.Files["commondir"], Oversized)
	}
	f := DiffGit(before, after)
	if len(f) != 1 || f[0].Path != ".git/commondir" || !strings.Contains(f[0].Reason, "oversized") {
		t.Fatalf("findings %+v", f)
	}
	// Still reported once accepted: its content was never checked.
	if f := DiffGit(after, printOf(t, ws)); len(f) != 1 {
		t.Fatalf("accepted oversized file not reported: %+v", f)
	}
}

func TestFingerprintNeverFollowsSymlinks(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "pre-commit"), "outside\n")
	writeFile(t, filepath.Join(outside, "config"), "[core]\n")
	if err := os.Symlink(outside, filepath.Join(ws, ".husky")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".git")); err != nil {
		t.Fatal(err)
	}
	p := printOf(t, ws)
	if p.Kind != "symlink" || len(p.Files) != 0 {
		t.Fatalf("followed .git: %+v", p)
	}
	if len(p.Hooks) != 1 || p.Hooks[".husky"] != "link:"+outside {
		t.Fatalf("followed .husky: %v", p.Hooks)
	}
	res, err := Resolve(ws, []byte("pre-commit\n"), nil)
	if err != nil || len(res.Masks) != 0 {
		t.Fatalf("mask walk followed a link: %+v %v", res, err)
	}
	if _, err := Lstat(openRoot(t, ws), ".husky/pre-commit"); !errors.Is(err, ErrLink) {
		t.Fatalf("lstat through a link: %v", err)
	}
}
