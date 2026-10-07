package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
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
// like every label that is not a path, while a path keeps its end, and that a
// highlighted rune in the kept start keeps its accent through truncation.
func TestAgentPresentation_RightTruncationAndHighlight(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Agents.Icon, p.Projects.Icon, p.Projects.Label, p.Projects.Detail = "X", "X", "{{ .Label }}", ""
	})
	m.query = "sec"
	agent := Row{Kind: RowCandidate, Match: MatchDirect, MatchedIndexes: []int{0, 1, 2}, Candidate: source.Candidate{
		Source: config.SourceAgents, Label: "security scan long title", Meta: map[string]string{"agent_status": "idle"},
	}}
	if v := m.buildRowView(agent); maskedRunes(v.label.text, v.label.hl) != "sec" || !v.keepStart {
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

	other := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Label: "~/security/scan/long/title"}}
	if got := renderRowLineText(m.renderRowLine(other, false, width-2)); !strings.HasSuffix(got, "X …long/title") {
		t.Errorf("project row = %q, want the path label's tail behind a leading ellipsis", got)
	}

	// The surviving highlighted start keeps the accent after truncation.
	cut := truncatePart(m.buildRowView(agent).label, 13, true)
	if got := maskedRunes(cut.text, cut.hl); cut.text != "✓ security s…" || got != "sec" {
		t.Errorf("truncated label %q highlights %q, want \"sec\"", cut.text, got)
	}
}

// TestHighlight_EmptyMatchedIndexesHasNoMask proves a direct match without
// matched indexes renders no highlight.
func TestHighlight_EmptyMatchedIndexesHasNoMask(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	v := m.buildRowView(Row{Kind: RowCandidate, Candidate: zoxideCandidate("café", "/home/dev/café"), Match: MatchDirect})
	if v.label.text != "café" || v.label.hl != nil {
		t.Errorf("view = %q mask %v, want café without a highlight mask", v.label.text, v.label.hl)
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
		if got := maskedRunes(v.label.text, v.label.hl); v.label.text != tc.label || got != tc.want {
			t.Errorf("query %q: view %q highlights %q, want %q", tc.query, v.label.text, got, tc.want)
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
		if v.label.hl != nil {
			t.Errorf("match %v: highlight mask %v, want none", match, v.label.hl)
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
	m.layout.Templates = tmpl.New("/home/dev")
	m.query = "proshep"
	label := "~/Proyectos/shep"
	rows := buildRows(rowBuildInput{query: m.query, candidates: []source.Candidate{zoxideCandidate("", "/home/dev/Proyectos/shep")}})
	if len(rows) != 1 {
		t.Fatalf("setup: want one direct row, got %+v", rows)
	}
	v := m.buildRowView(rows[0])
	if v.label.text != "shep" || v.detail.text != "~/Proyectos" {
		t.Fatalf("split = %q + %q, want shep + ~/Proyectos", v.label.text, v.detail.text)
	}
	_, indexes := fuzzy.Score(m.query, label)
	sep := strings.LastIndex(label, "/")
	for _, i := range indexes {
		switch {
		case i < sep && !v.detail.hl[i]:
			t.Errorf("parent rune %d (%q) not highlighted", i, string([]rune(label)[i]))
		case i > sep && !v.label.hl[i-sep-1]:
			t.Errorf("name rune %d (%q) not highlighted", i, string([]rune(label)[i]))
		}
	}
	if maskedRunes(v.label.text, v.label.hl) == "" || maskedRunes(v.detail.text, v.detail.hl) == "" {
		t.Errorf("highlights = %q / %q, want matches in both parts", maskedRunes(v.detail.text, v.detail.hl), maskedRunes(v.label.text, v.label.hl))
	}
}

// TestHighlight_ControlCharLabelMasksDisplayedRunes proves a label carrying
// control characters or escape sequences still matches on its raw text
// (filtering reads the candidate data, unchanged) but is shown plain, with
// its highlight scored on the displayed runes: the mask is exactly as long
// as the shown text and marks the matched word, never a shifted one.
func TestHighlight_ControlCharLabelMasksDisplayedRunes(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m = m.withPresentation(func(p *config.Presentations) {
		p.Custom = map[string]config.RowPresentation{"prs": {Label: "{{.Label}}"}}
	})
	for _, tc := range []struct {
		name, query   string
		cand          source.Candidate
		shown, marked string
	}{
		{"custom label with a tab and BEL", "fix", source.Candidate{Source: "prs", Label: "PR\t42\x07 fix"}, "PR 42 fix", "fix"},
		{"agent title with CR and an erase", "parser", source.Candidate{Source: config.SourceAgents, Label: "Fix\r\x1b[2Jparser"}, "Fix parser", "parser"},
		{"label colored by its source", "alert", source.Candidate{Source: "prs", Label: "\x1b[31mred\x1b[0m alert"}, "red alert", "alert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.query = tc.query
			rows := buildRows(rowBuildInput{query: tc.query, candidates: []source.Candidate{tc.cand}, ranked: true})
			if len(rows) != 1 || rows[0].Match != MatchDirect {
				t.Fatalf("setup: query %q must keep a direct row for %q, got %+v", tc.query, tc.cand.Label, rows)
			}
			v := m.buildRowView(rows[0])
			if v.label.text != tc.shown {
				t.Fatalf("shown label = %q, want %q", v.label.text, tc.shown)
			}
			if len(v.label.hl) != len([]rune(v.label.text)) {
				t.Fatalf("mask covers %d runes, the shown label has %d", len(v.label.hl), len([]rune(v.label.text)))
			}
			if got := maskedRunes(v.label.text, v.label.hl); got != tc.marked {
				t.Errorf("highlighted %q of %q, want %q", got, v.label.text, tc.marked)
			}
		})
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

	s := newPalette(testTheme(ThemeMocha), nil)
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
