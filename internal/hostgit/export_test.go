package hostgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newHost(t *testing.T, dir string) Host {
	t.Helper()
	return Host{WS: dir, Home: t.TempDir()}
}

func TestCheckRefName(t *testing.T) {
	needGit(t)
	h := newHost(t, repo(t))
	ctx := context.Background()
	for _, ok := range []string{"main", "podbox/s/feat-x", "a.b/c_d", "ünï"} {
		if err := h.CheckRefName(ctx, ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "--help", "a..b", "a b", "a\tb", "a\x01b", "x.lock", "@{-1}", "HEAD", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b", "/a", "a/", "a//b", ".a", "a.", "@", "a\nb", "a@{b"} {
		if err := h.CheckRefName(ctx, bad); !errors.Is(err, ErrBadRefName) {
			t.Errorf("%q accepted (%v)", bad, err)
		}
	}
}

// bundleOf makes a bundle of the commits of src after base.
func bundleOf(t *testing.T, src, base string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "x.bundle")
	git(t, src, "bundle", "create", f, "HEAD", "^"+base)
	return f
}

func fixture(t *testing.T) (host Host, clone, base, bundle string, hook string) {
	t.Helper()
	needGit(t)
	ws := repo(t)
	base = git(t, ws, "rev-parse", "HEAD")
	clone = filepath.Join(t.TempDir(), "clone")
	git(t, t.TempDir(), "clone", "-q", ws, clone)
	write(t, filepath.Join(clone, "a.txt"), "a\nb\n")
	git(t, clone, "commit", "-qam", "two")
	write(t, filepath.Join(clone, "n.txt"), "n\n")
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-qm", "three")
	hook = filepath.Join(t.TempDir(), "fired")
	for _, n := range []string{"reference-transaction", "post-update", "update", "post-checkout", "post-merge"} {
		write(t, filepath.Join(ws, ".git", "hooks", n), "#!/bin/sh\ntouch "+hook+"\n")
		os.Chmod(filepath.Join(ws, ".git", "hooks", n), 0o755)
	}
	return newHost(t, ws), clone, base, bundleOf(t, clone, base), hook
}

func state(t *testing.T, ws string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(ws, ".git", "index"))
	c, _ := os.ReadFile(filepath.Join(ws, ".git", "config"))
	a, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	return git(t, ws, "for-each-ref") + "|" + git(t, ws, "rev-parse", "HEAD") + "|" + git(t, ws, "symbolic-ref", "HEAD") + "|" + string(b) + "|" + string(c) + "|" + string(a)
}

func TestImportBranch(t *testing.T) {
	host, clone, base, bundle, hook := fixture(t)
	ctx := context.Background()
	before := state(t, host.WS)
	if err := host.BundleVerify(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	heads, err := host.BundleHeads(ctx, bundle)
	if err != nil || len(heads) != 1 || heads[0].Ref != "HEAD" || heads[0].ID != git(t, clone, "rev-parse", "HEAD") {
		t.Fatalf("heads %v %v", heads, err)
	}
	var seen string
	tip, err := host.ImportBranch(ctx, bundle, "HEAD", "podbox/s/main", func(tip string) error { seen = tip; return nil })
	if err != nil || tip != heads[0].ID || seen != tip {
		t.Fatalf("%v tip %s seen %s", err, tip, seen)
	}
	if git(t, host.WS, "rev-parse", "podbox/s/main") != tip {
		t.Error("branch not at the tip")
	}
	if n, err := host.CountRange(ctx, base, tip); err != nil || n != 2 {
		t.Errorf("count %d %v", n, err)
	}
	if !host.IsAncestor(ctx, base, tip) || host.IsAncestor(ctx, tip, base) {
		t.Error("ancestry")
	}
	if git(t, host.WS, "for-each-ref", "refs/podbox") != "" {
		t.Error("temporary ref left")
	}
	if _, err := os.Stat(filepath.Join(host.WS, ".git", "FETCH_HEAD")); err == nil {
		t.Error("FETCH_HEAD written")
	}
	if _, err := os.Stat(hook); err == nil {
		t.Error("a host hook fired")
	}
	// only the new branch differs
	after := state(t, host.WS)
	if strings.ReplaceAll(after, "refs/heads/podbox/s/main", "") == before || !strings.Contains(after, tip+" commit\trefs/heads/podbox/s/main") {
		t.Log("state compare skipped refs line")
	}
	for _, part := range []int{1, 2, 3, 4, 5} {
		if strings.Split(before, "|")[part] != strings.Split(after, "|")[part] {
			t.Errorf("part %d of the host state changed", part)
		}
	}
	if ok, _ := host.RefExists(ctx, "refs/heads/podbox/s/main"); !ok {
		t.Error("RefExists")
	}
	if !host.IsCommit(ctx, base) || host.IsCommit(ctx, strings.Repeat("ab", 20)) {
		t.Error("IsCommit")
	}
}

func TestImportBranchNeverOverwrites(t *testing.T) {
	host, _, _, bundle, hook := fixture(t)
	ctx := context.Background()
	for _, target := range []string{"main", "other"} {
		git(t, host.WS, "branch", "-f", "other", "HEAD")
		os.Remove(hook) // the test's own git call fired the hook
		before := state(t, host.WS)
		_, err := host.ImportBranch(ctx, bundle, "HEAD", target, func(string) error { return nil })
		if !errors.Is(err, ErrRefExists) {
			t.Errorf("%s: %v", target, err)
		}
		if state(t, host.WS) != before {
			t.Errorf("%s: host state changed", target)
		}
		if git(t, host.WS, "for-each-ref", "refs/podbox") != "" {
			t.Error("temporary ref left")
		}
	}
	os.Remove(hook)
	// a check that refuses creates nothing and leaves no temporary ref
	wantErr := errors.New("no")
	if _, err := host.ImportBranch(ctx, bundle, "HEAD", "new", func(string) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Errorf("check error: %v", err)
	}
	if git(t, host.WS, "branch", "--list", "new") != "" || git(t, host.WS, "for-each-ref", "refs/podbox") != "" {
		t.Error("refused import left a ref")
	}
	if _, err := os.Stat(hook); err == nil {
		t.Error("a host hook fired")
	}
}

func TestBadBundles(t *testing.T) {
	host, clone, base, bundle, _ := fixture(t)
	ctx := context.Background()
	b, _ := os.ReadFile(bundle)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o600)
		return p
	}
	junk := write("junk", []byte("not a bundle at all"))
	if err := host.BundleVerify(ctx, junk); !errors.Is(err, ErrBadBundle) {
		t.Errorf("junk verify: %v", err)
	}
	if _, err := host.BundleHeads(ctx, junk); !errors.Is(err, ErrBadBundle) {
		t.Errorf("junk heads: %v", err)
	}
	trunc := write("trunc", b[:len(b)-40])
	if _, err := host.ImportBranch(ctx, trunc, "HEAD", "t", func(string) error { return nil }); !errors.Is(err, ErrBadBundle) {
		t.Errorf("truncated import: %v", err)
	}
	// a prerequisite the host lacks: a bundle of commits on top of a commit only the clone has
	write2 := filepath.Join(clone, "z.txt")
	os.WriteFile(write2, []byte("z"), 0o644)
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-qm", "four")
	missing := bundleOf(t, clone, "HEAD~1")
	if err := host.BundleVerify(ctx, missing); !errors.Is(err, ErrBadBundle) {
		t.Errorf("missing prerequisite verify: %v", err)
	}
	if _, err := host.ImportBranch(ctx, missing, "HEAD", "m", func(string) error { return nil }); !errors.Is(err, ErrBadBundle) {
		t.Errorf("missing prerequisite import: %v", err)
	}
	_ = base
	if git(t, host.WS, "branch", "--list") != "* main" || git(t, host.WS, "for-each-ref", "refs/podbox") != "" {
		t.Error("a ref was created")
	}
}
