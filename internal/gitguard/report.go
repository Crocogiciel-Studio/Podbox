package gitguard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Integrity is the result of comparing .git with its fingerprint.
type Integrity struct {
	OK       bool      `json:"ok"`
	Findings []Finding `json:"findings"`
}

// Check compares workspace ws with the fingerprint old.
func Check(ws string, old Print) (*Integrity, error) {
	cur, err := Fingerprint(ws)
	if err != nil {
		return nil, err
	}
	f := Diff(old, cur)
	return &Integrity{OK: len(f) == 0, Findings: f}, nil
}

// Warning is the advice printed when Check has findings, for one sandbox or
// for several on the same workspace with the same findings.
func Warning(ws string, g Integrity, names ...string) string {
	name := strings.Join(names, ", ")
	var gitF, hookF []Finding
	for _, f := range g.Findings {
		if f.Hook {
			hookF = append(hookF, f)
		} else {
			gitF = append(gitF, f)
		}
	}
	var sb strings.Builder
	list := func(fs []Finding) {
		for _, f := range fs {
			if f.Path == "" {
				fmt.Fprintf(&sb, "  %s\n", f.Reason)
			} else {
				fmt.Fprintf(&sb, "  %s: %s\n", f.Path, f.Reason)
			}
		}
	}
	if len(gitF) > 0 {
		fmt.Fprintf(&sb, "%s: GIT DIRECTORY TAMPERING SUSPECTED in %s\n", name, ws)
		list(gitF)
		fmt.Fprintf(&sb, "Do NOT run git in %s until you have reviewed these paths: git on the host could run commands the agent chose.\n", ws)
		fmt.Fprintf(&sb, "Remove or restore what you did not create (e.g. %s).\n", filepath.Join(ws, filepath.FromSlash(gitF[0].Path)))
	}
	if len(hookF) > 0 {
		fmt.Fprintf(&sb, "%s: GIT HOOK FILES CHANGED in %s\n", name, ws)
		list(hookF)
		fmt.Fprintf(&sb, "Review the diff of these files (git diff, git status) before committing on the host: your next commit runs them.\n")
	}
	accept := make([]string, len(names))
	for i, n := range names {
		accept[i] = "podbox git-check " + n + " --accept"
	}
	fmt.Fprintf(&sb, "Once reviewed, run: %s", strings.Join(accept, " && "))
	return sb.String()
}

// Notice tells, at launch, which worktree files of ws define git hooks; ""
// when there are none.
func Notice(name, ws string) string {
	src := HookSources(ws)
	if len(src) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: this project runs code from %s on your commits; the agent can modify it: review the diff before committing on the host; for untrusted code create the sandbox with --clone",
		name, strings.Join(src, ", "))
}
