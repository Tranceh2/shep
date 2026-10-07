package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
)

func TestConfiguredTabs_FilterAndNavigation(t *testing.T) {
	candidates := []source.Candidate{
		{Source: config.SourceProjects, Path: "/p", Label: "project"},
		{Source: "review", Path: "/r", Label: "review item"},
	}
	m := NewModelWithLayout(candidates, nil, Layout{Tabs: []TabDefinition{
		{ID: "review", Kind: TabCustomSource},
		{ID: "projects", Kind: TabSource},
		{ID: "all", Kind: TabAll},
	}})
	if got := m.ActiveTab(); got != "review" {
		t.Fatalf("initial tab = %q", got)
	}
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "review item" {
		t.Fatalf("custom source rows = %+v", m.rows)
	}
	if !hasHint(m.footerHints(), keyBindingTab.footerChord, "projects") {
		t.Errorf("footer = %q, want the next tab named in the tab hint", footerText(m))
	}
	next, _ := m.cycleTabForward()
	m = next.(Model)
	if m.ActiveTab() != "projects" || len(m.rows) != 1 || m.rows[0].Candidate.Label != "project" {
		t.Fatalf("projects tab = %q, rows = %+v", m.ActiveTab(), m.rows)
	}
	next, _ = m.cycleTabBackward()
	m = next.(Model)
	if m.ActiveTab() != "review" {
		t.Errorf("backward tab = %q", m.ActiveTab())
	}
}

func TestConfiguredTabs_SingleAndHiddenAgents(t *testing.T) {
	m := NewModelWithLayout(nil, nil, Layout{Tabs: []TabDefinition{{ID: "projects", Kind: TabSource}}, InitialTab: "agents"})
	if m.ActiveTab() != "agents" {
		t.Fatalf("explicit agents tab = %q", m.ActiveTab())
	}
	next, _ := m.cycleTabForward()
	m = next.(Model)
	if m.ActiveTab() != "projects" {
		t.Fatalf("forward from hidden agents = %q", m.ActiveTab())
	}
	next, _ = m.cycleTabForward()
	m = next.(Model)
	if m.ActiveTab() != "projects" {
		t.Errorf("single tab wrap = %q", m.ActiveTab())
	}
}

func TestConfiguredTabs_InitialGroupSingleFlightWithProducer(t *testing.T) {
	calls := 0
	m := NewModelWithProducers([]SourceProducer{func(context.Context) SourceResultMsg {
		return SourceResultMsg{Source: config.SourceProjects}
	}}, "", nil, context.Background(), Layout{Tabs: []TabDefinition{{
		ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceProjects},
		Load: func(context.Context, *source.Snapshot) ([]source.Candidate, error) {
			calls++
			return []source.Candidate{{Source: config.SourceProjects, Path: "/team", Label: "team"}}, nil
		},
	}}})

	// Execute the initial group command, but hold its result until after the
	// unrelated producer has completed and the model has processed its result.
	initial := m.Init()
	if initial == nil {
		t.Fatal("expected startup commands")
	}
	batch, ok := initial().(tea.BatchMsg)
	if !ok {
		t.Fatal("expected startup batch")
	}
	var producerMsg SourceResultMsg
	producerFound := false
	var pending []groupResultMsg
	for _, child := range batch {
		switch value := child().(type) {
		case SourceResultMsg:
			producerFound = true
			producerMsg = value
		case groupResultMsg:
			pending = append(pending, value)
		}
	}
	if !producerFound || producerMsg.Source != config.SourceProjects {
		t.Fatalf("startup producer result = %+v, found = %v", producerMsg, producerFound)
	}
	if calls != 1 || len(pending) != 1 {
		t.Fatalf("initial collection calls = %d, results = %d; want one in-flight collection", calls, len(pending))
	}
	m, cmd := update(t, m, producerMsg)
	if cmd != nil {
		var deliver func(tea.Msg)
		deliver = func(msg tea.Msg) {
			switch value := msg.(type) {
			case tea.BatchMsg:
				for _, child := range value {
					deliver(child())
				}
			case groupResultMsg:
				pending = append(pending, value)
			}
		}
		deliver(cmd())
	}
	if calls != 1 || len(pending) != 1 {
		t.Fatalf("before first group result: calls = %d, results = %d; want one", calls, len(pending))
	}
	m, _ = update(t, m, pending[0])
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "team" {
		t.Fatalf("initial group rows = %+v", m.rows)
	}
}

func TestExplicitHiddenViewFiltersQueryWithoutJoiningAll(t *testing.T) {
	layout := Layout{SourceOrder: []string{config.SourceWorkspaces}, Tabs: []TabDefinition{{ID: "all", Kind: TabAll}, {ID: "projects", Kind: TabSource}}, InitialTab: "projects"}
	m := NewModelWithProducers(nil, "needle", nil, context.Background(), layout)
	if m.ActiveTab() != "projects" {
		t.Fatalf("initial tab = %q", m.ActiveTab())
	}
	m, _ = m.handleSourceResult(SourceResultMsg{Source: config.SourceProjects, Candidates: []source.Candidate{
		{Source: config.SourceProjects, Label: "needle", Path: "/needle"},
		{Source: config.SourceProjects, Label: "other", Path: "/other"},
	}})
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "needle" {
		t.Fatalf("filtered rows = %+v", m.rows)
	}
	m.activeTab = "all"
	m.applyFilter()
	if len(m.rows) != 0 {
		t.Fatalf("hidden view leaked into all: %+v", m.rows)
	}
}

func TestConfiguredTabs_QueryTabOnlyProviderPreservesAll(t *testing.T) {
	calls := 0
	layout := Layout{SourceOrder: []string{config.SourceWorkspaces}, Tabs: []TabDefinition{
		{ID: "all", Kind: TabAll},
		{ID: "review", Kind: TabCustomSource, Load: func(context.Context, *source.Snapshot) ([]source.Candidate, error) {
			calls++
			return []source.Candidate{{Source: "review", Path: "/review", Label: "team review"}}, nil
		}},
	}}
	m := newModelWithLayout([]source.Candidate{
		{Source: config.SourceWorkspaces, Path: "/one", Label: "team one"},
		{Source: config.SourceWorkspaces, Path: "/two", Label: "team two"},
	}, nil, context.Background(), layout)
	m.query = "team"
	m.applyFilter()
	if len(m.rows) != 2 || calls != 0 {
		t.Fatalf("all rows = %+v, lazy calls = %d", m.rows, calls)
	}
	next, cmd := m.cycleTabForward()
	m = next.(Model)
	if cmd == nil || calls != 0 {
		t.Fatalf("provider not lazy: calls = %d, cmd = %v", calls, cmd)
	}
	var deliver func(tea.Msg)
	deliver = func(msg tea.Msg) {
		switch value := msg.(type) {
		case tea.BatchMsg:
			for _, child := range value {
				deliver(child())
			}
		case groupResultMsg:
			m, _ = update(t, m, value)
		}
	}
	deliver(cmd())
	if calls != 1 || len(m.rows) != 1 || m.rows[0].Candidate.Source != "review" {
		t.Fatalf("review rows = %+v, calls = %d", m.rows, calls)
	}
	next, _ = m.cycleTabBackward()
	m = next.(Model)
	if len(m.rows) != 2 || m.rows[0].Candidate.Source != config.SourceWorkspaces {
		t.Fatalf("all gained tab-only candidates: %+v", m.rows)
	}
}

func TestConfiguredTabs_GroupLoadsLazilyOnce(t *testing.T) {
	calls := 0
	m := NewModelWithLayout([]source.Candidate{{Source: config.SourceProjects, Path: "/outside", Label: "outside"}}, nil, Layout{Tabs: []TabDefinition{
		{ID: "all", Kind: TabAll},
		{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceProjects}, Load: func(_ context.Context, _ *source.Snapshot) ([]source.Candidate, error) {
			calls++
			return []source.Candidate{{Source: config.SourceProjects, Path: "/team", Label: "team project"}}, nil
		}},
	}})
	if calls != 0 {
		t.Fatal("group loaded before activation")
	}
	next, cmd := m.cycleTabForward()
	m = next.(Model)
	if cmd == nil || calls != 0 {
		t.Fatalf("group load should be scheduled, calls = %d", calls)
	}
	// Bubble Tea batches the load command with the filter and preview commands.
	var deliver func(tea.Msg)
	deliver = func(msg tea.Msg) {
		switch value := msg.(type) {
		case tea.BatchMsg:
			for _, child := range value {
				deliver(child())
			}
		case groupResultMsg:
			m, _ = update(t, m, value)
		}
	}
	deliver(cmd())
	if calls != 1 || len(m.rows) != 1 || m.rows[0].Candidate.Label != "team project" {
		t.Fatalf("group rows = %+v, loads = %d", m.rows, calls)
	}
	next, _ = m.cycleTabBackward()
	m = next.(Model)
	next, cmd = m.cycleTabForward()
	m = next.(Model)
	if calls != 1 || m.groupLoading["team"] {
		t.Errorf("group reloaded: %d", calls)
	}
	_ = cmd
}

func TestConfiguredTabs_EmptyStates(t *testing.T) {
	cases := []struct {
		name string
		kind TabKind
		want string
	}{
		{name: "custom source", kind: TabCustomSource, want: "No custom source candidates available"},
		{name: "group", kind: TabGroup, want: "No group candidates available"},
		{name: "source", kind: TabSource, want: "No source candidates available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithLayout(nil, nil, Layout{Tabs: []TabDefinition{{ID: tc.name, Kind: tc.kind}}})
			if got := m.emptyStateLines(); len(got) == 0 || got[0] != tc.want {
				t.Fatalf("empty state = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestConfiguredTabs_GroupReusesLoadedSnapshot(t *testing.T) {
	snapshot := &source.Snapshot{Workspaces: []source.Workspace{{ID: "w1", CWD: "/team"}}}
	var got *source.Snapshot
	m := NewModelWithProducers([]SourceProducer{func(context.Context) SourceResultMsg {
		return SourceResultMsg{Source: config.SourceHerdr, Snapshot: snapshot}
	}}, "", nil, context.Background(), Layout{Tabs: []TabDefinition{{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceHerdr}, Load: func(_ context.Context, s *source.Snapshot) ([]source.Candidate, error) {
		got = s
		return nil, nil
	}}}})
	m, cmd := update(t, m, SourceResultMsg{Source: config.SourceHerdr, Snapshot: snapshot, producerID: 0})
	if cmd == nil {
		t.Fatal("group collection not scheduled after snapshot")
	}
	var deliver func(tea.Msg)
	deliver = func(msg tea.Msg) {
		switch value := msg.(type) {
		case tea.BatchMsg:
			for _, child := range value {
				deliver(child())
			}
		case groupResultMsg:
			m, _ = update(t, m, value)
		}
	}
	deliver(cmd())
	if got == nil || len(got.Workspaces) != 1 {
		t.Fatalf("group snapshot = %+v", got)
	}
}

func TestConfiguredTabs_GroupRefreshRecollectsCurrentGeneration(t *testing.T) {
	calls := 0
	m := NewModelWithLayout(nil, nil, Layout{Tabs: []TabDefinition{
		{ID: "all", Kind: TabAll},
		{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceHerdr}, Load: func(_ context.Context, snap *source.Snapshot) ([]source.Candidate, error) {
			calls++
			return []source.Candidate{{Source: config.SourceHerdr, Label: snap.Workspaces[0].Label, Meta: map[string]string{"workspace_id": snap.Workspaces[0].ID}}}, nil
		}},
	}})
	m, _ = update(t, m, SourceResultMsg{Source: config.SourceHerdr, Snapshot: &source.Snapshot{Workspaces: []source.Workspace{{ID: "old", Label: "old"}}}})
	next, cmd := m.cycleTabForward()
	m = next.(Model)
	var deliver func(tea.Msg)
	deliver = func(msg tea.Msg) {
		switch value := msg.(type) {
		case tea.BatchMsg:
			for _, child := range value {
				deliver(child())
			}
		case groupResultMsg:
			m, _ = update(t, m, value)
		}
	}
	deliver(cmd())
	if calls != 1 || len(m.rows) != 1 || m.rows[0].Candidate.Label != "old" {
		t.Fatalf("initial group rows = %+v, loads = %d", m.rows, calls)
	}
	m, cmd = update(t, m, resolvedGeneration(m.snapshotSeq, source.Snapshot{Workspaces: []source.Workspace{{ID: "new", Label: "new"}}}, nil))
	if cmd == nil || !m.groupLoading["team"] {
		t.Fatal("group not scheduled after snapshot refresh")
	}
	deliver(cmd())
	if calls != 2 || len(m.rows) != 1 || m.rows[0].Candidate.Label != "new" || m.cursor >= len(m.rows) {
		t.Fatalf("refreshed rows = %+v, cursor = %d, loads = %d", m.rows, m.cursor, calls)
	}
	next, _ = m.cycleTabBackward()
	m = next.(Model)
	next, _ = m.cycleTabForward()
	m = next.(Model)
	if calls != 2 || m.groupLoading["team"] {
		t.Fatalf("unchanged generation loaded again: %d", calls)
	}
}

func TestConfiguredTabs_GroupIgnoresOldInflightGeneration(t *testing.T) {
	m := NewModelWithLayout(nil, nil, Layout{Tabs: []TabDefinition{{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceHerdr}, Load: func(_ context.Context, snap *source.Snapshot) ([]source.Candidate, error) {
		return []source.Candidate{{Source: config.SourceHerdr, Label: snap.Workspaces[0].Label}}, nil
	}}}})
	m.startupSnapshot = &source.Snapshot{Workspaces: []source.Workspace{{ID: "old", Label: "old"}}}
	old := m.maybeLoadGroup()
	m, cmd := update(t, m, resolvedGeneration(m.snapshotSeq, source.Snapshot{Workspaces: []source.Workspace{{ID: "new", Label: "new"}}}, nil))
	if old == nil || cmd == nil {
		t.Fatal("expected old and new collection commands")
	}
	m, _ = update(t, m, old())
	if len(m.rows) != 0 {
		t.Fatalf("old snapshot result became visible: %+v", m.rows)
	}
	var deliver func(tea.Msg)
	deliver = func(msg tea.Msg) {
		switch value := msg.(type) {
		case tea.BatchMsg:
			for _, child := range value {
				deliver(child())
			}
		case groupResultMsg:
			m, _ = update(t, m, value)
		}
	}
	deliver(cmd())
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "new" {
		t.Fatalf("current group rows = %+v", m.rows)
	}
}

func TestConfiguredTabs_GroupFailureDoesNotRetrySnapshot(t *testing.T) {
	calls := 0
	m := NewModelWithProducers([]SourceProducer{func(context.Context) SourceResultMsg { return SourceResultMsg{Source: config.SourceHerdr} }}, "", nil, context.Background(), Layout{Tabs: []TabDefinition{{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceHerdr}, Load: func(_ context.Context, _ *source.Snapshot) ([]source.Candidate, error) {
		calls++
		return nil, nil
	}}}})
	m, cmd := update(t, m, SourceResultMsg{Source: config.SourceHerdr, Err: context.DeadlineExceeded, producerID: 0})
	if cmd != nil {
		// A batch can include other effects; group loading must stay disabled.
		_ = cmd
	}
	if m.groupLoading["team"] || calls != 0 {
		t.Fatalf("group started after snapshot failure: loading=%v calls=%d", m.groupLoading["team"], calls)
	}
	if lines := m.emptyStateLines(); len(lines) < 2 || !strings.Contains(lines[1], "deadline") {
		t.Fatalf("group failure state = %v", lines)
	}
}

func TestConfiguredTabs_GroupOrderAndSelection(t *testing.T) {
	m := NewModelWithLayout(nil, nil, Layout{Tabs: []TabDefinition{{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceProjects, config.SourceZoxide}}}})
	m, _ = update(t, m, groupResultMsg{id: "team", candidates: []source.Candidate{
		{Source: config.SourceZoxide, Path: "/z", Label: "zoxide"},
		{Source: config.SourceProjects, Path: "/p", Label: "project"},
	}})
	if len(m.rows) != 2 || m.rows[0].Candidate.Source != config.SourceProjects {
		t.Fatalf("group source order = %+v", m.rows)
	}
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	selected, ok := m.Selected()
	if !ok || selected.Path != "/p" {
		t.Fatalf("selected = %+v, ok = %v", selected, ok)
	}
}

func TestConfiguredTabs_SourceUsesUndeduplicatedProviderRows(t *testing.T) {
	m := NewModelWithProducers(nil, "", nil, context.Background(), Layout{SourceOrder: []string{config.SourceProjects}, Tabs: []TabDefinition{
		{ID: "all", Kind: TabAll}, {ID: "review", Kind: TabCustomSource},
	}})
	m, _ = update(t, m, SourceResultMsg{Source: config.SourceProjects, Candidates: []source.Candidate{{Path: "/same", Label: "project", Source: config.SourceProjects}}})
	m, _ = update(t, m, SourceResultMsg{Source: "review", Candidates: []source.Candidate{{Path: "/same", Label: "pull request", Source: "review"}}})
	if len(m.rows) != 1 || m.rows[0].Candidate.Source != config.SourceProjects {
		t.Fatalf("all rows = %+v", m.rows)
	}
	next, _ := m.cycleTabForward()
	m = next.(Model)
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "pull request" {
		t.Fatalf("custom source rows = %+v", m.rows)
	}
}

func TestConfiguredTabs_AllKeepsEnabledRowOnPathCollision(t *testing.T) {
	m := NewModelWithProducers(nil, "", nil, context.Background(), Layout{SourceOrder: []string{config.SourceProjects}, Tabs: []TabDefinition{
		{ID: "all", Kind: TabAll}, {ID: "review", Kind: TabCustomSource},
	}})
	m, _ = update(t, m, SourceResultMsg{Source: config.SourceProjects, Candidates: []source.Candidate{{Path: "/same", NormalizedPath: "/same", Label: "project", Source: config.SourceProjects}}})
	m, _ = update(t, m, SourceResultMsg{Source: "review", Candidates: []source.Candidate{{Path: "/same", NormalizedPath: "/same", Label: "pull request", Source: "review"}}})
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "project" {
		t.Fatalf("all collision rows = %+v", m.rows)
	}
	next, _ := m.cycleTabForward()
	m = next.(Model)
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "pull request" {
		t.Fatalf("custom source collision rows = %+v", m.rows)
	}
}

func TestConfiguredTabs_GroupAgentLiveStatus(t *testing.T) {
	candidate := source.Candidate{
		Source: config.SourceAgents, Path: "/team", Label: "agent session",
		Meta: map[string]string{"pane_id": "p1", "tab_id": "t1", "agent_status": "working", "kind": "agent"},
	}
	m := NewModelWithLayout(nil, nil, Layout{Icons: IconsASCII, Tabs: []TabDefinition{{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceAgents}}}})
	m, _ = update(t, m, groupResultMsg{id: "team", candidates: []source.Candidate{candidate}})
	if len(m.rows) != 1 || m.rows[0].Action != RowActionFocusTab {
		t.Fatalf("group agent row/action = %+v", m.rows)
	}
	before, _ := m.rowDisplayText(m.rows[0])
	if !strings.Contains(before, "agent session") || !strings.Contains(before, m.icons().StatusWorking) {
		t.Fatalf("initial group agent row = %q", before)
	}
	m, _ = update(t, m, paneStatusMsg{PaneID: "p1", Status: "blocked"})
	row := m.rows[0]
	after, _ := m.rowDisplayText(row)
	if row.Candidate.Meta["agent_status"] != "blocked" || !strings.HasPrefix(after, m.icons().StatusBlocked+" ") || strings.HasPrefix(after, m.icons().StatusWorking+" ") {
		t.Fatalf("updated group agent row = %q, candidate = %+v", after, row.Candidate)
	}
	if candidate.Meta["agent_status"] != "working" {
		t.Fatalf("original candidate metadata mutated: %+v", candidate.Meta)
	}
	if m.groupCandidates["team"][0].Meta["agent_status"] != "blocked" {
		t.Fatalf("owned group candidate not updated: %+v", m.groupCandidates["team"])
	}
	if m.previewSeq == 0 {
		t.Fatal("selected agent preview was not invalidated on status change")
	}
}

func TestConfiguredTabs_GroupLateResultKeepsLiveAgentStatus(t *testing.T) {
	m := NewModelWithLayout(nil, nil, Layout{Tabs: []TabDefinition{{ID: "team", Kind: TabGroup, SourceOrder: []string{config.SourceAgents}}}})
	m, _ = update(t, m, paneStatusMsg{PaneID: "p1", Status: "blocked"})
	original := source.Candidate{Source: config.SourceAgents, Label: "agent", Meta: map[string]string{"pane_id": "p1", "agent_status": "working"}}
	m, _ = update(t, m, groupResultMsg{id: "team", candidates: []source.Candidate{original}})
	if len(m.rows) != 1 || m.rows[0].Candidate.Meta["agent_status"] != "blocked" {
		t.Fatalf("late group rows = %+v", m.rows)
	}
	if original.Meta["agent_status"] != "working" {
		t.Fatal("late group result metadata mutated")
	}
}

func TestConfiguredTabs_SourceAgentLiveStatus(t *testing.T) {
	candidate := source.Candidate{Source: config.SourceAgents, Path: "/team", Label: "agent", Meta: map[string]string{"pane_id": "p1", "agent_status": "idle"}}
	m := NewModelWithLayout([]source.Candidate{candidate}, nil, Layout{Icons: IconsASCII, Tabs: []TabDefinition{{ID: config.SourceAgents, Kind: TabSource}}})
	if len(m.rows) != 1 || m.rows[0].Action != RowActionFocusTab {
		t.Fatalf("source agent rows = %+v", m.rows)
	}
	m, _ = update(t, m, paneStatusMsg{PaneID: "p1", Status: "done"})
	label, _ := m.rowDisplayText(m.rows[0])
	if !strings.HasPrefix(label, "* ") || m.rows[0].Candidate.Meta["agent_status"] != "done" {
		t.Fatalf("source agent status row = %q, %+v", label, m.rows[0])
	}
	if candidate.Meta["agent_status"] != "idle" {
		t.Fatal("original source candidate metadata mutated")
	}
}

func TestAgentScope_OrderingUrgencyAndMRU(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
			{ID: "w2", Label: "ecorp", CWD: "/srv/ecorp"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
			{ID: "t2", WorkspaceID: "w2", Label: "audit"},
		},
		Panes: []source.Pane{
			{
				ID:            "p_idle",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Label:         "idle-pane",
				Agent:         "opencode",
				AgentStatus:   "idle",
				TerminalTitle: "fsociety idle agent",
			},
			{
				ID:            "p_blocked",
				WorkspaceID:   "w2",
				TabID:         "t2",
				Label:         "blocked-pane",
				Agent:         "pi",
				AgentStatus:   "blocked",
				TerminalTitle: "ecorp security block",
			},
			{
				ID:            "p_working",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Label:         "working-pane",
				Agent:         "claude",
				AgentStatus:   "working",
				TerminalTitle: "payload generator",
			},
			{
				ID:            "p_done",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Label:         "done-pane",
				Agent:         "opencode",
				AgentStatus:   "done",
				TerminalTitle: "finished scan",
			},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 4 {
		t.Fatalf("expected 4 agent rows, got %d", len(m.rows))
	}

	// Expected tier order: unacknowledged attention (p_blocked, p_done) > working (p_working) > idle (p_idle)
	expectedOrder := []string{"p_blocked", "p_done", "p_working", "p_idle"}
	for i, wantID := range expectedOrder {
		gotID := m.rows[i].Candidate.Meta["pane_id"]
		if gotID != wantID {
			t.Errorf("row[%d] pane_id = %q, want %q", i, gotID, wantID)
		}
		if m.rows[i].Action != RowActionFocusTab {
			t.Errorf("row[%d] action = %v, want RowActionFocusTab", i, m.rows[i].Action)
		}
	}
}

func TestAgentScope_TiedUrgencyBreaksByWorkspaceMRU(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w_recent", Label: "arcade", CWD: "/srv/arcade"},
			{ID: "w_older", Label: "allsafe", CWD: "/srv/allsafe"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w_recent", Label: "t1"},
			{ID: "t2", WorkspaceID: "w_older", Label: "t2"},
		},
		Panes: []source.Pane{
			{
				ID:          "p_older",
				WorkspaceID: "w_older",
				TabID:       "t2",
				Agent:       "opencode",
				AgentStatus: "working",
			},
			{
				ID:          "p_recent",
				WorkspaceID: "w_recent",
				TabID:       "t1",
				Agent:       "pi",
				AgentStatus: "working",
			},
		},
	}

	rs := ranking.Snapshot{}.WithFilteredWorkspaceMRU(snapshot.Workspaces)
	// Make w_recent more recent (rank 0) than w_older
	rs = rs.WithWorkspaceMRU([]string{"w_recent", "w_older"})

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m.rankingSnapshot = rs
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m.rows))
	}

	if m.rows[0].Candidate.Meta["pane_id"] != "p_recent" {
		t.Errorf("row[0] = %q, want p_recent (more recent workspace)", m.rows[0].Candidate.Meta["pane_id"])
	}
	if m.rows[1].Candidate.Meta["pane_id"] != "p_older" {
		t.Errorf("row[1] = %q, want p_older", m.rows[1].Candidate.Meta["pane_id"])
	}
}

func TestAgentScope_LiveStatusUrgencyReorder(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "t1"},
		},
		Panes: []source.Pane{
			{
				ID:          "p1",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "opencode",
				AgentStatus: "working",
			},
			{
				ID:          "p2",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "pi",
				AgentStatus: "idle",
			},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = withActiveTab(m, "agents")
	m.applyFilter()

	// Initial order: p1 (working) > p2 (idle)
	if m.rows[0].Candidate.Meta["pane_id"] != "p1" {
		t.Fatalf("initial row[0] = %q, want p1", m.rows[0].Candidate.Meta["pane_id"])
	}

	// Live status: p2 becomes blocked -> should jump to row[0]
	updatedModel, _ := m.handlePaneStatus(paneStatusMsg{PaneID: "p2", Status: "blocked"})
	m = updatedModel

	if len(m.rows) != 2 {
		t.Fatalf("expected 2 rows after update, got %d", len(m.rows))
	}
	if m.rows[0].Candidate.Meta["pane_id"] != "p2" {
		t.Errorf("after p2 blocked, row[0] = %q, want p2", m.rows[0].Candidate.Meta["pane_id"])
	}
	if m.rows[0].Candidate.Meta["agent_status"] != "blocked" {
		t.Errorf("row[0] status = %q, want blocked", m.rows[0].Candidate.Meta["agent_status"])
	}
}

func TestAgentScope_Filtering(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
		},
		Panes: []source.Pane{
			{
				ID:            "p1",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Agent:         "opencode",
				AgentStatus:   "blocked",
				TerminalTitle: "compiler error",
			},
			{
				ID:            "p2",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Agent:         "claude",
				AgentStatus:   "working",
				TerminalTitle: "codegen",
			},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = withActiveTab(m, "agents")

	// Filter by status:blocked
	m.query = "status:blocked"
	m.applyFilter()
	if len(m.rows) != 1 || m.rows[0].Candidate.Meta["pane_id"] != "p1" {
		t.Errorf("filter status:blocked: expected only p1, got %d rows", len(m.rows))
	}

	// Filter by agent:claude
	m.query = "agent:claude"
	m.applyFilter()
	if len(m.rows) != 1 || m.rows[0].Candidate.Meta["pane_id"] != "p2" {
		t.Errorf("filter agent:claude: expected only p2, got %d rows", len(m.rows))
	}

	// Filter with negation !blocked
	m.query = "!blocked"
	m.applyFilter()
	if len(m.rows) != 1 || m.rows[0].Candidate.Meta["pane_id"] != "p2" {
		t.Errorf("filter !blocked: expected only p2, got %d rows", len(m.rows))
	}
}

func TestAgentScope_Counts(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Panes: []source.Pane{
			{ID: "p1", Agent: "opencode", AgentStatus: "blocked"},
			{ID: "p2", Agent: "claude", AgentStatus: "working"},
			{ID: "p3", Agent: "pi", AgentStatus: "idle"},
			{ID: "p4", Agent: "opencode", AgentStatus: "done"},
			{ID: "p5", Agent: "", AgentStatus: ""}, // non-agent pane
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot

	counts := m.AgentCounts()
	if counts.Total != 4 {
		t.Errorf("Total = %d, want 4", counts.Total)
	}
	if counts.Blocked != 1 {
		t.Errorf("Blocked = %d, want 1", counts.Blocked)
	}
	if counts.Working != 1 {
		t.Errorf("Working = %d, want 1", counts.Working)
	}
	if counts.Idle != 1 {
		t.Errorf("Idle = %d, want 1", counts.Idle)
	}
	if counts.Done != 1 {
		t.Errorf("Done = %d, want 1", counts.Done)
	}
}

func TestAgentScope_EnterDispatchesFocus(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
		},
		Panes: []source.Pane{
			{
				ID:          "p1",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "opencode",
				AgentStatus: "blocked",
			},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = withActiveTab(m, "agents")
	m.applyFilter()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected quit cmd on Enter")
	}
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", next)
	}
	cand, ok := mm.Selected()
	if !ok {
		t.Fatal("expected selection on Enter")
	}
	if cand.Meta["pane_id"] != "p1" {
		t.Errorf("selected pane_id = %q, want p1", cand.Meta["pane_id"])
	}
	if mm.SelectedAction() != RowActionFocusTab {
		t.Errorf("SelectedAction = %v, want RowActionFocusTab", mm.SelectedAction())
	}
}

func TestCollectAgentCandidates_SessionNameLabelAndFallbacks(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "ws1", CWD: "/srv/ws1"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "tab1"},
		},
		Panes: []source.Pane{
			{
				ID:            "p_title",
				WorkspaceID:   "w1",
				TabID:         "t1",
				Agent:         "pi",
				AgentStatus:   "working",
				Label:         "fallback-label",
				TerminalTitle: "security scan",
			},
			{
				ID:          "p_label",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "claude",
				AgentStatus: "idle",
				Label:       "codegen-task",
			},
			{
				ID:          "p_fallback",
				WorkspaceID: "w1",
				TabID:       "t1",
				Agent:       "opencode",
				AgentStatus: "blocked",
			},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot

	cands := m.collectAgentCandidates()
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}

	candMap := make(map[string]source.Candidate)
	for _, c := range cands {
		candMap[c.Meta["pane_id"]] = c
	}

	// 1. TerminalTitle takes precedence even when Agent is set and not in title
	if got := candMap["p_title"].Label; got != "security scan" {
		t.Errorf("p_title label = %q, want \"security scan\"", got)
	}
	// Meta keys preserved
	if got := candMap["p_title"].Meta["agent"]; got != "pi" {
		t.Errorf("p_title meta[agent] = %q, want \"pi\"", got)
	}
	if got := candMap["p_title"].Meta["terminal_title"]; got != "security scan" {
		t.Errorf("p_title meta[terminal_title] = %q, want \"security scan\"", got)
	}
	if got := candMap["p_title"].Meta["kind"]; got != "agent" {
		t.Errorf("p_title meta[kind] = %q, want \"agent\"", got)
	}

	// 2. Fallback to p.Label when TerminalTitle is empty
	if got := candMap["p_label"].Label; got != "codegen-task" {
		t.Errorf("p_label label = %q, want \"codegen-task\"", got)
	}

	// 3. Fallback to "agent " + ID when both are empty
	if got := candMap["p_fallback"].Label; got != "agent p_fallback" {
		t.Errorf("p_fallback label = %q, want \"agent p_fallback\"", got)
	}
}

func TestAgentScope_RowPrimaryText_SessionNameAndStatusIconOnly(t *testing.T) {
	t.Parallel()

	m := newRenderTestModel(ThemeMocha, FocusList)
	set := m.icons()

	agentRow := Row{
		Kind:   RowPane,
		Depth:  0,
		Action: RowActionFocusTab,
		Candidate: source.Candidate{
			Label: "security scan",
			Path:  "/srv/ws1/src",
			Meta: map[string]string{
				"agent":          "pi",
				"agent_status":   "blocked",
				"terminal_title": "security scan",
				"kind":           "agent",
			},
		},
	}

	primary, prefixRunes := m.rowPrimaryText(agentRow)
	icon := m.agentStatusIcon("blocked")
	want := icon + " security scan"
	if primary != want {
		t.Errorf("agent row primary = %q, want %q", primary, want)
	}
	if strings.Contains(primary, "·") {
		t.Errorf("agent row primary %q must not contain \"·\"", primary)
	}
	if strings.Contains(primary, "/srv/ws1") {
		t.Errorf("agent row primary %q must not contain path", primary)
	}
	if strings.Contains(primary, set.TreeMid) || strings.Contains(primary, set.TreeLast) || strings.Contains(primary, set.TreeVertical) {
		t.Errorf("agent row primary %q must not contain tree glyphs", primary)
	}
	// The status glyph leads the label (the agents label_format starts with
	// {{ status }}); with no icon there is no fixed prefix.
	if prefixRunes != 0 {
		t.Errorf("prefixRunes = %d, want 0", prefixRunes)
	}

	// Nested tree pane row (Depth: 2) uses formats.Pane: its label, or its
	// path when it has none — never the agents format.
	nestedTreeRow := Row{
		Kind:   RowPane,
		Depth:  2,
		IsLast: true,
		Candidate: source.Candidate{
			Label: "p1",
			Path:  "/srv/ws1/src",
			Meta: map[string]string{
				"agent_status": "idle",
			},
		},
	}
	nestedPrimary, _ := m.rowPrimaryText(nestedTreeRow)
	if !strings.HasSuffix(nestedPrimary, m.agentStatusIcon("idle")+" p1") {
		t.Errorf("nested tree pane primary %q should end with its status glyph and label", nestedPrimary)
	}
	nestedTreeRow.Candidate.Label = ""
	if nestedPrimary, secondary := m.rowDisplayText(nestedTreeRow); !strings.HasSuffix(nestedPrimary, " src") || secondary != "/srv/ws1" {
		t.Errorf("unlabeled nested tree pane = %q + %q, want its path, filename first", nestedPrimary, secondary)
	}
}

func TestAgentScope_FullDeterministicTiers(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
		},
		Panes: []source.Pane{
			{ID: "p_unknown", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "unknown", TerminalTitle: "unknown agent"},
			{ID: "p_idle", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "idle", TerminalTitle: "idle agent"},
			{ID: "p_blocked_acked", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "blocked", TerminalTitle: "acked blocked"},
			{ID: "p_done_acked", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "done", TerminalTitle: "acked done"},
			{ID: "p_working", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "working", TerminalTitle: "working agent"},
			{ID: "p_done_new", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "done", TerminalTitle: "new done"},
			{ID: "p_blocked_new", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "blocked", TerminalTitle: "new blocked"},
		},
	}

	rs := ranking.Snapshot{}.
		WithAcknowledgement("p_blocked_acked", "blocked").
		WithAcknowledgement("p_done_acked", "done")

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m.rankingSnapshot = rs
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 7 {
		t.Fatalf("expected 7 rows, got %d", len(m.rows))
	}

	// Expected order:
	// Tier 1: p_blocked_new, p_done_new (unacknowledged blocked/done, sorted by pane ID)
	// Tier 2: p_working
	// Tier 3: p_blocked_acked, p_done_acked (acknowledged blocked/done, sorted by pane ID)
	// Tier 4: p_idle
	// Tier 5: p_unknown
	expectedOrder := []string{
		"p_blocked_new",
		"p_done_new",
		"p_working",
		"p_blocked_acked",
		"p_done_acked",
		"p_idle",
		"p_unknown",
	}

	for i, wantID := range expectedOrder {
		gotID := m.rows[i].Candidate.Meta["pane_id"]
		if gotID != wantID {
			t.Errorf("row[%d] pane_id = %q, want %q", i, gotID, wantID)
		}
	}
}

func TestAgentScope_AttentionTierRemainsPrimaryDuringQuery(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
		},
		Panes: []source.Pane{
			{ID: "p_idle_exact", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "idle", TerminalTitle: "deploy"},
			{ID: "p_working_exact", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "working", TerminalTitle: "deploy"},
			{ID: "p_blocked_fuzzy", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "blocked", TerminalTitle: "deep-deploy-job"},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = withActiveTab(m, "agents")
	m.query = "deploy"
	m.applyFilter()

	if len(m.rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(m.rows))
	}

	// Even though p_idle_exact and p_working_exact have higher exact match scores for "deploy",
	// p_blocked_fuzzy (Tier 1) must sort before p_working_exact (Tier 2) which sorts before p_idle_exact (Tier 4).
	expectedOrder := []string{"p_blocked_fuzzy", "p_working_exact", "p_idle_exact"}
	for i, wantID := range expectedOrder {
		gotID := m.rows[i].Candidate.Meta["pane_id"]
		if gotID != wantID {
			t.Errorf("row[%d] pane_id = %q, want %q", i, gotID, wantID)
		}
	}
}

func TestAgentScope_LiveTransitionInvalidatesAckAndRestoresNewAttention(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
		},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "blocked", TerminalTitle: "scan agent"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "working", TerminalTitle: "worker agent"},
		},
	}

	var clearedPane string
	ackClearer := func(_ context.Context, paneID string) {
		clearedPane = paneID
	}

	rs := ranking.Snapshot{}.WithAcknowledgement("p1", "blocked")

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{AckClearer: ackClearer})
	m.startupSnapshot = snapshot
	m.rankingSnapshot = rs
	m = withActiveTab(m, "agents")
	m.applyFilter()

	// Initial: p1 is acknowledged blocked (Tier 3), so p2 (working, Tier 2) is first
	if m.rows[0].Candidate.Meta["pane_id"] != "p2" || m.rows[1].Candidate.Meta["pane_id"] != "p1" {
		t.Fatalf("initial order got [%s, %s], want [p2, p1]", m.rows[0].Candidate.Meta["pane_id"], m.rows[1].Candidate.Meta["pane_id"])
	}

	// Step 1: p1 transitions to "working" -> ack should be invalidated immediately
	updatedModel, cmd := m.handlePaneStatus(paneStatusMsg{PaneID: "p1", Status: "working"})
	m = updatedModel

	if m.rankingSnapshot.IsPaneAcknowledged("p1", "blocked") {
		t.Errorf("in-memory ack for p1 was not cleared on transition to working")
	}

	// Execute cmd (and every command of a batch) to test the AckClearer
	// callback; the spinner tick it may also carry is not run.
	runBatch(cmd)
	if clearedPane != "p1" {
		t.Errorf("AckClearer was not called with p1, got %q", clearedPane)
	}

	// Step 2: p1 transitions back to "blocked" -> must be Tier 1 (new attention!)
	updatedModel2, _ := m.handlePaneStatus(paneStatusMsg{PaneID: "p1", Status: "blocked"})
	m = updatedModel2

	if len(m.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m.rows))
	}
	// p1 is now unacknowledged blocked (Tier 1) -> must be first!
	if m.rows[0].Candidate.Meta["pane_id"] != "p1" {
		t.Errorf("row[0] = %q, want p1 (new attention after returning to blocked)", m.rows[0].Candidate.Meta["pane_id"])
	}
	if m.rows[1].Candidate.Meta["pane_id"] != "p2" {
		t.Errorf("row[1] = %q, want p2", m.rows[1].Candidate.Meta["pane_id"])
	}
}

func TestAgentScope_CandidatesUseSourceAgents(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "fsociety", CWD: "/srv/fsociety"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "arcade"},
		},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "opencode", AgentStatus: "working"},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(m.rows))
	}
	if got, want := m.rows[0].Candidate.Source, config.SourceAgents; got != want {
		t.Errorf("row[0].Candidate.Source = %q, want %q", got, want)
	}
}

func TestAgentScope_CurrentPaneLastAndPreviousFirst(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "w1", CWD: "/srv/w1"},
			{ID: "w2", Label: "w2", CWD: "/srv/w2"},
			{ID: "w3", Label: "w3", CWD: "/srv/w3"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "t1"},
			{ID: "t2", WorkspaceID: "w2", Label: "t2"},
			{ID: "t3", WorkspaceID: "w3", Label: "t3"},
		},
		Panes: []source.Pane{
			{ID: "p_current", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "working"},
			{ID: "p_prev", WorkspaceID: "w2", TabID: "t2", Agent: "pi", AgentStatus: "working"},
			{ID: "p_older", WorkspaceID: "w3", TabID: "t3", Agent: "opencode", AgentStatus: "working"},
		},
	}

	rs := ranking.Snapshot{}.WithFilteredWorkspaceMRU(snapshot.Workspaces)
	// w1 is current (MRU 0), w2 is previous (MRU 1), w3 is older (MRU 2)
	rs = rs.WithWorkspaceMRU([]string{"w1", "w2", "w3"})

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m.rankingSnapshot = rs
	m = m.WithCurrentPane(&snapshot.Panes[0]) // p_current
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(m.rows))
	}

	// Expected: p_prev (previous focus, MRU 1) -> p_older (older focus, MRU 2) -> p_current (current pane demoted to last)
	expectedOrder := []string{"p_prev", "p_older", "p_current"}
	for i, wantID := range expectedOrder {
		gotID := m.rows[i].Candidate.Meta["pane_id"]
		if gotID != wantID {
			t.Errorf("row[%d] = %q, want %q", i, gotID, wantID)
		}
	}
}

// Agent rows use workspace focus history only when the immediate preceding
// workspace contains exactly one agent; pane-selection history is not Herdr focus.
func TestAgentScope_PriorToggleAndFallback(t *testing.T) {
	panes := []source.Pane{
		{ID: "a", WorkspaceID: "wa", TabID: "ta", Agent: "pi", AgentStatus: "idle", TerminalTitle: "deploy"},
		{ID: "b", WorkspaceID: "wb", TabID: "tb", Agent: "pi", AgentStatus: "idle", TerminalTitle: "deploy"},
		{ID: "worker", WorkspaceID: "wc", TabID: "tc", Agent: "pi", AgentStatus: "working", TerminalTitle: "deploy"},
	}
	cases := []struct {
		name    string
		current string
		mru     []string
		recent  []string
		status  string
		ack     bool
		query   string
		want    []string
	}{
		{name: "A to B promotes idle A", current: "b", mru: []string{"wb", "wa", "wc"}, want: []string{"a", "worker", "b"}},
		{name: "B to A promotes idle B", current: "a", mru: []string{"wa", "wb", "wc"}, want: []string{"b", "worker", "a"}},
		{name: "new attention beats previous", current: "b", mru: []string{"wb", "wa", "wc"}, status: "blocked", want: []string{"worker", "a", "b"}},
		{name: "acknowledged attention is not new", current: "b", mru: []string{"wb", "wa", "wc"}, status: "blocked", ack: true, want: []string{"a", "worker", "b"}},
		{name: "current new attention is first", current: "b", mru: []string{"wb", "wa", "wc"}, status: "current-done", want: []string{"b", "a", "worker"}},
		{name: "no history working then idle current last", current: "b", want: []string{"worker", "a", "b"}},
		{name: "pane history without workspace history only ranks within tier", current: "b", recent: []string{"herdr:pane:a"}, want: []string{"worker", "a", "b"}},
		{name: "workspace MRU without current cannot prove predecessor", current: "b", mru: []string{"wa", "wc"}, want: []string{"worker", "a", "b"}},
		{name: "shell-only immediate predecessor does not promote older agent", current: "b", mru: []string{"wb", "shell", "wa", "wc"}, want: []string{"worker", "a", "b"}},
		{name: "shell-only predecessor ignores older Shep pane selection", current: "b", mru: []string{"wb", "shell", "wa", "wc"}, recent: []string{"herdr:pane:a"}, want: []string{"worker", "a", "b"}},
		{name: "current pane absent from agent rows", current: "shell", mru: []string{"wb", "wa", "wc"}, want: []string{"worker", "b", "a"}},
		{name: "query score breaks ties after attention and prior", current: "b", mru: []string{"wb", "wa", "wc"}, query: "deploy", want: []string{"a", "worker", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := source.Snapshot{
				Workspaces: []source.Workspace{{ID: "wa"}, {ID: "wb"}, {ID: "wc"}, {ID: "shell"}},
				Tabs:       []source.Tab{{ID: "ta", WorkspaceID: "wa"}, {ID: "tb", WorkspaceID: "wb"}, {ID: "tc", WorkspaceID: "wc"}, {ID: "ts", WorkspaceID: "shell"}},
				Panes:      append(append([]source.Pane(nil), panes...), source.Pane{ID: "shell-pane", WorkspaceID: "shell", TabID: "ts"}), FocusedPaneID: tc.current,
			}
			if tc.status == "current-done" {
				snap.Panes[1].AgentStatus = "done"
			} else if tc.status != "" {
				snap.Panes[2].AgentStatus = tc.status
			}
			rs := ranking.Snapshot{}.WithWorkspaceMRU(tc.mru).WithRecent(tc.recent)
			if tc.ack {
				rs = rs.WithAcknowledgement("worker", tc.status)
			}
			m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(snap), Layout{})
			m.startupSnapshot = &snap
			m.rankingSnapshot = rs
			m = withActiveTab(m, "agents")
			m.query = tc.query
			m.applyFilter()
			var got []string
			for _, row := range m.rows {
				got = append(got, row.Candidate.Meta["pane_id"])
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentScope_AmbiguousPriorWorkspace(t *testing.T) {
	base := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "current"}, {ID: "prior"}, {ID: "other"}},
		Panes: []source.Pane{
			{ID: "current", WorkspaceID: "current", TabID: "t0", Agent: "pi", AgentStatus: "idle"},
			{ID: "z_idle", WorkspaceID: "prior", TabID: "t1", Agent: "pi", AgentStatus: "idle"},
			{ID: "a_idle", WorkspaceID: "prior", TabID: "t2", Agent: "pi", AgentStatus: "idle"},
			{ID: "worker", WorkspaceID: "other", TabID: "t3", Agent: "pi", AgentStatus: "working"},
		}, FocusedPaneID: "current",
	}
	for _, tc := range []struct {
		name   string
		recent []string
		want   []string
	}{
		{name: "tab focus does not identify pane", want: []string{"worker", "a_idle", "z_idle", "current"}},
		{name: "stale pane selection does not identify prior", recent: []string{"herdr:pane:z_idle"}, want: []string{"worker", "z_idle", "a_idle", "current"}},
		{name: "unrelated pane selection cannot identify prior", recent: []string{"herdr:pane:worker"}, want: []string{"worker", "a_idle", "z_idle", "current"}},
		{name: "older prior pane selection not decisive", recent: []string{"herdr:pane:worker", "herdr:pane:z_idle"}, want: []string{"worker", "z_idle", "a_idle", "current"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(base), Layout{})
			m.startupSnapshot = &base
			m.rankingSnapshot = ranking.Snapshot{}.WithWorkspaceMRU([]string{"current", "prior", "other"}).WithRecent(tc.recent)
			m = withActiveTab(m, "agents")
			m.applyFilter()
			var got []string
			for _, row := range m.rows {
				got = append(got, row.Candidate.Meta["pane_id"])
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("rows = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentScope_CurrentPaneDoesNotHideUnacknowledgedAttention(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "w1", CWD: "/srv/w1"},
			{ID: "w2", Label: "w2", CWD: "/srv/w2"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "t1"},
			{ID: "t2", WorkspaceID: "w2", Label: "t2"},
		},
		Panes: []source.Pane{
			{ID: "p_current_blocked", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "blocked"},
			{ID: "p_other_blocked", WorkspaceID: "w2", TabID: "t2", Agent: "pi", AgentStatus: "blocked"},
			{ID: "p_prev_working", WorkspaceID: "w2", TabID: "t2", Agent: "opencode", AgentStatus: "working"},
		},
	}

	rs := ranking.Snapshot{}.WithFilteredWorkspaceMRU(snapshot.Workspaces)
	rs = rs.WithWorkspaceMRU([]string{"w1", "w2"})

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m.rankingSnapshot = rs
	m = m.WithCurrentPane(&snapshot.Panes[0]) // p_current_blocked
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(m.rows))
	}

	// Unacknowledged attention is Tier 1 (p_other_blocked, p_current_blocked).
	// Within Tier 1, p_current_blocked is demoted after p_other_blocked.
	// But both are before Tier 2 (p_prev_working) — unacknowledged attention is NOT hidden.
	expectedOrder := []string{"p_other_blocked", "p_current_blocked", "p_prev_working"}
	for i, wantID := range expectedOrder {
		gotID := m.rows[i].Candidate.Meta["pane_id"]
		if gotID != wantID {
			t.Errorf("row[%d] = %q, want %q", i, gotID, wantID)
		}
	}
}

func TestAgentScope_PriorSelectionFirstViaRanking(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "w1", CWD: "/srv/w1"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "t1"},
		},
		Panes: []source.Pane{
			{ID: "p_unranked_b", WorkspaceID: "w1", TabID: "t1", Agent: "opencode", AgentStatus: "working"},
			{ID: "p_older", WorkspaceID: "w1", TabID: "t1", Agent: "gemini", AgentStatus: "working"},
			{ID: "p_prior", WorkspaceID: "w1", TabID: "t1", Agent: "pi", AgentStatus: "working"},
			{ID: "p_unranked_a", WorkspaceID: "w1", TabID: "t1", Agent: "cursor", AgentStatus: "working"},
			{ID: "p_current", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "working"},
		},
	}

	// p_prior was selected most recently, p_older before that.
	rs := ranking.Snapshot{}.WithRecent([]string{
		"herdr:pane:p_prior",
		"herdr:pane:p_older",
	})

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m.rankingSnapshot = rs
	m = m.WithCurrentPane(&snapshot.Panes[4]) // p_current
	m = withActiveTab(m, "agents")
	m.applyFilter()

	if len(m.rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(m.rows))
	}

	// Expected:
	// 1. p_prior (most recent prior selection, rank 0)
	// 2. p_older (older prior selection, rank 1)
	// 3. p_unranked_a (no history: falls back deterministically by pane_id, "p_unranked_a" < "p_unranked_b")
	// 4. p_unranked_b (no history: pane_id "p_unranked_b")
	// 5. p_current (current pane: always demoted to last within tier)
	expectedOrder := []string{"p_prior", "p_older", "p_unranked_a", "p_unranked_b", "p_current"}
	for i, wantID := range expectedOrder {
		gotID := m.rows[i].Candidate.Meta["pane_id"]
		if gotID != wantID {
			t.Errorf("row[%d] = %q, want %q", i, gotID, wantID)
		}
	}
}

func TestAgentScope_QueryTieBreaksByCurrentPaneAndMRU(t *testing.T) {
	t.Parallel()

	snapshot := &source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "w1", CWD: "/srv/w1"},
			{ID: "w2", Label: "w2", CWD: "/srv/w2"},
		},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "t1"},
			{ID: "t2", WorkspaceID: "w2", Label: "t2"},
		},
		Panes: []source.Pane{
			{ID: "p_current", WorkspaceID: "w1", TabID: "t1", Agent: "agent", AgentStatus: "working", TerminalTitle: "task"},
			{ID: "p_other", WorkspaceID: "w2", TabID: "t2", Agent: "agent", AgentStatus: "working", TerminalTitle: "task"},
		},
	}

	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(*snapshot), Layout{})
	m.startupSnapshot = snapshot
	m = m.WithCurrentPane(&snapshot.Panes[0]) // p_current
	m = withActiveTab(m, "agents")
	m.query = "task"
	m.applyFilter()

	if len(m.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m.rows))
	}

	// Both match "task" identically in the same tier.
	// Current pane must be demoted after the non-current pane.
	if m.rows[0].Candidate.Meta["pane_id"] != "p_other" {
		t.Errorf("row[0] = %q, want p_other", m.rows[0].Candidate.Meta["pane_id"])
	}
	if m.rows[1].Candidate.Meta["pane_id"] != "p_current" {
		t.Errorf("row[1] = %q, want p_current", m.rows[1].Candidate.Meta["pane_id"])
	}
}

// agentsRefreshFixture builds one agent-pane generation for snapshot-refresh
// tests: each pane gets a distinct foreground CWD so candidate identity never
// collides.
func agentsRefreshFixture(panes ...source.Pane) source.Snapshot {
	return source.Snapshot{
		Workspaces: []source.Workspace{
			{ID: "w1", Label: "ws1", CWD: "/srv/ws1", ActiveTabID: "w1:t1"},
		},
		Tabs: []source.Tab{
			{ID: "w1:t1", WorkspaceID: "w1", Label: "editor", Number: 1, PaneCount: len(panes)},
		},
		Panes:              panes,
		FocusedPaneID:      panes[0].ID,
		FocusedTabID:       "w1:t1",
		FocusedWorkspaceID: "w1",
	}
}

// TestAgentScope_SnapshotRefreshReplacesSourceAgentsRows proves the periodic
// full-generation refresh re-derives the agents rows in the all view from the
// new snapshot instead of leaving stale SourceAgents rows behind: replaced
// statuses update, departed panes drop, new panes appear, and the refresh
// never injects herdr workspace rows into a view whose source_order enables
// only the agents source.
func TestAgentScope_SnapshotRefreshReplacesSourceAgentsRows(t *testing.T) {
	t.Parallel()

	snap1 := agentsRefreshFixture(
		source.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: "idle", ForegroundCWD: "/srv/ws1/a"},
		source.Pane{ID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", Agent: "pi", AgentStatus: "working", ForegroundCWD: "/srv/ws1/b"},
	)
	snap2 := agentsRefreshFixture(
		source.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: "blocked", ForegroundCWD: "/srv/ws1/a"},
		source.Pane{ID: "w1:p3", WorkspaceID: "w1", TabID: "w1:t1", Agent: "opencode", AgentStatus: "done", ForegroundCWD: "/srv/ws1/c"},
	)
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: snap2}}}

	prodAgents := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source:          config.SourceAgents,
			Candidates:      source.AgentCandidates(snap1),
			Snapshot:        &snap1,
			SnapshotDriver:  driver,
			SnapshotSources: []string{config.SourceAgents},
			Tree:            NewTreeExpanderFromSnapshot(snap1),
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodAgents}, "", nil, context.Background(), Layout{SourceOrder: []string{config.SourceAgents}})
	msg := prodAgents(context.Background())
	msg.producerID = 0
	next, _ := m.Update(msg)
	m = next.(Model)

	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	refreshCmd := m.applyFilter()
	if refreshCmd == nil {
		t.Fatal("expired generation did not schedule a snapshot refresh")
	}
	next, _ = m.Update(refreshCmd())
	m = next.(Model)

	byPane := map[string]source.Candidate{}
	for _, c := range m.baseCandidates {
		if c.Source == config.SourceHerdr {
			t.Errorf("agents-only refresh introduced herdr row %+v", c)
			continue
		}
		if c.Source == config.SourceAgents {
			byPane[c.Meta["pane_id"]] = c
		}
	}
	if got := byPane["w1:p1"].Meta["agent_status"]; got != "blocked" {
		t.Errorf("w1:p1 agent_status = %q, want %q from the new generation", got, "blocked")
	}
	if _, ok := byPane["w1:p2"]; ok {
		t.Error("departed pane w1:p2 still present after refresh")
	}
	if got := byPane["w1:p3"].Meta["agent_status"]; got != "done" {
		t.Errorf("w1:p3 agent_status = %q, want %q for the newly appeared pane", got, "done")
	}
}

// TestAgentScope_SnapshotRefreshPreservesLiveStatusAndSelection proves a
// refresh keeps the newest live pane status over the snapshot's own value,
// keeps the highlighted row's identity across the rebuild, and survives a late
// producer arrival without resurrecting stale agents rows.
func TestAgentScope_SnapshotRefreshPreservesLiveStatusAndSelection(t *testing.T) {
	t.Parallel()

	snap1 := agentsRefreshFixture(
		source.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: "idle", ForegroundCWD: "/srv/ws1/a"},
		source.Pane{ID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", Agent: "pi", AgentStatus: "working", ForegroundCWD: "/srv/ws1/b"},
	)
	// snap2 lists p3 first so index-pinning would visibly lose the selection.
	snap2 := agentsRefreshFixture(
		source.Pane{ID: "w1:p3", WorkspaceID: "w1", TabID: "w1:t1", Agent: "opencode", AgentStatus: "done", ForegroundCWD: "/srv/ws1/c"},
		source.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: "working", ForegroundCWD: "/srv/ws1/a"},
	)
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: snap2}}}

	prodAgents := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source:          config.SourceAgents,
			Candidates:      source.AgentCandidates(snap1),
			Snapshot:        &snap1,
			SnapshotDriver:  driver,
			SnapshotSources: []string{config.SourceAgents},
			Tree:            NewTreeExpanderFromSnapshot(snap1),
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodAgents}, "", nil, context.Background(), Layout{SourceOrder: []string{config.SourceAgents}})
	msg := prodAgents(context.Background())
	msg.producerID = 0
	next, _ := m.Update(msg)
	m = next.(Model)

	// Explicitly navigate to w1:p1's row so selection retention is exercised.
	p1Row := -1
	for i, r := range m.rows {
		if r.Candidate.Meta["pane_id"] == "w1:p1" {
			p1Row = i
			break
		}
	}
	if p1Row < 0 {
		t.Fatalf("no row for w1:p1 in %+v", m.rows)
	}
	m.cursor = p1Row
	m.cursorTouched = true
	prevRowID := m.currentRowID()

	// Request the refresh first, then let a live observation arrive so it is
	// newer than the request and must win over snap2's own status for p3.
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	refreshCmd := m.applyFilter()
	if refreshCmd == nil {
		t.Fatal("expired generation did not schedule a snapshot refresh")
	}
	next, _ = m.Update(paneStatusMsg{PaneID: "w1:p3", WorkspaceID: "w1", TabID: "w1:t1", Status: "blocked"})
	m = next.(Model)
	next, _ = m.Update(refreshCmd())
	m = next.(Model)

	if got := m.currentRowID(); got != prevRowID {
		t.Errorf("selected row after refresh = %q, want %q", got, prevRowID)
	}
	byPane := map[string]source.Candidate{}
	for _, c := range m.baseCandidates {
		if c.Source == config.SourceAgents {
			byPane[c.Meta["pane_id"]] = c
		}
	}
	if got := byPane["w1:p3"].Meta["agent_status"]; got != "blocked" {
		t.Errorf("w1:p3 agent_status = %q, want the live %q to win over the snapshot's %q", got, "blocked", "done")
	}
	if got := byPane["w1:p1"].Meta["agent_status"]; got != "working" {
		t.Errorf("w1:p1 agent_status = %q, want %q from the new generation", got, "working")
	}
	if _, ok := byPane["w1:p2"]; ok {
		t.Error("departed pane w1:p2 still present after refresh")
	}

	// A late producer arrival rebuilds the candidate set; the refreshed agents
	// rows must survive it instead of being resurrected from a stale slice.
	late := SourceResultMsg{
		Source: config.SourceWorkspaces,
		Candidates: []source.Candidate{
			{Label: "ws-late", Path: "/srv/late", Source: config.SourceWorkspaces},
		},
		producerID: 1,
	}
	next, _ = m.Update(late)
	m = next.(Model)
	stale := false
	for _, c := range m.baseCandidates {
		if c.Source != config.SourceAgents {
			continue
		}
		if c.Meta["pane_id"] == "w1:p2" {
			stale = true
		}
	}
	if stale {
		t.Error("late producer arrival resurrected the stale w1:p2 agents row")
	}
}

// TestAgentScope_SourceRowFormattingUsesAgentsLabelFormat verifies the agents
// source row formatting contract for both row shapes: a flat agents-scope pane
// row and a SourceAgents candidate row in the all view both render through
// [sources.agents].label_format with AgentStatus template context, never
// through the path fallback.
func TestAgentScope_SourceRowFormattingUsesAgentsLabelFormat(t *testing.T) {
	t.Parallel()

	m := newRenderTestModel(ThemeMocha, FocusList).withPresentation(func(p *config.Presentations) {
		p.Agents.Label = "{{.Label}} [{{.AgentStatus}}]"
	})

	flatPaneRow := Row{
		Kind:      RowPane,
		Depth:     0,
		Action:    RowActionFocusTab,
		Candidate: source.Candidate{Label: "security scan", Path: "/srv/ws1/src", Meta: map[string]string{"kind": "agent", "agent_status": "blocked"}},
	}
	if got := m.buildRowView(flatPaneRow).label.text; got != "security scan [blocked]" {
		t.Errorf("flat agent row label = %q, want %q", got, "security scan [blocked]")
	}

	allViewRow := Row{
		Kind:      RowCandidate,
		Candidate: source.Candidate{Label: "codegen", Path: "/srv/ws1/src", Source: config.SourceAgents, Meta: map[string]string{"agent_status": "working"}},
	}
	if got := m.buildRowView(allViewRow).label.text; got != "codegen [working]" {
		t.Errorf("all-view agents row label = %q, want %q", got, "codegen [working]")
	}
}

// TestAgentScope_SnapshotRefreshFillsEmptyAgentsSliceAndKeepsIcon triangulates
// the declaration-driven ownership: a source_order that enables agents while
// the startup generation holds no agent panes must still gain agents rows as
// panes appear later (SnapshotSources, not row presence, owns the slice), and
// the resolved agents presentation (here its icon) survives every
// re-derivation, in the all view and the agents view. The same message shape
// must not import herdr rows into the agents-only view.
func TestAgentScope_SnapshotRefreshFillsEmptyAgentsSliceAndKeepsIcon(t *testing.T) {
	t.Parallel()

	const sourceIcon = "🤖 "
	resolved := config.DefaultPresentations("").Agents
	resolve := func(candidates []source.Candidate) {
		p := &source.Presentation{Icon: sourceIcon, IconColor: resolved.IconColor, Label: resolved.Label, Detail: resolved.Detail, Marker: resolved.Marker}
		for i := range candidates {
			candidates[i].Presentation = p
		}
	}
	snap1 := agentsRefreshFixture(
		source.Pane{ID: "w1:p0", WorkspaceID: "w1", TabID: "w1:t1", CWD: "/srv/ws1/plain"},
	)
	snap2 := agentsRefreshFixture(
		source.Pane{ID: "w1:p9", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: "working", ForegroundCWD: "/srv/ws1/a"},
	)
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: snap2}}}

	prodAgents := func(ctx context.Context) SourceResultMsg {
		agents := source.AgentCandidates(snap1)
		resolve(agents)
		return SourceResultMsg{
			Source:             config.SourceAgents,
			Candidates:         agents,
			Snapshot:           &snap1,
			SnapshotDriver:     driver,
			SnapshotSources:    []string{config.SourceAgents},
			AgentPresentations: AgentPresentations(agents),
			Tree:               NewTreeExpanderFromSnapshot(snap1),
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodAgents}, "", nil, context.Background(), Layout{SourceOrder: []string{config.SourceAgents}, Resolve: resolve})
	msg := prodAgents(context.Background())
	msg.producerID = 0
	next, _ := m.Update(msg)
	m = next.(Model)

	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	refreshCmd := m.applyFilter()
	if refreshCmd == nil {
		t.Fatal("expired generation did not schedule a snapshot refresh")
	}
	next, _ = m.Update(refreshCmd())
	m = next.(Model)

	var row *source.Candidate
	for i := range m.baseCandidates {
		c := &m.baseCandidates[i]
		switch c.Source {
		case config.SourceHerdr:
			t.Errorf("agents-only refresh introduced herdr row %+v", c)
		case config.SourceAgents:
			row = c
		}
	}
	if row == nil {
		t.Fatalf("no agents row re-derived from the new generation: %+v", m.baseCandidates)
	}
	if row.Meta["pane_id"] != "w1:p9" {
		t.Errorf("agents row pane_id = %q, want w1:p9", row.Meta["pane_id"])
	}
	if row.Presentation == nil || row.Presentation.Icon != sourceIcon {
		t.Errorf("agents row presentation = %+v, want icon %q", row.Presentation, sourceIcon)
	}
	if got := renderRowLineText(m.renderRowLine(Row{Kind: RowCandidate, Candidate: *row}, false, 50)); !strings.Contains(got, sourceIcon+" "+m.agentStatusIcon("working")) {
		t.Errorf("refreshed agent row = %q, want source icon before status", got)
	}
	m.activeTab = "agents"
	m.applyFilter()
	if len(m.rows) == 0 || m.rows[0].Candidate.Presentation == nil || m.rows[0].Candidate.Presentation.Icon != sourceIcon {
		t.Fatalf("agents view row did not retain the resolved icon after refresh: %+v", m.rows)
	}
	if got := renderRowLineText(m.renderRowLine(m.rows[0], false, 50)); !strings.Contains(got, sourceIcon+" "+m.agentStatusIcon("working")) {
		t.Errorf("agents view row after refresh = %q, want source icon before status", got)
	}
	m.activeTab = "all"
	m.applyFilter()
	if len(m.rows) != 1 || m.rows[0].Candidate.Presentation == nil || m.rows[0].Candidate.Presentation.Icon != sourceIcon {
		t.Fatalf("all view after refresh = %+v, want the resolved icon", m.rows)
	}
}

// allTabFixture streams per-source results into a model with configured tabs
// (all, agents and a tab-only custom source) the way `shep open` does, over
// real directories: projects reports zoxide's directory through a symlink,
// so deduplication has to resolve it on disk under root. dedups counts
// deduplications.
func allTabFixture(t *testing.T, order []string) (m Model, snap source.Snapshot, root string, dedups *int) {
	t.Helper()
	root = t.TempDir()
	for _, dir := range []string{"api", "web", "work"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "api"), filepath.Join(root, "api-link")); err != nil {
		t.Fatal(err)
	}
	snap = source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "api"}},
		Tabs:       []source.Tab{{ID: "w1:t1", WorkspaceID: "w1", Label: "editor", Number: 1, PaneCount: 1}},
		Panes:      []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: filepath.Join(root, "api"), Agent: "claude", AgentStatus: "working"}},
	}
	m = NewModelWithProducers(nil, "", nil, context.Background(), Layout{SourceOrder: order, Tabs: []TabDefinition{
		{ID: "all", Kind: TabAll},
		{ID: "agents", Kind: TabAgents},
		{ID: "review", Kind: TabCustomSource},
	}})
	dedups = new(int)
	m.dedupFn = func(c []source.Candidate) []source.Candidate {
		*dedups++
		return resolver.Dedup(c)
	}
	m, _ = update(t, m, sizeMsg(120, 30))
	results := []SourceResultMsg{
		{Source: config.SourceHerdr, Candidates: source.HerdrCandidates(snap), Tree: NewTreeExpanderFromSnapshot(snap), Snapshot: &snap,
			SnapshotSources: []string{config.SourceHerdr, config.SourceAgents}},
		{Source: config.SourceWorkspaces, Candidates: []source.Candidate{workspaceEntryCandidate("work", filepath.Join(root, "work"))}},
		{Source: config.SourceZoxide, Candidates: []source.Candidate{zoxideCandidate("~/api", filepath.Join(root, "api")), zoxideCandidate("~/web", filepath.Join(root, "web"))}},
		{Source: config.SourceProjects, Candidates: []source.Candidate{projectCandidate("~/api", filepath.Join(root, "api-link"))}},
		// Tab-only: it must neither join all nor suppress zoxide's "~/web".
		{Source: "review", Candidates: []source.Candidate{{Source: "review", Label: "~/web", Path: filepath.Join(root, "web")}}},
		{Source: config.SourceAgents, Candidates: source.AgentCandidates(snap)},
	}
	for _, msg := range results {
		m, _ = update(t, m, msg)
	}
	return m, snap, root, dedups
}

// freshAllTab deduplicates the enabled sources' current results from scratch.
func freshAllTab(m Model) []source.Candidate {
	var enabled []source.Candidate
	for _, name := range m.resolvedSourceOrder() {
		enabled = append(enabled, m.candidatesBySource[name]...)
	}
	return resolver.Dedup(enabled)
}

// TestAllTab_StoredSetMatchesAFreshDedup proves the all tab's stored set is
// exactly what deduplicating the enabled sources would produce, after every
// kind of update that replaces their results: streamed source results, a
// live agent status, and a snapshot refresh, in either source order.
func TestAllTab_StoredSetMatchesAFreshDedup(t *testing.T) {
	t.Parallel()
	enabled := []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects, config.SourceAgents}
	for _, order := range [][]string{enabled, {config.SourceAgents, config.SourceProjects, config.SourceZoxide, config.SourceWorkspaces, config.SourceHerdr}} {
		m, snap, _, _ := allTabFixture(t, order)
		check := func(step string) {
			t.Helper()
			if want := freshAllTab(m); !reflect.DeepEqual(m.allTab, want) {
				t.Fatalf("order %v, after %s: stored all tab\n%+v\nwant a fresh dedup\n%+v", order, step, m.allTab, want)
			}
		}
		check("source results")
		for _, c := range m.allTab {
			if c.Source == "review" {
				t.Fatalf("order %v: the tab-only source joined all: %+v", order, c)
			}
		}
		if survivor := m.allTab[slices.IndexFunc(m.allTab, func(c source.Candidate) bool { return c.Label == "~/api" })]; survivor.Source != order[slices.IndexFunc(order, func(s string) bool { return s == config.SourceZoxide || s == config.SourceProjects })] {
			t.Errorf("order %v: the symlinked duplicate kept %s, want the first enabled source's", order, survivor.Source)
		}

		m, _ = update(t, m, paneStatusMsg{PaneID: "p1", WorkspaceID: "w1", TabID: "w1:t1", Status: "blocked"})
		check("a live agent status")
		agent := slices.IndexFunc(m.allTab, func(c source.Candidate) bool { return c.Source == config.SourceAgents })
		if agent < 0 || m.allTab[agent].Meta["agent_status"] != "blocked" {
			t.Errorf("order %v: the stored agent row missed the live status", order)
		}

		refreshed := snap
		refreshed.Workspaces = append(slices.Clone(snap.Workspaces), source.Workspace{ID: "w2", Label: "web"})
		refreshed.Tabs = append(slices.Clone(snap.Tabs), source.Tab{ID: "w2:t1", WorkspaceID: "w2", Label: "shell", Number: 1, PaneCount: 1})
		refreshed.Panes = append(slices.Clone(snap.Panes), source.Pane{ID: "p2", WorkspaceID: "w2", TabID: "w2:t1", CWD: m.candidatesBySource[config.SourceZoxide][1].Path, Agent: "codex", AgentStatus: "idle"})
		m, _ = update(t, m, resolvedGeneration(m.snapshotSeq, refreshed, nil))
		check("a snapshot refresh")
		if !slices.ContainsFunc(m.allTab, func(c source.Candidate) bool { return c.Meta["workspace_id"] == "w2" }) {
			t.Errorf("order %v: the refreshed workspace is missing from all", order)
		}
	}
}

// TestView_ConfiguredTabsNeverDeduplicates proves neither a frame nor a
// keystroke deduplicates with configured tabs, while the prompt still counts
// the deduplicated all tab. The directories are deleted before the frames:
// a frame that resolved paths on disk again would no longer collapse the
// symlinked duplicate and would count one more candidate.
func TestView_ConfiguredTabsNeverDeduplicates(t *testing.T) {
	t.Parallel()
	m, _, root, dedups := allTabFixture(t, nil)
	if *dedups == 0 {
		t.Fatal("setup: source results must deduplicate through the seam")
	}
	want := len(freshAllTab(m))
	if total := m.resultCount().total; total != want {
		t.Fatalf("all tab total = %d, want the deduplicated %d", total, want)
	}
	if !strings.Contains(promptText(m), strconv.Itoa(want)) {
		t.Errorf("prompt %q does not count the %d deduplicated candidates", promptText(m), want)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	before := *dedups
	for _, msg := range []tea.Msg{
		spinner.TickMsg{ID: m.spinner.ID()}, key("a"), key("p"), key("backspace"), key("down"),
		key("ctrl+t"), key("ctrl+t"), key("ctrl+t"), key("esc"),
	} {
		m, _ = update(t, m, msg)
		_ = m.View()
	}
	if *dedups != before {
		t.Errorf("frames and keystrokes deduplicated %d times, want none", *dedups-before)
	}
	if m.ActiveTab() != "all" || m.resultCount().total != want || !strings.Contains(promptText(m), strconv.Itoa(want)) {
		t.Errorf("back on %q the total is %d (prompt %q), want %d", m.ActiveTab(), m.resultCount().total, promptText(m), want)
	}
}
