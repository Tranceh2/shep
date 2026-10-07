package tui

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// esc and bel are the two control bytes the escape-sequence grammar below
// reasons about explicitly; every other byte of a sequence is either plain
// payload or part of that well-defined grammar, handled inline.
const (
	esc = 0x1b
	bel = 0x07
)

// sanitizePaneCapture is the single containment boundary for raw
// captured/live terminal text (a Herdr pane buffer, whether delivered via
// panePreviewMsg for a RowPane or via the renderer's active_pane Section for
// a Herdr workspace) before it is composed into the preview body and handed
// to the viewport.
//
// Fix round 1 (R1-001, R1-002): this is a single-pass, byte-oriented,
// stateful scanner/classifier — NOT a regex-based find-and-replace. It walks
// the input exactly once, and for each ESC-introduced sequence it finds,
// decides once whether to KEEP it verbatim or DROP it entirely, based on the
// sequence's own terminator, never on a textual pattern that could stop
// short and leave a reconstructed fragment behind (R1-001's exploit: a
// regex's negated character class excluded ESC from an OSC's "allowed
// interior" class, so it could match only an embedded, well-formed inner
// OSC and leave the outer opener + trailing ST intact — a syntactically
// valid, reconstructed OSC 52 clipboard-write sequence). Scope (R1-002): CSI
// (kept only for SGR, dropped for every other final byte — cursor
// movement, erase, scroll, mode-set, etc.), OSC, DCS, APC, PM, and SOS are
// all covered, not just OSC/DCS. Any ESC-introduced sequence this scanner
// cannot fully classify — an unrecognized introducer, or one that never
// reaches its terminator before end-of-input (truncated, e.g. a capture cut
// off at the 200-line boundary) — fails closed: everything from that ESC
// byte to end-of-input is dropped, since an unverified sequence's effect on
// the real terminal can never be assumed safe.
//
// Bare control characters move the cursor exactly like the CSI sequences
// dropped above, so they are contained too. The live symptom: `herdr pane
// read` returns CRLF lines, and a line's trailing "\r" sent the cursor back
// to column 0 of its terminal row, where the preview's padding blanked the
// list column until a later frame happened to rewrite that row. So:
//
//   - "\r\n" becomes "\n", and a bare "\r" is dropped;
//   - '\t' becomes the spaces up to the next multiple of tabStop of the
//     line's visible column (SGR sequences, kept or dropped sequences add no
//     cells; a rune adds its ansi.StringWidth cells, 2 for a wide one), so
//     every later width measurement sees the cells the terminal would draw;
//   - every other C0 control (BS, VT, FF, BEL, NUL…) and DEL is dropped;
//   - C1 controls U+0080–U+009F are dropped: several terminals act on 8-bit
//     CSI and OSC (U+009B, U+009D) as they would on ESC [ and ESC ];
//   - an invalid UTF-8 byte becomes U+FFFD, so the terminal and
//     ansi.StringWidth agree on its width (and a raw 8-bit C1 byte, never
//     valid UTF-8 on its own, cannot reach the terminal either).
//
// Everything else — printable ASCII, box-drawing characters, wide and
// multi-byte runes, '\n' and blank lines — is copied through unmodified, in
// runs. Escape-sequence scanning stays byte-oriented: every byte it acts on
// (ESC, BEL, CSI parameter/intermediate/final bytes) is ASCII, and never a
// lead or continuation byte of a multi-byte UTF-8 rune (those are >= 0x80).
// Input that needs no change at all is returned as is, without allocating.
//
// O(n) single pass, no backtracking, no regex on untrusted content.
func sanitizePaneCapture(s string) string {
	var b strings.Builder
	var col lineColumn
	n := len(s)
	i, plain := 0, 0 // s[plain:i] is copied through on the next flush
	for i < n {
		c := s[i]
		if (c >= 0x20 && c < 0x7f) || c == '\n' {
			i++
			continue
		}
		size := 1
		if c >= utf8.RuneSelf {
			var r rune
			r, size = utf8.DecodeRuneInString(s[i:])
			if size > 1 && !isC1(r) {
				i += size
				continue
			}
		}
		if b.Cap() == 0 {
			b.Grow(n)
		}
		b.WriteString(s[plain:i])
		switch {
		case c == esc:
			end, keep := scanEscape(s, i)
			if keep {
				b.WriteString(s[i:end])
			}
			i = end
		case c == '\t':
			col.expandTab(&b)
			i++
		case c >= utf8.RuneSelf && size == 1:
			b.WriteString(string(utf8.RuneError))
			i++
		default:
			// Any other C0 control, DEL or a C1 control: dropped.
			i += size
		}
		plain = i
	}
	if plain == 0 {
		return s
	}
	b.WriteString(s[plain:])
	return b.String()
}

// tabStop is the column interval a tab advances to, the terminal default.
const tabStop = 8

// lineColumn tracks the visible column sanitizePaneCapture's output has
// reached on its current line, for tab expansion. Only a tab needs it, so it
// is measured lazily: the cells of the output written since the last tab
// (from the last '\n' in it, when there is one) are counted when the next
// tab is met, so each output byte is measured at most once.
type lineColumn struct {
	from, cells int
}

// expandTab writes the spaces that take the output to the next tab stop.
func (c *lineColumn) expandTab(b *strings.Builder) {
	seg := b.String()[c.from:]
	if nl := strings.LastIndexByte(seg, '\n'); nl >= 0 {
		seg, c.cells = seg[nl+1:], 0
	}
	c.cells += ansi.StringWidth(seg)
	pad := tabStop - c.cells%tabStop
	b.WriteString(blanks[:pad])
	c.cells += pad
	c.from = b.Len()
}

// isC1 reports a C1 control code point (U+0080–U+009F).
func isC1(r rune) bool { return r >= 0x80 && r <= 0x9f }

// scanEscape classifies the ESC-introduced sequence at s[i] == ESC: it
// returns the index just past it and whether it is an SGR sequence (the
// only kind sanitizePaneCapture keeps). A sequence it cannot classify —
// ESC at end-of-input, an unrecognized introducer, a malformed or
// unterminated sequence — fails closed with end == len(s), so nothing after
// it survives.
func scanEscape(s string, i int) (end int, sgr bool) {
	n := len(s)
	if i+1 >= n {
		return n, false
	}
	switch s[i+1] {
	case '[':
		return scanCSI(s, i)
	case ']':
		// OSC: xterm convention accepts BEL or ST as terminator.
		end, _ = scanStringSequence(s, i+2, true)
		return end, false
	case 'P', '_', '^', 'X':
		// DCS, APC, PM, SOS: ECMA-48 string sequences, ST only.
		end, _ = scanStringSequence(s, i+2, false)
		return end, false
	default:
		// Unrecognized ESC-introduced sequence: cannot verify its grammar
		// or extent, so fail closed for the rest of the input.
		return n, false
	}
}

// plainText makes a short, externally sourced display string — a Herdr
// workspace, tab or pane label, a terminal title shown as an agent title, a
// custom source's label or icon, a Meta value — safe to print as part of one
// row. sanitizePaneCapture keeps SGR and line structure, which suits a
// capture; a label is styled by its row and must stay on it, so here every
// escape sequence is removed (with the same grammar and the same fail-closed
// rule), "\r", "\n" and "\t" separate words, and every other C0 control,
// DEL and C1 control is dropped. A whitespace run holding a separator
// becomes one space, and none at either end, so a title ending in "\r\n"
// does not end in a blank cell; runs of plain spaces are kept. An invalid
// UTF-8 byte becomes U+FFFD. Almost every string is already plain and is
// returned as is, without allocating — still, callers apply it where the
// string enters a display model (a row view, a preview composition), never
// on every frame.
func plainText(s string) string {
	if isPlainText(s) {
		return s
	}
	buf := make([]byte, 0, len(s))
	sep := false // a separator was met since the last kept rune
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == esc:
			i, _ = scanEscape(s, i)
			continue
		case c == '\t' || c == '\n' || c == '\r':
			buf = bytes.TrimRight(buf, " ")
			sep = true
			i++
			continue
		case (c == ' ' && sep) || c < 0x20 || c == 0x7f:
			i++
			continue
		}
		size := 1
		if c >= utf8.RuneSelf {
			var r rune
			r, size = utf8.DecodeRuneInString(s[i:])
			if isC1(r) {
				i += size
				continue
			}
		}
		if sep && len(buf) > 0 {
			buf = append(buf, ' ')
		}
		sep = false
		if c >= utf8.RuneSelf && size == 1 {
			buf = utf8.AppendRune(buf, utf8.RuneError)
		} else {
			buf = append(buf, s[i:i+size]...)
		}
		i += size
	}
	return string(buf)
}

// isPlainText reports a string plainText returns unchanged: no control
// byte, no C1 control and valid UTF-8.
func isPlainText(s string) bool {
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c < 0x20 || c == 0x7f {
				return false
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 1 || isC1(r) {
			return false
		}
		i += size
	}
	return true
}

// scanCSI scans a CSI (Control Sequence Introducer) sequence starting at
// s[start] == ESC, s[start+1] == '['. It scans forward through parameter
// bytes (0x30-0x3F) and intermediate bytes (0x20-0x2F) — collapsed here into
// a single "keep scanning" range 0x20-0x3F — until it finds the final byte
// (0x40-0x7E), which unambiguously ends a well-formed CSI sequence. Returns
// the index just past the sequence and whether it should be KEPT: only SGR
// (final byte 'm', color/style) is kept; every other final byte (cursor
// movement, erase, scroll, private mode set, etc.) is dropped. If a byte
// outside the valid parameter/intermediate/final ranges appears (including
// an embedded ESC — never valid inside a CSI sequence) or end-of-input is
// reached before any final byte, the sequence is malformed/truncated and
// fails closed: dropped, with end == n so nothing after it survives either.
func scanCSI(s string, start int) (end int, keep bool) {
	n := len(s)
	j := start + 2
	for j < n {
		c := s[j]
		if c >= 0x40 && c <= 0x7e {
			return j + 1, c == 'm'
		}
		if c < 0x20 || c > 0x3f {
			// Invalid byte inside a CSI sequence (including an embedded
			// ESC): malformed. Fail closed for the remainder of the input.
			return n, false
		}
		j++
	}
	// Reached end-of-input without a final byte: truncated. Fail closed.
	return n, false
}

// scanStringSequence scans the payload of a string-type sequence (OSC, DCS,
// APC, PM, or SOS) starting at s[from], which is the byte immediately after
// the ESC + introducer byte. It walks byte-by-byte looking for the
// sequence's terminator: BEL (0x07) when acceptBEL is true (OSC only, the
// long-standing xterm convention), or ST — ESC immediately followed by '\\'
// — always.
//
// This is the R1-001 fix at its core: any OTHER byte encountered while
// scanning, INCLUDING an embedded ESC that is not immediately followed by
// '\\', is treated as ordinary payload data and consumed — never as a fresh
// sequence boundary. The old regex's negated character class explicitly
// excluded ESC from the payload it would match, which let it stop short at
// an embedded ESC-introduced sequence and leave a reconstructed,
// syntactically valid sequence behind once the embedded piece was removed.
// By only ever recognizing the two literal terminator shapes and treating
// everything else — including look-alike embedded openers — as opaque data
// to scan across, the whole outer sequence (opener through its real
// terminator) is always identified and dropped as one unit; nothing can
// "close" it early and leave a reconstructed fragment.
//
// Returns the index just past the terminator, and false if the terminator
// is never found before end-of-input (fail closed on truncation).
func scanStringSequence(s string, from int, acceptBEL bool) (end int, ok bool) {
	n := len(s)
	j := from
	for j < n {
		c := s[j]
		if acceptBEL && c == bel {
			return j + 1, true
		}
		if c == esc && j+1 < n && s[j+1] == '\\' {
			return j + 2, true
		}
		j++
	}
	return n, false
}
