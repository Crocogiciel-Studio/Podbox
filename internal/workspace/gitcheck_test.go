package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@test", "GIT_COMMITTER_NAME=a",
		"GIT_COMMITTER_EMAIL=a@test", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ws := t.TempDir()
	git(t, ws, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(ws, "a.txt"), "one\n")
	git(t, ws, "add", ".")
	git(t, ws, "commit", "-qm", "init")
	return ws
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func printOf(t *testing.T, ws string) GitPrint {
	t.Helper()
	p, err := FingerprintGit(ws)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGitNormalUsageHasNoFindings(t *testing.T) {
	ws := repo(t)
	before := printOf(t, ws)
	git(t, ws, "checkout", "-qb", "feature")
	writeFile(t, filepath.Join(ws, "a.txt"), "two\n")
	git(t, ws, "commit", "-qam", "two")
	git(t, ws, "checkout", "-q", "main")
	writeFile(t, filepath.Join(ws, "b.txt"), "b\n")
	git(t, ws, "add", "b.txt")
	git(t, ws, "commit", "-qm", "b")
	git(t, ws, "rebase", "-q", "main", "feature")
	writeFile(t, filepath.Join(ws, "a.txt"), "three\n")
	git(t, ws, "stash", "-q")
	git(t, ws, "stash", "pop", "-q")
	git(t, ws, "commit", "-qam", "three")
	git(t, ws, "tag", "v1")
	git(t, ws, "merge", "-q", "--no-ff", "-m", "merge", "main")
	git(t, ws, "reset", "-q", "--hard", "HEAD~1")
	git(t, ws, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "v1")
	git(t, ws, "gc", "-q", "--aggressive", "--prune=now")
	if f := DiffGit(before, printOf(t, ws)); len(f) != 0 {
		t.Fatalf("normal git usage produced findings: %+v", f)
	}
}

func TestGitTamperingFound(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		do         func(t *testing.T, g string)
		want       string
	}{
		{"commondir", ".git/commondir", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "commondir"), "/tmp/evil\n") }, "another git directory"},
		{"alternates", ".git/objects/info/alternates", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "objects/info/alternates"), "/tmp/x\n") }, "another repository"},
		{"config.worktree", ".git/config.worktree", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "config.worktree"), "[core]\nfsmonitor = /tmp/x\n")
		}, "per-worktree"},
		{"info", ".git/info/attributes", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "info/attributes"), "* diff=x\n") }, "changes what git reads"},
		{"unknown entry", ".git/evil", func(t *testing.T, g string) { writeFile(t, filepath.Join(g, "evil"), "x") }, "unexpected entry"},
		{"symlink outside", ".git/refs/heads/x", func(t *testing.T, g string) {
			if err := os.Symlink("/etc/passwd", filepath.Join(g, "refs/heads/x")); err != nil {
				t.Fatal(err)
			}
		}, "outside .git"},
		{"HEAD symlink", ".git/HEAD", func(t *testing.T, g string) {
			if err := os.Remove(filepath.Join(g, "HEAD")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../outside", filepath.Join(g, "HEAD")); err != nil {
				t.Fatal(err)
			}
		}, "outside .git"},
		{"submodule hook", ".git/modules/sub/hooks/post-checkout", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "modules/sub/hooks/post-checkout"), "#!/bin/sh\n")
		}, "submodule"},
		{"rebase exec", ".git/rebase-merge/git-rebase-todo", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "rebase-merge/git-rebase-todo"), "pick abc x\nexec touch /tmp/pwned\n")
		}, "exec lines"},
		{"config", ".git/config", func(t *testing.T, g string) {
			writeFile(t, filepath.Join(g, "config"), "[core]\nfsmonitor = /tmp/x\n")
		}, "core.fsmonitor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := repo(t)
			before := printOf(t, ws)
			tc.do(t, filepath.Join(ws, ".git"))
			f := DiffGit(before, printOf(t, ws))
			if len(f) != 1 || f[0].Path != tc.path || !strings.Contains(f[0].Reason, tc.want) {
				t.Fatalf("want one finding on %s containing %q, got %+v", tc.path, tc.want, f)
			}
			if f := DiffGit(printOf(t, ws), printOf(t, ws)); len(f) != 0 {
				t.Fatalf("re-recorded fingerprint still has findings: %+v", f)
			}
		})
	}
}

func TestGitDirectoryKind(t *testing.T) {
	ws := t.TempDir()
	before := printOf(t, ws)
	if before.Kind != "none" {
		t.Fatalf("kind %s", before.Kind)
	}
	writeFile(t, filepath.Join(ws, ".git", "config"), "[core]\n")
	if f := DiffGit(before, printOf(t, ws)); len(f) != 1 || f[0].Path != ".git" || !strings.Contains(f[0].Reason, "is now a directory") {
		t.Fatalf("created .git not found: %+v", f)
	}

	ws = t.TempDir()
	writeFile(t, filepath.Join(ws, ".git"), "gitdir: ../main/.git/worktrees/w\n")
	before = printOf(t, ws)
	writeFile(t, filepath.Join(ws, ".git"), "gitdir: /tmp/evil\n")
	if f := DiffGit(before, printOf(t, ws)); len(f) != 1 || f[0].Path != ".git" {
		t.Fatalf("changed .git pointer not found: %+v", f)
	}
}
