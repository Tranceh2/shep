package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
)

func TestRowPrimaryText_ConfiguredLabelFormats(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{
		Herdr:      "workspace={{.Label}} path={{.Path}}",
		Workspaces: "entry={{.Label}} path={{.Path}}",
		Zoxide:     "history={{.Label}} path={{.Path}} icon={{.Icon}}",
		Projects:   "project={{.Label}} path={{.Path}}",
		Tab:        "tab={{.Label}} number={{.TabNumber}} path={{.Path}}",
		Pane:       "pane={{.Label}} status={{.AgentStatus}} path={{.Path}}",
	}

	tab := Row{
		Kind:   RowTab,
		Depth:  1,
		IsLast: true,
		Candidate: source.Candidate{
			Label: "3", Path: "/srv/tab", Meta: map[string]string{"tab_number": "3"},
		},
	}
	pane := Row{
		Kind:   RowPane,
		Depth:  2,
		IsLast: true,
		Candidate: source.Candidate{
			Label: "editor", Path: "/srv/pane", Meta: map[string]string{"agent_status": "idle"},
		},
	}

	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{
			name: "Herdr candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceHerdr, Label: "backend", Path: "/srv/backend", Icon: "◆",
			}},
			want: "◆ workspace=backend path=/srv/backend",
		},
		{
			name: "workspace candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceWorkspaces, Label: "api", Path: "/srv/api", Icon: "◆",
			}},
			want: "◆ entry=api path=/srv/api",
		},
		{
			name: "zoxide candidate template sees the shared icon field",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceZoxide, Label: "cache", Path: "/srv/cache", Icon: "◆",
			}},
			want: "◆ history=cache path=/srv/cache icon=◆",
		},
		{
			name: "project candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceProjects, Label: "shep", Path: "/srv/shep", Icon: "◆",
			}},
			want: "◆ project=shep path=/srv/shep",
		},
		{
			name: "tab uses deduplicated label and raw number",
			row:  tab,
			want: wantRowPrimary(m, tab, m.icons().TabIcon+" ", "tab=3 number=3 path=/srv/tab"),
		},
		{
			name: "pane uses pane format and agent status",
			row:  pane,
			want: wantRowPrimary(m, pane, m.agentStatusIcon("idle")+" ", "pane=editor status=idle path=/srv/pane"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := m.rowPrimaryText(tt.row)
			if got != tt.want {
				t.Errorf("rowPrimaryText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentPresentation_IconAndPrefixInTabs(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	icon := "X "
	for _, tt := range []struct {
		name string
		kind RowKind
	}{
		{"agents tab", RowPane},
		{"group or source tab", RowCandidate},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := Row{Kind: tt.kind, Candidate: source.Candidate{
				Source: config.SourceAgents, Label: "security scan", Icon: icon,
				Meta: map[string]string{"agent_status": "blocked"},
			}}
			primary, prefixRunes := m.rowPrimaryText(row)
			wantPrefix := icon + " " + m.agentStatusIcon("blocked") + " "
			if primary != wantPrefix+"security scan" || prefixRunes != len([]rune(wantPrefix)) {
				t.Fatalf("primary = %q, prefixRunes = %d; want %q, %d", primary, prefixRunes, wantPrefix+"security scan", len([]rune(wantPrefix)))
			}
			row.Candidate.Icon = ""
			primary, prefixRunes = m.rowPrimaryText(row)
			wantPrefix = m.agentStatusIcon("blocked") + " "
			if primary != wantPrefix+"security scan" || prefixRunes != len([]rune(wantPrefix)) {
				t.Fatalf("empty icon: primary = %q, prefixRunes = %d", primary, prefixRunes)
			}
		})
	}
}

func TestAgentPresentation_AgentsTabUsesConfiguredIcon(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.snapshotIcons = map[string]string{config.SourceAgents: "X "}
	m.startupSnapshot = &source.Snapshot{Panes: []source.Pane{{ID: "p1", Agent: "pi", AgentStatus: "idle", TerminalTitle: "title"}}}
	rows := m.buildAgentRows()
	if len(rows) != 1 {
		t.Fatalf("agent rows = %d, want 1", len(rows))
	}
	got := renderRowLineText(m.renderRowLine(rows[0], false, 40))
	if !strings.Contains(got, "X  "+m.agentStatusIcon("idle")+" title") {
		t.Errorf("agents tab rendered %q, want icon before status and title", got)
	}
}

func TestRowLabelFormat_MetaForAllRowKinds(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{
		Agents: "{{.Label}} {{.Meta.workspace_label}}/{{.Meta.agent}}/{{.Meta.absent}}",
		Tab:    "{{.Label}} {{.Meta.tab_label}}/{{.Meta.absent}}",
		Pane:   "{{.Label}} {{.Meta.agent_status}}/{{.Meta.absent}}",
	}
	for _, tt := range []struct {
		row  Row
		want string
	}{
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceAgents, Label: "task", Meta: map[string]string{"workspace_label": "home", "agent": "pi"}}}, "task home/pi/"},
		{Row{Kind: RowTab, Candidate: source.Candidate{Label: "tab", Meta: map[string]string{"tab_label": "ops"}}}, "tab ops/"},
		{Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "task", Meta: map[string]string{"agent_status": "idle"}}}, "task idle/"},
	} {
		if got := m.renderRowLabel(tt.row); got != tt.want {
			t.Errorf("kind %d label = %q, want %q", tt.row.Kind, got, tt.want)
		}
	}
	m.layout.LabelFormats.Agents = "{{.Label}}"
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceAgents, Label: "task", Meta: map[string]string{"agent": "pi"}}}
	if got := m.renderRowLabel(row); got != "task" {
		t.Errorf("existing template = %q, want task", got)
	}
}

func TestRowPrimaryText_SourceIconIsRendered(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: config.SourceProjects, Label: "shep", Icon: "\ue702 ", Path: "/srv/shep",
	}}
	got := renderRowLineText(m.renderRowLine(row, true, 40))
	if !strings.Contains(got, "❯ \ue702") || !strings.Contains(got, "shep") {
		t.Fatalf("rendered row = %q, want source icon and label", got)
	}
}

func TestRowPrimaryText_DefaultLabelFormatsAreLabelFirst(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	tab := Row{
		Kind:   RowTab,
		Depth:  1,
		IsLast: true,
		Candidate: source.Candidate{
			Label: "3", Path: "/srv/tab", Meta: map[string]string{"tab_number": "3"},
		},
	}
	pane := Row{
		Kind:   RowPane,
		Depth:  2,
		IsLast: true,
		Candidate: source.Candidate{
			Label: "editor", Path: "/srv/pane",
		},
	}

	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{
			name: "Herdr candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceHerdr, Label: "backend", Path: "/srv/backend", Icon: "◆",
			}},
			want: "◆ backend",
		},
		{
			name: "workspace candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceWorkspaces, Label: "api", Path: "/srv/api", Icon: "◆",
			}},
			want: "◆ api",
		},
		{
			name: "zoxide candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceZoxide, Label: "cache", Path: "/srv/cache", Icon: "◆",
			}},
			want: "◆ cache",
		},
		{
			name: "project candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceProjects, Label: "shep", Path: "/srv/shep", Icon: "◆",
			}},
			want: "◆ shep",
		},
		{
			name: "tab number matching label renders once",
			row:  tab,
			want: wantRowPrimary(m, tab, m.icons().TabIcon+" ", "3"),
		},
		{
			name: "pane",
			row:  pane,
			want: wantRowPrimary(m, pane, "", "editor"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := m.rowPrimaryText(tt.row)
			if got != tt.want {
				t.Errorf("rowPrimaryText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRowPrimaryText_CustomSourceUsesConfiguredFormatKeyedByName proves an
// custom source row's label_format resolves by Candidate.Source (the declared
// [[sources.custom]].name), mirroring the fixed built-in fields above but
// looked up in the open-ended CustomSources map instead.
func TestRowPrimaryText_DefaultDirectoryLabelsUseTildeAndFallback(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.homeDir = "/home/dev"
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
		{name: "fallback path under home is abbreviated", source: config.SourceZoxide, path: "/home/dev/Proyectos/shep", want: "shep", wantSec: "~/Proyectos"},
		{name: "home itself", source: config.SourceZoxide, path: "/home/dev", want: "~"},
		{name: "a sibling of home is not abbreviated", source: config.SourceZoxide, path: "/home/devops/x", want: "x", wantSec: "/home/devops"},
		{name: "directly under home", source: config.SourceZoxide, path: "/home/dev/notes", want: "notes", wantSec: "~"},
		{name: "directly under root", source: config.SourceZoxide, path: "/tmp", want: "tmp", wantSec: "/"},
		{name: "one trailing slash is ignored", source: config.SourceZoxide, label: "~/src/", path: "/x", want: "src", wantSec: "~"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: tt.source, Label: tt.label, Path: tt.path}}
			got, secondary := m.rowDisplayText(row)
			if got != tt.want || secondary != tt.wantSec {
				t.Fatalf("rowDisplayText() = %q + %q, want %q + %q", got, secondary, tt.want, tt.wantSec)
			}
			if strings.Contains(got+secondary, "/workspace/") || strings.Contains(got+secondary, "/home/dev/") {
				t.Fatalf("row leaked an absolute path: %q + %q", got, secondary)
			}
		})
	}
}

func TestRowPrimaryText_CustomSourceUsesConfiguredFormatKeyedByName(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{CustomSources: map[string]string{"prs": "PR {{.Label}}"}}
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: "prs", Label: "42", Icon: "P",
	}}
	got, _ := m.rowPrimaryText(row)
	if want := "P PR 42"; got != want {
		t.Errorf("rowPrimaryText() = %q, want %q", got, want)
	}
}

// TestRowPrimaryText_CustomSourceDefaultFormatIsLabelOnly proves the
// label_format config.Load defaults every declared custom source to
// ("{{.Label}}", see config.normalizeCustomSources) renders label-only, since
// a command-only custom source row (Meta["command"] set, no Path) would
// otherwise render blank under the historical path-only fallback.
func TestRowPrimaryText_CustomSourceDefaultFormatIsLabelOnly(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{CustomSources: map[string]string{"prs": "{{.Label}}"}}
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: "prs", Label: "PR 42", Icon: "P", Meta: map[string]string{"command": "gh pr view 42"},
	}}
	got, _ := m.rowPrimaryText(row)
	if want := "P PR 42"; got != want {
		t.Errorf("rowPrimaryText() = %q, want %q", got, want)
	}
}

// TestRowPrimaryText_UndeclaredSourceKeepsPathOnlyFallback proves a source
// name absent from Layout.LabelFormats.CustomSources (not a declared
// [[sources.custom]] entry — e.g. a direct --path candidate) keeps the
// historical path-only default (shown filename first), unaffected by the
// custom sources feature.
func TestRowPrimaryText_UndeclaredSourceKeepsPathOnlyFallback(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: "path", Label: "label", Path: "/srv/backend", Icon: "◆",
	}}
	if got, secondary := m.rowDisplayText(row); got != "◆ backend" || secondary != "/srv" {
		t.Errorf("rowDisplayText() = %q + %q, want the path, filename first", got, secondary)
	}
}

func TestRowPrimaryText_InvalidRuntimeFormatFallsBackToPath(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{Zoxide: "{{.Unknown}}"}
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: config.SourceZoxide, Label: "cache", Path: "/srv/cache", Icon: "◆",
	}}

	if got, secondary := m.rowDisplayText(row); got != "◆ cache" || secondary != "/srv" {
		t.Errorf("rowDisplayText() = %q + %q, want the safe path fallback, filename first", got, secondary)
	}
}

// TestRowPrimaryText_EmptyRenderedLabelFallsBackToPath covers a candidate
// built directly in Go (bypassing config.Load's mandatory-name validation)
// whose Label is empty. A bare {{.Label}} format renders successfully to "",
// which must fall back to the path the same way a render error does, instead
// of showing a silently blank row.
func TestRowPrimaryText_EmptyRenderedLabelFallsBackToPath(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{Workspaces: "{{.Label}}"}
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: config.SourceWorkspaces, Label: "", Path: "/srv/unnamed", Icon: "◆",
	}}

	if got, secondary := m.rowDisplayText(row); got != "◆ unnamed" || secondary != "/srv" {
		t.Errorf("rowDisplayText() = %q + %q, want the safe path fallback, filename first", got, secondary)
	}
}

// TestRowPrimaryText_SessionsUsesConfiguredFormatAndStatusSuffixes verifies
// sessions follow source icon/label conventions while rendering stable state
// metadata outside the template body.
func TestRowPrimaryText_SourcePaneFormatIsPresentationOnly(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{Pane: "display={{.Label}}"}
	row := Row{Kind: RowPane, Depth: 2, IsLast: true, Candidate: source.Candidate{
		Label: "persistent", Path: "/srv/pane", Meta: map[string]string{"pane_id": "w1:p1"},
	}}

	got, _ := m.rowPrimaryText(row)
	if want := wantRowPrimary(m, row, "", "display=persistent"); got != want {
		t.Fatalf("rowPrimaryText() = %q, want %q", got, want)
	}
	if got := row.Candidate.Meta["pane_id"]; got != "w1:p1" {
		t.Errorf("pane identity changed while rendering: %q", got)
	}
}

func TestRowPrimaryText_SessionsUsesConfiguredFormatAndStatusSuffixes(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{Sessions: "session={{.Label}}"}
	for _, tt := range []struct {
		name          string
		row           Row
		want, wantAcc string
	}{
		{
			name: "running default",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceSessions, Label: "alpha", Icon: "S", Meta: map[string]string{"running": "true", "default": "true"},
			}},
			want: "S session=alpha", wantAcc: "running · default",
		},
		{
			name: "stopped non-default",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceSessions, Label: "beta", Icon: "S", Meta: map[string]string{"running": "false", "default": "false"},
			}},
			want: "S session=beta", wantAcc: "stopped",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := m.rowPrimaryText(tt.row)
			if got != tt.want {
				t.Errorf("rowPrimaryText() = %q, want %q", got, tt.want)
			}
			if acc := m.rowAccessoryText(tt.row); acc != tt.wantAcc {
				t.Errorf("accessories = %q, want the session state %q outside the template", acc, tt.wantAcc)
			}
		})
	}
}

// TestRenderRowLabel_SharedTemplateDataPerRowKind proves row labels render
// with the shared template data: every candidate gets its source's Kind and
// the Herdr rows the picker synthesizes get tab, pane or agent, plus the tab,
// pane and agent fields.
func TestRenderRowLabel_SharedTemplateDataPerRowKind(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{
		Herdr:    "{{.Kind}}",
		Zoxide:   "{{.Kind}} {{ .Label | name }} [{{ .Label | parent }}]",
		Projects: "{{.Kind}} {{.RepoName}}@{{.Branch}} {{.Head}}",
		Tab:      "{{.Kind}} {{.Label}} n={{.TabNumber}} l={{.TabLabel}} ws={{.Workspace}}",
		Pane:     "{{.Kind}} {{.Agent}}/{{.AgentStatus}} {{.TabLabel}}",
		Agents:   "{{.Kind}} {{.Agent}} in {{ .Workspace | trimIcon | name }}",
	}
	tabMeta := map[string]string{"tab_number": "2", "tab_label": "editor", "workspace_label": "~/srv/api"}
	paneMeta := map[string]string{"agent": "claude", "agent_status": "idle", "tab_label": "editor"}
	agentMeta := map[string]string{"agent": "pi", "agent_status": "working", "workspace_label": "\U000f0cc6 ~/srv/api", "kind": "agent"}
	for _, tt := range []struct {
		name string
		row  Row
		want string
	}{
		{"herdr workspace", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Label: "api", Path: "/srv/api"}}, "workspace"},
		{"zoxide folder", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Label: "~/srv/api", Path: "/home/u/srv/api"}}, "folder api [~/srv]"},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Label: "api (x)", Path: "/t/x", Meta: map[string]string{"is_worktree": "true", "repo": "api", "branch": "x", "head": "0123456789"}}}, "worktree api@x 0123456"},
		{"tree tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "editor", Path: "/srv/api", Meta: tabMeta}}, "tab 2 editor n=2 l=editor ws=~/srv/api"},
		{"tree pane", Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "nvim", Path: "/srv/api", Meta: paneMeta}}, "pane claude/idle editor"},
		{"flat agent row", Row{Kind: RowPane, Depth: 0, Candidate: source.Candidate{Source: config.SourceAgents, Label: "fix", Path: "/srv/api", Meta: agentMeta}}, "agent pi in api"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.renderRowLabel(tt.row); got != tt.want {
				t.Errorf("renderRowLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNewModelWithLayout_BuildsEngineForHomeWhenNoneIsPassed proves a Layout
// without an engine still renders templates, with tilde abbreviating the
// Layout's home directory; a passed engine is used as is.
func TestNewModelWithLayout_BuildsEngineForHomeWhenNoneIsPassed(t *testing.T) {
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Label: "x", Path: "/home/me/src/x"}}
	m := NewModelWithLayout(nil, nil, Layout{HomeDir: "/home/me", LabelFormats: LabelFormats{Zoxide: "{{ .Path | tilde }}"}})
	if got := m.renderRowLabel(row); got != "~/src/x" {
		t.Fatalf("renderRowLabel() = %q, want ~/src/x", got)
	}
	engine := tmpl.New("/home")
	m = NewModelWithLayout(nil, nil, Layout{HomeDir: "/home/me", Templates: engine, LabelFormats: LabelFormats{Zoxide: "{{ .Path | tilde }}"}})
	if m.Layout().Templates != engine {
		t.Fatal("Layout().Templates is not the engine the caller passed")
	}
	if got := m.renderRowLabel(row); got != "~/me/src/x" {
		t.Fatalf("renderRowLabel() = %q, want the passed engine's ~/me/src/x", got)
	}
}
