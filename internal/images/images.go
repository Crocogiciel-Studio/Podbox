// Package images builds podbox images from embedded definitions.
package images

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/Crocogiciel-Studio/podbox/internal/runtime"
	"github.com/Crocogiciel-Studio/podbox/internal/version"
)

//go:embed defs
var defs embed.FS

// Kind names an image.
type Kind string

const (
	Proxy  Kind = "proxy"
	Base   Kind = "base"
	Claude Kind = "claude"
	Kiro   Kind = "kiro"
	Shell  Kind = "shell"
)

// Kinds lists every buildable image.
var Kinds = []Kind{Proxy, Base, Claude, Kiro, Shell}

// Experimental images are built from unverified definitions.
var Experimental = map[Kind]bool{Kiro: true}

// Labels set on every podbox image.
const (
	LabelImage        = "io.podbox.image"
	LabelAgent        = "io.podbox.agent"
	LabelVersion      = "io.podbox.version"
	LabelExperimental = "io.podbox.experimental"
)

// ParseKind validates an image name.
func ParseKind(s string) (Kind, error) {
	for _, k := range Kinds {
		if string(k) == s {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown image %q: want one of %v", s, Kinds)
}

func (k Kind) agent() bool { return k == Claude || k == Kiro || k == Shell }

// Builder resolves and builds images on one engine.
type Builder struct {
	RT       runtime.Runtime
	Base     string // base image for the base layer
	CAFile   string // optional PEM trusted in agent images
	Arch     string // engine architecture (amd64, arm64)
	Progress io.Writer
	// GOOS and Getenv give the host proxy variables to builds; a nil
	// Getenv leaves podman's defaults.
	GOOS   string
	Getenv func(string) string
}

func def(name string) []byte {
	b, err := defs.ReadFile("defs/" + name)
	if err != nil {
		panic(err) // embedded at build time
	}
	return b
}

func containerfile(k Kind) []byte { return def(string(k) + ".Containerfile") }

var tagUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func tag(k Kind, parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	v := tagUnsafe.ReplaceAllString(version.Version, "_")
	if len(v) > 64 {
		v = v[:64]
	}
	return fmt.Sprintf("localhost/podbox-%s:%s-%s", k, v, hex.EncodeToString(h.Sum(nil))[:12])
}

// inputs returns the build context files and the base image argument.
func (b *Builder) inputs(ctx context.Context, k Kind) (files map[string][]byte, baseArg string, err error) {
	files = map[string][]byte{"Containerfile": containerfile(k)}
	switch {
	case k == Proxy:
		bin, err := ProxyBinary(b.Arch)
		if err != nil {
			return nil, "", err
		}
		files["podbox"] = bin
	case k == Base:
		files["entrypoint.sh"] = def("entrypoint.sh")
		files["apt-install.sh"] = def("apt-install.sh")
		files["ca/.keep"] = nil
		if b.CAFile != "" {
			ca, err := os.ReadFile(b.CAFile)
			if err != nil {
				return nil, "", fmt.Errorf("ca_file: %w", err)
			}
			files["ca/podbox-ca.crt"] = ca
		}
		baseArg = b.Base
	case k.agent():
		if baseArg, err = b.Ref(ctx, Base); err != nil {
			return nil, "", err
		}
	}
	return files, baseArg, nil
}

// Ref is the content-addressed tag of an image: definitions, base, CA or
// proxy binary changes produce a new tag and therefore a rebuild.
func (b *Builder) Ref(ctx context.Context, k Kind) (string, error) {
	files, baseArg, err := b.inputs(ctx, k)
	if err != nil {
		return "", err
	}
	parts := [][]byte{[]byte(baseArg)}
	for _, name := range sortedNames(files) {
		parts = append(parts, []byte(name), files[name])
	}
	return tag(k, parts...), nil
}

// Ensure returns the image ref, building it (and its base) when missing or
// when force is set.
func (b *Builder) Ensure(ctx context.Context, k Kind, force bool) (string, error) {
	if k.agent() {
		if _, err := b.Ensure(ctx, Base, false); err != nil {
			return "", err
		}
	}
	ref, err := b.Ref(ctx, k)
	if err != nil {
		return "", err
	}
	if !force {
		ok, err := b.RT.ImageExists(ctx, ref)
		if err != nil || ok {
			return ref, err
		}
	}
	files, baseArg, err := b.inputs(ctx, k)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "podbox-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, data, 0o755); err != nil {
			return "", err
		}
	}
	labels := map[string]string{LabelImage: string(k), LabelVersion: version.Version}
	if k.agent() {
		labels[LabelAgent] = string(k)
	}
	if Experimental[k] {
		labels[LabelExperimental] = "true"
	}
	spec := runtime.BuildSpec{
		Tag: ref, Context: dir, File: filepath.Join(dir, "Containerfile"),
		Labels: labels, Stdout: b.Progress, Stderr: b.Progress,
	}
	if baseArg != "" {
		spec.BuildArgs = map[string]string{"BASE": baseArg}
	}
	buildProxy(&spec, b.GOOS, b.Getenv)
	if b.Progress != nil {
		fmt.Fprintf(b.Progress, "building %s\n", ref)
	}
	if err := b.RT.ImageBuild(ctx, spec); err != nil {
		return "", fmt.Errorf("build %s: %w", k, err)
	}
	b.prune(ctx, k, ref)
	return ref, nil
}

// prune removes older builds of k; images still in use or parents of
// other images are refused by the engine and kept.
func (b *Builder) prune(ctx context.Context, k Kind, keep string) {
	imgs, err := b.RT.ImageList(ctx, LabelImage+"="+string(k))
	if err != nil {
		return
	}
	for _, im := range imgs {
		for _, n := range im.Names {
			if n != keep {
				_ = b.RT.ImageRemove(ctx, n)
			}
		}
	}
}

// Info is a built podbox image.
type Info struct {
	Ref          string `json:"ref"`
	Image        string `json:"image"`
	Agent        string `json:"agent,omitempty"`
	Version      string `json:"version"`
	Experimental bool   `json:"experimental,omitempty"`
	ID           string `json:"id"`
}

// List returns every podbox image on the engine.
func List(ctx context.Context, rt runtime.Runtime) ([]Info, error) {
	imgs, err := rt.ImageList(ctx, LabelImage)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, im := range imgs {
		ref := ""
		if len(im.Names) > 0 {
			ref = im.Names[0]
		}
		id := im.ID
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, Info{
			Ref: ref, Image: im.Labels[LabelImage], Agent: im.Labels[LabelAgent],
			Version: im.Labels[LabelVersion], Experimental: im.Labels[LabelExperimental] == "true", ID: id,
		})
	}
	return out, nil
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
