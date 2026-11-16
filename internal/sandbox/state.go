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

	"github.com/Crocogiciel-Studio/podbox/internal/fsutil"
	"github.com/Crocogiciel-Studio/podbox/internal/gitguard"
	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
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
	// Mode is ModeShared (empty in records written before clone mode) or
	// ModeClone; fixed at creation.
	Mode string `json:"mode,omitempty"`
	// Base is the host commit a clone was made from.
	Base string `json:"base,omitempty"`
}

// Sandbox modes.
const (
	ModeShared = "shared"
	ModeClone  = "clone"
)

// IsClone reports a sandbox whose workspace is its own git clone.
func (m Meta) IsClone() bool { return m.Mode == ModeClone }

// ModeName is the mode, shared when unset.
func (m Meta) ModeName() string {
	if m.Mode == "" {
		return ModeShared
	}
	return m.Mode
}

// Guard is the git protection switch of this sandbox (see gitguard.Guard).
func (m Meta) Guard() gitguard.Guard { return gitguard.Guard{Clone: m.IsClone()} }

// Store keeps sandbox state under <state>/sandboxes/<name>.
type Store struct {
	Root       string // <state>/sandboxes
	ClonesRoot string // <state>/clones, a sibling of Root: the proxy mounts only <Root>/<name>
	ConfigDir  string // global config directory
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

// AgentDir is the only state directory the agent sees, read-only.
func (s Store) AgentDir(name string) string { return filepath.Join(s.Dir(name), "agent") }

// AgentPolicyPath is the agent's view of the effective policy.
func (s Store) AgentPolicyPath(name string) string {
	return filepath.Join(s.AgentDir(name), AgentPolicyFile)
}

// AgentPolicyFile is the agent view file name inside AgentDir.
const AgentPolicyFile = "policy.json"

// EffectiveFile is the effective policy file name inside the sandbox dir.
const EffectiveFile = "policy.effective.json"

// Load reads a sandbox record.
func (s Store) Load(name string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(s.metaPath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return m, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Mode != "" && m.Mode != ModeShared && m.Mode != ModeClone {
		return Meta{}, fmt.Errorf("sandbox %s has unknown mode %q", name, m.Mode)
	}
	return m, nil
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
	return fsutil.WriteAtomic(s.metaPath(m.Name), append(b, '\n'))
}

// Remove deletes a sandbox's state directory.
func (s Store) Remove(name string) error {
	return os.RemoveAll(s.Dir(name))
}

// CloneDir is where a sandbox's clone lives: outside the sandbox's own
// state directory, so removing the sandbox keeps it. Empty without ClonesRoot.
func (s Store) CloneDir(name string) string {
	if s.ClonesRoot == "" {
		return ""
	}
	return filepath.Join(s.ClonesRoot, name)
}

// Source is the host directory mounted in the agent and read by podbox: the
// clone for a clone sandbox, the project otherwise. Meta.Workspace stays the
// project path (labels, container path, agent view).
func (s Store) Source(m Meta) string {
	if m.IsClone() {
		return s.CloneDir(m.Name)
	}
	return m.Workspace
}

// HasClone reports an existing clone directory.
func (s Store) HasClone(name string) bool {
	d := s.CloneDir(name)
	if d == "" {
		return false
	}
	fi, err := os.Lstat(d)
	return err == nil && fi.IsDir()
}

// HasStaging reports a clone left half-made by an interrupted create.
func (s Store) HasStaging(name string) bool {
	d := s.CloneDir(name)
	if d == "" {
		return false
	}
	_, err := os.Lstat(hostgit.PartialDir(d))
	return err == nil
}

// RemoveClone deletes a sandbox's clone and any staging left of it.
func (s Store) RemoveClone(name string) error {
	d := s.CloneDir(name)
	if d == "" {
		return nil
	}
	return errors.Join(os.RemoveAll(d), os.RemoveAll(hostgit.PartialDir(d)))
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
	return e, s.writeEffective(m, e)
}

// writeEffective writes the effective policy the proxy enforces and the
// view of it the agent reads, each atomically.
func (s Store) writeEffective(m Meta, e policy.Effective) error {
	if err := policy.WriteEffective(s.EffectivePath(m.Name), e); err != nil {
		return err
	}
	b, err := json.MarshalIndent(e.AgentView(m.Workspace), "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(s.AgentPolicyPath(m.Name), append(b, '\n'))
}
