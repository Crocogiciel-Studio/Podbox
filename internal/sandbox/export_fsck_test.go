package sandbox

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
)

// evilCommit makes a commit in the clone, on top of HEAD, whose tree holds the
// given entries (name to content), written as raw objects so git does not
// refuse the names.
func (f *fixture) evilCommit(entries map[string]string) {
	f.t.Helper()
	var tree strings.Builder
	// git wants a tree sorted by name; one entry per call needs no sorting, several are sorted here
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		blob := sh(f.t, f.clone, "git hash-object -w --stdin", entries[n])
		id, _ := hex.DecodeString(blob)
		tree.WriteString("100644 " + n + "\x00" + string(id))
	}
	obj := sh(f.t, f.clone, "git hash-object -w -t tree --literally --stdin", tree.String())
	sh(f.t, f.clone, "c=$(git commit-tree "+obj+" -p HEAD -m evil); git update-ref HEAD $c")
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestExportObjectChecks(t *testing.T) {
	cases := map[string]map[string]string{
		".git":            {".git": "x"},
		".GIT":            {".GIT": "x"},
		".git space":      {".git ": "x"},
		".git dot":        {".git.": "x"},
		"git~1":           {"git~1": "x"},
		"dotdot":          {"..": "x"},
		"dot":             {".": "x"},
		"gitmodules path": {".gitmodules": "[submodule \"x\"]\n\tpath = -evil\n\turl = https://example.org/x\n"},
		"gitmodules url":  {".gitmodules": "[submodule \"x\"]\n\tpath = x\n\turl = -u./payload\n"},
		"gitmodules name": {".gitmodules": "[submodule \"../x\"]\n\tpath = x\n\turl = https://example.org/x\n"},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("a.txt", "x")
			f.evilCommit(entries)
			_, err := f.export(ExportOptions{As: AsBranch})
			if !errors.Is(err, hostgit.ErrObjectCheck) || errors.Is(err, hostgit.ErrBundleVerify) {
				t.Errorf("not refused by the object checks: %v", err)
			}
			if hostGit(t, f.host, "branch", "--list") != "* main" {
				t.Error("a branch was created")
			}
			f.noLeftovers()
		})
	}
}

// A repository-local fsck setting of the host cannot switch off the checks that matter.
func TestExportObjectChecksIgnoreLocalSettings(t *testing.T) {
	f := newFixture(t)
	skip := filepath.Join(t.TempDir(), "skip")
	f.commit("a.txt", "x")
	f.evilCommit(map[string]string{".git": "x"})
	bad := hostGit(t, f.clone, "rev-parse", "HEAD^{tree}")
	os.WriteFile(skip, []byte(bad+"\n"), 0o600)
	hostGit(t, f.host, "config", "fetch.fsck.hasDotgit", "ignore")
	hostGit(t, f.host, "config", "fsck.hasDotgit", "ignore")
	hostGit(t, f.host, "config", "fetch.fsck.skipList", skip)
	hostGit(t, f.host, "config", "transfer.fsck.skipList", skip)
	_, err := f.export(ExportOptions{As: AsBranch})
	if !errors.Is(err, hostgit.ErrObjectCheck) {
		t.Errorf("host-local fsck settings weakened the import: %v", err)
	}
}
