package sandbox

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

const testBase = "0123456789abcdef0123456789abcdef01234567"

func nul(parts ...string) []byte { return []byte(strings.Join(parts, "\x00") + "\x00") }

func TestParseChanges(t *testing.T) {
	status := nul("M", "a b.txt", "A", "new\nline.txt", "D", "gone", "R100", "old.go", "new.go", "A", "img.png", "A", "x\x1b[31m.txt", "T", "link")
	numstat := nul("2\t1\ta b.txt", "1\t0\tnew\nline.txt", "0\t3\tgone", "0\t0\t", "old.go", "new.go", "-\t-\timg.png", "1\t0\tx\x1b[31m.txt", "0\t0\tlink")
	others := nul("new\nline.txt", "img.png")
	got := parseChanges(status, numstat, others)
	want := []FileChange{
		{Path: "a b.txt", Status: "modified", Added: 2, Deleted: 1},
		{Path: "new?line.txt", Status: "added", Added: 1, Untracked: true},
		{Path: "gone", Status: "deleted", Deleted: 3},
		{Path: "new.go", OldPath: "old.go", Status: "renamed"},
		{Path: "img.png", Status: "added", Binary: true, Untracked: true},
		{Path: "x.txt", Status: "added", Added: 1},
		{Path: "link", Status: "typechange"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseChangesEmptyAndTruncated(t *testing.T) {
	if got := parseChanges(nil, nil, nil); got == nil || len(got) != 0 {
		t.Errorf("empty: %#v", got)
	}
	// truncated output never panics
	parseChanges(nul("R100", "old"), nul("0\t0\t", "old"), nil)
	parseChanges(nul("M"), []byte("1\t2"), nil)
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
	if key == "diff" && contains(s.Command, "--name-status") {
		key = "name-status"
	} else if key == "diff" && contains(s.Command, "--numstat") {
		key = "numstat"
	} else if key == "diff" {
		key = "patch"
	}
	if g.code != 0 {
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

func TestDiffCommands(t *testing.T) {
	rt := &gitRT{state: "running", out: map[string][]byte{
		"name-status": nul("M", "a.txt"), "numstat": nul("1\t1\ta.txt"), "ls-files": nul("u.txt"), "patch": []byte("PATCH\x1b[31m\n"),
	}}
	m := newDiffManager(t, cloneMetaFor(), rt)
	res, err := m.Diff(context.Background(), "s", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Patch != "PATCH\n" || len(res.Files) != 1 || res.Base != testBase {
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
		for _, want := range []string{"git -c safe.directory=/p/proj", "core.pager=cat", "diff.external=", "core.quotepath=off"} {
			if !strings.Contains(j, want) {
				t.Errorf("%s lacks %s", j, want)
			}
		}
		if s.Env["GIT_CONFIG_GLOBAL"] != "/dev/null" || s.Env["GIT_CONFIG_NOSYSTEM"] != "1" {
			t.Errorf("host-like config not disabled: %v", s.Env)
		}
		switch sub(s.Command) {
		case "ls-files": // the agent's own index, read only
			if s.Env["GIT_INDEX_FILE"] != "" {
				t.Errorf("ls-files uses %s", s.Env["GIT_INDEX_FILE"])
			}
		case "diff":
			if !strings.Contains(j, "--no-ext-diff --no-textconv --no-color") || !strings.HasSuffix(j, testBase) {
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
}

func TestDiffCapAndFailure(t *testing.T) {
	_, err := newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", big: true}).Diff(context.Background(), "s", true)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("cap: %v", err)
	}
	_, err = newDiffManager(t, cloneMetaFor(), &gitRT{state: "running", code: 128, stderr: "fatal: bad\x1b]0;x\x07 thing\n"}).Diff(context.Background(), "s", true)
	if err == nil || !strings.Contains(err.Error(), "fatal: bad thing") || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("failure: %v", err)
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
	q := &capWriter{max: 1, quiet: true}
	if n, err := q.Write([]byte("abc")); n != 3 || err != nil || q.buf.Len() != 0 {
		t.Error("quiet writer")
	}
}
