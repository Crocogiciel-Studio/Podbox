//go:build !windows

package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func link(t *testing.T, root, from, to string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(filepath.Join(root, to)), 0o755)
	if err := os.Link(filepath.Join(root, from), filepath.Join(root, to)); err != nil {
		t.Fatal(err)
	}
}

// TestHardLinksMaskedInAnyOrder: every hard link of a masked file is masked
// and recorded, whether it sorts before or after the masked file, and stays
// masked once recorded.
func TestHardLinksMaskedInAnyOrder(t *testing.T) {
	for _, alias := range []string{"a.txt", "z.txt", "sub/m.txt"} {
		t.Run(alias, func(t *testing.T) {
			root := tree(t, map[string]string{"q/cred.key": "S", "q/plain.txt": "p"})
			link(t, root, "q/cred.key", "q/"+alias)
			res, err := Resolve(root, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := "q/cred.key q/" + alias
			if alias < "cred.key" {
				want = "q/" + alias + " q/cred.key"
			}
			if got := maskList(res); got != want {
				t.Fatalf("masks %q, want %q", got, want)
			}
			if len(res.Tracked) != 2 {
				t.Fatalf("tracked %+v", res.Tracked)
			}
			// Recorded: the link alone stays masked once the original is gone.
			if err := os.Remove(filepath.Join(root, "q/cred.key")); err != nil {
				t.Fatal(err)
			}
			again, err := Resolve(root, nil, res.Tracked)
			if err != nil || !strings.Contains(maskList(again), "q/"+alias) {
				t.Fatalf("%s after removal: %v %v", alias, maskList(again), err)
			}
		})
	}
}

// TestLinkIntoMaskedDirectory: a link outside a masked directory to a file
// inside it is masked.
func TestLinkIntoMaskedDirectory(t *testing.T) {
	root := tree(t, map[string]string{"secrets/db.txt": "pw", "a/notes.txt": "n"})
	link(t, root, "secrets/db.txt", "a/copy.txt")
	res, err := Resolve(root, []byte("secrets/\n"), nil)
	if err != nil || maskList(res) != "a/copy.txt secrets/" {
		t.Fatalf("%s %v", maskList(res), err)
	}
}

// TestSpecialFileAtRecordedPath: a FIFO at a recorded path is masked there;
// a symbolic link refuses the start with an accurate message.
func TestSpecialFileAtRecordedPath(t *testing.T) {
	root := tree(t, map[string]string{"cfg/prod.yaml": "v1", "cfg/other.yaml": "v1"})
	first, err := Resolve(root, []byte("/cfg/*.yaml\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"prod.yaml", "other.yaml"} {
		if err := os.Remove(filepath.Join(root, "cfg", f)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(root, "cfg/prod.yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "cfg/other.yaml")); err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(root, nil, first.Tracked)
	if err != nil || maskList(res) != "cfg/prod.yaml" {
		t.Fatalf("%s %v", maskList(res), err)
	}
	if len(res.Missing) != 1 || !strings.Contains(res.Missing[0], "cfg/other.yaml (now a symbolic link") {
		t.Fatalf("missing %v", res.Missing)
	}
	if err := MissingError("v", res.Missing); !errors.Is(err, ErrMasks) {
		t.Fatalf("%v", err)
	}
}

func inode(fi fs.FileInfo) uint64 { return fi.Sys().(*syscall.Stat_t).Ino }

// TestResolveRandomLinksAndRenames resolves random trees of secrets, hard
// links and directory renames, carrying the record like start does, and
// checks independently that no path sharing a masked identity is visible.
func TestResolveRandomLinksAndRenames(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		rng := rand.New(rand.NewSource(seed))
		root := t.TempDir()
		dirs := []string{"d0", "d1", "d2", "d1/e"}
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		var files []string
		for i := 0; i < 2+rng.Intn(8); i++ {
			name := fmt.Sprintf("%s/f%d.txt", dirs[rng.Intn(len(dirs))], i)
			if rng.Intn(3) == 0 {
				name = strings.TrimSuffix(name, ".txt") + ".key"
			}
			writeFile(t, filepath.Join(root, name), "x")
			files = append(files, name)
		}
		secret := map[uint64]bool{} // identities masked so far: never unmasked
		var tracked []Tracked
		for round := 0; round < 4; round++ {
			for i := 0; i < rng.Intn(4); i++ { // links in random places and orders
				from := files[rng.Intn(len(files))]
				to := fmt.Sprintf("%s/%c%d", dirs[rng.Intn(len(dirs))], 'a'+rune(rng.Intn(26)), rng.Intn(100))
				if _, err := os.Lstat(filepath.Join(root, from)); err != nil {
					continue
				}
				if _, err := os.Lstat(filepath.Join(root, to)); err == nil {
					continue
				}
				link(t, root, from, to)
			}
			if rng.Intn(2) == 0 { // rename a top-level directory, as the agent may
				from := fmt.Sprintf("d%d", rng.Intn(3))
				to := fmt.Sprintf("r%d_%d", round, rng.Intn(10))
				_ = os.Rename(filepath.Join(root, from), filepath.Join(root, to))
			}
			res, err := Resolve(root, nil, tracked)
			if err != nil {
				t.Fatalf("seed %d round %d: %v", seed, round, err)
			}
			tracked = res.Tracked
			masked := map[string]bool{}
			for _, m := range res.Masks {
				masked[m.Rel] = true
			}
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				fi, _ := d.Info()
				rel, _ := filepath.Rel(root, p)
				if strings.HasSuffix(rel, ".key") {
					secret[inode(fi)] = true
				}
				return nil
			})
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				fi, _ := d.Info()
				rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
				if secret[inode(fi)] && !masked[rel] && !covered(rel, masked) {
					t.Fatalf("seed %d round %d: %s shares a secret identity and is visible; masks %s", seed, round, rel, maskList(res))
				}
				if masked[rel] && path.Ext(rel) != ".key" && !secret[inode(fi)] {
					t.Fatalf("seed %d round %d: %s masked without reason", seed, round, rel)
				}
				return nil
			})
		}
	}
}
