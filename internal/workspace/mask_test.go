package workspace

import (
	"errors"
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
	// A plain reload keeps the missing record and, without its pattern, the
	// moved one: it stays masked where it is now.
	if kept, dropped := Shrink(fourth, nil, false); len(kept) != 4 || len(dropped) != 0 {
		t.Fatalf("plain shrink: kept %+v dropped %v", kept, dropped)
	}
	kept, dropped := Shrink(fourth, nil, true)
	if strings.Join(dropped, " ") != "secret/data.txt (missing)" || len(kept) != 3 {
		t.Fatalf("forget-missing: kept %+v dropped %v", kept, dropped)
	}
	// Once moved back, a record no pattern matches is dropped.
	if err := os.Rename(filepath.Join(root, "config2/prod.yaml"), filepath.Join(root, "config/prod.yaml")); err != nil {
		t.Fatal(err)
	}
	fifth, err := Resolve(root, nil, kept)
	if err != nil {
		t.Fatal(err)
	}
	if _, dropped := Shrink(fifth, nil, false); strings.Join(dropped, " ") != "config/prod.yaml" {
		t.Fatalf("dropped %v", dropped)
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

func TestResolveRefusesUnreadableDirectories(t *testing.T) {
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced")
	}
	root := tree(t, map[string]string{"sub/.ssh/id_x": "k", "sub2/.env": "A=1", "deep/.ssh/id_y": "k"})
	first, err := Resolve(root, nil, nil)
	if err != nil || len(first.Masks) != 3 {
		t.Fatalf("%+v %v", first.Masks, err)
	}
	// An unreadable masked directory is masked without being read.
	lock(t, filepath.Join(root, "deep/.ssh"))
	if _, err := Resolve(root, nil, first.Tracked); err != nil {
		t.Fatalf("unreadable masked directory: %v", err)
	}
	for _, d := range []string{"sub", "sub2"} {
		lock(t, filepath.Join(root, d))
	}
	res, err := Resolve(root, nil, first.Tracked)
	if !errors.Is(err, ErrMasks) {
		t.Fatalf("got %v, want ErrMasks", err)
	}
	p := filepath.Join(root, "sub")
	if msg := err.Error(); !strings.Contains(msg, "cannot read "+p) || !strings.Contains(msg, "chmod u+rx") {
		t.Fatalf("message %q", msg)
	}
	if len(res.Masks) == 3 {
		t.Fatalf("partial result looks complete: %+v", res.Masks)
	}
}

// lock makes dir unreadable until the test ends.
func lock(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestRecordedPathKeptWhateverItsType(t *testing.T) {
	root := tree(t, map[string]string{"cfg/prod.yaml": "v1"})
	first, err := Resolve(root, []byte("/cfg/prod.yaml\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "cfg/prod.yaml")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(p, "inner"), "v2") // now a directory, pattern gone
	res, err := Resolve(root, nil, first.Tracked)
	if err != nil || len(res.Missing) != 0 || maskList(res) != "cfg/prod.yaml/" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMissingError(t *testing.T) {
	if MissingError("v", nil) != nil {
		t.Fatal("error without missing records")
	}
	err := MissingError("v", []string{"sub/.ssh", "sub2/.env"})
	if !errors.Is(err, ErrMasks) {
		t.Fatalf("%v is not ErrMasks", err)
	}
	for _, s := range []string{"  sub/.ssh\n", "  sub2/.env\n", "podbox policy reload v --forget-missing"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("missing %q in %q", s, err)
		}
	}
}
