package tui

import (
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/source"
)

// === Query input correctness: paste, rune-safe and word deletion ===
//
// Bubble Tea v2 decodes one key press per grapheme and delivers a bracketed
// paste as one tea.PasteMsg. These tests prove pasted text reaches the query
// as text only (printable runes, never a binding), that deletion never
// splits a multi-byte rune, and that ctrl+w/alt+backspace delete the
// previous word, from either focus state.

// paste is a bracketed paste of s.
func paste(s string) tea.PasteMsg { return tea.PasteMsg{Content: s} }

var (
	keyCtrlW        = key("ctrl+w")
	keyAltBackspace = key("alt+backspace")
)

func queryInputModel() Model {
	return NewModel([]source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("shep", "/shep"),
		zoxideCandidate("foobarbaz", "/foobarbaz"),
	}, nil)
}

// TestQueryInput_DeleteChordSpelling locks the String() Bubble Tea produces
// for the two word-deletion chords keys.go matches on (alt+backspace is
// ESC+DEL, i.e. KeyBackspace with ModAlt).
func TestQueryInput_DeleteChordSpelling(t *testing.T) {
	t.Parallel()
	if got := keyCtrlW.String(); got != "ctrl+w" {
		t.Errorf("ctrl+w String() = %q, want \"ctrl+w\"", got)
	}
	if got := keyAltBackspace.String(); got != "alt+backspace" {
		t.Errorf("alt+backspace String() = %q, want \"alt+backspace\"", got)
	}
}

// TestQueryInput_TypingRefilters proves typed runes build the query,
// refilter the rows and re-sync the preview.
func TestQueryInput_TypingRefilters(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	seq := m.previewSeq
	m = typeText(t, m, "shep")
	if m.query != "shep" || m.lastAppliedQuery != "shep" {
		t.Fatalf("query = %q (applied %q), want \"shep\" refiltered", m.query, m.lastAppliedQuery)
	}
	if m.previewSeq <= seq {
		t.Errorf("previewSeq = %d, want past %d (typing must re-sync the preview)", m.previewSeq, seq)
	}
	if row, ok := m.currentRow(); !ok || row.Candidate.Label != "shep" {
		t.Errorf("highlighted row = %+v (ok=%v), want the \"shep\" candidate", row.Candidate.Label, ok)
	}
}

// TestQueryInput_PasteSpellingAChordIsText proves a paste whose text spells
// a binding is still text: matching it as a key name would cancel, select,
// move or confirm a close.
func TestQueryInput_PasteSpellingAChordIsText(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"esc", "tab", "enter", "up", "down", "end", "ctrl+t", "ctrl+x", "backspace", "ctrl+w", "?", "y"} {
		m := queryInputModel()
		startTab := m.activeTab
		m, _ = update(t, m, paste(text))
		if m.query != text {
			t.Errorf("paste %q: query = %q, want the paste inserted as text", text, m.query)
		}
		if m.focus != FocusList {
			t.Errorf("paste %q: focus = %v, want FocusList", text, m.focus)
		}
		if m.cancelled || m.hasSelected || m.activeTab != startTab || m.closeConfirm != nil || m.closePending || m.actionStatus != (footerStatus{}) {
			t.Errorf("paste %q acted as a chord: cancelled=%v selected=%v tab=%q->%q close=%q", text, m.cancelled, m.hasSelected, startTab, m.activeTab, m.actionStatus.text)
		}
	}
}

// TestQueryInput_BracketedPasteDropsLineBreaksAndTabs proves a paste is
// inserted with its newlines, carriage returns and tabs removed (not
// replaced by spaces): the query is a single line.
func TestQueryInput_BracketedPasteDropsLineBreaksAndTabs(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m, _ = update(t, m, paste("foo\nbar\tbaz"))
	if m.query != "foobarbaz" {
		t.Fatalf("query after paste = %q, want \"foobarbaz\"", m.query)
	}
	if row, ok := m.currentRow(); !ok || row.Candidate.Label != "foobarbaz" {
		t.Errorf("highlighted row = %q (ok=%v), want the \"foobarbaz\" candidate", row.Candidate.Label, ok)
	}

	m, _ = update(t, m, paste("\r\nqux\r\n"))
	if m.query != "foobarbazqux" {
		t.Errorf("query after CRLF paste = %q, want \"foobarbazqux\"", m.query)
	}
}

// TestQueryInput_ControlRunesDropped proves C0 controls, DEL and C1
// controls in a paste never enter the query (a raw ESC echoed into the
// prompt row would drive the terminal), while the printable runes around
// them do; input with nothing printable leaves the query unfiltered.
func TestQueryInput_ControlRunesDropped(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m, _ = update(t, m, paste("a\x00b\x1bc\x7fd\u009be"))
	if m.query != "abcde" {
		t.Errorf("query = %q, want \"abcde\"", m.query)
	}
	m.query = "al"
	m.applyFilter()
	m, cmd := update(t, m, paste("\n\t\r\n"))
	if m.query != "al" || cmd != nil {
		t.Errorf("control-only paste: query = %q cmd = %v, want \"al\", nil", m.query, cmd != nil)
	}
}

// TestQueryInput_AltRunesAreNotText proves an alt-modified rune stays a
// chord, never text, exactly as under the previous single-rune contract.
func TestQueryInput_AltRunesAreNotText(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m, _ = update(t, m, key("alt+a"))
	if m.query != "" {
		t.Errorf("alt+a: query = %q, want empty", m.query)
	}
}

// TestQueryInput_BackspaceDeletesWholeRune proves backspace removes the
// last rune, not the last byte: deleting from "canción" never leaves half
// of "ó" (the old byte slice produced "canci\xc3").
func TestQueryInput_BackspaceDeletesWholeRune(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m = typeText(t, m, "canción")
	for _, want := range []string{"canció", "canci", "canc"} {
		m, _ = update(t, m, key("backspace"))
		if m.query != want || !utf8.ValidString(m.query) {
			t.Fatalf("backspace: query = %q (valid UTF-8 = %v), want %q", m.query, utf8.ValidString(m.query), want)
		}
	}

	m.query = "go🚀ñ"
	m.applyFilter()
	for _, want := range []string{"go🚀", "go", "g", "", ""} {
		m, _ = update(t, m, key("backspace"))
		if m.query != want || !utf8.ValidString(m.query) {
			t.Fatalf("backspace: query = %q (valid UTF-8 = %v), want %q", m.query, utf8.ValidString(m.query), want)
		}
	}
}

// TestQueryInput_WordDeletion proves ctrl+w and alt+backspace delete the
// previous word with readline unix-word-rubout semantics: trailing spaces
// first, then the run of non-space runes before them. An empty query is a
// no-op, like backspace.
func TestQueryInput_WordDeletion(t *testing.T) {
	t.Parallel()
	cases := []struct{ query, want string }{
		{"foo bar  ", "foo "},
		{"foo bar", "foo "},
		{"foo", ""},
		{"foo  bar", "foo  "},
		{"   ", ""},
		{"año café", "año "},
		{"", ""},
	}
	for _, chord := range []tea.KeyPressMsg{keyCtrlW, keyAltBackspace} {
		for _, tc := range cases {
			m := queryInputModel()
			m.query = tc.query
			m.applyFilter()
			m, _ = update(t, m, chord)
			if m.query != tc.want {
				t.Errorf("%s on %q: query = %q, want %q", chord.String(), tc.query, m.query, tc.want)
			}
			if m.lastAppliedQuery != tc.want {
				t.Errorf("%s on %q: lastAppliedQuery = %q, want %q (deletion must refilter)", chord.String(), tc.query, m.lastAppliedQuery, tc.want)
			}
			if m.focus != FocusList || m.cancelled {
				t.Errorf("%s on %q: focus = %v cancelled = %v", chord.String(), tc.query, m.focus, m.cancelled)
			}
		}
	}
}

// TestQueryInput_HelpSwallowsTextAndDeletion proves the modal help overlay
// ignores typing, pastes and word deletion, including a paste that spells
// its own close chord ("esc").
func TestQueryInput_HelpSwallowsTextAndDeletion(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m.query = "foo bar"
	m.applyFilter()
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp {
		t.Fatalf("setup: focus = %v, want FocusHelp", m.focus)
	}
	for _, msg := range []tea.Msg{paste("esc"), key("s"), paste("x"), keyCtrlW, keyAltBackspace} {
		m, _ = update(t, m, msg)
		if m.focus != FocusHelp || m.query != "foo bar" {
			t.Errorf("%v while help is open: focus = %v query = %q, want FocusHelp and \"foo bar\"", msg, m.focus, m.query)
		}
	}
}

// TestQueryEditHelpers covers the pure query-editing helpers directly,
// including their empty-input edges.
func TestQueryEditHelpers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"a", ""}, {"ñ", ""}, {"canción", "canció"}, {"x🚀", "x"},
	} {
		if got := deleteLastRune(tc.in); got != tc.want {
			t.Errorf("deleteLastRune(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {" ", ""}, {"foo", ""}, {"foo ", ""}, {"foo bar", "foo "},
		{"foo bar  ", "foo "}, {" foo", " "}, {"a b", "a "},
	} {
		if got := deleteLastWord(tc.in); got != tc.want {
			t.Errorf("deleteLastWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got, ok := appendQueryText("ab", "\n\t\x1b"); ok || got != "ab" {
		t.Errorf("appendQueryText(control only) = %q, %v; want \"ab\", false", got, ok)
	}
	if got, ok := appendQueryText("ab", ""); ok || got != "ab" {
		t.Errorf("appendQueryText(\"\") = %q, %v; want \"ab\", false", got, ok)
	}
	if got, ok := appendQueryText("ab", "c d\ne"); !ok || got != "abc de" {
		t.Errorf("appendQueryText(\"c d\\ne\") = %q, %v; want \"abc de\", true", got, ok)
	}
	if got := keyText(key("space")); got != " " {
		t.Errorf("keyText(space) = %q, want \" \"", got)
	}
	if got := keyText(key("ctrl+a")); got != "" {
		t.Errorf("keyText(ctrl+a) = %q, want none", got)
	}
}

// keyDoubleEsc is two Esc presses delivered in one read: without key
// disambiguation the terminal sends "\x1b\x1b", decoded as an alt-modified
// Esc.
var keyDoubleEsc = key("alt+esc")

// TestDoubleEsc_ActsAsTwoPresses proves a quick double tap of Esc behaves
// exactly like two separate presses instead of matching nothing (the picker
// used to stay open inside the Herdr popup).
func TestDoubleEsc_ActsAsTwoPresses(t *testing.T) {
	t.Parallel()
	if got := keyDoubleEsc.String(); got != "alt+esc" {
		t.Fatalf("double Esc spelling = %q, want \"alt+esc\"", got)
	}

	t.Run("empty query cancels", func(t *testing.T) {
		t.Parallel()
		next, _ := queryInputModel().handleKey(keyDoubleEsc)
		if !next.Cancelled() {
			t.Fatal("double Esc on an empty query did not cancel")
		}
	})

	t.Run("query is cleared, then the picker cancels", func(t *testing.T) {
		t.Parallel()
		m := queryInputModel()
		m.query = "shep"
		next, _ := m.handleKey(keyDoubleEsc)
		if !next.Cancelled() {
			t.Fatal("double Esc with a query did not cancel after clearing it")
		}
	})

	t.Run("help closes, then the query clears", func(t *testing.T) {
		t.Parallel()
		m := queryInputModel()
		m.query = "shep"
		m.focus = FocusHelp
		next, _ := m.handleKey(keyDoubleEsc)
		got := next
		if got.focus != FocusList {
			t.Fatalf("focus = %v, want FocusList after help closed", got.focus)
		}
		if got.query != "" {
			t.Fatalf("query = %q, want cleared by the second press", got.query)
		}
		if got.Cancelled() {
			t.Fatal("double Esc from help must not cancel the picker")
		}
	})

	t.Run("a pending close confirmation is cancelled first", func(t *testing.T) {
		t.Parallel()
		m := queryInputModel()
		m.query = "shep"
		m.closeConfirm = &herdrItem{kind: "tab", id: "t1", label: "api"}
		next, _ := m.handleKey(keyDoubleEsc)
		got := next
		if got.closeConfirm != nil {
			t.Fatal("double Esc left the close confirmation pending")
		}
		if got.query != "" || got.Cancelled() {
			t.Fatalf("query = %q, cancelled = %v; want cleared and still open", got.query, got.Cancelled())
		}
	})
}
