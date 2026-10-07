package tui

import (
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/source"
)

// === Query input correctness: bursts, paste, rune-safe and word deletion ===
//
// Bubble Tea v1 delivers a fast key burst, a tmux send-keys string, or a
// bracketed paste as ONE KeyRunes message carrying several runes. These
// tests prove all of them reach the query (printable runes only), that
// deletion never splits a multi-byte rune, and that ctrl+w/alt+backspace
// delete the previous word, from either focus state.

// burst is a multi-rune KeyRunes message, as Bubble Tea emits for fast typing.
func burst(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// paste is a bracketed-paste KeyRunes message (msg.String() is "[...]").
func paste(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true}
}

var (
	keyCtrlW        = tea.KeyMsg{Type: tea.KeyCtrlW}
	keyAltBackspace = tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}
)

func queryInputModel() Model {
	return NewModel([]source.Candidate{
		zoxideCandidate("alpha", "/alpha"),
		zoxideCandidate("shep", "/shep"),
		zoxideCandidate("foobarbaz", "/foobarbaz"),
	}, nil)
}

// TestQueryInput_DeleteChordSpelling locks the exact String() Bubble Tea
// v1.3.10 produces for the two word-deletion chords keys.go matches on
// (alt+backspace is ESC+DEL, i.e. KeyBackspace with Alt set).
func TestQueryInput_DeleteChordSpelling(t *testing.T) {
	t.Parallel()
	if got := keyCtrlW.String(); got != "ctrl+w" {
		t.Errorf("KeyCtrlW.String() = %q, want \"ctrl+w\"", got)
	}
	if got := keyAltBackspace.String(); got != "alt+backspace" {
		t.Errorf("alt KeyBackspace.String() = %q, want \"alt+backspace\"", got)
	}
}

// TestQueryInput_MultiRuneBurstAppendsAllRunes proves a burst is appended
// in full (it used to be dropped because only single runes were accepted)
// and refilters exactly like a single typed rune.
func TestQueryInput_MultiRuneBurstAppendsAllRunes(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	seq := m.previewSeq
	m, _ = update(t, m, burst("shep"))
	if m.query != "shep" {
		t.Fatalf("query after burst = %q, want \"shep\"", m.query)
	}
	if m.lastAppliedQuery != "shep" {
		t.Errorf("lastAppliedQuery = %q, want \"shep\" (burst must refilter)", m.lastAppliedQuery)
	}
	if m.previewSeq != seq+1 {
		t.Errorf("previewSeq = %d, want %d (burst must re-sync the preview)", m.previewSeq, seq+1)
	}
	if row, ok := m.currentRow(); !ok || row.Candidate.Label != "shep" {
		t.Errorf("highlighted row = %+v (ok=%v), want the \"shep\" candidate", row.Candidate.Label, ok)
	}

	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, burst("yz"))
	if m.query != "shepxyz" {
		t.Errorf("query after single rune + burst = %q, want \"shepxyz\"", m.query)
	}
}

// TestQueryInput_BurstSpellingAChordIsText proves a burst whose runes spell
// a chord name is still text: Bubble Tea's String() of the runes "esc" is
// exactly "esc", so matching on String() would cancel, select, or move.
func TestQueryInput_BurstSpellingAChordIsText(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"esc", "tab", "enter", "up", "down", "end", "ctrl+t", "ctrl+x", "backspace", "ctrl+w"} {
		for _, focus := range []Focus{FocusList} {
			m := queryInputModel()
			m.focus = focus
			startTab := m.activeTab
			m, _ = update(t, m, burst(text))
			if m.query != text {
				t.Errorf("focus %v, burst %q: query = %q, want the burst inserted as text", focus, text, m.query)
			}
			if m.focus != FocusList {
				t.Errorf("focus %v, burst %q: focus = %v, want FocusList", focus, text, m.focus)
			}
			if m.cancelled || m.hasSelected || m.activeTab != startTab || m.closeConfirm != nil || m.closePending || m.closeStatus != (footerStatus{}) {
				t.Errorf("focus %v, burst %q acted as a chord: cancelled=%v selected=%v tab=%q->%q close=%q", focus, text, m.cancelled, m.hasSelected, startTab, m.activeTab, m.closeStatus.text)
			}
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

// TestQueryInput_SinglePastedRuneIsText proves a one-rune paste reaches the
// query (its String() is "[?]", which the old single-rune check rejected)
// and never triggers the "?" help chord, while a typed "?" still opens help.
func TestQueryInput_SinglePastedRuneIsText(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m, _ = update(t, m, paste("?"))
	if m.query != "?" || m.focus != FocusList {
		t.Errorf("after pasting \"?\": query = %q focus = %v, want \"?\" and FocusList", m.query, m.focus)
	}
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp || m.query != "?" {
		t.Errorf("after typing \"?\": focus = %v query = %q, want FocusHelp and the query untouched", m.focus, m.query)
	}
}

// TestQueryInput_ControlRunesDropped proves C0 controls, DEL and C1
// controls never enter the query (a raw ESC echoed into the prompt row
// would drive the terminal), while the printable runes around them do.
func TestQueryInput_ControlRunesDropped(t *testing.T) {
	t.Parallel()
	for _, msg := range []tea.KeyMsg{burst("a\x00b\x1bc\x7fd\u009be"), paste("a\x00b\x1bc\x7fd\u009be")} {
		m := queryInputModel()
		m, _ = update(t, m, msg)
		if m.query != "abcde" {
			t.Errorf("%q: query = %q, want \"abcde\"", msg.String(), m.query)
		}
	}
}

// TestQueryInput_NothingPrintableIsUnboundKey proves input with no
// printable rune (a paste of only line breaks) behaves like any other
// unbound key: the query is not refiltered.
func TestQueryInput_NothingPrintableIsUnboundKey(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m.query = "al"
	m.applyFilter()
	m, cmd := update(t, m, paste("\n\t\r\n"))
	if m.query != "al" || cmd != nil {
		t.Errorf("control-only paste: query = %q cmd = %v, want \"al\", nil", m.query, cmd != nil)
	}
	m, _ = update(t, m, burst("\x01\x02"))
	if m.query != "al" {
		t.Errorf("control-only burst from list: query = %q, want \"al\"", m.query)
	}
}

// TestQueryInput_AltRunesAreNotText proves an alt-modified rune stays a
// chord, never text, exactly as under the previous single-rune contract.
func TestQueryInput_AltRunesAreNotText(t *testing.T) {
	t.Parallel()
	m := queryInputModel()
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true})
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
	m, _ = update(t, m, burst("canción"))
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
	for _, chord := range []tea.KeyMsg{keyCtrlW, keyAltBackspace} {
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
// ignores bursts, pastes and word deletion, including a burst that spells
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
	for _, msg := range []tea.KeyMsg{burst("esc"), burst("shep"), paste("x"), keyCtrlW, keyAltBackspace} {
		m, _ = update(t, m, msg)
		if m.focus != FocusHelp || m.query != "foo bar" {
			t.Errorf("%q while help is open: focus = %v query = %q, want FocusHelp and \"foo bar\"", msg.String(), m.focus, m.query)
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
	if got, ok := appendQueryRunes("ab", []rune("\n\t\x1b")); ok || got != "ab" {
		t.Errorf("appendQueryRunes(control only) = %q, %v; want \"ab\", false", got, ok)
	}
	if got, ok := appendQueryRunes("ab", nil); ok || got != "ab" {
		t.Errorf("appendQueryRunes(nil) = %q, %v; want \"ab\", false", got, ok)
	}
	if got, ok := appendQueryRunes("ab", []rune("c d\ne")); !ok || got != "abc de" {
		t.Errorf("appendQueryRunes(\"c d\\ne\") = %q, %v; want \"abc de\", true", got, ok)
	}
	if got := queryInputRunes(tea.KeyMsg{Type: tea.KeySpace}); string(got) != " " {
		t.Errorf("queryInputRunes(space) = %q, want \" \"", string(got))
	}
}

// keyDoubleEsc is two Esc presses delivered in one read: Bubble Tea decodes
// "\x1b\x1b" as an alt-modified Esc.
var keyDoubleEsc = tea.KeyMsg{Type: tea.KeyEscape, Alt: true}

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
		if !next.(Model).Cancelled() {
			t.Fatal("double Esc on an empty query did not cancel")
		}
	})

	t.Run("query is cleared, then the picker cancels", func(t *testing.T) {
		t.Parallel()
		m := queryInputModel()
		m.query = "shep"
		next, _ := m.handleKey(keyDoubleEsc)
		got := next.(Model)
		if !got.Cancelled() {
			t.Fatal("double Esc with a query did not cancel after clearing it")
		}
	})

	t.Run("help closes, then the query clears", func(t *testing.T) {
		t.Parallel()
		m := queryInputModel()
		m.query = "shep"
		m.focus = FocusHelp
		next, _ := m.handleKey(keyDoubleEsc)
		got := next.(Model)
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
		m.closeConfirm = &closeTarget{kind: "tab", id: "t1", label: "api"}
		next, _ := m.handleKey(keyDoubleEsc)
		got := next.(Model)
		if got.closeConfirm != nil {
			t.Fatal("double Esc left the close confirmation pending")
		}
		if got.query != "" || got.Cancelled() {
			t.Fatalf("query = %q, cancelled = %v; want cleared and still open", got.query, got.Cancelled())
		}
	})
}
