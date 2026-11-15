package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// The agent can write the workspace, including symbolic links to any host
// path. Host-side access to workspace content goes through an os.Root
// opened on the workspace (nothing resolves outside it) and through these
// helpers, which also refuse every symbolic link and special file on the
// way (nothing resolves through a link the agent planted). Paths are
// slash-separated and relative to the root.

// ErrLink reports a workspace path that is, or goes through, a symbolic
// link or a special file. It wraps ErrUnsafe.
var ErrLink = fmt.Errorf("%w: symbolic link or special file in the workspace", ErrUnsafe)

// Oversized is the hash recorded for a file larger than the hashing cap.
const Oversized = "oversized"

func linkErr(rel, what string) error {
	return fmt.Errorf("%w: %s is %s; podbox never follows it", ErrLink, rel, what)
}

func split(rel string) []string {
	rel = path.Clean(strings.TrimPrefix(rel, "/"))
	if rel == "." {
		return nil
	}
	return strings.Split(rel, "/")
}

// OpenDir opens the directory rel below r, refusing a symbolic link or a
// non-directory at any component. Each step is verified to be the
// directory that was checked, so a link swapped in meanwhile is refused.
func OpenDir(r *os.Root, rel string) (*os.Root, error) {
	cur, err := r.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	done := ""
	for _, name := range split(rel) {
		done = path.Join(done, name)
		fi, err := cur.Lstat(name)
		if err == nil && (fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir()) {
			err = linkErr(done, describe(fi)+", not a directory")
		}
		var next *os.Root
		if err == nil {
			next, err = cur.OpenRoot(name)
		}
		if err == nil {
			if st, serr := next.Stat("."); serr != nil || !os.SameFile(fi, st) {
				next.Close()
				err = linkErr(done, "replaced while podbox opened it")
			}
		}
		cur.Close()
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}

func describe(fi fs.FileInfo) string {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return "a symbolic link"
	case fi.IsDir():
		return "a directory"
	case fi.Mode().IsRegular():
		return "a regular file"
	}
	return "a special file"
}

// parent opens the directory holding rel and returns it with rel's base name.
func parent(r *os.Root, rel string) (*os.Root, string, error) {
	parts := split(rel)
	if len(parts) == 0 {
		return nil, "", fmt.Errorf("%q names the workspace itself", rel)
	}
	d, err := OpenDir(r, strings.Join(parts[:len(parts)-1], "/"))
	return d, parts[len(parts)-1], err
}

// Lstat returns the information of rel itself (a link is not followed),
// refusing a symbolic link in its parent components.
func Lstat(r *os.Root, rel string) (fs.FileInfo, error) {
	if len(split(rel)) == 0 {
		return r.Lstat(".")
	}
	d, base, err := parent(r, rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.Lstat(base)
}

// Check verifies that rel exists and that it and every component on the
// way are a plain directory or, for the last one, a regular file.
func Check(r *os.Root, rel string) error {
	fi, err := Lstat(r, rel)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() && !fi.Mode().IsRegular() {
		return linkErr(rel, describe(fi))
	}
	return nil
}

// OpenFile opens the regular file rel for reading (see OpenDir). A FIFO
// swapped in cannot block: it is opened non-blocking and then refused.
func OpenFile(r *os.Root, rel string) (*os.File, error) {
	d, base, err := parent(r, rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	fi, err := d.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, linkErr(rel, describe(fi)+", not a regular file")
	}
	f, err := d.OpenFile(base, os.O_RDONLY|nonBlock, 0)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
		f.Close()
		return nil, linkErr(rel, "replaced while podbox opened it")
	}
	return f, nil
}

// ErrTooLarge reports a workspace file over the size podbox reads.
var ErrTooLarge = errors.New("file too large")

// ReadFile reads the regular file rel, at most max bytes (see OpenFile).
func ReadFile(r *os.Root, rel string, max int64) ([]byte, error) {
	f, err := OpenFile(r, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		return nil, fmt.Errorf("%s: %w (over %d bytes)", rel, ErrTooLarge, max)
	}
	return b, err
}

// HashFile streams the sha256 of the regular file rel; a file over max
// bytes is not read through and hashes to Oversized.
func HashFile(r *os.Root, rel string, max int64) (string, error) {
	f, err := OpenFile(r, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	switch {
	case err != nil:
		return "", err
	case n > max:
		return Oversized, nil
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteFile replaces the regular file rel atomically: a temporary file in
// the same directory, renamed over rel, inside r. Missing parent
// directories are created; a symbolic link or special file on the way, or
// at rel, is refused. Rename never writes through a hard link either.
func WriteFile(r *os.Root, rel string, b []byte) error {
	parts := split(rel)
	if len(parts) == 0 {
		return fmt.Errorf("%q names the workspace itself", rel)
	}
	for i := range parts[:len(parts)-1] {
		dir := strings.Join(parts[:i+1], "/")
		if _, err := Lstat(r, dir); errors.Is(err, fs.ErrNotExist) {
			if err := r.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
	}
	d, base, err := parent(r, rel)
	if err != nil {
		return err
	}
	defer d.Close()
	if fi, err := d.Lstat(base); err == nil && !fi.Mode().IsRegular() {
		return linkErr(rel, describe(fi)+", not a regular file")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	tmp := "." + base + "." + hex.EncodeToString(rnd[:]) + ".tmp"
	f, err := d.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer d.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return d.Rename(tmp, base)
}

// Readlink returns the target of the symbolic link rel, refusing a link in
// its parent components.
func Readlink(r *os.Root, rel string) (string, error) {
	d, base, err := parent(r, rel)
	if err != nil {
		return "", err
	}
	defer d.Close()
	return d.Readlink(base)
}
