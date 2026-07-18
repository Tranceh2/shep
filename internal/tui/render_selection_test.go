package tui

import (
	"strings"
	"testing"
)

// newRenderTestModel builds the minimal Model state renderRowLine reads
// (styles, theme, focus) so selection-rendering tests can exercise the cursor
// row in isolation without driving a full Update/View lifecycle. width/height
// are set only so a stray renderList call would not panic; renderRowLine
// itself takes width as an argument.
func newRenderTestModel(themeName string, focus Focus) Model {
	th := themes[themeName]
	return Model{
		styles: newPalette(th),
		theme:  th,
		focus:  focus,
		width:  40,
		height: 10,
	}
}

// renderRowLineText strips any residual ANSI so assertions see the structural
// text only (the headless test color profile already emits none, but this
// keeps the tests robust against a future profile-forcing helper).
func renderRowLineText(s string) string {
	return stripNonSGRANSI(s)
}

// TestRenderRowLine_SelectedDescendantKeepsMarker proves a selected
// descendant-only match keeps its "~" marker and italic/muted style (the old
// code let the cursor ">" marker win over the descendant "~"; the Phase 2
// treatment keeps the row's own marker and replaces only the cursor indicator
// with the gutter).
func TestRenderRowLine_SelectedDescendantKeepsMarker(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDescendant,
		Expandable: true,
		Expanded:   true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, cursorGutterGlyph) {
		t.Errorf("selected descendant: expected leading gutter %q, got %q", cursorGutterGlyph, got)
	}
	if !strings.Contains(got, "~") {
		t.Errorf("selected descendant: ~ marker missing in %q", got)
	}
	if strings.Contains(got, ">") {
		t.Errorf("selected descendant: old > cursor marker present, want gutter instead: %q", got)
	}
}

// TestRenderRowLine_SelectedNormalCandidateDropsOldMarker proves a selected
// direct-match candidate shows the gutter in place of the old ">" cursor
// marker, while its kind prefix (▸ for an expandable workspace) survives.
func TestRenderRowLine_SelectedNormalCandidateDropsOldMarker(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, cursorGutterGlyph) {
		t.Errorf("selected candidate: expected leading gutter, got %q", got)
	}
	if strings.Contains(got, ">") {
		t.Errorf("selected candidate: old > cursor marker present, want gutter: %q", got)
	}
	if !strings.Contains(got, "▸") {
		t.Errorf("selected candidate: ▸ kind prefix missing in %q", got)
	}
}

// TestRenderRowLine_UnfocusedSelectedShowsGutter proves that when the preview
// pane owns focus (FocusPreview) the selected row still renders a gutter, now
// via the unfocused (rule-colored) variant. The focused/unfocused distinction
// is color-only and thus invisible in the headless fixture; the style-level
// distinction is proven by TestStyleRoles_CursorGutter, and the wiring (focus
// selects the unfocused style) is exercised here by confirming a gutter still
// appears under FocusPreview.
func TestRenderRowLine_UnfocusedSelectedShowsGutter(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusPreview)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, cursorGutterGlyph) {
		t.Errorf("unfocused selected: expected leading gutter under FocusPreview, got %q", got)
	}
}

// TestRenderRowLine_UnfocusedSelectedShowsGutterInFocusHelp proves the list's
// cursor row also drops to the unfocused gutter/surface variant while
// FocusHelp owns focus (not just FocusPreview) — the Focus enum grew a
// third member in Phase 6, and renderSelectedFromParts must treat "focus is
// anything other than FocusList" as unfocused, not just "focus ==
// FocusPreview".
func TestRenderRowLine_UnfocusedSelectedShowsGutterInFocusHelp(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusHelp)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, cursorGutterGlyph) {
		t.Errorf("unfocused selected: expected leading gutter under FocusHelp, got %q", got)
	}
}

// TestRenderRowLine_PlainSelectedUsesPlainGutter proves the plain (no-color)
// theme uses the "|" gutter glyph instead of the colored "▌", so the cursor
// stays visible in a TERM=dumb / $NO_COLOR environment where the old full-row
// cursor style (reverse-video) was invisible.
func TestRenderRowLine_PlainSelectedUsesPlainGutter(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	row := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDirect,
		Expandable: true,
	}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.HasPrefix(got, cursorGutterGlyphPlain) {
		t.Errorf("plain selected: expected plain gutter %q, got %q", cursorGutterGlyphPlain, got)
	}
	if strings.Contains(got, cursorGutterGlyph) {
		t.Errorf("plain selected: colored glyph %q leaked into no-color render: %q", cursorGutterGlyph, got)
	}
}

// TestRenderRowLine_NonSelectedUnchanged proves non-selected rows render with
// NO gutter and keep their own marker slot (the descendant "~" for a
// descendant match, the plain alignment slot otherwise) — the selection
// treatment is confined to the cursor row.
func TestRenderRowLine_NonSelectedUnchanged(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)

	// Non-selected descendant keeps "~", no gutter.
	desc := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Match:      MatchDescendant,
		Expandable: true,
		Expanded:   true,
	}
	got := renderRowLineText(m.renderRowLine(desc, false, 40))
	if strings.HasPrefix(got, cursorGutterGlyph) || strings.HasPrefix(got, cursorGutterGlyphPlain) {
		t.Errorf("non-selected descendant: unexpected gutter in %q", got)
	}
	if !strings.Contains(got, "~") {
		t.Errorf("non-selected descendant: ~ marker missing in %q", got)
	}

	// Non-selected expandable workspace keeps its ▸ collapse indicator, no
	// gutter.
	ws := Row{
		Kind:       RowCandidate,
		Candidate:  herdrCandidate("backend", "/srv/backend", "w1"),
		Expandable: true,
	}
	got = renderRowLineText(m.renderRowLine(ws, false, 40))
	if strings.HasPrefix(got, cursorGutterGlyph) || strings.HasPrefix(got, cursorGutterGlyphPlain) {
		t.Errorf("non-selected workspace: unexpected gutter in %q", got)
	}
	if !strings.Contains(got, "▸") {
		t.Errorf("non-selected workspace: ▸ indicator missing in %q", got)
	}
}
