package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Crocogiciel-Studio/podbox/internal/termsafe"
)

// DiffSchema is the version of the JSON of podbox diff.
const DiffSchema = 1

// maxIgnoredList caps what is read of the ignored paths, which only get counted.
const maxIgnoredList = 1 << 20

var validBase = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// DiffOptions are the choices of Diff.
type DiffOptions struct {
	// Patch includes the patch (without it, only the file list).
	Patch bool
	// ShowBinary prints the content of files that hold NUL bytes, which is
	// otherwise summarised; it needs Patch.
	ShowBinary bool
}

// DiffResult is what the agent changed in its clone since the base commit.
// Every string in it has been through termsafe; Patch is nil without the patch.
type DiffResult struct {
	Schema int          `json:"schema"`
	Name   string       `json:"name"`
	Base   string       `json:"base"`
	Files  []FileChange `json:"files"`
	Patch  *string      `json:"patch,omitempty"`
	// Ignored counts the untracked ignored paths (a directory counts once)
	// that are not in Files; IgnoredCapped says the count stopped at a limit.
	Ignored       int  `json:"ignored"`
	IgnoredCapped bool `json:"ignored_capped,omitempty"`
	// Filters names the git filters the agent configured or its attributes
	// use. They are never run by the diff, which shows the raw bytes, but a
	// commit by the agent's own git would pass the files through them.
	Filters []string `json:"filters"`
	NameNotes
}

// gitArgs is a git command line, bounded by timeout inside the container (so
// a killed podman client leaves nothing running), that ignores the clone's
// configuration for everything that could run a program or change the format.
func gitArgs(project string, limit time.Duration, sub ...string) []string {
	return append([]string{"timeout", "-k", "5", strconv.Itoa(int(limit.Seconds())), "git",
		"-c", "safe.directory=" + project, "-c", "core.pager=cat", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"-c", "core.sparseCheckout=false", "-c", "core.attributesFile=/dev/null", "-c", "diff.external=", "-c", "diff.ignoreSubmodules=none",
		"-c", "core.quotepath=off", "-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
	}, sub...)
}

// Diff reports what the agent changed in its clone since the base commit, as
// of the moment it runs (the worktree is live; this is not a snapshot of what
// an export would bring): commits, uncommitted changes, every file the agent's
// index or HEAD tracks (even one a .gitignore lists), and untracked files.
// Git runs in the agent container on a temporary index, so the agent's index,
// config and objects are not written.
//
// The diff shows the raw bytes of the worktree: every filter the agent's
// configuration defines is replaced by /bin/cat, so a clean filter cannot show
// a reviewer other bytes than the file holds. Attributes never decide what is
// readable either: the diff is made with --text, and a file is binary when its
// content holds a NUL byte. A file git's attributes call binary but whose
// content is text is shown as text (BinaryAttr).
func (m *Manager) Diff(ctx context.Context, name string, opt DiffOptions) (DiffResult, error) {
	meta, err := m.cloneMeta(ctx, name)
	if err != nil {
		return DiffResult{}, err
	}
	if !validBase.MatchString(meta.Base) {
		return DiffResult{}, fmt.Errorf("sandbox %s has no valid base commit (%q)", name, meta.Base)
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return DiffResult{}, err
	}
	idx := "/tmp/podbox-diff-" + hex.EncodeToString(rnd[:])
	defer m.runIn(context.WithoutCancel(ctx), meta, []string{"rm", "-f", idx, idx + ".lock"}, nil)
	sess := m.newGitSession(ctx, meta)
	gitInto, git := sess.into, sess.git

	// Every filter the agent defined becomes cat before any git call reads the worktree.
	names, err := sess.harden()
	if err != nil {
		return DiffResult{}, err
	}
	agent := sess.agent
	temp := merge(agent, map[string]string{"GIT_INDEX_FILE": idx})

	// The temporary index starts as the base tree...
	if _, err := git(temp, "read-tree", meta.Base); err != nil {
		return DiffResult{}, err
	}
	// ...gets every path the agent tracks, even one a .gitignore lists, as intent-to-add...
	force, err := m.trackedOutsideBase(git, agent, meta.Base)
	if err != nil {
		return DiffResult{}, err
	}
	if len(force) > 0 {
		stdin := []byte(strings.Join(force, "\x00") + "\x00")
		if err := gitInto(merge(temp, map[string]string{"GIT_LITERAL_PATHSPECS": "1"}), stdin, &capWriter{max: maxStderr, quiet: true}, "add", "-f", "-N", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return DiffResult{}, err
		}
	}
	// ...then what is untracked and not ignored, which is what is left over.
	others, err := git(temp, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return DiffResult{}, err
	}
	if _, err := git(temp, "add", "-N", "."); err != nil {
		return DiffResult{}, err
	}

	diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", "-M"}
	// One call gives the list and the counts, so they cannot disagree. --text
	// keeps attributes from making a file unreadable.
	rawOut, err := git(temp, append(diff, "--text", "-z", "--raw", "--numstat", meta.Base, "--")...)
	if err != nil {
		return DiffResult{}, err
	}
	raws, nums, err := parseDiff(rawOut)
	if err != nil {
		return DiffResult{}, err
	}
	// What git itself, attributes included, calls binary.
	plainOut, err := git(temp, append(diff, "-z", "--numstat", meta.Base, "--")...)
	if err != nil {
		return DiffResult{}, err
	}
	plain, err := parseNumstat(splitNUL(plainOut))
	if err != nil {
		return DiffResult{}, err
	}
	gitBinary := map[string]bool{}
	for _, e := range plain {
		if e.binary {
			gitBinary[e.path] = true
		}
	}
	// The patch is read even for --stat, to look at the content of each file,
	// but only as far as the sections need.
	keep := statKeep
	if opt.Patch {
		keep = maxFilePatch
	}
	sw := newSectionWriter(keep, maxOutput)
	if err := gitInto(temp, nil, sw, append(diff, "--text", meta.Base, "--")...); err != nil {
		return DiffResult{}, err
	}
	if err := sw.finish(); err != nil {
		return DiffResult{}, fmt.Errorf("%w: more than %d MiB", err, maxOutput>>20)
	}
	if len(sw.secs) != len(raws) {
		return DiffResult{}, fmt.Errorf("git listed %d files but its patch has %d", len(raws), len(sw.secs))
	}
	files, err := buildChanges(raws, nums, sw.secs, gitBinary, set(splitNUL(others)))
	if err != nil {
		return DiffResult{}, err
	}
	filters, err := m.conversions(gitInto, temp, raws, files, names)
	if err != nil {
		return DiffResult{}, err
	}
	res := DiffResult{Schema: DiffSchema, Name: name, Base: meta.Base, Files: files, Filters: filters, NameNotes: nameNotes(raws)}

	ign := &capWriter{max: maxIgnoredList, quiet: true}
	if err := gitInto(temp, nil, ign, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"); err != nil {
		return DiffResult{}, err
	}
	list := splitNUL(ign.buf.Bytes())
	if ign.cut && len(list) > 0 {
		list = list[:len(list)-1] // the last name may be cut
	}
	res.Ignored, res.IgnoredCapped = len(list), ign.cut

	for k := range res.Files {
		f := &res.Files[k]
		f.TooLarge = !f.Binary && sw.secs[k].size > int64(len(sw.secs[k].data)) && opt.Patch
		f.BinaryHidden = f.Binary && !(opt.Patch && opt.ShowBinary)
	}
	if opt.Patch {
		var patch bytes.Buffer
		for k, sec := range sw.secs {
			switch f := res.Files[k]; {
			case f.Binary && opt.ShowBinary:
				patch.Write(shownBinary(sec))
			case f.Binary:
				patch.Write(hiddenBinary(sec))
			case f.TooLarge:
				patch.Write(tooLarge(sec))
			default:
				patch.Write(sec.data)
			}
		}
		p := string(termsafe.Clean(patch.Bytes()))
		res.Patch = &p
	}
	return res, nil
}

// configuredFilters lists the filter names the agent's configuration defines,
// whatever file or include defines them.
func (m *Manager) configuredFilters(git func(map[string]string, ...string) ([]byte, error), env map[string]string) ([]string, error) {
	out, err := git(env, "config", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
	var ee *ExecError
	if errors.As(err, &ee) && ee.Code == 1 { // no match
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var names []string
	for _, key := range splitNUL(out) {
		rest := strings.TrimPrefix(key, "filter.")
		if i := strings.LastIndexByte(rest, '.'); i > 0 && !seen[rest[:i]] {
			seen[rest[:i]] = true
			names = append(names, rest[:i])
		}
	}
	sort.Strings(names)
	return names, nil
}

// maxFilters bounds the filters overridden through the environment.
const maxFilters = 100

// filterOverrides turns every named filter into cat, as environment config
// (GIT_CONFIG_KEY_n, which has no "=" to confuse a name). An empty process
// and required=false are needed: git otherwise starts the process filter or
// fails when a required filter is unset.
func filterOverrides(names []string) (map[string]string, error) {
	if len(names) > maxFilters {
		return nil, fmt.Errorf("the clone defines %d git filters; refusing to diff it", len(names))
	}
	env := map[string]string{}
	n := 0
	for _, name := range names {
		for _, kv := range [][2]string{{"clean", "/bin/cat"}, {"smudge", "/bin/cat"}, {"process", ""}, {"required", "false"}} {
			env[fmt.Sprintf("GIT_CONFIG_KEY_%d", n)] = "filter." + name + "." + kv[0]
			env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", n)] = kv[1]
			n++
		}
	}
	if n > 0 {
		env["GIT_CONFIG_COUNT"] = strconv.Itoa(n)
	}
	return env, nil
}

// conversions records, per changed file, the attributes that make git convert
// it on the way in, and returns the filter names to flag: the configured ones
// and those the attributes name.
func (m *Manager) conversions(gitInto func(map[string]string, []byte, limitedWriter, ...string) error, env map[string]string, raws []rawEntry, files []FileChange, configured []string) ([]string, error) {
	seen := map[string]bool{}
	var filters []string
	add := func(n string) {
		n = termsafe.CleanName(n)
		if !seen[n] {
			seen[n] = true
			filters = append(filters, n)
		}
	}
	for _, n := range configured {
		add(n)
	}
	if len(raws) > 0 {
		var in strings.Builder
		for _, r := range raws {
			in.WriteString(r.path + "\x00")
		}
		out := &capWriter{max: maxOutput}
		if err := gitInto(env, []byte(in.String()), out, "check-attr", "-z", "--stdin", "filter", "ident", "working-tree-encoding"); err != nil {
			return nil, err
		}
		toks := splitNUL(out.buf.Bytes())
		at := map[string]int{}
		for k, r := range raws {
			at[r.path] = k
		}
		for i := 0; i+2 < len(toks); i += 3 {
			path, attr, val := toks[i], toks[i+1], toks[i+2]
			k, ok := at[path]
			if !ok || val == "unspecified" || val == "unset" {
				continue
			}
			var c string
			switch attr {
			case "filter":
				c = "filter=" + val
				add(val)
			case "ident":
				if val != "set" {
					continue
				}
				c = "ident"
			default:
				c = attr + "=" + val
			}
			files[k].Conversions = append(files[k].Conversions, termsafe.CleanName(c))
		}
	}
	sort.Strings(filters)
	if filters == nil {
		filters = []string{}
	}
	return filters, nil
}

// trackedOutsideBase lists the paths the agent's index or HEAD track that the
// base tree does not hold and that exist on disk: they must be in the diff
// whether or not a .gitignore lists them.
func (m *Manager) trackedOutsideBase(git func(map[string]string, ...string) ([]byte, error), agent map[string]string, base string) ([]string, error) {
	cached, err := git(agent, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	deleted, err := git(agent, "ls-files", "-z", "--deleted")
	if err != nil {
		return nil, err
	}
	head, err := git(agent, "ls-tree", "-r", "-z", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	inBase, err := git(agent, "ls-tree", "-r", "-z", "--name-only", base)
	if err != nil {
		return nil, err
	}
	skip, gone, idx := set(splitNUL(inBase)), set(splitNUL(deleted)), set(splitNUL(cached))
	var force, headOnly []string
	for _, p := range splitNUL(cached) {
		if !skip[p] && !gone[p] {
			force = append(force, p)
		}
	}
	for _, p := range splitNUL(head) {
		if !skip[p] && !idx[p] {
			headOnly = append(headOnly, p)
		}
	}
	// In HEAD but dropped from the index: keep those still on disk. Ask about
	// those paths only, so a huge untracked tree cannot make this fail.
	literal := map[string]string{"GIT_LITERAL_PATHSPECS": "1"}
	for k, v := range agent {
		literal[k] = v
	}
	for len(headOnly) > 0 {
		chunk := headOnly
		if len(chunk) > 200 {
			chunk = chunk[:200]
		}
		headOnly = headOnly[len(chunk):]
		onDisk, err := git(literal, append([]string{"ls-files", "-z", "--others", "--"}, chunk...)...)
		if err != nil {
			return nil, err
		}
		force = append(force, splitNUL(onDisk)...)
	}
	return force, nil
}
