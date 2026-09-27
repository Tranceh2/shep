package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
