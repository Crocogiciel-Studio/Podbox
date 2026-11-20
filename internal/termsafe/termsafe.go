// Package termsafe makes text written by an untrusted party inert before it
// reaches a terminal, without hiding any of it from a reviewer: what is
// removed or made harmless leaves a visible marker, and the payload of an
// escape sequence stays on screen as plain text.
package termsafe

import (
	"bytes"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Clean makes b safe to print, and shows everything it changes. Each ESC
// becomes a marker naming what it introduces (<CSI>, <OSC>, <DCS>, <APC>,
// <PM>, <SOS>, <ST>, else <ESC>) and the bytes after it stay as ordinary text,
// so the payload of a clipboard write or a title is readable. Every other C0
// control (NUL, BEL, BS, CR, ...), DEL, every C1 control and every invisible
// code point (see isInvisible) becomes <U+XXXX>. An invalid UTF-8 byte becomes
// U+FFFD. Newline and tab are kept, and so is the CR of a CRLF line ending,
// which is shown as a plain newline. A literal "<" that would read as one of
// these markers becomes <U+003C>, so a marker in the output is never forged.
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
		case c == '\r' && i+1 < len(b) && b[i+1] == '\n':
			i++ // a CRLF ending reads as a newline
		case c == '\n' || c == '\t':
			out = append(out, c)
			i++
		case c < 0x20 || c == 0x7f:
			out = fmt.Appendf(out, "<U+%04X>", c)
			i++
		case c == '<' && looksLikeMarker(b[i:]):
			out = append(out, "<U+003C>"...)
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

// looksLikeMarker reports text that starts like a marker Clean writes.
func looksLikeMarker(b []byte) bool {
	for _, m := range introducers {
		if bytes.HasPrefix(b, []byte(m)) {
			return true
		}
	}
	if bytes.HasPrefix(b, []byte("<ESC>")) {
		return true
	}
	if !bytes.HasPrefix(b, []byte("<U+")) {
		return false
	}
	n := 0
	for _, c := range b[3:] {
		switch {
		case c >= '0' && c <= '9' || c >= 'A' && c <= 'F':
			n++
		case c == '>':
			return n >= 4 && n <= 6
		default:
			return false
		}
	}
	return false
}

// isInvisible reports a code point that shows nothing, reorders text, or
// looks like another character, so it could hide code from a reviewer: the
// format characters (Unicode category Cf: zero-width and joiner characters,
// bidirectional controls, tags, soft hyphen, ...), the C1 controls, private
// use and noncharacters, every space separator but the plain space (a
// no-break space looks like one), and a list of blanks and selectors.
func isInvisible(r rune) bool {
	switch {
	case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Cc, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Zs, r) && r != ' ':
		return true
	case r >= 0xfdd0 && r <= 0xfdef, r&0xfffe == 0xfffe: // noncharacters
		return true
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef: // variation selectors
		return true
	}
	switch r {
	case 0x034f, // combining grapheme joiner
		0x2028, 0x2029, // line and paragraph separators
		0x115f, 0x1160, 0x3164, 0xffa0, // Hangul fillers
		0x2800,         // braille blank
		0xfffc,         // object replacement
		0x17b4, 0x17b5: // Khmer inherent vowels
		return true
	}
	return false
}
