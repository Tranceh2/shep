package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/tranceh2/shep/internal/source"
)

// TestPromptRow_CursorAndPlaceholderPerView proves the prompt row's two
// states: an empty query shows the prompt, the cursor cell and the view's
// own placeholder; a typed query shows the prompt, the query and the cursor
// cell right after it.
func TestPromptRow_CursorAndPlaceholderPerView(t *testing.T) {
	t.Parallel()
	tabs := []TabDefinition{
		{ID: "all", Kind: TabAll},
		{ID: "agents", Kind: TabAgents},
		{ID: "projects", Kind: TabSource},
		{ID: "team", Kind: TabGroup, Label: "Platform"},
	}
	for _, tc := range []struct {
		tab, want string
	}{
		{"all", "Search workspaces, projects, folders"},
		{"agents", "Search agents"},
		{"projects", "Search projects"},
		{"team", "Search Platform"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			m := NewModelWithLayout(nil, nil, Layout{Theme: testTheme(ThemeMocha), Tabs: tabs, InitialTab: tc.tab})
			m, _ = update(t, m, sizeMsg(120, 30))
			if got := promptText(m); !strings.HasPrefix(got, "❯  "+tc.want) {
				t.Errorf("prompt row = %q, want the prompt, the cursor cell and %q", got, tc.want)
			}
		})
	}

	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 30))
	for _, r := range "al" {
		m, _ = update(t, m, key(string(r)))
	}
	row := m.renderPromptRow(m.geometry().ListWidth)
	want := m.styles.promptStyle.Render("❯") + " " + m.styles.queryTextStyle.Render("al") + m.styles.queryCursorStyle.Render(" ")
	if !strings.HasPrefix(row, want) {
		t.Errorf("prompt row = %q, want the query followed by the cursor cell", row)
	}
	if !strings.HasSuffix(strings.TrimSpace(stripNonSGRANSI(row)), "1/1") {
		t.Errorf("prompt row = %q, want the filtered count right-aligned", row)
	}
}

// TestPromptRow_QueryNeverCutForCount proves the row's priority: the count
// is dropped before the query loses a cell, and a query wider than the row
// keeps its end (where the cursor is) behind a leading ellipsis.
func TestPromptRow_QueryNeverCutForCount(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	for _, tc := range []struct {
		name, query, want string
	}{
		{"fits with the count", "alp", "❯ alp" + strings.Repeat(" ", 14) + "1/1"},
		{"count dropped first", "abcdefghijklmnop", "❯ abcdefghijklmnop"},
		{"query keeps its end", "abcdefghijklmnopqrstuvwxyz", "❯ …ijklmnopqrstuvwxyz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := m
			m.query = tc.query
			m.applyFilter()
			row := m.renderPromptRow(22)
			if got := ansi.StringWidth(row); got != 22 {
				t.Errorf("prompt row width = %d, want 22", got)
			}
			if got := strings.TrimRight(stripNonSGRANSI(row), " "); got != tc.want {
				t.Errorf("prompt row = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHelpOverlay_UsesTheGrid proves the help overlay keeps the grid: the
// tab strip on row 0, its title on the prompt row, a full rule, the help
// viewport over exactly the body rows, and its own footer.
func TestHelpOverlay_UsesTheGrid(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 36))
	m, _ = update(t, m, key("?"))
	g := m.geometry()
	if m.helpViewport.Width != g.ContentWidth || m.helpViewport.Height != 36-chromeRows {
		t.Errorf("help viewport = %dx%d, want %dx%d", m.helpViewport.Width, m.helpViewport.Height, g.ContentWidth, 36-chromeRows)
	}
	lines := viewLines(m)
	if len(lines) != 36 {
		t.Fatalf("help View() has %d lines, want 36", len(lines))
	}
	if !strings.Contains(lines[0], " all ") {
		t.Errorf("help row 0 = %q, want the tab strip", lines[0])
	}
	if got := strings.TrimSpace(lines[1]); got != helpTitle {
		t.Errorf("help title row = %q, want %q", got, helpTitle)
	}
	if got := strings.TrimSpace(lines[2]); got != strings.Repeat("─", g.ContentWidth) {
		t.Errorf("help rule = %q, want a full rule without a junction", got)
	}
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "esc close · ↑↓ scroll" {
		t.Errorf("help footer = %q", got)
	}
}

// TestView_PlainThemeStructureWithoutColor renders a real frame with a
// true-color profile and proves the plain theme stays structural: no
// foreground or background color sequence anywhere, while the active tab
// and the prompt cursor still render in reverse video. The mocha theme, in
// contrast, paints the cursor cell with the accent background. Not
// t.Parallel: it swaps lipgloss's global color profile.
func TestView_PlainThemeStructureWithoutColor(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	plain := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemePlain)})
	plain, _ = update(t, plain, sizeMsg(120, 30))
	view := plain.View()
	for _, color := range []string{"[38;", "[48;", ";38;", ";48;"} {
		if strings.Contains(view, color) {
			t.Fatalf("plain View() contains a color sequence %q", color)
		}
	}
	lines := strings.Split(view, "\n")
	if !strings.Contains(lines[0], "\x1b[7") && !strings.Contains(lines[0], ";7m") {
		t.Errorf("plain tab strip = %q, want the active tab in reverse video", lines[0])
	}
	if !strings.Contains(lines[1], plain.styles.queryCursorStyle.Render(" ")) {
		t.Errorf("plain prompt row = %q, want the reverse-video cursor cell", lines[1])
	}

	mocha := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha)})
	mocha, _ = update(t, mocha, sizeMsg(120, 30))
	if cursor := mocha.styles.queryCursorStyle.Render(" "); !strings.Contains(cursor, "48;2;") || !strings.Contains(mocha.View(), cursor) {
		t.Errorf("mocha cursor cell = %q, want an accent background in the prompt row", cursor)
	}
}
