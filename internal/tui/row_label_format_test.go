package tui

import (
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

func TestRowPrimaryText_DefaultLabelFormatsPreserveCurrentRendering(t *testing.T) {
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
			want: "◆ backend · /srv/backend",
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
			want: "◆ /srv/cache",
		},
		{
			name: "project candidate",
			row: Row{Kind: RowCandidate, Candidate: source.Candidate{
				Source: config.SourceProjects, Label: "shep", Path: "/srv/shep", Icon: "◆",
			}},
			want: "◆ /srv/shep",
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
