package hostgit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrRefExists reports a branch that already exists; it is never overwritten.
	ErrRefExists = errors.New("branch already exists")
	// ErrBadBundle reports a bundle the host refuses.
	ErrBadBundle = errors.New("bundle rejected")
	// ErrBundleVerify says which check refused a bundle: git bundle verify
	// (malformed, or prerequisites the repository lacks)...
	ErrBundleVerify = errors.New("bundle verify failed")
	// ErrObjectCheck ...or the object checks of the fetch (fsck).
	ErrObjectCheck = errors.New("object check failed")
	// ErrBadRefName reports a name git cannot use as a branch.
	ErrBadRefName = errors.New("invalid branch name")
)

// HostTimeout bounds each git command of Host.
const HostTimeout = 5 * time.Minute

// Host runs git in a host repository that podbox owns no part of: the
// clone's bundle is verified and imported into it. Every command ignores the
// host's global and system configuration (a fsck severity or a hook path set
// there must not weaken the import), runs no hook, and reads no clone.
type Host struct {
	WS   string // the repository top level
	Home string // an empty directory used as HOME and XDG_CONFIG_HOME
}

func (h Host) run(ctx context.Context, stdout io.Writer, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, HostTimeout)
	defer cancel()
	extra := []string{"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "HOME=" + h.Home, "XDG_CONFIG_HOME=" + h.Home}
	if stdout != nil {
		return nil, h.stream(ctx, extra, stdout, args)
	}
	return run(ctx, h.WS, MaxOutput, extra, nil, append([]string{"-c", "maintenance.auto=false", "-c", "core.fsmonitor=false"}, args...)...)
}

func (h Host) stream(ctx context.Context, extra []string, w io.Writer, args []string) error {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, base...), args...)...)
	cmd.Dir = h.WS
	cmd.Env = append(env(os.Environ()), extra...)
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = w, &limitedString{b: &stderr}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type limitedString struct{ b *strings.Builder }

func (l *limitedString) Write(p []byte) (int, error) {
	if l.b.Len() < 2000 {
		l.b.Write(p)
	}
	return len(p), nil
}

// Stream runs git with its stdout copied to w (no cap here: w caps).
func (h Host) Stream(ctx context.Context, w io.Writer, args ...string) error {
	_, err := h.run(ctx, w, args...)
	return err
}

// Output runs git and returns its capped stdout.
func (h Host) Output(ctx context.Context, args ...string) ([]byte, error) {
	return h.run(ctx, nil, args...)
}

// CheckRefName validates a branch name for refs/heads: git's own rules, plus
// no leading dash (it would read as an option in later commands), and no
// name git expands (@{-1}) or refuses as a branch (HEAD).
func (h Host) CheckRefName(ctx context.Context, name string) error {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\n") {
		return fmt.Errorf("%w: %q", ErrBadRefName, name)
	}
	out, err := h.Output(ctx, "check-ref-format", "--branch", name)
	if err != nil || strings.TrimRight(string(out), "\n") != name {
		return fmt.Errorf("%w: %q", ErrBadRefName, name)
	}
	if _, err := h.Output(ctx, "check-ref-format", "refs/heads/"+name); err != nil {
		return fmt.Errorf("%w: %q", ErrBadRefName, name)
	}
	return nil
}

// RefExists reports whether a full ref name exists.
func (h Host) RefExists(ctx context.Context, ref string) (bool, error) {
	out, err := h.Output(ctx, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == ref, nil
}

// IsCommit reports whether id names a commit in the host repository.
func (h Host) IsCommit(ctx context.Context, id string) bool {
	_, err := h.Output(ctx, "cat-file", "-e", id+"^{commit}")
	return err == nil
}

// IsAncestor reports whether a is an ancestor of b (or equal).
func (h Host) IsAncestor(ctx context.Context, a, b string) bool {
	_, err := h.Output(ctx, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// CountRange counts the commits reachable from tip and not from base.
func (h Host) CountRange(ctx context.Context, base, tip string) (int, error) {
	out, err := h.Output(ctx, "rev-list", "--count", tip, "^"+base)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// BundleVerify checks a bundle against the host repository: well formed and
// its prerequisite commits present.
func (h Host) BundleVerify(ctx context.Context, file string) error {
	if _, err := h.Output(ctx, "bundle", "verify", file); err != nil {
		return fmt.Errorf("%w: %w: %v", ErrBadBundle, ErrBundleVerify, err)
	}
	return nil
}

// BundleHead is one ref a bundle holds.
type BundleHead struct{ ID, Ref string }

// BundleHeads lists the refs of a bundle.
func (h Host) BundleHeads(ctx context.Context, file string) ([]BundleHead, error) {
	out, err := h.Output(ctx, "bundle", "list-heads", file)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBundle, err)
	}
	var heads []BundleHead
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			heads = append(heads, BundleHead{ID: f[0], Ref: f[1]})
		}
	}
	return heads, nil
}

// fsckConfig turns the object checks on. The fetch that matters runs in the
// quarantine repository, which has no configuration of the host's.
func fsckConfig() []string {
	return []string{"-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true"}
}

// quarantine makes an empty bare repository that reads the host's objects
// (alternates) and none of its configuration, hooks or attributes. The bundle
// is fetched and reviewed there, so the host's repository-local settings
// (fsck severities, a skip list, an include) cannot weaken the object checks,
// and a bundle that is refused leaves no object in the host repository.
func (h Host) quarantine(ctx context.Context) (Host, error) {
	dir, err := os.MkdirTemp(h.Home, "quarantine-")
	if err != nil {
		return Host{}, err
	}
	q := Host{WS: dir, Home: h.Home}
	if _, err := q.Output(ctx, "init", "--bare", "-q", "--template=", q.WS); err != nil {
		return q, err
	}
	out, err := h.Output(ctx, "rev-parse", "--git-path", "objects")
	if err != nil {
		return q, err
	}
	objects := strings.TrimSpace(string(out))
	if !filepath.IsAbs(objects) {
		objects = filepath.Join(h.WS, objects)
	}
	return q, os.WriteFile(filepath.Join(q.WS, "objects", "info", "alternates"), []byte(objects+"\n"), 0o600)
}

// ImportBranch brings ref of a verified bundle into the host repository as
// the new branch refs/heads/target. The bundle is fetched, with object checks,
// into a quarantine repository (see quarantine); check sees that repository
// and the tip, and refuses by returning an error. Only then are the objects
// fetched into the host, into a temporary ref, and the branch created with
// update-ref and the zero old value: atomic, never an overwrite. The temporary
// ref is deleted on every path. The index, working tree, configuration and
// other refs are never written. It returns the tip.
func (h Host) ImportBranch(ctx context.Context, file, ref, target string, check func(q Host, tip string) error) (string, error) {
	q, err := h.quarantine(ctx)
	defer os.RemoveAll(q.WS)
	if err != nil {
		return "", err
	}
	const qref = "refs/podbox/tip"
	if _, err := q.Output(ctx, append(fsckConfig(), "-c", "fetch.writeCommitGraph=false",
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", "--no-auto-gc",
		file, ref+":"+qref)...); err != nil {
		return "", fmt.Errorf("%w: %w: %v", ErrBadBundle, ErrObjectCheck, err)
	}
	out, err := q.Output(ctx, "rev-parse", "--verify", "-q", qref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: the fetched tip is not a commit", ErrBadBundle)
	}
	tip := strings.TrimSpace(string(out))
	if err := check(q, tip); err != nil {
		return tip, err
	}

	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return tip, err
	}
	tmp := "refs/podbox/tmp/" + hex.EncodeToString(rnd[:])
	defer func() { // also on failure and on an interrupt: the fetch may have created it
		_, _ = h.Output(context.WithoutCancel(ctx), "update-ref", "-d", tmp)
	}()
	if _, err := h.Output(ctx, append(fsckConfig(), "-c", "fetch.writeCommitGraph=false",
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", "--no-auto-gc",
		q.WS, qref+":"+tmp)...); err != nil {
		return tip, fmt.Errorf("%w: %w: %v", ErrBadBundle, ErrObjectCheck, err)
	}
	if got, err := h.Output(ctx, "rev-parse", "--verify", "-q", tmp+"^{commit}"); err != nil || strings.TrimSpace(string(got)) != tip {
		return tip, fmt.Errorf("%w: the objects fetched into the repository are not the ones reviewed", ErrBadBundle)
	}
	zero := strings.Repeat("0", len(tip))
	if _, err := h.Output(ctx, "update-ref", "-m", "podbox export", "refs/heads/"+target, tip, zero); err != nil {
		if ok, _ := h.RefExists(ctx, "refs/heads/"+target); ok {
			return tip, fmt.Errorf("%w: %s", ErrRefExists, target)
		}
		return tip, err
	}
	return tip, nil
}
