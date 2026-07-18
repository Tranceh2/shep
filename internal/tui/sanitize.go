package tui

import "strings"

// esc and bel are the two control bytes the scanner below reasons about
// explicitly; every other byte is either plain content or part of a
// well-defined escape-sequence grammar handled inline.
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
// Plain printable bytes/runes — including box-drawing characters,
// wide/multi-byte UTF-8 runes, and blank lines — are copied through
// unmodified. A pure byte-copy loop is UTF-8 safe here because none of the
// bytes this scanner acts on (ESC, BEL, CSI parameter/intermediate/final
// bytes) ever appear as a continuation or lead byte of a valid multi-byte
// UTF-8 rune (those are always >= 0x80); the scanner is only ever triggered
// by an actual 0x1B byte.
//
// O(n) single pass, no backtracking, no regex on untrusted content.
func sanitizePaneCapture(s string) string {
	n := len(s)
	var b strings.Builder
	b.Grow(n)
	i := 0
	for i < n {
		if s[i] != esc {
			b.WriteByte(s[i])
			i++
			continue
		}
		// s[i] == ESC with nothing after it: cannot classify, fail closed.
		if i+1 >= n {
			break
		}
		switch s[i+1] {
		case '[':
			end, keep := scanCSI(s, i)
			if keep {
				b.WriteString(s[i:end])
			}
			i = end
		case ']':
			// OSC: xterm convention accepts BEL or ST as terminator.
			end, ok := scanStringSequence(s, i+2, true)
			if !ok {
				i = n
				continue
			}
			i = end
		case 'P', '_', '^', 'X':
			// DCS, APC, PM, SOS: ECMA-48 string sequences, ST only.
			end, ok := scanStringSequence(s, i+2, false)
			if !ok {
				i = n
				continue
			}
			i = end
		default:
			// Unrecognized ESC-introduced sequence: cannot verify its
			// grammar or extent, so fail closed for the rest of the input.
			i = n
		}
	}
	return b.String()
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
