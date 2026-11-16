// Package hostgit runs git on the host. It is the only place that does:
// the environment is controlled, and git never runs inside an agent's
// clone, whose configuration the agent can write.
package hostgit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// ErrMissing reports git absent from PATH.
	ErrMissing = errors.New("git not found in PATH: install git 2.29 or newer to use --clone")
	// ErrTooOld reports a git older than MinMajor.MinMinor.
	ErrTooOld = errors.New("git is older than 2.29")
	// ErrNotRepo reports a directory that is not a git repository.
	ErrNotRepo = errors.New("not a git repository")
	// ErrNotTopLevel reports a directory inside a repository but not its top level.
	ErrNotTopLevel = errors.New("not the top level of its git repository")
	// ErrEmptyRepo reports a repository with no commit.
	ErrEmptyRepo = errors.New("the git repository has no commit")
)

const minMajor, minMinor = 2, 29

// MaxOutput caps what Run reads from git's stdout.
const MaxOutput = 64 << 20

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// Available locates git and checks its version.
func Available() (path, version string, err error) {
	path, err = exec.LookPath("git")
	if err != nil {
		return "", "", ErrMissing
	}
	out, err := Run(context.Background(), "", nil, "--version")
	if err != nil {
		return path, "", err
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return path, "", fmt.Errorf("cannot read the git version from %q", strings.TrimSpace(string(out)))
	}
	version = m[0]
	ma, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	if ma < minMajor || ma == minMajor && mi < minMinor {
		return path, version, fmt.Errorf("%w (found %s)", ErrTooOld, version)
	}
	return path, version, nil
}

// env is the process environment without any GIT_* variable.
func env(environ []string) []string {
	out := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
}

// base is prepended to every command: nothing in the repository
// configuration or the host may make git run a program.
var base = []string{"-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-c", "protocol.ext.allow=never", "-c", "core.hooksPath=/dev/null"}

type capWriter struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		w.over = true
		return 0, errors.New("output cap exceeded")
	}
	return w.buf.Write(p)
}

// Run runs git in dir (the current directory when empty) and returns its
// stdout. Output past MaxOutput is an error.
func Run(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	return run(ctx, dir, MaxOutput, nil, stdin, args...)
}

// run is Run with a cap and variables set after the GIT_* filter.
func run(ctx context.Context, dir string, limit int, extra []string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, base...), args...)...)
	cmd.Dir = dir
	cmd.Env = append(env(os.Environ()), extra...)
	cmd.Stdin = stdin
	out := &capWriter{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	err := cmd.Run()
	if out.over {
		return nil, fmt.Errorf("git %s: output larger than %d bytes", args[0], limit)
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrMissing
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		return nil, fmt.Errorf("git %s: %w: %s", subcommand(args), err, msg)
	}
	return out.buf.Bytes(), nil
}

// subcommand is the first argument that is not a -c option and its value.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return args[0]
}

// Repo is what Inspect learns about a host project.
type Repo struct {
	Head        string // HEAD commit
	Branch      string // empty when HEAD is detached
	Uncommitted int    // tracked files with changes, staged or not
	Untracked   int    // untracked files, not ignored
}

// Inspect checks that ws is the top level of a git repository with a
// commit, and counts what a clone would leave out.
func Inspect(ctx context.Context, ws string) (Repo, error) {
	var r Repo
	out, err := Run(ctx, ws, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return r, fmt.Errorf("%w: %s (%v)", ErrNotRepo, ws, err)
	}
	top := strings.TrimRight(string(out), "\n")
	if !sameDir(top, ws) {
		return r, fmt.Errorf("%w: %s is inside %s; run podbox from the top level", ErrNotTopLevel, ws, top)
	}
	out, err = Run(ctx, ws, nil, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if err != nil {
		return r, fmt.Errorf("%w: %s", ErrEmptyRepo, ws)
	}
	r.Head = strings.TrimSpace(string(out))
	if out, err = Run(ctx, ws, nil, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		r.Branch = strings.TrimSpace(string(out))
	}
	if out, err = Run(ctx, ws, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all"); err != nil {
		return r, err
	}
	toks := strings.Split(string(out), "\x00")
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 3 {
			continue
		}
		switch {
		case strings.HasPrefix(t, "??"):
			r.Untracked++
		case strings.HasPrefix(t, "!!"):
		default:
			r.Uncommitted++
			if t[0] == 'R' || t[0] == 'C' || t[1] == 'R' || t[1] == 'C' {
				i++ // the source path follows
			}
		}
	}
	return r, nil
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// PartialDir is where Clone stages dest: a name no sandbox can have (names
// start with a letter or a digit), so it never collides with a live clone.
func PartialDir(dest string) string {
	return filepath.Join(filepath.Dir(dest), ".partial-"+filepath.Base(dest))
}

// fileURL is the file:// URL of an absolute path, percent-encoded; a
// Windows drive path (C:\x) gets its leading slash (file:///C:/x).
func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// Clone makes dest a clone of the committed state of ws at base: no shared
// object, no remote, no host hook and no host configuration. branch is the
// checked out branch, empty for a detached HEAD. dest is created through
// PartialDir; a failure leaves neither. Git reads neither the host's global
// nor its system configuration here: an insteadOf or a protocol rule there
// must not redirect or refuse the clone.
func Clone(ctx context.Context, ws, dest, base, branch string) (err error) {
	partial := PartialDir(dest)
	if err := os.RemoveAll(partial); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmpl, err := os.MkdirTemp("", "podbox-template-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpl)
	isolated := []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "HOME=" + tmpl, "XDG_CONFIG_HOME=" + tmpl}
	git := func(dir string, args ...string) ([]byte, error) {
		return run(ctx, dir, MaxOutput, isolated, nil, args...)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(partial)
		}
	}()
	// A file:// URL sends only the objects the cloned branch reaches, not
	// the unreachable ones a plain-path clone would copy.
	args := []string{"clone", "--no-hardlinks", "--single-branch", "--no-tags", "--origin", "origin", "--template=" + tmpl}
	if branch != "" {
		args = append(args, "--branch="+branch)
	} else {
		args = append(args, "--no-checkout")
	}
	if _, err = git("", append(args, fileURL(ws), partial)...); err != nil {
		return err
	}
	if branch == "" {
		if _, err = git(partial, "checkout", "-q", "--detach", base); err != nil {
			return err
		}
	}
	if _, err = git(partial, "remote", "remove", "origin"); err != nil {
		return err
	}
	if branch != "" {
		// Absent after remote remove on current git; ignore "no such section".
		_, _ = git(partial, "config", "--remove-section", "branch."+branch)
	}
	head, err := git(partial, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(string(head)); got != base {
		return fmt.Errorf("clone is at %s, expected %s", got, base)
	}
	if err = checkConfig(filepath.Join(partial, ".git", "config")); err != nil {
		return err
	}
	return os.Rename(partial, dest)
}

// checkConfig verifies a fresh clone carries no remote, url or user value.
func checkConfig(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.ToLower(strings.TrimSpace(l))
		if strings.HasPrefix(l, "[remote") || strings.HasPrefix(l, "[user") || strings.HasPrefix(l, "[url") ||
			strings.HasPrefix(l, "url") || strings.HasPrefix(l, "[credential") || strings.HasPrefix(l, "[include") {
			return fmt.Errorf("fresh clone config holds %q", l)
		}
	}
	return nil
}
