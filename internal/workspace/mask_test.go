package workspace

import (
	"fmt"
	"os"
	"path/filepath"
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
	res, err := Resolve(root, []byte("secrets/\n!.env\n"))
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
	want := ".docker/config.json .env .env.local .git/hooks/ .ssh/ secrets/ src/server.pem"
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
	if _, err := Resolve(tree(t, files), nil); err == nil || !strings.Contains(err.Error(), "exceed the limit") {
		t.Fatal("limit not enforced")
	}
}

func TestEntropy(t *testing.T) {
	if hasHighEntropyToken("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") || !hasHighEntropyToken("key=Zx8Kq2Lm9Vb4Nw7Rt1Yh6Pd3Sg5Fj0Ac") {
		t.Fatal("entropy heuristic")
	}
}
