package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/source"
)

func TestModel_LiveStatusProhibitsFilterRecomputation(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "workspace1", CWD: "/ws1"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "tab1"}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Label: "pane1", AgentStatus: "working"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t1", Label: "pane2", AgentStatus: "idle"},
		},
	}
	tree := NewTreeExpanderFromSnapshot(snapshot)
	cands := source.HerdrCandidates(snapshot)

	m := NewModelWithTree(cands, nil, tree, Layout{})
	m.query = "working"
	m.applyFilter()

	initialRowIDs := make([]string, len(m.rows))
	for i, r := range m.rows {
		initialRowIDs[i] = r.ID
	}
	if len(initialRowIDs) == 0 {
		t.Fatal("expected at least one row matching query 'working'")
	}

	// p2 transitions to working: applyFilter must NOT be invoked, row IDs unchanged.
	updatedModel, _ := m.Update(paneStatusMsg{PaneID: "p2", WorkspaceID: "w1", Status: "working"})
	m2 := updatedModel.(Model)

	if len(m2.rows) != len(initialRowIDs) {
		t.Fatalf("row count changed: got %d, want %d", len(m2.rows), len(initialRowIDs))
	}
	for i, r := range m2.rows {
		if r.ID != initialRowIDs[i] {
			t.Errorf("row[%d] ID changed: got %q, want %q", i, r.ID, initialRowIDs[i])
		}
	}

	// p1 transitions to idle: row IDs must remain unchanged without applyFilter.
	updatedModel2, _ := m2.Update(paneStatusMsg{PaneID: "p1", WorkspaceID: "w1", Status: "idle"})
	m3 := updatedModel2.(Model)

	if len(m3.rows) != len(initialRowIDs) {
		t.Fatalf("row count changed after p1 update: got %d, want %d", len(m3.rows), len(initialRowIDs))
	}
	for i, r := range m3.rows {
		if r.ID != initialRowIDs[i] {
			t.Errorf("row[%d] ID changed: got %q, want %q", i, r.ID, initialRowIDs[i])
		}
	}

	w1, _ := m3.tree.Fetch(context.Background(), "w1")
	for _, p := range w1.Panes {
		if p.ID == "p1" && p.AgentStatus != "idle" {
			t.Errorf("tree p1 AgentStatus = %q, want idle", p.AgentStatus)
		}
		if p.ID == "p2" && p.AgentStatus != "working" {
			t.Errorf("tree p2 AgentStatus = %q, want working", p.AgentStatus)
		}
	}
}

func TestModel_LiveStatusPreservesCursorIdentityAndNoResort(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "ws1", CWD: "/ws1"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "tab1"}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Label: "pane1", AgentStatus: "working"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t1", Label: "pane2", AgentStatus: "idle"},
			{ID: "p3", WorkspaceID: "w1", TabID: "t1", Label: "pane3", AgentStatus: "done"},
		},
	}
	tree := NewTreeExpanderFromSnapshot(snapshot)
	cands := source.HerdrCandidates(snapshot)

	m := NewModelWithTree(cands, nil, tree, Layout{})
	m.query = "pane"
	m.applyFilter()

	p2Idx := -1
	for i, r := range m.rows {
		if r.Kind == RowPane && r.Candidate.Meta["pane_id"] == "p2" {
			p2Idx = i
			break
		}
	}
	if p2Idx < 0 {
		t.Fatal("pane p2 row not found")
	}

	m.cursor = p2Idx
	targetRowID := m.rows[p2Idx].ID

	updatedModel, _ := m.Update(paneStatusMsg{PaneID: "p1", WorkspaceID: "w1", Status: "blocked"})
	m2 := updatedModel.(Model)

	if m2.cursor != p2Idx {
		t.Errorf("cursor index = %d, want %d", m2.cursor, p2Idx)
	}
	if currentID := m2.currentRowID(); currentID != targetRowID {
		t.Errorf("cursor row ID = %q, want %q", currentID, targetRowID)
	}

	for _, r := range m2.rows {
		if r.Kind == RowPane && r.Candidate.Meta["pane_id"] == "p1" {
			if r.Candidate.Meta["agent_status"] != "blocked" {
				t.Errorf("p1 row Meta[agent_status] = %q, want blocked", r.Candidate.Meta["agent_status"])
			}
		}
	}
}

func TestModel_LiveStatusNormalizesUnknownAndDiscardsMalformed(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "ws1", CWD: "/ws1"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "tab1"}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Label: "pane1", AgentStatus: "idle"},
		},
	}
	tree := NewTreeExpanderFromSnapshot(snapshot)
	cands := source.HerdrCandidates(snapshot)

	m := NewModelWithTree(cands, nil, tree, Layout{})
	m.query = "pane"
	m.applyFilter()

	updatedModel, _ := m.Update(paneStatusMsg{PaneID: "p1", WorkspaceID: "w1", Status: "FLYING_DRAGON"})
	m2 := updatedModel.(Model)

	w1, _ := m2.tree.Fetch(context.Background(), "w1")
	if len(w1.Panes) > 0 && w1.Panes[0].AgentStatus != "unknown" {
		t.Errorf("tree pane status = %q, want normalized \"unknown\"", w1.Panes[0].AgentStatus)
	}
	for _, r := range m2.rows {
		if r.Kind == RowPane && r.Candidate.Meta["pane_id"] == "p1" {
			if r.Candidate.Meta["agent_status"] != "unknown" {
				t.Errorf("row Meta[agent_status] = %q, want \"unknown\"", r.Candidate.Meta["agent_status"])
			}
		}
	}

	updatedModel3, _ := m2.Update(paneStatusMsg{PaneID: "", Status: "working"})
	m3 := updatedModel3.(Model)
	if m3.cursor != m2.cursor {
		t.Errorf("cursor mutated on empty paneID: %d vs %d", m3.cursor, m2.cursor)
	}
}

func TestModel_LiveStatusHermeticSocketIntegration(t *testing.T) {
	t.Parallel()

	srv := newFakeUnixServer(t)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn := srv.NextConn(2 * time.Second)
		if conn == nil {
			return
		}
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			return
		}
		ack := `{"id":"shep-live-status","result":{"type":"subscription_started"}}` + "\n"
		_, _ = conn.Write([]byte(ack))
		ev := `{"event":"pane_agent_status_changed","data":{"pane_id":"p1","workspace_id":"w1","tab_id":"t1","agent_status":"working"}}` + "\n"
		_, _ = conn.Write([]byte(ev))
		time.Sleep(50 * time.Millisecond)
	}()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "ws1", CWD: "/ws1"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "tab1"}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Label: "pane1", AgentStatus: "idle"},
		},
	}
	tree := NewTreeExpanderFromSnapshot(snapshot)
	cands := source.HerdrCandidates(snapshot)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ls := startLiveStatus(ctx, NewUnixStatusDialer(srv.sockPath))
	if ls == nil {
		t.Fatal("startLiveStatus returned nil")
	}
	defer func() {
		_ = ls.Close()
		<-serverDone
	}()

	m := NewModelWithTree(cands, nil, tree, Layout{}).WithLiveStatus(ls.Events())
	m.query = "pane"
	m.applyFilter()

	cmd := waitForStatusCmd(ctx, ls.Events())
	if cmd == nil {
		t.Fatal("waitForStatusCmd returned nil")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("status command produced nil msg")
	}
	statusMsg, ok := msg.(paneStatusMsg)
	if !ok || statusMsg.PaneID != "p1" || statusMsg.Status != "working" {
		t.Fatalf("unexpected statusMsg: %+v", msg)
	}

	updated, _ := m.Update(statusMsg)
	m2 := updated.(Model)

	w1, _ := m2.tree.Fetch(ctx, "w1")
	if len(w1.Panes) == 0 || w1.Panes[0].AgentStatus != "working" {
		t.Errorf("pane status not updated in tree: %+v", w1.Panes)
	}
}

func TestRunProgram_TeardownOrdering(t *testing.T) {
	t.Parallel()

	srv := newFakeUnixServer(t)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn := srv.NextConn(2 * time.Second)
		if conn == nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil || n == 0 {
			return
		}
		ack := `{"id":"shep-live-status","result":{"type":"subscription_started"}}` + "\n"
		_, _ = conn.Write([]byte(ack))
		_, _ = conn.Read(buf)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	dialer := NewUnixStatusDialer(srv.sockPath)
	layout := Layout{StatusDialer: dialer}
	cands := []source.Candidate{{Label: "item1", Path: "/path1"}}
	m := NewModel(cands, nil)
	m.layout = layout

	_, _, _, _, err := runProgram(ctx, m, tea.WithInput(strings.NewReader("")), tea.WithoutRenderer())
	if err != nil && !errors.Is(err, ErrCancelled) && !errors.Is(err, context.Canceled) && !errors.Is(err, tea.ErrProgramKilled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("unexpected error from runProgram: %v", err)
	}
	<-serverDone
}
