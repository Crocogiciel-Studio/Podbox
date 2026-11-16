package hostgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, _, err := Available(); err != nil {
		t.Skip(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "commit.gpgsign=false"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	write(t, filepath.Join(dir, ".env"), "SECRET=1\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "one")
	return dir
}

func TestEnvDropsGitVariables(t *testing.T) {
	got := env([]string{"PATH=/bin", "GIT_DIR=/x", "GIT_CONFIG_COUNT=1", "HOME=/h", "GIT_SSH_COMMAND=sh"})
	for _, kv := range got {
		if strings.HasPrefix(kv, "GIT_") && kv != "GIT_TERMINAL_PROMPT=0" {
			t.Errorf("kept %s", kv)
		}
	}
	has := map[string]bool{}
	for _, kv := range got {
		has[kv] = true
	}
	for _, want := range []string{"PATH=/bin", "HOME=/h", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"} {
		if !has[want] {
			t.Errorf("missing %s", want)
		}
	}
}

func TestRunIgnoresInheritedGitDir(t *testing.T) {
	needGit(t)
	dir := repo(t)
	t.Setenv("GIT_DIR", t.TempDir()) // would point git at an empty directory
	if _, err := Run(context.Background(), dir, nil, "rev-parse", "HEAD"); err != nil {
		t.Fatalf("GIT_DIR leaked into the command: %v", err)
	}
}

func TestRunCapsOutput(t *testing.T) {
	needGit(t)
	dir := repo(t)
	_, err := run(context.Background(), dir, 5, nil, nil, "log")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestInspect(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	dir := repo(t)
	write(t, filepath.Join(dir, "a.txt"), "edited\n")
	write(t, filepath.Join(dir, "new.txt"), "x\n")
	write(t, filepath.Join(dir, "sub", ".env.local"), "x\n")
	write(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	write(t, filepath.Join(dir, "ignored.txt"), "x\n")
	r, err := Inspect(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Branch != "main" || len(r.Head) != 40 || r.Uncommitted != 1 || r.Untracked != 3 {
		t.Errorf("repo = %+v", r)
	}
	head := r.Head
	git(t, dir, "checkout", "-q", "--detach")
	if r, err = Inspect(ctx, dir); err != nil || r.Branch != "" || r.Head != head {
		t.Errorf("detached: %+v %v", r, err)
	}
}

func TestInspectSentinels(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	if _, err := Inspect(ctx, t.TempDir()); !errors.Is(err, ErrNotRepo) {
		t.Errorf("plain dir: %v", err)
	}
	empty := t.TempDir()
	git(t, empty, "init", "-q")
	if _, err := Inspect(ctx, empty); !errors.Is(err, ErrEmptyRepo) {
		t.Errorf("empty: %v", err)
	}
	dir := repo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, sub); !errors.Is(err, ErrNotTopLevel) {
		t.Errorf("subdir: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, link); err != nil {
		t.Errorf("symlinked top level: %v", err)
	}
}

func TestClone(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	src := repo(t)
	git(t, src, "remote", "add", "origin", "https://example.org/x.git")
	git(t, src, "config", "user.email", "host@example.org")
	write(t, filepath.Join(src, ".git", "hooks", "pre-commit"), "#!/bin/sh\nexit 1\n")
	// a dangling blob that must not reach the clone
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = src
	cmd.Stdin = strings.NewReader("dangling secret\n")
	blob, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "untracked.txt"), "x\n")
	r, err := Inspect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "clones", "x")
	if err := Clone(ctx, src, dest, r.Head, r.Branch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(PartialDir(dest)); err == nil {
		t.Error("partial left")
	}
	if _, err := os.Stat(filepath.Join(dest, "untracked.txt")); err == nil {
		t.Error("untracked file carried")
	}
	if b, _ := os.ReadFile(filepath.Join(dest, ".env")); string(b) != "SECRET=1\n" {
		t.Error("tracked file missing")
	}
	cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config"))
	for _, bad := range []string{"origin", "example.org", "host@", "url"} {
		if strings.Contains(string(cfg), bad) {
			t.Errorf("config holds %q:\n%s", bad, cfg)
		}
	}
	if es, _ := os.ReadDir(filepath.Join(dest, ".git", "hooks")); len(es) != 0 {
		t.Errorf("hooks carried: %v", es)
	}
	if _, err := os.Stat(filepath.Join(dest, ".git", "objects", "info", "alternates")); err == nil {
		t.Error("alternates present")
	}
	if err := exec.Command("git", "-C", dest, "cat-file", "-e", strings.TrimSpace(string(blob))).Run(); err == nil {
		t.Error("dangling blob reached the clone")
	}
	filepath.WalkDir(filepath.Join(dest, ".git", "objects"), func(p string, d os.DirEntry, _ error) error {
		if d != nil && d.Type().IsRegular() {
			fi, _ := d.Info()
			if n := linkCount(fi); n > 1 {
				t.Errorf("%s has %d links", p, n)
			}
		}
		return nil
	})
	if got := git(t, dest, "rev-parse", "HEAD"); got != r.Head {
		t.Errorf("HEAD %s", got)
	}
}

func TestCloneDetached(t *testing.T) {
	needGit(t)
	src := repo(t)
	write(t, filepath.Join(src, "b.txt"), "b\n")
	git(t, src, "add", ".")
	git(t, src, "commit", "-q", "-m", "two")
	git(t, src, "checkout", "-q", "--detach", "HEAD~1")
	r, err := Inspect(context.Background(), src)
	if err != nil || r.Branch != "" {
		t.Fatalf("%+v %v", r, err)
	}
	dest := filepath.Join(t.TempDir(), "c")
	if err := Clone(context.Background(), src, dest, r.Head, ""); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != r.Head {
		t.Errorf("HEAD %s want %s", got, r.Head)
	}
	if _, err := os.Stat(filepath.Join(dest, "b.txt")); err == nil {
		t.Error("later commit carried")
	}
}

func TestCloneFailureLeavesNothing(t *testing.T) {
	needGit(t)
	dest := filepath.Join(t.TempDir(), "c")
	if err := Clone(context.Background(), t.TempDir(), dest, "abc", "main"); err == nil {
		t.Fatal("clone of a non-repository succeeded")
	}
	for _, p := range []string{dest, PartialDir(dest)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s left", p)
		}
	}
}

func TestFileURL(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/a b%41#c": "file:///tmp/a%20b%2541%23c",
		"/tmp/plain":    "file:///tmp/plain",
		"C:/x/y":        "file:///C:/x/y",
	} {
		if got := fileURL(in); got != want {
			t.Errorf("fileURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCloneOfAnAwkwardPath(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "my repo %41 #1")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, src, "init", "-q", "-b", "main")
	write(t, filepath.Join(src, "a.txt"), "a\n")
	git(t, src, "add", ".")
	git(t, src, "commit", "-q", "-m", "one")
	r, err := Inspect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "c")
	if err := Clone(ctx, src, dest, r.Head, r.Branch); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "a\n" {
		t.Error("wrong content")
	}
}

// The host's global and system git configuration must neither redirect nor
// refuse the clone.
func TestCloneIgnoresHostGitConfig(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	src := repo(t)
	r, err := Inspect(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	write(t, filepath.Join(home, ".gitconfig"), "[url \"https://nowhere.invalid/\"]\n\tinsteadOf = file://\n[protocol \"file\"]\n\tallow = never\n[init]\n\ttemplateDir = /nonexistent\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	dest := filepath.Join(t.TempDir(), "c")
	if err := Clone(ctx, src, dest, r.Head, r.Branch); err != nil {
		t.Fatalf("host configuration reached the clone: %v", err)
	}
	if got := git(t, dest, "rev-parse", "HEAD"); got != r.Head {
		t.Errorf("HEAD %s", got)
	}
}
