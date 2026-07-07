package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

// A small candidate set reused across tests. Paths are synthetic so the test
// does not depend on a real filesystem layout.
func testCandidates() []source.Candidate {
	return []source.Candidate{
		{Path: "/code/shep", NormalizedPath: "/code/shep", Label: "shep", Source: "herdr"},
		{Path: "/code/shep-docs", NormalizedPath: "/code/shep-docs", Label: "shep-docs", Source: "herdr"},
		{Path: "/code/zoxide", NormalizedPath: "/code/zoxide", Label: "zoxide", Source: "zoxide"},
	}
}

// startTUI builds a teatest TestModel from a fresh tui.Model so we don't need
// a real TTY. Cleanup quits the underlying program. renderer may be nil (the
// picker degrades to its built-in candidate summary, no async preview).
func startTUI(t *testing.T, cands []source.Candidate, renderer preview.Renderer) *teatest.TestModel {
	t.Helper()
	tm := teatest.NewTestModel(t, tui.NewModel(cands, renderer),
		teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() {
		_ = tm.Quit()
	})
	return tm
}

// labelRenderer is a deterministic, side-effect-free preview.Renderer stub:
// it renders "preview-for-<label>" instantly so tests can assert the picker
// requested (and displayed) a preview for a specific candidate.
type labelRenderer struct{}

func (labelRenderer) Render(_ context.Context, cand source.Candidate) (preview.Result, error) {
	return preview.Result{Text: "preview-for-" + cand.Label}, nil
}

// gatedRenderer blocks Render until release is closed, letting tests observe
// the loading state before the async result arrives.
type gatedRenderer struct {
	release chan struct{}
}

func (g *gatedRenderer) Render(_ context.Context, cand source.Candidate) (preview.Result, error) {
	<-g.release
	return preview.Result{Text: "rendered:" + cand.Label}, nil
}

func finalModel(t *testing.T, tm *teatest.TestModel) tui.Model {
	t.Helper()
	fm := tm.FinalModel(t)
	if fm == nil {
		t.Fatal("expected a final model, got nil")
	}
	m, ok := fm.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", fm)
	}
	return m
}

// TestTUI_EnterSelectsFirstCandidate: pressing enter on the initial cursor
// (first candidate) selects "shep".
func TestTUI_EnterSelectsFirstCandidate(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	// Let the first frame render, then press enter.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection, got none")
	}
	if pick.Label != "shep" {
		t.Errorf("selected %q, want shep", pick.Label)
	}
	if m.Cancelled() {
		t.Error("model reports cancelled after enter")
	}
}

// TestTUI_DownThenEnterSelectsSecond: j/down moves the cursor and enter
// commits the second candidate.
func TestTUI_DownThenEnterSelectsSecond(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection, got none")
	}
	if pick.Label != "shep-docs" {
		t.Errorf("selected %q, want shep-docs", pick.Label)
	}
}

// TestTUI_TypeQueryFilters: typing filters candidates; with "sdocs" entered
// as the query, only "shep-docs" matches (as a fuzzy subsequence — sahilm/
// fuzzy does not require a contiguous substring), and enter selects it.
//
// The query is "sdocs", not the more obviously-named "docs", specifically so
// that the very FIRST keystroke ('s') already excludes "zoxide" outright:
// none of zoxide's letters (z,o,x,i,d,e) is 's'. Typing "docs" instead has a
// narrow but real transient-state hazard: after only the first keystroke
// ('d'), the query "d" is a subsequence match for BOTH "shep-docs" and
// "zoxide" (zoxide does contain the letter d) — a single-character render
// frame that teatest.WaitFor's condition can observe if it happens to poll
// right then, permanently failing the "!contains(zoxide)" assertion for the
// rest of that WaitFor call (its accumulator only ever grows, never resets
// mid-call). That window is normally too narrow to ever get its own
// rendered frame, but the preview/list panes now redraw at their full fixed
// outer height on every keystroke (see paneBoxStyle) — a larger per-frame
// ANSI payload than before that fix — which under `go test -race` was
// enough to make that one-keystroke frame independently observable and
// intermittently fail this test. "sdocs" removes the hazard at its root
// instead of racing the render pipeline: zoxide can never validly match any
// prefix of it.
func TestTUI_TypeQueryFilters(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Type("sdocs")
	// Wait for the filter to drop "zoxide" from the visible list.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "shep-docs") && !strings.Contains(s, "zoxide")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after query+enter, got none")
	}
	if pick.Label != "shep-docs" {
		t.Errorf("selected %q, want shep-docs", pick.Label)
	}
}

// TestTUI_EscCancels: pressing esc sets Cancelled and clears the selection.
func TestTUI_EscCancels(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})

	m := finalModel(t, tm)
	if !m.Cancelled() {
		t.Error("expected cancelled=true after esc")
	}
	if _, ok := m.Selected(); ok {
		t.Error("selection should be empty after esc")
	}
}

// TestTUI_CtrlGCancels: pressing ctrl+g cancels just like esc, so users
// stuck without an Escape key (some terminals/remote sessions) have a
// working cancel binding.
func TestTUI_CtrlGCancels(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlG})

	m := finalModel(t, tm)
	if !m.Cancelled() {
		t.Error("expected cancelled=true after ctrl+g")
	}
	if _, ok := m.Selected(); ok {
		t.Error("selection should be empty after ctrl+g")
	}
}

// TestTUI_KMovesUp and does not underflow the cursor.
func TestTUI_KMovesUpWithoutUnderflow(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	// Cursor sits at 0; pressing k should keep it at 0 so enter still selects
	// the first candidate.
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection, got none")
	}
	if pick.Label != "shep" {
		t.Errorf("selected %q, want shep (k must not underflow)", pick.Label)
	}
}

// TestTUI_CursorMoveRequestsNewPreview (PL-11): moving the cursor to a new
// candidate enqueues an async preview render for that candidate and the
// rendered pane updates to show it, proving the request is real (not a
// static label/path/source dump).
func TestTUI_CursorMoveRequestsNewPreview(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, labelRenderer{})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "preview-for-shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyDown})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "preview-for-shep-docs")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
}

// TestTUI_ShowsLoadingIndicatorBeforePreviewResolves (PL-11): while a preview
// render is in flight, the pane shows a loading indicator instead of stale or
// blank content, and cursor movement is not blocked by the pending render.
func TestTUI_ShowsLoadingIndicatorBeforePreviewResolves(t *testing.T) {
	cands := testCandidates()
	release := make(chan struct{})
	renderer := &gatedRenderer{release: release}
	tm := startTUI(t, cands, renderer)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "loading")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	close(release)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "rendered:shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
}

// TestModel_ViewHidesPreviewBelowMinWidth (PL-11): a terminal narrower than
// 80 columns hides the preview panel entirely to avoid breaking the layout.
func TestModel_ViewHidesPreviewBelowMinWidth(t *testing.T) {
	m := tui.NewModel(testCandidates(), nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 75, Height: 24})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", updated)
	}
	view := mm.View()
	// nil renderer degrades the preview pane to a "label  <value>" summary
	// line (see previewBody), a stable marker for "preview pane is shown"
	// that does not depend on the (now removed) "preview" header text.
	if strings.Contains(view, "label") {
		t.Errorf("expected preview pane hidden at width 75, got:\n%s", view)
	}
}

// TestModel_ViewShowsPreviewAtMinWidth (PL-11): at exactly 80 columns the
// preview panel remains visible (only widths strictly below 80 hide it).
func TestModel_ViewShowsPreviewAtMinWidth(t *testing.T) {
	m := tui.NewModel(testCandidates(), nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", updated)
	}
	view := mm.View()
	if !strings.Contains(view, "label") {
		t.Errorf("expected preview pane visible at width 80, got:\n%s", view)
	}
}

// TestModel_ViewHidesPreviewBelowMinHeight: a terminal shorter than 8 rows
// hides the preview panel entirely, mirroring the narrow-width rule, so a
// very short terminal never breaks the layout.
func TestModel_ViewHidesPreviewBelowMinHeight(t *testing.T) {
	m := tui.NewModel(testCandidates(), nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 7})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", updated)
	}
	view := mm.View()
	if strings.Contains(view, "label") {
		t.Errorf("expected preview pane hidden at height 7, got:\n%s", view)
	}
}

// TestModel_ViewShowsPreviewAtMinHeight: at exactly 8 rows the preview panel
// remains visible (only heights strictly below 8 hide it).
func TestModel_ViewShowsPreviewAtMinHeight(t *testing.T) {
	m := tui.NewModel(testCandidates(), nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 8})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", updated)
	}
	view := mm.View()
	if !strings.Contains(view, "label") {
		t.Errorf("expected preview pane visible at height 8, got:\n%s", view)
	}
}

// TestModel_ViewRendersRoundedBorders (PR1: TUI borders) proves the list and
// preview panes are each wrapped in a rounded Lip Gloss border, not plain
// unframed text blocks.
func TestModel_ViewRendersRoundedBorders(t *testing.T) {
	m := tui.NewModel(testCandidates(), nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", updated)
	}
	view := mm.View()
	if !strings.Contains(view, "╭") {
		t.Errorf("expected rounded border corners in view, got:\n%s", view)
	}
}

// TestModel_ViewFitsWithinReportedWidth (PR1: TUI borders) proves the
// border+padding chrome is subtracted from the reported terminal width, so
// no rendered line overflows the terminal.
func TestModel_ViewFitsWithinReportedWidth(t *testing.T) {
	m := tui.NewModel(testCandidates(), nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	mm, ok := updated.(tui.Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", updated)
	}
	view := mm.View()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 100 {
			t.Errorf("line width %d exceeds terminal width 100: %q", w, line)
		}
	}
}

// TestTUI_CtrlJMovesCursorDownSelectsSecond (PR2: ctrl+j navigation): Ctrl+j
// moves the cursor down exactly like arrow-down used to, so enter commits the
// second candidate instead of the first.
func TestTUI_CtrlJMovesCursorDownSelectsSecond(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after ctrl+j+enter, got none")
	}
	if pick.Label != "shep-docs" {
		t.Errorf("selected %q, want shep-docs (ctrl+j must move down)", pick.Label)
	}
}

// TestTUI_CtrlJAtBottomDoesNotOverflow (PR2: ctrl+j navigation): pressing
// ctrl+j past the last candidate keeps the cursor clamped at the last entry,
// so enter still selects the last one rather than skipping off the list.
func TestTUI_CtrlJAtBottomDoesNotOverflow(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	// Three candidates (indices 0,1,2): press ctrl+j four times; the extra
	// press at the bottom must NOT advance the cursor past zoxide.
	for i := 0; i < 4; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyCtrlJ})
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after ctrl+j overflow attempt, got none")
	}
	if pick.Label != "zoxide" {
		t.Errorf("selected %q, want zoxide (ctrl+j must clamp at last)", pick.Label)
	}
}

// TestTUI_CtrlKMovesCursorUpSelectsFirst (PR2: ctrl+k navigation): position
// the cursor on the second candidate using the arrow-down key (which already
// works), then ctrl+k must move it back up; enter then commits the first
// candidate. Using arrow-down rather than ctrl+j to position isolates the
// ctrl+k assertion: if ctrl+k were a no-op, enter would select shep-docs
// (second), not shep (first).
func TestTUI_CtrlKMovesCursorUpSelectsFirst(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyDown})  // -> index 1
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlK}) // -> index 0 (only if ctrl+k moves up)
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after down/ctrl+k, got none")
	}
	if pick.Label != "shep" {
		t.Errorf("selected %q, want shep (ctrl+k must move up)", pick.Label)
	}
}

// TestTUI_CtrlKAtTopDoesNotUnderflow (PR2: ctrl+k navigation): pressing
// ctrl+k at the top of the list keeps the cursor at index 0, so enter selects
// the first candidate instead of dropping off.
func TestTUI_CtrlKAtTopDoesNotUnderflow(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	for i := 0; i < 4; i++ {
		tm.Send(tea.KeyMsg{Type: tea.KeyCtrlK})
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after ctrl+k underflow attempt, got none")
	}
	if pick.Label != "shep" {
		t.Errorf("selected %q, want shep (ctrl+k must not underflow)", pick.Label)
	}
}

// jkCandidates is a candidate set where one label contains "j" and another
// contains "k", so a query of "j" or "k" filters down to exactly one entry.
func jkCandidates() []source.Candidate {
	return []source.Candidate{
		{Path: "/code/alpha", NormalizedPath: "/code/alpha", Label: "alpha", Source: "herdr"},
		{Path: "/code/project", NormalizedPath: "/code/project", Label: "project", Source: "herdr"},
		{Path: "/code/monkey", NormalizedPath: "/code/monkey", Label: "monkey", Source: "zoxide"},
	}
}

// TestTUI_TypingJAppendsToQueryFilters (PR2: bare j/k no longer navigate): a
// plain "j" keystroke is appended to the query and filters the list down to
// the single candidate whose label contains "j" (project), instead of moving
// the cursor down.
func TestTUI_TypingJAppendsToQueryFilters(t *testing.T) {
	cands := jkCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "alpha")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Type("j")

	// Filter must drop alpha and monkey, leaving only project visible.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "project") &&
			!strings.Contains(s, "alpha") &&
			!strings.Contains(s, "monkey")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after typing j, got none")
	}
	if pick.Label != "project" {
		t.Errorf("selected %q, want project (j must filter, not navigate)", pick.Label)
	}
}

// TestTUI_TypingKAppendsToQueryFilters (PR2: bare j/k no longer navigate): a
// plain "k" keystroke is appended to the query and filters the list down to
// the single candidate whose label contains "k" (monkey).
func TestTUI_TypingKAppendsToQueryFilters(t *testing.T) {
	cands := jkCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "alpha")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Type("k")

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "monkey") &&
			!strings.Contains(s, "alpha") &&
			!strings.Contains(s, "project")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	m := finalModel(t, tm)
	pick, ok := m.Selected()
	if !ok {
		t.Fatal("expected a selection after typing k, got none")
	}
	if pick.Label != "monkey" {
		t.Errorf("selected %q, want monkey (k must filter, not navigate)", pick.Label)
	}
}
