package sandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/hostgit"
	"github.com/Crocogiciel-Studio/podbox/internal/termsafe"
	"github.com/Crocogiciel-Studio/podbox/internal/workspace"
)

// ExportSchema is the version of the JSON of podbox export.
const ExportSchema = 1

// maxExport caps a bundle or a patch read from the sandbox (a variable for tests).
var maxExport int64 = 256 << 20

// maxListed caps the commits and file names an export lists.
const maxListed = 20

// Export kinds.
const (
	AsBranch = "branch"
	AsPatch  = "patch"
)

var (
	// ErrBadExport reports invalid export options (a usage error).
	ErrBadExport = errors.New("invalid export options")
	// ErrDetached reports a clone whose HEAD is not on a branch.
	ErrDetached = errors.New("the clone's HEAD is detached")
	// ErrBaseMissing reports a base commit the host repository no longer has.
	ErrBaseMissing = errors.New("the base commit is not in the host repository")
	// ErrHistory reports commits that do not descend from the base.
	ErrHistory = errors.New("the agent's history does not descend from the base commit")
	// ErrMaskedTouched reports commits that touch masked paths.
	ErrMaskedTouched = errors.New("the commits touch masked paths")
	// ErrFileExists reports a patch file that exists; it is never overwritten.
	ErrFileExists = errors.New("file already exists")
	// ErrChanged reports a sandbox whose HEAD moved while it was exported.
	ErrChanged = errors.New("the clone changed during the export")
)

// ExportOptions are the choices of Export.
type ExportOptions struct {
	As            string // AsBranch or AsPatch
	BranchName    string // branch only; default podbox/<sandbox>/<agent branch>
	Out           string // patch only; default <sandbox>.patch in the current directory
	IncludeMasked bool
}

// NotExported is the work an export leaves in the clone: only commits go.
type NotExported struct {
	Uncommitted      int      `json:"uncommitted"`
	Untracked        int      `json:"untracked"`
	UncommittedFiles []string `json:"uncommitted_files"`
	UntrackedFiles   []string `json:"untracked_files"`
}

// CommitLine is one exported commit, cleaned for display.
type CommitLine struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
}

// ExportWarning is something in the commits a reviewer must look at before
// checking the work out. Examples are cleaned for display.
type ExportWarning struct {
	Kind     string   `json:"kind"`
	Detail   string   `json:"detail"`
	Count    int      `json:"count"`
	Examples []string `json:"examples"`
}

// ExportResult is what an export did. Strings were cleaned for display.
type ExportResult struct {
	Schema   int    `json:"schema"`
	Name     string `json:"name"`
	As       string `json:"as"`
	Exported bool   `json:"exported"`
	// Branch is the host branch created, File the patch written.
	Branch string `json:"branch,omitempty"`
	File   string `json:"file,omitempty"`
	// AgentBranch is the clone's branch ("" for a detached HEAD);
	// BranchSanitized says the default host name had to be altered.
	AgentBranch     string       `json:"agent_branch,omitempty"`
	BranchSanitized bool         `json:"branch_sanitized,omitempty"`
	Base            string       `json:"base"`
	Head            string       `json:"head"`
	Commits         int          `json:"commits"`
	CommitList      []CommitLine `json:"commit_list"`
	FilesChanged    int          `json:"files_changed"`
	// HostVerified: the commit range was analysed from objects the host
	// verified (branch). A patch is text the sandbox produced; the host only
	// checks that the base commit exists.
	HostVerified     bool            `json:"host_verified"`
	NotExported      NotExported     `json:"not_exported"`
	MaskedTouched    []string        `json:"masked_touched"`
	MaskedTouchedB64 []string        `json:"masked_touched_b64"`
	Warnings         []ExportWarning `json:"warnings"`
}

// SanitizeBranchPart makes text an agent controls usable inside a branch
// name: ASCII letters, digits, '.', '_', '-' and '/', no empty, dotted or
// ".lock" component, at most 100 bytes; "agent" when nothing is left.
func SanitizeBranchPart(s string) string {
	var segs []string
	for _, seg := range strings.Split(s, "/") {
		var b strings.Builder
		for _, r := range seg {
			if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
				b.WriteRune(r)
			} else {
				b.WriteByte('-')
			}
		}
		seg = b.String()
		for strings.Contains(seg, "..") {
			seg = strings.ReplaceAll(seg, "..", ".-")
		}
		seg = strings.TrimRight(strings.TrimLeft(seg, "."), ".")
		if strings.HasSuffix(seg, ".lock") {
			seg = strings.TrimSuffix(seg, ".lock") + "-lock"
		}
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	out := strings.Join(segs, "/")
	if len(out) > 100 {
		out = strings.Trim(out[:100], "/.")
	}
	if out == "" {
		return "agent"
	}
	return out
}

// DefaultBranch is the host branch an export creates by default.
func DefaultBranch(sandbox, agentBranch string) string {
	return "podbox/" + sandbox + "/" + SanitizeBranchPart(agentBranch)
}

// maskSet decides whether a committed path is masked in a sandbox: by the
// patterns (the forced ones and .sandboxignore), at the path or at a parent
// directory, or by the record of masked paths (which follows a rename). The
// match is also tried on the lower-cased path: on a case-insensitive host
// ".ENV" is the file ".env".
type maskSet struct {
	match func(rel string, dir bool) bool
	rec   map[string]bool
}

func newMaskSet(ignore []byte, tracked []workspace.Tracked) maskSet {
	s := maskSet{match: workspace.Matcher(ignore), rec: map[string]bool{}}
	for _, t := range tracked {
		s.rec[t.Rel] = true
		if t.Now != "" {
			s.rec[t.Now] = true
		}
	}
	return s
}

func (s maskSet) masked(p string) bool {
	for _, q := range []string{p, strings.ToLower(p)} {
		if s.match(q, false) || s.rec[q] {
			return true
		}
		for i := 0; i < len(q); i++ {
			if q[i] == '/' && (s.match(q[:i], true) || s.rec[q[:i]]) {
				return true
			}
		}
	}
	return false
}

// refuseMasked is the rule: commits that touch masked paths are refused
// unless the human asked to include them.
func refuseMasked(touched []string, include bool) error {
	if len(touched) == 0 || include {
		return nil
	}
	shown := touched
	if len(shown) > maxListed {
		shown = shown[:maxListed]
	}
	names := make([]string, len(shown))
	for i, p := range shown {
		names[i] = termsafe.CleanName(p)
	}
	more := ""
	if len(touched) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(touched)-len(shown))
	}
	return fmt.Errorf("%w: %s%s. The agent saw them empty or absent, so the export would overwrite the real files on the host; review with podbox diff, then pass --include-masked to export anyway", ErrMaskedTouched, strings.Join(names, ", "), more)
}

// parseStatus reads git status --porcelain=v1 -z: tracked paths with changes
// and untracked paths (ignored ones are not listed).
func parseStatus(out []byte) (uncommitted, untracked []string) {
	toks := splitNUL(out)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 {
			continue
		}
		switch {
		case strings.HasPrefix(t, "??"):
			untracked = append(untracked, t[3:])
		case strings.HasPrefix(t, "!!"):
		default:
			uncommitted = append(uncommitted, t[3:])
			if t[0] == 'R' || t[0] == 'C' || t[1] == 'R' || t[1] == 'C' {
				i++ // the source path follows
			}
		}
	}
	return
}

func listed(paths []string) []string {
	out := []string{}
	for i, p := range paths {
		if i == maxListed {
			break
		}
		out = append(out, termsafe.CleanName(p))
	}
	return out
}

var hookLike = map[string]bool{
	"lefthook.yml": true, "lefthook.yaml": true, ".lefthook.yml": true, ".lefthook.yaml": true, "lefthook-local.yml": true,
	".pre-commit-config.yaml": true, ".pre-commit-config.yml": true, "pre-commit": true, ".overcommit.yml": true,
}

// analyze lists what in a commit range a reviewer must look at, from git's
// raw records and the per-file patch sections (read with --text, so a NUL
// byte shows).
func analyze(raws []rawEntry, secs []patchSection) []ExportWarning {
	type acc struct {
		detail string
		paths  []string
	}
	by := map[string]*acc{}
	var order []string
	add := func(kind, detail, p string) {
		a := by[kind]
		if a == nil {
			a = &acc{detail: detail}
			by[kind] = a
			order = append(order, kind)
		}
		a.paths = append(a.paths, p)
	}
	for k, r := range raws {
		p, b := r.path, path.Base(r.path)
		lower := strings.ToLower(p)
		switch {
		case r.mode == "120000":
			add("symlink", "a symlink can point outside the project; check its target before using the branch", p)
		}
		if r.mode == "160000" || r.oldMode == "160000" || b == ".gitmodules" {
			add("submodule", "a gitlink or .gitmodules change can make git fetch another repository", p)
		}
		if b == ".gitattributes" {
			add("attributes", ".gitattributes can set filters and conversions that run programs on checkout", p)
		}
		if hookLike[b] || strings.HasPrefix(lower, ".husky/") || strings.HasPrefix(lower, ".githooks/") || strings.Contains(lower, "/.husky/") {
			add("hook-like", "a hook manager configuration runs programs on your next commit or push", p)
		}
		for _, c := range strings.Split(lower, "/") {
			if c == ".git" {
				add("git-internal", "a path component named .git; git refuses it on checkout", p)
				break
			}
		}
		if p == workspace.IgnoreFile || strings.HasPrefix(p, ".podbox/") {
			add("podbox-config", "podbox's own configuration changes what the next sandbox masks and allows", p)
		}
		if k < len(secs) && secs[k].nul && r.mode != "160000" {
			add("nul", "the file holds NUL bytes, so podbox diff hides its content unless --show-binary is used", p)
		}
	}
	n := nameNotes(raws)
	var warns []ExportWarning
	for _, kind := range order {
		a := by[kind]
		warns = append(warns, ExportWarning{Kind: kind, Detail: a.detail, Count: len(a.paths), Examples: listed(a.paths)[:min(len(a.paths), maxExamples)]})
	}
	if n.CaseCollisions > 0 {
		warns = append(warns, ExportWarning{Kind: "case-collision", Detail: "paths that differ only by case collide on a case-insensitive file system", Count: n.CaseCollisions, Examples: n.CaseExamples})
	}
	if n.NonASCII > 0 {
		warns = append(warns, ExportWarning{Kind: "non-ascii", Detail: "non-ASCII path characters can imitate ASCII ones", Count: n.NonASCII, Examples: n.NonASCIIExamples})
	}
	return warns
}

// fileCap writes to a file and refuses bytes past max.
type fileCap struct {
	f    *os.File
	n    int64
	max  int64
	over bool
}

func (w *fileCap) Write(p []byte) (int, error) {
	if w.n+int64(len(p)) > w.max {
		w.over = true
		return 0, ErrTooLarge
	}
	n, err := w.f.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *fileCap) exceeded() bool { return w.over }

// Export brings the agent's commits since the base out of a running clone
// sandbox: as a new branch of the host repository (a bundle the host verifies
// and fetches with object checks) or as a patch file. Only committed work
// goes; nothing is merged, checked out or overwritten.
func (m *Manager) Export(ctx context.Context, name string, opt ExportOptions) (res ExportResult, err error) {
	if opt.As != AsBranch && opt.As != AsPatch {
		return res, fmt.Errorf("%w: --as must be branch or patch", ErrBadExport)
	}
	if opt.As == AsPatch && opt.BranchName != "" || opt.As == AsBranch && opt.Out != "" {
		return res, fmt.Errorf("%w: --branch-name goes with --as branch, --out with --as patch", ErrBadExport)
	}
	meta, err := m.cloneMeta(ctx, name)
	if err != nil {
		return res, err
	}
	if !validBase.MatchString(meta.Base) {
		return res, fmt.Errorf("sandbox %s has no valid base commit (%q)", name, meta.Base)
	}
	if _, _, err := hostgit.Available(); err != nil {
		return res, err
	}
	tmp, err := os.MkdirTemp("", "podbox-export-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	host := hostgit.Host{WS: meta.Workspace, Home: tmp}
	if !host.IsCommit(ctx, meta.Base) {
		return res, fmt.Errorf("%w: %s (garbage collected or history rewritten); the clone cannot be exported to this repository", ErrBaseMissing, meta.Base)
	}
	res = ExportResult{Schema: ExportSchema, Name: name, As: opt.As, Base: meta.Base, CommitList: []CommitLine{}, MaskedTouched: []string{}, MaskedTouchedB64: []string{}, Warnings: []ExportWarning{}}

	sess := m.newGitSession(ctx, meta)
	if _, err := sess.harden(); err != nil {
		return res, err
	}
	env := sess.agent
	gitLine := func(sub ...string) (string, error) {
		out, err := sess.git(env, sub...)
		return strings.TrimSpace(string(out)), err
	}
	head, err := gitLine("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil || !validBase.MatchString(head) {
		return res, fmt.Errorf("the clone has no usable HEAD commit: %v", err)
	}
	res.Head = head
	agentBranch, err := gitLine("symbolic-ref", "-q", "--short", "HEAD")
	var ee *ExecError
	switch {
	case errors.As(err, &ee) && ee.Code == 1:
		agentBranch = ""
	case err != nil:
		return res, err
	}
	res.AgentBranch = termsafe.CleanName(agentBranch)

	if _, err := sess.git(env, "merge-base", "--is-ancestor", meta.Base, head); errors.As(err, &ee) && ee.Code == 1 {
		return res, fmt.Errorf("%w (%s): the history was rewritten or replaced; nothing was exported. Review the clone with podbox diff %s", ErrHistory, meta.Base, name)
	} else if err != nil {
		return res, err
	}
	cnt, err := gitLine("rev-list", "--count", head, "^"+meta.Base)
	if err != nil {
		return res, err
	}
	if res.Commits, err = strconv.Atoi(cnt); err != nil {
		return res, fmt.Errorf("unexpected commit count %q", cnt)
	}
	st, err := sess.git(env, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none", "--no-renames")
	if err != nil {
		return res, err
	}
	unc, untr := parseStatus(st)
	res.NotExported = NotExported{Uncommitted: len(unc), Untracked: len(untr), UncommittedFiles: listed(unc), UntrackedFiles: listed(untr)}
	if res.Commits == 0 {
		return res, nil
	}

	if opt.As == AsBranch {
		return m.exportBranch(ctx, sess, host, meta, res, agentBranch, opt, tmp)
	}
	return m.exportPatch(ctx, sess, meta, res, opt)
}

// maskRules reads what masks the sandbox has: the host's .sandboxignore
// snapshot and the record of masked paths.
func (m *Manager) maskRules(meta Meta) (maskSet, error) {
	ignore, _, err := readSnap(m.Store.ProjectIgnorePath(meta.Name))
	if err != nil {
		return maskSet{}, err
	}
	rec, err := m.Store.loadMasks(meta.Name)
	if err != nil {
		return maskSet{}, err
	}
	return newMaskSet(ignore, rec), nil
}

// review turns a commit range into the result: files, warnings, the masked
// paths touched (refused unless included).
func (m *Manager) review(meta Meta, res *ExportResult, raws []rawEntry, secs []patchSection, include bool) error {
	if len(raws) != len(secs) {
		return fmt.Errorf("git listed %d files but its patch has %d", len(raws), len(secs))
	}
	res.FilesChanged = len(raws)
	res.Warnings = analyze(raws, secs)
	rules, err := m.maskRules(meta)
	if err != nil {
		return err
	}
	var touched []string
	for _, r := range raws {
		if rules.masked(r.path) {
			touched = append(touched, r.path)
		}
	}
	for _, p := range touched {
		res.MaskedTouched = append(res.MaskedTouched, termsafe.CleanName(p))
		res.MaskedTouchedB64 = append(res.MaskedTouchedB64, base64.StdEncoding.EncodeToString([]byte(p)))
	}
	return refuseMasked(touched, include)
}

var rangeDiff = []string{"--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=none"}

func (m *Manager) exportBranch(ctx context.Context, sess *gitSession, host hostgit.Host, meta Meta, res ExportResult, agentBranch string, opt ExportOptions, tmp string) (ExportResult, error) {
	target := opt.BranchName
	if target == "" {
		if agentBranch == "" {
			return res, fmt.Errorf("%w: create a branch in the clone (git switch -c work), or pass --branch-name", ErrDetached)
		}
		target = DefaultBranch(meta.Name, agentBranch)
		res.BranchSanitized = target != "podbox/"+meta.Name+"/"+agentBranch
	}
	if err := host.CheckRefName(ctx, target); err != nil {
		return res, fmt.Errorf("%w: %v", ErrBadExport, err)
	}
	if ok, err := host.RefExists(ctx, "refs/heads/"+target); err != nil {
		return res, err
	} else if ok {
		return res, fmt.Errorf("%w: %s; choose another with --branch-name", hostgit.ErrRefExists, target)
	}

	file := filepath.Join(tmp, "export.bundle")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return res, err
	}
	w := &fileCap{f: f, max: maxExport}
	// HEAD names no agent-chosen ref in an argument; the host checks the
	// bundle's tip against the head read just before.
	err = sess.into(sess.agent, nil, w, "bundle", "create", "-", "HEAD", "^"+meta.Base)
	f.Close()
	if err != nil {
		if w.over {
			return res, fmt.Errorf("%w: the bundle is over %d MiB", ErrTooLarge, maxExport>>20)
		}
		return res, err
	}
	if err := host.BundleVerify(ctx, file); err != nil {
		return res, err
	}
	heads, err := host.BundleHeads(ctx, file)
	if err != nil {
		return res, err
	}
	if len(heads) != 1 || heads[0].Ref != "HEAD" {
		return res, fmt.Errorf("%w: expected one head (HEAD), found %d", hostgit.ErrBadBundle, len(heads))
	}
	if heads[0].ID != res.Head {
		return res, fmt.Errorf("%w: the bundle holds %s but the clone's HEAD was %s", ErrChanged, heads[0].ID, res.Head)
	}

	tip, err := host.ImportBranch(ctx, file, "HEAD", target, func(tip string) error {
		if !host.IsAncestor(ctx, meta.Base, tip) {
			return fmt.Errorf("%w (%s): the bundle's commits are not built on it; nothing was exported", ErrHistory, meta.Base)
		}
		n, err := host.CountRange(ctx, meta.Base, tip)
		if err != nil {
			return err
		}
		res.Commits = n
		out, err := host.Output(ctx, "diff-tree", "-r", "-z", "--no-commit-id", "--no-renames", meta.Base, tip)
		if err != nil {
			return err
		}
		raws, _, err := parseDiff(out)
		if err != nil {
			return err
		}
		sw := newSectionWriter(0, maxOutput)
		if err := host.Stream(ctx, sw, append(append([]string{"diff"}, rangeDiff...), "--text", meta.Base, tip, "--")...); err != nil {
			return err
		}
		if err := sw.finish(); err != nil {
			return err
		}
		if out, err = host.Output(ctx, "-c", "log.showSignature=false", "log", "-z", "--no-notes", "--no-textconv", "--format=%H %s", "-n", strconv.Itoa(maxListed), tip, "^"+meta.Base, "--"); err != nil {
			return err
		}
		for _, l := range splitNUL(out) {
			id, subj, _ := strings.Cut(l, " ")
			res.CommitList = append(res.CommitList, CommitLine{ID: id, Subject: termsafe.CleanName(subj)})
		}
		return m.review(meta, &res, raws, sw.secs, opt.IncludeMasked)
	})
	if err != nil {
		return res, err
	}
	res.Head, res.Branch, res.Exported, res.HostVerified = tip, target, true, true
	return res, nil
}

func (m *Manager) exportPatch(ctx context.Context, sess *gitSession, meta Meta, res ExportResult, opt ExportOptions) (ExportResult, error) {
	out := opt.Out
	if out == "" {
		out = meta.Name + ".patch"
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return res, err
	}
	if _, err := os.Lstat(out); err == nil {
		return res, fmt.Errorf("%w: %s; choose another with --out", ErrFileExists, out)
	}
	rng := []string{meta.Base + ".." + res.Head}
	// What the commits change, as the sandbox reports it: git in the sandbox
	// is the only source of a patch, so the host cannot check this list.
	raw, err := sess.git(sess.agent, "diff-tree", "-r", "-z", "--no-commit-id", "--no-renames", meta.Base, res.Head)
	if err != nil {
		return res, err
	}
	raws, _, err := parseDiff(raw)
	if err != nil {
		return res, err
	}
	sw := newSectionWriter(0, maxOutput)
	if err := sess.into(sess.agent, nil, sw, append(append([]string{"diff"}, rangeDiff...), "--text", meta.Base, res.Head, "--")...); err != nil {
		return res, err
	}
	if err := sw.finish(); err != nil {
		return res, err
	}
	if log, err := sess.git(sess.agent, "-c", "log.showSignature=false", "log", "-z", "--no-notes", "--no-textconv", "--format=%H %s", "-n", strconv.Itoa(maxListed), res.Head, "^"+meta.Base, "--"); err == nil {
		for _, l := range splitNUL(log) {
			id, subj, _ := strings.Cut(l, " ")
			res.CommitList = append(res.CommitList, CommitLine{ID: id, Subject: termsafe.CleanName(subj)})
		}
	} else {
		return res, err
	}
	if err := m.review(meta, &res, raws, sw.secs, opt.IncludeMasked); err != nil {
		return res, err
	}

	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return res, fmt.Errorf("%w: %s; choose another with --out", ErrFileExists, out)
	}
	if err != nil {
		return res, err
	}
	w := &fileCap{f: f, max: maxExport}
	args := append([]string{
		"-c", "format.useAutoBase=false", "-c", "format.coverLetter=false", "-c", "format.numbered=false", "-c", "format.thread=false",
		"-c", "format.attach=false", "-c", "format.signOff=false",
		"format-patch", "--stdout", "--binary", "--full-index", "--no-signature", "--no-notes", "--no-attach", "--no-cover-letter",
		"--no-numbered", "--no-thread", "--no-add-header", "--no-to", "--no-cc", "--no-ext-diff", "--no-textconv",
		"--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", "-M",
	}, rng...)
	err = sess.into(sess.agent, nil, w, append(args, "--")...)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(out) // only the file this call created: O_EXCL made it
		if w.over {
			return res, fmt.Errorf("%w: the patch is over %d MiB", ErrTooLarge, maxExport>>20)
		}
		return res, err
	}
	res.File, res.Exported = out, true
	return res, nil
}
