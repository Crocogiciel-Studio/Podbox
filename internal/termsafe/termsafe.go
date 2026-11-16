// Package termsafe makes text written by an untrusted party inert before it
// reaches a terminal, without hiding any of it from a reviewer: what is
// removed or made harmless leaves a visible marker, and the payload of an
// escape sequence stays on screen as plain text.
package termsafe

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

// Clean makes b safe to print. Each ESC becomes a marker naming what it
// introduces (<CSI>, <OSC>, <DCS>, <APC>, <PM>, <SOS>, <ST>, else <ESC>) and
// the bytes after it stay as ordinary text, so the payload of a clipboard
// write or a title is readable. C0 controls other than newline and tab, DEL
// and an invalid UTF-8 byte are removed (the latter shown as U+FFFD). C1
// controls and invisible code points (bidirectional controls, zero-width
// characters, Unicode tags, line and paragraph separators, ...) become a
// visible <U+XXXX>. Newline and tab are kept.
func Clean(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			out = append(out, escMarker(b, i)...)
			i++
			if i < len(b) && marked(b[i]) { // the introducer is part of the marker
				i++
			}
		case c == '\n' || c == '\t':
			out = append(out, c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < 0x80:
			out = append(out, c)
			i++
		default:
			r, n := utf8.DecodeRune(b[i:])
			i += n
			switch {
			case r == utf8.RuneError && n == 1:
				out = utf8.AppendRune(out, utf8.RuneError)
			case isInvisible(r):
				out = fmt.Appendf(out, "<U+%04X>", r)
			default:
				out = utf8.AppendRune(out, r)
			}
		}
	}
	return out
}

// CleanName cleans a file name; newline and tab, which Clean keeps for text,
// become '?' so a name stays on one line. The result is for display only:
// cleaning is lossy, so never look a file up by it.
func CleanName(s string) string {
	b := Clean([]byte(s))
	b = bytes.ReplaceAll(b, []byte{'\n'}, []byte{'?'})
	return string(bytes.ReplaceAll(b, []byte{'\t'}, []byte{'?'}))
}

var introducers = map[byte]string{'[': "<CSI>", ']': "<OSC>", 'P': "<DCS>", '_': "<APC>", '^': "<PM>", 'X': "<SOS>", '\\': "<ST>"}

func marked(c byte) bool { _, ok := introducers[c]; return ok }

// escMarker names the sequence the ESC at b[i] starts.
func escMarker(b []byte, i int) string {
	if i+1 < len(b) {
		if m, ok := introducers[b[i+1]]; ok {
			return m
		}
	}
	return "<ESC>"
}

// isInvisible reports a code point that shows nothing, or reorders text, and
// so could hide code from a reviewer.
func isInvisible(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f: // C1 controls
	case r == 0x034f, r == 0x061c, r == 0x180e: // combining grapheme joiner, arabic letter mark, mongolian vowel separator
	case r >= 0x200b && r <= 0x200f: // zero-width space, joiners, direction marks
	case r == 0x2028, r == 0x2029: // line and paragraph separators
	case r >= 0x202a && r <= 0x202e: // bidi embeddings and overrides
	case r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069: // word joiner, invisible operators, bidi isolates
	case r == 0xfeff, r >= 0xfff9 && r <= 0xfffb: // zero-width no-break space, interlinear annotation
	case r >= 0xe0000 && r <= 0xe007f: // Unicode tags
	default:
		return false
	}
	return true
}
