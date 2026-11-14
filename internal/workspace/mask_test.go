package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolve(t *testing.T) {
	root := tree(t, map[string]string{
		".env":                    "A=1",
		".env.local":              "A=2",
		".ssh/id_x":               "k",
		".git/hooks/pre-commit":   "#!/bin/sh",
		".git/objects/ab":         "x",
		"src/main.go":             "package main",
		"src/server.pem":          "pem",
		"secrets/db.txt":          "pw",
		"keep/me.txt":             "ok",
		".docker/config.json":     "{}",
		".sandboxignore":          "keep/\n", // live file: ignored, the snapshot applies
		"config/api_token.json":   `{"token":"Zx8Kq2Lm9Vb4Nw7Rt1Yh6Pd3Sg5Fj0Ac"}`,
		"cert/store.p12":          "bin",
		"docs/password-reset.txt": "how to reset",
	})
	res, err := Resolve(root, []byte("secrets/\n!.env\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range res.Masks {
		s := m.Rel
		if m.Dir {
			s += "/"
		}
		got = append(got, s)
	}
	want := ".docker/config.json .env .env.local .ssh/ secrets/ src/server.pem" // .git: read-only binds, not masks
	if strings.Join(got, " ") != want {
		t.Fatalf("masks\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	w := strings.Join(res.Warnings, "\n")
	for _, s := range []string{"config/api_token.json is named like a secret and contains a high-entropy value", "cert/store.p12 has a key", "docs/password-reset.txt is named like a secret and is"} {
		if !strings.Contains(w, s) {
			t.Errorf("missing warning %q in\n%s", s, w)
		}
	}
	if strings.Contains(w, "keep/me.txt") || strings.Contains(w, "main.go") {
		t.Errorf("false positive: %s", w)
	}
}

func TestMaskLimit(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= MaxMasks; i++ {
		files[fmt.Sprintf("k/%d.key", i)] = "k"
	}
	if _, err := Resolve(tree(t, files), nil, nil); err == nil || !strings.Contains(err.Error(), "exceed the limit") {
		t.Fatal("limit not enforced")
	}
}

func TestEntropy(t *testing.T) {
	if hasHighEntropyToken("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") || !hasHighEntropyToken("key=Zx8Kq2Lm9Vb4Nw7Rt1Yh6Pd3Sg5Fj0Ac") {
		t.Fatal("entropy heuristic")
	}
}

func maskList(res Result) string {
	var got []string
	for _, m := range res.Masks {
		s := m.Rel
		if m.Dir {
			s += "/"
		}
		got = append(got, s)
	}
	return strings.Join(got, " ")
}

// TestMasksFollowRenamedParents: the F1 repro. A rename keeps the inode, so
// recorded masks follow their files when a parent directory is renamed.
func TestMasksFollowRenamedParents(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("no inode tracking on Windows")
	}
	root := tree(t, map[string]string{
		"config/prod.yaml":    "secret",
		"secret/data.txt":     "secret",
		".docker/config.json": "{}",
		".ssh/id_x":           "k",
		"src/a.go":            "package a",
	})
	ignore := []byte("/config/prod.yaml\nsecret/data.txt\n")
	first, err := Resolve(root, ignore, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := maskList(first); got != ".docker/config.json .ssh/ config/prod.yaml secret/data.txt" {
		t.Fatalf("masks: %s", got)
	}
	for _, mv := range [][2]string{{"config", "config2"}, {"secret", "s2"}, {".docker", "dk2"}, {".ssh", "keys"}} {
		if err := os.Rename(filepath.Join(root, mv[0]), filepath.Join(root, mv[1])); err != nil {
			t.Fatal(err)
		}
	}
	second, err := Resolve(root, ignore, first.Tracked)
	if err != nil {
		t.Fatal(err)
	}
	if got := maskList(second); got != "config2/prod.yaml dk2/config.json keys/ s2/data.txt" {
		t.Fatalf("masks after rename: %s", got)
	}
	if len(second.Moved) != 4 || len(second.Missing) != 0 || len(second.Tracked) != 4 {
		t.Fatalf("moved %+v missing %v tracked %d", second.Moved, second.Missing, len(second.Tracked))
	}
	// A decoy at the old path is masked as well; the record keeps the original file.
	writeFile(t, filepath.Join(root, "config/prod.yaml"), "decoy")
	third, err := Resolve(root, ignore, second.Tracked)
	if err != nil {
		t.Fatal(err)
	}
	if got := maskList(third); !strings.Contains(got, "config/prod.yaml") || !strings.Contains(got, "config2/prod.yaml") {
		t.Fatalf("masks with decoy: %s", got)
	}
	// Records never shrink on a start, only through Shrink.
	if err := os.RemoveAll(filepath.Join(root, "s2")); err != nil {
		t.Fatal(err)
	}
	fourth, err := Resolve(root, nil, third.Tracked)
	if err != nil {
		t.Fatal(err)
	}
	if len(fourth.Missing) != 1 || fourth.Missing[0] != "secret/data.txt" || !strings.Contains(maskList(fourth), "config2/prod.yaml") {
		t.Fatalf("missing %v masks %s", fourth.Missing, maskList(fourth))
	}
	kept, dropped := Shrink(fourth, nil)
	want := "config/prod.yaml (now config2/prod.yaml) secret/data.txt (missing)"
	if strings.Join(dropped, " ") != want {
		t.Fatalf("dropped %q, want %q", strings.Join(dropped, " "), want)
	}
	if len(kept) != 2 { // .docker/config.json and .ssh/ are forced
		t.Fatalf("kept %+v", kept)
	}
	if kept, _ := Shrink(fourth, ignore); len(kept) != 3 { // the rename alone never unmasks
		t.Fatalf("kept with the patterns %+v", kept)
	}
}

func TestMaskSurvivesReplacement(t *testing.T) {
	root := tree(t, map[string]string{"cfg/prod.yaml": "v1"})
	ignore := []byte("/cfg/prod.yaml\n")
	first, err := Resolve(root, ignore, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "cfg/prod.yaml")
	if err := os.Rename(p, p+".old"); err != nil { // an editor's save: new inode at the same path
		t.Fatal(err)
	}
	writeFile(t, p, "v2")
	if err := os.Remove(p + ".old"); err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(root, ignore, first.Tracked)
	if err != nil || len(second.Moved)+len(second.Missing) != 0 || maskList(second) != "cfg/prod.yaml" {
		t.Fatalf("%+v %v", second, err)
	}
}
