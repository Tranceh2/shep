package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)

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
	m = m.WithScope(ScopeAgents)
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
	if wantPrefix := len([]rune(icon + " ")); prefixRunes != wantPrefix {
		t.Errorf("prefixRunes = %d, want %d", prefixRunes, wantPrefix)
	}

	// Nested tree pane row (Depth: 2) continues to use formats.Pane
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
	if !strings.Contains(nestedPrimary, "/srv/ws1/src") {
		t.Errorf("nested tree pane primary %q should contain path", nestedPrimary)
	}
	if !strings.Contains(nestedPrimary, "·") {
		t.Errorf("nested tree pane primary %q should contain separator \"·\"", nestedPrimary)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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

	// Execute cmd if any to test AckClearer callback
	if cmd != nil {
		_ = cmd()
	}
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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
	m = m.WithScope(ScopeAgents)
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

	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.LabelFormats = LabelFormats{Agents: "{{.Label}} [{{.AgentStatus}}]"}.withDefaults()

	flatPaneRow := Row{
		Kind:      RowPane,
		Depth:     0,
		Action:    RowActionFocusTab,
		Candidate: source.Candidate{Label: "security scan", Path: "/srv/ws1/src", Meta: map[string]string{"kind": "agent", "agent_status": "blocked"}},
	}
	if got := m.renderRowLabel(flatPaneRow); got != "security scan [blocked]" {
		t.Errorf("flat agent row label = %q, want %q", got, "security scan [blocked]")
	}

	allViewRow := Row{
		Kind:      RowCandidate,
		Candidate: source.Candidate{Label: "codegen", Path: "/srv/ws1/src", Source: config.SourceAgents, Meta: map[string]string{"agent_status": "working"}},
	}
	if got := m.renderRowLabel(allViewRow); got != "codegen [working]" {
		t.Errorf("all-view agents row label = %q, want %q", got, "codegen [working]")
	}
}

// TestAgentScope_SnapshotRefreshFillsEmptyAgentsSliceAndKeepsIcon triangulates
// the declaration-driven ownership: a source_order that enables agents while
// the startup generation holds no agent panes must still gain agents rows as
// panes appear later (SnapshotSources, not row presence, owns the slice), and
// the configured agents icon survives every re-derivation. The same message
// shape must not import herdr rows into the agents-only view.
func TestAgentScope_SnapshotRefreshFillsEmptyAgentsSliceAndKeepsIcon(t *testing.T) {
	t.Parallel()

	const agentsIcon = "🤖 "
	snap1 := agentsRefreshFixture(
		source.Pane{ID: "w1:p0", WorkspaceID: "w1", TabID: "w1:t1", CWD: "/srv/ws1/plain"},
	)
	snap2 := agentsRefreshFixture(
		source.Pane{ID: "w1:p9", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: "working", ForegroundCWD: "/srv/ws1/a"},
	)
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: snap2}}}

	prodAgents := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source:          config.SourceAgents,
			Candidates:      source.AgentCandidates(snap1),
			Snapshot:        &snap1,
			SnapshotDriver:  driver,
			SnapshotSources: []string{config.SourceAgents},
			AgentsIcon:      agentsIcon,
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
	if row.Icon != agentsIcon {
		t.Errorf("agents row icon = %q, want %q", row.Icon, agentsIcon)
	}
}
