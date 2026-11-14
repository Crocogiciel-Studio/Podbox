package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/policy"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// The agent can write the workspace's .git, which git on the host trusts.
// podbox records a fingerprint of it at creation and compares the live
// directory against it after every session (see workspace.FingerprintGit).

// GitIntegrity is the result of comparing .git with its fingerprint.
type GitIntegrity struct {
	OK       bool                   `json:"ok"`
	Findings []workspace.GitFinding `json:"findings"`
}

func (s Store) gitPrintPath(name string) string {
	return filepath.Join(s.Dir(name), "git.fingerprint.json")
}

// RecordGit (re)records the fingerprint of the workspace's .git.
func (s Store) RecordGit(m Meta) error {
	p, err := workspace.FingerprintGit(m.Workspace)
	if err != nil {
		return fmt.Errorf("fingerprint %s/.git: %w", m.Workspace, err)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return policy.WriteAtomic(s.gitPrintPath(m.Name), append(b, '\n'))
}

// HasGitPrint reports a recorded fingerprint.
func (s Store) HasGitPrint(name string) bool {
	_, err := os.Stat(s.gitPrintPath(name))
	return err == nil
}

// CheckGit compares the workspace's .git with the recorded fingerprint; nil
// when none was recorded.
func (s Store) CheckGit(m Meta) (*GitIntegrity, error) {
	b, err := os.ReadFile(s.gitPrintPath(m.Name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var old workspace.GitPrint
	if err := json.Unmarshal(b, &old); err != nil {
		return nil, fmt.Errorf("git fingerprint of %s: %w", m.Name, err)
	}
	cur, err := workspace.FingerprintGit(m.Workspace)
	if err != nil {
		return nil, err
	}
	f := workspace.DiffGit(old, cur)
	return &GitIntegrity{OK: len(f) == 0, Findings: f}, nil
}

// GitWarning is the advice printed when CheckGit has findings.
func GitWarning(m Meta, g GitIntegrity) string {
	var gitF, hookF []workspace.GitFinding
	for _, f := range g.Findings {
		if f.Hook {
			hookF = append(hookF, f)
		} else {
			gitF = append(gitF, f)
		}
	}
	var sb strings.Builder
	list := func(fs []workspace.GitFinding) {
		for _, f := range fs {
			fmt.Fprintf(&sb, "  %s: %s\n", f.Path, f.Reason)
		}
	}
	if len(gitF) > 0 {
		fmt.Fprintf(&sb, "%s: GIT DIRECTORY TAMPERING SUSPECTED in %s\n", m.Name, m.Workspace)
		list(gitF)
		fmt.Fprintf(&sb, "Do NOT run git in %s until you have reviewed these paths: git on the host could run commands the agent chose.\n", m.Workspace)
		fmt.Fprintf(&sb, "Remove what you did not create (e.g. rm %s/.git/commondir).\n", m.Workspace)
	}
	if len(hookF) > 0 {
		fmt.Fprintf(&sb, "%s: GIT HOOK FILES CHANGED in %s\n", m.Name, m.Workspace)
		list(hookF)
		fmt.Fprintf(&sb, "Review the diff of these files (git diff, git status) before committing on the host: your next commit runs them.\n")
	}
	fmt.Fprintf(&sb, "Once reviewed, run: podbox git-check %s --accept", m.Name)
	return sb.String()
}

// HookNotice tells, at launch, which worktree files define git hooks; ""
// when there are none.
func HookNotice(m Meta) string {
	src := workspace.HookSources(m.Workspace)
	if len(src) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: this project runs code from %s on your commits; the agent can modify it: review the diff before committing on the host; for untrusted code use --clone (coming soon)",
		m.Name, strings.Join(src, ", "))
}
