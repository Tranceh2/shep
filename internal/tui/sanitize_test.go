package tui

import (
	"strings"
	"testing"
)

// Phase 5 — OSC/DCS containment for live pane capture. STRICT TDD: these
// tests are written before sanitizePaneCapture exists/behaves correctly and
// must fail RED against the pre-fix code.
//
// Fix round 1 (R1-001, R1-002): the regex-based approach was replaced with a
// single-pass, stateful scanner. The tests below were added RED-first against
// the still-regex-based implementation to prove both CRITICAL findings, then
// stayed as permanent regression coverage once the scanner replaced it.

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
// unaltered — the sanitizer must never touch ordinary printable content
// (Phase 4's blank-line-safe Sections handling must not regress).
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

// --- Fix round 1: R1-001 (closure-safety / nested-OSC reconstruction) ---

// TestSanitizePaneCapture_NestedOSCExploit_R1001 is the exact adversarially
// verified exploit from R1-001: an outer OSC 52 (clipboard write) opener,
// followed by an inner, fully-formed OSC (BEL-terminated) before the outer's
// own ST terminator. The old regex-based negated-character-class approach
// stopped short at the embedded ESC, matched only the well-formed inner OSC,
// removed it, and left a syntactically valid, reconstructed OSC 52 sequence
// behind (`\x1b]52;c;cGF5bG9hZA==\x1b\\`). The fix must never let ANY OSC
// opener survive — assert there is no `\x1b]` anywhere in the output, not
// just that the original inner match is gone.
func TestSanitizePaneCapture_NestedOSCExploit_R1001(t *testing.T) {
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

// --- Fix round 1: R1-002 (scope too narrow — non-SGR CSI, APC/PM/SOS) ---

// TestSanitizePaneCapture_NonSGRCSIStripped proves cursor/erase-family CSI
// sequences (clear screen, cursor home, scrollback erase) are stripped —
// previously only OSC/DCS were sanitized, letting these reach the real
// terminal via viewport.View() -> lipgloss.Render() -> Model.View() and
// potentially clear/reposition the surrounding shep UI.
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
// all stripped — R1-002 found these entirely unmatched by the old
// OSC/DCS-only regex pair.
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
// adversarial nested/overlapping case beyond the R1-001 exploit itself.
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
