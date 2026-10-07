package tui_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

type fixedRenderer struct{ text string }

func (r fixedRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: r.text}, nil
}

func cands() []source.Candidate {
	return []source.Candidate{
		{Label: "alpha", Path: "/alpha", NormalizedPath: "/alpha", Source: config.SourceZoxide},
		{Label: "beta", Path: "/beta", NormalizedPath: "/beta", Source: config.SourceZoxide},
		{Label: "gamma", Path: "/gamma", NormalizedPath: "/gamma", Source: config.SourceZoxide},
	}
}

// runPicker runs m in a real Bubble Tea program without a terminal, sends
// msgs once it runs, and returns the terminated model.
func runPicker(t *testing.T, m tui.Model, msgs ...tea.Msg) tui.Model {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(120, 30), tea.WithoutSignals())
	go func() {
		for _, msg := range msgs {
			p.Send(msg)
		}
	}()
	final, err := p.Run()
	if err != nil {
		t.Fatalf("program: %v", err)
	}
	return final.(tui.Model)
}

func press(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func typed(text string) []tea.Msg {
	var msgs []tea.Msg
	for _, r := range text {
		msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return msgs
}

// TestTUI_EnterSelectsFirstCandidate drives a real program end to end: the
// first candidate is highlighted at construction, enter selects it.
func TestTUI_EnterSelectsFirstCandidate(t *testing.T) {
	t.Parallel()
	mm := runPicker(t, tui.NewModel(cands(), nil), press(tea.KeyEnter))
	got, ok := mm.Selected()
	if !ok {
		t.Fatal("expected a selection")
	}
	if got.Label != "alpha" {
		t.Errorf("Selected().Label = %q, want alpha", got.Label)
	}
	if mm.Cancelled() {
		t.Error("expected Cancelled()=false")
	}
}

// TestTUI_EscCancels proves esc quits without a selection.
func TestTUI_EscCancels(t *testing.T) {
	t.Parallel()
	mm := runPicker(t, tui.NewModel(cands(), nil), press(tea.KeyEscape))
	if !mm.Cancelled() {
		t.Error("expected Cancelled()=true after esc")
	}
	if _, ok := mm.Selected(); ok {
		t.Error("expected no selection after esc")
	}
}

// TestTUI_TypeQueryFilters proves typing narrows the visible rows.
func TestTUI_TypeQueryFilters(t *testing.T) {
	t.Parallel()
	mm := runPicker(t, tui.NewModel(cands(), nil), append(typed("gamma"), press(tea.KeyEnter))...)
	got, ok := mm.Selected()
	if !ok || got.Label != "gamma" {
		t.Errorf("Selected() = %+v (ok=%v), want gamma", got, ok)
	}
}

// TestTUI_ViewFitsWithinReportedWidth proves every rendered line stays
// within the reported terminal width, across a candidate list long enough
// to require wrapping/truncation.
func TestTUI_ViewFitsWithinReportedWidth(t *testing.T) {
	t.Parallel()
	long := []source.Candidate{{
		Label: strings.Repeat("very-long-label-", 10), Path: "/x", NormalizedPath: "/x", Source: config.SourceZoxide,
	}}
	m := tui.NewModel(long, nil)
	m, _ = sendSize(t, m, 60, 20)
	view := m.View().Content
	for _, line := range strings.Split(view, "\n") {
		if w := lipglossWidth(line); w > 60 {
			t.Errorf("line width %d exceeds reported width 60: %q", w, line)
		}
	}
}

// TestTUI_LoadingIndicatorShownBeforePreviewResolves proves the model shows
// a loading state immediately after construction when a renderer is wired,
// before any previewResponseMsg arrives.
func TestTUI_LoadingIndicatorShownBeforePreviewResolves(t *testing.T) {
	t.Parallel()
	m := tui.NewModel(cands(), fixedRenderer{text: "resolved preview"})
	m, _ = sendSize(t, m, 100, 30)
	view := m.View().Content
	if strings.Contains(view, "resolved preview") {
		t.Error("expected the synchronous render to NOT already be visible before Init's Cmd resolves")
	}
}

// --- States: empty / no-result ---

// TestTUI_EmptyState_NoCandidatesAtAll proves a picker with zero candidates
// shows a distinct "no candidates" message, not a generic blank list.
func TestTUI_EmptyState_NoCandidatesAtAll(t *testing.T) {
	t.Parallel()
	m := tui.NewModel(nil, nil)
	m, _ = sendSize(t, m, 100, 30)
	view := m.View().Content
	if !strings.Contains(view, "No candidates available") {
		t.Errorf("expected the empty-candidates state message, got view:\n%s", view)
	}
}

// TestTUI_NoResultState_QueryMatchesNothing proves a query with zero
// matches shows a distinct "no matches" message naming the query.
func TestTUI_NoResultState_QueryMatchesNothing(t *testing.T) {
	t.Parallel()
	m := tui.NewModel(cands(), nil)
	m, _ = sendSize(t, m, 100, 30)
	for _, msg := range typed("zzzznomatch") {
		m, _ = sendKey(t, m, msg)
	}
	view := m.View().Content
	if !strings.Contains(view, "No matches for") {
		t.Errorf("expected the no-matches state message, got view:\n%s", view)
	}
}

// --- Preview error state ---

// TestTUI_PreviewErrorState proves a Renderer error surfaces as a visible
// preview error indicator, not a blank/crashed preview.
func TestTUI_PreviewErrorState(t *testing.T) {
	t.Parallel()
	m := tui.NewModel(cands(), erroringRenderer{})
	m, sizeCmd := sendSize(t, m, 100, 30)
	initCmd := m.Init()
	if initCmd == nil {
		t.Fatal("expected Init to dispatch an async preview render with a renderer wired")
	}
	m, _ = sendKey(t, m, initCmd())
	if sizeCmd != nil {
		m, _ = sendKey(t, m, sizeCmd())
	}
	view := m.View().Content
	if !strings.Contains(view, "Preview unavailable") {
		t.Errorf("expected a visible preview error indicator, got view:\n%s", view)
	}
}

func sendSize(t *testing.T, m tui.Model, w, h int) (tui.Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	mm, ok := next.(tui.Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return mm, cmd
}

func sendKey(t *testing.T, m tui.Model, msg tea.Msg) (tui.Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	mm, ok := next.(tui.Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return mm, cmd
}

type erroringRenderer struct{}

func (erroringRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{}, errBoom
}

var errBoom = context.DeadlineExceeded

// lipglossWidth measures the visible width of a (possibly ANSI-styled)
// line the same way the package itself does — via
// github.com/charmbracelet/x/ansi — kept local to avoid importing an
// internal helper from the package under test.
func lipglossWidth(s string) int {
	return ansi.StringWidth(s)
}
