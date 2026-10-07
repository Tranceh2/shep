package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/theme"
	"github.com/tranceh2/shep/internal/tmpl"
)

// drawnParts is a row's four parts as drawn, as plain text (a working status
// glyph shows the spinner's current frame).
type drawnParts struct{ icon, label, detail, marker string }

func (m Model) drawnParts(row Row) drawnParts {
	v := m.buildRowView(row)
	return drawnParts{m.partText(&v.icon), m.partText(&v.label), m.partText(&v.detail), m.partText(&v.marker)}
}

// TestDefaultPresentations_ReproduceTheRowTexts proves the built-in
// presentation templates draw every kind of row exactly as the picker drew it
// before rows were built from templates: the name-first split of path-like
// labels (home shown as "~"), the source icons and the worktree glyph, and the
// markers in their order — current, a worktree's branch, a session's state,
// missing, an open workspace's status, the pin, the group chevron; an agent's
// status glyph before its title and its workspace's last path element on the
// right; a tab's number and label; a pane's status glyph, name-first path
// fallback and agent.
func TestDefaultPresentations_ReproduceTheRowTexts(t *testing.T) {
	t.Parallel()
	const (
		herdrIcon     = "\U000f0cc6 "
		workspaceIcon = "\ue615 "
		zoxideIcon    = "\uf114 "
		projectIcon   = "\ue702 "
		worktreeIcon  = "\ue725 "
	)
	pinned := zoxideCandidate("~/src/pinned", "/home/dev/src/pinned")
	group := source.Candidate{Label: "team", Path: "/home/dev/teams/platform", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	pinnedHerdr := herdrCandidate("api", "/srv/api", "w1")
	pins := ranking.Snapshot{}.WithPinned(ranking.PinKey(pinned), true).WithPinned(ranking.PinKey(group), true).WithPinned(ranking.PinKey(pinnedHerdr), true)
	current := &source.Pane{ID: "p-cur", TabID: "t-cur", WorkspaceID: "w1"}
	working := newRenderTestModel(ThemeMocha, FocusList).agentStatusIcon("working")
	tab := func(label, number string) Row {
		meta := map[string]string{"tab_id": "t1"}
		if number != "" {
			meta["tab_number"] = number
		}
		if label != "" {
			meta["tab_label"] = label
		}
		return Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: label, Path: "/srv/api", Meta: meta}}
	}
	pane := func(label, path string, meta map[string]string) Row {
		return Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: label, Path: path, Meta: meta}}
	}
	for _, tc := range []struct {
		name    string
		row     Row
		tree    *TreeExpander
		current *source.Pane
		want    drawnParts
	}{
		{"herdr workspace named by its path", Row{Kind: RowCandidate, Candidate: herdrCandidate("/home/dev/Proyectos/shep", "/home/dev/Proyectos/shep", "w9")}, nil, nil,
			drawnParts{herdrIcon, "shep", "~/Proyectos", ""}},
		{"herdr workspace with a name", Row{Kind: RowCandidate, Candidate: herdrCandidate("backend", "/srv/backend", "w9")}, nil, nil,
			drawnParts{herdrIcon, "backend", "", ""}},
		{"current, working, pinned workspace", Row{Kind: RowCandidate, Candidate: pinnedHerdr}, treeWith("w1", "idle", "working"), current,
			drawnParts{herdrIcon, "api", "", "current " + working + " ★"}},
		{"idle workspace", Row{Kind: RowCandidate, Candidate: herdrCandidate("web", "/srv/web", "w2")}, treeWith("w2", "idle"), nil,
			drawnParts{herdrIcon, "web", "", "✓"}},
		{"missing blocked workspace", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "gone", Source: config.SourceHerdr, Missing: true, Meta: map[string]string{"workspace_id": "w2"}}}, treeWith("w2", "blocked"), nil,
			drawnParts{herdrIcon, "gone", "", "missing ◉"}},
		{"project", Row{Kind: RowCandidate, Candidate: projectCandidate("~/Proyectos/shep", "/home/dev/Proyectos/shep")}, nil, nil,
			drawnParts{projectIcon, "shep", "~/Proyectos", ""}},
		{"missing project", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "~/old/archive", Path: "/home/dev/old/archive", Source: config.SourceProjects, Missing: true}}, nil, nil,
			drawnParts{projectIcon, "archive", "~/old", "missing"}},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "~/trees/api-fix", Path: "/home/dev/trees/api-fix", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "fix/parser"}}}, nil, nil,
			drawnParts{worktreeIcon, "api-fix", "~/trees", "fix/parser"}},
		{"zoxide folder", Row{Kind: RowCandidate, Candidate: zoxideCandidate("~/src/repo-01", "/home/dev/src/repo-01")}, nil, nil,
			drawnParts{zoxideIcon, "repo-01", "~/src", ""}},
		{"zoxide folder without a label", Row{Kind: RowCandidate, Candidate: zoxideCandidate("", "/home/dev/Proyectos/shep")}, nil, nil,
			drawnParts{zoxideIcon, "shep", "~/Proyectos", ""}},
		{"pinned folder", Row{Kind: RowCandidate, Candidate: pinned}, nil, nil,
			drawnParts{zoxideIcon, "pinned", "~/src", "★"}},
		{"configured workspace", Row{Kind: RowCandidate, Candidate: workspaceEntryCandidate("notes", "/home/dev/notes")}, nil, nil,
			drawnParts{workspaceIcon, "notes", "", ""}},
		{"pinned group", Row{Kind: RowCandidate, Candidate: group}, nil, nil,
			drawnParts{workspaceIcon, "team", "", "★ ›"}},
		{"running default session", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "main", Source: config.SourceSessions, Meta: map[string]string{"running": "true", "default": "true"}}}, nil, nil,
			drawnParts{"", "main", "", "running · default"}},
		{"stopped session", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "old", Source: config.SourceSessions, Meta: map[string]string{"running": "false"}}}, nil, nil,
			drawnParts{"", "old", "", "stopped"}},
		{"agent with a workspace", Row{Kind: RowPane, Candidate: source.Candidate{Label: "Review the parser", Source: config.SourceAgents, Meta: map[string]string{"agent_status": "blocked", "workspace_label": "/home/dev/allsafe/whiterose-db"}}}, nil, nil,
			drawnParts{"", "◉ Review the parser", "", "whiterose-db"}},
		{"agent in the all view", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "fix", Source: config.SourceAgents, Meta: map[string]string{"agent_status": "idle", "workspace_label": "billing"}}}, nil, nil,
			drawnParts{"", "✓ fix", "", "billing"}},
		{"agent without a status", Row{Kind: RowPane, Candidate: source.Candidate{Label: "Review", Source: config.SourceAgents}}, nil, nil,
			drawnParts{"", "Review", "", ""}},
		{"tab with a number and a label", tab("editor", "2"), nil, nil, drawnParts{"◫", "2 editor", "", ""}},
		{"tab labelled with its own number", tab("3", "3"), nil, nil, drawnParts{"◫", "3", "", ""}},
		{"tab with only a number", tab("", "4"), nil, nil, drawnParts{"◫", "4", "", ""}},
		{"tab without a number", tab("api", ""), nil, nil, drawnParts{"◫", "api", "", ""}},
		{"current tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t-cur", "tab_label": "api"}}}, nil, current,
			drawnParts{"◫", "api", "", "current"}},
		{"pane with a label", pane("nvim", "/srv/api", nil), nil, nil, drawnParts{"", "nvim", "", ""}},
		{"pane without a label", pane("", "/home/dev/allsafe/app", nil), nil, nil, drawnParts{"", "app", "~/allsafe", ""}},
		{"pane with an agent and a status", pane("zsh", "/srv/api", map[string]string{"agent": "claude", "agent_status": "idle"}), nil, nil,
			drawnParts{"", "✓ zsh", "", "claude"}},
		{"pane titled after its agent", pane("Claude Code", "/srv/api", map[string]string{"agent": "claude", "agent_status": "working"}), nil, nil,
			drawnParts{"", working + " Claude Code", "", ""}},
		{"current pane with an agent", pane("zsh", "/srv/api", map[string]string{"pane_id": "p-cur", "agent": "codex"}), nil, current,
			drawnParts{"", "zsh", "", "current codex"}},
		{"custom source row", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "#42 fix", Icon: "P", Source: "prs"}}, nil, nil,
			drawnParts{"P", "#42 fix", "", ""}},
		{"direct path candidate: its path, name first", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "label", Path: "/srv/x", Source: "path"}}, nil, nil,
			drawnParts{"", "x", "/srv", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
				p.Custom = map[string]config.RowPresentation{"prs": config.DefaultCustomPresentation("")}
			})
			m.layout.Templates = tmpl.New("/home/dev")
			m.tree = tc.tree
			m.currentPane = tc.current
			m.rankingSnapshot = pins
			if got := m.drawnParts(tc.row); got != tc.want {
				t.Errorf("parts =\n %q\nwant\n %q", got, tc.want)
			}
		})
	}
}

// TestDefaultPresentations_ASCIITier proves the ASCII tier draws its own
// glyphs: the tab icon, the session separator, the status glyphs, the pin
// and the group chevron, and no Nerd Font source icon, so it stays 7-bit.
func TestDefaultPresentations_ASCIITier(t *testing.T) {
	t.Parallel()
	m := newRenderTestModelWithIcons(ThemeMocha, IconsASCII)
	group := source.Candidate{Label: "team", Path: "/srv/team", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	m.rankingSnapshot = ranking.Snapshot{}.WithPinned(ranking.PinKey(group), true)
	m.tree = treeWith("w1", "blocked")
	for _, tc := range []struct {
		name string
		row  Row
		want drawnParts
	}{
		{"tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_label": "api", "tab_number": "1"}}}, drawnParts{"t", "1 api", "", ""}},
		{"session", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "main", Source: config.SourceSessions, Meta: map[string]string{"running": "true", "default": "true"}}}, drawnParts{"", "main", "", "running - default"}},
		{"blocked workspace", Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, drawnParts{"", "api", "", "!"}},
		{"pinned group", Row{Kind: RowCandidate, Candidate: group}, drawnParts{"", "team", "", "* >"}},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "x", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "main"}}}, drawnParts{"", "x", "", "main"}},
		{"working agent", Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix", Source: config.SourceAgents, Meta: map[string]string{"agent_status": "working"}}}, drawnParts{"", "o fix", "", ""}},
	} {
		if got := m.drawnParts(tc.row); got != tc.want {
			t.Errorf("%s: parts = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPresentation_ExplicitEmptyPartsAreKept proves an explicit "" part
// draws nothing instead of falling back to a default: no icon (and no icon
// cell), no detail, no marker.
func TestPresentation_ExplicitEmptyPartsAreKept(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Zoxide.Icon, p.Zoxide.Detail, p.Zoxide.Marker = "", "", ""
	})
	m.layout.Templates = tmpl.New("/home/dev")
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "~/src/repo", Path: "/home/dev/src/repo", Source: config.SourceZoxide, Missing: true}}
	v := m.buildRowView(row)
	if got := m.drawnParts(row); got != (drawnParts{"", "repo", "", ""}) || v.fixedW != 0 {
		t.Errorf("parts = %q (fixed %d cells), want the label alone", got, v.fixedW)
	}
	if line := ansi.Strip(m.renderRowLine(row, false, 30)); line != "  repo"+strings.Repeat(" ", 24) {
		t.Errorf("row = %q, want the label right after the gutter", line)
	}
}

// TestPresentation_StyleAndLiveFunctions proves custom templates style their
// parts with the semantic functions and place live markers anywhere: each
// run draws in its role, and a marker with nothing to show leaves no gap.
func TestPresentation_StyleAndLiveFunctions(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Herdr.Icon = "{{ status }}"
		p.Herdr.Label = "{{ bold .Label }} {{ pin }}"
		p.Herdr.Detail = "{{ accent .Path }}"
		p.Herdr.Marker = "{{ muted \"ws\" }} {{ current }}  {{ group }} {{ missing }}"
	})
	m.tree = treeWith("w1", "blocked")
	row := Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}
	if got := m.drawnParts(row); got != (drawnParts{"◉", "api", "/srv/api", "ws"}) {
		t.Fatalf("parts = %q, want the status as the icon and no gaps for the absent markers", got)
	}
	v := m.buildRowView(row)
	if len(v.label.runs) != 1 || !v.label.runs[0].bold || v.detail.runs[0].role != roleAccent || v.marker.runs[0].role != roleMuted || v.icon.runs[0].role != roleStatusBlocked {
		t.Fatalf("runs: icon %+v label %+v detail %+v marker %+v", v.icon.runs, v.label.runs, v.detail.runs, v.marker.runs)
	}
	th := testTheme(ThemeMocha)
	line := m.renderRowLine(row, false, 50)
	for _, want := range []string{
		lipgloss.NewStyle().Foreground(th.Role(theme.RoleStatusBlocked).Lipgloss()).Bold(true).Render("◉"),
		m.styles.rowPlain.label.Bold(true).Render("api"),
		lipgloss.NewStyle().Foreground(th.Role(theme.RoleAccent).Lipgloss()).Render("/srv/api"),
		lipgloss.NewStyle().Foreground(th.Role(theme.RoleTextMuted).Lipgloss()).Render("ws"),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("row %q does not draw %q", line, want)
		}
	}
	m.rankingSnapshot = ranking.Snapshot{}.WithPinned(ranking.PinKey(row.Candidate), true)
	if got := m.drawnParts(row).label; got != "api ★" {
		t.Errorf("label with the pin = %q, want \"api ★\"", got)
	}
}

// TestPresentation_DataCannotStyleARow proves provider text that carries the
// reserved markup runes is drawn as plain text: the runes are removed before
// the template runs, so a label can never style its row or fake a marker.
func TestPresentation_DataCannotStyleARow(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Custom = map[string]config.RowPresentation{"prs": config.DefaultCustomPresentation("")}
	})
	muted, end, pin := string(rune(0xFDD1)), string(rune(0xFDD0)), string(rune(0xFDD9))
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Label: muted + "evil" + end + pin, Source: "prs", Icon: pin}}
	v := m.buildRowView(row)
	if v.label.text != "evil" || v.label.runs != nil || v.icon.width != 0 || v.marker.width != 0 {
		t.Errorf("view = label %q runs %+v icon %q marker %q, want plain \"evil\"", v.label.text, v.label.runs, v.icon.text, v.marker.text)
	}
}

// TestPresentation_FailingTemplateKeepsTheRow proves a template that fails
// for one row costs only its own part: the label falls back to the path
// (home shown as "~") and the other parts still render.
func TestPresentation_FailingTemplateKeepsTheRow(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Zoxide.Label = "{{ slice .Label 0 40 }}"
	})
	m.layout.Templates = tmpl.New("/home/dev")
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "~/src/repo", Path: "/home/dev/src/repo", Source: config.SourceZoxide, Missing: true}}
	if got := m.drawnParts(row); got != (drawnParts{"\uf114 ", "~/src/repo", "~/src", "missing"}) {
		t.Errorf("parts = %q, want the path label and the other parts", got)
	}
}

// TestPresentation_TemplateData proves row templates see the shared data
// model: every candidate gets its source's Kind and the Herdr rows the
// picker synthesizes get tab, pane or agent, plus the tab, pane and agent
// fields and the Meta map.
func TestPresentation_TemplateData(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Herdr.Label = "{{.Kind}}"
		p.Zoxide.Label = "{{.Kind}} {{ .Label | name }} [{{ .Label | parent }}]"
		p.Projects.Label = "{{.Kind}} {{.RepoName}}@{{.Branch}} {{.Head}}"
		p.HerdrTab.Label = "{{.Kind}} {{.Label}} n={{.TabNumber}} l={{.TabLabel}} ws={{.Workspace}}"
		p.HerdrPane.Label = "{{.Kind}} {{.Agent}}/{{.AgentStatus}} {{.TabLabel}} {{.Meta.absent}}x"
		p.Agents.Label = "{{.Kind}} {{.Agent}} in {{ .Workspace | trimIcon | name }}"
		p.Custom = map[string]config.RowPresentation{"prs": {Label: "PR {{.Label}} {{.Meta.author}}"}}
	})
	tabMeta := map[string]string{"tab_number": "2", "tab_label": "editor", "workspace_label": "~/srv/api"}
	paneMeta := map[string]string{"agent": "claude", "agent_status": "idle", "tab_label": "editor"}
	agentMeta := map[string]string{"agent": "pi", "agent_status": "working", "workspace_label": "\U000f0cc6 ~/srv/api", "kind": "agent"}
	for _, tc := range []struct {
		name string
		row  Row
		want string
	}{
		{"herdr workspace", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Label: "api", Path: "/srv/api"}}, "workspace"},
		{"zoxide folder", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Label: "~/srv/api", Path: "/home/u/srv/api"}}, "folder api [~/srv]"},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Label: "api (x)", Path: "/t/x", Meta: map[string]string{"is_worktree": "true", "repo": "api", "branch": "x", "head": "0123456789"}}}, "worktree api@x 0123456"},
		{"tree tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "editor", Path: "/srv/api", Meta: tabMeta}}, "tab editor n=2 l=editor ws=~/srv/api"},
		{"tree pane", Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "nvim", Path: "/srv/api", Meta: paneMeta}}, "pane claude/idle editor x"},
		{"flat agent row", Row{Kind: RowPane, Depth: 0, Candidate: source.Candidate{Source: config.SourceAgents, Label: "fix", Path: "/srv/api", Meta: agentMeta}}, "agent pi in api"},
		{"custom source by name", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: "prs", Label: "42", Meta: map[string]string{"author": "octocat"}}}, "PR 42 octocat"},
	} {
		if got := m.buildRowView(tc.row).label.text; got != tc.want {
			t.Errorf("%s: label = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRowView_KindLabels proves the preview title's kind vocabulary: the
// template Kind of every row, an agent's program, a custom source's name.
func TestRowView_KindLabels(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	for _, tc := range []struct {
		row  Row
		want string
	}{
		{Row{Kind: RowCandidate, Candidate: herdrCandidate("a", "/a", "w1")}, "workspace"},
		{Row{Kind: RowCandidate, Candidate: workspaceEntryCandidate("a", "/a")}, "configured"},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}}, "group"},
		{Row{Kind: RowCandidate, Candidate: zoxideCandidate("a", "/a")}, "folder"},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Path: "/a", Source: "path"}}, "folder"},
		{Row{Kind: RowCandidate, Candidate: projectCandidate("a", "/a")}, "project"},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true"}}}, "worktree"},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceSessions}}, "session"},
		{Row{Kind: RowPane, Candidate: source.Candidate{Label: "a", Source: config.SourceAgents, Meta: map[string]string{"agent": "claude"}}}, "claude"},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: config.SourceAgents}}, "agent"},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "a", Source: "prs"}}, "prs"},
		{Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "a"}}, "tab"},
		{Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "a"}}, "pane"},
	} {
		if got := m.buildRowView(tc.row).kind; got != tc.want {
			t.Errorf("row %+v: kind = %q, want %q", tc.row.Candidate, got, tc.want)
		}
	}
}

// TestRowView_AgentIconBeforeStatusAndTitle proves an agents presentation's
// icon draws before the status glyph and the title, in the agents tab and in
// source or group tabs alike.
func TestRowView_AgentIconBeforeStatusAndTitle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) { p.Agents.Icon = "X " })
	m.startupSnapshot = &source.Snapshot{Panes: []source.Pane{{ID: "p1", Agent: "pi", AgentStatus: "idle", TerminalTitle: "title"}}}
	rows := m.buildAgentRows()
	if len(rows) != 1 {
		t.Fatalf("agent rows = %d, want 1", len(rows))
	}
	if got := ansi.Strip(m.renderRowLine(rows[0], false, 40)); !strings.Contains(got, "X  "+ansi.Strip(m.agentStatusIcon("idle"))+" title") {
		t.Errorf("agents tab rendered %q, want icon before status and title", got)
	}
	for _, kind := range []RowKind{RowPane, RowCandidate} {
		row := Row{Kind: kind, Candidate: source.Candidate{Source: config.SourceAgents, Label: "security scan", Meta: map[string]string{"agent_status": "blocked"}}}
		primary, prefix := m.rowPrimaryText(row)
		if want := "X  " + ansi.Strip(m.agentStatusIcon("blocked")) + " security scan"; primary != want || prefix != 3 {
			t.Errorf("kind %d: primary = %q (prefix %d), want %q (prefix 3)", kind, primary, prefix, want)
		}
	}
}

// TestRowView_DirectoryLabelsUseTildeAndFallback proves the default
// name-first layout of directory rows: home shown as "~", the path when
// there is no label, and the split of every path-like label.
func TestRowView_DirectoryLabelsUseTildeAndFallback(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.Templates = tmpl.New("/home/dev")
	for _, tt := range []struct {
		name          string
		source        string
		label         string
		path          string
		want, wantSec string
	}{
		{name: "zoxide relative label", source: config.SourceZoxide, label: "~/Proyectos/shep", path: "/workspace/Proyectos/shep", want: "shep", wantSec: "~/Proyectos"},
		{name: "projects relative label", source: config.SourceProjects, label: "~/Proyectos/shep", path: "/workspace/Proyectos/shep", want: "shep", wantSec: "~/Proyectos"},
		{name: "zoxide empty label fallback", source: config.SourceZoxide, path: "/opt/shep", want: "shep", wantSec: "/opt"},
		{name: "projects empty label fallback", source: config.SourceProjects, path: "/opt/shep", want: "shep", wantSec: "/opt"},
		{name: "configured workspace without a name", source: config.SourceWorkspaces, path: "/srv/unnamed", want: "unnamed", wantSec: "/srv"},
		{name: "fallback path under home is abbreviated", source: config.SourceZoxide, path: "/home/dev/Proyectos/shep", want: "shep", wantSec: "~/Proyectos"},
		{name: "home itself", source: config.SourceZoxide, path: "/home/dev", want: "~"},
		{name: "a sibling of home is not abbreviated", source: config.SourceZoxide, path: "/home/devops/x", want: "x", wantSec: "/home/devops"},
		{name: "directly under home", source: config.SourceZoxide, path: "/home/dev/notes", want: "notes", wantSec: "~"},
		{name: "directly under root", source: config.SourceZoxide, path: "/tmp", want: "tmp", wantSec: "/"},
		{name: "one trailing slash is ignored", source: config.SourceZoxide, label: "~/src/", path: "/x", want: "src", wantSec: "~"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := m.buildRowView(Row{Kind: RowCandidate, Candidate: source.Candidate{Source: tt.source, Label: tt.label, Path: tt.path}})
			if v.label.text != tt.want || v.detail.text != tt.wantSec {
				t.Fatalf("parts = %q + %q, want %q + %q", v.label.text, v.detail.text, tt.want, tt.wantSec)
			}
		})
	}
}

// TestNewModelWithLayout_BuildsEngineForHomeWhenNoneIsPassed proves a Layout
// without an engine still renders templates, with tilde abbreviating the
// Layout's home directory; a passed engine is used as is.
func TestNewModelWithLayout_BuildsEngineForHomeWhenNoneIsPassed(t *testing.T) {
	t.Parallel()
	p := config.DefaultPresentations("")
	p.Zoxide.Label, p.Zoxide.Detail = "{{ .Path | tilde }}", ""
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Label: "x", Path: "/home/me/src/x"}}
	m := NewModelWithLayout(nil, nil, Layout{HomeDir: "/home/me", Presentation: &p})
	if got := m.buildRowView(row).label.text; got != "~/src/x" {
		t.Fatalf("label = %q, want ~/src/x", got)
	}
	engine := tmpl.New("/home")
	m = NewModelWithLayout(nil, nil, Layout{HomeDir: "/home/me", Templates: engine, Presentation: &p})
	if m.Layout().Templates != engine {
		t.Fatal("Layout().Templates is not the engine the caller passed")
	}
	if got := m.buildRowView(row).label.text; got != "~/me/src/x" {
		t.Fatalf("label = %q, want the passed engine's ~/me/src/x", got)
	}
}

// TestBuildPart_BlankRules pins how a part's blanks are normalized: every
// part is trimmed; an absent live marker takes the blank run beside it; the
// marker part also collapses every inner blank run, while the label keeps
// the blanks its data carries.
func TestBuildPart_BlankRules(t *testing.T) {
	t.Parallel()
	e := tmpl.New("")
	set := resolveIconSet("")
	for _, tc := range []struct {
		name   string
		format string
		live   liveValues
		shape  partShape
		want   string
	}{
		{"all markers absent", "{{ current }} {{ status }} {{ pin }}", liveValues{}, shapeMarker, ""},
		{"middle marker absent", "{{ current }} {{ status }} {{ pin }}", liveValues{current: true, pinned: true}, shapeMarker, "current ★"},
		{"first marker absent", "{{ current }} {{ status }} {{ pin }}", liveValues{status: "done", pinned: true}, shapeMarker, "● ★"},
		{"absent marker after text", "x {{ pin }}", liveValues{}, shapeText, "x"},
		{"absent marker between text", "a {{ pin }} b", liveValues{}, shapeText, "a b"},
		{"marker collapses data blanks", "{{ .Label }}  {{ .Meta.none }}  {{ pin }}", liveValues{pinned: true}, shapeMarker, "a b ★"},
		{"label keeps data blanks", "{{ .Label }}", liveValues{}, shapeText, "a  b"},
		{"label trims", "  {{ .Label }}  ", liveValues{}, shapeText, "a  b"},
		{"icon keeps its blanks", "X {{ pin }}", liveValues{}, shapeIcon, "X"},
		{"icon keeps a trailing blank", "X ", liveValues{}, shapeIcon, "X "},
	} {
		raw, err := e.Render(tc.format, tmpl.Data{Label: "a  b"})
		if err != nil {
			t.Fatalf("%s: Render: %v", tc.name, err)
		}
		p := buildPart(raw, &tc.live, &set, tc.shape)
		if p.text != tc.want {
			t.Errorf("%s: part = %q, want %q", tc.name, p.text, tc.want)
		}
	}
}

// TestIsPathLike pins which labels truncate from the left (keep their
// deepest element) and which keep their start.
func TestIsPathLike(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label string
		want  bool
	}{
		{"~/ops/whiterose-db", true},
		{"/srv/api", true},
		{" ~/pipelines/dark-army-images", true}, // icon-prefixed Herdr workspace name
		{"Proyectos/shep", true},
		{"ecorp-prod-vault-01", false},
		{"OC | Revisión registries neutrales Python/Go", false}, // a title with a slash
		{"", false},
	} {
		if got := isPathLike(tc.label); got != tc.want {
			t.Errorf("isPathLike(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}
