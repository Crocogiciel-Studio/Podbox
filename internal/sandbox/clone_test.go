package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

func cloneStore(t *testing.T) Store {
	t.Helper()
	d := t.TempDir()
	return Store{Root: filepath.Join(d, "sandboxes"), ClonesRoot: filepath.Join(d, "clones"), ConfigDir: t.TempDir()}
}

func TestMetaModes(t *testing.T) {
	st := cloneStore(t)
	// A record written before clone mode has no mode: shared.
	if err := os.MkdirAll(st.Dir("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(st.Dir("old"), "meta.json"), `{"name":"old","agent":"shell","workspace":"/p","created":"2026-01-01T00:00:00Z"}`)
	m, err := st.Load("old")
	if err != nil || m.IsClone() || m.ModeName() != ModeShared || m.Guard().Clone || st.Source(m) != "/p" {
		t.Fatalf("legacy meta: %+v %v", m, err)
	}
	c := Meta{Name: "c", Agent: "shell", Workspace: "/p", Mode: ModeClone, Base: "abc"}
	if err := st.Save(c); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load("c")
	if err != nil || got.Mode != ModeClone || got.Base != "abc" || !got.IsClone() || !got.Guard().Clone {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if want := filepath.Join(st.ClonesRoot, "c"); st.Source(got) != want {
		t.Errorf("Source = %s, want %s", st.Source(got), want)
	}
	if strings.HasPrefix(st.CloneDir("c"), st.Dir("c")) {
		t.Error("the clone must live outside the sandbox directory")
	}
	write(t, filepath.Join(st.Dir("old"), "meta.json"), `{"name":"old","mode":"weird"}`)
	if _, err := st.Load("old"); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("unknown mode: %v", err)
	}
	if b, _ := json.Marshal(Meta{Name: "s"}); strings.Contains(string(b), "mode") {
		t.Errorf("shared meta writes a mode: %s", b)
	}
}

func TestCloneDirNeedsClonesRoot(t *testing.T) {
	st := Store{Root: t.TempDir()}
	if st.CloneDir("x") != "" || st.HasClone("x") || st.RemoveClone("x") != nil {
		t.Error("a store without ClonesRoot must not touch the working directory")
	}
}

func TestCloneExistsRefusesBeforeAnything(t *testing.T) {
	st := cloneStore(t)
	if err := st.cloneExists("x"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.CloneDir("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := st.cloneExists("x")
	if !errors.Is(err, ErrCloneExists) || !strings.Contains(err.Error(), "podbox rm x --volumes") {
		t.Fatalf("err = %v", err)
	}
}

// engine is a runtime with no object: only what Remove and List call.
type engine struct {
	runtime.Runtime
}

func (engine) ContainerList(context.Context, string) ([]runtime.Container, error) { return nil, nil }
func (engine) NetworkList(context.Context, string) ([]string, error)              { return nil, nil }
func (engine) NetworkRemove(context.Context, string) error                        { return runtime.ErrNotFound }
func (engine) VolumeRemove(context.Context, string) error                         { return runtime.ErrNotFound }

func TestRemoveKeepsTheCloneUntilVolumes(t *testing.T) {
	st := cloneStore(t)
	m := &Manager{RT: engine{}, Store: st, Warn: func(string, ...any) {}}
	meta := Meta{Name: "c", Agent: "shell", Workspace: "/p", Mode: ModeClone}
	if err := st.Save(meta); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(st.CloneDir("c"), "work.txt"), "agent work")

	list, err := m.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Mode != ModeClone || list[0].Clone != st.CloneDir("c") {
		t.Fatalf("list: %+v %v", list, err)
	}
	rm, err := m.Remove(context.Background(), "c", false)
	if err != nil || !rm.CloneKept || rm.ClonePath != st.CloneDir("c") {
		t.Fatalf("rm: %+v %v", rm, err)
	}
	if _, err := os.Stat(filepath.Join(st.CloneDir("c"), "work.txt")); err != nil {
		t.Fatal("rm without --volumes lost the agent's work")
	}
	if _, err := st.Load("c"); !errors.Is(err, ErrNotFound) {
		t.Fatal("sandbox state not removed")
	}

	// the orphan clone is listed and still removable
	list, err = m.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "c" || list[0].State != "missing" || list[0].Mode != ModeClone {
		t.Fatalf("orphan list: %+v %v", list, err)
	}
	rm, err = m.Remove(context.Background(), "c", true)
	if err != nil || rm.CloneKept || rm.ClonePath == "" {
		t.Fatalf("rm --volumes on the orphan: %+v %v", rm, err)
	}
	if st.HasClone("c") {
		t.Fatal("clone left after rm --volumes")
	}
	if _, err := m.Remove(context.Background(), "c", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("second rm: %v", err)
	}
}

func TestRemoveSharedReportsNoClone(t *testing.T) {
	st := cloneStore(t)
	m := &Manager{RT: engine{}, Store: st, Warn: func(string, ...any) {}}
	if err := st.Save(Meta{Name: "s", Agent: "shell", Workspace: "/p"}); err != nil {
		t.Fatal(err)
	}
	if rm, err := m.Remove(context.Background(), "s", false); err != nil || rm != (Removed{}) {
		t.Fatalf("%+v %v", rm, err)
	}
}

func TestAgentSpecOfAClone(t *testing.T) {
	m := Meta{Name: "c", Agent: "shell", Workspace: "/home/u/proj", Mode: ModeClone}
	s := agentSpec(agentInput{Meta: m, Source: "/state/clones/c", Image: "img", GOOS: "linux", EmptyDir: "/e", AgentDir: "/a"})
	argv := strings.Join(runtime.CreateArgs(s), " ")
	if !strings.Contains(argv, "--volume /state/clones/c:/home/u/proj ") || !strings.Contains(argv, "--workdir /home/u/proj") {
		t.Errorf("clone not mounted on the project path:\n%s", argv)
	}
	for _, bad := range []string{"/home/u/proj:", "/.git", "io.podbox.mode=shared"} {
		if strings.Contains(argv, bad) {
			t.Errorf("clone spec holds %q:\n%s", bad, argv)
		}
	}
	if !strings.Contains(argv, "io.podbox.mode=clone") {
		t.Errorf("no mode label:\n%s", argv)
	}
	shared := agentSpec(agentInput{Meta: Meta{Name: "s", Agent: "shell", Workspace: "/home/u/proj"}, GOOS: "linux", GitDir: true})
	if sa := strings.Join(runtime.CreateArgs(shared), " "); !strings.Contains(sa, "--volume /home/u/proj:/home/u/proj ") ||
		!strings.Contains(sa, "/home/u/proj/.git:/home/u/proj/.git") || !strings.Contains(sa, "io.podbox.mode=shared") {
		t.Errorf("shared spec changed:\n%s", sa)
	}
}

func TestMasksAndProjectFilesComeFromTheClone(t *testing.T) {
	st := cloneStore(t)
	project := t.TempDir()
	write(t, filepath.Join(project, ".sandboxignore"), "/host-only\n")
	m := Meta{Name: "c", Agent: "shell", Workspace: project, Mode: ModeClone}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	clone := st.CloneDir("c")
	write(t, filepath.Join(clone, ".sandboxignore"), "/.env\n")
	write(t, filepath.Join(clone, ".env"), "SECRET=1")
	write(t, filepath.Join(clone, ".podbox", "policy.yaml"), "rules:\n  - host: example.org\n    allow: true\n")
	c, err := st.Snapshot(m)
	if err != nil || len(c.IgnoreAdded) != 1 || c.IgnoreAdded[0] != "/.env" {
		t.Fatalf("snapshot read the project, not the clone: %+v %v", c, err)
	}
	res, err := st.ResolveMasks(m)
	if err != nil || len(res.Masks) != 1 || res.Masks[0].Rel != ".env" {
		t.Fatalf("masks: %+v %v", res.Masks, err)
	}
	if d := st.Drift(m); len(d) != 0 {
		t.Errorf("drift %v", d)
	}
	write(t, filepath.Join(project, ".sandboxignore"), "/changed\n") // the host file is not watched
	if d := st.Drift(m); len(d) != 0 {
		t.Errorf("drift follows the host project: %v", d)
	}
	// no fingerprint for a clone, and nothing to compare
	if err := st.RecordGit(m); err != nil || st.HasGitPrint("c") {
		t.Errorf("clone recorded a fingerprint: %v", err)
	}
	if g, err := st.CheckGit(m); g != nil || err != nil {
		t.Errorf("CheckGit = %v %v", g, err)
	}
}
