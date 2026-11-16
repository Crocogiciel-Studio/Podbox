package sandbox

import (
	"bytes"
	"fmt"
)

const (
	// maxFilePatch is how much of one file's patch is kept; past it the file
	// is "too large" and, when binary, shown truncated.
	maxFilePatch = 4 << 20
	// maxBinaryShown is how much of a binary file --show-binary prints.
	maxBinaryShown = 256 << 10
	// statKeep is how much of each patch section --stat keeps: the headers and
	// the first hunk line, enough to tell whether there is content.
	statKeep = 8 << 10
)

// patchSection is one file's part of a patch: the first bytes, the whole
// size, and whether any byte of it is NUL.
type patchSection struct {
	data []byte
	size int64
	nul  bool
}

// sectionWriter cuts a patch stream at the "diff --git " header of each file
// (only a header starts a line that way; a content line begins with ' ', '+',
// '-' or '\'), keeping at most keep bytes of each section and at most max in
// all, while still counting the size of, and looking for NUL in, every byte.
type sectionWriter struct {
	keep, max int
	secs      []patchSection
	cur       bool
	carry     []byte
	total     int
	over      bool
}

const sectionSep = "\ndiff --git "

func newSectionWriter(keep, max int) *sectionWriter {
	return &sectionWriter{keep: keep, max: max, carry: []byte("\n")}
}

func (w *sectionWriter) exceeded() bool { return w.over }

func (w *sectionWriter) Write(p []byte) (int, error) {
	b := append(append(make([]byte, 0, len(w.carry)+len(p)), w.carry...), p...)
	for {
		i := bytes.Index(b, []byte(sectionSep))
		if i < 0 {
			break
		}
		if err := w.add(b[:i+1]); err != nil {
			return 0, err
		}
		w.secs, w.cur = append(w.secs, patchSection{}), true
		b = b[i+1:]
	}
	hold := len(sectionSep) - 1
	if len(b) > hold {
		if err := w.add(b[:len(b)-hold]); err != nil {
			return 0, err
		}
		b = b[len(b)-hold:]
	}
	w.carry = append(w.carry[:0], b...)
	return len(p), nil
}

// finish adds the bytes held back to spot a header across two writes.
func (w *sectionWriter) finish() error {
	err := w.add(w.carry)
	w.carry = nil
	return err
}

func (w *sectionWriter) add(b []byte) error {
	if !w.cur || len(b) == 0 {
		return nil
	}
	s := &w.secs[len(w.secs)-1]
	s.size += int64(len(b))
	if bytes.IndexByte(b, 0) >= 0 {
		s.nul = true
	}
	if room := w.keep - len(s.data); room > 0 {
		if room > len(b) {
			room = len(b)
		}
		w.total += room
		if w.total > w.max {
			w.over = true
			return ErrTooLarge
		}
		s.data = append(s.data, b[:room]...)
	}
	return nil
}

// headerLines is the header of a section: everything before its first
// "--- " or "@@ " line.
func headerLines(sec []byte) []byte {
	var b bytes.Buffer
	for _, line := range bytes.SplitAfter(sec, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("--- ")) || bytes.HasPrefix(line, []byte("@@ ")) {
			break
		}
		b.Write(line)
	}
	return b.Bytes()
}

// firstBytes is the start of a section's content as a hint, raw: the patch
// is cleaned as a whole, once, afterwards.
func firstBytes(sec []byte) string {
	i := bytes.Index(sec, []byte("\n@@ "))
	if i < 0 {
		return ""
	}
	rest := sec[i+1:]
	if j := bytes.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[j+1:]
	} else {
		return ""
	}
	if len(rest) > 1 {
		rest = rest[1:] // the '+', '-' or ' ' of the line
	}
	if len(rest) > 16 {
		rest = rest[:16]
	}
	rest = bytes.ReplaceAll(append([]byte(nil), rest...), []byte("\n"), []byte("?"))
	return string(bytes.ReplaceAll(rest, []byte("\t"), []byte("?")))
}

// hiddenBinary stands for a binary section whose content is not shown.
func hiddenBinary(sec patchSection) []byte {
	var b bytes.Buffer
	b.Write(headerLines(sec.data))
	hint := ""
	if f := firstBytes(sec.data); f != "" {
		hint = fmt.Sprintf(", starts with \"%s\"", f)
	}
	fmt.Fprintf(&b, "Binary file by content (contains NUL bytes), %d bytes in the patch%s; content not shown, use --show-binary to inspect it\n", sec.size, hint)
	return b.Bytes()
}

// shownBinary is a binary section printed, up to maxBinaryShown bytes.
func shownBinary(sec patchSection) []byte {
	d := sec.data
	if len(d) > maxBinaryShown {
		d = d[:maxBinaryShown]
	}
	var b bytes.Buffer
	b.Write(d)
	if int64(len(d)) < sec.size {
		if !bytes.HasSuffix(d, []byte("\n")) {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "... truncated, %d bytes not shown\n", sec.size-int64(len(d)))
	}
	return b.Bytes()
}

// tooLarge stands for a section past the per-file cap.
func tooLarge(sec patchSection) []byte {
	var b bytes.Buffer
	b.Write(headerLines(sec.data))
	fmt.Fprintf(&b, "Too large: %d bytes in the patch, not shown (the limit is %d bytes per file); use --stat, or read the file in the clone\n", sec.size, maxFilePatch)
	return b.Bytes()
}
