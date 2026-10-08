package tui

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/config"
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

// keyCodes are the named keys tests press, by their tea.Key.String() name.
var keyCodes = map[string]rune{
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab, "space": tea.KeySpace,
	"backspace": tea.KeyBackspace, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft,
	"right": tea.KeyRight, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome,
	"end": tea.KeyEnd,
}

// key is the press of one key as the terminal decoder reports it: its name
// with modifier prefixes in Bubble Tea's order ("ctrl+t", "alt+backspace",
// "shift+tab") or one printable rune, which types its text.
func key(s string) tea.KeyPressMsg {
	var k tea.KeyPressMsg
	for _, m := range []struct {
		prefix string
		mod    tea.KeyMod
	}{{"ctrl+", tea.ModCtrl}, {"alt+", tea.ModAlt}, {"shift+", tea.ModShift}} {
		if rest, ok := strings.CutPrefix(s, m.prefix); ok && rest != "" {
			k.Mod |= m.mod
			s = rest
		}
	}
	if code, ok := keyCodes[s]; ok {
		k.Code = code
		if code == tea.KeySpace {
			k.Text = " "
		}
		return k
	}
	r, size := utf8.DecodeRuneInString(s)
	if size != len(s) {
		panic("key: " + strconv.Quote(s) + " is not one key")
	}
	k.Code = r
	if k.Mod == 0 {
		k.Text = s
	}
	return k
}

// typeText presses one key per rune of text, as typing it does.
func typeText(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = update(t, m, key(string(r)))
	}
	return m
}

// TestListHalfPageKeys moves by half the visible list rows, including at
// zero-height and at both boundaries, without editing the active query.
func TestListHalfPageKeys(t *testing.T) {
	for _, visible := range []int{0, 1, 2, 3, 10} {
		for _, tc := range []struct {
			name, chord string
			start, want int
		}{
			{"down", "ctrl+d", 2, min(11, 2+max(1, visible/2))},
			{"up", "ctrl+u", 9, max(0, 9-max(1, visible/2))},
			{"down clamp", "ctrl+d", 11, 11},
			{"up clamp", "ctrl+u", 0, 0},
		} {
			t.Run(tc.name+"/rows="+strconv.Itoa(visible), func(t *testing.T) {
				m := NewModel(nil, nil)
				m, _ = update(t, m, sizeMsg(120, visible+chromeRows))
				if got := m.geometry().ListInnerRows; got != visible {
					t.Fatalf("ListInnerRows = %d, want %d", got, visible)
				}
				m.rows = make([]Row, 12)
				m.cursor = tc.start
				m.query = "keep"
				m.previewText = "stale"
				seq := m.previewSeq
				m, _ = update(t, m, key(tc.chord))
				if m.cursor != tc.want || !m.cursorTouched {
					t.Errorf("%s: cursor = %d touched = %v; want %d, true", tc.chord, m.cursor, m.cursorTouched, tc.want)
				}
				if m.query != "keep" || len(m.rows) != 12 {
					t.Errorf("%s changed query or filtered rows: query=%q rows=%d", tc.chord, m.query, len(m.rows))
				}
				if m.previewSeq != seq+1 || m.previewText != "" {
					t.Errorf("%s failed preview sync: seq=%d (previous %d), text=%q", tc.chord, m.previewSeq, seq, m.previewText)
				}
			})
		}
	}
	for _, chord := range []string{"ctrl+d", "ctrl+u"} {
		t.Run(chord+"/empty", func(t *testing.T) {
			m := NewModel(nil, nil)
			m, _ = update(t, m, sizeMsg(120, 5))
			m, _ = update(t, m, key(chord))
			if m.cursor != 0 || !m.cursorTouched {
				t.Errorf("empty %s: cursor=%d touched=%v", chord, m.cursor, m.cursorTouched)
			}
		})
	}
}

func TestEscStillClearsQueryAfterHalfPageNavigation(t *testing.T) {
	m := NewModel([]source.Candidate{zoxideCandidate("alpha", "/alpha")}, nil)
	m, _ = update(t, m, key("a"))
	m, _ = update(t, m, key("ctrl+u"))
	if m.query != "a" {
		t.Fatalf("ctrl+u query = %q, want a", m.query)
	}
	m, _ = update(t, m, key("esc"))
	if m.query != "" || m.cancelled {
		t.Errorf("esc: query=%q cancelled=%v, want cleared without quit", m.query, m.cancelled)
	}
}

// --- Focus + viewport scrolling ---

// TestCycleTabs_TabAndShiftTabWrapAllAndAgents proves Tab and Shift+Tab
// cycle the default all/agents tabs in both directions, wrapping at each end.
func TestCycleTabs_TabAndShiftTabWrapAllAndAgents(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	for i, step := range []struct{ key, want string }{
		{"", "all"},
		{"tab", "agents"},
		{"tab", "all"}, // wraps forward
		{"shift+tab", "agents"},
		{"shift+tab", "all"}, // wraps backward
	} {
		if step.key != "" {
			m, _ = update(t, m, key(step.key))
		}
		if got := m.ActiveTab(); got != step.want {
			t.Errorf("step %d (%q): active tab = %q, want %q", i, step.key, got, step.want)
		}
	}
}

// TestCycleTabs_WorksInListOnlyMode proves Tab/Shift+Tab cycle the tabs
// even when the current layout has no preview pane available.
func TestCycleTabs_WorksInListOnlyMode(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m.mode = modeListOnly
	m, _ = update(t, m, key("tab"))
	if got := m.ActiveTab(); got != "agents" {
		t.Errorf("tab in list-only mode: active tab = %q, want agents", got)
	}
	m, _ = update(t, m, key("shift+tab"))
	if got := m.ActiveTab(); got != "all" {
		t.Errorf("shift+tab in list-only mode: active tab = %q, want all", got)
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

// TestLeftRight_ExpandCollapseWorkspace proves Right (or ctrl+l) expands a
// workspace's tab/pane children at an empty query and Left (or ctrl+h)
// collapses them again.
func TestLeftRight_ExpandCollapseWorkspace(t *testing.T) {
	t.Parallel()
	for _, keys := range [][2]string{{"right", "left"}, {"ctrl+l", "ctrl+h"}} {
		driver := &fakeTreeDriver{tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}}}
		tree := treeFromFake(driver)
		base := []source.Candidate{herdrCandidate("backend", "/svc", "w1")}
		m := NewModelWithTree(base, nil, tree, Layout{})
		m.cursor = 0 // the workspace row (no group header anymore)

		m, _ = update(t, m, key(keys[0]))
		foundTab := false
		for _, r := range m.rows {
			if r.Kind == RowTab {
				foundTab = true
			}
		}
		if !foundTab {
			t.Fatalf("expected a RowTab after %s on an expandable workspace, rows=%+v", keys[0], m.rows)
		}

		m, _ = update(t, m, key(keys[1]))
		for _, r := range m.rows {
			if r.Kind == RowTab {
				t.Fatalf("expected no RowTab after %s collapses the workspace, rows=%+v", keys[1], m.rows)
			}
		}
	}
}

// TestLeftRight_MoveThroughTheTree proves the arrows move through an
// expanded workspace like a tree: Right on a workspace whose children show
// enters its first child, and Left on a child moves back up to the
// workspace and collapses it.
func TestLeftRight_MoveThroughTheTree(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/svc/api"}},
	}
	base := []source.Candidate{herdrCandidate("backend", "/svc", "w1"), herdrCandidate("frontend", "/web", "w2")}
	m := NewModelWithTree(base, nil, treeFromFake(driver), Layout{})
	m, _ = update(t, m, key("right"))
	if m.cursor != 0 || len(m.rows) != 4 {
		t.Fatalf("after expanding: cursor %d rows %v, want the tree under the first workspace", m.cursor, rowSummary(m.rows))
	}
	m, _ = update(t, m, key("ctrl+l"))
	if m.rows[m.cursor].Kind != RowTab {
		t.Fatalf("Right on an expanded workspace: cursor on %v, want its first tab", rowSummary(m.rows[m.cursor:m.cursor+1]))
	}
	m, _ = update(t, m, key("down"))
	if m.rows[m.cursor].Kind != RowPane {
		t.Fatalf("setup: cursor on %v, want the pane", rowSummary(m.rows[m.cursor:m.cursor+1]))
	}
	m, _ = update(t, m, key("ctrl+h"))
	if m.cursor != 0 || len(m.rows) != 2 {
		t.Errorf("Left on a pane: cursor %d rows %v, want the collapsed workspace highlighted", m.cursor, rowSummary(m.rows))
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

// TestSelectWithTarget_NoOpInFocusHelp proves ctrl+t/ctrl+p are swallowed
// while FocusHelp, even with an otherwise-eligible current pane and
// candidate.
func TestSelectWithTarget_NoOpInFocusHelp(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)
	m.focus = FocusHelp
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

// --- ctrl+r layout toggle ---

// TestCtrlR_TogglesWhatIsVisible proves ctrl+r always changes the visible
// layout, in the same key step: side by side becomes list only and back. At
// a wide terminal (the Herdr popup) the responsive layout is already side by
// side, so the first toggle must hide the preview; at a narrow one it must
// show it. Toggling back returns to the configured orientation, so the
// layout keeps following the terminal size.
func TestCtrlR_TogglesWhatIsVisible(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		width              int
		start, toggled     string
		toggledOrientation string
	}{
		{"wide terminal", 120, modeWide, modeListOnly, LayoutListOnly},
		{"narrow terminal", 64, modeListOnly, modeWide, LayoutLandscape},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
			m, _ = update(t, m, sizeMsg(tc.width, 30))
			if m.mode != tc.start {
				t.Fatalf("setup: mode = %q, want %q", m.mode, tc.start)
			}
			m, _ = update(t, m, key("ctrl+r"))
			if m.mode != tc.toggled || m.layout.Orientation != tc.toggledOrientation {
				t.Errorf("after ctrl+r: mode %q orientation %q, want %q %q", m.mode, m.layout.Orientation, tc.toggled, tc.toggledOrientation)
			}
			m, _ = update(t, m, key("ctrl+r"))
			if m.mode != tc.start || m.layout.Orientation != "" {
				t.Errorf("after 2nd ctrl+r: mode %q orientation %q, want %q back on auto", m.mode, m.layout.Orientation, tc.start)
			}
		})
	}
}

// TestCtrlR_KeepsAConfiguredLandscape proves toggling back restores a
// configured landscape orientation rather than switching to auto.
func TestCtrlR_KeepsAConfiguredLandscape(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("a", "/a")}, nil, Layout{Orientation: LayoutLandscape})
	m, _ = update(t, m, sizeMsg(64, 30))
	m, _ = update(t, m, key("ctrl+r"))
	if m.mode != modeListOnly {
		t.Fatalf("after ctrl+r: mode = %q, want list only", m.mode)
	}
	m, _ = update(t, m, key("ctrl+r"))
	if m.mode != modeWide || m.layout.Orientation != LayoutLandscape {
		t.Errorf("after 2nd ctrl+r: mode %q orientation %q, want the configured landscape", m.mode, m.layout.Orientation)
	}
}

// TestCtrlR_TooShortTerminalKeepsTheListAlone proves a terminal below the
// preview's height floor stays list only.
func TestCtrlR_TooShortTerminalKeepsTheListAlone(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, minPreviewHeight-1))
	m, _ = update(t, m, key("ctrl+r"))
	if m.mode != modeListOnly {
		t.Errorf("mode = %q, want list only below the height floor", m.mode)
	}
}

// --- List-only actions (enter/ctrl+r/left/right) are no-ops off-List ---

// TestListOnlyActions_NoOpInFocusHelp proves the same enter/ctrl+r/left/
// right (and ctrl+h/ctrl+l) no-op contract while FocusHelp.
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

	for _, k := range []string{"enter", "ctrl+r", "left", "right", "ctrl+h", "ctrl+l"} {
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

// --- Help overlay: open/close ---

// TestHelpToggle_FromList_Opens proves "?" from FocusList opens FocusHelp.
func TestHelpToggle_FromList_Opens(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, key("?"))
	if m.focus != FocusHelp {
		t.Fatalf("focus = %v, want FocusHelp after \"?\"", m.focus)
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
	m, _ = update(t, m, key("esc"))
	if m.focus != FocusList {
		t.Errorf("focus = %v, want FocusList (help closed back to the list)", m.focus)
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
	lines := strings.Split(m.helpBodyText(m.helpViewport.Width()), "\n")
	lastLine := lines[len(lines)-1]
	if strings.Contains(m.helpViewport.View(), lastLine) {
		t.Fatalf("setup: last help line %q already visible at top of a short viewport; test needs content taller than the viewport", lastLine)
	}
	startOffset := m.helpViewport.YOffset()

	for i := 0; i < 40 && !strings.Contains(m.helpViewport.View(), lastLine); i++ {
		m, _ = update(t, m, key("down"))
	}

	if m.helpViewport.YOffset() <= startOffset {
		t.Errorf("helpViewport.YOffset() = %d, want > %d after scrolling down", m.helpViewport.YOffset(), startOffset)
	}
	if !strings.Contains(m.helpViewport.View(), lastLine) {
		t.Errorf("expected the last help line %q to become visible after scrolling, view:\n%s", lastLine, m.helpViewport.View())
	}
}
