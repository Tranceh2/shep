package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestResultCount_MultiSourceMatchesKeepTotal proves a query matching more
// than one source keeps the compact "shown/total" tally. The row icons
// distinguish sources; the prompt count reports only the result total.
func TestResultCount_MultiSourceMatchesKeepTotal(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
		zoxideCandidate("back-alley", "/x/back-alley"),
		projectCandidate("frontend", "/srv/frontend"),
	}
	m := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 36))
	for _, r := range "back" {
		m, _ = update(t, m, key(string(r)))
	}
	if got := m.resultCount(); got != (resultCount{shown: 2, total: 3, filtered: true}) {
		t.Errorf("resultCount = %+v, want 2 of 3 filtered", got)
	}
	prompt := promptText(m)
	if !strings.HasSuffix(prompt, " 2/3") {
		t.Errorf("prompt row = %q, want the compact \"2/3\" count", prompt)
	}
	if strings.Contains(prompt, "herdr 1") || strings.Contains(prompt, "zoxide 1") {
		t.Errorf("prompt row = %q, a source breakdown must not replace the result total", prompt)
	}
}

// TestResultCount_TextStates proves every tally state renders in the compact
// form: unfiltered total, filtered shown/total, and the loading placeholder
// while producers stream with nothing loaded yet.
func TestResultCount_TextStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		count resultCount
		want  string
	}{
		{"unfiltered", resultCount{shown: 651, total: 651}, "651"},
		{"filtered", resultCount{shown: 12, total: 651, filtered: true}, "12/651"},
		{"filtered no match", resultCount{total: 651, filtered: true}, "0/651"},
		{"loading with results", resultCount{shown: 30, total: 30, loading: true}, "30"},
		{"loading filtered", resultCount{shown: 2, total: 30, filtered: true, loading: true}, "2/30"},
		{"loading nothing yet", resultCount{loading: true}, "loading…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.count.text(); got != tc.want {
				t.Errorf("text() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResultCount_LoadingPrefixesSpinner proves the rendered tally carries
// the shared spinner frame while producers stream, and only then.
func TestResultCount_LoadingPrefixesSpinner(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(nil, nil, Layout{Theme: ThemeMocha})
	frame := m.spinner.View()
	if got := m.renderResultCount(resultCount{shown: 3, total: 3, loading: true}); got != frame+" 3" {
		t.Errorf("loading tally = %q, want the spinner frame then the count", got)
	}
	if got := m.renderResultCount(resultCount{shown: 3, total: 3}); strings.Contains(got, frame) {
		t.Errorf("settled tally = %q, must not carry the spinner", got)
	}
}

// TestResultCount_AgentsViewCountsAgents proves the agents view counts its
// own agent rows: unfiltered just the row count, filtered against every
// detected agent.
func TestResultCount_AgentsViewCountsAgents(t *testing.T) {
	t.Parallel()
	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "api"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1"}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "working", TerminalTitle: "fix the parser"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t1", Agent: "codex", AgentStatus: "idle", TerminalTitle: "write docs"},
		},
	}
	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(snapshot), Layout{Theme: ThemeMocha, InitialTab: "agents"})
	m, _ = update(t, m, sizeMsg(120, 36))
	if got := m.resultCount().text(); got != "2" {
		t.Errorf("unfiltered agents tally = %q, want \"2\"", got)
	}
	for _, r := range "parser" {
		m, _ = update(t, m, key(string(r)))
	}
	if got := m.resultCount().text(); got != "1/2" {
		t.Errorf("filtered agents tally = %q, want \"1/2\"", got)
	}
}
