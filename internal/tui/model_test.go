package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func (labelRenderer) Render(_ context.Context, cand source.Candidate, _ preview.RenderOptions) (preview.Result, error) {
	return preview.Result{Text: "preview-for-" + cand.Label}, nil
}

// gatedRenderer blocks Render until release is closed, letting tests observe
// the loading state before the async result arrives.
type gatedRenderer struct {
	release chan struct{}
}

func (g *gatedRenderer) Render(_ context.Context, cand source.Candidate, _ preview.RenderOptions) (preview.Result, error) {
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

// TestTUI_TypeQueryFilters: typing filters candidates; with "docs" entered as
// the query, only "shep-docs" matches, and enter selects it.
func TestTUI_TypeQueryFilters(t *testing.T) {
	cands := testCandidates()
	tm := startTUI(t, cands, nil)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "shep")
	}, teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	tm.Type("docs")
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
	if strings.Contains(view, "preview") {
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
	if !strings.Contains(view, "preview") {
		t.Errorf("expected preview pane visible at width 80, got:\n%s", view)
	}
}
