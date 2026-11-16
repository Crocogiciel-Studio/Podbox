package sandbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Crocogiciel-Studio/podbox/internal/termsafe"
)

// FileChange is one file that differs from the clone's base commit. Strings
// have been through termsafe, so Path and OldPath are for display only:
// cleaning is lossy, and a later command must find a file by PathB64.
type FileChange struct {
	Path       string `json:"path"`
	PathB64    string `json:"path_b64"` // the raw path, base64: lossless
	OldPath    string `json:"old_path,omitempty"`
	OldPathB64 string `json:"old_path_b64,omitempty"`
	// Status is added, modified, deleted, renamed, typechange, unmerged or unknown.
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	// Binary: the content has NUL bytes; it is summarised, never printed.
	Binary bool `json:"binary"`
	// BinaryAttr: git attributes mark the file binary but its content is text;
	// it is shown as text.
	BinaryAttr bool `json:"binary_attr"`
	// BinaryHidden: the file is binary and its content is not in this output
	// (the default, and --stat); an export should ask the human first.
	BinaryHidden bool `json:"binary_hidden"`
	// TooLarge: the file's patch is past the per-file cap and was left out.
	TooLarge  bool `json:"too_large"`
	Untracked bool `json:"untracked"`
	// Conversions lists what attributes make git do to this file before it
	// stores it (filter=<name>, ident, working-tree-encoding=<enc>): the diff
	// shows the raw bytes, so a commit by the agent's own git may differ.
	Conversions []string `json:"conversions,omitempty"`
	// Gitlink marks a nested repository or submodule (mode 160000): only its
	// commit is compared, not its files.
	Gitlink bool `json:"gitlink"`
	Symlink bool `json:"symlink"`
	Exec    bool `json:"exec"`
	// Mode is the git mode of the file now (of the old file when deleted).
	Mode    string `json:"mode"`
	OldMode string `json:"old_mode,omitempty"`
	ID      string `json:"id,omitempty"` // blob id when git knows it (not for a file only on disk)
	OldID   string `json:"old_id,omitempty"`
}

type rawEntry struct {
	status        byte
	oldMode, mode string
	oldID, id     string
	oldPath, path string
}

type numEntry struct {
	added, deleted int
	binary         bool
	path           string
}

// parseDiff reads the -z output of git diff --raw --numstat: the raw records,
// which start with ':', then the numstat records.
func parseDiff(out []byte) ([]rawEntry, []numEntry, error) {
	toks := splitNUL(out)
	var raws []rawEntry
	i := 0
	for i < len(toks) && strings.HasPrefix(toks[i], ":") {
		f := strings.Fields(toks[i])
		if len(f) != 5 {
			return nil, nil, fmt.Errorf("unexpected git diff output %q", toks[i])
		}
		e := rawEntry{status: f[4][0], oldMode: strings.TrimPrefix(f[0], ":"), mode: f[1], oldID: f[2], id: f[3]}
		n := 1
		if e.status == 'R' || e.status == 'C' { // C needs -C, which is not asked; both carry two names
			n = 2
		}
		if i+n >= len(toks) {
			return nil, nil, errors.New("truncated git diff output")
		}
		e.path = toks[i+n]
		if n == 2 {
			e.oldPath = toks[i+1]
		}
		raws = append(raws, e)
		i += 1 + n
	}
	nums, err := parseNumstat(toks[i:])
	return raws, nums, err
}

// parseNumstat reads -z numstat records.
func parseNumstat(toks []string) ([]numEntry, error) {
	var nums []numEntry
	for i := 0; i < len(toks); i++ {
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected git numstat output %q", toks[i])
		}
		e := numEntry{path: f[2], binary: f[0] == "-" && f[1] == "-"}
		e.added, _ = strconv.Atoi(f[0])
		e.deleted, _ = strconv.Atoi(f[1])
		if e.path == "" { // rename: the next two names are the old and the new
			if i+2 >= len(toks) {
				return nil, errors.New("truncated git numstat output")
			}
			e.path = toks[i+2]
			i += 2
		}
		nums = append(nums, e)
	}
	return nums, nil
}

var statusNames = map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'T': "typechange", 'U': "unmerged"}

const zeroMode = "000000"

func realID(id string) string {
	if strings.Trim(id, "0") == "" {
		return ""
	}
	return id
}

// buildChanges joins what git said about the changed files. counts come from
// the --text diff, binary from the content of each file's patch section (a
// file with no section keeps the verdict of gitBinary), and gitBinary is what
// git itself, attributes included, calls binary. A list and counts that do
// not match are an error, never a zero.
func buildChanges(raws []rawEntry, nums []numEntry, sections []patchSection, gitBinary, untracked map[string]bool) ([]FileChange, error) {
	if len(nums) != len(raws) {
		return nil, fmt.Errorf("git listed %d files but counted %d", len(raws), len(nums))
	}
	files := []FileChange{}
	for k, r := range raws {
		if nums[k].path != r.path {
			return nil, fmt.Errorf("git listed %q but counted %q", r.path, nums[k].path)
		}
		st := statusNames[r.status]
		if st == "" {
			st = "unknown"
		}
		mode, old := r.mode, r.oldMode
		if mode == zeroMode { // deleted: the file that was
			mode = r.oldMode
		}
		if old == zeroMode {
			old = ""
		}
		f := FileChange{
			Path: termsafe.CleanName(r.path), PathB64: base64.StdEncoding.EncodeToString([]byte(r.path)),
			Status: st, Added: nums[k].added, Deleted: nums[k].deleted,
			Untracked: untracked[r.path] && st == "added",
			Gitlink:   r.mode == "160000" || r.oldMode == "160000", Symlink: mode == "120000", Exec: mode == "100755",
			Mode: mode, OldMode: old, ID: realID(r.id), OldID: realID(r.oldID),
		}
		if r.oldPath != "" {
			f.OldPath, f.OldPathB64 = termsafe.CleanName(r.oldPath), base64.StdEncoding.EncodeToString([]byte(r.oldPath))
		}
		gb := gitBinary[r.path]
		if k < len(sections) && bytes.Contains(sections[k].data, []byte("\n@@ ")) { // a rename or a mode change has no content to look at
			f.Binary = sections[k].nul
			f.BinaryAttr = gb && !f.Binary
		} else {
			f.Binary = gb
		}
		if f.Binary {
			f.Added, f.Deleted = 0, 0
		}
		files = append(files, f)
	}
	return files, nil
}

// NameNotes reports paths a reviewer can misread: names that differ only by
// case (they collide on a case-insensitive file system) and non-ASCII names
// (which can imitate ASCII ones).
type NameNotes struct {
	CaseCollisions   int      `json:"case_collisions"`
	CaseExamples     []string `json:"case_examples,omitempty"`
	NonASCII         int      `json:"non_ascii"`
	NonASCIIExamples []string `json:"non_ascii_examples,omitempty"`
}

const maxExamples = 5

func nameNotes(raws []rawEntry) NameNotes {
	var n NameNotes
	byFold := map[string][]string{}
	for _, r := range raws {
		k := strings.ToLower(r.path)
		byFold[k] = append(byFold[k], r.path)
		for i := 0; i < len(r.path); i++ {
			if r.path[i] >= 0x80 {
				n.NonASCII++
				if len(n.NonASCIIExamples) < maxExamples {
					n.NonASCIIExamples = append(n.NonASCIIExamples, termsafe.CleanName(r.path))
				}
				break
			}
		}
	}
	keys := make([]string, 0, len(byFold))
	for k, v := range byFold {
		if len(v) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		n.CaseCollisions += len(byFold[k])
		for _, p := range byFold[k] {
			if len(n.CaseExamples) < maxExamples {
				n.CaseExamples = append(n.CaseExamples, termsafe.CleanName(p))
			}
		}
	}
	return n
}

func splitNUL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

func set(toks []string) map[string]bool {
	m := make(map[string]bool, len(toks))
	for _, t := range toks {
		m[t] = true
	}
	return m
}
