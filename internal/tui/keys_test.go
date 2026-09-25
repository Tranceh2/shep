package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return mm, cmd
}

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+t":
		return tea.KeyMsg{Type: tea.KeyCtrlT}
	case "ctrl+p":
		return tea.KeyMsg{Type: tea.KeyCtrlP}
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}
	case "ctrl+l":
		return tea.KeyMsg{Type: tea.KeyCtrlL}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+j":
		return tea.KeyMsg{Type: tea.KeyCtrlJ}
	case "ctrl+k":
		return tea.KeyMsg{Type: tea.KeyCtrlK}
	case "ctrl+g":
		return tea.KeyMsg{Type: tea.KeyCtrlG}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "?":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// --- Focus + viewport scrolling ---

// TestCycleFocus_TabAndShiftTabWrapListAndPreview proves Tab/Shift+Tab
// cycles focus between the list and the preview pane in both directions,
// wrapping at each end of the ring (List<->Preview is the whole ring, so
// TestCycleScope_TabAndShiftTabWrapAllAndAgents proves Tab and Shift+Tab
// cycle the filter scope between ScopeAll and ScopeAgents.
func TestCycleScope_TabAndShiftTabWrapAllAndAgents(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	if m.scope != ScopeAll {
		t.Fatalf("initial scope = %v, want ScopeAll", m.scope)
	}
	m, _ = update(t, m, key("tab"))
	if m.scope != ScopeAgents {
		t.Errorf("after tab: scope = %v, want ScopeAgents", m.scope)
	}
	m, _ = update(t, m, key("tab")) // wraps forward ScopeAgents -> ScopeAll
	if m.scope != ScopeAll {
		t.Errorf("after 2nd tab (wrap): scope = %v, want ScopeAll", m.scope)
	}
	m, _ = update(t, m, key("shift+tab")) // wraps backward ScopeAll -> ScopeAgents
	if m.scope != ScopeAgents {
		t.Errorf("after shift+tab (wrap): scope = %v, want ScopeAgents", m.scope)
	}
	m, _ = update(t, m, key("shift+tab"))
	if m.scope != ScopeAll {
		t.Errorf("after 2nd shift+tab: scope = %v, want ScopeAll", m.scope)
	}
}

// TestCycleScope_WorksInListOnlyMode proves Tab/Shift+Tab cycle scopes
// even when the current layout has no preview pane available.
func TestCycleScope_WorksInListOnlyMode(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.mode = modeListOnly
	m, _ = update(t, m, key("tab"))
	if m.scope != ScopeAgents {
		t.Errorf("tab in list-only mode: scope = %v, want ScopeAgents", m.scope)
	}
	m, _ = update(t, m, key("shift+tab"))
	if m.scope != ScopeAll {
		t.Errorf("shift+tab in list-only mode: scope = %v, want ScopeAll", m.scope)
	}
}

// TestCycleFocus_NoOpInFocusHelp proves Tab/Shift+Tab never leave FocusHelp
// — help is modal, not a ring member.
func TestCycleFocus_NoOpInFocusHelp(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.focus = FocusHelp
	m, _ = update(t, m, key("tab"))
	if m.focus != FocusHelp {
		t.Errorf("tab while FocusHelp: focus = %v, want unchanged FocusHelp", m.focus)
	}
	m, _ = update(t, m, key("shift+tab"))
	if m.focus != FocusHelp {
		t.Errorf("shift+tab while FocusHelp: focus = %v, want unchanged FocusHelp", m.focus)
	}
}

// --- Resize-triggered focus correction (fix round 1, Candidate A) ---
//
// A user can Tab into FocusPreview at a wide size, then shrink the terminal
// to a size that resolves to modeListOnly (preview pane hidden). Without a
// correction, focus is left stranded at FocusPreview: cycleFocusForward/
// Backward are no-ops in modeListOnly (nothing to Tab back to), and
// handlePreviewFocusedKey keeps routing every key since routing is keyed on
// m.focus, not m.mode — so Down/Enter/Tab are all silently swallowed. See
// Update's tea.WindowSizeMsg handling in model.go for the fix.

// TestResize_ToListOnly_FromFocusPreview_RefocusesList proves that shrinking
// the terminal to a modeListOnly size while FocusPreview is active
// automatically refocuses FocusList, so the user is never stranded.
func TestResize_ToListOnly_FromFocusPreview_RefocusesList(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36)) // wide: preview reachable
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, sizeMsg(64, 24)) // shrink to a list-only size
	if m.mode != modeListOnly {
		t.Fatalf("setup: expected mode = modeListOnly after shrink, got %v", m.mode)
	}
	if m.focus != FocusList {
		t.Errorf("focus after shrink-to-list-only = %v, want FocusList (must not strand focus on an unavailable preview)", m.focus)
	}
}

// TestResize_ToListOnly_WithHelpOpenPrevFocusPreview_DegradesPrevFocus
// proves the FocusHelp/prevFocus corollary: opening Help while FocusPreview
// records prevFocus=FocusPreview; shrinking to modeListOnly while Help is
// still open must degrade that recorded prevFocus to FocusList, so closing
// Help afterwards lands on FocusList instead of restoring a stale,
// unavailable FocusPreview.
func TestResize_ToListOnly_WithHelpOpenPrevFocusPreview_DegradesPrevFocus(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.focus = FocusPreview
	m, _ = update(t, m, key("?")) // open help; prevFocus = FocusPreview
	if m.focus != FocusHelp || m.prevFocus != FocusPreview {
		t.Fatalf("setup: focus = %v, prevFocus = %v, want FocusHelp/FocusPreview", m.focus, m.prevFocus)
	}
	m, _ = update(t, m, sizeMsg(64, 24)) // shrink to list-only while help is open
	if m.mode != modeListOnly {
		t.Fatalf("setup: expected mode = modeListOnly after shrink, got %v", m.mode)
	}
	if m.focus != FocusHelp {
		t.Fatalf("setup: expected help to remain open across the resize, focus = %v", m.focus)
	}
	m, _ = update(t, m, key("esc")) // close help
	if m.focus != FocusList {
		t.Errorf("focus after closing help post-shrink = %v, want FocusList (prevFocus must have degraded, not restored a stale FocusPreview)", m.focus)
	}
}

// TestPreviewFocused_ArrowsScrollViewportNotCursor proves that while the
// preview pane owns focus, up/down move the viewport's scroll offset
// instead of the list cursor. The preview body for a RowCandidate is built
// from the renderer's structured Result.Sections (see
// preview_body.go:standardCandidatePreview), not the raw previewText field
// (which only backs RowPane captures) — so the fixture must populate
// previewSections with enough multiline content to overflow the viewport.
func TestPreviewFocused_ArrowsScrollViewportNotCursor(t *testing.T) {
	t.Parallel()
	longText := ""
	for i := 0; i < 100; i++ {
		longText += "line\n"
	}
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a"), zoxideCandidate("b", "/b")}, stubRenderer{})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.previewSections = []preview.Section{{Kind: config.PreviewDir, Text: longText}}
	m.previewLoading = false
	m.syncViewport()
	m.focus = FocusPreview
	startCursor := m.cursor
	startOffset := m.viewport.YOffset

	m, _ = update(t, m, key("down"))
	if m.cursor != startCursor {
		t.Errorf("list cursor moved (%d -> %d) while preview focused, want unchanged", startCursor, m.cursor)
	}
	if m.viewport.YOffset <= startOffset {
		t.Errorf("viewport.YOffset = %d, want > %d after scrolling down while focused", m.viewport.YOffset, startOffset)
	}
}

// TestPreviewFocused_PrintableRuneReturnsToListAndSearches proves the
// non-negotiable "a printable rune returns to list and searches" contract.
func TestPreviewFocused_PrintableRuneReturnsToListAndSearches(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("alpha", "/alpha"), zoxideCandidate("beta", "/beta")}, nil)
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, key("b"))
	if m.focus != FocusList {
		t.Errorf("after printable rune while preview-focused: focus = %v, want FocusList", m.focus)
	}
	if m.query != "b" {
		t.Errorf("query = %q, want \"b\" (the rune must extend the query)", m.query)
	}
}

// TestPreviewFocused_CtrlUReturnsToListAndClearsQuery proves ctrl+u while
// the preview pane owns focus refocuses List and clears the query in the
// same step (mirrors the printable-rune and backspace contracts, not the
// viewport's own half-page-scroll binding).
func TestPreviewFocused_CtrlUReturnsToListAndClearsQuery(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("alpha", "/alpha"), zoxideCandidate("beta", "/beta")}, nil)
	m.query = "al"
	m.applyFilter()
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, key("ctrl+u"))
	if m.focus != FocusList {
		t.Errorf("after ctrl+u while preview-focused: focus = %v, want FocusList", m.focus)
	}
	if m.query != "" {
		t.Errorf("query = %q, want cleared", m.query)
	}
}

// TestPreviewFocused_BackspaceReturnsToListAndDeletes proves backspace while
// the preview pane owns focus refocuses List and deletes the last query
// rune in the same step.
func TestPreviewFocused_BackspaceReturnsToListAndDeletes(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("alpha", "/alpha"), zoxideCandidate("beta", "/beta")}, nil)
	m.query = "al"
	m.applyFilter()
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, key("backspace"))
	if m.focus != FocusList {
		t.Errorf("after backspace while preview-focused: focus = %v, want FocusList", m.focus)
	}
	if m.query != "a" {
		t.Errorf("query = %q, want \"a\" (last rune deleted)", m.query)
	}
}

// --- Enter: row selection ---

// TestEnter_OnCandidateSelectsAndQuits proves Enter on a row selects it and
// requests tea.Quit. Group headers no longer exist, so every row (including
// row 0) is a plain candidate.
func TestEnter_OnCandidateSelectsAndQuits(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.cursor = 0
	m, cmd := update(t, m, key("enter"))
	if cmd == nil {
		t.Fatal("expected a quit Cmd from selecting a candidate")
	}
	got, ok := m.Selected()
	if !ok || got.Label != "a" {
		t.Errorf("Selected() = %+v (ok=%v), want candidate \"a\"", got, ok)
	}
}

// --- Expand/collapse ---

// TestLeftRight_ExpandCollapseWorkspace proves Right expands a workspace's
// tab/pane children at an empty query and Left collapses them again.
func TestLeftRight_ExpandCollapseWorkspace(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}}}
	tree := treeFromFake(driver)
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m.cursor = 0 // the workspace row (no group header anymore)

	m, _ = update(t, m, key("right"))
	foundTab := false
	for _, r := range m.rows {
		if r.Kind == RowTab {
			foundTab = true
		}
	}
	if !foundTab {
		t.Fatalf("expected a RowTab after Right on an expandable workspace, rows=%+v", m.rows)
	}

	// Move cursor back onto the workspace row before collapsing (Left
	// collapses whichever row is highlighted).
	for i, r := range m.rows {
		if r.Kind == RowCandidate {
			m.cursor = i
		}
	}
	m, _ = update(t, m, key("left"))
	for _, r := range m.rows {
		if r.Kind == RowTab {
			t.Fatalf("expected no RowTab after Left collapses the workspace, rows=%+v", m.rows)
		}
	}
}

// --- ctrl+t / ctrl+p target matrix, incl. unsupported pane action semantics ---

// TestSelectWithTarget_RequiresCurrentPaneAndSupportedCandidate proves
// ctrl+t/ctrl+p are no-ops without a current pane, and no-ops on an
// unsupported candidate (e.g. an already-open Herdr workspace row).
func TestSelectWithTarget_RequiresCurrentPaneAndSupportedCandidate(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.cursor = 0
	m, cmd := update(t, m, key("ctrl+t"))
	if cmd != nil {
		t.Error("ctrl+t without a current pane must be a no-op")
	}

	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)
	m.cursor = 0
	m, cmd = update(t, m, key("ctrl+t"))
	if cmd == nil {
		t.Fatal("ctrl+t on a zoxide candidate with a current pane should select+quit")
	}
	if m.ChosenTarget() != "tab" {
		t.Errorf("ChosenTarget() = %q, want \"tab\"", m.ChosenTarget())
	}
}

func TestSelectWithTarget_NoFocusKeepsSelectorUsableWithFeedback(t *testing.T) {
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, cmd := update(t, m, key("ctrl+p"))
	if cmd != nil {
		t.Error("ctrl+p without a focused pane must not select or quit")
	}
	if _, selected := m.Selected(); selected {
		t.Error("ctrl+p without focus unexpectedly selected a candidate")
	}
	if m.previewErr != "no focused Herdr pane" {
		t.Errorf("previewErr = %q, want concise no-focus feedback", m.previewErr)
	}
}

// TestSelectWithTarget_HerdrTabAndPaneRowsAreUnsupported proves that
// ctrl+t/ctrl+p on a synthesized RowTab/RowPane are no-ops:
// source.SupportsCurrentWorkspaceTarget only recognizes zoxide/projects/
// command-workspaces, and a synthesized tab/pane candidate carries no Source
// at all (see internal/tui/tree.go's synthesizeWorkspaceChildren) so it falls
// through to the default false — the "unsupported pane action semantics"
// contract. Enter (not ctrl+t/ctrl+p) is the only supported action on a
// tab/pane row, and it routes (at the command layer) via the typed
// RowActionFocusTab to driver.FocusTab — never a per-pane focus call, because
// Herdr exposes none.
func TestSelectWithTarget_HerdrTabAndPaneRowsAreUnsupported(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}}}
	tree := treeFromFake(driver)
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()

	var tabIdx = -1
	for i, r := range m.rows {
		if r.Kind == RowTab {
			tabIdx = i
		}
	}
	if tabIdx < 0 {
		t.Fatalf("expected a RowTab row, got %+v", m.rows)
	}
	m.cursor = tabIdx
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)

	m, cmd := update(t, m, key("ctrl+t"))
	if cmd != nil {
		t.Error("ctrl+t on a RowTab must be a no-op")
	}
	if _, ok := m.Selected(); ok {
		t.Error("expected no selection from ctrl+t on a RowTab")
	}
}

// TestSelectWithTarget_WorksIdenticallyFromPreviewFocus proves ctrl+t/
// ctrl+p apply identically whether the list or the preview pane owns
// focus, gated only by currentPane != nil and
// source.SupportsCurrentWorkspaceTarget — the same rule handleKey applies
// as a global binding regardless of m.focus.
func TestSelectWithTarget_WorksIdenticallyFromPreviewFocus(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, cmd := update(t, m, key("ctrl+p"))
	if cmd == nil {
		t.Fatal("ctrl+p from FocusPreview with a current pane should select+quit")
	}
	if m.ChosenTarget() != "pane" {
		t.Errorf("ChosenTarget() = %q, want \"pane\"", m.ChosenTarget())
	}
}

// TestSelectWithTarget_NoOpInFocusHelp proves ctrl+t/ctrl+p are swallowed
// while FocusHelp, even with an otherwise-eligible current pane and
// candidate.
func TestSelectWithTarget_NoOpInFocusHelp(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)
	m.focus = FocusHelp
	m.prevFocus = FocusList
	m, cmd := update(t, m, key("ctrl+t"))
	if cmd != nil {
		t.Error("ctrl+t while FocusHelp must be a no-op")
	}
	if _, ok := m.Selected(); ok {
		t.Error("expected no selection from ctrl+t while FocusHelp")
	}
	if m.focus != FocusHelp {
		t.Errorf("focus = %v, want unchanged FocusHelp", m.focus)
	}
}

// === SPEC-NAV-4.1/4.2: target guard order (eligibility before missing-pane) ===

// TestSelectWithTarget_IneligibleNilPaneIsSilentNoOp proves SPEC-NAV-4.1: an
// ineligible row (a session candidate, which SupportsCurrentWorkspaceTarget
// rejects) with NO current pane is a fully silent no-op — nothing is selected
// AND no "no focused Herdr pane" error is set. The eligibility guard must run
// before the missing-pane error, so an ineligible row never triggers the
// pane-focus diagnostic it can never act on.
func TestSelectWithTarget_IneligibleNilPaneIsSilentNoOp(t *testing.T) {
	t.Parallel()
	sess := source.Candidate{Label: "alpha", Source: config.SourceSessions, Meta: map[string]string{"session_name": "alpha"}}
	m := NewModel([]source.Candidate{sess}, nil)
	m.cursor = 0
	if m.currentPane != nil {
		t.Fatal("setup: expected nil current pane")
	}
	m, cmd := update(t, m, key("ctrl+t"))
	if cmd != nil {
		t.Error("ctrl+t on an ineligible row with no pane must be a no-op")
	}
	if _, ok := m.Selected(); ok {
		t.Error("expected no selection on an ineligible row with no pane")
	}
	if m.previewErr != "" {
		t.Errorf("previewErr = %q, want empty (ineligible row must not trigger the missing-pane diagnostic)", m.previewErr)
	}
}

// TestSelectWithTarget_EligibleNilPanePreservesFocusError proves SPEC-NAV-4.1:
// an ELIGIBLE row (zoxide) with no current pane preserves the existing
// "no focused Herdr pane" diagnostic — the reorder must not weaken feedback
// for a row that would otherwise be actionable once a pane exists.
func TestSelectWithTarget_EligibleNilPanePreservesFocusError(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.cursor = 0
	if m.currentPane != nil {
		t.Fatal("setup: expected nil current pane")
	}
	m, cmd := update(t, m, key("ctrl+t"))
	if cmd != nil {
		t.Error("ctrl+t on an eligible row with no pane must not select or quit")
	}
	if _, ok := m.Selected(); ok {
		t.Error("expected no selection when the eligible row has no pane")
	}
	if m.previewErr != "no focused Herdr pane" {
		t.Errorf("previewErr = %q, want \"no focused Herdr pane\" (eligible row keeps existing feedback)", m.previewErr)
	}
}

// TestSelectWithTarget_EligiblePaneDispatchUnchanged proves SPEC-NAV-4.2: an
// eligible row WITH a current pane keeps the unchanged select+quit dispatch
// and target selection — the guard reorder must not disturb the working path.
func TestSelectWithTarget_EligiblePaneDispatchUnchanged(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)
	m.cursor = 0
	m, cmd := update(t, m, key("ctrl+p"))
	if cmd == nil {
		t.Fatal("ctrl+p on an eligible row with a pane should select+quit")
	}
	if _, ok := m.Selected(); !ok {
		t.Error("expected a selection on an eligible row with a pane")
	}
	if m.ChosenTarget() != "pane" {
		t.Errorf("ChosenTarget() = %q, want \"pane\"", m.ChosenTarget())
	}
	if m.SelectedAction() != RowActionOpen {
		t.Errorf("SelectedAction() = %v, want RowActionOpen", m.SelectedAction())
	}
}

// --- ctrl+l layout cycling ---

// TestCtrlL_TogglesAutoLandscapeAuto proves the two-state toggle (the
// stacked/portrait third state was removed along with the stacked layout).
func TestCtrlL_TogglesAutoLandscapeAuto(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	if m.layout.Orientation != "" {
		t.Fatalf("initial orientation = %q, want auto (\"\")", m.layout.Orientation)
	}
	m, _ = update(t, m, key("ctrl+l"))
	if m.layout.Orientation != LayoutLandscape {
		t.Errorf("after 1st ctrl+l: orientation = %q, want landscape", m.layout.Orientation)
	}
	m, _ = update(t, m, key("ctrl+l"))
	if m.layout.Orientation != "" {
		t.Errorf("after 2nd ctrl+l: orientation = %q, want back to auto", m.layout.Orientation)
	}
}

// TestCtrlL_RecomputesModeImmediately proves that toggling the orientation
// override recomputes m.mode in the same key handling step, instead of
// leaving the cached mode stale until the next tea.WindowSizeMsg. A narrow
// terminal (modeListOnly) forcing landscape via ctrl+l must show the
// side-by-side layout right away — not only after a subsequent resize.
func TestCtrlL_RecomputesModeImmediately(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(64, 24)) // narrow: resolves modeListOnly
	if m.mode != modeListOnly {
		t.Fatalf("setup: expected mode = modeListOnly at a narrow width, got %v", m.mode)
	}
	m, _ = update(t, m, key("ctrl+l")) // force landscape
	if m.layout.Orientation != LayoutLandscape {
		t.Fatalf("setup: expected orientation forced to landscape, got %q", m.layout.Orientation)
	}
	if m.mode != modeWide {
		t.Errorf("mode after ctrl+l = %q, want modeWide immediately (must not wait for a WindowSizeMsg)", m.mode)
	}

	// Toggling back to auto at the same narrow width must revert mode to
	// modeListOnly immediately too, not stay stuck on modeWide.
	m, _ = update(t, m, key("ctrl+l")) // back to auto
	if m.layout.Orientation != "" {
		t.Fatalf("setup: expected orientation back to auto, got %q", m.layout.Orientation)
	}
	if m.mode != modeListOnly {
		t.Errorf("mode after 2nd ctrl+l = %q, want modeListOnly immediately (auto at a narrow width)", m.mode)
	}
}

// --- List-only actions (enter/ctrl+l/left/right) are no-ops off-List ---

// TestListOnlyActions_NoOpInFocusPreview proves enter, ctrl+l, left, and
// right mutate nothing (no selection, no orientation change, no cursor
// move) while the preview pane owns focus — selection/layout-cycle/expand-
// collapse are List-only actions.
func TestListOnlyActions_NoOpInFocusPreview(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}}}
	tree := treeFromFake(driver)
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.cursor = 0
	startCursor := m.cursor
	startOrientation := m.layout.Orientation
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}

	for _, k := range []string{"enter", "ctrl+l", "left", "right"} {
		m, cmd := update(t, m, key(k))
		if cmd != nil {
			t.Errorf("%s while FocusPreview: expected no Cmd, got one", k)
		}
		if _, ok := m.Selected(); ok {
			t.Errorf("%s while FocusPreview: unexpected selection", k)
		}
		if m.layout.Orientation != startOrientation {
			t.Errorf("%s while FocusPreview: orientation changed to %q, want unchanged %q", k, m.layout.Orientation, startOrientation)
		}
		if m.cursor != startCursor {
			t.Errorf("%s while FocusPreview: cursor moved to %d, want unchanged %d", k, m.cursor, startCursor)
		}
		for _, r := range m.rows {
			if r.Kind == RowTab {
				t.Errorf("%s while FocusPreview: unexpected RowTab (expand/collapse must be a no-op)", k)
			}
		}
	}
}

// TestListOnlyActions_NoOpInFocusHelp proves the same enter/ctrl+l/left/
// right no-op contract while FocusHelp.
func TestListOnlyActions_NoOpInFocusHelp(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}}}
	tree := treeFromFake(driver)
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
	m := NewModelWithTree(base, nil, tree, Layout{})
	m.cursor = 0
	startCursor := m.cursor
	startOrientation := m.layout.Orientation
	m.focus = FocusHelp
	m.prevFocus = FocusList

	for _, k := range []string{"enter", "ctrl+l", "left", "right"} {
		m, cmd := update(t, m, key(k))
		if cmd != nil {
			t.Errorf("%s while FocusHelp: expected no Cmd, got one", k)
		}
		if _, ok := m.Selected(); ok {
			t.Errorf("%s while FocusHelp: unexpected selection", k)
		}
		if m.layout.Orientation != startOrientation {
			t.Errorf("%s while FocusHelp: orientation changed to %q, want unchanged %q", k, m.layout.Orientation, startOrientation)
		}
		if m.cursor != startCursor {
			t.Errorf("%s while FocusHelp: cursor moved to %d, want unchanged %d", k, m.cursor, startCursor)
		}
		if m.focus != FocusHelp {
			t.Errorf("%s while FocusHelp: focus changed to %v, want unchanged FocusHelp", k, m.focus)
		}
	}
}

// --- Help overlay: open/close + prevFocus round trip ---

// TestHelpToggle_FromList_RecordsPrevFocusAndOpens proves "?" from FocusList
// records prevFocus=FocusList and opens FocusHelp.
func TestHelpToggle_FromList_RecordsPrevFocusAndOpens(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp {
		t.Fatalf("focus = %v, want FocusHelp after \"?\"", m.focus)
	}
	if m.prevFocus != FocusList {
		t.Errorf("prevFocus = %v, want FocusList", m.prevFocus)
	}
}

// TestHelpToggle_FromPreview_RecordsPrevFocusAndOpens proves "?" from
// FocusPreview records prevFocus=FocusPreview and opens FocusHelp.
func TestHelpToggle_FromPreview_RecordsPrevFocusAndOpens(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp {
		t.Fatalf("focus = %v, want FocusHelp after \"?\"", m.focus)
	}
	if m.prevFocus != FocusPreview {
		t.Errorf("prevFocus = %v, want FocusPreview", m.prevFocus)
	}
}

// TestHelpToggle_QuestionMarkRoundTrip_FromList proves "?" opens help from
// FocusList and a second "?" closes it, restoring exactly FocusList — the
// no-contradiction round trip for the List side.
func TestHelpToggle_QuestionMarkRoundTrip_FromList(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, key("?"))
	m, _ = update(t, m, key("?"))
	if m.focus != FocusList {
		t.Errorf("focus after round trip = %v, want FocusList", m.focus)
	}
}

// TestHelpToggle_EscRoundTrip_FromPreview proves "?" opens help from
// FocusPreview and Esc closes it, restoring exactly FocusPreview (not
// FocusList) — the no-contradiction round trip for the Preview side.
func TestHelpToggle_EscRoundTrip_FromPreview(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.focus = FocusPreview
	m, _ = update(t, m, key("?"))
	m, _ = update(t, m, key("esc"))
	if m.focus != FocusPreview {
		t.Errorf("focus after round trip = %v, want FocusPreview", m.focus)
	}
	if m.cancelled {
		t.Error("esc while help is open must close help, not cancel the picker")
	}
}

// TestHelpFocused_SwallowsQueryMutatingKeys proves printable runes,
// backspace, and ctrl+u never mutate the query or change focus while
// FocusHelp — only "?"/Esc close it.
func TestHelpFocused_SwallowsQueryMutatingKeys(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp {
		t.Fatalf("setup: expected FocusHelp")
	}
	m, _ = update(t, m, key("x"))
	m, _ = update(t, m, key("backspace"))
	m, _ = update(t, m, key("ctrl+u"))
	if m.query != "" {
		t.Errorf("query = %q, want unchanged while help is open", m.query)
	}
	if m.focus != FocusHelp {
		t.Errorf("focus = %v, want still FocusHelp (only ?/esc close it)", m.focus)
	}
}

// --- Esc priority: FocusHelp > non-empty query > cancel ---

// TestEscPriority_HelpOpenTakesPrecedenceOverQuery proves that when both
// FocusHelp is active and the query happens to be non-empty, closing help
// wins: the query is left untouched and the picker is not cancelled.
func TestEscPriority_HelpOpenTakesPrecedenceOverQuery(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.query = "abc"
	m.focus = FocusHelp
	m.prevFocus = FocusList
	m, _ = update(t, m, key("esc"))
	if m.focus != FocusList {
		t.Errorf("focus = %v, want FocusList (help closed, prevFocus restored)", m.focus)
	}
	if m.query != "abc" {
		t.Errorf("query = %q, want unchanged \"abc\" (help-close takes precedence over query-clear)", m.query)
	}
	if m.cancelled {
		t.Error("expected cancelled=false")
	}
}

// TestEscPriority_NonEmptyQueryClearsAndRefocusesList_FromList proves Esc
// with a non-empty query clears it and refocuses List without cancelling.
func TestEscPriority_NonEmptyQueryClearsAndRefocusesList_FromList(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.query = "abc"
	m.applyFilter()
	m, _ = update(t, m, key("esc"))
	if m.query != "" {
		t.Errorf("query = %q, want cleared", m.query)
	}
	if m.focus != FocusList {
		t.Errorf("focus = %v, want FocusList", m.focus)
	}
	if m.cancelled {
		t.Error("expected cancelled=false")
	}
}

// TestEscPriority_NonEmptyQueryClearsAndRefocusesList_FromPreview proves the
// same non-empty-query rule applies from FocusPreview, refocusing List.
func TestEscPriority_NonEmptyQueryClearsAndRefocusesList_FromPreview(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.query = "abc"
	m.applyFilter()
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, key("esc"))
	if m.query != "" {
		t.Errorf("query = %q, want cleared", m.query)
	}
	if m.focus != FocusList {
		t.Errorf("focus = %v, want FocusList (esc refocuses list even from preview)", m.focus)
	}
	if m.cancelled {
		t.Error("expected cancelled=false")
	}
}

// TestEscPriority_EmptyQueryNoHelpCancels proves Esc with an empty query and
// no help open falls through to the unchanged cancel behavior.
func TestEscPriority_EmptyQueryNoHelpCancels(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, cmd := update(t, m, key("esc"))
	if cmd == nil {
		t.Fatal("expected a quit Cmd")
	}
	if !m.cancelled {
		t.Error("expected cancelled=true")
	}
}

// --- 'q' is an ordinary query character, not a cancel key (fix round 1,
// Candidate B) ---
//
// The global switch used to unconditionally cancel on "q" before any
// focus-based routing, making it structurally impossible to ever type the
// literal character 'q' into the search query — breaking fuzzy-finding for
// any query containing it (queue, quick, sql, unique, sequence, ...). Only
// esc/ctrl+c/ctrl+g remain unconditional cancel keys; 'q' now behaves like
// any other printable rune.

// TestQ_ExtendsQueryInFocusList_DoesNotCancel proves "q" while FocusList
// with a non-empty existing query extends the query and never cancels.
func TestQ_ExtendsQueryInFocusList_DoesNotCancel(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("queue", "/queue")}, nil)
	m.query = "backend-"
	m.applyFilter()
	m, cmd := update(t, m, key("q"))
	if m.cancelled {
		t.Error("expected cancelled=false: \"q\" must never cancel the picker")
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Error("expected no tea.Quit from \"q\"")
		}
	}
	if !strings.HasSuffix(m.query, "q") {
		t.Errorf("query = %q, want it to end with \"q\"", m.query)
	}
}

// TestQ_FromFocusPreview_RefocusesListAndExtendsQuery proves "q" while
// FocusPreview behaves exactly like any other printable rune: it refocuses
// FocusList and extends the query, instead of cancelling.
func TestQ_FromFocusPreview_RefocusesListAndExtendsQuery(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("queue", "/queue"), zoxideCandidate("other", "/other")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.focus = FocusPreview
	if m.focus != FocusPreview {
		t.Fatalf("setup: expected FocusPreview")
	}
	m, _ = update(t, m, key("q"))
	if m.cancelled {
		t.Error("expected cancelled=false: \"q\" must never cancel the picker")
	}
	if m.focus != FocusList {
		t.Errorf("after \"q\" while preview-focused: focus = %v, want FocusList", m.focus)
	}
	if m.query != "q" {
		t.Errorf("query = %q, want \"q\" (the rune must extend the query)", m.query)
	}
}

// --- Spinner: no duplicate tick loops ---

// TestSpinner_OnlyOneTickLoopWhileLoading proves maybeStartSpinner issues a
// Tick only on the false->true edge, and repeated selection-change events
// while still loading never spawn a second concurrent tick loop.
func TestSpinner_OnlyOneTickLoopWhileLoading(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a"), zoxideCandidate("b", "/b")}, blockingRenderer{})
	if !m.previewLoading {
		t.Fatal("setup: expected previewLoading=true with a wired renderer")
	}
	cmd := m.maybeStartSpinner()
	if cmd == nil {
		t.Fatal("expected the first maybeStartSpinner call to issue a Tick")
	}
	if !m.spinnerRunning {
		t.Fatal("expected spinnerRunning=true after issuing the first Tick")
	}
	// A second call while still loading (e.g. another selection change
	// before the render resolves) must NOT issue a second Tick.
	if cmd := m.maybeStartSpinner(); cmd != nil {
		t.Error("expected no second Tick while a loop is already running")
	}
}

// TestSpinner_TickLoopStopsWhenLoadingEnds proves handleSpinnerTick lets the
// loop die (returns a nil Cmd) once previewLoading goes false.
func TestSpinner_TickLoopStopsWhenLoadingEnds(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, blockingRenderer{})
	m.spinnerRunning = true
	m.previewLoading = false // render already resolved
	mm, cmd := m.handleSpinnerTick(spinner.TickMsg{})
	if cmd != nil {
		t.Error("expected handleSpinnerTick to return a nil Cmd once loading has ended")
	}
	if mm.spinnerRunning {
		t.Error("expected spinnerRunning=false once the tick loop stops")
	}
}

// TestSpinner_TickLoopContinuesWhileLoading proves the loop keeps
// rescheduling itself while still loading.
func TestSpinner_TickLoopContinuesWhileLoading(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, blockingRenderer{})
	m.previewLoading = true
	m.spinnerRunning = true
	_, cmd := m.handleSpinnerTick(spinner.TickMsg{})
	if cmd == nil {
		t.Error("expected handleSpinnerTick to reschedule while still loading")
	}
}

// --- Help viewport: scrolling reveals clipped content ---

// TestHelpViewport_ScrollsAndRevealsHiddenContent proves the help overlay's
// content is reachable via scroll rather than silently clipped at a short
// terminal height: the last line of help content is not visible at the top
// of a small viewport, but becomes visible after scrolling down.
func TestHelpViewport_ScrollsAndRevealsHiddenContent(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(100, 10)) // short terminal: small help viewport
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp {
		t.Fatalf("setup: expected FocusHelp")
	}
	lines := strings.Split(m.helpBodyText(), "\n")
	lastLine := lines[len(lines)-1]
	if strings.Contains(m.helpViewport.View(), lastLine) {
		t.Fatalf("setup: last help line %q already visible at top of a short viewport; test needs content taller than the viewport", lastLine)
	}
	startOffset := m.helpViewport.YOffset

	for i := 0; i < 40 && !strings.Contains(m.helpViewport.View(), lastLine); i++ {
		m, _ = update(t, m, key("down"))
	}

	if m.helpViewport.YOffset <= startOffset {
		t.Errorf("helpViewport.YOffset = %d, want > %d after scrolling down", m.helpViewport.YOffset, startOffset)
	}
	if !strings.Contains(m.helpViewport.View(), lastLine) {
		t.Errorf("expected the last help line %q to become visible after scrolling, view:\n%s", lastLine, m.helpViewport.View())
	}
}
