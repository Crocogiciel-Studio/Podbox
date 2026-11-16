// Package termsafe makes text written by an untrusted party inert before it
// reaches a terminal: escape sequences and control characters are removed.
package termsafe

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

// Clean removes from b every escape sequence (CSI, OSC, DCS, SOS, PM, APC and
// two-character escapes), every C0 control except newline and tab, DEL and
// the C1 range U+0080 to U+009F. Bidirectional controls, which reorder the
// text a reviewer reads, become a visible <U+XXXX>; a byte that is not valid
// UTF-8 becomes U+FFFD. A sequence never reaches past the end of its line, so
// a missing terminator cannot hide the text that follows.
func Clean(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			i = skipEscape(b, i)
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
			case r >= 0x80 && r <= 0x9f:
			case isBidi(r):
				out = fmt.Appendf(out, "<U+%04X>", r)
			default:
				out = utf8.AppendRune(out, r)
			}
		}
	}
	return out
}

// CleanName cleans a file name; newline and tab, which Clean keeps for text,
// become '?' so a name stays on one line.
func CleanName(s string) string {
	b := Clean([]byte(s))
	b = bytes.ReplaceAll(b, []byte{'\n'}, []byte{'?'})
	return string(bytes.ReplaceAll(b, []byte{'\t'}, []byte{'?'}))
}

func isBidi(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069
}

// skipEscape returns the index after the escape sequence starting at b[i].
func skipEscape(b []byte, i int) int {
	j := i + 1
	if j >= len(b) {
		return len(b)
	}
	switch b[j] {
	case '[': // CSI: parameters, intermediates, one final byte
		j++
		for j < len(b) && b[j] >= 0x30 && b[j] <= 0x3f {
			j++
		}
		for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f {
			j++
		}
		if j < len(b) && b[j] >= 0x40 && b[j] <= 0x7e {
			j++
		}
		return j
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: up to BEL or ST
		for j++; j < len(b); j++ {
			switch {
			case b[j] == 0x07:
				return j + 1
			case b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\':
				return j + 2
			case b[j] == 0x1b || b[j] == '\n':
				return j // a new sequence, or the end of the line
			}
		}
		return len(b)
	}
	for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f { // nF escape: intermediates, then a final
		j++
	}
	if j < len(b) && b[j] >= 0x30 && b[j] <= 0x7e {
		j++
	}
	return j
}
