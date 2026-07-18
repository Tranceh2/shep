package tui

import (
	"strings"
	"testing"

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
