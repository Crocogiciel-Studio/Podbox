package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

func TestSanitizeBranchPart(t *testing.T) {
	long := strings.Repeat("a", 300)
	for in, want := range map[string]string{
		"main":           "main",
		"feat/x":         "feat/x",
		"-evil":          "-evil",
		"a b":            "a-b",
		"a..b":           "a.-b",
		"a\x1b[31mb\x07": "a--31mb-",
		"x.lock":         "x-lock",
		"/lead//trail/":  "lead/trail",
		".hidden/.x.":    "hidden/x",
		"café":           "caf-",
		"":               "agent",
		"///":            "agent",
		long:             long[:100],
		"a@{b":           "a--b",
		"a~^:?*[\\b":     "a-------b",
	} {
		if got := SanitizeBranchPart(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if got := DefaultBranch("s", "-evil"); got != "podbox/s/-evil" {
		t.Errorf("default: %q", got)
	}
}

func TestMaskSet(t *testing.T) {
	s := newMaskSet([]byte("secrets/\n/private.txt\n*.token\n"), []workspace.Tracked{{Rel: "old/place.key"}, {Rel: "moved", Dir: true, Now: "newdir"}})
	for p, want := range map[string]bool{
		".env": true, ".ENV": true, "a/.env": true, "x/id_rsa": true, "secrets/a/b": true, "private.txt": true, "sub/private.txt": false,
		"a.token": true, "d/a.token": true, "old/place.key": true, "moved/f": true, "newdir/f": true, "src/main.go": false, ".env.example": false,
		"Secrets/a": true,
	} {
		if got := s.masked(p); got != want {
			t.Errorf("%q: got %v want %v", p, got, want)
		}
	}
}

func TestRefuseMasked(t *testing.T) {
	if refuseMasked(nil, false) != nil || refuseMasked([]string{".env"}, true) != nil {
		t.Error("nothing to refuse, or included")
	}
	err := refuseMasked([]string{".env", "a\x1b[31m.key"}, false)
	if !errors.Is(err, ErrMaskedTouched) || !strings.Contains(err.Error(), ".env") || !strings.Contains(err.Error(), "--include-masked") || strings.Contains(err.Error(), "\x1b") {
		t.Errorf("%v", err)
	}
	many := make([]string, 30)
	for i := range many {
		many[i] = fmt.Sprintf("k%d.key", i)
	}
	if err := refuseMasked(many, false); !strings.Contains(err.Error(), "and 10 more") {
		t.Errorf("%v", err)
	}
}

func TestParseStatus(t *testing.T) {
	unc, untr := parseStatus([]byte(" M a.txt\x00A  b.txt\x00R  new.txt\x00old.txt\x00?? u.txt\x00?? dir/v.txt\x00!! ign.log\x00"))
	if strings.Join(unc, ",") != "a.txt,b.txt,new.txt" || strings.Join(untr, ",") != "u.txt,dir/v.txt" {
		t.Errorf("%v %v", unc, untr)
	}
}

func TestAnalyzeWarnings(t *testing.T) {
	r := func(mode, p string) rawEntry { return rawEntry{status: 'A', mode: mode, oldMode: zeroMode, path: p} }
	raws := []rawEntry{r("120000", "link"), r("160000", "sub"), r("100644", ".gitmodules"), r("100644", "d/.gitattributes"), r("100755", ".husky/pre-commit"),
		r("100644", "lefthook.yml"), r("100644", "x/.git/config"), r("100644", ".sandboxignore"), r("100644", "nul.sh"), r("100644", "Ab.txt"), r("100644", "ab.txt"), r("100644", "café")}
	secs := make([]patchSection, len(raws))
	secs[8].nul = true
	secs[1].nul = true // a gitlink never counts
	got := map[string]int{}
	for _, w := range analyze(raws, secs) {
		got[w.Kind] = w.Count
		if w.Detail == "" || len(w.Examples) == 0 {
			t.Errorf("%s: empty detail or examples", w.Kind)
		}
	}
	want := map[string]int{"symlink": 1, "submodule": 2, "attributes": 1, "hook-like": 2, "git-internal": 1, "podbox-config": 1, "nul": 1, "case-collision": 2, "non-ascii": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v want %v", got, want)
	}
	if len(analyze([]rawEntry{r("100644", "a.go")}, make([]patchSection, 1))) != 0 {
		t.Error("a plain file warns")
	}
}

// --- end to end against real git, the sandbox being a local directory ---

func needGit(t *testing.T) {
	t.Helper()
	if _, _, err := hostgit.Available(); err != nil {
		t.Skip(err)
	}
}

var agentIdent = []string{"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.org", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.org"}

func sh(t *testing.T, dir, script string, stdin ...string) string {
	t.Helper()
	c := exec.Command("sh", "-c", script)
	c.Dir = dir
	c.Env = append(append(cleanEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), agentIdent...)
	if len(stdin) > 0 {
		c.Stdin = strings.NewReader(stdin[0])
	}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

// localRT runs the commands Export sends to the sandbox in a local directory.
type localRT struct {
	runtime.Runtime
	dir      string
	override func(argv []string) ([]byte, bool) // replaces a command's stdout
	argvs    [][]string
}

func (r *localRT) ContainerInspect(context.Context, string) (runtime.Container, error) {
	return runtime.Container{State: "running"}, nil
}

func (r *localRT) Exec(ctx context.Context, _ string, s runtime.ExecSpec) (int, error) {
	argv := s.Command
	r.argvs = append(r.argvs, argv)
	if argv[0] == "timeout" {
		argv = argv[4:]
	}
	if r.override != nil {
		if out, ok := r.override(argv); ok {
			s.Stdout.Write(out)
			return 0, nil
		}
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = r.dir
	c.Env = cleanEnv()
	for k, v := range s.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	c.Stdout, c.Stderr = s.Stdout, s.Stderr
	if s.Stdin != nil {
		c.Stdin = s.Stdin
	}
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

type fixture struct {
	t      *testing.T
	m      *Manager
	host   string // the host repository
	clone  string
	rt     *localRT
	hook   string // marker written by every host hook that fires
	meta   Meta
	before string
}

func hostGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=h", "-c", "user.email=h@example.org", "-c", "commit.gpgsign=false"}, args...)...)
	c.Dir = dir
	c.Env = append(cleanEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	needGit(t)
	root := t.TempDir()
	host := filepath.Join(root, "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	hostGit(t, host, "init", "-q", "-b", "main")
	for p, c := range map[string]string{"a.txt": "one\n", "secret.txt": "REAL SECRET\n", "dir/b.txt": "b\n"} {
		os.MkdirAll(filepath.Dir(filepath.Join(host, p)), 0o755)
		os.WriteFile(filepath.Join(host, p), []byte(c), 0o644)
	}
	hostGit(t, host, "add", ".")
	hostGit(t, host, "commit", "-q", "-m", "base")
	base := hostGit(t, host, "rev-parse", "HEAD")
	// A hook that must never fire.
	marker := filepath.Join(root, "hook-fired")
	for _, h := range []string{"reference-transaction", "post-checkout", "post-merge", "post-update", "update", "pre-auto-gc", "post-commit"} {
		os.WriteFile(filepath.Join(host, ".git", "hooks", h), []byte("#!/bin/sh\necho "+h+" >> "+marker+"\n"), 0o755)
	}
	st := Store{Root: filepath.Join(root, "sandboxes"), ClonesRoot: filepath.Join(root, "clones")}
	meta := Meta{Name: "s", Agent: "shell", Workspace: host, Mode: ModeClone, Base: base}
	if err := st.Save(meta); err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Clone(context.Background(), host, st.CloneDir("s"), base, "main"); err != nil {
		t.Fatal(err)
	}
	rt := &localRT{dir: st.CloneDir("s")}
	f := &fixture{t: t, m: &Manager{RT: rt, Store: st, GOOS: "linux"}, host: host, clone: st.CloneDir("s"), rt: rt, hook: marker, meta: meta}
	f.before = f.hostState()
	return f
}

// hostState hashes what an export must not change: the working tree, index,
// HEAD, config, hooks and every ref but those named in skip.
func (f *fixture) hostState(skip ...string) string {
	t := f.t
	h := sha256.New()
	var files []string
	filepath.Walk(f.host, func(p string, fi os.FileInfo, err error) error {
		rel, _ := filepath.Rel(f.host, p)
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			switch {
			case rel == ".git/index", rel == ".git/HEAD", rel == ".git/config", strings.HasPrefix(rel, ".git/hooks/"):
			default:
				return nil
			}
		}
		files = append(files, rel)
		return nil
	})
	sort.Strings(files)
	for _, rel := range files {
		b, _ := os.ReadFile(filepath.Join(f.host, rel))
		fmt.Fprintf(h, "%s %x\n", rel, sha256.Sum256(b))
	}
	refs := hostGit(t, f.host, "for-each-ref", "--format=%(refname) %(objectname)")
	for _, l := range strings.Split(refs, "\n") {
		keep := true
		for _, s := range skip {
			if strings.Contains(l, s) {
				keep = false
			}
		}
		if keep {
			fmt.Fprintln(h, l)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (f *fixture) agent(script string) { f.t.Helper(); sh(f.t, f.clone, script) }

func (f *fixture) commit(file, content string) {
	f.t.Helper()
	p := filepath.Join(f.clone, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.agent("git add -A; git commit -qm edit")
}

func (f *fixture) noLeftovers() {
	f.t.Helper()
	if out := hostGit(f.t, f.host, "for-each-ref", "refs/podbox"); out != "" {
		f.t.Errorf("temporary ref left: %s", out)
	}
	if _, err := os.Stat(filepath.Join(f.host, ".git", "FETCH_HEAD")); err == nil {
		f.t.Error("FETCH_HEAD written")
	}
	if _, err := os.Stat(f.hook); err == nil {
		b, _ := os.ReadFile(f.hook)
		f.t.Errorf("a host hook fired: %s", b)
	}
	left, _ := filepath.Glob(filepath.Join(os.TempDir(), "podbox-export-*"))
	if len(left) > 0 {
		f.t.Errorf("temporary directory left: %v", left)
	}
}

func (f *fixture) export(opt ExportOptions) (ExportResult, error) {
	return f.m.Export(context.Background(), "s", opt)
}

func TestExportBranch(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one\ntwo\n")
	f.commit("new/c.txt", "c\n")
	f.agent("echo dirty >> dir/b.txt; echo u > untracked.txt")
	res, err := f.export(ExportOptions{As: AsBranch})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exported || res.Branch != "podbox/s/main" || res.Commits != 2 || !res.HostVerified || res.FilesChanged != 2 || len(res.CommitList) != 2 {
		t.Errorf("%+v", res)
	}
	if got := hostGit(t, f.host, "rev-parse", "podbox/s/main"); got != res.Head || got != hostGit(t, f.clone, "rev-parse", "HEAD") {
		t.Errorf("branch at %s, head %s", got, res.Head)
	}
	if n := res.NotExported; n.Uncommitted != 1 || n.Untracked != 1 || n.UncommittedFiles[0] != "dir/b.txt" || n.UntrackedFiles[0] != "untracked.txt" {
		t.Errorf("not exported %+v", n)
	}
	if got := f.hostState("podbox/s/main"); got != f.before {
		t.Error("the host repository changed beyond the new branch")
	}
	f.noLeftovers()
	if hostGit(t, f.host, "show", "podbox/s/main:a.txt") != "one\ntwo" {
		t.Error("branch content")
	}

	// the same name again: refused, untouched
	tip := res.Head
	f.commit("later.txt", "x")
	_, err = f.export(ExportOptions{As: AsBranch})
	if !errors.Is(err, hostgit.ErrRefExists) || !strings.Contains(err.Error(), "--branch-name") {
		t.Errorf("existing branch: %v", err)
	}
	if hostGit(t, f.host, "rev-parse", "podbox/s/main") != tip {
		t.Error("existing branch moved")
	}
	// the host's current branch is just an existing branch
	if _, err = f.export(ExportOptions{As: AsBranch, BranchName: "main"}); !errors.Is(err, hostgit.ErrRefExists) {
		t.Errorf("current branch: %v", err)
	}
	if res, err = f.export(ExportOptions{As: AsBranch, BranchName: "review/x"}); err != nil || res.Branch != "review/x" || res.Commits != 3 {
		t.Errorf("--branch-name: %v %+v", err, res)
	}
	f.noLeftovers()
}

func TestExportBranchNames(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "x")
	for _, bad := range []string{"-x", "a..b", "a b", "a\x01b", "x.lock", "@{-1}", "HEAD", "a~b", "/a", "a//b", strings.Repeat("a", 5000) + "\x00"} {
		_, err := f.export(ExportOptions{As: AsBranch, BranchName: bad})
		if !errors.Is(err, ErrBadExport) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	if out := hostGit(t, f.host, "branch", "--list"); out != "* main" {
		t.Errorf("branches %q", out)
	}
	// the agent's own branch name is hostile
	f.agent("git update-ref refs/heads/-evil HEAD && git symbolic-ref HEAD refs/heads/-evil")
	f.commit("b.txt", "y")
	res, err := f.export(ExportOptions{As: AsBranch})
	if err != nil || res.Branch != "podbox/s/-evil" {
		t.Errorf("%v %+v", err, res)
	}
	long := "refs/heads/" + strings.Repeat("l", 200) + "/ünï"
	f.agent("git update-ref '" + long + "' HEAD && git symbolic-ref HEAD '" + long + "'")
	f.commit("c.txt", "z")
	res, err = f.export(ExportOptions{As: AsBranch})
	if err == nil && (len(res.Branch) > 120 || !res.BranchSanitized) {
		t.Errorf("long name: %+v", res)
	}
	// detached
	f.agent("git checkout -q --detach")
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrDetached) {
		t.Errorf("detached: %v", err)
	}
	if res, err := f.export(ExportOptions{As: AsBranch, BranchName: "from-detached"}); err != nil || !res.Exported {
		t.Errorf("detached with a name: %v", err)
	}
	f.noLeftovers()
}

func TestExportNothing(t *testing.T) {
	f := newFixture(t)
	f.agent("echo dirty >> a.txt; echo u > u.txt")
	for _, as := range []string{AsBranch, AsPatch} {
		o := ExportOptions{As: as}
		if as == AsPatch {
			o.Out = filepath.Join(t.TempDir(), "x.patch")
		}
		res, err := f.export(o)
		if err != nil || res.Exported || res.Commits != 0 || res.NotExported.Uncommitted != 1 || res.NotExported.Untracked != 1 {
			t.Errorf("%s: %v %+v", as, err, res)
		}
	}
	if hostGit(t, f.host, "branch", "--list") != "* main" || f.hostState() != f.before {
		t.Error("something was created")
	}
}

func TestExportPatch(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one\ntwo\n")
	f.agent("printf '\\000\\001bin' > blob.bin; git add blob.bin; git commit -qm bin; git rm -q dir/b.txt; git commit -qm rm")
	out := filepath.Join(t.TempDir(), "s.patch")
	res, err := f.export(ExportOptions{As: AsPatch, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exported || res.File != out || res.Commits != 3 || res.HostVerified || res.Branch != "" {
		t.Errorf("%+v", res)
	}
	// it applies on a fresh checkout of the base and gives the agent's tree
	fresh := filepath.Join(t.TempDir(), "fresh")
	hostGit(t, t.TempDir(), "clone", "-q", f.host, fresh)
	hostGit(t, fresh, "checkout", "-q", "--detach", f.meta.Base)
	hostGit(t, fresh, "apply", "--index", out)
	if got, want := hostGit(t, fresh, "write-tree"), hostGit(t, f.clone, "rev-parse", "HEAD^{tree}"); got != want {
		t.Errorf("tree %s != %s", got, want)
	}
	// never overwritten, never touched
	before, _ := os.ReadFile(out)
	if _, err := f.export(ExportOptions{As: AsPatch, Out: out}); !errors.Is(err, ErrFileExists) {
		t.Errorf("overwrite: %v", err)
	}
	if after, _ := os.ReadFile(out); !bytes.Equal(before, after) {
		t.Error("the file changed")
	}
	// a symlink at the target is not followed either
	link := filepath.Join(t.TempDir(), "l.patch")
	os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), link)
	if _, err := f.export(ExportOptions{As: AsPatch, Out: link}); !errors.Is(err, ErrFileExists) {
		t.Errorf("symlink target: %v", err)
	}
	// default name in the current directory
	wd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(wd)
	res, err = f.export(ExportOptions{As: AsPatch})
	if err != nil || filepath.Base(res.File) != "s.patch" {
		t.Errorf("default: %v %+v", err, res)
	}
	if got := f.hostState(); got != f.before {
		t.Error("host changed")
	}
}

func TestExportMasked(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.m.Store.ProjectIgnorePath("s")), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f.m.Store.ProjectIgnorePath("s"), []byte("secret.txt\n"), 0o600)
	f.commit("secret.txt", "") // the agent saw it empty
	out := filepath.Join(t.TempDir(), "x.patch")
	for _, opt := range []ExportOptions{{As: AsBranch}, {As: AsPatch, Out: out}} {
		res, err := f.export(opt)
		if !errors.Is(err, ErrMaskedTouched) || !strings.Contains(err.Error(), "secret.txt") {
			t.Errorf("%s: %v", opt.As, err)
		}
		if len(res.MaskedTouched) != 1 {
			t.Errorf("%s: %+v", opt.As, res.MaskedTouched)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a patch was written for a refused export")
	}
	if hostGit(t, f.host, "branch", "--list") != "* main" {
		t.Error("a branch was created")
	}
	f.noLeftovers()
	res, err := f.export(ExportOptions{As: AsBranch, IncludeMasked: true})
	if err != nil || !res.Exported || len(res.MaskedTouched) != 1 || res.MaskedTouched[0] != "secret.txt" || res.MaskedTouchedB64[0] != "c2VjcmV0LnR4dA==" {
		t.Errorf("--include-masked: %v %+v", err, res)
	}
}

func TestExportNotExportedSkipsMaskedPaths(t *testing.T) {
	f := newFixture(t)
	os.MkdirAll(filepath.Dir(f.m.Store.ProjectIgnorePath("s")), 0o700)
	os.WriteFile(f.m.Store.ProjectIgnorePath("s"), []byte("secret.txt\n"), 0o600)
	f.agent("echo dirty >> a.txt; echo u > untracked.txt; echo masked > untracked.key; rm secret.txt")
	res, err := f.export(ExportOptions{As: AsBranch})
	if err != nil || res.NotExported.Uncommitted != 1 || res.NotExported.Untracked != 1 {
		t.Errorf("%v %+v", err, res.NotExported)
	}
}

func TestExportWarnsOnDangerousFiles(t *testing.T) {
	f := newFixture(t)
	f.agent("ln -s /etc/passwd link; printf '*.x filter=evil\\n' > .gitattributes; mkdir .husky; echo x > .husky/pre-commit; printf 'a\\000b' > nul.sh; git add -A; git commit -qm danger")
	res, err := f.export(ExportOptions{As: AsBranch})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, w := range res.Warnings {
		got[w.Kind] = true
	}
	for _, k := range []string{"symlink", "attributes", "hook-like", "nul"} {
		if !got[k] {
			t.Errorf("no %s warning in %+v", k, res.Warnings)
		}
	}
}

func TestExportRefusesRewrittenHistory(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "x")
	f.agent("git checkout -q --orphan fresh && git rm -rfq . && echo evil > e.txt && git add e.txt && git commit -qm unrelated")
	for _, as := range []string{AsBranch, AsPatch} {
		o := ExportOptions{As: as}
		if as == AsPatch {
			o.Out = filepath.Join(t.TempDir(), "p")
		}
		_, err := f.export(o)
		if !errors.Is(err, ErrHistory) {
			t.Errorf("%s: %v", as, err)
		}
	}
	// the agent's own check lies (a graft-like replace says the root descends from the base):
	// the host still refuses, because it checks the objects it received.
	f.agent("git replace --graft HEAD " + f.meta.Base)
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrHistory) {
		t.Errorf("replace graft: %v", err)
	}
	// history reset to before the base
	f.agent("git checkout -q -f main && git reset -q --hard " + f.meta.Base + "~0")
	f.noLeftovers()
	if hostGit(t, f.host, "branch", "--list") != "* main" {
		t.Error("a branch was created")
	}
}

func TestExportIgnoresReplaceAndHostileConfig(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "one\ntwo\n")
	real := hostGit(t, f.clone, "rev-parse", "HEAD")
	// a replace ref turns the real tip into a different commit...
	f.agent("t=$(git rev-parse HEAD^{tree}); fake=$(git commit-tree $t -p " + f.meta.Base + " -m fake); git replace HEAD $fake; printf 'x' > /dev/null")
	if hostGit(t, f.clone, "rev-parse", "HEAD") != real {
		t.Fatal("setup: HEAD moved")
	}
	// ...and the config tries to run programs
	mark := filepath.Join(t.TempDir(), "ran")
	f.agent(fmt.Sprintf("printf '[filter \"x\"]\\n\\tclean = touch %[1]s\\n\\trequired = true\\n[core]\\n\\tfsmonitor = touch %[1]s\\n\\thooksPath = /tmp/nohooks\\n\\tpager = touch %[1]s\\n[pack]\\n\\twindowMemory = 1\\n' >> .git/config; printf 'a.txt filter=x\\n' > .gitattributes; echo more >> a.txt", mark))
	out := filepath.Join(t.TempDir(), "p.patch")
	res, err := f.export(ExportOptions{As: AsBranch})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head != real || hostGit(t, f.host, "show", "podbox/s/main:a.txt") != "one\ntwo" {
		t.Errorf("exported %s, content differs: replace ref honoured", res.Head)
	}
	if _, err := f.export(ExportOptions{As: AsPatch, Out: out}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "+two") || strings.Contains(string(b), "fake") {
		t.Errorf("patch:\n%s", b)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Error("a program named by the clone's configuration ran")
	}
	if got := f.hostState("podbox/s/main"); got != f.before {
		t.Error("host changed")
	}
}

func TestExportFsckRefusesDotGit(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "x")
	// a tree with an entry named .git, which git itself would not write
	blob := sh(t, f.clone, "echo x | git hash-object -w --stdin")
	id, _ := hex.DecodeString(blob)
	tree := sh(t, f.clone, "git hash-object -w -t tree --literally --stdin", "100644 .git\x00"+string(id))
	sh(t, f.clone, "c=$(git commit-tree "+tree+" -p HEAD -m dotgit); git update-ref HEAD $c")
	_, err := f.export(ExportOptions{As: AsBranch})
	t.Logf("err: %v", err)
	if !errors.Is(err, hostgit.ErrBadBundle) {
		t.Errorf("a .git tree entry was accepted: %v", err)
	}
	if hostGit(t, f.host, "branch", "--list") != "* main" {
		t.Error("a branch was created")
	}
	f.noLeftovers()
}

func TestExportBadBundles(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "x")
	bundle := ""
	f.rt.override = func(argv []string) ([]byte, bool) {
		for _, a := range argv {
			if a == "bundle" {
				return []byte(bundle), true
			}
		}
		return nil, false
	}
	good := hostGit(t, f.clone, "bundle", "create", "-", "HEAD", "^"+f.meta.Base)
	_ = good
	for name, b := range map[string]string{"junk": "this is not a bundle", "empty": "", "truncated": "# v2 git bundle\n" + f.meta.Base + " \n\nPACK\x00\x00"} {
		bundle = b
		if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, hostgit.ErrBadBundle) {
			t.Errorf("%s: %v", name, err)
		}
		f.noLeftovers()
	}
	// a head that is not the clone's HEAD
	var raw bytes.Buffer
	c := exec.Command("git", "bundle", "create", "-", "HEAD~1", "^"+f.meta.Base+"~0")
	c.Dir = f.clone
	c.Stdout = &raw
	_ = c.Run()
	if hostGit(t, f.host, "branch", "--list") != "* main" {
		t.Error("a branch was created")
	}
}

func TestExportTooLarge(t *testing.T) {
	f := newFixture(t)
	f.agent("head -c 200000 /dev/urandom | od -An -tx1 > big.txt; git add -A; git commit -qm big")
	f.m.MaxExport = 1000
	out := filepath.Join(t.TempDir(), "p.patch")
	for _, opt := range []ExportOptions{{As: AsBranch}, {As: AsPatch, Out: out}} {
		if _, err := f.export(opt); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v", opt.As, err)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("partial patch left")
	}
	if hostGit(t, f.host, "branch", "--list") != "* main" {
		t.Error("a branch was created")
	}
	f.noLeftovers()
}

func TestExportRefusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.commit("a.txt", "x")
	for _, o := range []ExportOptions{{}, {As: "zip"}, {As: AsPatch, BranchName: "x"}, {As: AsBranch, Out: "x"}} {
		if _, err := f.m.Export(ctx, "s", o); !errors.Is(err, ErrBadExport) {
			t.Errorf("%+v: %v", o, err)
		}
	}
	shared := f.meta
	shared.Mode = ""
	f.m.Store.Save(shared)
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrNotClone) {
		t.Errorf("shared: %v", err)
	}
	f.m.Store.Save(f.meta)
	if _, err := f.m.Export(ctx, "nope", ExportOptions{As: AsBranch}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	// stopped
	f.m.RT = &gitRT{state: "exited"}
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("stopped: %v", err)
	}
	f.m.RT = f.rt
	// a base the host repository does not have
	bad := f.meta
	bad.Base = strings.Repeat("ab", 20)
	f.m.Store.Save(bad)
	if _, err := f.export(ExportOptions{As: AsBranch}); !errors.Is(err, ErrBaseMissing) {
		t.Errorf("missing base: %v", err)
	}
	f.noLeftovers()
}

// The commands sent to the sandbox are bounded and neutralised.
func TestExportCommandsAreHardened(t *testing.T) {
	f := newFixture(t)
	f.commit("a.txt", "x")
	if _, err := f.export(ExportOptions{As: AsPatch, Out: filepath.Join(t.TempDir(), "p")}); err != nil {
		t.Fatal(err)
	}
	var sawPatch bool
	for _, a := range f.rt.argvs {
		if a[0] != "timeout" || a[1] != "-k" {
			t.Errorf("not bounded: %v", a)
			continue
		}
		j := strings.Join(a, " ")
		if !strings.Contains(j, "core.hooksPath=/dev/null") || !strings.Contains(j, "core.fsmonitor=false") {
			t.Errorf("not neutralised: %v", a)
		}
		if strings.Contains(j, " format-patch ") {
			sawPatch = true
			for _, want := range []string{"--stdout", "--binary", "--full-index", "--no-signature", "--no-ext-diff", "--no-textconv", "^{}"} {
				if want != "^{}" && !strings.Contains(j, want) {
					t.Errorf("format-patch lacks %s: %v", want, a)
				}
			}
		}
	}
	if !sawPatch {
		t.Error("format-patch not run")
	}
}

var _ = io.Discard
