package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// stubRenderer returns a fixed Result; used where only "a Renderer is wired"
// matters, not its output.
type stubRenderer struct{}

func (stubRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: "x"}, nil
}

type contextCheckingRenderer struct{}

func (contextCheckingRenderer) Render(ctx context.Context, _ source.Candidate) (preview.Result, error) {
	return preview.Result{}, ctx.Err()
}

// ansiStubRenderer returns a fixed Result whose Text carries real ANSI color
// escape codes, simulating what the "dir" (lsd/eza --color=always) or
// "active_pane" (captured pane buffer) built-in sections actually produce.
// Used to prove the TUI preview pane never strips that color end-to-end.
type ansiStubRenderer struct {
	text string
}

func (r ansiStubRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: r.text}, nil
}

func internalTestCands() []source.Candidate {
	return []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "a"},
		{Path: "/b", NormalizedPath: "/b", Label: "b"},
	}
}

// commandOnlyCandidate is a Command-only workspace fixture: a plain
// `command = "..."` entry that is neither a group nor a template — the only
// entry type candidateIsCommandOnly reports true for, and therefore the
// only one selectWithTarget/hintsFor treat as tab/pane-launchable.
func commandOnlyCandidate() source.Candidate {
	return source.Candidate{
		Path: "/cmd", NormalizedPath: "/cmd", Label: "allsafe start",
		Meta: map[string]string{"command": "allsafe start"},
	}
}

// groupCandidate is a group workspace fixture (Meta["group"] == "true"): not
// Command-only, so it cannot be launched via ctrl+t/ctrl+p.
func groupCandidate() source.Candidate {
	return source.Candidate{
		Path: "/grp", NormalizedPath: "/grp", Label: "ECORP",
		Meta: map[string]string{"group": "true"},
	}
}

// templateCandidate is a template workspace fixture (Meta["template"] !=
// ""): not Command-only, so it cannot be launched via ctrl+t/ctrl+p.
func templateCandidate() source.Candidate {
	return source.Candidate{
		Path: "/tpl", NormalizedPath: "/tpl", Label: "k8s-ecorp",
		Meta: map[string]string{"template": "k8s"},
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

// TestModel_SelectionChangeClearsPreviewText (staleness guard) proves that
// when the highlighted candidate changes, the previous candidate's
// previewText is cleared immediately — so a stale candidate's rendered text
// can never flash even if an in-flight render for it arrives out of order.
// Before this guard, only the empty-selection branch (newKey == "") cleared
// previewText; the newKey != prevKey branch left the old candidate's text
// sitting in the field until the new render landed.
func TestModel_SelectionChangeClearsPreviewText(t *testing.T) {
	t.Parallel()
	m := NewModel(internalTestCands(), stubRenderer{})
	// Simulate a completed render for the first candidate.
	m.previewText = "rendered-for-/a"
	prevKey := m.currentPreviewKey()

	// Move the cursor to the second candidate (different highlight key).
	m.cursor = 1
	m.syncPreviewAfterSelectionChange(prevKey)

	if m.previewText != "" {
		t.Errorf("expected previewText cleared on selection change, got %q", m.previewText)
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

// TestModel_HandlePreviewResponse_ErrShowsVisibleIndicator proves the err
// return path of Renderer.Render — a distinct mechanism from the removed
// Result.Warning field — still surfaces to the user instead of leaving the
// preview pane silently blank (indistinguishable from any other empty
// state). Reachable via context cancellation, or any future Renderer
// implementation that returns a real error.
func TestModel_HandlePreviewResponse_ErrShowsVisibleIndicator(t *testing.T) {
	t.Parallel()
	m := NewModel(internalTestCands(), stubRenderer{})

	updated, _ := m.Update(previewResponseMsg{seq: m.previewSeq, err: errors.New("boom")})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	body := mm.previewBody(40)
	if !strings.Contains(body, "preview error") {
		t.Errorf("expected previewBody to show a visible error indicator, got %q", body)
	}
}

// TestModel_HandlePreviewResponse_SuccessClearsPriorError proves a normal
// successful render clears any previously shown error indicator — no stale
// error should linger after recovery.
func TestModel_HandlePreviewResponse_SuccessClearsPriorError(t *testing.T) {
	t.Parallel()
	m := NewModel(internalTestCands(), stubRenderer{})

	updated, _ := m.Update(previewResponseMsg{seq: m.previewSeq, err: errors.New("boom")})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	updated, _ = m.Update(previewResponseMsg{seq: m.previewSeq, result: preview.Result{Text: "fresh"}})
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	body := m.previewBody(40)
	if strings.Contains(body, "preview error") {
		t.Errorf("expected error indicator cleared after successful render, got %q", body)
	}
	if !strings.Contains(body, "fresh") {
		t.Errorf("expected fresh preview text rendered, got %q", body)
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

	_, _, ok, err := finalizeRun(m)
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

	cand, target, ok, err := finalizeRun(m)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a selected candidate")
	}
	if cand.Label != "a" {
		t.Errorf("candidate = %+v, want label %q", cand, "a")
	}
	if target != "" {
		t.Errorf("target = %q, want empty after plain enter (no ctrl+t/ctrl+p override)", target)
	}
}

// TestFinalizeRun_CtrlTTarget_ReturnsChosenTarget proves finalizeRun surfaces
// the ctrl+t/ctrl+p target override (ChosenTarget) alongside the selected
// candidate, so tuiSelector.Select can forward it to the caller.
func TestFinalizeRun_CtrlTTarget_ReturnsChosenTarget(t *testing.T) {
	pane := source.Pane{ID: "p1"}
	m := NewModel([]source.Candidate{commandOnlyCandidate()}, nil).WithCurrentPane(&pane)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	cand, target, ok, err := finalizeRun(m)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for a selected candidate")
	}
	if cand.Label != "allsafe start" {
		t.Errorf("candidate = %+v, want label %q", cand, "allsafe start")
	}
	if target != "tab" {
		t.Errorf("target = %q, want %q after ctrl+t", target, "tab")
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

// TestModel_RenderListCursorVisibleWhenScrolled (requirement: the cursor must
// not disappear when the list is scrolled) proves the cursor marker ">"
// appears on exactly one row and on the correct candidate when the viewport
// is scrolled past the first page. Previously the cursor check compared the
// slice-relative index against the absolute cursor, so the cursor vanished
// whenever offset > 0.
func TestModel_RenderListCursorVisibleWhenScrolled(t *testing.T) {
	cands := make([]source.Candidate, 50)
	for i := range cands {
		cands[i] = source.Candidate{Path: fmt.Sprintf("/c/%d", i), Label: fmt.Sprintf("c%d", i)}
	}
	m := NewModel(cands, nil)
	m.height = 10
	m.cursor = 40 // deep in the list, forcing a scroll offset

	out := m.renderList(30)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	cursorRows := 0
	for _, line := range lines {
		if strings.Contains(line, "> c40") {
			cursorRows++
		}
	}
	if cursorRows != 1 {
		t.Errorf("expected exactly one cursor row on c40, got %d:\n%s", cursorRows, out)
	}
}

// TestModel_RenderListTruncatesLongQuery (requirement: a long query must not
// wrap to a second line and push the UI off screen) proves the query line is
// truncated to fit within width instead of wrapping.
func TestModel_RenderListTruncatesLongQuery(t *testing.T) {
	m := NewModel(internalTestCands(), nil)
	m.query = strings.Repeat("x", 100)
	const width = 20
	out := m.renderList(width)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("expected at least the query line")
	}
	queryLine := lines[0]
	if w := lipgloss.Width(queryLine); w != width {
		t.Errorf("query line width = %d, want %d (must not overflow):\n%s", w, width, queryLine)
	}
	// The query line must end with ellipsis (truncated) rather than the full
	// 100-char query.
	if !strings.Contains(queryLine, "…") {
		t.Errorf("expected ellipsis in truncated query line:\n%s", queryLine)
	}
}

// TestModel_RenderListMarksMissingCandidates (requirement: missing
// configured workspaces must be clearly marked in the picker) confirms a
// candidate with Missing=true renders with a visible "(missing)" marker.
func TestModel_RenderListMarksMissingCandidates(t *testing.T) {
	m := NewModel([]source.Candidate{
		{Path: "/a", Label: "a"},
		{Path: "/b", Label: "b", Missing: true},
	}, nil)
	out := m.renderList(40)
	if !strings.Contains(out, "b (missing)") {
		t.Errorf("expected missing marker on candidate b, got:\n%s", out)
	}
	if strings.Contains(out, "a (missing)") {
		t.Errorf("candidate a must not be marked missing:\n%s", out)
	}
}

// TestModel_RenderListShowsSourceIcon (requirement: [sources.*].icon must be
// shown in the TUI) confirms the candidate's Icon is rendered before the
// Label in the list rows.
func TestModel_RenderListShowsSourceIcon(t *testing.T) {
	m := NewModel([]source.Candidate{
		{Path: "/a", Label: "alpha", Icon: "★"},
	}, nil)
	out := m.renderList(40)
	if !strings.Contains(out, "★ alpha") {
		t.Errorf("expected icon '★' before label 'alpha', got:\n%s", out)
	}
}

// TestModel_RenderListOmitsIconWhenEmpty confirms no stray whitespace or
// marker is added when a candidate has no configured icon.
func TestModel_RenderListOmitsIconWhenEmpty(t *testing.T) {
	m := NewModel([]source.Candidate{
		{Path: "/a", Label: "alpha", Icon: ""},
	}, nil)
	out := m.renderList(40)
	// The row should start with the marker "  " (no cursor) followed
	// directly by the label, not by a leading space from an empty icon.
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected label 'alpha' in output, got:\n%s", out)
	}
}

// TestSplitWidths_HonoursPercentageConfig confirms preview_width as a
// percentage drives the actual pane split (requirement: list_width/
// preview_width config wiring), not the hardcoded 3/5 heuristic.
func TestSplitWidths_HonoursPercentageConfig(t *testing.T) {
	list, prev := splitWidths(100, Layout{ListWidth: "auto", PreviewWidth: "60%"})
	if prev != 60 {
		t.Errorf("preview width = %d, want 60", prev)
	}
	if list != 39 {
		t.Errorf("list width = %d, want 39 (100 - 60 - 1 gap)", list)
	}
}

// TestSplitWidths_ListPercentageDrivesRemainder confirms list_width as a
// percentage leaves the remainder to preview when preview_width is auto.
func TestSplitWidths_ListPercentageDrivesRemainder(t *testing.T) {
	list, prev := splitWidths(100, Layout{ListWidth: "70%", PreviewWidth: "auto"})
	if list != 70 {
		t.Errorf("list width = %d, want 70", list)
	}
	if prev != 29 {
		t.Errorf("preview width = %d, want 29 (100 - 70 - 1 gap)", prev)
	}
}

// TestSplitWidths_AutoFallsBackToHeuristic confirms the zero-value Layout
// (both "auto"/empty) preserves the original 3/5 split heuristic.
func TestSplitWidths_AutoFallsBackToHeuristic(t *testing.T) {
	list, prev := splitWidths(100, Layout{})
	if want := 100 * 3 / 5; list != want {
		t.Errorf("list width = %d, want %d", list, want)
	}
	if want := 100 - (100 * 3 / 5) - 1; prev != want {
		t.Errorf("preview width = %d, want %d", prev, want)
	}
}

// TestSplitWidths_BothPercentagesSumToExactly100_NeverOverflows confirms
// that even a combination summing to exactly 100% (which, before
// reconciliation, would overflow by the 1-column pane gap) never exceeds the
// reported terminal width.
func TestSplitWidths_BothPercentagesSumToExactly100_NeverOverflows(t *testing.T) {
	list, prev := splitWidths(100, Layout{ListWidth: "60%", PreviewWidth: "40%"})
	if list+1+prev > 100 {
		t.Errorf("list(%d) + gap(1) + prev(%d) = %d, overflows width 100", list, prev, list+1+prev)
	}
	if list != 60 {
		t.Errorf("list width = %d, want 60 (list_width authoritative)", list)
	}
}

// TestSplitWidths_BothPercentagesOverflow_ReconciledProportionally is the
// defence-in-depth case (requirement: TUI sizing must never overflow): a
// Layout with both fields set past 100% combined (rejected by
// config.Load's validateTUI, but still reachable if a Layout is built
// directly) must still be reconciled so the rendered panes never overflow
// the terminal width.
func TestSplitWidths_BothPercentagesOverflow_ReconciledProportionally(t *testing.T) {
	list, prev := splitWidths(100, Layout{ListWidth: "60%", PreviewWidth: "60%"})
	if list+1+prev > 100 {
		t.Errorf("list(%d) + gap(1) + prev(%d) = %d, overflows width 100", list, prev, list+1+prev)
	}
	// Equal input percentages should scale down to roughly equal shares.
	if diff := list - prev; diff > 2 || diff < -2 {
		t.Errorf("expected roughly equal list(%d)/prev(%d) after proportional scaling", list, prev)
	}
}

// TestSplitWidths_BothPercentagesOverflow_RespectsMinimums confirms the
// existing 20/10 minimum-width floors still apply after reconciliation, on
// a narrow terminal where an extreme overflow combination is configured.
func TestSplitWidths_BothPercentagesOverflow_RespectsMinimums(t *testing.T) {
	list, prev := splitWidths(30, Layout{ListWidth: "90%", PreviewWidth: "90%"})
	if list < 20 {
		t.Errorf("list width = %d, must not go below the 20-column floor", list)
	}
	if prev < 10 {
		t.Errorf("preview width = %d, must not go below the 10-column floor", prev)
	}
}

// TestSplitWidths_OneSidedListPercentageNeverOverflows (requirement: TUI
// sizing must never overflow) confirms an extreme one-sided list_width
// (e.g. 95%) never forces list+gap+prev past the reported terminal width:
// before reconciliation, the derived remainder (prev = width - list - 1)
// would be clamped up to the 10-column preview floor without shrinking
// list back down, overflowing the terminal.
func TestSplitWidths_OneSidedListPercentageNeverOverflows(t *testing.T) {
	list, prev := splitWidths(100, Layout{ListWidth: "95%", PreviewWidth: "auto"})
	if list+1+prev > 100 {
		t.Errorf("list(%d) + gap(1) + prev(%d) = %d, overflows width 100", list, prev, list+1+prev)
	}
	if prev < 10 {
		t.Errorf("preview width = %d, must not go below the 10-column floor", prev)
	}
}

// TestSplitWidths_OneSidedPreviewPercentageNeverOverflows mirrors the list
// case for an extreme one-sided preview_width (e.g. 95%): the derived
// remainder for list would be clamped up to the 20-column floor without
// shrinking preview back down, overflowing the terminal.
func TestSplitWidths_OneSidedPreviewPercentageNeverOverflows(t *testing.T) {
	list, prev := splitWidths(100, Layout{ListWidth: "auto", PreviewWidth: "95%"})
	if list+1+prev > 100 {
		t.Errorf("list(%d) + gap(1) + prev(%d) = %d, overflows width 100", list, prev, list+1+prev)
	}
	if list < 20 {
		t.Errorf("list width = %d, must not go below the 20-column floor", list)
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

// TestModel_RenderPreviewTruncatesTallBody (requirement: long outputs like
// dir or active pane content must not expand infinitely and push the search
// box off screen) proves the preview body is capped to m.height - chromeRows
// lines so JoinHorizontal never breaks the layout with an oversized pane.
func TestModel_RenderPreviewTruncatesTallBody(t *testing.T) {
	m := newModel(internalTestCands(), stubRenderer{}, context.TODO())
	m.height = 10
	// Simulate a completed async render with very long text.
	m.previewLoading = false
	m.previewText = strings.Repeat("line\n", 100)

	out := m.renderPreview(40)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// chromeRows (border+header+blank+help) + body must not exceed m.height.
	// Allow a small tolerance for border/padding accounting, but the body
	// must be dramatically shorter than 100 lines.
	maxLines := m.height // generous upper bound; the point is no 100-line body
	if len(lines) > maxLines {
		t.Errorf("preview rendered %d lines, want <= %d (height=%d):\n%s", len(lines), maxLines, m.height, out)
	}
}

// TestModel_RenderPreviewEmptyHeightNoTruncation confirms that when height is
// unknown (0), the preview body is NOT truncated so previews still render
// fully in headless/test contexts where no WindowSizeMsg has arrived.
func TestModel_RenderPreviewEmptyHeightNoTruncation(t *testing.T) {
	m := newModel(internalTestCands(), stubRenderer{}, context.TODO())
	m.previewLoading = false
	m.previewText = "a\nb\nc\nd\ne"
	out := m.renderPreview(40)
	for _, want := range []string{"a", "b", "c", "d", "e"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in preview when height unknown, got:\n%s", want, out)
		}
	}
}

// TestModel_RenderListTruncatesLongLabel_NeverWraps is the regression test
// for the TUI overflow bug: a candidate whose label is much longer than the
// pane's content width must render as exactly ONE physical terminal line
// ending in "…", never wrap into two lines. Before the fix, rowStyle/
// cursorStyle's Width() word-wrapped instead of truncating, so a long
// candidate silently added extra physical lines the height budget never
// accounted for, pushing the top of the TUI off screen.
func TestModel_RenderListTruncatesLongLabel_NeverWraps(t *testing.T) {
	longPath := "/Users/example/Work/very/deeply/nested/project/directory/structure/that/keeps/going/on/and/on/service"
	m := NewModel([]source.Candidate{
		{Path: longPath, Label: longPath},
	}, nil)
	const width = 24
	out := m.renderList(width)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Query line + exactly one candidate row.
	if len(lines) != 2 {
		t.Fatalf("renderList produced %d lines for 1 candidate, want 2 (query + row):\n%s", len(lines), out)
	}
	row := lines[1]
	if w := lipgloss.Width(row); w != width {
		t.Errorf("row width = %d, want %d (must not overflow):\n%q", w, width, row)
	}
	if !strings.Contains(row, "…") {
		t.Errorf("expected ellipsis in truncated row, got:\n%q", row)
	}
}

// TestModel_RenderListNarrowWidthLongPaths_LineCountNeverInflates simulates
// the user's exact repro shape: many candidates with long realistic paths at
// a narrow content width (as produced by a narrow tui.list_width like
// "30%"). Before the fix, rows whose label/path exceeded the content width
// word-wrapped into 2-3 physical lines each, so the total rendered line
// count silently exceeded the height budget (maxRows + 1 for the query
// line) even though renderList only iterated over maxRows *candidates*.
func TestModel_RenderListNarrowWidthLongPaths_LineCountNeverInflates(t *testing.T) {
	const numCandidates = 40
	cands := make([]source.Candidate, numCandidates)
	for i := range cands {
		// Realistic long path, comfortably over 60 chars, mimicking deeply
		// nested project trees reported by the user.
		p := fmt.Sprintf("/Users/example/Work/ECORP/tech/platforms/dex/playground/andesproduct/service-%02d", i)
		cands[i] = source.Candidate{Path: p, Label: p}
	}
	m := NewModel(cands, nil)
	m.height = 20 // narrow terminal height
	const narrowContentWidth = 22
	maxRows := m.height - chromeRows

	out := m.renderList(narrowContentWidth)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// query line + at most maxRows candidate rows: never more, regardless of
	// how long any individual label/path is.
	if want := maxRows + 1; len(lines) > want {
		t.Errorf("renderList produced %d lines, want <= %d (maxRows=%d) — wrapping inflated the line count:\n%s",
			len(lines), want, maxRows, out)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != narrowContentWidth {
			t.Errorf("line %d width = %d, want %d (must not overflow/wrap): %q", i, w, narrowContentWidth, line)
		}
	}
}

// TestPreviewBody_LongLine_NeverWrapsAtNarrowWidth is the previewBody
// counterpart of the overflow regression: a single long line (e.g. a `dir`
// listing row or an active-pane buffer line) must render truncated to one
// physical line at a narrow preview width, not word-wrapped into several —
// otherwise capPreviewBodyLines' logical-line-count cap under-counts the
// actual rendered height.
func TestPreviewBody_LongLine_NeverWrapsAtNarrowWidth(t *testing.T) {
	m := newModel(internalTestCands(), stubRenderer{}, context.TODO())
	m.height = 12
	m.previewLoading = false
	// A long single "line" (e.g. a wide `dir` listing entry), no newlines.
	m.previewText = strings.Repeat("x", 200)

	const narrowPreviewWidth = 24
	body := m.previewBody(narrowPreviewWidth)
	bodyLines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(bodyLines) != 1 {
		t.Fatalf("previewBody produced %d lines for one long logical line, want 1 (must truncate, not wrap):\n%s", len(bodyLines), body)
	}
	if w := lipgloss.Width(bodyLines[0]); w != narrowPreviewWidth {
		t.Errorf("previewBody line width = %d, want %d:\n%q", w, narrowPreviewWidth, bodyLines[0])
	}

	// End-to-end through renderPreview + capPreviewBodyLines: total rendered
	// preview line count must stay within the height budget even though the
	// underlying text was a single 200-char line.
	out := m.renderPreview(narrowPreviewWidth)
	outLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(outLines) > m.height {
		t.Errorf("renderPreview produced %d lines, want <= %d (height=%d):\n%s", len(outLines), m.height, m.height, out)
	}
}

// TestModel_RenderPreviewNoHeaderOrHelp_AtFloorWidth is the regression test
// for the header/help removal: renderPreview used to render 4 lines (header
// + body + blank + help) even for a single-line body, and 2 of those
// constant strings ("preview", the help line) could themselves word-wrap at
// a narrow enough width. Now there is no header or help chrome at all — the
// body gets the pane's full budget — so a single-line body must render as
// exactly ONE line, padded to width, at the narrowest content width the
// layout can clamp down to. clampWidths enforces minPrev=10 as the floor
// outer preview pane width; paneContentWidth converts that to the actual
// content width (6) fed to renderPreview, the same floor a small
// `preview_width` percentage like "8%" can reach.
func TestModel_RenderPreviewNoHeaderOrHelp_AtFloorWidth(t *testing.T) {
	_, floorPrevOuter := clampWidths(100, 98, 1)
	floorWidth := paneContentWidth(floorPrevOuter)
	if floorWidth != 6 {
		t.Fatalf("sanity check failed: floor preview content width = %d, want 6 (clampWidths/paneContentWidth changed?)", floorWidth)
	}

	m := newModel(internalTestCands(), stubRenderer{}, context.TODO())
	m.height = 12
	m.previewLoading = false
	m.previewText = "x"

	out := m.renderPreview(floorWidth)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	const wantLines = 1
	if len(lines) != wantLines {
		t.Fatalf("renderPreview at floor width %d produced %d lines, want %d (no header/blank/help chrome):\n%q",
			floorWidth, len(lines), wantLines, out)
	}
	if w := lipgloss.Width(lines[0]); w != floorWidth {
		t.Errorf("body line width = %d, want %d: %q", w, floorWidth, lines[0])
	}
	if strings.Contains(out, "preview") {
		t.Errorf("expected no \"preview\" header in renderPreview output, got: %q", out)
	}
	if strings.Contains(out, "enter select") {
		t.Errorf("expected no help line in renderPreview output, got: %q", out)
	}
}

// TestPreviewBody_NoSelectionAndLoading_NeverWrapAtFloorWidth covers the
// remaining two previewBody constant strings ("(no selection)", "loading…")
// at the same floor preview content width as
// TestModel_RenderPreviewConstantStrings_NeverWrapAtFloorWidth.
func TestPreviewBody_NoSelectionAndLoading_NeverWrapAtFloorWidth(t *testing.T) {
	_, floorPrevOuter := clampWidths(100, 98, 1)
	floorWidth := paneContentWidth(floorPrevOuter)

	t.Run("no selection", func(t *testing.T) {
		m := newModel(nil, stubRenderer{}, context.TODO())
		body := m.previewBody(floorWidth)
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		if len(lines) != 1 {
			t.Fatalf("previewBody(\"(no selection)\") produced %d lines at width %d, want 1:\n%q", len(lines), floorWidth, body)
		}
		if w := lipgloss.Width(lines[0]); w != floorWidth {
			t.Errorf("line width = %d, want %d: %q", w, floorWidth, lines[0])
		}
	})

	t.Run("loading", func(t *testing.T) {
		m := newModel(internalTestCands(), stubRenderer{}, context.TODO())
		m.previewLoading = true
		body := m.previewBody(floorWidth)
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		if len(lines) != 1 {
			t.Fatalf("previewBody(\"loading…\") produced %d lines at width %d, want 1:\n%q", len(lines), floorWidth, body)
		}
		if w := lipgloss.Width(lines[0]); w != floorWidth {
			t.Errorf("line width = %d, want %d: %q", w, floorWidth, lines[0])
		}
	})
}

// TestPreviewBody_NilRenderer_StyledPrefixNeverOverflowsAtNarrowWidth is the
// regression test for the ANSI-after-truncation bug: the nil-renderer
// fallback branch of previewBody styled the "label  "/"path   "/"source "
// prefixes with palette.labelStyle BEFORE truncating, so the ANSI escape
// bytes those Render calls inject ate into truncateLinesToWidth's raw rune
// budget. At a narrow width, the visible (ANSI-aware) width of the resulting
// line no longer matched the target width — truncation must happen on the
// raw, unstyled text first, then styling is layered on top.
func TestPreviewBody_NilRenderer_StyledPrefixNeverOverflowsAtNarrowWidth(t *testing.T) {
	// Force ANSI output regardless of whether the test binary's stdout is a
	// TTY: palette styles use the package-global lipgloss renderer, which by
	// default detects color support from os.Stdout and emits no escape
	// codes in a non-interactive test run — silently hiding this bug.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	longLabel := strings.Repeat("very-long-label-", 10)
	m := NewModel([]source.Candidate{
		{Path: "/some/very/long/path/that/keeps/going/on", Label: longLabel, Source: "workspace"},
	}, nil)

	const narrowWidth = 10
	body := m.previewBody(narrowWidth)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("previewBody(nil renderer) produced %d lines, want 3 (label/path/source):\n%q", len(lines), body)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != narrowWidth {
			t.Errorf("line %d visible width = %d, want %d (ANSI-aware truncation broke): %q", i, w, narrowWidth, line)
		}
		assertNoUnterminatedANSI(t, line)
	}
}

// assertNoUnterminatedANSI fails t if line contains an ANSI CSI escape
// sequence ("\x1b[...") that never reaches its closing 'm' byte — the
// signature of truncation cutting mid-escape-sequence, which leaves a
// dangling code that can leak color/attributes into subsequent terminal
// output.
func assertNoUnterminatedANSI(t *testing.T, line string) {
	t.Helper()
	for idx := 0; idx < len(line); {
		start := strings.Index(line[idx:], "\x1b[")
		if start == -1 {
			return
		}
		start += idx
		rest := line[start+2:]
		end := strings.IndexByte(rest, 'm')
		if end == -1 {
			t.Fatalf("found unterminated ANSI escape sequence: %q", line)
		}
		idx = start + 2 + end + 1
	}
}

// TestTruncateToWidth_ANSIStyledInput_StaysVisibleWidthAndWellFormed is the
// defense-in-depth regression test for truncateToWidth itself: dirArgv
// intentionally forces --color=always and ReadPane intentionally requests
// --format ansi (both restored on purpose so dir/active_pane show their
// real colors), and a user-declared [preview.commands.<name>] custom
// command is outside shep's control and could also emit ANSI color codes.
// truncateToWidth must not count the
// invisible escape bytes against its rune budget — that undercounts the
// visible width and can sever an escape sequence mid-code, which is
// exactly the bug the user hit ("las previsualizaciones estan falladas").
// This asserts the ANSI-aware VISIBLE width (lipgloss.Width), not raw
// len/rune count, and that no escape sequence is left unterminated.
func TestTruncateToWidth_ANSIStyledInput_StaysVisibleWidthAndWellFormed(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	styled := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Render(strings.Repeat("x", 50))

	const width = 10
	got := truncateToWidth(styled, width)
	if w := lipgloss.Width(got); w != width {
		t.Errorf("truncateToWidth(ANSI-styled) visible width = %d, want %d: %q", w, width, got)
	}
	assertNoUnterminatedANSI(t, got)
}

// TestTruncateToWidth_ANSIStyledInput_AlreadyFitsReturnsUnchanged confirms
// the "already fits" fast path still holds for ANSI-styled input: a short
// styled string within width must come back byte-for-byte unchanged, not
// re-wrapped or re-escaped.
func TestTruncateToWidth_ANSIStyledInput_AlreadyFitsReturnsUnchanged(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	styled := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Render("hi")

	got := truncateToWidth(styled, 10)
	if got != styled {
		t.Errorf("truncateToWidth(short ANSI-styled) = %q, want unchanged %q", got, styled)
	}
}

// TestModel_PreviewPreservesRealRendererANSIEndToEnd is the end-to-end
// regression lock for the TUI picker's preview pane: unlike `shep preview`'s
// CLI path (internal/command/preview.go), the interactive picker must NEVER
// strip real ANSI color coming from the renderer (e.g. lsd/eza's own
// coloring for "dir", or a captured pane's real terminal colors for
// "active_pane"). This drives the full path a real render takes — stub
// Renderer.Render -> Init's preview Cmd -> Update(previewResponseMsg) ->
// previewBody/renderPreview/View — and asserts the final rendered output
// still contains a real ANSI escape sequence, proving no layer along the
// way silently strips it.
func TestModel_PreviewPreservesRealRendererANSIEndToEnd(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	rendererText := lipgloss.NewStyle().Foreground(lipgloss.Color(colorAccent)).Render("README.md")
	renderer := ansiStubRenderer{text: rendererText}

	m := newModel(internalTestCands(), renderer, context.TODO())
	m.width = 160
	m.height = 40

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected a preview render Cmd from Init")
	}
	msg, ok := cmd().(previewResponseMsg)
	if !ok {
		t.Fatalf("expected previewResponseMsg, got %T", msg)
	}
	updated, _ := m.Update(msg)
	m, ok = updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}

	body := m.previewBody(paneContentWidth(m.width))
	if !strings.Contains(body, "\x1b[") {
		t.Errorf("previewBody stripped real renderer ANSI, got: %q", body)
	}

	view := m.View()
	if !strings.Contains(view, "\x1b[") {
		t.Errorf("View() stripped real renderer ANSI end-to-end, got: %q", view)
	}
}

// TestModel_RenderListNoMatches_NeverWrapsEvenBelowMinList guards the
// "  no matches" constant string in renderList for consistency and
// defense-in-depth: it's currently unreachable below content width 16
// because clampWidths enforces minList=20, but had no truncation guard of
// its own, so it would silently break if minList were ever lowered. Exercise
// it well below that floor directly to prove the guard holds independent of
// clampWidths.
func TestModel_RenderListNoMatches_NeverWrapsEvenBelowMinList(t *testing.T) {
	m := NewModel(nil, nil)
	const width = 6
	out := m.renderList(width)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// query line + "no matches" line.
	if len(lines) != 2 {
		t.Fatalf("renderList produced %d lines for empty candidates, want 2 (query + no-matches):\n%q", len(lines), out)
	}
	noMatches := lines[1]
	if w := lipgloss.Width(noMatches); w != width {
		t.Errorf("no-matches line width = %d, want %d: %q", w, width, noMatches)
	}
}

// TestCandidateDisplayText_IconLabelAndMissing (footer/list shared helper)
// proves candidateDisplayText builds "icon label" and appends the
// "(missing)" suffix, matching renderList's existing per-row construction
// exactly so the footer (which reuses this helper) shows the identical text.
func TestCandidateDisplayText_IconLabelAndMissing(t *testing.T) {
	t.Parallel()
	got := candidateDisplayText(source.Candidate{Path: "/a", Label: "alpha", Icon: "★", Missing: true})
	want := "★ alpha (missing)"
	if got != want {
		t.Errorf("candidateDisplayText = %q, want %q", got, want)
	}
}

// TestCandidateDisplayText_FallsBackToPath_NoIconNoMissing triangulates the
// happy path with no icon and no Missing flag, and Label empty so Path is
// used instead — a different code path than the icon+missing case above.
func TestCandidateDisplayText_FallsBackToPath_NoIconNoMissing(t *testing.T) {
	t.Parallel()
	got := candidateDisplayText(source.Candidate{Path: "/b/bravo"})
	want := "/b/bravo"
	if got != want {
		t.Errorf("candidateDisplayText = %q, want %q", got, want)
	}
}

// TestModel_FooterText_ShowsCurrentCandidateFullText (footer line) proves
// footerText returns the full, untruncated icon+label-or-path(+missing) text
// for the currently highlighted candidate, reusing candidateDisplayText.
func TestModel_FooterText_ShowsCurrentCandidateFullText(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{
		{Path: "/a", Label: "alpha", Icon: "★"},
		{Path: "/b", Label: "bravo"},
	}, nil)
	m.cursor = 1
	got := m.footerText()
	want := "bravo"
	if got != want {
		t.Errorf("footerText = %q, want %q", got, want)
	}
}

// TestModel_FooterText_EmptyFilteredSet triangulates the no-candidates case:
// footerText must degrade to a muted placeholder instead of panicking or
// returning a stale candidate's text.
func TestModel_FooterText_EmptyFilteredSet(t *testing.T) {
	t.Parallel()
	m := NewModel(nil, nil)
	got := m.footerText()
	want := "(no selection)"
	if got != want {
		t.Errorf("footerText (empty) = %q, want %q", got, want)
	}
}

// TestModel_ViewFooter_ShowsFullTextEvenWhenListRowTruncated (footer line)
// proves the footer, which spans the FULL terminal width below both panes,
// shows a highlighted candidate's complete label even when the same
// candidate's row is truncated inside the (narrower) list pane column —
// the whole point of the footer per the Atuin-inspired "always show the
// full command" pattern.
func TestModel_ViewFooter_ShowsFullTextEvenWhenListRowTruncated(t *testing.T) {
	t.Parallel()
	longLabel := "a-fairly-long-candidate-label-that-does-not-fit-the-narrow-list-column"
	m := newModelWithLayout([]source.Candidate{
		{Path: "/x/" + longLabel, Label: longLabel},
	}, nil, context.TODO(), Layout{ListWidth: "20%", PreviewWidth: "auto"})
	// Wide enough that label + separator + the full footer hints text
	// (see hintsFor) comfortably fit on one line — this test is about the
	// footer showing the full label despite the LIST PANE's own narrow
	// column, not about the footer's own narrow-width truncation (see
	// TestModel_ViewFooter_DefensivelyTruncatedAtExtremelyNarrowWidth).
	m.width = 150
	m.height = 24

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, longLabel) {
		t.Errorf("footer must show the FULL label at a wide terminal, got footer line: %q", footer)
	}

	// Directly assert on the rendered LIST-PANE portion of this SAME view
	// string — not a separate pre-existing test plus a width-math sanity
	// check as indirect evidence. Isolate just the list pane's own outer
	// width (listW) on each physical line via ansi.Cut (ANSI-aware, no
	// ellipsis inserted, unlike truncateToWidth) so the check cannot be
	// contaminated by the preview pane's own (wider) rendering on the same
	// joined line, then locate the candidate's row within that isolated
	// portion by its label prefix (distinct from the empty query line,
	// which also starts with the cursor-marker-shaped "> " but carries no
	// label text at all).
	listW, _ := splitWidths(m.width, m.layout)
	labelPrefix := longLabel[:10]
	var listRow string
	found := false
	for _, line := range lines {
		portion := ansi.Cut(line, 0, listW)
		if strings.Contains(portion, labelPrefix) {
			listRow = portion
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("could not locate the candidate's row within the list-pane portion of the rendered view:\n%s", view)
	}
	if strings.Contains(listRow, longLabel) {
		t.Errorf("list-pane portion must NOT contain the full label (expected truncation), got: %q", listRow)
	}
	if !strings.Contains(listRow, "…") {
		t.Errorf("list-pane portion must show a truncation ellipsis, got: %q", listRow)
	}
	// Sanity: at list_width=20% of 100, the list pane's content width is far
	// narrower than the label — the row inside the list pane must be
	// truncated (this is the contrast the footer exists to fix).
	if paneContentWidth(listW) >= len(longLabel) {
		t.Fatalf("test setup invalid: list pane content width %d must be narrower than the label (%d chars)",
			paneContentWidth(listW), len(longLabel))
	}
}

// TestModel_ViewFooter_EmptyCandidates_DegradesGracefully proves the footer
// shows the muted placeholder (not a panic, not stale text) when there are
// zero candidates to highlight.
func TestModel_ViewFooter_EmptyCandidates_DegradesGracefully(t *testing.T) {
	t.Parallel()
	m := NewModel(nil, nil)
	m.width = 100
	m.height = 24

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "no selection") {
		t.Errorf("expected footer placeholder for zero candidates, got: %q", footer)
	}
}

// TestModel_ViewFooter_DefensivelyTruncatedAtExtremelyNarrowWidth proves the
// footer goes through the same ANSI-safe truncateToWidth as every other line
// in this file: at an extremely narrow terminal width, the footer line must
// still fit within m.width (never corrupting the layout) even though the
// full candidate text does not fit.
func TestModel_ViewFooter_DefensivelyTruncatedAtExtremelyNarrowWidth(t *testing.T) {
	t.Parallel()
	longLabel := strings.Repeat("x", 200)
	m := NewModel([]source.Candidate{{Path: "/y", Label: longLabel}}, nil)
	const narrowWidth = 12
	m.width = narrowWidth
	m.height = 24

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	footer := lines[len(lines)-1]
	if w := lipgloss.Width(footer); w > narrowWidth {
		t.Errorf("footer width = %d, must not exceed terminal width %d: %q", w, narrowWidth, footer)
	}
	if !strings.Contains(footer, "…") {
		t.Errorf("expected footer truncated with ellipsis at narrow width, got: %q", footer)
	}
}

// TestModel_ViewReservesFooterLine_HeightBudgetIntact proves View() reserves
// exactly 1 line for the footer by shrinking the pane height budget (height-1
// fed to renderList's chromeRows accounting), so a tall candidate list still
// fits within m.height total lines including the footer, instead of
// overflowing by one row.
func TestModel_ViewReservesFooterLine_HeightBudgetIntact(t *testing.T) {
	t.Parallel()
	cands := make([]source.Candidate, 50)
	for i := range cands {
		cands[i] = source.Candidate{Path: fmt.Sprintf("/c/%d", i), Label: fmt.Sprintf("c%d", i)}
	}
	m := NewModel(cands, nil)
	m.width = 100
	m.height = 20
	m.cursor = len(cands) - 1

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Errorf("View() produced %d lines, want <= %d (height=%d, 1 reserved for footer):\n%s",
			len(lines), m.height, m.height, view)
	}
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "c49") {
		t.Errorf("expected footer to show the highlighted last candidate 'c49', got: %q", footer)
	}
}

// TestSplitWidths_AppliedToHeightAxis_MirrorsWidthAxis (portrait layout)
// mirrors TestSplitWidths_HonoursPercentageConfig along the height axis:
// splitWidths is genuinely axis-agnostic (it only operates on an opaque
// "total" int), so feeding it a terminal height instead of width must split
// list_width/preview_width percentages exactly the same way portrait mode
// needs, with zero new percent-parsing code.
func TestSplitWidths_AppliedToHeightAxis_MirrorsWidthAxis(t *testing.T) {
	t.Parallel()
	listH, prevH := splitWidths(40, Layout{ListWidth: "auto", PreviewWidth: "30%"})
	if prevH != 12 {
		t.Errorf("preview height share = %d, want 12 (30%% of 40)", prevH)
	}
	if listH != 27 {
		t.Errorf("list height share = %d, want 27 (40 - 12 - 1 gap)", listH)
	}
}

// TestView_PortraitLayout_StacksListAbovePreview_FullWidth proves that when
// Layout.Orientation is LayoutPortrait, View() stacks the list pane above
// the preview pane (JoinVertical), each spanning the full reported terminal
// width — the opposite of landscape's JoinHorizontal side-by-side split.
func TestView_PortraitLayout_StacksListAbovePreview_FullWidth(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(internalTestCands(), nil, context.TODO(), Layout{Orientation: LayoutPortrait})
	m.width = 100
	m.height = 30

	view := m.View()
	lines := strings.Split(view, "\n")
	// Locate the list pane's own bottom border and the preview pane's own
	// top border — in portrait, both panes are bordered boxes stacked
	// vertically, so the FIRST "╭" is the list pane's top border and the
	// SECOND is the preview pane's top border; the preview's top border
	// must appear strictly AFTER the list pane's own bottom border (i.e.
	// below it, not beside it). (There is no "preview" header text to
	// locate anymore — see renderPreview.)
	listBottomIdx := -1
	previewTopIdx := -1
	topBorderCount := 0
	for i, line := range lines {
		if strings.Contains(line, "╰") && listBottomIdx == -1 {
			listBottomIdx = i
		}
		if strings.Contains(line, "╭") {
			topBorderCount++
			if topBorderCount == 2 && previewTopIdx == -1 {
				previewTopIdx = i
			}
		}
	}
	if listBottomIdx == -1 || previewTopIdx == -1 {
		t.Fatalf("could not locate list bottom border or preview top border in output:\n%s", view)
	}
	if previewTopIdx <= listBottomIdx {
		t.Errorf("expected preview top border (line %d) below list pane bottom border (line %d) in portrait mode:\n%s",
			previewTopIdx, listBottomIdx, view)
	}
	// Every non-footer bordered line should span the full reported width
	// (both panes span the FULL terminal width in portrait).
	for i, line := range lines[:listBottomIdx+1] {
		if w := lipgloss.Width(line); w != m.width {
			t.Errorf("portrait list pane line %d width = %d, want %d (full terminal width): %q", i, w, m.width, line)
		}
	}
}

// TestView_LandscapeLayout_StillJoinsHorizontally is the approval test
// locking existing landscape behavior unchanged: the zero-value Orientation
// (LayoutLandscape) must still split panes side by side via JoinHorizontal,
// never triggering the new portrait branch.
func TestView_LandscapeLayout_StillJoinsHorizontally(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(internalTestCands(), nil, context.TODO(), Layout{})
	m.width = 100
	m.height = 30

	view := m.View()
	lines := strings.Split(view, "\n")
	// The first content row (after the top border) must contain both the
	// list's query line ">" and, side by side on the SAME line, the preview
	// pane's border — proof the two panes sit horizontally, not stacked.
	found := false
	for _, line := range lines {
		if strings.Contains(line, ">") && strings.Count(line, "│") >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected list and preview panes side by side on the same line in landscape mode:\n%s", view)
	}
}

// TestHandleKey_CtrlL_TogglesLandscapePortrait proves ctrl+l flips
// m.layout.Orientation between landscape and portrait for the current
// session, and flips back on a second press — a live, in-memory toggle, not
// a config mutation (config.TUIConfig is not reachable from Model at all,
// so there is nothing here that could write back to disk).
func TestHandleKey_CtrlL_TogglesLandscapePortrait(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(internalTestCands(), nil, context.TODO(), Layout{})
	if m.layout.Orientation != "" && m.layout.Orientation != LayoutLandscape {
		t.Fatalf("setup: expected initial orientation to be landscape/empty, got %q", m.layout.Orientation)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.layout.Orientation != LayoutPortrait {
		t.Errorf("after first ctrl+l, orientation = %q, want %q", mm.layout.Orientation, LayoutPortrait)
	}

	updated, _ = mm.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	mm2, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm2.layout.Orientation != LayoutLandscape {
		t.Errorf("after second ctrl+l, orientation = %q, want %q", mm2.layout.Orientation, LayoutLandscape)
	}
}

// TestView_PortraitLayout_Height24_NeverOverflowsBudget is a regression test
// for the CRITICAL portrait-overflow bug: renderPortrait fed m.height
// through splitWidths, which floors both shares via clampWidths'
// minList=20/minPrev=10 — column-WIDTH floors, tuned for the width axis,
// reused unchanged for the height axis. Any terminal height in [8, 29]
// (including 24, a very common default terminal/tmux pane height) hit those
// floors and rendered a fixed ~29-line block regardless of the actual
// reported height. This proves View()'s total rendered output for height=24
// fits within the 24-line budget.
func TestView_PortraitLayout_Height24_NeverOverflowsBudget(t *testing.T) {
	t.Parallel()
	cands := make([]source.Candidate, 50)
	for i := range cands {
		cands[i] = source.Candidate{Path: fmt.Sprintf("/c/%d", i), Label: fmt.Sprintf("c%d", i)}
	}
	m := newModelWithLayout(cands, nil, context.TODO(), Layout{Orientation: LayoutPortrait})
	m.width = 100
	m.height = 24

	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > m.height {
		t.Errorf("portrait View() at height=24 produced %d lines, want <= %d:\n%s", len(lines), m.height, view)
	}
}

// TestView_PortraitLayout_AcrossShortHeights_NeverOverflowsBudget sweeps the
// previously-uncovered [8, 29] height range. The pre-existing portrait test
// (TestView_PortraitLayout_StacksListAbovePreview_FullWidth) only exercised
// height=30, exactly the boundary where the width-tuned floors happen to
// stop dominating the split — it never caught the overflow. This proves the
// "never breaks layout" guarantee holds continuously across the range, not
// just at height=30+.
func TestView_PortraitLayout_AcrossShortHeights_NeverOverflowsBudget(t *testing.T) {
	t.Parallel()
	cands := make([]source.Candidate, 50)
	for i := range cands {
		cands[i] = source.Candidate{Path: fmt.Sprintf("/c/%d", i), Label: fmt.Sprintf("c%d", i)}
	}

	for _, h := range []int{8, 9, 10, 12, 14, 16, 18, 20, 22, 24, 26, 28, 29} {
		t.Run(fmt.Sprintf("height=%d", h), func(t *testing.T) {
			t.Parallel()
			m := newModelWithLayout(cands, nil, context.TODO(), Layout{Orientation: LayoutPortrait})
			m.width = 100
			m.height = h

			view := m.View()
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > h {
				t.Errorf("portrait View() at height=%d produced %d lines, want <= %d:\n%s", h, len(lines), h, view)
			}
		})
	}
}

// TestView_PortraitLayout_BelowMinPortraitHeight_FallsBackToListOnly proves
// that below portrait's own real minimum height (minPortraitHeight — the
// smallest height at which its height-axis floors, minListH/minPrevH, can
// both be honoured without overflow) View() falls back to the single
// list-only pane, the same fallback mechanism minPreviewHeight already uses
// for landscape, instead of attempting a dual-pane split that cannot fit.
func TestView_PortraitLayout_BelowMinPortraitHeight_FallsBackToListOnly(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(internalTestCands(), nil, context.TODO(), Layout{Orientation: LayoutPortrait})
	m.width = 100
	m.height = minPortraitHeight - 1

	view := m.View()
	// nil renderer degrades the preview pane to a "label  <value>" summary
	// line (see previewBody); its absence proves the preview pane is
	// hidden entirely (there is no "preview" header text anymore — see
	// renderPreview).
	if strings.Contains(view, "label") {
		t.Errorf("expected preview pane hidden below minPortraitHeight (%d), but found preview content at height=%d:\n%s",
			minPortraitHeight, m.height, view)
	}
}

// previewBottomBorderRow locates the preview pane's own bottom border
// ("╰") row index within view: in portrait, both the list and preview panes
// are bordered boxes stacked vertically, each rendering their own "╰" —
// this is the SECOND occurrence, since the first belongs to the list pane
// stacked above it. (There is no "preview" header text to scan forward from
// anymore — see renderPreview — so this counts border occurrences directly
// instead.)
func previewBottomBorderRow(t *testing.T, view string) int {
	t.Helper()
	lines := strings.Split(view, "\n")
	bottomBorderCount := 0
	for i, line := range lines {
		if strings.Contains(line, "╰") {
			bottomBorderCount++
			if bottomBorderCount == 2 {
				return i
			}
		}
	}
	t.Fatalf("could not locate the preview pane's own bottom border (2nd \"╰\") in view:\n%s", view)
	return -1
}

// TestView_PortraitLayout_PreviewBorderStaysFixed_TallBody is the RED test
// for the preview-border-stability fix: today the preview pane's outer
// height is the JoinVertical'd content's OWN natural height (header+body+
// blank+help+border), so a highlighted candidate with a long preview body
// pushes the bottom border down past the row splitSizes actually assigned
// the pane (prevH). This proves the bottom border stays pinned at that
// fixed row regardless of how many lines the body has.
func TestView_PortraitLayout_PreviewBorderStaysFixed_TallBody(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(internalTestCands(), stubRenderer{}, context.TODO(), Layout{Orientation: LayoutPortrait})
	m.width = 80
	m.height = 40
	m.previewLoading = false
	m.previewText = strings.Repeat("line\n", 30)

	view := m.View()
	gotRow := previewBottomBorderRow(t, view)

	paneHeight := m.height - 1 // footer reserved, mirrors View()
	listH, prevH := splitSizes(paneHeight, m.layout, minListH, minPrevH)
	wantRow := listH + prevH - 1

	if gotRow != wantRow {
		t.Errorf("preview bottom border at row %d, want %d (fixed at the splitSizes budget, listH=%d prevH=%d) — full view:\n%s",
			gotRow, wantRow, listH, prevH, view)
	}
}

// TestView_PortraitLayout_PreviewBorderStaysFixed_ShortBody is the RED test
// for the padding side of the same fix: a SHORT preview body (here, zero
// candidates -> "(no selection)", a single line) must NOT collapse the
// pane's border up to the content's natural height — the bottom border must
// land on the exact same row as the tall-body case above, since both are
// anchored to the same splitSizes budget (prevH), not to body length.
func TestView_PortraitLayout_PreviewBorderStaysFixed_ShortBody(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(nil, nil, context.TODO(), Layout{Orientation: LayoutPortrait})
	m.width = 80
	m.height = 40

	view := m.View()
	gotRow := previewBottomBorderRow(t, view)

	paneHeight := m.height - 1
	listH, prevH := splitSizes(paneHeight, m.layout, minListH, minPrevH)
	wantRow := listH + prevH - 1

	if gotRow != wantRow {
		t.Errorf("preview bottom border at row %d, want %d (must match the tall-body case, not collapse to the short content's natural height) — full view:\n%s",
			gotRow, wantRow, view)
	}
}

// TestView_LandscapeLayout_BothPanesBordersAlign_TallPreview proves that in
// landscape mode both panes share the SAME fixed outer height (paneHeight),
// so their bottom borders land on the identical row even when the preview
// pane's body has far more lines than the list pane's own content — before
// the fix, the preview pane grew taller than the list pane and the two
// borders drifted apart.
func TestView_LandscapeLayout_BothPanesBordersAlign_TallPreview(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(internalTestCands(), stubRenderer{}, context.TODO(), Layout{})
	m.width = 100
	m.height = 30
	m.previewLoading = false
	m.previewText = strings.Repeat("line\n", 30)

	view := m.View()
	lines := strings.Split(view, "\n")

	paneHeight := m.height - 1
	wantRow := paneHeight - 1
	if wantRow < 0 || wantRow >= len(lines) {
		t.Fatalf("computed wantRow %d out of range for %d rendered lines:\n%s", wantRow, len(lines), view)
	}

	got := strings.Count(lines[wantRow], "╰")
	if got != 2 {
		t.Errorf("expected both panes' bottom border (╰) on the same row %d, found %d occurrence(s): %q\nfull view:\n%s",
			wantRow, got, lines[wantRow], view)
	}
}

// TestView_LandscapeLayout_ListPaneBorderStaysFixed_EmptyCandidates proves
// the list pane also gets the fixed-outer-height treatment: with zero
// candidates, renderList's own natural content is just 2 lines (query +
// "no matches"), far shorter than the pane's assigned budget — the border
// must still sit at the budget's row, not collapse around the 2-line
// content. Uses the single-pane (hidePreview) fallback so the located "╰"
// row is unambiguously the list pane's own bottom border.
func TestView_LandscapeLayout_ListPaneBorderStaysFixed_EmptyCandidates(t *testing.T) {
	t.Parallel()
	m := newModelWithLayout(nil, nil, context.TODO(), Layout{})
	m.width = 70 // < minPreviewWidth: hidePreview, single list-only pane
	m.height = 24

	view := m.View()
	lines := strings.Split(view, "\n")

	paneHeight := m.height - 1
	wantRow := paneHeight - 1
	if wantRow < 0 || wantRow >= len(lines) {
		t.Fatalf("computed wantRow %d out of range for %d rendered lines:\n%s", wantRow, len(lines), view)
	}
	if !strings.Contains(lines[wantRow], "╰") {
		t.Errorf("expected list pane's bottom border at row %d even with zero candidates, got: %q\nfull view:\n%s",
			wantRow, lines[wantRow], view)
	}
}

// filteredLabels maps the model's filtered indices to their labels, in display
// order. Used by ranking/order assertions that need to assert which candidate
// lands where without depending on rendering.
func filteredLabels(m Model) []string {
	out := make([]string, 0, len(m.filtered))
	for _, idx := range m.filtered {
		out = append(out, m.candidates[idx].Label)
	}
	return out
}

// TestApplyFilter_RanksBetterSubsequenceMatchFirst is the core contract test
// for replacing the boolean subsequence matcher with scored fuzzy matching
// (sahilm/fuzzy). Both candidates below contain the query "abc" as a
// subsequence, but "abc-service" matches it as a contiguous prefix (each
// letter adjacent, plus a first-character bonus) while "banana-fabric-doc"
// only matches it with the letters scattered far apart and NOT aligned to any
// "-" separator boundary (a@1, b@9, c@12 — none of them immediately follow a
// "-"). The contiguous match must sort first. Under the old boolean matcher
// the result order was just the providers' input order (meaningless);
// sahilm/fuzzy must reorder strong -> weak. Candidates are listed weakest-first
// so a non-ranking matcher would keep them in the (wrong) input order and this
// test would fail.
//
// NOTE: an earlier version of this fixture used "alpha-beta-cache" as the weak
// candidate. That accidentally matched "abc" right after each "-" separator
// (alpha-BETA-CAche), which sahilm/fuzzy rewards with a
// matchFollowingSeparatorBonus per letter — a real and correct scoring rule
// (it is how "abc" fuzzy-matches "Alpha Beta Cache"-style initials), but it
// made "alpha-beta-cache" outscore the contiguous "abc-service" match,
// contradicting the test's intent. "banana-fabric-doc" was chosen to keep the
// letters scattered without landing on any separator boundary, verified
// directly against the library (score -19) versus the contiguous match's
// score (22).
func TestApplyFilter_RanksBetterSubsequenceMatchFirst(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/b/banana-fabric-doc", Label: "banana-fabric-doc"}, // weak: "abc" scattered, no separator alignment
		{Path: "/a/abc-service", Label: "abc-service"},             // strong: "abc" contiguous prefix
	}
	m := NewModel(cands, nil)
	m.query = "abc"
	m.applyFilter()

	got := filteredLabels(m)
	want := []string{"abc-service", "banana-fabric-doc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applyFilter ranking (best match must be first):\n  got:  %v\n  want: %v\n"+
			"both candidates match %q as a subsequence, but the contiguous prefix match must rank above the scattered one",
			got, want, "abc")
	}
}

// TestApplyFilter_EmptyQueryKeepsOriginalOrder locks the unchanged empty-query
// behavior: ranking is meaningless when there is no query, so every candidate
// must stay in the original (provider) order exactly as NewModel produced them.
// This is depended on by existing UX and tests, so it is an explicit contract.
func TestApplyFilter_EmptyQueryKeepsOriginalOrder(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/c/charlie", Label: "charlie"},
		{Path: "/a/alpha", Label: "alpha"},
		{Path: "/b/bravo", Label: "bravo"},
	}
	m := NewModel(cands, nil)
	m.query = ""
	m.applyFilter()

	got := filteredLabels(m)
	want := []string{"charlie", "alpha", "bravo"} // input order, deliberately NOT sorted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("empty query must preserve original provider order:\n  got:  %v\n  want: %v", got, want)
	}
}

// TestApplyFilter_CaseInsensitive confirms the move to sahilm/fuzzy preserves
// the existing case-insensitive UX. sahilm/fuzzy matches via equalFold (see its
// fuzzy.go), so an upper-case query still matches lower-case labels. We must
// NOT lowercase the haystack ourselves, or we would destroy sahilm/fuzzy's
// camelCase-boundary scoring (one of the main reasons for adopting it). "zebra"
// is chosen as the non-matching control because it contains no 'c', so it can
// never spuriously satisfy the "abc" subsequence under any matcher.
func TestApplyFilter_CaseInsensitive(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/a/abc-service", Label: "abc-service"},
		{Path: "/z/zebra", Label: "zebra"},
	}
	m := NewModel(cands, nil)
	m.query = "ABC" // upper-case query against lower-case labels
	m.applyFilter()

	got := filteredLabels(m)
	want := []string{"abc-service"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("case-insensitive match broke (upper query must match lower labels):\n  got:  %v\n  want: %v", got, want)
	}
}

// TestApplyFilter_RanksCaseVariantsEqually documents a deliberate consequence
// of NOT lowercasing the haystack: an upper-case query and the equivalent
// lower-case query must produce the same candidate set and the same ordering,
// because equalFold treats them identically. If anyone later re-introduces a
// manual strings.ToLower, this guards that the result stays consistent.
func TestApplyFilter_RanksCaseVariantsEqually(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/b/alpha-beta-cache", Label: "alpha-beta-cache"},
		{Path: "/a/abc-service", Label: "abc-service"},
	}
	lower := NewModel(cloneCandidates(cands), nil)
	lower.query = "abc"
	lower.applyFilter()

	upper := NewModel(cloneCandidates(cands), nil)
	upper.query = "ABC"
	upper.applyFilter()

	if got := filteredLabels(lower); !reflect.DeepEqual(got, filteredLabels(upper)) {
		t.Errorf("query case must not change ranking:\n  lower: %v\n  upper: %v", filteredLabels(lower), filteredLabels(upper))
	}
}

// TestApplyFilter_CursorClampsAfterMovingThenNarrowing guards the cursor-clamp
// branch in applyFilter (model.go) for the case where the cursor has already
// moved away from 0 *before* a query narrows the result set below the
// cursor's position — the scenario every other applyFilter test above skips,
// since they all filter while the cursor sits at its zero-value.
//
// Five candidates start in provider order with no query. The cursor is moved
// down twice (via real "down" key messages, exactly like production input)
// to land on index 2 ("gadget-charlie"). Typing "widget" then narrows
// m.filtered to just the two candidates whose label contains "widget"
// ("widget-alpha" and "widget-echo") — neither of which is the candidate the
// cursor was previously on, and both indices land below cursor position 2.
// applyFilter's clamp (`if m.cursor >= len(m.filtered) { ... }`) must pull the
// cursor back into range, and currentCandidate() must then resolve to one of
// the two surviving "widget" candidates — never "gadget-charlie" (which fell
// out of the filtered set) and never a zero-value/invalid candidate.
func TestApplyFilter_CursorClampsAfterMovingThenNarrowing(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/w/widget-alpha", Label: "widget-alpha"},
		{Path: "/o/orange-fruit", Label: "orange-fruit"},
		{Path: "/g/gadget-charlie", Label: "gadget-charlie"},
		{Path: "/g/gizmo-delta", Label: "gizmo-delta"},
		{Path: "/w/widget-echo", Label: "widget-echo"},
	}
	m := NewModel(cands, nil)

	// Move the cursor down twice with real key messages, same as production
	// input, landing on index 2 ("gadget-charlie") while the query is still
	// empty and all 5 candidates are shown.
	for i := 0; i < 2; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		mm, ok := updated.(Model)
		if !ok {
			t.Fatalf("expected Model, got %T", updated)
		}
		m = mm
	}
	if m.cursor != 2 {
		t.Fatalf("setup failed: cursor = %d after two down presses, want 2", m.cursor)
	}
	if cand, ok := m.currentCandidate(); !ok || cand.Label != "gadget-charlie" {
		t.Fatalf("setup failed: cursor is on %+v (ok=%v), want gadget-charlie", cand, ok)
	}

	// Narrow the filtered set to 2 candidates, both of which sort below the
	// prior cursor position (2) and neither of which is the candidate the
	// cursor was previously on.
	m.query = "widget"
	m.applyFilter()

	if got := filteredLabels(m); len(got) != 2 {
		t.Fatalf("setup failed: query %q filtered to %v, want exactly 2 candidates", m.query, got)
	}

	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		t.Fatalf("cursor not clamped into range: cursor = %d, len(filtered) = %d", m.cursor, len(m.filtered))
	}

	cand, ok := m.currentCandidate()
	if !ok {
		t.Fatal("currentCandidate() ok = false after narrowing; want a valid highlighted candidate")
	}
	if cand.Label != "widget-alpha" && cand.Label != "widget-echo" {
		t.Errorf("currentCandidate() after clamp = %q, want one of {widget-alpha, widget-echo} (must not point at the removed gadget-charlie or a stale/invalid candidate)", cand.Label)
	}

	// Selecting now (enter) must resolve through the same clamped cursor to
	// the same valid candidate, proving the clamp isn't only correct for
	// currentCandidate()'s own defensive re-clamp but for the real selection
	// path too.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	selected, ok := mm.Selected()
	if !ok {
		t.Fatal("Selected() ok = false after enter; want the clamped candidate to be selectable")
	}
	if selected.Label != cand.Label {
		t.Errorf("Selected() = %q, want it to match the clamped currentCandidate() = %q", selected.Label, cand.Label)
	}
}

// cloneCandidates returns a shallow copy so two models built from the same
// fixture cannot alias each other's candidate slice.
func cloneCandidates(in []source.Candidate) []source.Candidate {
	out := make([]source.Candidate, len(in))
	copy(out, in)
	return out
}

// --- --target=tab|pane TUI bindings (ctrl+t / ctrl+p) ---

// TestHandleKey_CtrlT_SetsTabTargetSelectsAndQuits proves ctrl+t, when shep
// is running inside a Herdr pane (currentPane != nil) and the highlighted
// candidate is a Command-only workspace, selects the highlighted candidate
// exactly like enter, records "tab" as the chosen launch target, and quits.
func TestHandleKey_CtrlT_SetsTabTargetSelectsAndQuits(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	m := NewModel([]source.Candidate{commandOnlyCandidate()}, nil).WithCurrentPane(&pane)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.ChosenTarget() != "tab" {
		t.Errorf("ChosenTarget() = %q, want %q", mm.ChosenTarget(), "tab")
	}
	if cmd == nil {
		t.Fatal("expected a quit Cmd after ctrl+t")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Errorf("expected cmd() to be tea.QuitMsg, got %T", cmd())
	}
	if _, ok := mm.Selected(); !ok {
		t.Error("expected ctrl+t to also select the highlighted candidate, like enter")
	}
}

// TestHandleKey_CtrlP_SetsPaneTargetSelectsAndQuits mirrors the ctrl+t test
// for ctrl+p / "pane".
func TestHandleKey_CtrlP_SetsPaneTargetSelectsAndQuits(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	m := NewModel([]source.Candidate{commandOnlyCandidate()}, nil).WithCurrentPane(&pane)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.ChosenTarget() != "pane" {
		t.Errorf("ChosenTarget() = %q, want %q", mm.ChosenTarget(), "pane")
	}
	if cmd == nil {
		t.Fatal("expected a quit Cmd after ctrl+p")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Errorf("expected cmd() to be tea.QuitMsg, got %T", cmd())
	}
	if _, ok := mm.Selected(); !ok {
		t.Error("expected ctrl+p to also select the highlighted candidate, like enter")
	}
}

// TestHandleKey_CtrlT_EmptyFiltered_IsNoOp mirrors the no-current-pane no-op
// guard: when the candidate list is filtered down to nothing (e.g. a query
// with zero matches), ctrl+t must not set a chosenTarget or quit — there is
// no highlighted candidate to launch, and setting chosenTarget anyway would
// make runOpen's disallowTarget surface a confusing "requires an entry with
// a command" error for a launch that never had a candidate at all.
func TestHandleKey_CtrlT_EmptyFiltered_IsNoOp(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	m := NewModel(nil, nil).WithCurrentPane(&pane)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.ChosenTarget() != "" {
		t.Errorf("ChosenTarget() = %q, want empty when filtered is empty", mm.ChosenTarget())
	}
	if cmd != nil {
		t.Error("expected a nil Cmd (no quit) when ctrl+t fires with an empty filtered list")
	}
	if _, ok := mm.Selected(); ok {
		t.Error("expected no selection when ctrl+t fires with an empty filtered list")
	}
}

// TestHandleKey_Enter_ChosenTargetStaysEmpty proves enter never sets a
// target override: ChosenTarget stays "" even when currentPane is set, so
// runOpen falls back to the --target flag value unchanged.
func TestHandleKey_Enter_ChosenTargetStaysEmpty(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	m := NewModel(internalTestCands(), nil).WithCurrentPane(&pane)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.ChosenTarget() != "" {
		t.Errorf("ChosenTarget() = %q, want empty after enter", mm.ChosenTarget())
	}
}

// TestHandleKey_CtrlT_NoCurrentPane_IsNoOp proves ctrl+t is disabled — no
// selection, no target, no quit — when shep is not running inside a Herdr
// pane (currentPane == nil).
func TestHandleKey_CtrlT_NoCurrentPane_IsNoOp(t *testing.T) {
	t.Parallel()
	m := NewModel(internalTestCands(), nil) // currentPane stays nil

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.ChosenTarget() != "" {
		t.Errorf("ChosenTarget() = %q, want empty when no current pane", mm.ChosenTarget())
	}
	if cmd != nil {
		t.Error("expected a nil Cmd (no quit) when ctrl+t fires with no current pane")
	}
	if _, ok := mm.Selected(); ok {
		t.Error("expected no selection when ctrl+t fires with no current pane")
	}
}

// TestHandleKey_CtrlP_NoCurrentPane_IsNoOp mirrors the ctrl+t no-op test for
// ctrl+p / "pane".
func TestHandleKey_CtrlP_NoCurrentPane_IsNoOp(t *testing.T) {
	t.Parallel()
	m := NewModel(internalTestCands(), nil)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	mm, ok := updated.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", updated)
	}
	if mm.ChosenTarget() != "" {
		t.Errorf("ChosenTarget() = %q, want empty when no current pane", mm.ChosenTarget())
	}
	if cmd != nil {
		t.Error("expected a nil Cmd (no quit) when ctrl+p fires with no current pane")
	}
}

// TestHandleKey_CtrlT_NonCommandOnlyEntry_IsNoOp proves ctrl+t is a no-op —
// no selection, no chosenTarget, no quit — when shep IS running inside a
// Herdr pane (currentPane != nil) but the highlighted candidate cannot be
// launched as a tab/pane target: a group workspace or a template workspace.
// Before this guard, ctrl+t on either entry set chosenTarget and quit the
// TUI, only for App.launchInCurrentWorkspace to fail afterward with
// "requires an entry with a command" — this proves the TUI now stays put
// silently instead.
func TestHandleKey_CtrlT_NonCommandOnlyEntry_IsNoOp(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	tests := []struct {
		name string
		cand source.Candidate
	}{
		{"group", groupCandidate()},
		{"template", templateCandidate()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewModel([]source.Candidate{tt.cand}, nil).WithCurrentPane(&pane)

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
			mm, ok := updated.(Model)
			if !ok {
				t.Fatalf("expected Model, got %T", updated)
			}
			if mm.ChosenTarget() != "" {
				t.Errorf("ChosenTarget() = %q, want empty for a non-Command-only entry", mm.ChosenTarget())
			}
			if cmd != nil {
				t.Error("expected a nil Cmd (no quit) when ctrl+t fires on a non-Command-only entry")
			}
			if _, ok := mm.Selected(); ok {
				t.Error("expected no selection when ctrl+t fires on a non-Command-only entry")
			}
		})
	}
}

// TestHandleKey_CtrlP_NonCommandOnlyEntry_IsNoOp mirrors the ctrl+t no-op
// test above for ctrl+p / "pane".
func TestHandleKey_CtrlP_NonCommandOnlyEntry_IsNoOp(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	tests := []struct {
		name string
		cand source.Candidate
	}{
		{"group", groupCandidate()},
		{"template", templateCandidate()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewModel([]source.Candidate{tt.cand}, nil).WithCurrentPane(&pane)

			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
			mm, ok := updated.(Model)
			if !ok {
				t.Fatalf("expected Model, got %T", updated)
			}
			if mm.ChosenTarget() != "" {
				t.Errorf("ChosenTarget() = %q, want empty for a non-Command-only entry", mm.ChosenTarget())
			}
			if cmd != nil {
				t.Error("expected a nil Cmd (no quit) when ctrl+p fires on a non-Command-only entry")
			}
			if _, ok := mm.Selected(); ok {
				t.Error("expected no selection when ctrl+p fires on a non-Command-only entry")
			}
		})
	}
}

// --- candidateIsCommandOnly ---

// TestCandidateIsCommandOnly_CommandOnlyWorkspace proves a plain command
// entry (no group/template) reports true.
func TestCandidateIsCommandOnly_CommandOnlyWorkspace(t *testing.T) {
	t.Parallel()
	if !candidateIsCommandOnly(commandOnlyCandidate()) {
		t.Error("expected candidateIsCommandOnly() = true for a Command-only workspace")
	}
}

// TestCandidateIsCommandOnly_GroupWorkspace proves a group workspace reports
// false, even though App-side "group" entries never carry a command anyway
// — the check must still hold if one somehow did (see the edge case test).
func TestCandidateIsCommandOnly_GroupWorkspace(t *testing.T) {
	t.Parallel()
	if candidateIsCommandOnly(groupCandidate()) {
		t.Error("expected candidateIsCommandOnly() = false for a group workspace")
	}
}

// TestCandidateIsCommandOnly_TemplateWorkspace proves a template workspace
// reports false.
func TestCandidateIsCommandOnly_TemplateWorkspace(t *testing.T) {
	t.Parallel()
	if candidateIsCommandOnly(templateCandidate()) {
		t.Error("expected candidateIsCommandOnly() = false for a template workspace")
	}
}

// TestCandidateIsCommandOnly_PlainSource proves a plain source entry (no
// command Meta at all) reports false.
func TestCandidateIsCommandOnly_PlainSource(t *testing.T) {
	t.Parallel()
	if candidateIsCommandOnly(source.Candidate{Path: "/plain", Label: "plain"}) {
		t.Error("expected candidateIsCommandOnly() = false for a plain source entry")
	}
}

// TestCandidateIsCommandOnly_CommandAndGroup_Edge proves a candidate with
// both command and group Meta set reports false (group wins).
func TestCandidateIsCommandOnly_CommandAndGroup_Edge(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{Meta: map[string]string{"command": "x", "group": "true"}}
	if candidateIsCommandOnly(cand) {
		t.Error("expected candidateIsCommandOnly() = false when group is also set")
	}
}

// TestCandidateIsCommandOnly_CommandAndTemplate_Edge proves a candidate with
// both command and template Meta set reports false (template wins).
func TestCandidateIsCommandOnly_CommandAndTemplate_Edge(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{Meta: map[string]string{"command": "x", "template": "y"}}
	if candidateIsCommandOnly(cand) {
		t.Error("expected candidateIsCommandOnly() = false when template is also set")
	}
}

// --- renderList has no entry type tags ---

// TestRenderList_NoEntryTypeTags proves the rendered list never carries a
// bracketed entry-type tag ("[cmd]", "[grp]", "[tpl]") on any row, for a
// Command-only workspace, a group workspace, a template workspace, or a
// plain source entry. The footer's context-sensitive ctrl+t/ctrl+p hint
// (see hintsFor) is the sole indicator of "openable as tab/pane" — the
// per-row tags were removed because they broke the list's visual structure.
func TestRenderList_NoEntryTypeTags(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		commandOnlyCandidate(),
		groupCandidate(),
		templateCandidate(),
		{Path: "/plain/path"},
	}
	m := NewModel(cands, nil)
	out := m.renderList(60)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// lines[0] is the query line; candidate rows follow in provider order.
	if len(lines) != 5 {
		t.Fatalf("renderList produced %d lines, want 5 (query + 4 rows):\n%s", len(lines), out)
	}
	for i, row := range lines[1:] {
		for _, tag := range []string{"[cmd]", "[grp]", "[tpl]"} {
			if strings.Contains(row, tag) {
				t.Errorf("row %d = %q, want no entry type tag, found %q", i, row, tag)
			}
		}
	}
}

// --- footer keybinding hints (merged into the footer line) ---

// TestHintsFor_NoCurrentPane_ExcludesTabPaneHints proves hintsFor omits the
// ctrl+t/ctrl+p hints entirely (not dimmed — absent) when shep is not
// running inside a Herdr pane, regardless of the highlighted candidate,
// since selectWithTarget always no-ops both bindings in that case.
func TestHintsFor_NoCurrentPane_ExcludesTabPaneHints(t *testing.T) {
	t.Parallel()
	got := hintsFor(commandOnlyCandidate(), false)
	for _, want := range []string{"enter: open", "esc: cancel", "ctrl+l: layout"} {
		if !strings.Contains(got, want) {
			t.Errorf("hintsFor() = %q, want to contain %q", got, want)
		}
	}
	for _, absent := range []string{"ctrl+t", "ctrl+p"} {
		if strings.Contains(got, absent) {
			t.Errorf("hintsFor() = %q, want no %q hint (no current pane)", got, absent)
		}
	}
}

// TestHintsFor_CurrentPaneCommandOnly_IncludesTabPaneHints proves hintsFor
// includes ALL hints — enter/ctrl+t/ctrl+p/esc/ctrl+l — when shep IS
// running inside a Herdr pane AND the highlighted candidate is a
// Command-only workspace, the only entry selectWithTarget actually launches.
func TestHintsFor_CurrentPaneCommandOnly_IncludesTabPaneHints(t *testing.T) {
	t.Parallel()
	got := hintsFor(commandOnlyCandidate(), true)
	for _, want := range []string{"enter: open", "ctrl+t: tab", "ctrl+p: pane", "esc: cancel", "ctrl+l: layout"} {
		if !strings.Contains(got, want) {
			t.Errorf("hintsFor() = %q, want to contain %q", got, want)
		}
	}
}

// TestHintsFor_CurrentPaneNonCommandOnly_ExcludesTabPaneHints proves hintsFor
// still omits ctrl+t/ctrl+p when shep IS running inside a Herdr pane but the
// highlighted candidate is a group or template workspace — selectWithTarget
// would no-op the binding on either, so advertising it would be misleading.
func TestHintsFor_CurrentPaneNonCommandOnly_ExcludesTabPaneHints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cand source.Candidate
	}{
		{"group", groupCandidate()},
		{"template", templateCandidate()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hintsFor(tt.cand, true)
			for _, want := range []string{"enter: open", "esc: cancel", "ctrl+l: layout"} {
				if !strings.Contains(got, want) {
					t.Errorf("hintsFor() = %q, want to contain %q", got, want)
				}
			}
			for _, absent := range []string{"ctrl+t", "ctrl+p"} {
				if strings.Contains(got, absent) {
					t.Errorf("hintsFor() = %q, want no %q hint (non-Command-only entry)", got, absent)
				}
			}
		})
	}
}

// TestRenderFooter_NoCurrentPane_ExcludesTabPaneHints proves renderFooter
// itself (not just hintsFor in isolation) omits ctrl+t/ctrl+p when there is
// no current pane.
func TestRenderFooter_NoCurrentPane_ExcludesTabPaneHints(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{commandOnlyCandidate()}, nil)
	m.width = 100

	footer := m.renderFooter()
	if strings.Contains(footer, "ctrl+t") || strings.Contains(footer, "ctrl+p") {
		t.Errorf("expected no ctrl+t/ctrl+p hints without a current pane, got: %q", footer)
	}
	if !strings.Contains(footer, "enter: open") || !strings.Contains(footer, "ctrl+l: layout") {
		t.Errorf("expected the always-live hints present, got: %q", footer)
	}
}

// TestRenderFooter_CurrentPaneCommandOnly_IncludesTabPaneHints proves
// renderFooter shows ctrl+t/ctrl+p when shep is running inside a Herdr pane
// and the highlighted candidate is Command-only.
func TestRenderFooter_CurrentPaneCommandOnly_IncludesTabPaneHints(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	m := NewModel([]source.Candidate{commandOnlyCandidate()}, nil).WithCurrentPane(&pane)
	m.width = 100

	footer := m.renderFooter()
	if !strings.Contains(footer, "ctrl+t: tab") || !strings.Contains(footer, "ctrl+p: pane") {
		t.Errorf("expected footer to show ctrl+t/ctrl+p hints for a Command-only entry, got: %q", footer)
	}
}

// TestRenderFooter_CurrentPaneNonCommandOnly_ExcludesTabPaneHints proves
// renderFooter hides ctrl+t/ctrl+p when the highlighted candidate is a group
// or template workspace, even with a current pane set.
func TestRenderFooter_CurrentPaneNonCommandOnly_ExcludesTabPaneHints(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	tests := []struct {
		name string
		cand source.Candidate
	}{
		{"group", groupCandidate()},
		{"template", templateCandidate()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := NewModel([]source.Candidate{tt.cand}, nil).WithCurrentPane(&pane)
			m.width = 100

			footer := m.renderFooter()
			if strings.Contains(footer, "ctrl+t") || strings.Contains(footer, "ctrl+p") {
				t.Errorf("expected no ctrl+t/ctrl+p hints for a non-Command-only entry, got: %q", footer)
			}
		})
	}
}

// TestView_FooterIncludesKeybindingHints proves View()'s rendered output
// includes the merged footer/hints line end-to-end, not just renderFooter in
// isolation.
func TestView_FooterIncludesKeybindingHints(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p1"}
	m := NewModel([]source.Candidate{commandOnlyCandidate()}, nil).WithCurrentPane(&pane)
	m.width = 100
	m.height = 24

	view := m.View()
	if !strings.Contains(view, "ctrl+t: tab") || !strings.Contains(view, "ctrl+p: pane") {
		t.Errorf("expected View() to include the ctrl+t/ctrl+p hints, got:\n%s", view)
	}
}

// TestRenderFooter_NarrowWidth_PrefersHintsOverLabel proves that when the
// composed footer line (label + separator + hints) does not fit m.width,
// the hints are kept intact (they are the actionable part) and the label is
// truncated instead — the reverse of naively truncating the whole string,
// which would eat into the hints first since they come last.
func TestRenderFooter_NarrowWidth_PrefersHintsOverLabel(t *testing.T) {
	t.Parallel()
	longLabel := strings.Repeat("x", 200)
	m := NewModel([]source.Candidate{{Path: "/y", Label: longLabel}}, nil)
	m.width = 60

	footer := m.renderFooter()
	if w := lipgloss.Width(footer); w > m.width {
		t.Fatalf("footer width = %d, must not exceed terminal width %d: %q", w, m.width, footer)
	}
	for _, want := range []string{"enter: open", "esc: cancel", "ctrl+l: layout"} {
		if !strings.Contains(footer, want) {
			t.Errorf("footer = %q, want the full hints preserved (%q missing)", footer, want)
		}
	}
	if strings.Contains(footer, longLabel) {
		t.Errorf("footer = %q, want the label truncated at this narrow width", footer)
	}
	if !strings.Contains(footer, "…") {
		t.Errorf("footer = %q, want a truncation ellipsis on the label", footer)
	}
}
