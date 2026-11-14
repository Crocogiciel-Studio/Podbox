package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
)

// ErrNotFound reports an unknown sandbox.
var ErrNotFound = errors.New("sandbox not found")

// Meta is the host-side record of a sandbox.
type Meta struct {
	Name      string    `json:"name"`
	Agent     string    `json:"agent"`
	Workspace string    `json:"workspace"`
	Created   time.Time `json:"created"`
}

// Store keeps sandbox state under <state>/sandboxes/<name>.
type Store struct {
	Root      string // <state>/sandboxes
	ConfigDir string // global config directory
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// ValidateName enforces names usable in podman object names.
func ValidateName(n string) error {
	if !validName.MatchString(n) {
		return fmt.Errorf("invalid sandbox name %q: lowercase letters, digits, '.', '_' or '-'", n)
	}
	return nil
}

// DefaultName derives <agent>-<dir> from a workspace path.
func DefaultName(agent, workspace string) string {
	base := strings.ToLower(filepath.Base(workspace))
	var sb strings.Builder
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('-')
		}
	}
	n := strings.Trim(agent+"-"+sb.String(), "-.")
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}

func (s Store) Dir(name string) string           { return filepath.Join(s.Root, name) }
func (s Store) metaPath(name string) string      { return filepath.Join(s.Dir(name), "meta.json") }
func (s Store) SessionPath(name string) string   { return filepath.Join(s.Dir(name), "session.yaml") }
func (s Store) EffectivePath(name string) string { return filepath.Join(s.Dir(name), EffectiveFile) }
func (s Store) EmptyDir(name string) string      { return filepath.Join(s.Dir(name), "masks", "empty") }

// EffectiveFile is the effective policy file name inside the sandbox dir.
const EffectiveFile = "policy.effective.json"

// Load reads a sandbox record.
func (s Store) Load(name string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(s.metaPath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// Save writes a sandbox record and its directories.
func (s Store) Save(m Meta) error {
	if err := os.MkdirAll(s.EmptyDir(m.Name), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return policy.WriteAtomic(s.metaPath(m.Name), append(b, '\n'))
}

// Remove deletes a sandbox's state directory.
func (s Store) Remove(name string) error {
	return os.RemoveAll(s.Dir(name))
}

// Sources lists the policy layers of a sandbox.
func (s Store) Sources(m Meta) policy.Sources {
	return policy.Sources{
		Sandbox: m.Name,
		Agent:   m.Agent,
		Global:  filepath.Join(s.ConfigDir, "policy.yaml"),
		Project: s.ProjectPolicyPath(m.Name), // snapshot, never the live workspace file
		Session: s.SessionPath(m.Name),
	}
}

// RefreshPolicy recompiles and atomically rewrites the effective policy.
func (s Store) RefreshPolicy(m Meta) (policy.Effective, error) {
	e := policy.Compile(s.Sources(m))
	return e, policy.WriteEffective(s.EffectivePath(m.Name), e)
}
