package gitguard

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func guardRepo(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	for _, d := range []string{".git/hooks", ".husky"} {
		if err := os.MkdirAll(filepath.Join(ws, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for f, s := range map[string]string{".git/config": "[core]\n", ".git/HEAD": "ref: refs/heads/main\n"} {
		if err := os.WriteFile(filepath.Join(ws, f), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return ws
}

func TestInactiveGuardProtectsNothing(t *testing.T) {
	ws := guardRepo(t)
	r, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	g := Guard{Clone: true}
	if g.Active() {
		t.Error("clone guard is active")
	}
	if g.ReadOnly(r) != nil || g.GitDir(r) || g.Pointer(r) || g.Notice("n", ws) != "" {
		t.Error("an inactive guard still protects")
	}
	if p, err := g.Record(ws); p != nil || err != nil {
		t.Errorf("Record = %v, %v", p, err)
	}
	if i, err := g.Check(ws, Print{}); i != nil || err != nil {
		t.Errorf("Check = %v, %v", i, err)
	}
}

func TestActiveGuardEqualsTheFunctions(t *testing.T) {
	ws := guardRepo(t)
	r, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	g := Guard{}
	if !g.Active() {
		t.Fatal("shared guard is inactive")
	}
	if got, want := g.ReadOnly(r), ReadOnly(r); !reflect.DeepEqual(got, want) || len(got) == 0 {
		t.Errorf("ReadOnly = %v, want %v", got, want)
	}
	if g.GitDir(r) != HasDir(r) || g.Pointer(r) != IsPointer(r) || g.Notice("n", ws) != Notice("n", ws) || g.Notice("n", ws) == "" {
		t.Error("an active guard differs from the functions")
	}
	p, err := g.Record(ws)
	if err != nil || p == nil {
		t.Fatalf("Record = %v, %v", p, err)
	}
	if i, err := g.Check(ws, *p); err != nil || i == nil || !i.OK {
		t.Errorf("Check = %v, %v", i, err)
	}
}
