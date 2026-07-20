package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/source"
)

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
	if got, want := parts[0].style.Render(parts[0].text), m.styles.rowStyle.Render("/home/dev/café"); got != want {
		t.Errorf("base row styling = %q, want %q", got, want)
	}
}

func TestHighlight_MatchedRunesUseAccentStyle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "dev" // the visible provider path has the only meaningful match.
	row := Row{
		Kind:           RowCandidate,
		Candidate:      zoxideCandidate("alpha", "/home/dev/alpha"),
		Match:          MatchDirect,
		MatchedIndexes: []int{0, 1, 2},
	}

	parts := m.rowLineParts(row)
	if len(parts) != 1 {
		t.Fatalf("part count = %d, want 1", len(parts))
	}
	if !parts[0].rendered {
		t.Fatal("matched runes were not rendered with per-rune styling")
	}

	base := m.styles.rowStyle
	want := base.Render("/home/") +
		m.styles.queryStyle.Render("d") +
		m.styles.queryStyle.Render("e") +
		m.styles.queryStyle.Render("v") +
		base.Render("/alpha")
	// renderHighlighted (not a pre-rendered parts[0].text — rendering is now
	// deferred to display time so truncation can protect the fixed prefix;
	// see rowPart.renderHighlighted) with maxW<=0 renders the full,
	// untruncated content.
	if got := parts[0].renderHighlighted(0, lipgloss.Style{}, false); got != want {
		t.Errorf("rendered matched runes = %q, want %q", got, want)
	}
}

func TestHighlight_LabelOnlyMatchDoesNotHighlightProviderPath(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "café" // matches the hidden provider label, never the visible path.
	cand := zoxideCandidate("café", "/home/dev/project")
	rows := buildRows(rowBuildInput{query: m.query, candidates: []source.Candidate{cand}})
	if len(rows) != 1 || rows[0].Match != MatchDirect {
		t.Fatalf("setup: label-only query must keep a direct candidate row, got %+v", rows)
	}

	parts := m.rowLineParts(rows[0])
	if len(parts) != 1 {
		t.Fatalf("part count = %d, want 1", len(parts))
	}
	if parts[0].rendered {
		t.Fatal("a label-only match must not paint unrelated runes in the visible provider path")
	}
	if got, want := parts[0].style.Render(parts[0].text), m.styles.rowStyle.Render("/home/dev/project"); got != want {
		t.Errorf("label-only match provider rendering: got %q, want %q", got, want)
	}
}

// TestHighlight_ProviderPathMatchUsesVisiblePathIndexes proves highlighting
// rescoring follows the text rendered for an ordinary provider candidate,
// rather than the hidden label or the label+path fuzzy haystack.
func TestHighlight_ProviderPathMatchUsesVisiblePathIndexes(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.query = "omp"
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
	// rawText excludes the one-cell external gutter. The visible path's "omp"
	// occupies indexes 11, 12, and 13.
	wantHighlighted := map[int]bool{11: true, 12: true, 13: true}
	for i, got := range parts[0].highlighted {
		if want := wantHighlighted[i]; got != want {
			t.Errorf("highlighted[%d] (rune %q) = %v, want %v — rawText=%q", i, string(rawRunes[i]), got, want, parts[0].rawText)
		}
	}
	if got, want := string(rawRunes[11:14]), "omp"; got != want {
		t.Fatalf("setup sanity: rawText[11:14] = %q, want %q", got, want)
	}
	if !strings.HasPrefix(parts[0].rawText, "/home/dev/components") {
		t.Errorf("rawText = %q, want it to contain only the visible provider path", parts[0].rawText)
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
			wantText:  "/home/dev/workspace",
			wantStyle: m.styles.rowDescendantStyle.Render("/home/dev/workspace"),
		},
		{
			name:      "non-match",
			match:     MatchNone,
			wantText:  "/home/dev/workspace",
			wantStyle: m.styles.rowStyle.Render("/home/dev/workspace"),
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
