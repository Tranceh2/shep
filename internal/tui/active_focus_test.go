package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// The rows that hold the pane shep runs in — that pane, its tab and its
// open workspace — say so with a muted "current" accessory instead of a
// marker glyph in the tree prefix.

func currentPaneModel() Model {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.currentPane = &source.Pane{ID: "p1", TabID: "t1", WorkspaceID: "w1"}
	return m
}

// TestCurrentAccessory_MarksThePaneItsTabAndItsWorkspace proves the three
// rows that contain the current pane carry the accessory and their siblings
// do not.
func TestCurrentAccessory_MarksThePaneItsTabAndItsWorkspace(t *testing.T) {
	t.Parallel()
	m := currentPaneModel()
	for _, tc := range []struct {
		name string
		row  Row
		want bool
	}{
		{"current pane", Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"pane_id": "p1", "tab_id": "t1"}}}, true},
		{"sibling pane", Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"pane_id": "p2", "tab_id": "t1"}}}, false},
		{"containing tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}, true},
		{"other tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "web", Meta: map[string]string{"tab_id": "t2"}}}, false},
		{"containing workspace", Row{Kind: RowCandidate, Candidate: herdrCandidate("backend", "/srv/backend", "w1")}, true},
		{"other workspace", Row{Kind: RowCandidate, Candidate: herdrCandidate("frontend", "/srv/frontend", "w2")}, false},
		{"directory with the same id meta", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "x", Source: config.SourceZoxide, Meta: map[string]string{"workspace_id": "w1"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Contains(m.rowAccessoryText(tc.row), currentMarker); got != tc.want {
				t.Errorf("accessories = %q, want current=%v", m.rowAccessoryText(tc.row), tc.want)
			}
		})
	}
}

// TestCurrentAccessory_NoCurrentPaneNeverMarks proves nothing is marked
// before the current pane is known.
func TestCurrentAccessory_NoCurrentPaneNeverMarks(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}
	if got := m.rowAccessoryText(row); got != "" {
		t.Errorf("accessories = %q, want none without a current pane", got)
	}
}

// TestCurrentAccessory_RendersMutedAtTheRowEnd proves the accessory renders
// right-aligned (ASCII tier included) and that the tree prefix no longer
// reserves a marker slot: tree glyphs start right after the indent.
func TestCurrentAccessory_RendersMutedAtTheRowEnd(t *testing.T) {
	t.Parallel()
	for _, icons := range []string{IconsUnicode, IconsASCII} {
		m := currentPaneModel()
		m.layout.Icons = icons
		m = m.withPresentation(nil)
		set := m.icons()
		row := Row{Kind: RowTab, Depth: 1, IsLast: true, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}
		line := strings.TrimRight(ansi.Strip(m.renderRowLine(row, false, 40)), " ")
		if !strings.HasSuffix(line, " current") {
			t.Errorf("[%s] tab row = %q, want the current accessory at its end", icons, line)
		}
		if want := "    " + set.TreeLast + " " + defaultTabIcon(set.Name) + " api"; !strings.HasPrefix(line, want) {
			t.Errorf("[%s] tab row = %q, want the tree glyph right after the indent (%q)", icons, line, want)
		}
	}
}

// TestKindPrefix_TreeGlyphColumnsAlign proves sibling tab and pane rows put
// their tree glyphs in the same columns, the current pane included.
func TestKindPrefix_TreeGlyphColumnsAlign(t *testing.T) {
	t.Parallel()
	m := currentPaneModel()
	set := m.icons()
	tab := m.kindPrefix(Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Meta: map[string]string{"tab_id": "t1"}}})
	pane := m.kindPrefix(Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Meta: map[string]string{"pane_id": "p1"}}})
	other := m.kindPrefix(Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Meta: map[string]string{"pane_id": "p2"}}})
	if tab != "  "+set.TreeMid+" " {
		t.Errorf("tab prefix = %q", tab)
	}
	if pane != other || pane != "  "+set.TreeVertical+set.TreeMid+" " {
		t.Errorf("pane prefixes = %q / %q, want identical ancestor and branch columns", pane, other)
	}
}
