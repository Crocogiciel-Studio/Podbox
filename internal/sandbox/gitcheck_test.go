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
