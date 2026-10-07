package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
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

// TestRowAccessories_PerKind proves each row kind's right-aligned
// accessories, in display order.
func TestRowAccessories_PerKind(t *testing.T) {
	t.Parallel()
	pinnedZoxide := zoxideCandidate("cache", "/srv/cache")
	group := source.Candidate{Label: "team", Path: "/srv/team", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	for _, tc := range []struct {
		name string
		tree *TreeExpander
		row  Row
		want []accessory
	}{
		{"open workspace: most urgent agent status", treeWith("w1", "idle", "working", "blocked", ""), Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")},
			[]accessory{{text: "blocked", role: accessoryStatus, width: 1}}},
		{"open workspace: working beats done and idle", treeWith("w1", "done", "working", "idle"), Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")},
			[]accessory{{text: "working", role: accessoryStatus, width: 1}}},
		{"open workspace without agents", treeWith("w1", "", "unknown"), Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, nil},
		{"pinned candidate", nil, Row{Kind: RowCandidate, Candidate: pinnedZoxide}, []accessory{{text: "★", role: accessoryPin, width: 1}}},
		{"pinned group workspace", nil, Row{Kind: RowCandidate, Candidate: group},
			[]accessory{{text: "★", role: accessoryPin, width: 1}, {text: "›", width: 1}}},
		{"worktree branch", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "api", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "main"}}},
			[]accessory{{text: "main", width: 4}}},
		{"session state", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "s", Source: config.SourceSessions, Meta: map[string]string{"running": "true", "default": "true"}}},
			[]accessory{{text: "running · default", width: 17}}},
		{"missing path", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "gone", Path: "/gone", Source: config.SourceProjects, Missing: true}},
			[]accessory{{text: "missing", role: accessoryError, width: 7}}},
		{"agent row: a path-like workspace shows its last element", nil, Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix it", Source: config.SourceAgents, Meta: map[string]string{"workspace_label": "/home/dev/Proyectos/fsociety/"}}},
			[]accessory{{text: "fsociety", width: 8, shrink: true}}},
		{"agent row: a named workspace keeps its label", nil, Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix it", Source: config.SourceAgents, Meta: map[string]string{"workspace_label": "app contract"}}},
			[]accessory{{text: "app contract", width: 12, shrink: true}}},
		{"tree pane: its agent", nil, Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"agent": "claude"}}},
			[]accessory{{text: "claude", width: 6}}},
		{"tab row: none", nil, Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRenderTestModel(ThemeMocha, FocusList)
			m.homeDir = "/home/dev"
			m.tree = tc.tree
			m.rankingSnapshot = m.rankingSnapshot.WithPinned(ranking.PinKey(pinnedZoxide), true).WithPinned(ranking.PinKey(group), true)
			got := m.buildRowView(tc.row).accessories
			if len(got) != len(tc.want) {
				t.Fatalf("accessories = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("accessory %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRowView_AgentWorkspaceLabelCappedAndTitleKeepsStart proves an agent
// row's workspace accessory takes at most min(30% of the row, 24 cells),
// keeping its start, and is dropped when the title would keep fewer than 24
// cells; the title keeps its start.
func TestRowView_AgentWorkspaceLabelCappedAndTitleKeepsStart(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowPane, Candidate: source.Candidate{Label: "Refactor the render path of shep and measure everything", Source: config.SourceAgents,
		Meta: map[string]string{"workspace_label": "platform-engineering-workspace"}}}
	line := strings.TrimRight(renderRowLineText(m.renderRowLine(row, false, 60)), " ")
	if !strings.HasSuffix(line, " platform-engineer…") || ansi.StringWidth("platform-engineer…") != 60*agentLabelMaxPercent/100 {
		t.Errorf("agent row = %q, want the workspace label capped at 30%% of the row, keeping its start", line)
	}
	if !strings.HasPrefix(line, "  Refactor the render path of shep and m… ") {
		t.Errorf("agent row = %q, want the title's start, truncated on the right", line)
	}
	if got := fitAccessoriesWidth(200); got != agentLabelMaxCells {
		t.Errorf("workspace label at 200 columns = %d cells, want the %d-cell cap", got, agentLabelMaxCells)
	}
	narrow := strings.TrimRight(renderRowLineText(m.renderRowLine(row, false, 36)), " ")
	if strings.Contains(narrow, "platform") || !strings.HasPrefix(narrow, "  Refactor the render path of shep") || ansi.StringWidth(narrow) != 36 {
		t.Errorf("narrow agent row = %q, want the workspace dropped so the title keeps its room", narrow)
	}
}

// fitAccessoriesWidth is the width an agent workspace label 40 cells wide
// gets in a row of rowWidth cells.
func fitAccessoriesWidth(rowWidth int) int {
	_, w := fitAccessories([]accessory{{text: strings.Repeat("w", 40), width: 40, shrink: true}}, rowWidth)
	return w
}

// TestRowView_IconRolesPerSource proves icons are colored by source family
// and that the plain theme leaves them uncolored. Not t.Parallel: it swaps
// lipgloss's global color profile.
func TestRowView_IconRolesPerSource(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	mocha := themes[ThemeMocha]
	for _, tc := range []struct {
		name  string
		row   Row
		role  iconRole
		color string
	}{
		{"herdr", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "H", Source: config.SourceHerdr}}, iconRoleHerdr, mocha.Success},
		{"workspaces", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "W", Source: config.SourceWorkspaces}}, iconRoleWorkspaces, mocha.Lavender},
		{"zoxide", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "Z", Source: config.SourceZoxide}}, iconRoleZoxide, mocha.Blue},
		{"projects", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "P", Source: config.SourceProjects}}, iconRoleProjects, mocha.Peach},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "P", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true"}}}, iconRoleProjects, mocha.Peach},
		{"sessions", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "S", Source: config.SourceSessions}}, iconRoleSessions, mocha.Teal},
		{"agents", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "A", Source: config.SourceAgents}}, iconRoleAgents, mocha.Accent},
		{"custom", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Icon: "C", Source: "prs"}}, iconRoleCustom, mocha.Sky},
		{"tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "a"}}, iconRoleTab, mocha.Muted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRenderTestModel(ThemeMocha, FocusList)
			v := m.buildRowView(tc.row)
			if v.iconRole != tc.role {
				t.Fatalf("icon role = %d, want %d", v.iconRole, tc.role)
			}
			want := lipgloss.NewStyle().Foreground(lipgloss.Color(tc.color)).Render(v.icon)
			if line := m.renderRowLine(tc.row, false, 30); !strings.Contains(line, want) {
				t.Errorf("row %q does not draw the icon as %q", line, want)
			}
			plain := newRenderTestModel(ThemePlain, FocusList)
			if line := plain.renderRowLine(tc.row, false, 30); strings.Contains(line, "38;2;") {
				t.Errorf("plain row %q colors its icon", line)
			}
		})
	}
}

// TestLabelFormatDefaults_MatchConfig proves the picker's fallback row
// formats (used by direct Layout callers) stay identical to the defaults
// config.Load fills in, including the label-only tree child formats.
func TestLabelFormatDefaults_MatchConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[general]\nsource_order = [\"herdr\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := LabelFormats{}.withDefaults()
	s := cfg.Sources
	for _, tc := range []struct{ name, tui, config string }{
		{"herdr", got.Herdr, s.Herdr.LabelFormat},
		{"tab", got.Tab, s.Herdr.TabLabelFormat},
		{"pane", got.Pane, s.Herdr.PaneLabelFormat},
		{"sessions", got.Sessions, s.Sessions.LabelFormat},
		{"workspaces", got.Workspaces, s.Workspaces.LabelFormat},
		{"zoxide", got.Zoxide, s.Zoxide.LabelFormat},
		{"projects", got.Projects, s.Projects.LabelFormat},
		{"agents", got.Agents, s.Agents.LabelFormat},
	} {
		if tc.tui != tc.config {
			t.Errorf("%s default: tui %q, config %q", tc.name, tc.tui, tc.config)
		}
	}
	if got.Tab != "{{.Label}}" || got.Pane != "{{if .Label}}{{.Label}}{{else}}{{.Path}}{{end}}" {
		t.Errorf("tree defaults = %q / %q, want label-only", got.Tab, got.Pane)
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
	m := NewModelWithLayout(numberedCandidates(30), nil, Layout{Theme: ThemeMocha})
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
	wide := NewModelWithLayout(numberedCandidates(20), nil, Layout{Theme: ThemeMocha})
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
	narrow := NewModelWithLayout(long, nil, Layout{Theme: ThemeMocha})
	narrow, _ = update(t, narrow, sizeMsg(64, 10+chromeRows))
	last := narrow.geometry().Margin + narrow.geometry().ListWidth - 1
	if got := bodyColumn(narrow, last); got != "┃┃┃┃┃│││││" {
		t.Errorf("list-only track = %q, want the thumb in the list's last column", got)
	}
	if got := bodyColumn(narrow, last-1); got != strings.Repeat(" ", 10) {
		t.Errorf("cells before the list-only track = %q, want a blank gap on every row", got)
	}

	fits := NewModelWithLayout(numberedCandidates(5), nil, Layout{Theme: ThemeMocha})
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
	m := NewModelWithTree([]source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, nil, treeWith("w1", "idle"), Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 20))
	if m.spinnerNeeded() {
		t.Fatal("idle workspace must not arm the spinner")
	}
	if got := m.rowWindow.views[0].accessories; len(got) != 1 || got[0].text != "idle" {
		t.Fatalf("cached accessories = %+v, want the idle status", got)
	}
	m, cmd := update(t, m, paneStatusMsg{PaneID: "w1:pa", Status: "working"})
	if !m.spinnerNeeded() || !m.spinnerRunning || cmd == nil {
		t.Fatalf("working workspace: needed=%v running=%v cmd=%v, want the spinner armed", m.spinnerNeeded(), m.spinnerRunning, cmd != nil)
	}
	if got := m.rowWindow.views[0].accessories; len(got) != 1 || got[0].text != "working" {
		t.Errorf("cached accessories after the live update = %+v, want working", got)
	}
}

// TestRowWindow_ReusedAcrossFrames proves a frame that changes no row (a
// spinner tick) reuses the cached row views instead of rebuilding them,
// while a new query rebuilds them.
func TestRowWindow_ReusedAcrossFrames(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(numberedCandidates(30), nil, Layout{Theme: ThemeMocha})
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
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: ThemeMocha})
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
	if strings.TrimSpace(preview) != "" || strings.Contains(m.View(), "(no selection)") {
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

	m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: ThemeMocha})
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
	m := NewModelWithTree([]source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, nil, treeWith("w1", "idle"), Layout{Theme: ThemeMocha})
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	m, _ = update(t, m, sizeMsg(120, 20))
	idle, blocked := m.agentStatusIcon("idle"), m.agentStatusIcon("blocked")
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
// and draws muted, while blocked keeps its color. Not t.Parallel: it swaps
// lipgloss's global color profile.
func TestRowAccessories_IdleWorkspaceIsMuted(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

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
// label in the row style, in the list and in the preview title. Not
// t.Parallel: it swaps lipgloss's global color profile.
func TestRowView_TabNumberMuted(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "code", Meta: map[string]string{"tab_number": "2"}}}
	line := m.renderRowLine(row, false, 40)
	if want := m.styles.rowPlain.muted.Render("2") + m.styles.rowPlain.primary.Render(" code"); !strings.Contains(line, want) {
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
// Not t.Parallel: it swaps lipgloss's global color profile.
func TestPlainTheme_SelectedRowIsBoldAndPreviewHasNoSGR(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	m := newRenderTestModel(ThemePlain, FocusList)
	row := Row{Kind: RowCandidate, Candidate: zoxideCandidate("alpha", "/a")}
	selected := m.renderRowLine(row, true, 30)
	if !strings.Contains(selected, "\x1b[1") || strings.Contains(selected, ";2m") || strings.Contains(selected, "[2m") {
		t.Errorf("plain selected row = %q, want bold and never faint", selected)
	}

	plain := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: ThemePlain})
	plain.rows[0].Kind = RowPane
	plain.previewText = "\x1b[31mred\x1b[0m line\n\x1b]0;evil\x07"
	plain.tree = treeWith("w1")
	body := plain.previewBody(60, 20)
	if strings.Contains(body, "[31m") || strings.Contains(body, "]0;evil") || !strings.Contains(body, "red line") {
		t.Errorf("plain preview = %q, want the capture without any SGR", body)
	}
}
