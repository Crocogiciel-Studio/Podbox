package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func sampleDiff(patch bool) diffResult {
	res := diffResult{Name: "s", Base: strings.Repeat("a", 40), Files: []sandbox.FileChange{
		{Path: "a.txt", Status: "modified", Added: 2, Deleted: 1},
		{Path: "new.txt", Status: "added", Added: 1, Untracked: true},
		{Path: "img.png", Status: "added", Binary: true},
		{Path: "new name", OldPath: "old name", Status: "renamed"},
	}}
	if patch {
		p := "diff --git a/a.txt b/a.txt\n+x\n"
		res.Patch = &p
	}
	return res
}

func TestWriteDiffText(t *testing.T) {
	var b bytes.Buffer
	writeDiff(&b, sampleDiff(true))
	out := b.String()
	for _, want := range []string{"STATUS", "untracked", "old name -> new name", "4 file(s) changed, +3 -1 since aaaaaaaaaaaa", "\ndiff --git a/a.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	b.Reset()
	writeDiff(&b, sampleDiff(false))
	if strings.Contains(b.String(), "diff --git") {
		t.Errorf("--stat printed a patch:\n%s", b.String())
	}
	b.Reset()
	writeDiff(&b, diffResult{Base: strings.Repeat("b", 40), Files: []sandbox.FileChange{}})
	if b.String() != "no changes since bbbbbbbbbbbb\n" {
		t.Errorf("empty: %q", b.String())
	}
}

func TestDiffJSONShape(t *testing.T) {
	enc := func(r diffResult) map[string]any {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	m := enc(sampleDiff(true))
	if _, ok := m["patch"]; !ok {
		t.Error("patch missing")
	}
	if _, ok := enc(sampleDiff(false))["patch"]; ok {
		t.Error("--stat has a patch")
	}
	empty := enc(diffResult{Name: "s", Base: "x", Files: []sandbox.FileChange{}})
	if f, ok := empty["files"].([]any); !ok || len(f) != 0 {
		t.Errorf("files of an empty diff: %v", empty["files"])
	}
	f := m["files"].([]any)[0].(map[string]any)
	for _, k := range []string{"path", "status", "added", "deleted", "binary", "untracked"} {
		if _, ok := f[k]; !ok {
			t.Errorf("file key %q missing", k)
		}
	}
}
