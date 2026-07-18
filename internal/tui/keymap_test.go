package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// === Phase 7: footer/help KeyMap unification ===
//
// These tests prove the footer's compact hints and the full "?" help
// overlay render their key-chord notation from the SAME keyBinding values
// (keyBindingEnter, keyBindingTab, keyBindingCtrlT, keyBindingCtrlP,
// keyBindingEsc, keyBindingHelp) instead of two independently hand-typed
// string literals — the drift this phase closes.

// TestFooterHints_MatchSharedKeyBindingValues proves footerHints() builds
// each segment from the shared keyBinding vars' footerChord/footerLabel
// fields, not independent literals.
func TestFooterHints_MatchSharedKeyBindingValues(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	got := m.footerHints()

	want := strings.Join([]string{
		formatHint(keyBindingEnter.footerChord, keyBindingEnter.footerLabel),
		formatHint(keyBindingTab.footerChord, keyBindingTab.footerLabel),
		formatHint(keyBindingEsc.footerChord, keyBindingEsc.footerLabel),
		formatHint(keyBindingHelp.footerChord, keyBindingHelp.footerLabel),
	}, footerSeparator)

	if got != want {
		t.Errorf("footerHints() = %q, want %q", got, want)
	}
}

// TestFooterHints_HerdrSegmentsMatchSharedKeyBindingValues proves the
// conditional ctrl+t/ctrl+p footer segments (shown only when the current
// pane + candidate support a workspace target) also come from the shared
// keyBindingCtrlT/keyBindingCtrlP values.
func TestFooterHints_HerdrSegmentsMatchSharedKeyBindingValues(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)

	got := m.footerHints()
	wantCtrlT := formatHint(keyBindingCtrlT.footerChord, keyBindingCtrlT.footerLabel)
	wantCtrlP := formatHint(keyBindingCtrlP.footerChord, keyBindingCtrlP.footerLabel)
	if !strings.Contains(got, wantCtrlT) {
		t.Errorf("footerHints() = %q, missing shared ctrl+t hint %q", got, wantCtrlT)
	}
	if !strings.Contains(got, wantCtrlP) {
		t.Errorf("footerHints() = %q, missing shared ctrl+p hint %q", got, wantCtrlP)
	}
}

// TestKeyMap_FooterBindingsRenderIdenticallyInHelpBody is the core
// drift-guard: for every keyMap binding with a non-empty footerChord (i.e.
// one that also appears in the footer), its help-overlay line — built via
// renderHelpLine from that exact same keyBinding value — must be present
// verbatim in helpBodyText(). Because footerHints() and helpBodyText() both
// read from the identical keyBinding struct value, a future edit to a
// shared chord or label can only be made once, in one place.
func TestKeyMap_FooterBindingsRenderIdenticallyInHelpBody(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	body := m.helpBodyText()

	var checked int
	for _, section := range keyMap {
		for _, b := range section.bindings {
			if b.footerChord == "" {
				continue
			}
			checked++
			line := renderHelpLine(b)
			if !strings.Contains(body, line) {
				t.Errorf("helpBodyText() missing line %q for footer-visible binding (chord=%q)", line, b.footerChord)
			}
		}
	}
	if checked == 0 {
		t.Fatal("setup: expected at least one keyMap binding with a footerChord")
	}
}

// TestRenderHelpLine_PadsChordColumnTo28 proves renderHelpLine keeps the
// help overlay's existing fixed-width key column (2-space indent + a
// 26-column chord field) so refactoring the line-generation logic never
// silently reflows the help body's layout.
func TestRenderHelpLine_PadsChordColumnTo28(t *testing.T) {
	t.Parallel()
	cases := []struct {
		chord string
		help  string
		want  string
	}{
		{chord: "enter", help: "open the highlighted row", want: "  enter                     open the highlighted row"},
		{chord: "tab / shift+tab", help: "switch focus between list and preview", want: "  tab / shift+tab           switch focus between list and preview"},
		{chord: "esc, ctrl+c, ctrl+g", help: "cancel the picker", want: "  esc, ctrl+c, ctrl+g       cancel the picker"},
	}
	for _, tc := range cases {
		got := renderHelpLine(keyBinding{chord: tc.chord, help: tc.help})
		if got != tc.want {
			t.Errorf("renderHelpLine(chord=%q) = %q, want %q", tc.chord, got, tc.want)
		}
	}
}

// TestHelpBodyText_MatchesApprovedContent locks the full help body text so
// this phase's refactor is proven byte-identical to the pre-refactor
// hand-typed version (no accidental reflow of headings/blank lines).
func TestHelpBodyText_MatchesApprovedContent(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))

	want := strings.Join([]string{
		m.styles.helpHeadingStyle.Render("Navigation"),
		"  up/down, ctrl+j/ctrl+k    move the cursor",
		"  left/right                collapse/expand a workspace's tabs/panes",
		"  enter                     open the highlighted row",
		"  tab / shift+tab           switch focus between list and preview",
		"  ctrl+u                    clear the query",
		"  backspace                 delete the last query character",
		"",
		m.styles.helpHeadingStyle.Render("Preview (while focused)"),
		"  up/down, ctrl+j/ctrl+k    scroll one line",
		"  pgup/pgdown               scroll one page",
		"  home/end                  jump to top/bottom",
		"  any letter                return to the list and search",
		"",
		m.styles.helpHeadingStyle.Render("Herdr"),
		"  ctrl+t                    open in a new tab of the current workspace (list or preview focus)",
		"  ctrl+p                    open in a new pane of the current workspace (list or preview focus)",
		"",
		m.styles.helpHeadingStyle.Render("Layout"),
		"  ctrl+l                    toggle layout: auto / landscape (list focus only)",
		"",
		m.styles.helpHeadingStyle.Render("Help (this screen)"),
		"  up/down, ctrl+j/ctrl+k    scroll one line",
		"  pgup/pgdown               scroll one page",
		"  home/end                  jump to top/bottom",
		"  ?, esc                    close help and return to what you were doing",
		"",
		m.styles.helpHeadingStyle.Render("Session"),
		"  esc, ctrl+c, ctrl+g       cancel the picker",
	}, "\n")

	if got := m.helpBodyText(); got != want {
		t.Errorf("helpBodyText() changed by the KeyMap refactor:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
