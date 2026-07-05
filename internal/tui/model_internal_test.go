package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// stubRenderer returns a fixed Result; used where only "a Renderer is wired"
// matters, not its output.
type stubRenderer struct{}

func (stubRenderer) Render(context.Context, source.Candidate, preview.RenderOptions) (preview.Result, error) {
	return preview.Result{Text: "x"}, nil
}

type contextCheckingRenderer struct{}

func (contextCheckingRenderer) Render(ctx context.Context, _ source.Candidate, _ preview.RenderOptions) (preview.Result, error) {
	return preview.Result{}, ctx.Err()
}

func internalTestCands() []source.Candidate {
	return []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "a"},
		{Path: "/b", NormalizedPath: "/b", Label: "b"},
	}
}

// TestModel_CursorMoveIncrementsPreviewSeq (PL-11) proves the cursor-move
// handler actually mutates previewSeq and returns a non-nil render Cmd when
// the highlighted candidate changes, instead of silently no-op'ing.
func TestModel_CursorMoveIncrementsPreviewSeq(t *testing.T) {
	m := NewModel(internalTestCands(), stubRenderer{})
	beforeSeq := m.previewSeq

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.previewSeq == beforeSeq {
		t.Fatalf("expected previewSeq to change after cursor move, got %d (before %d)", mm.previewSeq, beforeSeq)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil preview request Cmd after cursor move")
	}
}

// TestModel_StaleResponseIgnored (PL-11) is the core anti-regression test for
// stale async previews: a previewResponseMsg tagged with an older seq than
// the model's current previewSeq must never overwrite previewText, while a
// response tagged with the current seq must.
func TestModel_StaleResponseIgnored(t *testing.T) {
	m := NewModel(internalTestCands(), stubRenderer{})

	// Move the cursor so previewSeq advances past its initial value.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	staleSeq := m.previewSeq - 1

	// Current response updates state.
	updated, _ = m.Update(previewResponseMsg{seq: m.previewSeq, result: preview.Result{Text: "fresh"}})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if m.previewText != "fresh" {
		t.Fatalf("expected previewText %q, got %q", "fresh", m.previewText)
	}

	// A stale response (from before the cursor move) must be ignored.
	updated, _ = m.Update(previewResponseMsg{seq: staleSeq, result: preview.Result{Text: "stale"}})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if m.previewText != "fresh" {
		t.Errorf("stale response overwrote previewText: got %q, want %q", m.previewText, "fresh")
	}
}

// TestModel_RenderListRespectsWidth is the regression test for the
// palette/list width drift bug: renderList must pad every rendered line to
// the width it was asked to render at, not a style-level hardcoded width.
func TestModel_RenderListRespectsWidth(t *testing.T) {
	m := NewModel(internalTestCands(), nil)
	const width = 30
	out := m.renderList(width)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if got := lipgloss.Width(line); got != width {
			t.Errorf("line %q rendered width = %d, want %d", line, got, width)
		}
	}
}

// TestFinalizeRun_CancelledReturnsErrCancelled proves Run's tail logic
// returns the quiet ErrCancelled sentinel (not just ok=false) when the final
// model reports Cancelled(), so callers can distinguish "user cancelled"
// from "no selector available".
func TestFinalizeRun_CancelledReturnsErrCancelled(t *testing.T) {
	m := NewModel(internalTestCands(), nil)
	m.cancelled = true

	_, ok, err := finalizeRun(m)
	if ok {
		t.Error("expected ok=false when cancelled")
	}
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("expected ErrCancelled, got %v", err)
	}
}

// TestFinalizeRun_SelectedReturnsCandidateNilError proves a normal enter
// selection still returns (candidate, true, nil) with finalizeRun in place.
func TestFinalizeRun_SelectedReturnsCandidateNilError(t *testing.T) {
	m := NewModel(internalTestCands(), nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	cand, ok, err := finalizeRun(m)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a selected candidate")
	}
	if cand.Label != "a" {
		t.Errorf("candidate = %+v, want label %q", cand, "a")
	}
}

// TestModel_RenderListRowCapAccountsForChromeRows (PR1: TUI borders) proves
// renderList caps visible candidate rows using height - chromeRows (border
// top/bottom + query line), not the raw reported terminal height, so the
// border and query line never push the last row off-screen. Scrolled to the
// bottom of 50 candidates at height=10, the last candidate must still be
// reachable within the clamped viewport.
func TestModel_RenderListRowCapAccountsForChromeRows(t *testing.T) {
	cands := make([]source.Candidate, 50)
	for i := range cands {
		cands[i] = source.Candidate{Path: fmt.Sprintf("/c/%d", i), Label: fmt.Sprintf("c%d", i)}
	}
	m := NewModel(cands, nil)
	m.height = 10
	m.cursor = len(cands) - 1 // scrolled to the bottom

	out := m.renderList(30)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	visibleRows := len(lines) - 1 // first line is the query line
	wantMax := 10 - chromeRows
	if visibleRows > wantMax {
		t.Errorf("visible rows = %d, want <= %d (height=10, chromeRows=%d)", visibleRows, wantMax, chromeRows)
	}
	if !strings.Contains(out, "c49") {
		t.Error("expected the last candidate to be reachable when scrolled to bottom")
	}
}

// TestModel_PreviewCmdUsesRunContext proves async preview renders use the
// caller context threaded into Run/newModel, not a fresh background context.
func TestModel_PreviewCmdUsesRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := newModel(internalTestCands(), contextCheckingRenderer{}, ctx)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected preview command")
	}
	msg, ok := cmd().(previewResponseMsg)
	if !ok {
		t.Fatalf("expected previewResponseMsg, got %T", msg)
	}
	if msg.err != context.Canceled {
		t.Fatalf("preview err: got %v want %v", msg.err, context.Canceled)
	}
}
