package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/source"
)

// TestKindPrefix_ActiveFocusRow_MarksMatchingPane proves a RowPane whose
// pane_id matches m.currentPane's ID gets a visible active-focus marker in
// its kindPrefix — the picker can never truthfully claim to focus an exact
// pane on Enter (Herdr has no per-pane focus command), but it CAN show the
// user which row already identifies where shep is currently running.
func TestKindPrefix_ActiveFocusRow_MarksMatchingPane(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.currentPane = &source.Pane{ID: "p1", TabID: "t1"}
	row := Row{Kind: RowPane, Candidate: source.Candidate{Meta: map[string]string{"pane_id": "p1", "tab_id": "t1"}}}
	set := m.icons()
	got := m.kindPrefix(row)
	if !strings.Contains(got, set.ActiveMarker) {
		t.Errorf("kindPrefix(matching pane) = %q, want it to contain the active marker %q", got, set.ActiveMarker)
	}
}

// TestKindPrefix_ActiveFocusRow_MarksContainingTab proves a RowTab whose
// tab_id matches m.currentPane's TabID gets the active marker too — the
// containing tab of the pane shep is running inside.
func TestKindPrefix_ActiveFocusRow_MarksContainingTab(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.currentPane = &source.Pane{ID: "p1", TabID: "t1"}
	row := Row{Kind: RowTab, Candidate: source.Candidate{Meta: map[string]string{"tab_id": "t1"}}}
	set := m.icons()
	got := m.kindPrefix(row)
	if !strings.Contains(got, set.ActiveMarker) {
		t.Errorf("kindPrefix(containing tab) = %q, want it to contain the active marker %q", got, set.ActiveMarker)
	}
}

// TestKindPrefix_ActiveFocusRow_NoMarkerWhenNotMatching triangulates: a
// pane/tab row whose ids do NOT match the current pane gets no marker.
func TestKindPrefix_ActiveFocusRow_NoMarkerWhenNotMatching(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.currentPane = &source.Pane{ID: "p1", TabID: "t1"}
	set := m.icons()

	otherPane := Row{Kind: RowPane, Candidate: source.Candidate{Meta: map[string]string{"pane_id": "p2", "tab_id": "t2"}}}
	if got := m.kindPrefix(otherPane); strings.Contains(got, set.ActiveMarker) {
		t.Errorf("kindPrefix(non-matching pane) = %q, must not contain the active marker", got)
	}

	otherTab := Row{Kind: RowTab, Candidate: source.Candidate{Meta: map[string]string{"tab_id": "t2"}}}
	if got := m.kindPrefix(otherTab); strings.Contains(got, set.ActiveMarker) {
		t.Errorf("kindPrefix(non-matching tab) = %q, must not contain the active marker", got)
	}
}

// TestKindPrefix_ActiveFocusRow_NoCurrentPaneNeverMarks proves a nil
// m.currentPane (not running inside Herdr, or the probe failed) never marks
// any row, regardless of the row's own ids.
func TestKindPrefix_ActiveFocusRow_NoCurrentPaneNeverMarks(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()
	row := Row{Kind: RowPane, Candidate: source.Candidate{Meta: map[string]string{"pane_id": "p1"}}}
	if got := m.kindPrefix(row); strings.Contains(got, set.ActiveMarker) {
		t.Errorf("kindPrefix with nil currentPane = %q, must not contain the active marker", got)
	}
}

// TestKindPrefix_ActiveFocusRow_CandidateRowsNeverMark proves a RowCandidate
// (top-level workspace row) never gets the active marker even if it happens
// to carry a workspace_id — the active-focus indicator is specifically for
// tab/pane rows (the scope this feature covers), not top-level candidates.
func TestKindPrefix_ActiveFocusRow_CandidateRowsNeverMark(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.currentPane = &source.Pane{ID: "p1", TabID: "t1", WorkspaceID: "w1"}
	set := m.icons()
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Meta: map[string]string{"workspace_id": "w1"}}}
	if got := m.kindPrefix(row); strings.Contains(got, set.ActiveMarker) {
		t.Errorf("kindPrefix(RowCandidate) = %q, must not contain the active marker", got)
	}
}

// TestKindPrefix_TreeGlyphColumnAlignsRegardlessOfActiveMarker proves the
// active-focus marker's leading slot is a FIXED width on every RowTab/
// RowPane row (TRL-4): a sibling row where isActiveFocusRow is false must
// reserve the exact same visual width for that slot (as blank space) as a
// row where it is true, so the tree glyph (├─/└─) that follows always
// starts at the same column across siblings — instead of only the one
// active row shifting its OWN tree glyph rightward relative to every other
// sibling. Measured with lipgloss.Width (not len/byte-count or rune-count)
// because IconSet.ActiveMarker is a multi-byte Unicode glyph ("◆") whose
// byte length does not equal its terminal cell width.
func TestKindPrefix_TreeGlyphColumnAlignsRegardlessOfActiveMarker(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.currentPane = &source.Pane{ID: "p1", TabID: "t1"}
	set := m.icons()

	active := Row{Kind: RowTab, Candidate: source.Candidate{Meta: map[string]string{"tab_id": "t1"}}}
	sibling := Row{Kind: RowTab, Candidate: source.Candidate{Meta: map[string]string{"tab_id": "t2"}}, IsLast: true}

	activePrefix := m.kindPrefix(active)
	siblingPrefix := m.kindPrefix(sibling)

	if !strings.Contains(activePrefix, set.ActiveMarker) {
		t.Fatalf("active row kindPrefix = %q, want it to contain the active marker %q", activePrefix, set.ActiveMarker)
	}
	if strings.Contains(siblingPrefix, set.ActiveMarker) {
		t.Fatalf("sibling row kindPrefix = %q, must not contain the active marker", siblingPrefix)
	}

	if got, want := lipgloss.Width(activePrefix), lipgloss.Width(siblingPrefix); got != want {
		t.Errorf("active row prefix width = %d (%q), sibling prefix width = %d (%q): tree glyph column misaligned", got, activePrefix, want, siblingPrefix)
	}

	// The tree glyph itself (everything after the fixed active-marker slot)
	// must be identical between the two rows: the active row differs ONLY
	// in its leading slot content, never in the tree glyph placement.
	activeTreeGlyph := strings.TrimPrefix(activePrefix, set.ActiveMarker+" ")
	siblingBlankSlot := strings.Repeat(" ", lipgloss.Width(set.ActiveMarker+" "))
	siblingTreeGlyph := strings.TrimPrefix(siblingPrefix, siblingBlankSlot)
	if activeTreeGlyph != set.TreeMid+" " {
		t.Errorf("active row tree glyph = %q, want %q", activeTreeGlyph, set.TreeMid+" ")
	}
	if siblingTreeGlyph != set.TreeLast+" " {
		t.Errorf("sibling row tree glyph = %q, want %q", siblingTreeGlyph, set.TreeLast+" ")
	}
}
