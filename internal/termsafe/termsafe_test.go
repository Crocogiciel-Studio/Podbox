package termsafe

import "testing"

func TestClean(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "hello\tworld\n", "hello\tworld\n"},
		{"utf8 kept", "café 日本 \U0001F600\n", "café 日本 \U0001F600\n"},
		{"patch structure", "@@ -1 +1 @@\n-a\n+b\n \tc\n", "@@ -1 +1 @@\n-a\n+b\n \tc\n"},
		{"CSI colour", "\x1b[31mred\x1b[0m", "red"},
		{"CSI with intermediates", "a\x1b[?25;1$pb", "ab"},
		{"CSI cursor and erase", "x\x1b[2J\x1b[Hy", "xy"},
		{"OSC title BEL", "a\x1b]0;title\x07b", "ab"},
		{"OSC 52 clipboard ST", "a\x1b]52;c;aGVsbG8=\x1b\\b", "ab"},
		{"OSC 8 hyperlink", "\x1b]8;;http://x\x07link\x1b]8;;\x07", "link"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"APC PM SOS", "a\x1b_x\x07b\x1b^y\x07c\x1bXz\x07d", "abcd"},
		{"two-character escapes", "a\x1bcb\x1b7c\x1b=d", "abcd"},
		{"nF escape", "a\x1b(Bb", "ab"},
		{"BEL BS NUL VT FF DEL", "a\x07b\x08c\x00d\x0be\x0cf\x7fg", "abcdefg"},
		{"CR hides text", "safe\rEVIL\r\nok", "safeEVIL\nok"},
		{"C1 as UTF-8", "a\u009bb\u009d0;x\u0007c\u0085d", "ab0;xcd"},
		{"C1 raw bytes", "a\x9bb\x90c\x9dd", "a�b�c�d"},
		{"invalid UTF-8", "a\xffb\xc3", "a�b�"},
		{"bidi override", "a‮b⁦c‏d", "a<U+202E>b<U+2066>c<U+200F>d"},
		{"truncated CSI", "a\x1b[31", "a"},
		{"truncated OSC", "a\x1b]52;c;AAAA", "a"},
		{"lone ESC", "a\x1b", "a"},
		{"ESC then newline", "a\x1b\nb", "a\nb"},
		{"unterminated OSC stops at the line", "a\x1b]0;x\nsecond line\n", "a\nsecond line\n"},
		{"string ended by a new ESC", "\x1b]0;x\x1b[31mred", "red"},
		{"CSI broken by newline", "\x1b[3\n1m", "\n1m"},
		{"ESC inside CSI params", "\x1b[1\x1b[2Jz", "z"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := string(Clean([]byte(c.in))); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// No byte of a hostile input may survive as a control.
func TestCleanLeavesNoControl(t *testing.T) {
	in := "\x1b]52;c;x\x07\x1b[2J\x9b\x1bP1\x1b\\\r\x00\x7f\u0080\u009f‮⁧ok\n\t"
	for _, r := range string(Clean([]byte(in))) {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			t.Errorf("control %U survived", r)
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/b.txt", "a/b.txt"},
		{"x\x1b[31my", "xy"},
		{"x\ny\tz", "x?y?z"},
		{"x\x1b]0;t\x07y", "xy"},
		{"r‮txt.exe", "r<U+202E>txt.exe"},
		{"café", "café"},
	}
	for _, c := range cases {
		if got := CleanName(c.in); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanDoesNotModifyInput(t *testing.T) {
	in := []byte("a\x1b[31mb")
	Clean(in)
	if string(in) != "a\x1b[31mb" {
		t.Fatal("input modified")
	}
}
