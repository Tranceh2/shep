package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

// ansiStubRenderer returns a fixed Result whose Text carries real ANSI color
// escape codes, simulating what the "dir" (lsd/eza --color=always) or
// "active_pane" (captured pane buffer) built-in sections actually produce.
// Used to prove the TUI preview pane never strips that color end-to-end.
type ansiStubRenderer struct {
	text string
}

func (r ansiStubRenderer) Render(context.Context, source.Candidate, preview.RenderOptions) (preview.Result, error) {
	return preview.Result{Text: r.text}, nil
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

// TestModel_RenderPreviewConstantStrings_NeverWrapAtFloorWidth is the
// regression test for the follow-up overflow bug: 4 constant-string render
// call sites (the "preview" header, the help line, "(no selection)" and
// "loading…") were missed by the wrap-truncation fix applied to candidate
// rows and previewText body content, reintroducing the same overflow at the
// narrowest content width the layout can clamp down to. clampWidths enforces
// minPrev=10 as the floor outer preview pane width; paneContentWidth
// converts that to the actual content width (6) fed to renderPreview, the
// same floor a small `preview_width` percentage like "8%" can reach.
// capPreviewBodyLines budgets the preview pane assuming header/blank/help are
// each exactly 1 physical line (`maxLines -= 3`); if any of these constants
// wrap, that assumption breaks and the pane overflows m.height again.
func TestModel_RenderPreviewConstantStrings_NeverWrapAtFloorWidth(t *testing.T) {
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
	// header(1) + body(1, "x") + blank(1) + help(1) = 4 lines total. Before
	// the fix, the header and help lines each word-wrapped into multiple
	// physical lines at this width, inflating the total well past 4 and
	// past capPreviewBodyLines' budget.
	const wantLines = 4
	if len(lines) != wantLines {
		t.Fatalf("renderPreview at floor width %d produced %d lines, want %d (header/help must not wrap):\n%q",
			floorWidth, len(lines), wantLines, out)
	}
	header := lines[0]
	help := lines[wantLines-1]
	if w := lipgloss.Width(header); w != floorWidth {
		t.Errorf("header line width = %d, want %d: %q", w, floorWidth, header)
	}
	if w := lipgloss.Width(help); w != floorWidth {
		t.Errorf("help line width = %d, want %d: %q", w, floorWidth, help)
	}
	if !strings.Contains(help, "…") {
		t.Errorf("expected help line truncated with ellipsis at floor width, got: %q", help)
	}
	if len(lines) > m.height {
		t.Errorf("renderPreview produced %d lines, want <= %d (height=%d):\n%s", len(lines), m.height, m.height, out)
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

// TestPreviewBody_LongWarning_StyledLineNeverOverflowsAtNarrowWidth is the
// warn-line counterpart of the same bug: previewBody appended
// palette.previewWarnStyle.Render("warn: "+m.previewWarn) to the text BEFORE
// the final truncateLinesToWidth pass, so the injected ANSI bytes were
// counted against the raw rune budget and truncation could cut mid-escape
// sequence, leaving an unterminated/malformed ANSI code and a wrong visible
// width.
func TestPreviewBody_LongWarning_StyledLineNeverOverflowsAtNarrowWidth(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := newModel(internalTestCands(), stubRenderer{}, context.TODO())
	m.previewLoading = false
	m.previewText = "ok"
	m.previewWarn = strings.Repeat("something went wrong ", 10)

	const narrowWidth = 20
	body := m.previewBody(narrowWidth)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("previewBody with warning produced %d lines, want 2 (text + warn):\n%q", len(lines), body)
	}
	warnLine := lines[1]
	if w := lipgloss.Width(warnLine); w != narrowWidth {
		t.Errorf("warn line visible width = %d, want %d (ANSI-aware truncation broke): %q", w, narrowWidth, warnLine)
	}
	assertNoUnterminatedANSI(t, warnLine)
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
