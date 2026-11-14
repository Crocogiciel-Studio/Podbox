package workspace

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The agent can write the git directory (it must, to commit), and git on the
// host trusts what it finds there. A GitPrint records the parts of .git that
// can make host git run commands or read another repository; normal churn
// (objects, refs, logs, index, HEAD, ...) is left out so commits, branches,
// rebases, stashes and gc do not change it.

// GitPrint is a fingerprint of a workspace's .git.
type GitPrint struct {
	Kind    string            `json:"kind"`              // dir, file, symlink, none or other
	Pointer string            `json:"pointer,omitempty"` // sha256 of a .git file
	Entries []string          `json:"entries,omitempty"` // top-level names
	Files   map[string]string `json:"files,omitempty"`   // watched path -> sha256
	Links   map[string]string `json:"links,omitempty"`   // symlink path -> target
	// PendingExec reports a rebase todo with exec lines.
	PendingExec bool `json:"pending_exec,omitempty"`
}

// GitFinding is one suspicious difference, with a path relative to the
// workspace.
type GitFinding struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// churn lists top-level .git entries that git creates and removes itself.
var churn = map[string]bool{
	"HEAD": true, "config": true, "description": true, "index": true, "packed-refs": true, "shallow": true,
	"objects": true, "refs": true, "logs": true, "info": true, "hooks": true, "branches": true,
	"rebase-merge": true, "rebase-apply": true, "sequencer": true, "worktrees": true, "modules": true,
	"rr-cache": true, "lfs": true, "gc.pid": true, "gc.log": true, "AUTO_MERGE": true,
	"COMMIT_EDITMSG": true, "TAG_EDITMSG": true, "SQUASH_MSG": true, "NOTES_EDITMSG": true, "EDIT_DESCRIPTION": true,
}

func isChurn(name string) bool {
	return churn[name] || strings.HasSuffix(name, "_HEAD") || strings.HasSuffix(name, ".lock") ||
		strings.HasPrefix(name, "MERGE_") || strings.HasPrefix(name, "BISECT_") ||
		strings.HasPrefix(name, "sharedindex.") || strings.HasPrefix(name, "index.stash.") ||
		strings.HasPrefix(name, "fsmonitor--daemon")
}

// why explains a watched path; "" means the path is not watched.
func why(rel string) string {
	switch rel {
	case "config":
		return "git configuration, which can set core.fsmonitor, core.hooksPath or filters run by git on the host"
	case "commondir":
		return "points git at another git directory, whose config and hooks git on the host would use"
	case "gitdir":
		return "redirects git to another git directory"
	case "config.worktree":
		return "per-worktree configuration read by git on the host (core.fsmonitor, core.hooksPath, ...)"
	case "objects/info/alternates", "objects/info/http-alternates":
		return "makes git read objects from another repository"
	}
	if strings.HasPrefix(rel, "info/") && rel != "info/refs" { // info/refs: written by repack
		return "changes what git reads (excludes, attributes, sparse checkout, grafts)"
	}
	if strings.HasPrefix(rel, "modules/") {
		base := path.Base(rel)
		if base == "config" || base == "commondir" || base == "config.worktree" ||
			strings.HasSuffix(rel, "/objects/info/alternates") ||
			strings.Contains(rel, "/hooks/") && !strings.HasSuffix(rel, ".sample") {
			return "submodule git directory file (config or hook) that git on the host uses in that submodule"
		}
	}
	return ""
}

// FingerprintGit records the .git of workspace ws.
func FingerprintGit(ws string) (GitPrint, error) {
	root := filepath.Join(ws, ".git")
	fi, err := os.Lstat(root)
	switch {
	case os.IsNotExist(err):
		return GitPrint{Kind: "none"}, nil
	case err != nil:
		return GitPrint{}, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return GitPrint{Kind: "symlink"}, nil
	case fi.Mode().IsRegular():
		sum, err := hashFile(root)
		return GitPrint{Kind: "file", Pointer: sum}, err
	case !fi.IsDir():
		return GitPrint{Kind: "other"}, nil
	}
	p := GitPrint{Kind: "dir", Files: map[string]string{}, Links: map[string]string{}}
	entries, err := os.ReadDir(root)
	if err != nil {
		return p, err
	}
	for _, e := range entries {
		p.Entries = append(p.Entries, e.Name())
	}
	err = filepath.WalkDir(root, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || fp == root {
			return nil // unreadable entries are unreadable to host git too
		}
		rel := filepath.ToSlash(mustRel(root, fp))
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			if t, err := os.Readlink(fp); err == nil {
				p.Links[rel] = t
			}
		case d.Type().IsRegular() && why(rel) != "":
			if sum, err := hashFile(fp); err == nil {
				p.Files[rel] = sum
			}
		}
		return nil
	})
	p.PendingExec = pendingExec(filepath.Join(root, "rebase-merge", "git-rebase-todo"))
	return p, err
}

func hashFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

// pendingExec reports exec lines that `git rebase --continue` would run.
func pendingExec(todo string) bool {
	b, err := os.ReadFile(todo)
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 0 && (f[0] == "x" || f[0] == "exec") {
			return true
		}
	}
	return false
}

// DiffGit lists what changed from old to cur that git on the host could
// act on, sorted by path.
func DiffGit(old, cur GitPrint) []GitFinding {
	out := []GitFinding{}
	add := func(rel, reason string) {
		p := ".git"
		if rel != "" {
			p += "/" + rel
		}
		out = append(out, GitFinding{Path: p, Reason: reason})
	}
	if old.Kind != cur.Kind {
		reason := fmt.Sprintf("was %s, is now %s", kindName(old.Kind), kindName(cur.Kind))
		if cur.Kind != "none" {
			reason += ": git on the host would use a git directory written in the sandbox"
		}
		add("", reason)
		return out
	}
	if cur.Kind == "file" && old.Pointer != cur.Pointer {
		add("", "changed: this pointer sends git on the host to another git directory")
	}
	if cur.Kind != "dir" {
		return out
	}
	seen := map[string]bool{}
	for rel, sum := range cur.Files {
		if prev, ok := old.Files[rel]; !ok || prev != sum {
			add(rel, changeVerb(ok)+": "+why(rel))
			seen[rel] = true
		}
	}
	if _, ok := cur.Files["config"]; !ok && old.Files["config"] != "" {
		add("config", "removed: "+why("config"))
		seen["config"] = true
	}
	for rel, t := range cur.Links {
		if prev, ok := old.Links[rel]; ok && prev == t {
			continue
		}
		reason := changeVerb(hasKey(old.Links, rel)) + ": symbolic link to " + t
		if outside(rel, t) {
			reason += ", outside .git"
		}
		add(rel, reason)
		seen[rel] = true
	}
	known := map[string]bool{}
	for _, e := range old.Entries {
		known[e] = true
	}
	for _, e := range cur.Entries {
		if !known[e] && !isChurn(e) && !seen[e] {
			add(e, "appeared: unexpected entry in .git")
			seen[e] = true
		}
	}
	if cur.PendingExec && !old.PendingExec {
		add("rebase-merge/git-rebase-todo", "pending rebase has exec lines that git rebase --continue would run")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func changeVerb(existed bool) string {
	if existed {
		return "changed"
	}
	return "appeared"
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

func kindName(k string) string {
	switch k {
	case "dir":
		return "a directory"
	case "file":
		return "a file"
	case "symlink":
		return "a symbolic link"
	case "none":
		return "absent"
	}
	return "a special file"
}

// outside reports a link target escaping .git.
func outside(rel, target string) bool {
	if path.IsAbs(target) || filepath.IsAbs(target) {
		return true
	}
	p := path.Join(path.Dir(rel), filepath.ToSlash(target))
	return p == ".." || strings.HasPrefix(p, "../")
}
