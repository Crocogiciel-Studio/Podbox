package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func sampleDiff(patch bool) sandbox.DiffResult {
	res := sandbox.DiffResult{Schema: sandbox.DiffSchema, Name: "s", Base: strings.Repeat("a", 40), Files: []sandbox.FileChange{
		{Path: "a.txt", Status: "modified", Added: 2, Deleted: 1},
		{Path: "new.txt", Status: "added", Added: 1, Untracked: true},
		{Path: "img.png", Status: "added", Binary: true},
		{Path: "code.py", Status: "added", Added: 3, BinaryAttr: true},
		{Path: "new name", OldPath: "old name", Status: "renamed"},
		{Path: "sub", Status: "added", Gitlink: true},
		{Path: "ln", Status: "added", Symlink: true},
	}, Ignored: 3, NameNotes: sandbox.NameNotes{CaseCollisions: 2, CaseExamples: []string{"A", "a"}, NonASCII: 1, NonASCIIExamples: []string{"café"}}}
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
	for _, want := range []string{"STATUS", "untracked", "old name -> new name", "7 file(s) changed, +6 -1 since aaaaaaaaaaaa",
		"nested repository", "[symlink]", "contains NUL bytes, not shown", "git attributes mark it binary; shown as text",
		"3 ignored untracked path(s) not shown", "2 path(s) differ only by case and collide", "1 path(s) have non-ASCII characters", "café", "\ndiff --git a/a.txt"} {
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
	writeDiff(&b, sandbox.DiffResult{Base: strings.Repeat("b", 40), Files: []sandbox.FileChange{}})
	if b.String() != "no changes since bbbbbbbbbbbb\n" {
		t.Errorf("empty: %q", b.String())
	}
}

func TestDiffJSONShape(t *testing.T) {
	enc := func(r sandbox.DiffResult) map[string]any {
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
	empty := enc(sandbox.DiffResult{Name: "s", Base: "x", Files: []sandbox.FileChange{}})
	if f, ok := empty["files"].([]any); !ok || len(f) != 0 {
		t.Errorf("files of an empty diff: %v", empty["files"])
	}
	if m["schema"] != float64(1) || m["ignored"] != float64(3) || m["case_collisions"] != float64(2) || m["non_ascii"] != float64(1) {
		t.Errorf("top level %v", m)
	}
	f := m["files"].([]any)[0].(map[string]any)
	for _, k := range []string{"path", "path_b64", "status", "added", "deleted", "binary", "binary_attr", "untracked", "gitlink", "symlink", "exec", "mode"} {
		if _, ok := f[k]; !ok {
			t.Errorf("file key %q missing", k)
		}
	}
}
