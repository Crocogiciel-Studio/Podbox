package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
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
// config and objects are not written. The patch is left out unless withPatch.
//
// Attributes never decide what is readable: the diff is made with --text, and
// a file is binary when its content holds a NUL byte. A file git's attributes
// call binary but whose content is text is shown as text (BinaryAttr).
func (m *Manager) Diff(ctx context.Context, name string, withPatch bool) (DiffResult, error) {
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
	limit := m.GitTimeout
	if limit <= 0 {
		limit = DefaultGitTimeout
	}
	agent := map[string]string{ // the agent's own index: read only
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_OPTIONAL_LOCKS": "0",
		"GIT_NO_REPLACE_OBJECTS": "1", "GIT_ATTR_NOSYSTEM": "1",
	}
	temp := map[string]string{"GIT_INDEX_FILE": idx}
	for k, v := range agent {
		temp[k] = v
	}
	project := WorkspaceTarget(m.GOOS, meta.Workspace)
	fail := func(sub string, err error) error {
		var ee *ExecError
		if errors.As(err, &ee) && (ee.Code == 124 || ee.Code == 137) {
			return fmt.Errorf("%w after %ds; the sandbox may contain a hostile filter or hook", ErrTimeout, int(limit.Seconds()))
		}
		return fmt.Errorf("git %s: %w", sub, err)
	}
	gitInto := func(env map[string]string, stdin []byte, out *capWriter, sub ...string) error {
		var in io.Reader
		if stdin != nil {
			in = bytes.NewReader(stdin)
		}
		if err := m.runInto(ctx, meta, gitArgs(project, limit, sub...), env, in, out); err != nil {
			return fail(sub[0], err)
		}
		return nil
	}
	git := func(env map[string]string, sub ...string) ([]byte, error) {
		out := &capWriter{max: maxOutput}
		if err := gitInto(env, nil, out, sub...); err != nil {
			return nil, err
		}
		return out.buf.Bytes(), nil
	}

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
		env := map[string]string{"GIT_LITERAL_PATHSPECS": "1"}
		for k, v := range temp {
			env[k] = v
		}
		stdin := []byte(strings.Join(force, "\x00") + "\x00")
		if err := gitInto(env, stdin, &capWriter{max: maxStderr, quiet: true}, "add", "-f", "-N", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
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
	// The patch is read even for --stat, to look at the content of each file;
	// there it is truncated at the cap rather than refused.
	patchOut := &capWriter{max: maxOutput, quiet: !withPatch}
	if err := gitInto(temp, nil, patchOut, append(diff, "--text", meta.Base, "--")...); err != nil {
		return DiffResult{}, err
	}
	sections := splitSections(patchOut.buf.Bytes())
	if withPatch && len(sections) != len(raws) {
		return DiffResult{}, fmt.Errorf("git listed %d files but its patch has %d", len(raws), len(sections))
	}
	if patchOut.cut && len(sections) > 0 {
		sections = sections[:len(sections)-1] // the last one is cut
	}
	files, err := buildChanges(raws, nums, sections, gitBinary, set(splitNUL(others)))
	if err != nil {
		return DiffResult{}, err
	}
	res := DiffResult{Schema: DiffSchema, Name: name, Base: meta.Base, Files: files, NameNotes: nameNotes(raws)}

	ign := &capWriter{max: maxIgnoredList, quiet: true}
	if err := gitInto(temp, nil, ign, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"); err != nil {
		return DiffResult{}, err
	}
	list := splitNUL(ign.buf.Bytes())
	if ign.cut && len(list) > 0 {
		list = list[:len(list)-1] // the last name may be cut
	}
	res.Ignored, res.IgnoredCapped = len(list), ign.cut

	if withPatch {
		var patch bytes.Buffer
		for k, sec := range sections {
			if files[k].Binary {
				sec = binaryStub(sec)
			}
			patch.Write(sec)
		}
		p := string(termsafe.Clean(patch.Bytes()))
		res.Patch = &p
	}
	return res, nil
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
	if len(headOnly) > 0 { // in HEAD but dropped from the index: keep those still on disk
		onDisk, err := git(agent, "ls-files", "-z", "--others")
		if err != nil {
			return nil, err
		}
		here := set(splitNUL(onDisk))
		for _, p := range headOnly {
			if here[p] {
				force = append(force, p)
			}
		}
	}
	return force, nil
}
