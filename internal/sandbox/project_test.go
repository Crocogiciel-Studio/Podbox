package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func allows(t *testing.T, st Store, m Meta, host string) bool {
	t.Helper()
	e, err := st.RefreshPolicy(m)
	if err != nil {
		t.Fatal(err)
	}
	return e.Decide(host).Allow
}

func TestSnapshotIgnoresLiveEditsUntilReload(t *testing.T) {
	ws := t.TempDir()
	st := Store{Root: t.TempDir(), ConfigDir: t.TempDir()}
	m := Meta{Name: "s", Agent: "shell", Workspace: ws}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	write(t, policy.ProjectFile(ws), "rules:\n  - host: good.test\n    allow: true\n")
	write(t, filepath.Join(ws, ".sandboxignore"), "secrets/\n")
	if c, err := st.Snapshot(m); err != nil || !reflect.DeepEqual(c.RulesAdded, []string{"allow good.test"}) {
		t.Fatalf("initial snapshot %+v %v", c, err)
	}
	if d := st.Drift(m); len(d) != 0 {
		t.Fatalf("drift right after snapshot: %v", d)
	}

	// The agent rewrites the project files.
	write(t, policy.ProjectFile(ws), "rules:\n  - host: good.test\n    allow: true\n  - host: evil.test\n    allow: true\n")
	write(t, filepath.Join(ws, ".sandboxignore"), "# nothing\n")
	if allows(t, st, m, "evil.test") || !allows(t, st, m, "good.test") {
		t.Fatal("recompilation read the live project file")
	}
	if ig, _ := os.ReadFile(st.ProjectIgnorePath(m.Name)); string(ig) != "secrets/\n" {
		t.Fatalf("ignore snapshot changed: %q", ig)
	}
	if d := st.Drift(m); !reflect.DeepEqual(d, []string{ProjectPolicyRel, ".sandboxignore"}) {
		t.Fatalf("drift %v", d)
	}

	// The human reviews and reloads.
	c, err := st.Snapshot(m)
	if err != nil {
		t.Fatal(err)
	}
	want := Changes{RulesAdded: []string{"allow evil.test"}, RulesRemoved: []string{}, IgnoreAdded: []string{}, IgnoreRemoved: []string{"secrets/"}}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("changes\n got %+v\nwant %+v", c, want)
	}
	if !allows(t, st, m, "evil.test") || len(st.Drift(m)) != 0 {
		t.Fatal("reload not applied")
	}

	// Removing the files removes the snapshot.
	os.RemoveAll(filepath.Join(ws, ".podbox"))
	if c, _ := st.Snapshot(m); len(c.RulesRemoved) != 2 || allows(t, st, m, "good.test") {
		t.Fatalf("removal not applied: %+v", c)
	}
}

func TestSnapshotRefusesSymlinks(t *testing.T) {
	ws, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "policy.yaml"), "rules:\n  - host: evil.test\n    allow: true\n")
	if err := os.Symlink(outside, filepath.Join(ws, ".podbox")); err != nil {
		t.Fatal(err)
	}
	st := Store{Root: t.TempDir()}
	m := Meta{Name: "s", Agent: "shell", Workspace: ws}
	c, err := st.Snapshot(m)
	if err != nil || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "symbolic link") {
		t.Fatalf("%+v %v", c, err)
	}
	if allows(t, st, m, "evil.test") {
		t.Fatal("symlinked policy applied")
	}
	if ro := readOnlyProject(ws); len(ro) != 0 {
		t.Fatalf("symlink mounted read-only: %v", ro)
	}
}

func TestAgentSpecReadOnlyProjectFiles(t *testing.T) {
	ws := t.TempDir()
	write(t, filepath.Join(ws, ".sandboxignore"), "x\n")
	ro := readOnlyProject(ws)
	if !reflect.DeepEqual(ro, []string{".sandboxignore"}) {
		t.Fatalf("read-only paths %v (absent .podbox must not be created or mounted)", ro)
	}
	if _, err := os.Stat(filepath.Join(ws, ".podbox")); err == nil {
		t.Fatal(".podbox created on the host")
	}
	s := agentSpec(agentInput{Meta: Meta{Name: "t", Agent: "shell", Workspace: "/p"}, GOOS: "linux", ReadOnly: []string{".podbox", ".sandboxignore"}})
	argv := strings.Join(runtime.CreateArgs(s), " ")
	for _, want := range []string{"--volume /p/.podbox:/p/.podbox:ro", "--volume /p/.sandboxignore:/p/.sandboxignore:ro"} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %q in %s", want, argv)
		}
	}
}

func TestGitConfigReadOnly(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "config"), "[core]\n")
	if ro := readOnlyProject(repo); !reflect.DeepEqual(ro, []string{".git/config"}) || gitPointer(repo) {
		t.Fatalf("git dir: read-only %v, pointer %v", ro, gitPointer(repo))
	}
	if !gitDir(repo) {
		t.Fatal(".git directory not detected")
	}
	s := agentSpec(agentInput{Meta: Meta{Name: "t", Agent: "shell", Workspace: "/p"}, GOOS: "linux", ReadOnly: []string{".git/config"}, GitDir: true})
	argv := strings.Join(runtime.CreateArgs(s), " ")
	for _, want := range []string{"--volume /p/.git:/p/.git ", "--volume /p/.git/config:/p/.git/config:ro"} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %q in %s", want, argv)
		}
	}

	worktree := t.TempDir()
	write(t, filepath.Join(worktree, ".git"), "gitdir: "+filepath.Join(repo, ".git")+"\n")
	if ro := readOnlyProject(worktree); len(ro) != 0 || !gitPointer(worktree) || gitDir(worktree) {
		t.Fatalf("git file: read-only %v, pointer %v", ro, gitPointer(worktree))
	}

	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(linked, ".git")); err != nil {
		t.Fatal(err)
	}
	if ro := readOnlyProject(linked); len(ro) != 0 || gitDir(linked) {
		t.Fatalf("symlinked .git mounted: %v", ro)
	}
}

func TestGitHooksReadOnly(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, ".git", "config"), "[core]\n")
	if ro := readOnlyProject(repo); !reflect.DeepEqual(ro, []string{".git/config"}) {
		t.Fatalf("absent hooks mounted: %v", ro)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "hooks")); err == nil {
		t.Fatal(".git/hooks created on the host")
	}
	for _, f := range []string{"hooks/pre-commit", "modules/sub/HEAD", "modules/sub/hooks/post-checkout", "modules/sub/objects/hooks/x",
		"modules/lib/foo/HEAD", "modules/lib/foo/hooks/x", "modules/sub/modules/inner/HEAD", "modules/sub/modules/inner/hooks/x"} {
		write(t, filepath.Join(repo, ".git", f), "x\n")
	}
	want := []string{".git/config", ".git/hooks", ".git/modules/lib/foo/hooks", ".git/modules/sub/hooks", ".git/modules/sub/modules/inner/hooks"}
	if ro := readOnlyProject(repo); !reflect.DeepEqual(ro, want) {
		t.Fatalf("read-only %v, want %v", ro, want)
	}
	res, err := workspace.Resolve(repo, nil)
	if err != nil || len(res.Masks) != 0 {
		t.Fatalf("hooks masked: %+v %v", res.Masks, err)
	}
	s := agentSpec(agentInput{Meta: Meta{Name: "t", Agent: "shell", Workspace: "/p"}, GOOS: "linux", ReadOnly: []string{".git/config", ".git/hooks"}, GitDir: true, EmptyDir: "/empty"})
	argv := strings.Join(runtime.CreateArgs(s), " ")
	if !strings.Contains(argv, "--volume /p/.git/hooks:/p/.git/hooks:ro") || strings.Contains(argv, "/empty") {
		t.Fatalf("want .git/hooks bound read-only on itself: %s", argv)
	}
	if i, j := strings.Index(argv, "--volume /p/.git:/p/.git "), strings.Index(argv, "/p/.git/hooks:ro"); i < 0 || j < i {
		t.Fatalf(".git/hooks must be mounted after .git: %s", argv)
	}
}
