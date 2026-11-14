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

// maxHookFile caps the bytes hashed of one hook file; a larger one is
// recorded as Oversized and always reported.
const maxHookFile = 16 << 20

// HookSources lists the existing worktree paths that define git hooks
// (directories end with "/"), including core.hooksPath from .git/config when
// it points inside the workspace, outside .git.
func HookSources(ws string) []string {
	r, err := os.OpenRoot(ws)
	if err != nil {
		return nil
	}
	defer r.Close()
	var out []string
	for _, rel := range hookCandidates(r, ws) {
		if _, err := Lstat(r, strings.TrimSuffix(rel, "/")); err != nil {
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

func hookCandidates(r *os.Root, ws string) []string {
	c := append([]string{}, hookFiles...)
	if hp := hooksPath(r, ws); hp != "" {
		c = append(c, hp+"/")
	}
	return c
}

// hooksPath returns core.hooksPath from .git/config as a slash-separated
// workspace-relative path, or "" when unset, outside the workspace or in .git.
func hooksPath(r *os.Root, ws string) string {
	b, err := ReadFile(r, ".git/config", maxGitFile)
	if err != nil {
		return "" // an oversized config is reported by the git fingerprint
	}
	v := ""
	core := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, maxGitFile)
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
	r, err := os.OpenRoot(ws)
	if err != nil {
		return map[string]string{}
	}
	defer r.Close()
	return fingerprintHooks(r)
}

func fingerprintHooks(r *os.Root) map[string]string {
	out := map[string]string{}
	for _, src := range hookCandidates(r, r.Name()) {
		src = strings.TrimSuffix(src, "/")
		fi, err := Lstat(r, src)
		switch {
		case err != nil:
			continue
		case fi.Mode()&fs.ModeSymlink != 0:
			if t, err := Readlink(r, src); err == nil {
				out[src] = "link:" + t
			}
			continue
		case fi.Mode().IsRegular():
			if sum, err := HashFile(r, src, maxHookFile); err == nil {
				out[src] = sum
			}
			continue
		case !fi.IsDir():
			continue
		}
		d, err := OpenDir(r, src)
		if err != nil {
			continue
		}
		_ = fs.WalkDir(d.FS(), ".", func(p string, e fs.DirEntry, err error) error {
			if err != nil || len(out) >= maxHookFiles {
				return nil
			}
			rel := path.Join(src, p)
			switch {
			case e.Type()&fs.ModeSymlink != 0:
				if t, err := d.Readlink(p); err == nil {
					out[rel] = "link:" + t
				}
			case e.Type().IsRegular():
				if sum, err := HashFile(d, p, maxHookFile); err == nil {
					out[rel] = sum
				}
			}
			return nil
		})
		d.Close()
	}
	return out
}

// diffHooks lists hook files changed, added or removed, as findings.
func diffHooks(old, cur map[string]string) []GitFinding {
	const why = ": git runs it on your commits; review the diff before committing on the host"
	var out []GitFinding
	for rel, sum := range cur {
		if sum == Oversized {
			out = append(out, GitFinding{Path: rel, Reason: oversized + why, Hook: true})
		} else if prev, ok := old[rel]; !ok || prev != sum {
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
