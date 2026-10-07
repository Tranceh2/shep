package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
)

// maskedRunes returns the runes of text a highlight mask marks.
func maskedRunes(text string, mask []bool) string {
	var b strings.Builder
	for i, r := range []rune(text) {
		if i < len(mask) && mask[i] {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestAgentPresentation_RightTruncationAndHighlight proves agent titles keep
// their start when truncated (the start of a sentence carries its meaning),
// unlike every other row, and that a highlighted rune in the kept start keeps
// its accent through truncation.
func TestAgentPresentation_RightTruncationAndHighlight(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "sec"
	agent := Row{Kind: RowCandidate, Match: MatchDirect, MatchedIndexes: []int{0, 1, 2}, Candidate: source.Candidate{
		Source: config.SourceAgents, Icon: "X", Label: "security scan long title", Meta: map[string]string{"agent_status": "idle"},
	}}
	if v := m.buildRowView(agent); maskedRunes(v.primary, v.primaryHL) != "sec" || !v.keepStart {
		t.Fatalf("agent view = %+v, want the title highlighted and kept from its start", v)
	}
	prefix := "X " + m.agentStatusIcon("idle") + " "
	width := cursorPrefixWidth + len([]rune(prefix)) + 11
	for _, cursor := range []bool{false, true} {
		got := renderRowLineText(m.renderRowLine(agent, cursor, width))
		if !strings.HasSuffix(got, prefix+"security s…") {
			t.Errorf("cursor=%v agent row = %q, want the title's start and a trailing ellipsis", cursor, got)
		}
	}
	pane := agent
	pane.Kind = RowPane // the agents tab derives flat pane rows from the snapshot
	if got := renderRowLineText(m.renderRowLine(pane, true, width)); !strings.HasSuffix(got, prefix+"security s…") {
		t.Errorf("agents tab pane row = %q, want the same right truncation", got)
	}

	other := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Icon: "X", Label: "security scan long title"}}
	if got := renderRowLineText(m.renderRowLine(other, false, width-2)); !strings.HasSuffix(got, "X …long title") {
		t.Errorf("project row = %q, want the label's tail behind a leading ellipsis", got)
	}

	// The surviving highlighted start keeps the accent after truncation.
	_, mask := truncateMasked("security scan long title", m.buildRowView(agent).primaryHL, 11, true)
	if got := maskedRunes("security s…", mask); got != "sec" {
		t.Errorf("truncated mask highlights %q, want \"sec\"", got)
	}
}

// TestHighlight_EmptyMatchedIndexesHasNoMask proves a direct match without
// matched indexes renders no highlight.
func TestHighlight_EmptyMatchedIndexesHasNoMask(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	v := m.buildRowView(Row{Kind: RowCandidate, Candidate: zoxideCandidate("café", "/home/dev/café"), Match: MatchDirect})
	if v.primary != "café" || v.primaryHL != nil {
		t.Errorf("view = %q mask %v, want café without a highlight mask", v.primary, v.primaryHL)
	}
}

// TestHighlight_MatchUsesVisibleRowIndexes proves highlighting rescores the
// text the row shows — the label — rather than reusing the indexes scored
// against the label+path haystack.
func TestHighlight_MatchUsesVisibleRowIndexes(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	for _, tc := range []struct {
		query, label, want string
	}{
		{"ha", "alpha", "ha"},
		{"com", "components", "com"},
		{"café", "café", "café"},
	} {
		m.query = tc.query
		rows := buildRows(rowBuildInput{query: tc.query, candidates: []source.Candidate{zoxideCandidate(tc.label, "/home/dev/"+tc.label)}})
		if len(rows) != 1 || rows[0].Match != MatchDirect {
			t.Fatalf("setup: query %q must keep a direct row, got %+v", tc.query, rows)
		}
		v := m.buildRowView(rows[0])
		if got := maskedRunes(v.primary, v.primaryHL); v.primary != tc.label || got != tc.want {
			t.Errorf("query %q: view %q highlights %q, want %q", tc.query, v.primary, got, tc.want)
		}
	}
}

// TestHighlight_NonDirectMatchesUseNoQueryStyle proves only direct matches
// are highlighted; a descendant-only match renders in the descendant style.
func TestHighlight_NonDirectMatchesUseNoQueryStyle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "wk"
	for _, match := range []MatchKind{MatchDescendant, MatchNone} {
		v := m.buildRowView(Row{Kind: RowCandidate, Candidate: zoxideCandidate("workspace", "/home/dev/workspace"), Match: match, MatchedIndexes: []int{0, 2}})
		if v.primaryHL != nil {
			t.Errorf("match %v: highlight mask %v, want none", match, v.primaryHL)
		}
		if v.descendant != (match == MatchDescendant) {
			t.Errorf("match %v: descendant = %v", match, v.descendant)
		}
	}
}

// TestHighlight_FilenameFirstMapsAcrossParentAndName proves a match spanning
// the parent path and the name is scored on the whole displayed label and
// mapped onto both parts; the separating "/" is not shown.
func TestHighlight_FilenameFirstMapsAcrossParentAndName(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.homeDir = "/home/dev"
	m.query = "proshep"
	label := "~/Proyectos/shep"
	rows := buildRows(rowBuildInput{query: m.query, candidates: []source.Candidate{zoxideCandidate("", "/home/dev/Proyectos/shep")}})
	if len(rows) != 1 {
		t.Fatalf("setup: want one direct row, got %+v", rows)
	}
	v := m.buildRowView(rows[0])
	if v.primary != "shep" || v.secondary != "~/Proyectos" {
		t.Fatalf("split = %q + %q, want shep + ~/Proyectos", v.primary, v.secondary)
	}
	_, indexes := fuzzy.Score(m.query, label)
	sep := strings.LastIndex(label, "/")
	for _, i := range indexes {
		switch {
		case i < sep && !v.secondaryHL[i]:
			t.Errorf("parent rune %d (%q) not highlighted", i, string([]rune(label)[i]))
		case i > sep && !v.primaryHL[i-sep-1]:
			t.Errorf("name rune %d (%q) not highlighted", i, string([]rune(label)[i]))
		}
	}
	if maskedRunes(v.primary, v.primaryHL) == "" || maskedRunes(v.secondary, v.secondaryHL) == "" {
		t.Errorf("highlights = %q / %q, want matches in both parts", maskedRunes(v.secondary, v.secondaryHL), maskedRunes(v.primary, v.primaryHL))
	}
}

// TestWriteRuns_OneRenderPerStyleRun proves highlighted text renders as
// style runs — one Render per run of equally styled runes, never one per
// rune — and keeps the visible text unchanged. Not t.Parallel: it swaps
// lipgloss's global color profile so styles are distinguishable.
func TestWriteRuns_OneRenderPerStyleRun(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	s := newPalette(themes[ThemeMocha])
	base, hl := s.rowStyle, s.queryStyle
	for _, tc := range []struct {
		text string
		mask []bool
		want string
	}{
		{"alpha", []bool{false, false, false, true, true}, base.Render("alp") + hl.Render("ha")},
		{"alpha", []bool{true, true, false, false, true}, hl.Render("al") + base.Render("ph") + hl.Render("a")},
		{"café", []bool{false, false, false, true}, base.Render("caf") + hl.Render("é")},
		{"alpha", nil, base.Render("alpha")},
	} {
		var b strings.Builder
		writeRuns(&b, tc.text, tc.mask, base, hl)
		if got := b.String(); got != tc.want {
			t.Errorf("writeRuns(%q, %v) = %q, want %q", tc.text, tc.mask, got, tc.want)
		}
		if got := reSGR.ReplaceAllString(b.String(), ""); got != tc.text {
			t.Errorf("writeRuns(%q) visible text = %q", tc.text, got)
		}
	}
}
