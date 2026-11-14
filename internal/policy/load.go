package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// File is the YAML layer format.
type File struct {
	Mode    string     `yaml:"mode,omitempty"`
	Default string     `yaml:"default,omitempty"`
	Rules   []FileRule `yaml:"rules"`
}

// FileRule is one YAML rule; allow is required so a typo never allows.
type FileRule struct {
	Host         string `yaml:"host"`
	Allow        *bool  `yaml:"allow"`
	AllowPrivate bool   `yaml:"allow_private,omitempty"`
	Ports        []int  `yaml:"ports,omitempty"` // default: 80 and 443 for an allow, all for a deny
}

// Parse decodes and validates a layer. Warnings do not invalidate it.
func Parse(b []byte) (File, []string, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, nil, err
	}
	var warns []string
	switch f.Mode {
	case "", "strict":
	case "ask", "learn":
		warns = append(warns, fmt.Sprintf("mode %q is not supported in v0.1; strict applies", f.Mode))
	default:
		return File{}, nil, fmt.Errorf("unknown mode %q", f.Mode)
	}
	if f.Default != "" && f.Default != "deny" {
		return File{}, nil, fmt.Errorf("default must be deny, got %q", f.Default)
	}
	for i, r := range f.Rules {
		if err := ValidatePattern(r.Host); err != nil {
			return File{}, nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
		if r.Allow == nil {
			return File{}, nil, fmt.Errorf("rule %d (%s): allow must be true or false", i+1, r.Host)
		}
		if err := ValidatePorts(r.Ports); err != nil {
			return File{}, nil, fmt.Errorf("rule %d (%s): %w", i+1, r.Host, err)
		}
	}
	return f, warns, nil
}

// LoadLayer reads a layer file. A missing file is an empty layer; an invalid
// one contributes nothing and yields a warning (fail closed).
func LoadLayer(path, layer string) ([]Rule, []string) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("%s policy %s ignored: %v", layer, path, err)}
	}
	f, warns, err := Parse(b)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s policy %s ignored: %v", layer, path, err)}
	}
	for i := range warns {
		warns[i] = fmt.Sprintf("%s policy %s: %s", layer, path, warns[i])
	}
	return f.rules(layer), warns
}

func (f File) rules(layer string) []Rule {
	rs := make([]Rule, 0, len(f.Rules))
	for _, r := range f.Rules {
		rs = append(rs, Rule{Host: NormalizeHost(r.Host), Allow: *r.Allow, AllowPrivate: r.AllowPrivate, Ports: r.Ports, Layer: layer})
	}
	return rs
}

// Sources locates every layer of a sandbox.
type Sources struct {
	Sandbox string
	Agent   string
	Global  string // <config>/policy.yaml
	Project string // <workspace>/.podbox/policy.yaml
	Session string // <sandbox state>/session.yaml
}

// Compile merges defaults, global, project and session layers.
func Compile(s Sources) Effective {
	e := Effective{Version: EffectiveVersion, Sandbox: s.Sandbox, Rules: Defaults(s.Agent)}
	for _, l := range []struct{ path, name string }{
		{s.Global, LayerGlobal}, {s.Project, LayerProject}, {s.Session, LayerSession},
	} {
		if l.path == "" {
			continue
		}
		rs, warns := LoadLayer(l.path, l.name)
		e.Rules = append(e.Rules, rs...)
		e.Warnings = append(e.Warnings, warns...)
	}
	return e
}

// WriteEffective writes e atomically: readers see the old or the new file.
func WriteEffective(path string, e Effective) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(b, '\n'))
}

// ReadEffective loads an effective policy file and checks its version.
func ReadEffective(path string) (*Effective, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e Effective
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if e.Version != EffectiveVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", path, e.Version)
	}
	return &e, nil
}

// WriteAtomic writes via a temp file and rename in the same directory.
func WriteAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ValidatePorts rejects ports outside 1-65535.
func ValidatePorts(ps []int) error {
	for _, p := range ps {
		if p < 1 || p > 65535 {
			return fmt.Errorf("invalid port %d", p)
		}
	}
	return nil
}

// SetRule rewrites the podbox-owned layer file at path (session or
// snapshot, never a workspace file: see sandbox.SetProjectRule) so host has
// exactly one rule. It only replaces that file, by rename.
func SetRule(path, host string, allow bool, ports []int) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := EditRule(b, host, allow, ports)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return WriteAtomic(path, out)
}

// EditRule returns layer content b (nil: empty) with exactly one rule for
// host, limited to ports when given. Invalid content is never rewritten.
func EditRule(b []byte, host string, allow bool, ports []int) ([]byte, error) {
	if err := ValidatePattern(host); err != nil {
		return nil, err
	}
	if err := ValidatePorts(ports); err != nil {
		return nil, err
	}
	f, _, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("invalid, fix it first: %w", err)
	}
	h := NormalizeHost(host)
	kept := f.Rules[:0]
	var private bool
	for _, r := range f.Rules {
		if NormalizeHost(r.Host) == h {
			private = private || r.AllowPrivate
			continue
		}
		kept = append(kept, r)
	}
	f.Rules = append(kept, FileRule{Host: h, Allow: &allow, AllowPrivate: allow && private, Ports: ports})
	return yaml.Marshal(f)
}
