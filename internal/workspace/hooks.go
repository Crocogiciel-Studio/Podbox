package workspace

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Hook managers read their hooks from worktree files, which the agent can
// edit: the next commit on the host runs what it wrote. podbox cannot make
// them read-only without breaking normal work, so it fingerprints them.

// hookFiles are the worktree paths hook managers read; a trailing slash
// marks a directory.
var hookFiles = []string{".githooks/", ".husky/", ".lefthook.yml", ".pre-commit-config.yaml", "lefthook.yaml", "lefthook.yml"}

// maxHookFiles bounds the files hashed under hook directories.
const maxHookFiles = 1000

// HookSources lists the existing worktree paths that define git hooks
// (directories end with "/"), including core.hooksPath from .git/config when
// it points inside the workspace, outside .git.
func HookSources(ws string) []string {
	var out []string
	for _, rel := range hookCandidates(ws) {
		if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(strings.TrimSuffix(rel, "/")))); err != nil {
			continue
		}
		covered := false
		for _, o := range out {
			if strings.HasSuffix(o, "/") && strings.HasPrefix(rel, o) {
				covered = true
			}
		}
		if !covered {
			out = append(out, rel)
		}
	}
	return out
}

func hookCandidates(ws string) []string {
	c := append([]string{}, hookFiles...)
	if hp := hooksPath(ws); hp != "" {
		c = append(c, hp+"/")
	}
	return c
}

// hooksPath returns core.hooksPath from .git/config as a slash-separated
// workspace-relative path, or "" when unset, outside the workspace or in .git.
func hooksPath(ws string) string {
	cfg := filepath.Join(ws, ".git", "config")
	if fi, err := os.Lstat(cfg); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	b, err := os.ReadFile(cfg)
	if err != nil {
		return ""
	}
	v := ""
	core := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			core = strings.EqualFold(strings.TrimSpace(strings.Trim(l, "[]")), "core")
			continue
		}
		k, val, ok := strings.Cut(l, "=")
		if core && ok && strings.EqualFold(strings.TrimSpace(k), "hookspath") {
			v = strings.Trim(strings.TrimSpace(val), `"`) // the last value wins, as in git
		}
	}
	if v == "" || strings.HasPrefix(v, "~") {
		return ""
	}
	p := filepath.FromSlash(v)
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	rel, err := filepath.Rel(ws, p)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return ""
	}
	return rel
}

// FingerprintHooks maps every file under the hook sources to its sha256
// ("link:<target>" for a symbolic link, never followed).
func FingerprintHooks(ws string) map[string]string {
	out := map[string]string{}
	for _, rel := range hookCandidates(ws) {
		root := filepath.Join(ws, filepath.FromSlash(strings.TrimSuffix(rel, "/")))
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || len(out) >= maxHookFiles {
				return nil
			}
			r := path.Clean(filepath.ToSlash(mustRel(ws, p)))
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				if t, err := os.Readlink(p); err == nil {
					out[r] = "link:" + t
				}
			case d.Type().IsRegular():
				if sum, err := hashFile(p); err == nil {
					out[r] = sum
				}
			}
			return nil
		})
	}
	return out
}

// diffHooks lists hook files changed, added or removed, as findings.
func diffHooks(old, cur map[string]string) []GitFinding {
	const why = ": git runs it on your commits; review the diff before committing on the host"
	var out []GitFinding
	for rel, sum := range cur {
		if prev, ok := old[rel]; !ok || prev != sum {
			out = append(out, GitFinding{Path: rel, Reason: changeVerb(ok) + why, Hook: true})
		}
	}
	for rel := range old {
		if _, ok := cur[rel]; !ok {
			out = append(out, GitFinding{Path: rel, Reason: "removed" + why, Hook: true})
		}
	}
	return out
}
