package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

func TestAgentPresentation_SharedLeftTruncationAndHighlight(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "sec"
	row := Row{Kind: RowCandidate, Match: MatchDirect, MatchedIndexes: []int{0, 1, 2}, Candidate: source.Candidate{
		Source: config.SourceAgents, Icon: "X ", Label: "security scan long title", Meta: map[string]string{"agent_status": "idle"},
	}}
	parts := m.rowLineParts(row)
	if !parts[0].rendered {
		t.Fatal("agent match did not enable highlighting")
	}
	prefix := "X  " + m.agentStatusIcon("idle") + " "
	width := len([]rune(prefix)) + 11 + cursorPrefixWidth
	for _, cursor := range []bool{false, true} {
		got := renderRowLineText(m.renderRowLine(row, cursor, width))
		if !strings.Contains(got, prefix+"…long title") {
			t.Errorf("cursor=%v rendered %q; want prefix and label ending with leading ellipsis", cursor, got)
		}
	}
	row.Kind = RowPane // the agents tab derives pane rows from the snapshot
	paneParts := m.rowLineParts(row)
	if !paneParts[0].rendered {
		t.Fatal("agents tab pane match did not enable highlighting")
	}
	if got := renderRowLineText(m.renderRowLine(row, true, width)); !strings.Contains(got, prefix+"…long title") {
		t.Errorf("truncated agents tab cursor row = %q", got)
	}
	// A match discarded by left truncation cannot retain its accent; a
	// surviving match in the title suffix must still be accented.
	m.query = "title"
	row.MatchedIndexes = []int{19, 20, 21, 22, 23}
	parts = m.rowLineParts(Row{Kind: RowCandidate, Match: row.Match, MatchedIndexes: row.MatchedIndexes, Candidate: row.Candidate})
	accent := m.styles.queryStyle.Render("t")
	if got := parts[0].renderHighlighted(width-cursorPrefixWidth, lipgloss.Style{}, false); !strings.Contains(got, accent) {
		t.Errorf("truncated highlighted row %q lost surviving accented match %q", got, accent)
	}
	// Identical prefixes isolate the truncation algorithm from status styling.
	other := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Icon: "X ", Label: "security scan long title"}}
	agent := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceAgents, Icon: "X ", Label: other.Candidate.Label}}
	for _, cursor := range []bool{false, true} {
		got := renderRowLineText(m.renderRowLine(agent, cursor, width))
		want := renderRowLineText(m.renderRowLine(other, cursor, width))
		if got != want || !strings.Contains(got, "X  …") || !strings.HasSuffix(strings.TrimSpace(got), "title") {
			t.Errorf("cursor=%v agent row = %q, other row = %q; want equal left truncation", cursor, got, want)
		}
	}
}

func TestHighlight_EmptyMatchedIndexesUsesBaseStyle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{
		Kind:      RowCandidate,
		Candidate: zoxideCandidate("café", "/home/dev/café"),
		Match:     MatchDirect,
	}

	parts := m.rowLineParts(row)
	if len(parts) != 1 {
		t.Fatalf("part count = %d, want 1", len(parts))
	}
	if parts[0].rendered {
		t.Fatal("empty matched indexes rendered per-rune styling")
	}
	if got, want := parts[0].style.Render(parts[0].text), m.styles.rowStyle.Render("café"); got != want {
		t.Errorf("base row styling = %q, want %q", got, want)
	}
}

func TestHighlight_MatchedRunesUseAccentStyle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "ha" // the visible provider label has the only meaningful match.
	row := Row{
		Kind:           RowCandidate,
		Candidate:      zoxideCandidate("alpha", "/home/dev/alpha"),
		Match:          MatchDirect,
		MatchedIndexes: []int{0, 1},
	}

	parts := m.rowLineParts(row)
	if len(parts) != 1 {
		t.Fatalf("part count = %d, want 1", len(parts))
	}
	if !parts[0].rendered {
		t.Fatal("matched runes were not rendered with per-rune styling")
	}

	// Rows are label-first now, so the visible text is the label "alpha" rather
	// than the path, and the query accents the two runes it matches there. The
	// point of this test is that those runes render with the accent style while
	// the rest keeps the base style; asserting a single base-styled string would
	// pass while proving nothing, because an uncolored profile renders every
	// style identically.
	base := m.styles.rowStyle
	accent := m.styles.queryStyle
	want := base.Render("a") + base.Render("l") + base.Render("p") +
		accent.Render("h") + accent.Render("a")
	// renderHighlighted (not a pre-rendered parts[0].text — rendering is now
	// deferred to display time so truncation can protect the fixed prefix;
	// see rowPart.renderHighlighted) with maxW<=0 renders the full,
	// untruncated content.
	if got := parts[0].renderHighlighted(0, lipgloss.Style{}, false); got != want {
		t.Errorf("rendered matched runes = %q, want %q", got, want)
	}
}

// TestHighlight_LabelMatchHighlightsTheVisibleLabel covers a query that matches
// only the label. Rows are label-first now, so the label IS the visible text and
// the match must be accented there. The old name said the opposite, describing
// the era when a provider row rendered its path and a label-only match had no
// rendered rune to accent.
func TestHighlight_LabelMatchHighlightsTheVisibleLabel(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "café" // matches the visible provider label.
	cand := zoxideCandidate("café", "/home/dev/project")
	rows := buildRows(rowBuildInput{query: m.query, candidates: []source.Candidate{cand}})
	if len(rows) != 1 || rows[0].Match != MatchDirect {
		t.Fatalf("setup: label-only query must keep a direct candidate row, got %+v", rows)
	}

	parts := m.rowLineParts(rows[0])
	if len(parts) != 1 {
		t.Fatalf("part count = %d, want 1", len(parts))
	}
	if !parts[0].rendered {
		t.Fatal("a label match should highlight the visible label")
	}
	if got := parts[0].rawText; got != "café" {
		t.Errorf("label-only match rendering: got %q, want %q", got, "café")
	}
}

// TestHighlight_MatchUsesVisibleRowIndexes proves highlighting rescoring follows
// the text the row actually renders for an ordinary provider candidate, rather
// than the label+path fuzzy haystack used for ranking.
func TestHighlight_MatchUsesVisibleRowIndexes(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "com"
	cand := zoxideCandidate("components", "/home/dev/components")

	row := Row{
		Kind:           RowCandidate,
		Candidate:      cand,
		Match:          MatchDirect,
		MatchedIndexes: []int{0, 1, 2},
	}

	parts := m.rowLineParts(row)
	if len(parts) != 1 {
		t.Fatalf("part count = %d, want 1", len(parts))
	}
	if !parts[0].rendered {
		t.Fatal("visible provider-path match was not highlighted")
	}

	rawRunes := []rune(parts[0].rawText)
	// rawText excludes the one-cell external gutter. The visible label's "com"
	// occupies indexes 0, 1, and 2.
	wantHighlighted := map[int]bool{0: true, 1: true, 2: true}
	for i, got := range parts[0].highlighted {
		if want := wantHighlighted[i]; got != want {
			t.Errorf("highlighted[%d] (rune %q) = %v, want %v — rawText=%q", i, string(rawRunes[i]), got, want, parts[0].rawText)
		}
	}
	if got, want := string(rawRunes[0:3]), "com"; got != want {
		t.Fatalf("setup sanity: rawText[11:14] = %q, want %q", got, want)
	}
	if !strings.HasPrefix(parts[0].rawText, "components") {
		t.Errorf("rawText = %q, want it to contain only the visible provider label", parts[0].rawText)
	}
}

func TestHighlight_NonDirectMatchesUseNoQueryStyle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)

	tests := []struct {
		name      string
		match     MatchKind
		wantText  string
		wantStyle string
	}{
		{
			name:      "descendant match",
			match:     MatchDescendant,
			wantText:  "workspace",
			wantStyle: m.styles.rowDescendantStyle.Render("workspace"),
		},
		{
			name:      "non-match",
			match:     MatchNone,
			wantText:  "workspace",
			wantStyle: m.styles.rowStyle.Render("workspace"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := Row{
				Kind:           RowCandidate,
				Candidate:      zoxideCandidate("workspace", "/home/dev/workspace"),
				Match:          tt.match,
				MatchedIndexes: []int{0, 2},
			}

			parts := m.rowLineParts(row)
			if len(parts) != 1 {
				t.Fatalf("part count = %d, want 1", len(parts))
			}
			if parts[0].rendered {
				t.Fatal("non-direct row rendered with query accent styling")
			}
			if got := parts[0].text; got != tt.wantText {
				t.Errorf("row text = %q, want %q", got, tt.wantText)
			}
			if got := parts[0].style.Render(parts[0].text); got != tt.wantStyle {
				t.Errorf("row style = %q, want %q", got, tt.wantStyle)
			}
		})
	}
}
