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
	"strconv"
	"strings"
	"time"
)

var (
	// ErrRefExists reports a branch that already exists; it is never overwritten.
	ErrRefExists = errors.New("branch already exists")
	// ErrBadBundle reports a bundle the host refuses.
	ErrBadBundle = errors.New("bundle rejected")
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
		return fmt.Errorf("%w: %v", ErrBadBundle, err)
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

// ImportBranch fetches ref of a verified bundle into a temporary ref with
// object checks on, hands the tip to check, and then creates refs/heads/target
// at the tip only if target does not exist (an update-ref with the zero old
// value: atomic, never an overwrite). The temporary ref is deleted on every
// path. It writes no index, no working tree, no config and no other ref.
// It returns the tip.
func (h Host) ImportBranch(ctx context.Context, file, ref, target string, check func(tip string) error) (string, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	tmp := "refs/podbox/tmp/" + hex.EncodeToString(rnd[:])
	defer func() { // also on failure: the fetch may have created it
		_, _ = h.Output(context.WithoutCancel(ctx), "update-ref", "-d", tmp)
	}()
	if _, err := h.Output(ctx, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "-c", "fetch.writeCommitGraph=false",
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--no-auto-maintenance", "--no-auto-gc",
		file, ref+":"+tmp); err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadBundle, err)
	}
	out, err := h.Output(ctx, "rev-parse", "--verify", "-q", tmp+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: the fetched tip is not a commit", ErrBadBundle)
	}
	tip := strings.TrimSpace(string(out))
	if err := check(tip); err != nil {
		return tip, err
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
