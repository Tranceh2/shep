package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
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
			name: "zoxide candidate keeps icon outside template body",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceZoxide, Label: "cache", Path: "/srv/cache", Icon: "◆",
			}},
			want: "◆ history=cache path=/srv/cache icon=",
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
			want: wantRowPrimary(m, tab, m.icons().TabIcon+" ", "3 · /srv/tab"),
		},
		{
			name: "pane",
			row:  pane,
			want: wantRowPrimary(m, pane, "", "editor · /srv/pane"),
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
	for _, tt := range []struct {
		name   string
		source string
		label  string
		path   string
		want   string
	}{
		{name: "zoxide relative label", source: config.SourceZoxide, label: "~/Proyectos/shep", path: "/workspace/Proyectos/shep", want: "~/Proyectos/shep"},
		{name: "projects relative label", source: config.SourceProjects, label: "~/Proyectos/shep", path: "/workspace/Proyectos/shep", want: "~/Proyectos/shep"},
		{name: "zoxide empty label fallback", source: config.SourceZoxide, path: "/opt/shep", want: "/opt/shep"},
		{name: "projects empty label fallback", source: config.SourceProjects, path: "/opt/shep", want: "/opt/shep"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: tt.source, Label: tt.label, Path: tt.path}}
			got, _ := m.rowPrimaryText(row)
			if got != tt.want {
				t.Fatalf("rowPrimaryText() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "/workspace/") {
				t.Fatalf("row leaked absolute path: %q", got)
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
// historical path-only default, unaffected by the custom sources feature.
func TestRowPrimaryText_UndeclaredSourceKeepsPathOnlyFallback(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: "path", Label: "backend", Path: "/srv/backend", Icon: "◆",
	}}
	got, _ := m.rowPrimaryText(row)
	if want := "◆ /srv/backend"; got != want {
		t.Errorf("rowPrimaryText() = %q, want %q", got, want)
	}
}

func TestRowPrimaryText_InvalidRuntimeFormatFallsBackToPath(t *testing.T) {
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{Zoxide: "{{.Unknown}}"}
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{
		Source: config.SourceZoxide, Label: "cache", Path: "/srv/cache", Icon: "◆",
	}}

	got, _ := m.rowPrimaryText(row)
	if want := "◆ /srv/cache"; got != want {
		t.Errorf("rowPrimaryText() = %q, want safe fallback %q", got, want)
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

	got, _ := m.rowPrimaryText(row)
	if want := "◆ /srv/unnamed"; got != want {
		t.Errorf("rowPrimaryText() = %q, want safe fallback %q", got, want)
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
		name string
		row  Row
		want string
	}{
		{
			name: "running default",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceSessions, Label: "alpha", Icon: "S", Meta: map[string]string{"running": "true", "default": "true"},
			}},
			want: "S session=alpha (running, default)",
		},
		{
			name: "stopped non-default",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceSessions, Label: "beta", Icon: "S", Meta: map[string]string{"running": "false", "default": "false"},
			}},
			want: "S session=beta (stopped)",
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
