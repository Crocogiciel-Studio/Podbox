package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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

// DefaultMaxExport caps a bundle or a patch read from the sandbox.
const DefaultMaxExport = 256 << 20

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
	// ErrPatchMismatch reports a patch that changes other files than the review listed.
	ErrPatchMismatch = errors.New("the patch does not match the reviewed changes")
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

// Validate checks the options on their own, before any sandbox is looked at.
func (o ExportOptions) Validate() error {
	switch {
	case o.As != AsBranch && o.As != AsPatch:
		return fmt.Errorf("%w: --as is required: branch or patch", ErrBadExport)
	case o.As == AsPatch && o.BranchName != "":
		return fmt.Errorf("%w: --branch-name goes with --as branch", ErrBadExport)
	case o.As == AsBranch && o.Out != "":
		return fmt.Errorf("%w: --out goes with --as patch", ErrBadExport)
	}
	return nil
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
		// Cut, then clean again: the cut can leave a trailing '/', '.' or ".lock".
		out = SanitizeBranchPart(out[:100])
	}
	if out == "" {
		return "agent"
	}
	return out
}

// DefaultBranch is the host branch an export creates by default.
func DefaultBranch(sandbox, agentBranch string) string {
	return "podbox/" + SanitizeBranchPart(sandbox) + "/" + SanitizeBranchPart(agentBranch)
}

// maskSet decides whether a committed path is masked in a sandbox: by the
// patterns (the forced ones and .sandboxignore), at the path or at a parent
// directory, or by the record of masked paths (which follows a rename). The
// host may ignore case (macOS, Windows), so everything is lower-cased: the
// patterns, the recorded paths and the candidate. It over-matches by design;
// Unicode normalisation and NTFS trailing dots, spaces and short names are not
// handled.
type maskSet struct {
	match func(rel string, dir bool) bool
	rec   map[string]bool
}

func newMaskSet(ignore []byte, tracked []workspace.Tracked) maskSet {
	s := maskSet{match: workspace.MatcherFold(ignore), rec: map[string]bool{}}
	for _, t := range tracked {
		s.rec[strings.ToLower(t.Rel)] = true
		if t.Now != "" {
			s.rec[strings.ToLower(t.Now)] = true
		}
	}
	return s
}

func (s maskSet) masked(p string) bool {
	q := strings.ToLower(p)
	if s.match(q, false) || s.rec[q] {
		return true
	}
	for i := 0; i < len(q); i++ {
		if q[i] == '/' && (s.match(q[:i], true) || s.rec[q[:i]]) {
			return true
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

func without(paths []string, rules maskSet) []string {
	var out []string
	for _, p := range paths {
		if !rules.masked(p) {
			out = append(out, p)
		}
	}
	return out
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

func hasPath(l []string, p string) bool {
	for _, x := range l {
		if x == p {
			return true
		}
	}
	return false
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
		if len(a.paths) == 0 || !hasPath(a.paths, p) { // a path changed in several commits counts once
			a.paths = append(a.paths, p)
		}
	}
	for k, r := range raws {
		p := r.path
		lower := strings.ToLower(p)
		b := path.Base(lower) // the host may ignore case: .GitAttributes is .gitattributes
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
		if lower == workspace.IgnoreFile || strings.HasPrefix(lower, ".podbox/") {
			add("podbox-config", "podbox's own configuration changes what the next sandbox masks and allows", p)
		}
		if k < len(secs) && secs[k].nul && r.mode != "160000" {
			add("nul", "the file holds NUL bytes, so podbox diff hides its content unless --show-binary is used", p)
		}
	}
	var once []rawEntry
	seenPath := map[string]bool{}
	for _, r := range raws {
		if !seenPath[r.path] {
			seenPath[r.path] = true
			once = append(once, r)
		}
	}
	n := nameNotes(once)
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
	if err := opt.Validate(); err != nil {
		return res, err
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
	rules, err := m.maskRules(meta)
	if err != nil {
		return res, err
	}
	// A masked tracked file is a device in the clone, so git always lists it as
	// changed: that is the mask, not the agent's work.
	unc, untr := parseStatus(st)
	unc, untr = without(unc, rules), without(untr, rules)
	res.NotExported = NotExported{Uncommitted: len(unc), Untracked: len(untr), UncommittedFiles: listed(unc), UntrackedFiles: listed(untr)}
	if res.Commits == 0 {
		return res, nil
	}

	if opt.As == AsBranch {
		return m.exportBranch(ctx, sess, host, meta, res, agentBranch, opt, tmp)
	}
	return m.exportPatch(ctx, sess, host, meta, res, opt)
}

func (m *Manager) exportCap() int64 {
	if m.MaxExport > 0 {
		return m.MaxExport
	}
	return DefaultMaxExport
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

// gitRunner runs read-only git for the range review, in the sandbox or on the
// host, with the same arguments.
type gitRunner struct {
	out    func(args ...string) ([]byte, error)
	stream func(w limitedWriter, args ...string) error
}

func sandboxRunner(sess *gitSession) gitRunner {
	return gitRunner{
		out:    func(a ...string) ([]byte, error) { return sess.git(sess.agent, a...) },
		stream: func(w limitedWriter, a ...string) error { return sess.into(sess.agent, nil, w, a...) },
	}
}

func hostRunner(ctx context.Context, h hostgit.Host) gitRunner {
	return gitRunner{
		out:    func(a ...string) ([]byte, error) { return h.Output(ctx, a...) },
		stream: func(w limitedWriter, a ...string) error { return h.Stream(ctx, w, a...) },
	}
}

// rangeDiff are the options of every diff of a range: nothing a configuration
// can change, no program run. No --text: with core.bigFileThreshold, git calls
// a file past it binary without reading it, which keeps a decompression bomb
// from being inflated by the review; a binary file counts as hidden.
var rangeDiff = []string{"--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=none"}

// bigFile is the size past which the review does not read a blob.
const bigFile = hostgit.BigFile

// maxReviewSections caps the memory the review keeps (the start of each file's patch).
const maxReviewSections = 256 << 20

// hiddenContent: the file holds NUL bytes, or git did not show it as text.
func hiddenContent(s patchSection) bool {
	return s.nul || bytes.Contains(s.data, []byte("\nBinary files ")) || bytes.Contains(s.data, []byte("\nGIT binary patch"))
}

// readSections streams a patch into sections, in the order git printed the files.
func readSections(run gitRunner, args ...string) ([]patchSection, error) {
	sw := newSectionWriter(16<<10, maxReviewSections)
	if err := run.stream(sw, args...); err != nil {
		return nil, err
	}
	if err := sw.finish(); err != nil {
		return nil, fmt.Errorf("%w: the review of the commits is too large", err)
	}
	for i := range sw.secs {
		sw.secs[i].nul = hiddenContent(sw.secs[i])
	}
	return sw.secs, nil
}

// parseRaw reads -z --raw records and nothing else.
func parseRaw(out []byte) ([]rawEntry, error) {
	raws, nums, err := parseDiff(out)
	if err == nil && len(nums) > 0 {
		err = errors.New("unexpected git output")
	}
	return raws, err
}

// rangeFacts is what a commit range holds: the net change base..tip, and the
// change of every commit (merges against each parent), so a path touched in
// one commit and put back in a later one is still seen.
type rangeFacts struct {
	net  []rawEntry
	all  []rawEntry // net and history, one entry per path, mode and blob
	secs []patchSection
}

func uniqueKey(r rawEntry) string {
	return r.path + "\x00" + r.mode + "\x00" + r.oldMode + "\x00" + r.id
}

func reviewRange(run gitRunner, base, tip string) (rangeFacts, error) {
	var f rangeFacts
	pre := []string{"-c", "core.bigFileThreshold=" + bigFile, "-c", "log.showSignature=false"}
	pair := func(rawArgs, patchArgs []string) ([]rawEntry, []patchSection, error) {
		out, err := run.out(rawArgs...)
		if err != nil {
			return nil, nil, err
		}
		raws, err := parseRaw(out)
		if err != nil {
			return nil, nil, err
		}
		secs, err := readSections(run, patchArgs...)
		if err != nil {
			return nil, nil, err
		}
		if len(raws) != len(secs) {
			return nil, nil, fmt.Errorf("git listed %d files but its patch has %d", len(raws), len(secs))
		}
		return raws, secs, nil
	}
	net, netSecs, err := pair(
		append(append([]string{}, pre...), "diff-tree", "-r", "-z", "--no-commit-id", "--no-renames", base, tip),
		append(append(append([]string{}, pre...), "diff"), append(rangeDiff, base, tip, "--")...))
	if err != nil {
		return f, err
	}
	hist, histSecs, err := pair(
		append(append([]string{}, pre...), "log", "-z", "--raw", "--no-abbrev", "-m", "--no-renames", "--no-ext-diff", "--format=", tip, "^"+base, "--"),
		append(append(append([]string{}, pre...), "log", "-p", "-m", "--format="), append(rangeDiff, tip, "^"+base, "--")...))
	if err != nil {
		return f, err
	}
	f.net = net
	seen := map[string]int{}
	for _, part := range []struct {
		raws []rawEntry
		secs []patchSection
	}{{net, netSecs}, {hist, histSecs}} {
		for k, r := range part.raws {
			if i, ok := seen[uniqueKey(r)]; ok {
				f.secs[i].nul = f.secs[i].nul || part.secs[k].nul
				continue
			}
			seen[uniqueKey(r)] = len(f.all)
			f.all = append(f.all, r)
			f.secs = append(f.secs, patchSection{nul: part.secs[k].nul})
		}
	}
	return f, nil
}

// commitList reads the first commits of the range, cleaned.
func commitList(run gitRunner, base, tip string) ([]CommitLine, error) {
	out, err := run.out("-c", "log.showSignature=false", "log", "-z", "--no-notes", "--no-textconv", "--format=%H %s", "-n", strconv.Itoa(maxListed), tip, "^"+base, "--")
	if err != nil {
		return nil, err
	}
	list := []CommitLine{}
	for _, l := range splitNUL(out) {
		id, subj, _ := strings.Cut(l, " ")
		list = append(list, CommitLine{ID: id, Subject: termsafe.CleanName(subj)})
	}
	return list, nil
}

// review fills the result from a commit range and refuses it when it touches
// masked paths (unless included).
func (m *Manager) review(meta Meta, res *ExportResult, run gitRunner, tip string, include bool) ([]rawEntry, error) {
	f, err := reviewRange(run, meta.Base, tip)
	if err != nil {
		return nil, err
	}
	if res.CommitList, err = commitList(run, meta.Base, tip); err != nil {
		return nil, err
	}
	res.FilesChanged = len(f.net)
	res.Warnings = analyze(f.all, f.secs)
	rules, err := m.maskRules(meta)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var touched []string
	for _, r := range f.all {
		if !seen[r.path] && rules.masked(r.path) {
			seen[r.path] = true
			touched = append(touched, r.path)
		}
	}
	for _, p := range touched {
		res.MaskedTouched = append(res.MaskedTouched, termsafe.CleanName(p))
		res.MaskedTouchedB64 = append(res.MaskedTouchedB64, base64.StdEncoding.EncodeToString([]byte(p)))
	}
	return f.net, refuseMasked(touched, include)
}

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
	w := &fileCap{f: f, max: m.exportCap()}
	// HEAD names no agent-chosen ref in an argument; the host checks the
	// bundle's tip against the head read just before.
	err = sess.into(sess.agent, nil, w, "bundle", "create", "-", "HEAD", "^"+meta.Base)
	f.Close()
	if err != nil {
		if w.over {
			return res, fmt.Errorf("%w: the bundle is over %d MiB", ErrTooLarge, m.exportCap()>>20)
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

	tip, err := host.ImportBranch(ctx, file, "HEAD", target, func(q hostgit.Host, tip string) error {
		if !q.IsAncestor(ctx, meta.Base, tip) {
			return fmt.Errorf("%w (%s): the bundle's commits are not built on it; nothing was exported", ErrHistory, meta.Base)
		}
		n, err := q.CountRange(ctx, meta.Base, tip)
		if err != nil {
			return err
		}
		res.Commits = n
		_, err = m.review(meta, &res, hostRunner(ctx, q), tip, opt.IncludeMasked)
		return err
	})
	if err != nil {
		return res, err
	}
	res.Head, res.Branch, res.Exported, res.HostVerified = tip, target, true, true
	return res, nil
}

// patchArgs is the one combined diff base..head: no commit message, so nothing
// the file holds was not reviewed, and the merges' changes are in it.
func patchArgs(base, head string) []string {
	return []string{"diff", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color",
		"--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", base, head, "--"}
}

// checkPatchFile has the host's git read the finished patch and compares the
// files it would change with the reviewed list; a difference refuses it.
func checkPatchFile(ctx context.Context, host hostgit.Host, file string, net []rawEntry) error {
	out, err := host.Output(ctx, "apply", "--numstat", "-z", file)
	if err != nil {
		return fmt.Errorf("the host's git cannot read the patch: %v", err)
	}
	got := map[string]bool{}
	for _, rec := range splitNUL(out) {
		f := strings.SplitN(rec, "\t", 3)
		if len(f) != 3 {
			return fmt.Errorf("unexpected git apply output %q", rec)
		}
		got[f[2]] = true
	}
	want := map[string]bool{}
	for _, r := range net {
		want[r.path] = true
	}
	if len(got) != len(want) {
		return fmt.Errorf("%w: the patch changes %d files, the review listed %d", ErrPatchMismatch, len(got), len(want))
	}
	for p := range got {
		if !want[p] {
			return fmt.Errorf("%w: the patch changes %q, which the review did not list", ErrPatchMismatch, termsafe.CleanName(p))
		}
	}
	return nil
}

func (m *Manager) exportPatch(ctx context.Context, sess *gitSession, host hostgit.Host, meta Meta, res ExportResult, opt ExportOptions) (ExportResult, error) {
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
	// What the commits change, as the sandbox reports it: git in the sandbox
	// is the only source of a patch, so the host cannot check this list against
	// objects, only against the patch it is given.
	run := sandboxRunner(sess)
	net, err := m.review(meta, &res, run, res.Head, opt.IncludeMasked)
	if err != nil {
		return res, err
	}
	// The patch is written and verified under a temporary name in the same
	// directory, then linked to its final name: link fails if the name exists
	// (nothing is overwritten, a symlink there is not followed), and the final
	// name never holds a partial or unverified file, even if podbox is killed.
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return res, err
	}
	tmp := filepath.Join(filepath.Dir(out), "."+filepath.Base(out)+".tmp-"+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return res, err
	}
	defer os.Remove(tmp) // after a successful link too: the final name is another link
	w := &fileCap{f: f, max: m.exportCap()}
	err = sess.into(sess.agent, nil, w, patchArgs(meta.Base, res.Head)...)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = checkPatchFile(ctx, host, tmp, net)
	}
	if err != nil {
		if w.over {
			return res, fmt.Errorf("%w: the patch is over %d MiB", ErrTooLarge, m.exportCap()>>20)
		}
		return res, err
	}
	if err := os.Link(tmp, out); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return res, fmt.Errorf("%w: %s; choose another with --out", ErrFileExists, out)
		}
		return res, fmt.Errorf("cannot create %s (the patch is written with a hard link, so the directory must allow them): %w", out, err)
	}
	res.File, res.Exported = out, true
	return res, nil
}
