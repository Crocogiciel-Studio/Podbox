// Package gitguard holds every protection podbox applies to a workspace's git
// directory and hook files: the read-only binds (mounts.go), the fingerprint
// and its diff (fingerprint.go, hooks.go) and the advice printed on findings
// (report.go).
package gitguard

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// The agent can write the git directory (it must, to commit), and git on the
// host trusts what it finds there. A Print records the parts of .git that
// can make host git run commands or read another repository; normal churn
// (objects, refs, logs, index, HEAD, ...) is left out so commits, branches,
// rebases, stashes and gc do not change it. It also records the worktree
// files that define hooks (see fingerprintHooks).

// Print is a fingerprint of a workspace's .git.
type Print struct {
	Kind    string `json:"kind"`              // dir, file, symlink, none or other
	Pointer string `json:"pointer,omitempty"` // sha256 of a .git file
	// Entries are the top-level names, and the entries of each linked
	// worktree directory as paths (worktrees/<name>/<entry>).
	Entries []string          `json:"entries,omitempty"`
	Files   map[string]string `json:"files,omitempty"` // watched path -> sha256
	Links   map[string]string `json:"links,omitempty"` // symlink path -> target
	// PendingExec lists rebase todos with exec lines.
	PendingExec []string `json:"pending_exec_todos,omitempty"`
	// Hooks maps worktree hook files to their sha256.
	Hooks map[string]string `json:"hooks,omitempty"`
	// HookFiles counts the files under the hook sources; past maxHookFiles
	// only the first ones, in name order, are hashed.
	HookFiles int `json:"hook_files,omitempty"`
}

// Finding is one suspicious difference, with a path relative to the
// workspace.
type Finding struct {
	Path   string `json:"path"` // "" for a finding on all hook files
	Reason string `json:"reason"`
	Hook   bool   `json:"hook,omitempty"` // a worktree hook file, outside .git
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

// worktreeChurn adds to churn the entries of a linked worktree's git
// directory (.git/worktrees/<name>) that git manages; commondir, gitdir and
// config.worktree are watched files, reported by content. A config there is
// not churn.
var worktreeChurn = map[string]bool{"locked": true, "commondir": true, "gitdir": true, "config.worktree": true}

// worktreeFile matches the watched files of a linked worktree's git
// directory, in .git or in a submodule git directory under .git/modules.
var worktreeFile = regexp.MustCompile(`(^|/)worktrees/[^/]+/(commondir|gitdir|config\.worktree)$`)

// worktreeDir reports a linked worktree's git directory:
// <gitdir>/worktrees/<name>, rel being relative to .git (opened as g).
func worktreeDir(g *os.Root, rel string) bool {
	parent := path.Dir(rel)
	if path.Base(parent) != "worktrees" {
		return false
	}
	gd := path.Dir(parent)
	return gd == "." || isGitDir(g, gd)
}

// isGitDir reports a git directory at rel: one holding a regular HEAD.
func isGitDir(r *os.Root, rel string) bool {
	fi, err := workspace.Lstat(r, path.Join(rel, "HEAD"))
	return err == nil && fi.Mode().IsRegular()
}

// isChurnEntry reports an entry git creates and removes itself: a
// top-level name, or a worktree entry path (see Print.Entries).
func isChurnEntry(e string) bool {
	if i := strings.LastIndexByte(e, '/'); i >= 0 {
		n := e[i+1:]
		return worktreeChurn[n] || n != "config" && isChurn(n)
	}
	return isChurn(e)
}

// stdCommondir is the sha256 of the commondir git writes in a new linked
// worktree ("../..").
var stdCommondir = func() string { s := sha256.Sum256([]byte("../..\n")); return hex.EncodeToString(s[:]) }()

// newWorktreeFile reports a watched file of a worktree git directory that
// did not exist in old, holding what git itself writes there: the agent (or
// the user) ran git worktree add. Its gitdir is a back-pointer git only uses
// to list and prune worktrees.
func newWorktreeFile(old Print, rel, sum string) bool {
	dir := path.Dir(rel)
	for _, e := range old.Entries {
		if strings.HasPrefix(e, dir+"/") {
			return false // the worktree existed: any change is reported
		}
	}
	switch path.Base(rel) {
	case "gitdir":
		return true
	case "commondir":
		return sum == stdCommondir
	}
	return false
}

// why explains a watched path; "" means the path is not watched.
func why(rel string) string {
	if m := worktreeFile.FindStringSubmatch(rel); m != nil {
		switch m[2] {
		case "commondir":
			return "points this linked worktree at a git directory, whose config and hooks git on the host uses in that worktree"
		case "gitdir":
			return "linked worktree back-pointer, changed in the sandbox"
		}
		return "per-worktree configuration read by git on the host in that linked worktree (core.fsmonitor, core.hooksPath, ...)"
	}
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
	if strings.HasPrefix(rel, "hooks/") && !strings.HasSuffix(rel, ".sample") {
		return "git hook run by git on the host (hooks are read-only in the sandbox only when .git/hooks existed at start)"
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

// maxGitFile caps the bytes hashed of one watched git file; a larger one
// is recorded as workspace.Oversized and always reported.
const maxGitFile = 1 << 20

// maxTodo caps the rebase todo read; a larger one counts as pending exec.
const maxTodo = 8 << 20

// Fingerprint records the .git of workspace ws and its hook files. It
// reads ws through an os.Root and never follows a symbolic link.
func Fingerprint(ws string) (Print, error) {
	r, err := os.OpenRoot(ws)
	if err != nil {
		return Print{}, err
	}
	defer r.Close()
	p, err := fingerprintGitDir(r)
	p.Hooks, p.HookFiles = fingerprintHooks(r)
	return p, err
}

func fingerprintGitDir(r *os.Root) (Print, error) {
	fi, err := r.Lstat(".git")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Print{Kind: "none"}, nil
	case err != nil:
		return Print{}, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return Print{Kind: "symlink"}, nil
	case fi.Mode().IsRegular():
		sum, err := workspace.HashFile(r, ".git", maxGitFile)
		return Print{Kind: "file", Pointer: sum}, err
	case !fi.IsDir():
		return Print{Kind: "other"}, nil
	}
	p := Print{Kind: "dir", Files: map[string]string{}, Links: map[string]string{}}
	g, err := workspace.OpenDir(r, ".git")
	if err != nil {
		return p, err
	}
	defer g.Close()
	gfs := g.FS()
	entries, err := fs.ReadDir(gfs, ".")
	if err != nil {
		return p, err
	}
	for _, e := range entries {
		p.Entries = append(p.Entries, e.Name())
	}
	err = fs.WalkDir(gfs, ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || rel == "." {
			return nil // unreadable entries are unreadable to host git too
		}
		if d.IsDir() && worktreeDir(g, rel) {
			if es, err := fs.ReadDir(gfs, rel); err == nil {
				for _, e := range es {
					p.Entries = append(p.Entries, rel+"/"+e.Name())
				}
			}
		}
		switch {
		case d.Name() == "git-rebase-todo" && path.Base(path.Dir(rel)) == "rebase-merge":
			if pendingExec(g, rel) {
				p.PendingExec = append(p.PendingExec, rel)
			}
		case d.Type()&fs.ModeSymlink != 0:
			if t, err := g.Readlink(rel); err == nil {
				p.Links[rel] = t
			}
		case d.Type().IsRegular() && why(rel) != "":
			if sum, err := workspace.HashFile(g, rel, maxGitFile); err == nil {
				p.Files[rel] = sum
			}
		}
		return nil
	})
	return p, err
}

// pendingExec reports exec lines that `git rebase --continue` would run.
func pendingExec(g *os.Root, todo string) bool {
	b, err := workspace.ReadFile(g, todo, maxTodo)
	if errors.Is(err, workspace.ErrTooLarge) {
		return true // unchecked: reported
	}
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, maxTodo)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 0 && (f[0] == "x" || f[0] == "exec") {
			return true
		}
	}
	return false
}

// Diff lists what changed from old to cur that git on the host could
// act on, sorted by path.
func Diff(old, cur Print) []Finding {
	out := append(diffGitDir(old, cur), diffHooks(old, cur)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func diffGitDir(old, cur Print) []Finding {
	out := []Finding{}
	add := func(rel, reason string) {
		p := ".git"
		if rel != "" {
			p += "/" + rel
		}
		out = append(out, Finding{Path: p, Reason: reason})
	}
	if old.Kind != cur.Kind {
		reason := fmt.Sprintf("was %s, is now %s", kindName(old.Kind), kindName(cur.Kind))
		if cur.Kind != "none" {
			reason += ": git on the host would use a git directory written in the sandbox"
		}
		add("", reason)
		return out
	}
	if cur.Kind == "file" && cur.Pointer == workspace.Oversized {
		add("", oversized)
	} else if cur.Kind == "file" && old.Pointer != cur.Pointer {
		add("", "changed: this pointer sends git on the host to another git directory")
	}
	if cur.Kind != "dir" {
		return out
	}
	seen := map[string]bool{}
	for rel, sum := range cur.Files {
		if sum == workspace.Oversized {
			add(rel, oversized+": "+why(rel))
			seen[rel] = true
			continue
		}
		if prev, ok := old.Files[rel]; !ok && !newWorktreeFile(old, rel, sum) || ok && prev != sum {
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
		if !known[e] && !isChurnEntry(e) && !seen[e] {
			add(e, "appeared: unexpected entry in .git")
			seen[e] = true
		}
	}
	for _, todo := range cur.PendingExec {
		if !slices.Contains(old.PendingExec, todo) {
			add(todo, "pending rebase has exec lines that git rebase --continue would run")
		}
	}
	return out
}

// oversized explains a watched file podbox did not hash.
const oversized = "oversized (over 1 MiB): podbox did not read it through; review it"

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
