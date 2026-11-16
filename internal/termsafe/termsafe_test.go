package termsafe

import "testing"

func TestClean(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "hello\tworld\n", "hello\tworld\n"},
		{"utf8 kept", "caf\u00e9 \u65e5\u672c \U0001F600\n", "caf\u00e9 \u65e5\u672c \U0001F600\n"},
		{"patch structure", "@@ -1 +1 @@\n-a\n+b\n \tc\n", "@@ -1 +1 @@\n-a\n+b\n \tc\n"},
		{"CSI colour", "\x1b[31mred\x1b[0m", "<CSI>31mred<CSI>0m"},
		{"CSI with intermediates", "a\x1b[?25;1$pb", "a<CSI>?25;1$pb"},
		{"CSI cursor and erase", "x\x1b[2J\x1b[Hy", "x<CSI>2J<CSI>Hy"},
		{"OSC title keeps payload", "a\x1b]0;title\x07b", "a<OSC>0;titleb"},
		{"OSC 52 payload visible", "a\x1b]52;c;aGVsbG8=\x1b\\b", "a<OSC>52;c;aGVsbG8=<ST>b"},
		{"OSC 8 hyperlink", "\x1b]8;;http://x\x07link\x1b]8;;\x07", "<OSC>8;;http://xlink<OSC>8;;"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "a<DCS>q#0;2;0;0;0<ST>b"},
		{"APC PM SOS", "a\x1b_x\x07b\x1b^y\x07c\x1bXz\x07d", "a<APC>xb<PM>yc<SOS>zd"},
		{"two-character escapes", "a\x1bcb\x1b7c\x1b=d", "a<ESC>cb<ESC>7c<ESC>=d"},
		{"BEL BS NUL VT FF DEL", "a\x07b\x08c\x00d\x0be\x0cf\x7fg", "abcdefg"},
		{"CR keeps both texts", "safe\rEVIL\r\nok", "safeEVIL\nok"},
		{"C1 as UTF-8 is visible", "a\u009bb\u009d0;x\u0007c\u0085d", "a<U+009B>b<U+009D>0;xc<U+0085>d"},
		{"C1 raw bytes", "a\x9bb\x90c\x9dd", "a\ufffdb\ufffdc\ufffdd"},
		{"invalid UTF-8", "a\xffb\xc3", "a\ufffdb\ufffd"},
		{"bidi override", "a\u202eb\u2066c\u200fd", "a<U+202E>b<U+2066>c<U+200F>d"},
		{"zero width", "a\u200bb\u200cc\u200dd\u2060e\ufeff", "a<U+200B>b<U+200C>c<U+200D>d<U+2060>e<U+FEFF>"},
		{"tags hide text", "ok\U000e0041\U000e0042", "ok<U+E0041><U+E0042>"},
		{"separators and annotations", "a\u2028b\u2029c\ufff9d\ufffbe", "a<U+2028>b<U+2029>c<U+FFF9>d<U+FFFB>e"},
		{"joiner and vowel separator", "a\u034fb\u180ec", "a<U+034F>b<U+180E>c"},
		{"hidden payload stays visible", "run \x1b]0;rm -rf ~\x07 now", "run <OSC>0;rm -rf ~ now"},
		{"truncated CSI", "a\x1b[31", "a<CSI>31"},
		{"truncated OSC keeps the rest of the line", "a\x1b]52;c;AAAA\nnext\n", "a<OSC>52;c;AAAA\nnext\n"},
		{"lone ESC", "a\x1b", "a<ESC>"},
		{"ESC then newline", "a\x1b\nb", "a<ESC>\nb"},
		{"ESC inside CSI params", "\x1b[1\x1b[2Jz", "<CSI>1<CSI>2Jz"},
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
		if r < 0x20 || r == 0x7f || isInvisible(r) {
			t.Errorf("control %U survived", r)
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a/b.txt", "a/b.txt"},
		{"x\x1b[31my", "x<CSI>31my"},
		{"x\ny\tz", "x?y?z"},
		{"x\x1b]0;t\x07y", "x<OSC>0;ty"},
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
