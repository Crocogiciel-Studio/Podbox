package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// patchTree applies a patch on a fresh checkout of the base and returns the
// resulting tree and the content of path there.
func (f *fixture) patchTree(patch, path string) (tree, content string) {
	f.t.Helper()
	fresh := filepath.Join(f.t.TempDir(), "fresh")
	hostGit(f.t, f.t.TempDir(), "clone", "-q", f.host, fresh)
	hostGit(f.t, fresh, "checkout", "-q", "--detach", f.meta.Base)
	hostGit(f.t, fresh, "apply", "--index", patch)
	b, _ := os.ReadFile(filepath.Join(fresh, path))
	return hostGit(f.t, fresh, "write-tree"), string(b)
}

func (f *fixture) mask(patterns string) {
	f.t.Helper()
	p := f.m.Store.ProjectIgnorePath("s")
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(patterns), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// A commit message that holds a diff must not change what the patch applies.
func TestPatchHoldsNoCommitMessage(t *testing.T) {
	f := newFixture(t)
	f.mask("secret.txt\n")
	msg := "innocent\n\ndiff --git a/secret.txt b/secret.txt\n--- a/secret.txt\n+++ b/secret.txt\n@@ -1 +1 @@\n-REAL SECRET\n+pwned\n"
	mf := filepath.Join(t.TempDir(), "msg")
	os.WriteFile(mf, []byte(msg), 0o600)
	f.commit("a.txt", "one\ntwo\n")
	f.agent("git commit -q --allow-empty -F " + mf)
	out := filepath.Join(t.TempDir(), "p.patch")
	if _, err := f.export(ExportOptions{As: AsPatch, Out: out}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), "pwned") {
		t.Errorf("the commit message is in the patch:\n%s", b)
	}
	tree, secret := f.patchTree(out, "secret.txt")
	if secret != "REAL SECRET\n" {
		t.Errorf("the patch changed the masked file: %q", secret)
	}
	if tree != hostGit(t, f.clone, "rev-parse", "HEAD^{tree}") {
		t.Error("the patch does not give the agent's tree")
	}
}

// A merge commit's own changes (an evil merge) are in the patch and in the branch.
func TestExportMerge(t *testing.T) {
	f := newFixture(t)
	f.agent("git checkout -q -b side && echo s > s.txt && git add s.txt && git commit -qm side && git checkout -q main && echo m > m.txt && git add m.txt && git commit -qm main2 && git merge -q --no-ff --no-commit side && echo evil >> a.txt && git add a.txt && git commit -qm merge")
	want := hostGit(t, f.clone, "rev-parse", "HEAD^{tree}")
	out := filepath.Join(t.TempDir(), "p.patch")
	res, err := f.export(ExportOptions{As: AsPatch, Out: out})
	if err != nil || res.Commits != 3 || res.FilesChanged != 3 {
		t.Fatalf("%v %+v", err, res)
	}
	if tree, _ := f.patchTree(out, "s.txt"); tree != want {
		t.Error("the patch does not give the agent's tree")
	}
	res, err = f.export(ExportOptions{As: AsBranch})
	if err != nil || hostGit(t, f.host, "rev-parse", res.Branch+"^{tree}") != want {
		t.Errorf("branch: %v", err)
	}
}

// A masked path touched by one commit and put back by a later one is still refused.
func TestMaskedTouchedThenReverted(t *testing.T) {
	f := newFixture(t)
	f.mask("secret.txt\n")
	f.commit("secret.txt", "")
	f.agent("git checkout " + f.meta.Base + " -- secret.txt && git commit -qm restore && ln -s /etc/passwd lnk && git add lnk && git commit -qm link && git rm -q lnk && git commit -qm unlink")
	if n := hostGit(t, f.clone, "diff", "--name-only", f.meta.Base, "HEAD"); n != "" {
		t.Fatalf("setup: the net change is %q", n)
	}
	out := filepath.Join(t.TempDir(), "p.patch")
	for _, o := range []ExportOptions{{As: AsBranch}, {As: AsPatch, Out: out}} {
		_, err := f.export(o)
		if !errors.Is(err, ErrMaskedTouched) || !strings.Contains(err.Error(), "secret.txt") {
			t.Errorf("%s: %v", o.As, err)
		}
	}
	res, err := f.export(ExportOptions{As: AsBranch, IncludeMasked: true})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, w := range res.Warnings {
		kinds[w.Kind] = true
	}
	if !kinds["symlink"] {
		t.Errorf("a symlink added and removed in history is not warned about: %+v", res.Warnings)
	}
}

func TestMaskCaseFolding(t *testing.T) {
	f := newFixture(t)
	f.mask("Secrets/\n")
	f.commit("SECRETS/x", "x")
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrMaskedTouched) {
		t.Errorf("pattern Secrets/ and path SECRETS/x: %v", err)
	}
	f.mask("")
	f.agent("git reset -q --hard HEAD~1 2>&1; git clean -fdq")
	if err := f.m.Store.saveMasks("s", []workspace.Tracked{{Rel: "Config/Secret.json"}}); err != nil {
		t.Fatal(err)
	}
	f.commit("config/secret.json", "{}")
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrMaskedTouched) {
		t.Errorf("recorded Config/Secret.json and path config/secret.json: %v", err)
	}
	// a path that is not masked is not caught
	f.agent("git reset -q --hard HEAD~1")
	f.commit("other/x", "x")
	if _, err := f.export(ExportOptions{As: AsBranch}); err != nil {
		t.Errorf("an unmasked path was refused: %v", err)
	}
}

func TestMatcherFold(t *testing.T) {
	m := workspace.MatcherFold([]byte("Secrets/\n/Private.TXT\n"))
	for p, want := range map[string]bool{"SECRETS/x": true, "secrets/x": true, "a/.ENV": true, "PRIVATE.txt": true, "sub/private.txt": false, "a.go": false} {
		if got := m(p, false); got != want {
			t.Errorf("%q: %v", p, got)
		}
	}
}

// An interrupt in the middle of an export leaves nothing on the host.
type blockRT struct {
	*localRT
	cancel func()
}

func (b blockRT) Exec(ctx context.Context, name string, s runtime.ExecSpec) (int, error) {
	for _, a := range s.Command {
		if a == "bundle" {
			b.cancel() // the export reached the bundle
			<-ctx.Done()
			return -1, ctx.Err()
		}
	}
	return b.localRT.Exec(ctx, name, s)
}

func TestExportInterruptedLeavesNothing(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "x")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx, cancel := context.WithCancel(context.Background())
	f.m.RT = blockRT{f.rt, cancel}
	_, err := f.m.Export(ctx, "s", ExportOptions{As: AsBranch})
	if err == nil {
		t.Fatal("an interrupted export succeeded")
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "podbox-export-*")); len(left) > 0 {
		t.Errorf("temporary directory left: %v", left)
	}
	if hostGit(t, f.host, "for-each-ref", "refs/podbox") != "" || hostGit(t, f.host, "branch", "--list") != "* main" {
		t.Error("a ref was left")
	}
}

func TestDefaultBranchIsAlwaysAValidName(t *testing.T) {
	needGit(t)
	long := strings.Repeat("a", 95) + ".lock" + "xyz"
	for _, c := range [][2]string{
		{"s", long}, {"s", strings.Repeat("a", 99) + "/b"}, {"s", strings.Repeat("a", 99) + "."}, {"s", strings.Repeat("a/", 60)},
		{"foo.", "main"}, {"a..b", "main"}, {"x.lock", "main"}, {"s", "-evil"}, {"s", "a\x1b[31m"},
	} {
		name := DefaultBranch(c[0], c[1])
		if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, "/") || strings.Contains(name, "..") {
			t.Errorf("%q: %q", c, name)
		}
	}
	f := newFixture(t)
	for _, agent := range []string{long, strings.Repeat("a", 99) + "/b", strings.Repeat("a/", 60)} {
		f.agent("git update-ref '" + "refs/heads/" + strings.Trim(agent, "/") + "' HEAD && git symbolic-ref HEAD '" + "refs/heads/" + strings.Trim(agent, "/") + "'")
		f.commit("a"+string(rune('a'+len(agent)%20))+".txt", "x")
		if _, err := f.export(ExportOptions{As: AsBranch}); err != nil {
			t.Errorf("default name for %q: %v", agent, err)
		}
	}
}

// A file past core.bigFileThreshold is not read by the review: it counts as hidden.
func TestBigFileIsNotInflatedByTheReview(t *testing.T) {
	f := newFixture(t)
	f.commit("big.txt", strings.Repeat("a", 70<<20))
	res, err := f.export(ExportOptions{As: AsBranch})
	if err != nil {
		t.Fatal(err)
	}
	var nul bool
	for _, w := range res.Warnings {
		nul = nul || w.Kind == "nul"
	}
	if !nul {
		t.Errorf("a 70 MiB file is not flagged as hidden: %+v", res.Warnings)
	}
}

// The final name never holds a partial file, a refused target leaves no debris.
func TestPatchIsLinkedIntoPlaceOnlyWhenWhole(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one\ntwo\n")
	dir := t.TempDir()
	out := filepath.Join(dir, "p.patch")
	var existedWhileStreaming bool
	f.rt.override = func(argv []string) ([]byte, bool) {
		for _, a := range argv {
			if a == "--binary" {
				_, err := os.Lstat(out)
				existedWhileStreaming = err == nil
			}
		}
		return nil, false
	}
	if _, err := f.export(ExportOptions{As: AsPatch, Out: out}); err != nil {
		t.Fatal(err)
	}
	if existedWhileStreaming {
		t.Error("the final name existed while the patch was being written")
	}
	debris := func() []string {
		m, _ := filepath.Glob(filepath.Join(dir, ".*"))
		return m
	}
	if d := debris(); len(d) != 0 {
		t.Errorf("temporary file left: %v", d)
	}
	if fi, err := os.Stat(out); err != nil || fi.Mode().Perm()&0o044 == 0 || fi.Mode().Perm()&0o200 == 0 {
		t.Errorf("mode %v %v", fi, err)
	}
	f.rt.override = nil
	before, _ := os.ReadFile(out)
	if _, err := f.export(ExportOptions{As: AsPatch, Out: out}); !errors.Is(err, ErrFileExists) {
		t.Errorf("existing file: %v", err)
	}
	link := filepath.Join(dir, "l.patch")
	os.Symlink(filepath.Join(dir, "elsewhere"), link)
	if _, err := f.export(ExportOptions{As: AsPatch, Out: link}); !errors.Is(err, ErrFileExists) {
		t.Errorf("symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "elsewhere")); err == nil {
		t.Error("a symlink target was written")
	}
	if after, _ := os.ReadFile(out); string(after) != string(before) {
		t.Error("the file changed")
	}
	if d := debris(); len(d) != 0 {
		t.Errorf("temporary file left after a refusal: %v", d)
	}
	// a failure while writing leaves nothing either
	f.m.MaxExport = 100
	if _, err := f.export(ExportOptions{As: AsPatch, Out: filepath.Join(dir, "big.patch")}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("cap: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*big*")); len(m) != 0 {
		t.Errorf("debris after the cap: %v", m)
	}
	if d := debris(); len(d) != 0 {
		t.Errorf("hidden debris after the cap: %v", d)
	}
}
