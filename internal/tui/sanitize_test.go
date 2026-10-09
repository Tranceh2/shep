package tui

import (
	"strings"
	"testing"
)

// These tests cover OSC/DCS containment for live pane capture.
// sanitizePaneCapture is a single-pass, stateful scanner: it strips OSC, DCS,
// APC/PM/SOS and non-SGR CSI sequences and bare control characters, keeps SGR
// color byte-for-byte, and never lets a nested, overlapping or unterminated
// sequence leave an OSC or DCS opener behind.

// TestSanitizePaneCapture_OSCBELRemoved proves an OSC sequence terminated by
// BEL (\x07) is stripped, with surrounding text left intact.
func TestSanitizePaneCapture_OSCBELRemoved(t *testing.T) {
	t.Parallel()
	in := "before\x1b]0;evil title\x07after"
	want := "beforeafter"
	if got := sanitizePaneCapture(in); got != want {
		t.Errorf("sanitizePaneCapture(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitizePaneCapture_OSCSTRemoved proves an OSC sequence terminated by
// ST (ESC \) is stripped, with surrounding text left intact.
func TestSanitizePaneCapture_OSCSTRemoved(t *testing.T) {
	t.Parallel()
	in := "before\x1b]2;title\x1b\\after"
	want := "beforeafter"
	if got := sanitizePaneCapture(in); got != want {
		t.Errorf("sanitizePaneCapture(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitizePaneCapture_DCSRemoved proves a DCS sequence is stripped, with
// surrounding text left intact.
func TestSanitizePaneCapture_DCSRemoved(t *testing.T) {
	t.Parallel()
	in := "before\x1bPsome dcs payload\x1b\\after"
	want := "beforeafter"
	if got := sanitizePaneCapture(in); got != want {
		t.Errorf("sanitizePaneCapture(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitizePaneCapture_SGRPreservedByteForByte proves SGR (color) escape
// sequences survive untouched — this is the containment/coloring contract:
// strip OSC/DCS, never touch SGR.
func TestSanitizePaneCapture_SGRPreservedByteForByte(t *testing.T) {
	t.Parallel()
	in := "\x1b[31mred text\x1b[0m"
	if got := sanitizePaneCapture(in); got != in {
		t.Errorf("sanitizePaneCapture(%q) = %q, want unchanged (SGR must be preserved)", in, got)
	}
}

// TestSanitizePaneCapture_MixedOSCAndSGR proves a buffer containing both an
// unsafe OSC sequence and a legitimate SGR color sequence has ONLY the OSC
// removed; the SGR survives byte-for-byte.
func TestSanitizePaneCapture_MixedOSCAndSGR(t *testing.T) {
	t.Parallel()
	in := "\x1b]0;evil title\x07\x1b[31mred\x1b[0m"
	want := "\x1b[31mred\x1b[0m"
	if got := sanitizePaneCapture(in); got != want {
		t.Errorf("sanitizePaneCapture(%q) = %q, want %q", in, got, want)
	}
}

// TestSanitizePaneCapture_PrintableTextUnchanged proves box-drawing
// characters, wide/multi-byte runes, and blank lines pass through completely
// unaltered — the sanitizer must never touch ordinary printable content,
// blank lines included.
func TestSanitizePaneCapture_PrintableTextUnchanged(t *testing.T) {
	t.Parallel()
	in := "┌────────┐\n│ pane 1 │\n└────────┘\n\n日本語 emoji 🎉\n\nline after blank"
	if got := sanitizePaneCapture(in); got != in {
		t.Errorf("sanitizePaneCapture(%q) = %q, want unchanged", in, got)
	}
}

// TestSanitizePaneCapture_NoEscapeSequences proves a plain buffer with no
// escape sequences at all is returned unchanged.
func TestSanitizePaneCapture_NoEscapeSequences(t *testing.T) {
	t.Parallel()
	in := "plain text\nwith multiple\nlines"
	if got := sanitizePaneCapture(in); got != in {
		t.Errorf("sanitizePaneCapture(%q) = %q, want unchanged", in, got)
	}
}

// --- Closure safety: a nested OSC must not reconstruct an OSC ---

// TestSanitizePaneCapture_NestedOSCExploit proves an adversarially verified
// exploit is contained: an outer OSC 52 (clipboard write) opener, followed
// by an inner, fully-formed OSC (BEL-terminated) before the outer's own ST
// terminator. A regex-based negated-character-class matcher would stop
// short at the embedded ESC, match only the well-formed inner OSC, remove
// it, and leave a syntactically valid, reconstructed OSC 52 sequence behind
// (`\x1b]52;c;cGF5bG9hZA==\x1b\\`). The sanitizer must never let ANY OSC
// opener survive — assert there is no `\x1b]` anywhere in the output, not
// just that the inner match is gone.
func TestSanitizePaneCapture_NestedOSCExploit(t *testing.T) {
	t.Parallel()
	in := "\x1b]52;c;cGF5bG9hZA==\x1b]0;x\x07\x1b\\"
	got := sanitizePaneCapture(in)
	if strings.Contains(got, "\x1b]") {
		t.Errorf("sanitizePaneCapture(%q) = %q, must not contain any surviving OSC opener (\\x1b]))", in, got)
	}
	if strings.Contains(got, "cGF5bG9hZA==") {
		t.Errorf("sanitizePaneCapture(%q) = %q, the clipboard payload must not survive in any form", in, got)
	}
}

// TestSanitizePaneCapture_UnterminatedOSCFailsClosed proves an OSC opener
// with no BEL/ST terminator before end-of-input never leaks its raw
// "ESC ] ..." bytes into the output (fail closed on truncation/malformed
// input — e.g. a capture cut off at the 200-line boundary).
func TestSanitizePaneCapture_UnterminatedOSCFailsClosed(t *testing.T) {
	t.Parallel()
	in := "before\x1b]0;never terminated"
	got := sanitizePaneCapture(in)
	if strings.Contains(got, "\x1b]") {
		t.Errorf("sanitizePaneCapture(%q) = %q, unterminated OSC opener must not leak: %q", in, got, got)
	}
	if !strings.Contains(got, "before") {
		t.Errorf("sanitizePaneCapture(%q) = %q, text before the unterminated OSC should survive", in, got)
	}
}

// --- Scope: non-SGR CSI and APC/PM/SOS are stripped too ---

// TestSanitizePaneCapture_NonSGRCSIStripped proves cursor/erase-family CSI
// sequences (clear screen, cursor home, scrollback erase) are stripped, so
// none of them reaches the real terminal via viewport.View() ->
// lipgloss.Render() -> Model.View() to clear or reposition the surrounding
// shep UI.
func TestSanitizePaneCapture_NonSGRCSIStripped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"clear screen", "before\x1b[2Jafter", "beforeafter"},
		{"cursor home", "before\x1b[Hafter", "beforeafter"},
		{"clear scrollback", "before\x1b[3Jafter", "beforeafter"},
		{"combined clear+home", "before\x1b[2J\x1b[Hafter", "beforeafter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePaneCapture(tt.in); got != tt.want {
				t.Errorf("sanitizePaneCapture(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizePaneCapture_SGRMultiParamPreserved proves an SGR sequence with
// multiple semicolon-separated parameters (e.g. bold+red) survives
// byte-for-byte, not just the single-parameter case already covered.
func TestSanitizePaneCapture_SGRMultiParamPreserved(t *testing.T) {
	t.Parallel()
	in := "\x1b[1;31mbold red\x1b[0m"
	if got := sanitizePaneCapture(in); got != in {
		t.Errorf("sanitizePaneCapture(%q) = %q, want unchanged (multi-param SGR must be preserved)", in, got)
	}
}

// TestSanitizePaneCapture_APCPMSOSStripped proves APC, PM, and SOS
// string-type sequences (same shape as DCS/OSC: ESC introducer ... ST) are
// all stripped, not only OSC and DCS.
func TestSanitizePaneCapture_APCPMSOSStripped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"DCS", "before\x1bPsome dcs payload\x1b\\after", "beforeafter"},
		{"APC", "before\x1b_some apc payload\x1b\\after", "beforeafter"},
		{"PM", "before\x1b^some pm payload\x1b\\after", "beforeafter"},
		{"SOS", "before\x1bXsome sos payload\x1b\\after", "beforeafter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePaneCapture(tt.in); got != tt.want {
				t.Errorf("sanitizePaneCapture(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizePaneCapture_MixedRealisticBuffer proves a realistic capture —
// printable text, box-drawing characters, blank lines, SGR color, and an
// embedded hostile OSC — has ONLY the OSC removed: text, box-drawing, blank
// lines, and SGR all survive exactly, and no reconstructed OSC fragment
// appears anywhere in the output.
func TestSanitizePaneCapture_MixedRealisticBuffer(t *testing.T) {
	t.Parallel()
	in := "┌────────┐\n│ pane 1 │\n└────────┘\n\n\x1b[31mred error line\x1b[0m\n\n\x1b]0;evil title\x07\nline after"
	got := sanitizePaneCapture(in)
	want := "┌────────┐\n│ pane 1 │\n└────────┘\n\n\x1b[31mred error line\x1b[0m\n\n\nline after"
	if got != want {
		t.Errorf("sanitizePaneCapture(%q) = %q, want %q", in, got, want)
	}
	if strings.Contains(got, "\x1b]") {
		t.Errorf("sanitizePaneCapture(%q) = %q, must not contain any surviving OSC opener", in, got)
	}
}

// TestSanitizePaneCapture_AdversarialOverlappingOpeners proves an OSC opener
// immediately followed by a DCS opener, before either finds its own
// terminator, does not produce any surviving dangerous sequence — a second
// adversarial nested/overlapping case beyond
// TestSanitizePaneCapture_NestedOSCExploit.
func TestSanitizePaneCapture_AdversarialOverlappingOpeners(t *testing.T) {
	t.Parallel()
	in := "\x1b]52;c;\x1bPdcs-inside-osc\x1b\\\x07"
	got := sanitizePaneCapture(in)
	if strings.Contains(got, "\x1b]") {
		t.Errorf("sanitizePaneCapture(%q) = %q, must not contain any surviving OSC opener", in, got)
	}
	if strings.Contains(got, "\x1bP") {
		t.Errorf("sanitizePaneCapture(%q) = %q, must not contain any surviving DCS opener", in, got)
	}
}

// --- Bare control characters (CRLF captures must not blank list rows) ---

// TestSanitizePaneCapture_ControlCharacters proves every bare C0 control,
// DEL and C1 control is contained like the cursor-moving CSI sequences
// above: "\r\n" becomes "\n", a bare "\r" and every other control is
// dropped, and an invalid UTF-8 byte becomes U+FFFD. `herdr pane read`
// returns CRLF lines; a line's trailing "\r" would send the terminal's
// cursor back to column 0, where the preview's padding would blank the list
// column.
func TestSanitizePaneCapture_ControlCharacters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"CRLF becomes LF", "PASS foo\r\nFAIL bar\r\n", "PASS foo\nFAIL bar\n"},
		{"CRLF before an SGR reset", "\x1b[32mPASS\x1b[0m foo\r\n\x1b[m", "\x1b[32mPASS\x1b[0m foo\n\x1b[m"},
		{"bare CR dropped", "progress 10%\rprogress 99%", "progress 10%progress 99%"},
		{"trailing CR dropped", "unconfigured\r", "unconfigured"},
		{"backspace dropped", "typo\b\bfixed", "typofixed"},
		{"NUL, BEL, VT, FF and other C0 dropped", "a\x00b\x07c\x0bd\x0ce\x01f\x1fg", "abcdefg"},
		{"DEL dropped", "a\x7fb", "ab"},
		{"C1 CSI dropped", "before\u009b2Jafter", "before2Jafter"},
		{"C1 OSC dropped", "a\u009d0;title\x07b", "a0;titleb"},
		{"every C1 code point dropped", "a\u0080\u0085\u008d\u009fb", "ab"},
		{"invalid byte becomes U+FFFD", "bad \xff byte", "bad � byte"},
		{"raw 8-bit CSI byte becomes U+FFFD", "\x9b2J", "�2J"},
		{"truncated rune becomes U+FFFD per byte", "box \xe2\x94", "box ��"},
		{"valid U+FFFD and U+00A0 kept", "� ok", "� ok"},
		{"controls inside an OSC go with it", "a\x1b]0;x\ry\bz\x07b", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizePaneCapture(tt.in); got != tt.want {
				t.Errorf("sanitizePaneCapture(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizePaneCapture_TabExpansion proves a tab becomes the spaces up
// to the next multiple of 8 of the line's visible column: SGR and dropped
// sequences add no cells, a wide rune adds two, and a new line starts at
// column 0.
func TestSanitizePaneCapture_TabExpansion(t *testing.T) {
	t.Parallel()
	sp := strings.Repeat(" ", 8)
	tests := []struct {
		name, in, want string
	}{
		{"at line start", "\tx", sp + "x"},
		{"mid stop", "ab\tc", "ab" + sp[:6] + "c"},
		{"at a stop", "abcdefgh\ti", "abcdefgh" + sp + "i"},
		{"two tabs", "a\tb\tc", "a" + sp[:7] + "b" + sp[:7] + "c"},
		{"consecutive tabs", "a\t\tb", "a" + sp[:7] + sp + "b"},
		{"column resets per line", "abc\n\tx\nabcde\ty", "abc\n" + sp + "x\nabcde" + sp[:3] + "y"},
		{"after CRLF", "abc\r\n\tx", "abc\n" + sp + "x"},
		{"after a kept SGR", "\x1b[31mab\x1b[0m\tc", "\x1b[31mab\x1b[0m" + sp[:6] + "c"},
		{"after a dropped CSI", "ab\x1b[2J\tc", "ab" + sp[:6] + "c"},
		{"after a dropped OSC", "ab\x1b]0;long title\x07\tc", "ab" + sp[:6] + "c"},
		{"after a dropped control", "ab\r\b\tc", "ab" + sp[:6] + "c"},
		{"after a wide rune", "日\tx", "日" + sp[:6] + "x"},
		{"after wide runes filling a stop", "日本語中\tx", "日本語中" + sp + "x"},
		{"after a box-drawing rune", "│\tx", "│" + sp[:7] + "x"},
		{"after an invalid byte", "\xff\tx", "�" + sp[:7] + "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizePaneCapture(tt.in); got != tt.want {
				t.Errorf("sanitizePaneCapture(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizePaneCapture_CleanInputNotCopied proves input that needs no
// change is returned as is: the common capture costs no copy.
func TestSanitizePaneCapture_CleanInputNotCopied(t *testing.T) {
	in := "┌────────┐\n│ pane 1 │\n└────────┘\n\n日本語 emoji 🎉"
	if allocs := testing.AllocsPerRun(100, func() { _ = sanitizePaneCapture(in) }); allocs != 0 {
		t.Errorf("sanitizePaneCapture(clean) allocates %.0f times, want 0", allocs)
	}
}

// --- plainText: labels, titles and Meta values shown on one row ---

// TestPlainText proves plainText removes every escape sequence (SGR too) and
// control character from a short display string: "\r", "\n" and "\t"
// separate words, a whitespace run holding one becomes a single space (none
// at either end), runs of plain spaces stay, other C0, DEL and C1 controls
// are dropped and invalid UTF-8 becomes U+FFFD.
func TestPlainText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, in, want string
	}{
		{"plain ASCII unchanged", "api-gateway", "api-gateway"},
		{"wide and multi-byte runes unchanged", "日本語 café 🎉 │", "日本語 café 🎉 │"},
		{"plain double spaces kept", "two  spaces", "two  spaces"},
		{"empty", "", ""},
		{"CR becomes a space", "Fix\rparser", "Fix parser"},
		{"LF becomes a space", "Fix\nparser", "Fix parser"},
		{"tab becomes a space", "PR\t42", "PR 42"},
		{"CRLF becomes one space", "a\r\nb", "a b"},
		{"separator run with spaces collapses", "a \t \r\n  b", "a b"},
		{"separators at the ends trimmed", "\t\r\nfoo bar\r\n", "foo bar"},
		{"spaces before a trailing separator trimmed", "foo  \n", "foo"},
		{"only separators", "\r\n\t", ""},
		{"SGR removed", "\x1b[31mred\x1b[0m alert", "red alert"},
		{"cursor CSI removed", "x\x1b[2J\x1b[Hy", "xy"},
		{"OSC removed", "ti\x1b]0;evil\x07tle", "title"},
		{"OSC 52 removed", "a\x1b]52;c;cGF5bG9hZA==\x1b\\b", "ab"},
		{"unterminated OSC fails closed", "ok\x1b]52;c;cGF5", "ok"},
		{"unrecognized escape fails closed", "a\x1b7b", "a"},
		{"lone ESC at the end dropped", "a\x1b", "a"},
		{"other C0 dropped without a space", "a\x00b\x07c\bd", "abcd"},
		{"DEL dropped", "a\x7fb", "ab"},
		{"C1 dropped", "a\u009b2J\u0085b", "a2Jb"},
		{"invalid UTF-8 becomes U+FFFD", "bad\xffbyte", "bad�byte"},
		{"BEL and tab in a custom label", "PR\t42\x07 fix", "PR 42 fix"},
		{"CR and erase in a terminal title", "Fix\r\x1b[2Jparser", "Fix parser"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := plainText(tt.in)
			if got != tt.want {
				t.Errorf("plainText(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !isPlainText(got) {
				t.Errorf("plainText(%q) = %q, still not plain", tt.in, got)
			}
		})
	}
}

// TestPlainText_CleanInputNotCopied proves a string that is already plain —
// nearly every label — is returned as is, so a row view build pays a scan,
// not an allocation, for it.
func TestPlainText_CleanInputNotCopied(t *testing.T) {
	in := "~/allsafe/ECORP/whiterose-db 日本語"
	if allocs := testing.AllocsPerRun(100, func() { _ = plainText(in) }); allocs != 0 {
		t.Errorf("plainText(clean) allocates %.0f times, want 0", allocs)
	}
}
