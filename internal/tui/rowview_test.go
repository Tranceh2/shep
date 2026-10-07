package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/theme"
	"github.com/tranceh2/shep/internal/tmpl"
)

// treeWith builds a one-workspace tree whose panes report statuses.
func treeWith(workspaceID string, statuses ...string) *TreeExpander {
	snap := source.Snapshot{
		Workspaces: []source.Workspace{{ID: workspaceID}},
		Tabs:       []source.Tab{{ID: workspaceID + ":t1", WorkspaceID: workspaceID, Number: 1}},
	}
	for i, status := range statuses {
		snap.Panes = append(snap.Panes, source.Pane{ID: workspaceID + ":p" + string(rune('a'+i)), WorkspaceID: workspaceID, TabID: workspaceID + ":t1", AgentStatus: status})
	}
	return NewTreeExpanderFromSnapshot(snap)
}

// TestRowMarkers_PerKind proves each row kind's default marker part, in
// display order: the live markers it shows and the text around them, with
// no gap left by the markers a row does not show.
func TestRowMarkers_PerKind(t *testing.T) {
	t.Parallel()
	pinnedZoxide := zoxideCandidate("cache", "/srv/cache")
	group := source.Candidate{Label: "team", Path: "/srv/team", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	working := newRenderTestModel(ThemeMocha, FocusList).agentStatusIcon("working")
	for _, tc := range []struct {
		name string
		tree *TreeExpander
		row  Row
		want string
	}{
		{"open workspace: most urgent agent status", treeWith("w1", "idle", "working", "blocked", ""), Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, "◉"},
		{"open workspace: working beats done and idle", treeWith("w1", "done", "working", "idle"), Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, working},
		{"open workspace without agents", treeWith("w1", "", "unknown"), Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, ""},
		{"pinned candidate", nil, Row{Kind: RowCandidate, Candidate: pinnedZoxide}, "★"},
		{"pinned group workspace", nil, Row{Kind: RowCandidate, Candidate: group}, "★ ›"},
		{"worktree branch", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "api", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "main"}}}, "main"},
		{"session state", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "s", Source: config.SourceSessions, Meta: map[string]string{"running": "true", "default": "true"}}}, "running · default"},
		{"stopped session", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "s", Source: config.SourceSessions}}, "stopped"},
		{"missing path", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "gone", Path: "/gone", Source: config.SourceProjects, Missing: true}}, "missing"},
		{"missing open workspace", treeWith("w1", "blocked"), Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "api", Source: config.SourceHerdr, Missing: true, Meta: map[string]string{"workspace_id": "w1"}}}, "missing ◉"},
		{"agent row: a path-like workspace shows its last element", nil, Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix it", Source: config.SourceAgents, Meta: map[string]string{"workspace_label": "/home/dev/Proyectos/fsociety/"}}}, "fsociety"},
		{"agent row: a named workspace keeps its label", nil, Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix it", Source: config.SourceAgents, Meta: map[string]string{"workspace_label": "app contract"}}}, "app contract"},
		{"tree pane: its agent", nil, Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"agent": "claude"}}}, "claude"},
		{"tab row: none", nil, Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRenderTestModel(ThemeMocha, FocusList)
			m.layout.Templates = tmpl.New("/home/dev")
			m.tree = tc.tree
			m.rankingSnapshot = m.rankingSnapshot.WithPinned(ranking.PinKey(pinnedZoxide), true).WithPinned(ranking.PinKey(group), true)
			if got := m.rowAccessoryText(tc.row); got != tc.want {
				t.Errorf("marker = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRowView_AgentMarkerCappedAndTitleKeepsStart proves the truncation
// order on an agent row: under pressure its workspace marker is capped at
// 30% of the row, keeping its start, and the title keeps its start; the
// marker is dropped only when the title would keep fewer than 16 cells, and
// a row with room shows the marker whole.
func TestRowView_AgentMarkerCappedAndTitleKeepsStart(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowPane, Candidate: source.Candidate{Label: "Refactor the render path of shep and measure everything", Source: config.SourceAgents,
		Meta: map[string]string{"workspace_label": "platform-engineering-workspace"}}}
	line := strings.TrimRight(ansi.Strip(m.renderRowLine(row, false, 60)), " ")
	if !strings.HasSuffix(line, " platform-engineer…") || ansi.StringWidth("platform-engineer…") != 60*markerMaxPercent/100 {
		t.Errorf("agent row = %q, want the workspace marker capped at 30%% of the row, keeping its start", line)
	}
	if !strings.HasPrefix(line, "  Refactor the render path of shep and m… ") {
		t.Errorf("agent row = %q, want the title's start, truncated on the right", line)
	}
	if wide := ansi.Strip(m.renderRowLine(row, false, 120)); !strings.Contains(wide, "everything") || !strings.HasSuffix(strings.TrimRight(wide, " "), " platform-engineering-workspace") {
		t.Errorf("wide agent row = %q, want the whole title and marker", wide)
	}
	narrow := strings.TrimRight(ansi.Strip(m.renderRowLine(row, false, 25)), " ")
	if strings.Contains(narrow, "platform") || !strings.HasPrefix(narrow, "  Refactor the render pa") || ansi.StringWidth(narrow) != 25 {
		t.Errorf("narrow agent row = %q, want the marker dropped so the title keeps its room", narrow)
	}
	if kept := strings.TrimRight(ansi.Strip(m.renderRowLine(row, false, 26)), " "); !strings.HasSuffix(kept, " platfo…") {
		t.Errorf("agent row at 26 = %q, want the capped marker kept while the title keeps 16 cells", kept)
	}
}

// TestRowView_IconColorsPerPresentation proves icons are colored by their
// presentation's icon_color — by default the source.<name> roles, the tab
// glyph text.muted — and that the plain theme leaves them uncolored.
func TestRowView_IconColorsPerPresentation(t *testing.T) {
	t.Parallel()
	th := testTheme(ThemeMocha)
	letters := func(p *config.Presentations) {
		p.Herdr.Icon, p.Workspaces.Icon, p.Zoxide.Icon, p.Sessions.Icon, p.Agents.Icon = "H", "W", "Z", "S", "A"
	}
	for _, tc := range []struct {
		name string
		row  Row
		icon string
		role theme.Role
	}{
		{"herdr", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceHerdr}}, "H", theme.RoleSourceHerdr},
		{"workspaces", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceWorkspaces}}, "W", theme.RoleSourceWorkspaces},
		{"zoxide", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceZoxide}}, "Z", theme.RoleSourceZoxide},
		{"projects", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceProjects}}, "\ue702 ", theme.RoleSourceProjects},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true"}}}, "\ue725 ", theme.RoleSourceProjects},
		{"sessions", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceSessions}}, "S", theme.RoleSourceSessions},
		{"agents", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceAgents}}, "A", theme.RoleSourceAgents},
		{"custom: the row's own icon", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "C", Source: "prs"}}, "C", theme.RoleSourceCustom},
		{"tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "a"}}, "◫", theme.RoleTextMuted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(letters)
			if v := m.buildRowView(tc.row); v.icon.text != tc.icon {
				t.Fatalf("icon = %q, want %q", v.icon.text, tc.icon)
			}
			want := lipgloss.NewStyle().Foreground(th.Role(tc.role).Lipgloss()).Render(tc.icon)
			if line := m.renderRowLine(tc.row, false, 30); !strings.Contains(line, want) {
				t.Errorf("row %q does not draw the icon as %q", line, want)
			}
			plain := newRenderTestModel(ThemePlain, FocusList).withPresentation(letters)
			if line := plain.renderRowLine(tc.row, false, 30); strings.Contains(line, "38;2;") {
				t.Errorf("plain row %q colors its icon", line)
			}
		})
	}
}

// TestScrollOffset_ScrollOff proves the window follows the cursor with a
// scroll-off margin instead of re-centering: it moves only when the cursor
// comes within min(3, (visible-1)/2) rows of an edge.
func TestScrollOffset_ScrollOff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                           string
		offset, cursor, total, visible int
		want                           int
	}{
		{"fits: no scrolling", 5, 3, 8, 10, 0},
		{"cursor inside the window: unchanged", 0, 6, 50, 10, 0},
		{"cursor reaches the bottom margin", 0, 7, 50, 10, 1},
		{"cursor jumps far down", 0, 30, 50, 10, 24},
		{"cursor inside after scrolling down", 24, 28, 50, 10, 24},
		{"cursor reaches the top margin", 24, 26, 50, 10, 23},
		{"clamped at the end", 40, 49, 50, 10, 40},
		{"clamped at the start", 3, 0, 50, 10, 0},
		{"tiny window keeps the cursor visible", 0, 3, 50, 2, 2},
		{"unknown height", 7, 30, 50, 0, 0},
	} {
		if got := scrollOffset(tc.offset, tc.cursor, tc.total, tc.visible); got != tc.want {
			t.Errorf("%s: scrollOffset(%d, %d, %d, %d) = %d, want %d", tc.name, tc.offset, tc.cursor, tc.total, tc.visible, got, tc.want)
		}
	}
}

// numberedCandidates returns n zoxide candidates "cand-00"...
func numberedCandidates(n int) []source.Candidate {
	out := make([]source.Candidate, n)
	for i := range out {
		name := "cand-" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		out[i] = zoxideCandidate(name, "/x/"+name)
	}
	return out
}

// TestList_ScrollOffFollowsCursorAndQueryResets drives a long list: moving
// the cursor down leaves the window still until the cursor nears its bottom
// edge, then scrolls one row per move; a new query resets the window.
func TestList_ScrollOffFollowsCursorAndQueryResets(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(numberedCandidates(30), nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 10+chromeRows))
	for i := 1; i <= 6; i++ {
		m, _ = update(t, m, key("down"))
		if m.listOffset != 0 {
			t.Fatalf("cursor %d: offset = %d, want the window still", m.cursor, m.listOffset)
		}
	}
	m, _ = update(t, m, key("down"))
	if m.cursor != 7 || m.listOffset != 1 {
		t.Fatalf("cursor %d offset %d, want the window to scroll by one at the edge margin", m.cursor, m.listOffset)
	}
	if first := viewLines(m)[3]; !strings.Contains(first, "cand-01") {
		t.Errorf("first body row = %q, want cand-01 after scrolling one row", first)
	}
	m, _ = update(t, m, key("c"))
	if m.cursor != 0 || m.listOffset != 0 {
		t.Errorf("after a query: cursor %d offset %d, want both reset", m.cursor, m.listOffset)
	}
}

// TestScrollThumb_SizeAndPosition proves the thumb is proportional to the
// visible share (at least one row) and travels from the top to the bottom
// of the track with the offset.
func TestScrollThumb_SizeAndPosition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		offset, total, visible int
		start, length          int
	}{
		{0, 10, 10, 0, 0},
		{0, 20, 10, 0, 5},
		{5, 20, 10, 3, 5},
		{10, 20, 10, 5, 5},
		{0, 100, 10, 0, 1},
		{45, 100, 10, 5, 1},
		{90, 100, 10, 9, 1},
		{0, 1000, 10, 0, 1},
	} {
		start, length := scrollThumb(tc.offset, tc.total, tc.visible)
		if start != tc.start || length != tc.length {
			t.Errorf("scrollThumb(%d, %d, %d) = (%d, %d), want (%d, %d)", tc.offset, tc.total, tc.visible, start, length, tc.start, tc.length)
		}
	}
}

// TestView_ScrollbarOnDividerAndListOnlyColumn proves the thumb is drawn
// over the divider in wide mode, over the list's last column in list-only
// mode (which reserves that column, and a blank cell before it, only when
// scrolling is needed), and not at all when the rows fit.
func TestView_ScrollbarOnDividerAndListOnlyColumn(t *testing.T) {
	t.Parallel()
	bodyColumn := func(m Model, col int) string {
		var b strings.Builder
		lines := viewLines(m)
		for _, line := range lines[3 : len(lines)-1] {
			b.WriteString(ansi.Cut(line, col, col+1))
		}
		return b.String()
	}
	wide := NewModelWithLayout(numberedCandidates(20), nil, Layout{Theme: testTheme(ThemeMocha)})
	wide, _ = update(t, wide, sizeMsg(120, 10+chromeRows))
	g := wide.geometry()
	col := g.Margin + g.ListWidth + 1
	if got := bodyColumn(wide, col); got != "┃┃┃┃┃│││││" {
		t.Errorf("wide divider = %q, want a 5-row thumb at the top", got)
	}
	for range 19 {
		wide, _ = update(t, wide, key("down"))
	}
	if got := bodyColumn(wide, col); got != "│││││┃┃┃┃┃" {
		t.Errorf("wide divider at the end = %q, want the thumb at the bottom", got)
	}

	// Labels wider than the column: rows run to their edge, so only the
	// reserved gap keeps them off the track.
	long := numberedCandidates(20)
	for i := range long {
		long[i].Label += strings.Repeat("-wide", 20)
	}
	narrow := NewModelWithLayout(long, nil, Layout{Theme: testTheme(ThemeMocha)})
	narrow, _ = update(t, narrow, sizeMsg(64, 10+chromeRows))
	last := narrow.geometry().Margin + narrow.geometry().ListWidth - 1
	if got := bodyColumn(narrow, last); got != "┃┃┃┃┃│││││" {
		t.Errorf("list-only track = %q, want the thumb in the list's last column", got)
	}
	if got := bodyColumn(narrow, last-1); got != strings.Repeat(" ", 10) {
		t.Errorf("cells before the list-only track = %q, want a blank gap on every row", got)
	}

	fits := NewModelWithLayout(numberedCandidates(5), nil, Layout{Theme: testTheme(ThemeMocha)})
	fits, _ = update(t, fits, sizeMsg(64, 10+chromeRows))
	if got := bodyColumn(fits, last); strings.ContainsAny(got, "┃│") {
		t.Errorf("list-only last column = %q, want no track when the rows fit", got)
	}
}

// TestSpinnerNeeded_WorkspaceAccessory proves an open workspace whose
// aggregate agent status is working keeps the spinner armed even while
// collapsed, and that a live status change re-arms it and refreshes the
// cached row view.
func TestSpinnerNeeded_WorkspaceAccessory(t *testing.T) {
	t.Parallel()
	m := NewModelWithTree([]source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, nil, treeWith("w1", "idle"), Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 20))
	if m.spinnerNeeded() {
		t.Fatal("idle workspace must not arm the spinner")
	}
	if got := m.partText(&m.rowWindow.views[0].marker); got != m.icons().StatusIdle {
		t.Fatalf("cached marker = %q, want the idle status", got)
	}
	m, cmd := update(t, m, paneStatusMsg{PaneID: "w1:pa", Status: "working"})
	if !m.spinnerNeeded() || !m.spinnerRunning || cmd == nil {
		t.Fatalf("working workspace: needed=%v running=%v cmd=%v, want the spinner armed", m.spinnerNeeded(), m.spinnerRunning, cmd != nil)
	}
	if got := m.partText(&m.rowWindow.views[0].marker); got != ansi.Strip(m.agentStatusIcon("working")) {
		t.Errorf("cached marker after the live update = %q, want working", got)
	}
}

// TestRowWindow_ReusedAcrossFrames proves a frame that changes no row (a
// spinner tick) reuses the cached row views instead of rebuilding them,
// while a new query rebuilds them.
func TestRowWindow_ReusedAcrossFrames(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(numberedCandidates(30), nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 20))
	views := &m.rowWindow.views[0]
	m, _ = update(t, m, spinner.TickMsg{ID: m.spinner.ID()})
	if &m.rowWindow.views[0] != views {
		t.Error("a spinner tick rebuilt the row views")
	}
	m, _ = update(t, m, key("c"))
	if &m.rowWindow.views[0] == views {
		t.Error("a new query reused stale row views")
	}
	if !m.rowWindow.covers(m.rows, m.listOffset, len(m.rowWindow.views)) {
		t.Error("the rebuilt window does not cover the visible rows")
	}
}

// TestEmptyState_AlignedWithRowsAndBlankPreview proves the empty states sit
// after the two-cell gutter (message in the row style, hint muted below)
// and that the preview column stays blank with nothing selected.
func TestEmptyState_AlignedWithRowsAndBlankPreview(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 20))
	for _, r := range "zzz" {
		m, _ = update(t, m, key(string(r)))
	}
	lines := viewLines(m)
	_, preview, _ := strings.Cut(lines[3], "│")
	if left, _, _ := strings.Cut(lines[3], "│"); strings.TrimRight(left, " ") != "   No matches for “zzz”" {
		t.Errorf("first empty-state row = %q, want the message after the gutter", lines[3])
	}
	if left, _, _ := strings.Cut(lines[4], "│"); strings.TrimRight(left, " ") != "   esc clears the search" {
		t.Errorf("second empty-state row = %q, want the hint after the gutter", lines[4])
	}
	if strings.TrimSpace(preview) != "" || strings.Contains(m.View().Content, "(no selection)") {
		t.Errorf("preview column = %q, want it blank with no row", preview)
	}
	ascii := m
	ascii.layout.Icons = IconsASCII
	if got := ascii.emptyStateLines()[0]; got != `No matches for "zzz"` {
		t.Errorf("ASCII no-match line = %q, want plain quotes", got)
	}
}

// TestFitPlaceholder_FallsBackToAWholeWord proves the placeholder never
// breaks mid-word for the count: the full phrase, else "Search", else the
// count alone.
func TestFitPlaceholder_FallsBackToAWholeWord(t *testing.T) {
	t.Parallel()
	const full = "Search workspaces, projects, folders" // 36 cells
	for _, tc := range []struct {
		room, countW int
		want         string
		keepCount    bool
	}{
		{40, 3, full, true},
		{39, 3, "Search", true},
		{10, 3, "Search", true},
		{9, 3, "", true},
		{3, 3, "", true},
		{2, 3, "S…", false},
	} {
		got, keep := fitPlaceholder(full, tc.room, tc.countW)
		if got != tc.want || keep != tc.keepCount {
			t.Errorf("fitPlaceholder(room %d, count %d) = %q, %v; want %q, %v", tc.room, tc.countW, got, keep, tc.want, tc.keepCount)
		}
	}

	m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(80, 24))
	if prompt := promptText(m); strings.Contains(prompt, "…") || !strings.HasPrefix(prompt, "❯  Search") {
		t.Errorf("prompt at 80x24 = %q, want a whole-word placeholder", prompt)
	}
}

// TestPaneStatus_InvalidatesCachedRowsInPlace proves a live status update
// that patches rows in place (an expanded tree pane) is reflected by the
// next frame instead of a stale cached view.
func TestPaneStatus_InvalidatesCachedRowsInPlace(t *testing.T) {
	t.Parallel()
	m := NewModelWithTree([]source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, nil, treeWith("w1", "idle"), Layout{Theme: testTheme(ThemeMocha)})
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	m, _ = update(t, m, sizeMsg(120, 20))
	idle, blocked := ansi.Strip(m.agentStatusIcon("idle")), ansi.Strip(m.agentStatusIcon("blocked"))
	if body := strings.Join(viewLines(m)[3:6], "\n"); !strings.Contains(body, idle) {
		t.Fatalf("body = %q, want the idle glyph", body)
	}
	m, _ = update(t, m, paneStatusMsg{PaneID: "w1:pa", Status: "blocked"})
	body := strings.Join(viewLines(m)[3:6], "\n")
	if !strings.Contains(body, blocked) || strings.Contains(body, idle) {
		t.Errorf("body after the live update = %q, want only the blocked glyph", body)
	}
}

var _ tea.Msg = paneStatusMsg{}

// TestRowAccessories_IdleWorkspaceIsMuted proves the open workspace's
// aggregate status only colors attention states: idle is the resting state
// and draws muted, while blocked keeps its color.
func TestRowAccessories_IdleWorkspaceIsMuted(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}
	set := m.icons()
	m.tree = treeWith("w1", "idle")
	if line := m.renderRowLine(row, false, 40); !strings.Contains(line, m.styles.mutedStyle.Render(set.StatusIdle)) {
		t.Errorf("idle workspace row = %q, want the muted idle glyph", line)
	}
	m.tree = treeWith("w1", "blocked")
	if line := m.renderRowLine(row, false, 40); !strings.Contains(line, m.styles.statusBlockedStyle.Render(set.StatusBlocked)) {
		t.Errorf("blocked workspace row = %q, want the blocked color", line)
	}
	pane := Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"agent_status": "idle"}}}
	if line := m.renderRowLine(pane, false, 40); !strings.Contains(line, m.styles.statusIdleStyle.Render(set.StatusIdle)) {
		t.Errorf("idle pane row = %q, want Herdr's idle color", line)
	}
}

// TestRowAccessories_PaneAgentNotRepeated proves a tree pane titled after
// its agent does not repeat the agent name as its accessory.
func TestRowAccessories_PaneAgentNotRepeated(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	for _, tc := range []struct{ label, want string }{
		{"OpenCode session", ""},
		{"zsh", "opencode"},
	} {
		row := Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: tc.label, Meta: map[string]string{"agent": "opencode"}}}
		if got := m.rowAccessoryText(row); got != tc.want {
			t.Errorf("pane %q accessories = %q, want %q", tc.label, got, tc.want)
		}
	}
}

// TestRowView_TabNumberMuted proves a tab row's number reads muted and its
// label in the row style, in the list and in the preview title.
func TestRowView_TabNumberMuted(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "code", Meta: map[string]string{"tab_number": "2"}}}
	line := m.renderRowLine(row, false, 40)
	if want := m.styles.rowPlain.muted.Render("2") + m.styles.rowPlain.label.Render(" code"); !strings.Contains(line, want) {
		t.Errorf("tab row = %q, want the muted number then the label %q", line, want)
	}
	m.rows = []Row{row}
	if title, want := m.renderPreviewTitle(30), m.styles.mutedStyle.Render("2")+m.styles.titleStyle.Render(" code"); !strings.HasPrefix(title, want) {
		t.Errorf("preview title = %q, want %q", title, want)
	}
}

// TestPlainTheme_SelectedRowIsBoldAndPreviewHasNoSGR proves the plain theme
// draws the selected row bold (never faint) and strips every SGR from
// preview content, which tools emit even when the user asked for no color.
func TestPlainTheme_SelectedRowIsBoldAndPreviewHasNoSGR(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	row := Row{Kind: RowCandidate, Candidate: zoxideCandidate("alpha", "/a")}
	selected := m.renderRowLine(row, true, 30)
	if !strings.Contains(selected, "\x1b[1") || strings.Contains(selected, ";2m") || strings.Contains(selected, "[2m") {
		t.Errorf("plain selected row = %q, want bold and never faint", selected)
	}

	plain := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemePlain)})
	plain.rows[0].Kind = RowPane
	plain.previewText = "\x1b[31mred\x1b[0m line\n\x1b]0;evil\x07"
	plain.tree = treeWith("w1")
	body := plain.previewBody(60, 20)
	if strings.Contains(body, "[31m") || strings.Contains(body, "]0;evil") || !strings.Contains(body, "red line") {
		t.Errorf("plain preview = %q, want the capture without any SGR", body)
	}
}
