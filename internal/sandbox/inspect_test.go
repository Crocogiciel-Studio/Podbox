package sandbox

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

const testBase = "0123456789abcdef0123456789abcdef01234567"

func nul(parts ...string) []byte { return []byte(strings.Join(parts, "\x00") + "\x00") }

func raw(mode, status string, paths ...string) string {
	return ":" + mode + " " + mode + " 1111111 2222222 " + status + "\x00" + strings.Join(paths, "\x00") + "\x00"
}

func TestParseChanges(t *testing.T) {
	out := []byte(raw("100644", "M", "a b.txt") + raw("100644", "A", "new\nline.txt") + raw("100644", "D", "gone") +
		raw("100644", "R100", "old.go", "new.go") + raw("100644", "A", "img.png") + raw("100644", "A", "x\x1b[31m.txt") +
		raw("100644", "T", "link") + raw("160000", "A", "nested") + raw("100644", "Z", "odd") +
		"2\t1\ta b.txt\x00" + "1\t0\tnew\nline.txt\x00" + "0\t3\tgone\x00" + "0\t0\t\x00old.go\x00new.go\x00" +
		"-\t-\timg.png\x00" + "1\t0\tx\x1b[31m.txt\x00" + "0\t0\tlink\x00" + "1\t0\tnested\x00" + "0\t0\todd\x00")
	others := nul("new\nline.txt", "img.png")
	got, err := parseChanges(out, others)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileChange{
		{Path: "a b.txt", Status: "modified", Added: 2, Deleted: 1},
		{Path: "new?line.txt", Status: "added", Added: 1, Untracked: true},
		{Path: "gone", Status: "deleted", Deleted: 3},
		{Path: "new.go", OldPath: "old.go", Status: "renamed"},
		{Path: "img.png", Status: "added", Binary: true, Untracked: true},
		{Path: "x<CSI>31m.txt", Status: "added", Added: 1},
		{Path: "link", Status: "typechange"},
		{Path: "nested", Status: "added", Added: 1, Gitlink: true},
		{Path: "odd", Status: "unknown"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseChangesEmptyAndInconsistent(t *testing.T) {
	if got, err := parseChanges(nil, nil); err != nil || got == nil || len(got) != 0 {
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
		if _, err := parseChanges([]byte(in), nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// gitRT answers the git commands of Diff.
type gitRT struct {
	runtime.Runtime
	state  string
	specs  []runtime.ExecSpec
	out    map[string][]byte // git subcommand -> stdout
	code   int
	big    bool
	stderr string
}

func (g *gitRT) ContainerInspect(context.Context, string) (runtime.Container, error) {
	if g.state == "" {
		return runtime.Container{}, runtime.ErrNotFound
	}
	return runtime.Container{State: g.state}, nil
}

func sub(argv []string) string {
	if argv[0] == "timeout" {
		argv = argv[4:]
	}
	for i, a := range argv {
		if a == "-c" {
			continue
		}
		if i > 0 && argv[i-1] == "-c" {
			continue
		}
		if a != "git" {
			return a
		}
	}
	return ""
}

func (g *gitRT) Exec(_ context.Context, _ string, s runtime.ExecSpec) (int, error) {
	g.specs = append(g.specs, s)
	if g.big && sub(s.Command) == "diff" && contains(s.Command, "--binary") {
		_, err := s.Stdout.Write(make([]byte, maxOutput+1))
		return -1, err
	}
	key := sub(s.Command)
	if key == "diff" && contains(s.Command, "--raw") {
		key = "raw"
	} else if key == "diff" {
		key = "patch"
	}
	if key == "ls-files" && contains(s.Command, "--ignored") {
		key = "ignored"
	}
	if g.code != 0 && s.Command[0] != "rm" {
		io.WriteString(s.Stderr, g.stderr)
		return g.code, nil
	}
	s.Stdout.Write(g.out[key])
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

func TestDiffRefusals(t *testing.T) {
	ctx := context.Background()
	shared := cloneMetaFor()
	shared.Mode = ""
	if _, err := newDiffManager(t, shared, &gitRT{state: "running"}).Diff(ctx, "s", true); !errors.Is(err, ErrNotClone) {
		t.Errorf("shared: %v", err)
	}
	if _, err := newDiffManager(t, Meta{}, &gitRT{state: "running"}).Diff(ctx, "s", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	for _, st := range []string{"", "exited"} {
		rt := &gitRT{state: st}
		_, err := newDiffManager(t, cloneMetaFor(), rt).Diff(ctx, "s", true)
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
	if _, err := newDiffManager(t, bad, rt).Diff(ctx, "s", true); err == nil || len(rt.specs) != 0 {
		t.Errorf("invalid base accepted: %v", err)
	}
}

func diffOut() map[string][]byte {
	return map[string][]byte{
		"raw": []byte(raw("100644", "M", "a.txt") + "1\t1\ta.txt\x00"), "ls-files": nul("u.txt"),
		"ignored": nul("node_modules/", "x.log"), "patch": []byte("PATCH\x1b[31m\n"),
	}
}

func TestDiffCommands(t *testing.T) {
	rt := &gitRT{state: "running", out: diffOut()}
	m := newDiffManager(t, cloneMetaFor(), rt)
	res, err := m.Diff(context.Background(), "s", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Patch != "PATCH<CSI>31m\n" || len(res.Files) != 1 || res.Base != testBase || res.Ignored != 2 || res.IgnoredCapped {
		t.Errorf("result %+v", res)
	}
	var idx string
	for _, s := range rt.specs {
		if s.User != AgentUser || s.Workdir != "/p/proj" || s.Stdin != nil || s.Interactive || s.TTY {
			t.Errorf("exec spec %+v", s)
		}
		if s.Command[0] == "rm" {
			if !strings.HasPrefix(s.Command[2], "/tmp/podbox-diff-") {
				t.Errorf("rm %v", s.Command)
			}
			idx = s.Command[2]
			continue
		}
		j := strings.Join(s.Command, " ")
		if !strings.HasPrefix(j, "timeout -k 5 120 git -c safe.directory=/p/proj") {
			t.Errorf("not bounded: %s", j)
		}
		for _, want := range []string{"core.pager=cat", "diff.external=", "core.quotepath=off", "core.hooksPath=/dev/null", "core.sparseCheckout=false", "diff.ignoreSubmodules=none", "core.fsmonitor=false"} {
			if !strings.Contains(j, want) {
				t.Errorf("%s lacks %s", j, want)
			}
		}
		for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_NO_REPLACE_OBJECTS": "1", "GIT_ATTR_NOSYSTEM": "1"} {
			if s.Env[k] != v {
				t.Errorf("%s: env %s=%q", j, k, s.Env[k])
			}
		}
		switch sub(s.Command) {
		case "ls-files": // the agent's own index, read only
			if s.Env["GIT_INDEX_FILE"] != "" {
				t.Errorf("ls-files uses %s", s.Env["GIT_INDEX_FILE"])
			}
		case "diff":
			if !strings.Contains(j, "--no-ext-diff --no-textconv --no-color --ignore-submodules=none") || !strings.HasSuffix(j, testBase+" --") {
				t.Errorf("diff %s", j)
			}
			fallthrough
		default:
			if !strings.HasPrefix(s.Env["GIT_INDEX_FILE"], "/tmp/podbox-diff-") {
				t.Errorf("%s runs on the agent's index", j)
			}
		}
	}
	if idx == "" || rt.specs[len(rt.specs)-1].Command[0] != "rm" || idx != rt.specs[0].Env["GIT_INDEX_FILE"] {
		t.Errorf("temporary index %q not removed last", idx)
	}
	rt.specs = nil
	if res, err = m.Diff(context.Background(), "s", false); err != nil || res.Patch != "" {
		t.Fatal(res, err)
	}
	for _, s := range rt.specs {
		if contains(s.Command, "--binary") {
			t.Error("--stat asked for the patch")
		}
	}
	m.GitTimeout = 7 * time.Second
	rt.specs = nil
	m.Diff(context.Background(), "s", false)
	if rt.specs[0].Command[3] != "7" {
		t.Errorf("timeout %v", rt.specs[0].Command[:4])
	}
}

func TestDiffCapTimeoutAndFailure(t *testing.T) {
	ctx := context.Background()
	_, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", big: true, out: diffOut()}).Diff(ctx, "s", true)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("cap: %v", err)
	}
	for _, code := range []int{124, 137} {
		_, err = newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", code: code}).Diff(ctx, "s", true)
		if !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "timed out after 120s") || !strings.Contains(err.Error(), "hostile filter or hook") {
			t.Errorf("exit %d: %v", code, err)
		}
	}
	_, err = newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", code: 128, stderr: "fatal: bad\x1b]0;x\x07 thing\n"}).Diff(ctx, "s", true)
	if err == nil || !strings.Contains(err.Error(), "git read-tree: exit status 128: fatal: bad<OSC>0;x thing") || strings.ContainsRune(err.Error(), 0x1b) || strings.Contains(err.Error(), "git exited") {
		t.Errorf("failure: %v", err)
	}
}

func TestIgnoredCountIsCapped(t *testing.T) {
	out := diffOut()
	out["ignored"] = []byte(strings.Repeat("x/\x00", maxIgnoredList/2)) // 2 bytes more than fits
	res, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", out: out}).Diff(context.Background(), "s", false)
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
