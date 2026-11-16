package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/sandbox"
)

func sampleExport(as string) sandbox.ExportResult {
	res := sandbox.ExportResult{Schema: sandbox.ExportSchema, Name: "s", As: as, Exported: true, Base: strings.Repeat("a", 40), Head: strings.Repeat("b", 40),
		Commits: 22, FilesChanged: 3, HostVerified: as == "branch",
		CommitList:    []sandbox.CommitLine{{ID: strings.Repeat("c", 40), Subject: "fix it"}},
		NotExported:   sandbox.NotExported{Uncommitted: 1, Untracked: 2, UncommittedFiles: []string{"a.txt"}, UntrackedFiles: []string{"u1", "u2"}},
		MaskedTouched: []string{".env"}, MaskedTouchedB64: []string{"LmVudg=="},
		Warnings: []sandbox.ExportWarning{{Kind: "symlink", Detail: "a symlink can point outside", Count: 1, Examples: []string{"link"}}},
	}
	if as == "branch" {
		res.Branch = "podbox/s/main"
	} else {
		res.File = "/tmp/s.patch"
	}
	return res
}

func TestWriteExportText(t *testing.T) {
	var b bytes.Buffer
	writeExport(&b, sampleExport("branch"))
	out := b.String()
	for _, want := range []string{"created branch podbox/s/main with 22 commit(s)", "cccccccccccc fix it", "... 21 more", "3 file(s) changed", "WARNING: --include-masked", ".env",
		"WARNING: the commits contain things", "symlink (1)", "podbox diff and git diff", "not exported: 1 uncommitted, 2 untracked", "uncommitted  a.txt", "untracked    u2", "nothing was checked out or merged"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	b.Reset()
	writeExport(&b, sampleExport("patch"))
	if !strings.Contains(b.String(), "wrote /tmp/s.patch") || !strings.Contains(b.String(), "only the base commit was checked on the host") {
		t.Errorf("patch:\n%s", b.String())
	}
	b.Reset()
	writeExport(&b, sandbox.ExportResult{Name: "s", Base: strings.Repeat("a", 40), NotExported: sandbox.NotExported{Uncommitted: 2}})
	if !strings.Contains(b.String(), "nothing to export: s has no commit since aaaaaaaaaaaa") || !strings.Contains(b.String(), "not exported: 2 uncommitted") || strings.Contains(b.String(), "created") {
		t.Errorf("empty:\n%s", b.String())
	}
}

func TestExportJSONShape(t *testing.T) {
	enc := func(r sandbox.ExportResult) map[string]any {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	m := enc(sampleExport("branch"))
	for _, k := range []string{"schema", "name", "as", "exported", "branch", "base", "head", "commits", "commit_list", "files_changed", "host_verified", "not_exported", "masked_touched", "masked_touched_b64", "warnings"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing", k)
		}
	}
	if _, ok := m["file"]; ok {
		t.Error("branch export has a file")
	}
	if _, ok := enc(sampleExport("patch"))["branch"]; ok {
		t.Error("patch export has a branch")
	}
	ne := m["not_exported"].(map[string]any)
	if ne["uncommitted"] != float64(1) || ne["untracked"] != float64(2) {
		t.Errorf("not_exported %v", ne)
	}
	empty := enc(sandbox.ExportResult{Name: "s", CommitList: []sandbox.CommitLine{}, Warnings: []sandbox.ExportWarning{}, MaskedTouched: []string{}})
	if w, ok := empty["warnings"].([]any); !ok || len(w) != 0 || empty["exported"] != false {
		t.Errorf("empty: %v", empty)
	}
}
