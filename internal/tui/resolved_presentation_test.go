package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/effective"
	"github.com/tranceh2/shep/internal/source"
)

// TestRowView_DrawsTheCandidatesResolvedPresentation proves a candidate's
// attached presentation draws its row — every part and its icon color —
// instead of its source's; a color the model was not prepared with keeps the
// source's icon color; Herdr tab rows keep [sources.herdr.tab].
func TestRowView_DrawsTheCandidatesResolvedPresentation(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.IconColors = []string{"peach"}
	m = m.withPresentation(nil)
	resolved := &source.Presentation{Icon: "Z ", IconColor: "peach", Label: "{{ .Label }}!", Detail: "{{ muted \"d\" }}", Marker: "m"}
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Label: "x", Path: "/x", Presentation: resolved}}
	v := m.buildRowView(row)
	if got := []string{m.partText(&v.icon), m.partText(&v.label), m.partText(&v.detail), m.partText(&v.marker)}; got[0] != "Z " || got[1] != "x!" || got[2] != "d" || got[3] != "m" {
		t.Errorf("parts = %q, want the resolved presentation's", got)
	}
	if got, want := v.iconStyle, m.formats.iconIndex["peach"]; got != want {
		t.Errorf("icon style = %d, want peach's %d", got, want)
	}

	unknown := *resolved
	unknown.IconColor = "teal"
	row.Candidate.Presentation = &unknown
	if got, want := m.buildRowView(row).iconStyle, m.formats.zoxide.icon; got != want {
		t.Errorf("unprepared icon color style = %d, want the zoxide source's %d", got, want)
	}

	tab := Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "editor", Meta: map[string]string{"tab_number": "2"}, Presentation: resolved}}
	tv := m.buildRowView(tab)
	if got := m.partText(&tv.label); got != "2 editor" {
		t.Errorf("tab label = %q, want the [sources.herdr.tab] one", got)
	}
}

// TestRowView_CustomRowIconThroughTheTemplate proves a custom source row's
// own icon is the default icon template's .Icon (never overwritten), a
// template can default it, and a configured icon still overrides it.
func TestRowView_CustomRowIconThroughTheTemplate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		icon    *string
		rowIcon string
		want    string
	}{
		{name: "the row's own icon", rowIcon: "P", want: "P"},
		{name: "a default for rows without one", icon: strPtr(`{{ .Icon | default "x" }}`), want: "x"},
		{name: "the template keeps a row's icon", icon: strPtr(`{{ .Icon | default "x" }}`), rowIcon: "P", want: "P"},
		{name: "a configured icon overrides it", icon: strPtr("C"), rowIcon: "P", want: "C"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Defaults()
			cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "prs", Presentation: config.Presentation{Icon: tc.icon}}}
			cands := []source.Candidate{{Source: "prs", Label: "PR 42", Icon: tc.rowIcon, Meta: map[string]string{"custom_source": "true"}}}
			effective.New(cfg).Attach(cands)
			m := newRenderTestModel(ThemeMocha, FocusList)
			v := m.buildRowView(Row{Kind: RowCandidate, Candidate: cands[0]})
			if got := m.partText(&v.icon); got != tc.want {
				t.Errorf("icon = %q, want %q", got, tc.want)
			}
			if cands[0].Icon != tc.rowIcon {
				t.Errorf("candidate icon = %q, want the row's own %q", cands[0].Icon, tc.rowIcon)
			}
		})
	}
}

// TestAgentsView_RowsTakeTheirPanesResolvedPresentation proves the agents
// view's rows, derived from the snapshot on every filter, take their pane's
// presentation resolved with the generation, and a pane it did not know
// draws as the agents source.
func TestAgentsView_RowsTakeTheirPanesResolvedPresentation(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.startupSnapshot = &source.Snapshot{Panes: []source.Pane{
		{ID: "p1", Agent: "pi", AgentStatus: "idle", TerminalTitle: "one"},
		{ID: "p2", Agent: "pi", AgentStatus: "idle", TerminalTitle: "two"},
	}}
	resolved := &source.Presentation{Icon: "R ", Label: "{{ .Label }}"}
	m.agentPresentations = map[string]*source.Presentation{"p1": resolved}
	rows := m.buildAgentRows()
	if len(rows) != 2 {
		t.Fatalf("agent rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		v := m.buildRowView(row)
		switch row.Candidate.Meta["pane_id"] {
		case "p1":
			if row.Candidate.Presentation != resolved || m.partText(&v.icon) != "R " || m.partText(&v.label) != "one" {
				t.Errorf("p1 drew icon %q label %q, want the resolved presentation", m.partText(&v.icon), m.partText(&v.label))
			}
		case "p2":
			if row.Candidate.Presentation != nil || m.partText(&v.label) != ansi.Strip(m.agentStatusIcon("idle"))+" two" {
				t.Errorf("p2 drew label %q, want the agents source's", m.partText(&v.label))
			}
		}
	}
}

func strPtr(s string) *string { return &s }
