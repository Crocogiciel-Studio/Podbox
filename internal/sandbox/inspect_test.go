package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

const testBase = "0123456789abcdef0123456789abcdef01234567"

func nul(parts ...string) []byte { return []byte(strings.Join(parts, "\x00") + "\x00") }

// gitRT answers the git commands of Diff.
type gitRT struct {
	runtime.Runtime
	state  string
	specs  []runtime.ExecSpec
	stdin  []byte            // what the forced add received
	out    map[string][]byte // see key
	code   int
	bigAll bool // the patch is many files of text past the overall cap
	stderr string
}

func (g *gitRT) ContainerInspect(context.Context, string) (runtime.Container, error) {
	if g.state == "" {
		return runtime.Container{}, runtime.ErrNotFound
	}
	return runtime.Container{State: g.state}, nil
}

// gitCmd is the git subcommand and its arguments, without the timeout and -c options.
func gitCmd(argv []string) []string {
	if argv[0] == "timeout" {
		argv = argv[4:]
	}
	var out []string
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-c" {
			i++
			continue
		}
		out = append(out, argv[i])
	}
	return out
}

func sub(argv []string) string {
	if c := gitCmd(argv); len(c) > 1 {
		return c[1]
	}
	return argv[0]
}

// key names a command by what it asks.
func key(argv []string) string {
	c := gitCmd(argv)
	if c[0] == "rm" {
		return "rm"
	}
	switch c[1] {
	case "ls-files":
		switch {
		case contains(c, "--deleted"):
			if contains(c, "--cached") {
				panic("ls-files --cached --deleted lists every cached file")
			}
			return "deleted"
		case contains(c, "--cached"):
			return "cached"
		case contains(c, "--ignored"):
			return "ignored"
		case contains(c, "--exclude-standard"):
			return "untracked"
		}
		return "others"
	case "ls-tree":
		if contains(c, "HEAD") {
			return "head"
		}
		return "basetree"
	case "config":
		return "config"
	case "check-attr":
		return "check-attr"
	case "add":
		if contains(c, "-f") {
			return "addforce"
		}
		return "addall"
	case "diff":
		switch {
		case contains(c, "--raw"):
			return "raw"
		case contains(c, "--numstat"):
			return "plain"
		}
		return "patch"
	}
	return c[1]
}

func (g *gitRT) Exec(_ context.Context, _ string, s runtime.ExecSpec) (int, error) {
	g.specs = append(g.specs, s)
	k := key(s.Command)
	if g.bigAll && k == "patch" {
		for i := 0; i < 10; i++ {
			if _, err := s.Stdout.Write([]byte(section(fmt.Sprintf("f%d", i), "100644", strings.Repeat("x", maxFilePatch)))); err != nil {
				return -1, err
			}
		}
		return 0, nil
	}
	if k == "config" && len(g.out["config"]) == 0 && g.code == 0 {
		return 1, nil // no filter configured
	}
	if g.code != 0 && k != "rm" {
		io.WriteString(s.Stderr, g.stderr)
		return g.code, nil
	}
	if k == "addforce" {
		g.stdin, _ = io.ReadAll(s.Stdin)
	}
	s.Stdout.Write(g.out[k])
	return 0, nil
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func newDiffManager(t *testing.T, meta Meta, rt *gitRT) *Manager {
	t.Helper()
	st := Store{Root: filepath.Join(t.TempDir(), "sandboxes")}
	if meta.Name != "" {
		if err := st.Save(meta); err != nil {
			t.Fatal(err)
		}
	}
	return &Manager{RT: rt, Store: st, GOOS: "linux"}
}

func cloneMetaFor() Meta {
	return Meta{Name: "s", Agent: "shell", Workspace: "/p/proj", Mode: ModeClone, Base: testBase}
}

func section(path, mode, body string) string {
	return "diff --git a/" + path + " b/" + path + "\nnew file mode " + mode + "\nindex 0000000..1111111\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+" + body + "\n"
}

func raw(mode, status string, paths ...string) string {
	return ":" + mode + " " + mode + " 1111111 2222222 " + status + "\x00" + strings.Join(paths, "\x00") + "\x00"
}

func diffOut() map[string][]byte {
	return map[string][]byte{
		"cached": nul("a.txt", "hidden.txt"), "deleted": nil, "head": nul("a.txt", "hidden.txt"), "basetree": nul("a.txt"),
		"untracked": nul("u.txt"), "ignored": nul("node_modules/", "x.log"),
		"raw":   []byte(raw("100644", "M", "a.txt") + raw("100644", "A", "hidden.txt") + "1\t1\ta.txt\x00" + "1\t0\thidden.txt\x00"),
		"plain": []byte("1\t1\ta.txt\x00" + "1\t0\thidden.txt\x00"),
		"patch": []byte(section("a.txt", "100644", "x\x1b[31m") + section("hidden.txt", "100644", "h")),
	}
}

func TestDiffRefusals(t *testing.T) {
	ctx := context.Background()
	shared := cloneMetaFor()
	shared.Mode = ""
	if _, err := newDiffManager(t, shared, &gitRT{state: "running"}).Diff(ctx, "s", DiffOptions{Patch: true}); !errors.Is(err, ErrNotClone) {
		t.Errorf("shared: %v", err)
	}
	if _, err := newDiffManager(t, Meta{}, &gitRT{state: "running"}).Diff(ctx, "s", DiffOptions{Patch: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	for _, st := range []string{"", "exited"} {
		rt := &gitRT{state: st}
		_, err := newDiffManager(t, cloneMetaFor(), rt).Diff(ctx, "s", DiffOptions{Patch: true})
		if !errors.Is(err, ErrNotRunning) || !strings.Contains(err.Error(), "podbox start s") {
			t.Errorf("state %q: %v", st, err)
		}
		if len(rt.specs) != 0 {
			t.Errorf("state %q: git ran in a stopped sandbox", st)
		}
	}
	bad := cloneMetaFor()
	bad.Base = "--output=/etc/x"
	rt := &gitRT{state: "running"}
	if _, err := newDiffManager(t, bad, rt).Diff(ctx, "s", DiffOptions{Patch: true}); err == nil || len(rt.specs) != 0 {
		t.Errorf("invalid base accepted: %v", err)
	}
}

func TestDiffCommands(t *testing.T) {
	rt := &gitRT{state: "running", out: diffOut()}
	m := newDiffManager(t, cloneMetaFor(), rt)
	res, err := m.Diff(context.Background(), "s", DiffOptions{Patch: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Patch == nil || !strings.Contains(*res.Patch, "+x<CSI>31m\n") || len(res.Files) != 2 || res.Base != testBase || res.Name != "s" || res.Schema != DiffSchema || res.Ignored != 2 || res.IgnoredCapped {
		t.Errorf("result %+v", res)
	}
	// a path tracked outside the base is forced into the temporary index, literally
	if string(rt.stdin) != "hidden.txt\x00" {
		t.Errorf("forced paths %q", rt.stdin)
	}
	if res.Files[1].Path != "hidden.txt" || res.Files[1].Untracked || res.Files[0].Untracked {
		t.Errorf("files %+v", res.Files)
	}
	var idx string
	var order []string
	for _, s := range rt.specs {
		if s.User != AgentUser || s.Workdir != "/p/proj" || s.TTY {
			t.Errorf("exec spec %+v", s)
		}
		k := key(s.Command)
		order = append(order, k)
		if k == "rm" {
			idx = s.Command[2]
			if !strings.HasPrefix(idx, "/tmp/podbox-diff-") {
				t.Errorf("rm %v", s.Command)
			}
			continue
		}
		if (s.Stdin != nil) != (k == "addforce" || k == "check-attr") || s.Interactive != (s.Stdin != nil) {
			t.Errorf("%s: stdin %v", k, s.Stdin)
		}
		j := strings.Join(s.Command, " ")
		if !strings.HasPrefix(j, "timeout -k 5 120 git -c safe.directory=/p/proj") || contains(s.Command, "--binary") {
			t.Errorf("bad command: %s", j)
		}
		for _, want := range []string{"core.pager=cat", "diff.external=", "core.quotepath=off", "core.hooksPath=/dev/null", "core.sparseCheckout=false", "diff.ignoreSubmodules=none", "core.fsmonitor=false", "core.attributesFile=/dev/null"} {
			if !strings.Contains(j, want) {
				t.Errorf("%s lacks %s", j, want)
			}
		}
		for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_NO_REPLACE_OBJECTS": "1", "GIT_ATTR_NOSYSTEM": "1"} {
			if s.Env[k] != v {
				t.Errorf("%s: env %s=%q", j, k, s.Env[k])
			}
		}
		onAgentIndex := map[string]bool{"config": true, "cached": true, "deleted": true, "head": true, "basetree": true, "others": true}
		hasTemp := strings.HasPrefix(s.Env["GIT_INDEX_FILE"], "/tmp/podbox-diff-")
		if onAgentIndex[k] == hasTemp {
			t.Errorf("%s: GIT_INDEX_FILE=%q", k, s.Env["GIT_INDEX_FILE"])
		}
		if k == "addforce" && (s.Env["GIT_LITERAL_PATHSPECS"] != "1" || !contains(s.Command, "-f") || !contains(s.Command, "-N")) {
			t.Errorf("forced add %v %v", s.Command, s.Env)
		}
		if strings.HasPrefix(k, "raw") || k == "plain" || k == "patch" {
			if s.Command[len(s.Command)-1] != "--" || s.Command[len(s.Command)-2] != testBase || !contains(s.Command, "--no-ext-diff") || !contains(s.Command, "--ignore-submodules=none") {
				t.Errorf("diff %s", j)
			}
			if (k != "plain") != contains(s.Command, "--text") {
				t.Errorf("%s: --text %v", k, contains(s.Command, "--text"))
			}
		}
	}
	// the temporary index is built from the base, then forced, then listed, then the rest added
	want := []string{"config", "read-tree", "cached", "deleted", "head", "basetree", "addforce", "untracked", "addall", "raw", "plain", "patch", "check-attr", "ignored", "rm"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Errorf("order %v", order)
	}
	if rt.specs[1].Env["GIT_INDEX_FILE"] != idx {
		t.Errorf("temporary index %q not the one removed", idx)
	}
	rt.specs = nil
	if res, err = m.Diff(context.Background(), "s", DiffOptions{}); err != nil || res.Patch != nil {
		t.Fatal(res, err)
	}
	m.GitTimeout = 7 * time.Second
	rt.specs = nil
	m.Diff(context.Background(), "s", DiffOptions{})
	if rt.specs[0].Command[3] != "7" {
		t.Errorf("timeout %v", rt.specs[0].Command[:4])
	}
}

// A path of HEAD that the agent dropped from its index is kept when it is on disk.
func TestDiffHeadOnlyPaths(t *testing.T) {
	out := diffOut()
	out["cached"] = nul("a.txt")
	out["others"] = nul("hidden.txt")
	rt := &gitRT{state: "running", out: out}
	if _, err := newDiffManager(t, cloneMetaFor(), rt).Diff(context.Background(), "s", DiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if string(rt.stdin) != "hidden.txt\x00" {
		t.Errorf("forced %q", rt.stdin)
	}
	// not on disk: not forced
	out["others"] = nil
	rt = &gitRT{state: "running", out: out}
	newDiffManager(t, cloneMetaFor(), rt).Diff(context.Background(), "s", DiffOptions{})
	if rt.stdin != nil {
		t.Errorf("forced %q", rt.stdin)
	}
	// deleted from the worktree but in the index: not forced
	out = diffOut()
	out["deleted"] = nul("hidden.txt")
	rt = &gitRT{state: "running", out: out}
	newDiffManager(t, cloneMetaFor(), rt).Diff(context.Background(), "s", DiffOptions{})
	if rt.stdin != nil {
		t.Errorf("forced %q", rt.stdin)
	}
}

// Attributes cannot make text unreadable, and a real binary stays binary.
func TestDiffBinaryByContent(t *testing.T) {
	nulSection := section("bin.dat", "100644", "\x00\x01bin")
	out := diffOut()
	out["raw"] = []byte(raw("100644", "A", "code.py") + raw("100644", "A", "bin.dat") + raw("100644", "R100", "old.bin", "new.bin") +
		"1\t0\tcode.py\x00" + "1\t0\tbin.dat\x00" + "0\t0\t\x00old.bin\x00new.bin\x00")
	out["plain"] = []byte("-\t-\tcode.py\x00" + "-\t-\tbin.dat\x00" + "-\t-\t\x00old.bin\x00new.bin\x00") // git says all three are binary
	renamed := "diff --git a/old.bin b/new.bin\nsimilarity index 100%\nrename from old.bin\nrename to new.bin\n"
	out["patch"] = []byte(section("code.py", "100644", "print(1)") + nulSection + renamed)
	rt := &gitRT{state: "running", out: out}
	res, err := newDiffManager(t, cloneMetaFor(), rt).Diff(context.Background(), "s", DiffOptions{Patch: true})
	if err != nil {
		t.Fatal(err)
	}
	code, bin, ren := res.Files[0], res.Files[1], res.Files[2]
	if code.Binary || !code.BinaryAttr || code.Added != 1 {
		t.Errorf("text marked binary by attributes: %+v", code)
	}
	if !bin.Binary || bin.BinaryAttr || bin.Added != 0 {
		t.Errorf("binary by content: %+v", bin)
	}
	if !ren.Binary || ren.BinaryAttr {
		t.Errorf("a binary renamed without content change keeps git's verdict: %+v", ren)
	}
	if !bin.BinaryHidden || code.BinaryHidden {
		t.Errorf("hidden: %+v %+v", bin, code)
	}
	p := *res.Patch
	if !strings.Contains(p, "--show-binary") || !strings.Contains(p, `starts with "<U+0000><U+0001>bin`) {
		t.Errorf("stub:\n%s", p)
	}
	shown, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", out: out}).Diff(context.Background(), "s", DiffOptions{Patch: true, ShowBinary: true})
	if err != nil || shown.Files[1].BinaryHidden || !shown.Files[1].Binary || !strings.Contains(*shown.Patch, "+<U+0000><U+0001>bin") {
		t.Errorf("--show-binary: %+v %v", shown.Files[1], err)
	}
	if !strings.Contains(p, "+print(1)") || strings.Contains(p, "+<U+0000>") || !strings.Contains(p, "diff --git a/bin.dat b/bin.dat\nnew file mode 100644\n") || strings.Contains(p, "@@ -0,0 +1 @@\n+<U+0000>") {
		t.Errorf("patch:\n%s", p)
	}
	if strings.Count(p, "diff --git") != 3 {
		t.Errorf("patch:\n%s", p)
	}
}

// A big file does not block the others; many big files hit the overall cap.
func TestDiffPerFileCap(t *testing.T) {
	out := diffOut()
	big := section("a.txt", "100644", strings.Repeat("y", maxFilePatch+10))
	bigBin := section("hidden.txt", "100644", "\x00"+strings.Repeat("z", maxFilePatch+10))
	out["patch"] = []byte(big + bigBin)
	rt := &gitRT{state: "running", out: out}
	res, err := newDiffManager(t, cloneMetaFor(), rt).Diff(context.Background(), "s", DiffOptions{Patch: true, ShowBinary: true})
	if err != nil {
		t.Fatal(err)
	}
	a, h := res.Files[0], res.Files[1]
	if !a.TooLarge || a.Binary || !h.Binary || h.BinaryHidden || h.TooLarge {
		t.Errorf("flags %+v %+v", a, h)
	}
	p := *res.Patch
	if !strings.Contains(p, "Too large: ") || strings.Contains(p, "yyyyyyyyyy") || !strings.Contains(p, "... truncated, ") || strings.Count(p, "z") > maxBinaryShown {
		t.Errorf("patch has %d bytes", len(p))
	}
	if len(p) > 2*maxBinaryShown {
		t.Errorf("patch has %d bytes", len(p))
	}
	// --stat: the big file is not "too large", it has no patch at all
	res, err = newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", out: out}).Diff(context.Background(), "s", DiffOptions{})
	if err != nil || res.Files[0].TooLarge || !res.Files[1].Binary || !res.Files[1].BinaryHidden || res.Patch != nil {
		t.Errorf("%+v %v", res.Files, err)
	}
}

func TestSectionWriter(t *testing.T) {
	p := section("a", "100644", "one") + section("b", "100644", "t\x00wo") + section("c", "100644", strings.Repeat("k", 100))
	for _, chunk := range []int{1, 3, 11, 12, 13, 50, len(p)} {
		w := newSectionWriter(60, 1<<20)
		for i := 0; i < len(p); i += chunk {
			end := i + chunk
			if end > len(p) {
				end = len(p)
			}
			w.Write([]byte(p[i:end]))
		}
		w.finish()
		if len(w.secs) != 3 || w.secs[0].nul || !w.secs[1].nul || w.secs[2].nul {
			t.Fatalf("chunk %d: %d sections %+v", chunk, len(w.secs), w.secs)
		}
		if !bytes.HasPrefix(w.secs[1].data, []byte("diff --git a/b b/b")) || len(w.secs[2].data) != 60 || w.secs[2].size != int64(len(section("c", "100644", strings.Repeat("k", 100)))) {
			t.Errorf("chunk %d: %q %d", chunk, w.secs[1].data, w.secs[2].size)
		}
	}
	// a content line that reads like a header cannot split a section: it starts with a prefix
	w := newSectionWriter(1000, 1<<20)
	w.Write([]byte(section("a", "100644", "diff --git fake")))
	w.finish()
	if len(w.secs) != 1 {
		t.Errorf("%d sections", len(w.secs))
	}
	// the total cap
	w = newSectionWriter(1000, 100)
	if _, err := w.Write([]byte(p)); !errors.Is(err, ErrTooLarge) || !w.exceeded() {
		t.Errorf("no total cap: %v", err)
	}
}

func TestDiffFiltersAreNeutralised(t *testing.T) {
	out := diffOut()
	out["config"] = nul("filter.x.clean", "filter.a.b.required", "filter.x.process")
	out["check-attr"] = nul("a.txt", "filter", "x", "a.txt", "ident", "set", "hidden.txt", "working-tree-encoding", "utf-16", "hidden.txt", "filter", "unspecified", "a.txt", "ident", "unset")
	rt := &gitRT{state: "running", out: out}
	res, err := newDiffManager(t, cloneMetaFor(), rt).Diff(context.Background(), "s", DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Filters, ",") != "a.b,x" {
		t.Errorf("filters %v", res.Filters)
	}
	if strings.Join(res.Files[0].Conversions, ",") != "filter=x,ident" || strings.Join(res.Files[1].Conversions, ",") != "working-tree-encoding=utf-16" {
		t.Errorf("conversions %v %v", res.Files[0].Conversions, res.Files[1].Conversions)
	}
	for _, s := range rt.specs {
		k := key(s.Command)
		if k == "config" || k == "rm" {
			if s.Env["GIT_CONFIG_COUNT"] != "" {
				t.Errorf("%s runs with overrides before the filters are known", k)
			}
			continue
		}
		if s.Env["GIT_CONFIG_COUNT"] != "8" {
			t.Errorf("%s: GIT_CONFIG_COUNT=%q", k, s.Env["GIT_CONFIG_COUNT"])
			continue
		}
		got := map[string]string{}
		for i := 0; i < 8; i++ {
			got[s.Env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]] = s.Env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)]
		}
		for _, name := range []string{"x", "a.b"} {
			if got["filter."+name+".clean"] != "/bin/cat" || got["filter."+name+".smudge"] != "/bin/cat" || got["filter."+name+".required"] != "false" {
				t.Errorf("%s: filter %s not neutralised: %v", k, name, got)
			}
			if v, ok := got["filter."+name+".process"]; !ok || v != "" {
				t.Errorf("%s: process of %s: %q %v", k, name, v, ok)
			}
		}
	}
	if _, err := filterOverrides(make([]string, maxFilters+1)); err == nil {
		t.Error("too many filters accepted")
	}
}

func TestDiffJSONFields(t *testing.T) {
	out := diffOut()
	out["raw"] = []byte(":100644 100755 aaaaaaa bbbbbbb M\x00run.sh\x00:000000 120000 0000000 0000000 A\x00ln\x00:160000 000000 ccccccc 0000000 D\x00sub\x00" +
		"0\t0\trun.sh\x000\t0\tln\x000\t0\tsub\x00")
	out["plain"] = nil
	out["patch"] = []byte(section("run.sh", "100755", "x") + section("ln", "120000", "t") + section("sub", "160000", "s"))
	res, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", out: out}).Diff(context.Background(), "s", DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run, ln, sub := res.Files[0], res.Files[1], res.Files[2]
	if !run.Exec || run.Mode != "100755" || run.OldMode != "100644" || run.ID != "bbbbbbb" || run.OldID != "aaaaaaa" || run.PathB64 != "cnVuLnNo" {
		t.Errorf("exec %+v", run)
	}
	if !ln.Symlink || ln.ID != "" || ln.OldID != "" || ln.OldMode != "" {
		t.Errorf("symlink %+v", ln)
	}
	if !sub.Gitlink || sub.Status != "deleted" || sub.Mode != "160000" {
		t.Errorf("deleted gitlink %+v", sub)
	}
}

func TestNameNotes(t *testing.T) {
	n := nameNotes([]rawEntry{{path: "README"}, {path: "readme"}, {path: "café.txt"}, {path: "a/B"}, {path: "ok"}, {path: "аpple"}})
	if n.CaseCollisions != 2 || len(n.CaseExamples) != 2 || n.NonASCII != 2 || n.NonASCIIExamples[0] != "café.txt" {
		t.Errorf("%+v", n)
	}
	if n := nameNotes(nil); n.CaseCollisions != 0 || n.NonASCII != 0 {
		t.Errorf("%+v", n)
	}
}

func TestDiffCapTimeoutAndFailure(t *testing.T) {
	ctx := context.Background()
	_, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", bigAll: true, out: diffOut()}).Diff(ctx, "s", DiffOptions{Patch: true})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("cap: %v", err)
	}
	for _, code := range []int{124, 137} {
		_, err = newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", code: code}).Diff(ctx, "s", DiffOptions{Patch: true})
		if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "timed out after 120s") || !strings.Contains(err.Error(), "hostile program") {
			t.Errorf("exit %d: %v", code, err)
		}
	}
	_, err = newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", code: 128, stderr: "fatal: bad\x1b]0;x\x07 thing\n"}).Diff(ctx, "s", DiffOptions{Patch: true})
	if err == nil || !strings.Contains(err.Error(), "git config: exit status 128: fatal: bad<OSC>0;x<U+0007> thing") || strings.ContainsRune(err.Error(), 0x1b) || strings.Contains(err.Error(), "git exited") {
		t.Errorf("failure: %v", err)
	}
}

func TestIgnoredCountIsCapped(t *testing.T) {
	out := diffOut()
	out["ignored"] = []byte(strings.Repeat("x/\x00", maxIgnoredList/2))
	res, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", out: out}).Diff(context.Background(), "s", DiffOptions{})
	if err != nil || !res.IgnoredCapped || res.Ignored < 100 {
		t.Errorf("%+v %v", res.Ignored, err)
	}
}

func TestCapWriter(t *testing.T) {
	w := &capWriter{max: 4}
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("e")); !errors.Is(err, ErrTooLarge) || !w.over {
		t.Error("no cap")
	}
	q := &capWriter{max: 4, quiet: true}
	if n, err := q.Write([]byte("abcdef")); n != 6 || err != nil || q.buf.String() != "abcd" || !q.cut {
		t.Errorf("quiet writer keeps %q", q.buf.String())
	}
}

func TestParseDiffAndSections(t *testing.T) {
	out := []byte(raw("100644", "M", "a b.txt") + raw("100644", "R100", "old.go", "new.go") + raw("100644", "Z", "odd") +
		"2\t1\ta b.txt\x00" + "0\t0\t\x00old.go\x00new.go\x00" + "0\t0\todd\x00")
	raws, nums, err := parseDiff(out)
	if err != nil || len(raws) != 3 || len(nums) != 3 || raws[1].oldPath != "old.go" || nums[1].path != "new.go" {
		t.Fatalf("%+v %+v %v", raws, nums, err)
	}
	files, err := buildChanges(raws, nums, nil, nil, nil)
	if err != nil || files[2].Status != "unknown" || files[1].Status != "renamed" {
		t.Errorf("%+v %v", files, err)
	}
	if got, err := buildChanges(nil, nil, nil, nil, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty: %#v %v", got, err)
	}
	bad := map[string]string{
		"missing counts": raw("100644", "M", "a") + raw("100644", "M", "b") + "1\t1\ta\x00",
		"other path":     raw("100644", "M", "a") + "1\t1\tb\x00",
		"truncated raw":  raw("100644", "R100", "old")[:40],
		"truncated num":  raw("100644", "R100", "o", "n") + "0\t0\t\x00o\x00",
		"bad numstat":    raw("100644", "M", "a") + "garbage\x00",
	}
	for name, in := range bad {
		r, n, err := parseDiff([]byte(in))
		if err == nil {
			_, err = buildChanges(r, n, nil, nil, nil)
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
