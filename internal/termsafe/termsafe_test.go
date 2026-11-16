package termsafe

import (
	"fmt"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "hello\tworld\n", "hello\tworld\n"},
		{"utf8 kept", "café 日本 \U0001F600\n", "café 日本 \U0001F600\n"},
		{"patch structure", "@@ -1 +1 @@\n-a\n+b\n \tc\n", "@@ -1 +1 @@\n-a\n+b\n \tc\n"},
		{"CSI colour", "\x1b[31mred\x1b[0m", "<CSI>31mred<CSI>0m"},
		{"CSI with intermediates", "a\x1b[?25;1$pb", "a<CSI>?25;1$pb"},
		{"CSI cursor and erase", "x\x1b[2J\x1b[Hy", "x<CSI>2J<CSI>Hy"},
		{"OSC title keeps payload", "a\x1b]0;title\x07b", "a<OSC>0;title<U+0007>b"},
		{"OSC 52 payload visible", "a\x1b]52;c;aGVsbG8=\x1b\\b", "a<OSC>52;c;aGVsbG8=<ST>b"},
		{"OSC 8 hyperlink", "\x1b]8;;http://x\x07link\x1b]8;;\x07", "<OSC>8;;http://x<U+0007>link<OSC>8;;<U+0007>"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "a<DCS>q#0;2;0;0;0<ST>b"},
		{"APC PM SOS", "a\x1b_x\x07b\x1b^y\x07c\x1bXz\x07d", "a<APC>x<U+0007>b<PM>y<U+0007>c<SOS>z<U+0007>d"},
		{"two-character escapes", "a\x1bcb\x1b7c\x1b=d", "a<ESC>cb<ESC>7c<ESC>=d"},
		{"BEL BS NUL VT FF DEL", "a\x07b\x08c\x00d\x0be\x0cf\x7fg", "a<U+0007>b<U+0008>c<U+0000>d<U+000B>e<U+000C>f<U+007F>g"},
		{"lone CR hides nothing", "safe\rEVIL\r\nok", "safe<U+000D>EVIL\nok"},
		{"CRLF is a newline", "a\r\nb\r\n", "a\nb\n"},
		{"C1 as UTF-8 is visible", "a\u009bb\u009d0;x\u0007c\u0085d", "a<U+009B>b<U+009D>0;x<U+0007>c<U+0085>d"},
		{"C1 raw bytes", "a\x9bb\x90c\x9dd", "a�b�c�d"},
		{"invalid UTF-8", "a\xffb\xc3", "a�b�"},
		{"bidi override", "a‮b⁦c‏d", "a<U+202E>b<U+2066>c<U+200F>d"},
		{"zero width", "a​b‌c‍d⁠e\ufeff", "a<U+200B>b<U+200C>c<U+200D>d<U+2060>e<U+FEFF>"},
		{"tags hide text", "ok\U000e0041\U000e0042", "ok<U+E0041><U+E0042>"},
		{"separators and annotations", "a b c￹d￻e", "a<U+2028>b<U+2029>c<U+FFF9>d<U+FFFB>e"},
		{"joiner and vowel separator", "a͏b᠎c", "a<U+034F>b<U+180E>c"},
		{"soft hyphen and invisible operators", "a­b⁪c⁣d", "a<U+00AD>b<U+206A>c<U+2063>d"},
		{"variation selectors", "a️b\U000e0100c", "a<U+FE0F>b<U+E0100>c"},
		{"hangul fillers, braille blank, object replacement", "aᅟbᅠcㅤdﾠe⠀f￼g", "a<U+115F>b<U+1160>c<U+3164>d<U+FFA0>e<U+2800>f<U+FFFC>g"},
		{"khmer vowels", "a឴b឵c", "a<U+17B4>b<U+17B5>c"},
		{"private use", "ab\U000f0000c", "a<U+E000>b<U+F0000>c"},
		{"noncharacters", "a﷐b￿c\U0001fffed", "a<U+FDD0>b<U+FFFF>c<U+1FFFE>d"},
		{"hidden payload stays visible", "run \x1b]0;rm -rf ~\x07 now", "run <OSC>0;rm -rf ~<U+0007> now"},
		{"truncated CSI", "a\x1b[31", "a<CSI>31"},
		{"truncated OSC keeps the rest of the line", "a\x1b]52;c;AAAA\nnext\n", "a<OSC>52;c;AAAA\nnext\n"},
		{"lone ESC", "a\x1b", "a<ESC>"},
		{"ESC then newline", "a\x1b\nb", "a<ESC>\nb"},
		{"ESC inside CSI params", "\x1b[1\x1b[2Jz", "<CSI>1<CSI>2Jz"},
		{"a literal marker is escaped", "see <CSI> and <OSC>x <U+200B> <ESC> <ST>", "see <U+003C>CSI> and <U+003C>OSC>x <U+003C>U+200B> <U+003C>ESC> <U+003C>ST>"},
		{"other angle brackets are kept", "a < b <U+12> <div> <U+ZZZZ>", "a < b <U+12> <div> <U+ZZZZ>"},
		{"a real and a forged marker differ", "\x1b[ <CSI>", "<CSI> <U+003C>CSI>"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := string(Clean([]byte(c.in))); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Nothing is dropped silently: each C0 control, DEL and C1 control leaves a
// marker, ESC leaves its own, and only the documented bytes pass through.
func TestCleanDropsNothingSilently(t *testing.T) {
	for r := rune(0); r < 0x80; r++ {
		got := string(Clean([]byte{'a', byte(r), 'b'}))
		switch {
		case r == '\n' || r == '\t':
			if got != "a"+string(r)+"b" {
				t.Errorf("%U: %q", r, got)
			}
		case r == 0x1b:
			if got != "a<ESC>b" {
				t.Errorf("ESC: %q", got)
			}
		case r < 0x20 || r == 0x7f:
			if want := fmt.Sprintf("a<U+%04X>b", r); got != want {
				t.Errorf("%U: got %q, want %q", r, got, want)
			}
		default:
			if got != "a"+string(r)+"b" && r != '<' {
				t.Errorf("%U: %q", r, got)
			}
		}
		for _, c := range []byte(got) {
			if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
				t.Errorf("%U: control byte %#x in %q", r, c, got)
			}
		}
	}
	// the C1 range as UTF-8 and as raw bytes
	for r := rune(0x80); r < 0xa0; r++ {
		if got := string(Clean([]byte("a" + string(r) + "b"))); got != fmt.Sprintf("a<U+%04X>b", r) {
			t.Errorf("C1 %U: %q", r, got)
		}
		if got := string(Clean([]byte{'a', byte(r), 'b'})); got != "a�b" {
			t.Errorf("raw C1 %#x: %q", r, got)
		}
	}
	// every escape form leaves a marker
	for _, f := range []string{"\x1b[", "\x1b]", "\x1bP", "\x1b_", "\x1b^", "\x1bX", "\x1b\\", "\x1bc", "\x1b("} {
		if got := string(Clean([]byte("a" + f + "b"))); !strings.Contains(got, "<") || strings.ContainsRune(got, 0x1b) {
			t.Errorf("%q: %q", f, got)
		}
	}
}

// No raw control of any kind survives in the output.
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
		{"x\x1b]0;t\x07y", "x<OSC>0;t<U+0007>y"},
		{"r‮txt.exe", "r<U+202E>txt.exe"},
		{"a​b", "a<U+200B>b"},
		{"café", "café"},
		{"<CSI>", "<U+003C>CSI>"},
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
