package images

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
)

type fakeRT struct {
	runtime.Runtime
	built  []runtime.BuildSpec
	exists map[string]bool
	files  map[string]map[string]string // tag -> context files
}

func (f *fakeRT) ImageList(context.Context, string) ([]runtime.Image, error) { return nil, nil }

func (f *fakeRT) ImageExists(_ context.Context, ref string) (bool, error) { return f.exists[ref], nil }

func (f *fakeRT) ImageBuild(_ context.Context, s runtime.BuildSpec) error {
	f.built = append(f.built, s)
	got := map[string]string{}
	_ = filepath.Walk(s.Context, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(s.Context, p)
			b, _ := os.ReadFile(p)
			got[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	if f.files == nil {
		f.files = map[string]map[string]string{}
	}
	f.files[s.Tag] = got
	f.exists[s.Tag] = true
	return nil
}

func TestEnsureBuildsBaseThenAgent(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "corp.pem")
	_ = os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\nx\n"), 0o644)
	rt := &fakeRT{exists: map[string]bool{}}
	b := &Builder{RT: rt, Base: "registry.corp/node:22", CAFile: ca}
	ref, err := b.Ensure(context.Background(), Claude, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.built) != 2 || rt.built[0].BuildArgs["BASE"] != "registry.corp/node:22" {
		t.Fatalf("%+v", rt.built)
	}
	baseRef := rt.built[0].Tag
	if rt.built[1].BuildArgs["BASE"] != baseRef || rt.built[1].Tag != ref || rt.built[1].Labels[LabelAgent] != "claude" {
		t.Fatalf("%+v", rt.built[1])
	}
	if !strings.HasPrefix(rt.files[baseRef]["ca/podbox-ca.crt"], "-----BEGIN") {
		t.Fatal("CA not in base build context")
	}
	if !strings.Contains(rt.files[baseRef]["Containerfile"], "NODE_EXTRA_CA_CERTS") {
		t.Fatal("base must point node at the system bundle")
	}
	// Present images are not rebuilt.
	if _, err := b.Ensure(context.Background(), Claude, false); err != nil || len(rt.built) != 2 {
		t.Fatal("rebuilt an existing image")
	}
	// A different base changes every tag.
	b2 := &Builder{RT: rt, Base: "docker.io/library/node:22-bookworm-slim"}
	ref2, _ := b2.Ref(context.Background(), Claude)
	if ref2 == ref {
		t.Fatal("tag ignores base")
	}
}

// The base ships the URL opener; the kiro image pins and checks kiro-cli and
// turns the browser flow on.
func TestOpenerAndKiroImage(t *testing.T) {
	rt := &fakeRT{exists: map[string]bool{}}
	b := &Builder{RT: rt, Base: "docker.io/library/node:22-bookworm-slim"}
	if _, err := b.Ensure(context.Background(), Kiro, false); err != nil {
		t.Fatal(err)
	}
	base, kiro := rt.files[rt.built[0].Tag], rt.files[rt.built[1].Tag]
	if !strings.Contains(base["podbox-open.sh"], "/tmp/podbox-open") || !strings.Contains(base["Containerfile"], "/usr/local/bin/xdg-open") {
		t.Fatal("base lacks the opener")
	}
	for _, want := range []string{"sha256sum -c", "ARG KIRO_CLI_VERSION=", "/${KIRO_CLI_VERSION}/kirocli-", "DISPLAY=:0", "BROWSER=/usr/local/bin/podbox-open"} {
		if !strings.Contains(kiro["Containerfile"], want) {
			t.Errorf("kiro Containerfile lacks %q", want)
		}
	}
	if strings.Contains(kiro["Containerfile"], "latest/kirocli-") {
		t.Error("kiro-cli must be pinned, not latest")
	}
}

func TestProxyContext(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("uses the test binary as proxy binary")
	}
	rt := &fakeRT{exists: map[string]bool{}}
	b := &Builder{RT: rt, Arch: goruntime.GOARCH}
	if _, err := ProxyBinary(goruntime.GOARCH); err != nil {
		t.Skipf("test binary not static: %v", err)
	}
	ref, err := b.Ensure(context.Background(), Proxy, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rt.files[ref]["Containerfile"], "FROM scratch") || rt.files[ref]["podbox"] == "" {
		t.Fatalf("proxy context: %v", rt.files[ref]["Containerfile"])
	}
	if len(rt.built[0].BuildArgs) != 0 {
		t.Fatal("proxy image takes no base")
	}
}

func TestMissingProxyBinary(t *testing.T) {
	_, err := ProxyBinary("riscv64")
	if err == nil || !strings.Contains(err.Error(), "make cross-embed") {
		t.Fatalf("%v", err)
	}
}

func TestParseKind(t *testing.T) {
	if _, err := ParseKind("claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKind("docker"); err == nil {
		t.Fatal("accepted unknown")
	}
}
