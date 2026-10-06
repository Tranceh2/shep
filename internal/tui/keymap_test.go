package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
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
		renderKeycap(m.styles, keyBindingEnter.footerChord, keyBindingEnter.footerLabel),
		renderKeycap(m.styles, keyBindingTab.footerChord, keyBindingTab.footerLabel),
		renderKeycap(m.styles, keyBindingHelp.footerChord, keyBindingHelp.footerLabel),
		renderKeycap(m.styles, keyBindingEsc.footerChord, keyBindingEsc.footerLabel),
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
	wantCtrlT := renderKeycap(m.styles, keyBindingCtrlT.footerChord, keyBindingCtrlT.footerLabel)
	wantCtrlP := renderKeycap(m.styles, keyBindingCtrlP.footerChord, keyBindingCtrlP.footerLabel)
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
		"  ctrl+d/ctrl+u             move half a page",
		"  left/right                collapse/expand a workspace's tabs/panes",
		"  enter                     open the highlighted row",
		"  ctrl+f                    pin/unpin the highlighted top-level candidate",
		"  tab / shift+tab           switch between all and agents filter",
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
		"  ctrl+x                    close the highlighted open Herdr pane, tab, or workspace (y/n if configured)",
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

// === SPEC-NAV-1: descriptor-driven footer/help Enter copy ===

// TestFooterHints_TabRowShowsFocusTabLabel proves SPEC-NAV-1.5: when a
// synthesized tab row is highlighted, the footer's Enter hint reads
// "Focus tab" (matching driver.FocusTab), not the generic "open" label.
func TestFooterHints_TabRowShowsFocusTabLabel(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.rows = []Row{{Kind: RowTab, Action: RowActionFocusTab, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}}
	m.cursor = 0
	got := m.footerHints()
	if !strings.Contains(got, "enter Focus tab") {
		t.Errorf("footerHints() = %q, want it to contain the tab-row Enter hint \"enter Focus tab\"", got)
	}
	if strings.Contains(got, "enter open") {
		t.Errorf("footerHints() = %q, must not show the generic \"open\" label for a tab row", got)
	}
}

// TestFooterHints_CandidateRowKeepsOpenLabel proves SPEC-NAV-1.1/1.3: a
// top-level candidate row keeps the truthful generic "open" Enter label and
// never promises create/focus-existing.
func TestFooterHints_CandidateRowKeepsOpenLabel(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	got := m.footerHints()
	if !strings.Contains(got, "enter open") {
		t.Errorf("footerHints() = %q, want the candidate-row \"open\" Enter hint", got)
	}
	if strings.Contains(strings.ToLower(got), "create") {
		t.Errorf("footerHints() = %q, must not promise \"create\"", got)
	}
}

// TestFooterHints_NoHighlightInertEnter proves SPEC-NAV-1.7: with no row
// highlighted (empty rows), the footer Enter hint is inert/absent — it must
// not describe opening/focusing anything, and no target hints appear.
func TestFooterHints_NoHighlightInertEnter(t *testing.T) {
	t.Parallel()
	m := NewModel(nil, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	if _, ok := m.currentRow(); ok {
		t.Fatalf("setup: expected no highlighted row with a nil candidate set")
	}
	got := m.footerHints()
	// No target hints when nothing is highlighted.
	if strings.Contains(got, keyChordCtrlT) || strings.Contains(got, keyChordCtrlP) {
		t.Errorf("footerHints() = %q, must not show target hints with no highlight", got)
	}
	// The Enter hint must not promise a row-specific action ("Focus tab").
	if strings.Contains(got, "Focus tab") {
		t.Errorf("footerHints() = %q, must not show a row-specific Enter action with no highlight", got)
	}
}

// TestFooterHints_TargetHintsOnlyWhenEligibleAndPane proves SPEC-NAV-1.4/4.2:
// ctrl+t/ctrl+p target hints appear only when a current pane exists AND the
// highlighted candidate supports a current-workspace target (eligible), and
// are suppressed for an ineligible row (session) even with a pane present.
func TestFooterHints_TargetHintsOnlyWhenEligibleAndPane(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p0"}

	// Eligible zoxide candidate + pane: hints present.
	eligible := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	eligible, _ = update(t, eligible, sizeMsg(120, 36))
	eligible = eligible.WithCurrentPane(&pane)
	if got := eligible.footerHints(); !strings.Contains(got, keyChordCtrlT) || !strings.Contains(got, keyChordCtrlP) {
		t.Errorf("eligible+pane footerHints() = %q, want ctrl+t and ctrl+p hints", got)
	}

	// Ineligible session candidate + pane: hints suppressed.
	sess := source.Candidate{Label: "alpha", Source: config.SourceSessions, Meta: map[string]string{"session_name": "alpha"}}
	ineligible := NewModel([]source.Candidate{sess}, nil)
	ineligible, _ = update(t, ineligible, sizeMsg(120, 36))
	ineligible = ineligible.WithCurrentPane(&pane)
	if got := ineligible.footerHints(); strings.Contains(got, keyChordCtrlT) || strings.Contains(got, keyChordCtrlP) {
		t.Errorf("ineligible session row footerHints() = %q, must not show target hints", got)
	}
}
