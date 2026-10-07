package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// TestModel_AsyncLoader_FirstFrameAvailableBeforeBlockedProducersComplete proves
// that NewModelWithProducers immediately returns a renderable model in loading state,
// and View() succeeds without waiting on background producers.
func TestModel_AsyncLoader_FirstFrameAvailableBeforeBlockedProducersComplete(t *testing.T) {
	t.Parallel()

	unblockA := make(chan struct{})
	unblockB := make(chan struct{})
	defer close(unblockA)
	defer close(unblockB)

	prodA := func(ctx context.Context) SourceResultMsg {
		<-unblockA
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "ws-1", Source: config.SourceWorkspaces},
			},
		}
	}
	prodB := func(ctx context.Context) SourceResultMsg {
		<-unblockB
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "proj-1", Source: config.SourceProjects},
			},
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodA, prodB}, "", nil, context.Background(), Layout{})
	if !m.loadingCandidates {
		t.Fatal("expected loadingCandidates to be true while producers are pending")
	}

	// First frame is rendered immediately without blocking
	view := m.View().Content
	if strings.Contains(view, "[/]") {
		t.Errorf("view = %q, want no [/] search keycap", view)
	}
	if !strings.Contains(view, "❯") {
		t.Errorf("view = %q, want it to contain the search prompt ❯", view)
	}
	if !strings.Contains(view, "loading") {
		t.Errorf("view = %q, want it to contain loading indicator", view)
	}
}

// TestModel_AsyncLoader_FastProducerAppearsWhileOthersBlocked proves that as soon as
// one producer completes, its candidates are visible in the TUI even while other producers
// remain blocked.
func TestModel_AsyncLoader_FastProducerAppearsWhileOthersBlocked(t *testing.T) {
	t.Parallel()

	unblockSlow := make(chan struct{})
	defer close(unblockSlow)

	prodFast := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "fast-workspace", Path: "/path/fast", Source: config.SourceWorkspaces},
			},
		}
	}
	prodSlow := func(ctx context.Context) SourceResultMsg {
		<-unblockSlow
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "slow-project", Path: "/path/slow", Source: config.SourceProjects},
			},
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodFast, prodSlow}, "", nil, context.Background(), Layout{})

	// Deliver fast producer result
	msgFast := prodFast(context.Background())
	msgFast.producerID = 0
	next, _ := m.Update(msgFast)
	m = next.(Model)

	// Fast candidate is immediately visible
	if len(m.rows) != 1 {
		t.Fatalf("expected 1 visible row from fast producer, got %d", len(m.rows))
	}
	if m.rows[0].Candidate.Label != "fast-workspace" {
		t.Errorf("visible row = %q, want 'fast-workspace'", m.rows[0].Candidate.Label)
	}
	// Model still reports loading because slow producer is pending
	if !m.loadingCandidates {
		t.Fatal("expected loadingCandidates to remain true while slow producer is pending")
	}

	// The prompt count keeps counting while the slow producer streams, with
	// the shared spinner frame as the loading indicator.
	if c := m.resultCount(); !c.loading || c.total != 1 {
		t.Errorf("result count = %+v, want a loading tally of 1", c)
	}
	if prompt := m.renderPromptRow(m.geometry().ListWidth); !strings.Contains(prompt, m.spinner.View()+" ") {
		t.Errorf("prompt row = %q, want the spinner frame before the count", prompt)
	}
}

// TestModel_AsyncLoader_QueryKeystrokesRetainedAcrossIncrementalArrivals proves
// that keystrokes typed before and between producer arrivals remain in m.query, and
// all incoming candidates are progressively filtered against that query.
func TestModel_AsyncLoader_QueryKeystrokesRetainedAcrossIncrementalArrivals(t *testing.T) {
	t.Parallel()

	prod1 := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "alpha-ws", Path: "/path/aws", Source: config.SourceWorkspaces},
				{Label: "beta-ws", Path: "/path/bws", Source: config.SourceWorkspaces},
			},
		}
	}
	prod2 := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "beta-proj", Path: "/path/bproj", Source: config.SourceProjects},
				{Label: "gamma-proj", Path: "/path/gproj", Source: config.SourceProjects},
			},
		}
	}

	m := NewModelWithProducers([]SourceProducer{prod1, prod2}, "", nil, context.Background(), Layout{})

	// Type 'b', 'e' before any producer finishes
	for _, ch := range []string{"b", "e"} {
		next, _ := m.Update(key(ch))
		m = next.(Model)
	}

	if m.query != "be" {
		t.Fatalf("query = %q, want 'be'", m.query)
	}

	// Producer 1 delivers
	msg1 := prod1(context.Background())
	msg1.producerID = 0
	next, _ := m.Update(msg1)
	m = next.(Model)

	if m.query != "be" {
		t.Fatalf("query changed after prod1: got %q, want 'be'", m.query)
	}
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "beta-ws" {
		t.Fatalf("expected 1 match ('beta-ws'), got %+v", m.rows)
	}

	// Type 't' -> query becomes 'bet'
	next, _ = m.Update(key("t"))
	m = next.(Model)

	if m.query != "bet" {
		t.Fatalf("query = %q, want 'bet'", m.query)
	}

	// Producer 2 delivers
	msg2 := prod2(context.Background())
	msg2.producerID = 1
	next, _ = m.Update(msg2)
	m = next.(Model)

	if m.query != "bet" {
		t.Fatalf("query changed after prod2: got %q, want 'bet'", m.query)
	}
	if len(m.rows) != 2 {
		t.Fatalf("expected 2 matches ('beta-ws', 'beta-proj'), got %d: %+v", len(m.rows), m.rows)
	}
	if m.rows[0].Candidate.Label != "beta-ws" || m.rows[1].Candidate.Label != "beta-proj" {
		t.Errorf("unexpected matches: %+v", m.rows)
	}
	if m.loadingCandidates {
		t.Fatal("expected loadingCandidates to be false after all producers finish")
	}
}

// TestModel_AsyncLoader_OutOfOrderCompletionPreservesCanonicalSourceOrder proves
// that even if a lower-priority producer completes before a higher-priority one,
// candidates are rendered in the canonical configured source order.
func TestModel_AsyncLoader_OutOfOrderCompletionPreservesCanonicalSourceOrder(t *testing.T) {
	t.Parallel()

	prodWorkspaces := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "workspace-1", Path: "/path/ws1", Source: config.SourceWorkspaces},
			},
		}
	}
	prodProjects := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "project-1", Path: "/path/p1", Source: config.SourceProjects},
			},
		}
	}

	layout := Layout{
		SourceOrder: []string{config.SourceWorkspaces, config.SourceProjects},
	}
	m := NewModelWithProducers([]SourceProducer{prodWorkspaces, prodProjects}, "", nil, context.Background(), layout)

	// Out of order: Projects finishes FIRST
	msgProjects := prodProjects(context.Background())
	msgProjects.producerID = 1
	next, _ := m.Update(msgProjects)
	m = next.(Model)

	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "project-1" {
		t.Fatalf("expected project-1 visible first, got %+v", m.rows)
	}

	// Workspaces finishes SECOND
	msgWorkspaces := prodWorkspaces(context.Background())
	msgWorkspaces.producerID = 0
	next, _ = m.Update(msgWorkspaces)
	m = next.(Model)

	if len(m.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(m.rows))
	}
	// Workspaces must precede Projects because SourceOrder = [workspaces, projects]
	if m.rows[0].Candidate.Label != "workspace-1" || m.rows[1].Candidate.Label != "project-1" {
		t.Errorf("expected canonical source order [workspace-1, project-1], got [%s, %s]",
			m.rows[0].Candidate.Label, m.rows[1].Candidate.Label)
	}
}

// TestModel_AsyncLoader_RankingLateArrivalReordersWithoutChangingSelectedIdentity proves
// that when ranking data arrives after candidates, candidates are reordered by ranking
// while retaining selection on the user's highlighted candidate by stable identity.
func TestModel_AsyncLoader_RankingLateArrivalReordersWithoutChangingSelectedIdentity(t *testing.T) {
	t.Parallel()

	cand1 := source.Candidate{Label: "proj-1", Path: "/path/1", Source: config.SourceProjects}
	cand2 := source.Candidate{Label: "proj-2", Path: "/path/2", Source: config.SourceProjects}
	cand3 := source.Candidate{Label: "proj-3", Path: "/path/3", Source: config.SourceProjects}

	prodCandidates := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source:     config.SourceProjects,
			Candidates: []source.Candidate{cand1, cand2, cand3},
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodCandidates}, "", nil, context.Background(), Layout{})

	// Deliver candidates
	msgCands := prodCandidates(context.Background())
	msgCands.producerID = 0
	next, _ := m.Update(msgCands)
	m = next.(Model)

	// Move cursor to proj-2 (index 1)
	next, _ = m.Update(key("down"))
	m = next.(Model)

	cur, ok := m.currentRow()
	if !ok || cur.Candidate.Label != "proj-2" {
		t.Fatalf("expected cursor on proj-2, got %+v", cur)
	}

	// Ranking snapshot arrives late
	rankingSnap := ranking.Snapshot{}
	msgRanking := SourceResultMsg{
		Source:          "ranking",
		RankingSnapshot: &rankingSnap,
	}
	next, _ = m.Update(msgRanking)
	m = next.(Model)

	// Selection must remain on proj-2
	cur, ok = m.currentRow()
	if !ok || cur.Candidate.Label != "proj-2" {
		t.Fatalf("expected selection retained on proj-2 after ranking arrival, got %+v", cur)
	}
}

// TestModel_AsyncLoader_SourceErrorPreservesHealthyRowsAndCompletesLoading proves
// that a failure in one producer does not blank healthy candidates from other producers,
// and loading finishes cleanly when all producers report back.
func TestModel_AsyncLoader_SourceErrorPreservesHealthyRowsAndCompletesLoading(t *testing.T) {
	t.Parallel()

	prodHealthy := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "healthy-ws", Path: "/path/ws", Source: config.SourceWorkspaces},
			},
		}
	}
	prodFailing := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceZoxide,
			Err:    errors.New("zoxide command failed"),
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodHealthy, prodFailing}, "", nil, context.Background(), Layout{})

	// Deliver failing producer
	msgFail := prodFailing(context.Background())
	msgFail.producerID = 1
	next, _ := m.Update(msgFail)
	m = next.(Model)

	// Deliver healthy producer
	msgHealthy := prodHealthy(context.Background())
	msgHealthy.producerID = 0
	next, _ = m.Update(msgHealthy)
	m = next.(Model)

	if m.loadingCandidates {
		t.Fatal("expected loadingCandidates to be false after all producers finish")
	}
	if len(m.rows) != 1 || m.rows[0].Candidate.Label != "healthy-ws" {
		t.Fatalf("expected healthy row retained, got %+v", m.rows)
	}
	if m.previewErr != "" {
		t.Errorf("previewErr = %q, want empty because healthy candidates exist", m.previewErr)
	}
}

// TestModel_AsyncLoader_AllSourcesErrorSetsPreviewErr proves that when all producers
// fail and no candidates exist, the error is displayed.
func TestModel_AsyncLoader_AllSourcesErrorSetsPreviewErr(t *testing.T) {
	t.Parallel()

	prodFail := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Err:    errors.New("all sources failed"),
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodFail}, "", nil, context.Background(), Layout{})
	msg := prodFail(context.Background())
	msg.producerID = 0
	next, _ := m.Update(msg)
	m = next.(Model)

	if m.loadingCandidates {
		t.Fatal("expected loadingCandidates to be false")
	}
	if m.previewErr != "all sources failed" {
		t.Errorf("previewErr = %q, want 'all sources failed'", m.previewErr)
	}
}

// TestModel_AsyncLoader_CancelWhileLoading proves that Esc cancels cleanly.
func TestModel_AsyncLoader_CancelWhileLoading(t *testing.T) {
	t.Parallel()

	unblock := make(chan struct{})
	defer close(unblock)

	prod := func(ctx context.Context) SourceResultMsg {
		<-unblock
		return SourceResultMsg{Source: config.SourceProjects}
	}

	m := NewModelWithProducers([]SourceProducer{prod}, "", nil, context.Background(), Layout{})
	next, cmd := m.Update(key("esc"))
	m = next.(Model)

	if !m.Cancelled() {
		t.Errorf("Cancelled() = false, want true")
	}
	if cmd == nil {
		t.Errorf("expected tea.Quit cmd on cancel")
	}
}

// TestModel_AsyncLoader_EnterWhileLoadingEmptyIsNoop proves that pressing Enter
// while candidates are still loading empty does nothing.
func TestModel_AsyncLoader_EnterWhileLoadingEmptyIsNoop(t *testing.T) {
	t.Parallel()

	unblock := make(chan struct{})
	defer close(unblock)

	prod := func(ctx context.Context) SourceResultMsg {
		<-unblock
		return SourceResultMsg{Source: config.SourceProjects}
	}

	m := NewModelWithProducers([]SourceProducer{prod}, "", nil, context.Background(), Layout{})
	next, _ := m.Update(key("enter"))
	m = next.(Model)

	if _, ok := m.Selected(); ok {
		t.Errorf("Selected() ok = true, want false while loading with 0 rows")
	}
}

// TestModel_AsyncLoader_HerdrSnapshotAndTreePopulatedIncrementally proves that
// Herdr coherent snapshot and tree expander delivered by Herdr producer are wired into the model.
func TestModel_AsyncLoader_HerdrSnapshotAndTreePopulatedIncrementally(t *testing.T) {
	t.Parallel()

	snap := source.Snapshot{
		FocusedWorkspaceID: "ws1",
		Workspaces: []source.Workspace{
			{ID: "ws1", Label: "herdr-ws", Focused: true},
		},
	}
	tree := NewTreeExpanderFromSnapshot(snap)

	prodHerdr := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceHerdr,
			Candidates: []source.Candidate{
				{Label: "herdr-ws", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "ws1"}},
			},
			Tree:            tree,
			Snapshot:        &snap,
			RankingSnapshot: &ranking.Snapshot{},
		}
	}

	m := NewModelWithProducers([]SourceProducer{prodHerdr}, "", nil, context.Background(), Layout{})
	msg := prodHerdr(context.Background())
	msg.producerID = 0
	next, _ := m.Update(msg)
	m = next.(Model)

	if m.tree == nil {
		t.Fatal("expected m.tree to be non-nil after Herdr producer delivered")
	}
	if len(m.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(m.rows))
	}
	if m.StartupSnapshot() == nil || m.StartupSnapshot().FocusedWorkspaceID != "ws1" {
		t.Errorf("StartupSnapshot = %+v, want focused ws1", m.StartupSnapshot())
	}
}

// TestModel_AsyncLoader_OutOfOrderProducersKeepCursorPinnedToFirstRowWithoutNavigation proves
// that when multiple sources and late ranking arrive without explicit user navigation,
// the cursor remains pinned to row index 0 after every arrival.
func TestModel_AsyncLoader_OutOfOrderProducersKeepCursorPinnedToFirstRowWithoutNavigation(t *testing.T) {
	t.Parallel()

	prodProjects := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "proj-1", Path: "/path/p1", Source: config.SourceProjects},
				{Label: "proj-2", Path: "/path/p2", Source: config.SourceProjects},
			},
		}
	}
	prodWorkspaces := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "ws-1", Path: "/path/ws1", Source: config.SourceWorkspaces},
				{Label: "ws-2", Path: "/path/ws2", Source: config.SourceWorkspaces},
			},
		}
	}
	prodHerdr := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceHerdr,
			Candidates: []source.Candidate{
				{Label: "herdr-1", Path: "/path/h1", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "h1"}},
			},
		}
	}

	layout := Layout{
		SourceOrder: []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceProjects},
	}
	m := NewModelWithProducers([]SourceProducer{prodProjects, prodWorkspaces, prodHerdr}, "", nil, context.Background(), layout)

	// Step 1: Projects arrives first (lowest priority)
	msgProjects := prodProjects(context.Background())
	msgProjects.producerID = 0
	next, _ := m.Update(msgProjects)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after projects arrival: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "proj-1" {
		t.Fatalf("after projects arrival: current row = %+v, want proj-1", cur)
	}

	// Step 2: Workspaces arrives second (higher priority than projects)
	msgWorkspaces := prodWorkspaces(context.Background())
	msgWorkspaces.producerID = 1
	next, _ = m.Update(msgWorkspaces)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after workspaces arrival: cursor = %d, want 0 (pinned to first row)", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "ws-1" {
		t.Fatalf("after workspaces arrival: current row = %+v, want ws-1 (first canonical row)", cur)
	}

	// Step 3: Herdr arrives third (highest priority)
	msgHerdr := prodHerdr(context.Background())
	msgHerdr.producerID = 2
	next, _ = m.Update(msgHerdr)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after herdr arrival: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "herdr-1" {
		t.Fatalf("after herdr arrival: current row = %+v, want herdr-1", cur)
	}

	// Step 4: Late ranking arrival
	rankingSnap := ranking.Snapshot{}
	msgRanking := SourceResultMsg{
		Source:          "ranking",
		RankingSnapshot: &rankingSnap,
	}
	next, _ = m.Update(msgRanking)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after ranking arrival: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "herdr-1" {
		t.Fatalf("after ranking arrival: current row = %+v, want herdr-1", cur)
	}
}

// TestModel_AsyncLoader_QueryTypingKeepsFirstMatchingRowPinnedAsArrivalsOccur proves
// that typing a query without cursor navigation keeps cursor pinned to row 0 as sources arrive.
func TestModel_AsyncLoader_QueryTypingKeepsFirstMatchingRowPinnedAsArrivalsOccur(t *testing.T) {
	t.Parallel()

	prodProjects := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "proj-alpha", Path: "/path/p1", Source: config.SourceProjects},
			},
		}
	}
	prodWorkspaces := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "ws-alpha", Path: "/path/ws1", Source: config.SourceWorkspaces},
			},
		}
	}

	layout := Layout{
		SourceOrder: []string{config.SourceWorkspaces, config.SourceProjects},
	}
	m := NewModelWithProducers([]SourceProducer{prodProjects, prodWorkspaces}, "", nil, context.Background(), layout)

	// User types "alpha" before sources finish
	for _, ch := range []string{"a", "l", "p", "h", "a"} {
		next, _ := m.Update(key(ch))
		m = next.(Model)
	}

	// Projects finishes first
	msgProjects := prodProjects(context.Background())
	msgProjects.producerID = 0
	next, _ := m.Update(msgProjects)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after projects arrival with query: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "proj-alpha" {
		t.Fatalf("after projects arrival: current row = %+v, want proj-alpha", cur)
	}

	// Workspaces finishes second (canonical order puts ws before proj)
	msgWorkspaces := prodWorkspaces(context.Background())
	msgWorkspaces.producerID = 1
	next, _ = m.Update(msgWorkspaces)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after workspaces arrival with query: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "ws-alpha" {
		t.Fatalf("after workspaces arrival: current row = %+v, want ws-alpha", cur)
	}
}

// TestModel_AsyncLoader_ExplicitNavigationRetainsSelectedCandidateIdentityOnLateArrivals proves
// that once the user explicitly navigates (e.g. Up/Down), late producer arrivals retain the
// selected candidate identity across rebuilds.
func TestModel_AsyncLoader_ExplicitNavigationRetainsSelectedCandidateIdentityOnLateArrivals(t *testing.T) {
	t.Parallel()

	prodProjects := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "proj-1", Path: "/path/p1", Source: config.SourceProjects},
				{Label: "proj-2", Path: "/path/p2", Source: config.SourceProjects},
			},
		}
	}
	prodWorkspaces := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "ws-1", Path: "/path/ws1", Source: config.SourceWorkspaces},
			},
		}
	}

	layout := Layout{
		SourceOrder: []string{config.SourceWorkspaces, config.SourceProjects},
	}
	m := NewModelWithProducers([]SourceProducer{prodProjects, prodWorkspaces}, "", nil, context.Background(), layout)

	// Projects finishes first -> rows: [proj-1, proj-2]
	msgProjects := prodProjects(context.Background())
	msgProjects.producerID = 0
	next, _ := m.Update(msgProjects)
	m = next.(Model)

	// User explicitly moves down to proj-2
	next, _ = m.Update(key("down"))
	m = next.(Model)

	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "proj-2" {
		t.Fatalf("expected cursor on proj-2, got %+v", cur)
	}

	// Workspaces finishes second -> rows: [ws-1, proj-1, proj-2]
	msgWorkspaces := prodWorkspaces(context.Background())
	msgWorkspaces.producerID = 1
	next, _ = m.Update(msgWorkspaces)
	m = next.(Model)

	// Selection must remain on proj-2 (which is now index 2)
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "proj-2" {
		t.Fatalf("expected selection retained on proj-2 after workspaces arrived, got %+v (cursor=%d)", cur, m.cursor)
	}
	if m.cursor != 2 {
		t.Fatalf("cursor index = %d, want 2", m.cursor)
	}
}

// TestModel_AsyncLoader_QueryEditAfterExplicitNavigationResetsCursorAndPinsFirstRow proves
// that editing the query after explicit navigation resets cursor to 0 and subsequent arrivals keep cursor pinned to 0.
func TestModel_AsyncLoader_QueryEditAfterExplicitNavigationResetsCursorAndPinsFirstRow(t *testing.T) {
	t.Parallel()

	prodProjects := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceProjects,
			Candidates: []source.Candidate{
				{Label: "alpha-proj-1", Path: "/path/p1", Source: config.SourceProjects},
				{Label: "alpha-proj-2", Path: "/path/p2", Source: config.SourceProjects},
			},
		}
	}
	prodWorkspaces := func(ctx context.Context) SourceResultMsg {
		return SourceResultMsg{
			Source: config.SourceWorkspaces,
			Candidates: []source.Candidate{
				{Label: "alpha-ws-1", Path: "/path/ws1", Source: config.SourceWorkspaces},
			},
		}
	}

	layout := Layout{
		SourceOrder: []string{config.SourceWorkspaces, config.SourceProjects},
	}
	m := NewModelWithProducers([]SourceProducer{prodProjects, prodWorkspaces}, "", nil, context.Background(), layout)

	// Projects finishes first
	msgProjects := prodProjects(context.Background())
	msgProjects.producerID = 0
	next, _ := m.Update(msgProjects)
	m = next.(Model)

	// User navigates down to alpha-proj-2 (index 1)
	next, _ = m.Update(key("down"))
	m = next.(Model)

	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	// User types "alpha" - query edit resets cursor to 0
	for _, ch := range []string{"a", "l", "p", "h", "a"} {
		next, _ = m.Update(key(ch))
		m = next.(Model)
	}

	if m.cursor != 0 {
		t.Fatalf("after typing query: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "alpha-proj-1" {
		t.Fatalf("after typing query: current row = %+v, want alpha-proj-1", cur)
	}

	// Now Workspaces arrives late -> rows: [alpha-ws-1, alpha-proj-1, alpha-proj-2]
	// Since user hasn't explicitly navigated since the query edit, cursor must pin to row 0 (alpha-ws-1)
	msgWorkspaces := prodWorkspaces(context.Background())
	msgWorkspaces.producerID = 1
	next, _ = m.Update(msgWorkspaces)
	m = next.(Model)

	if m.cursor != 0 {
		t.Fatalf("after late arrival without navigation on new query: cursor = %d, want 0", m.cursor)
	}
	if cur, ok := m.currentRow(); !ok || cur.Candidate.Label != "alpha-ws-1" {
		t.Fatalf("current row = %+v, want alpha-ws-1", cur)
	}
}

func TestModel_AsyncLoader_FocusMRU_OrderingAndArrivalOrderInvariance(t *testing.T) {
	t.Parallel()

	candA := source.Candidate{Source: config.SourceHerdr, Label: "ws-a", Meta: map[string]string{"workspace_id": "ws-a"}}
	candB := source.Candidate{Source: config.SourceHerdr, Label: "ws-b", Meta: map[string]string{"workspace_id": "ws-b"}}
	candC := source.Candidate{Source: config.SourceHerdr, Label: "ws-c", Meta: map[string]string{"workspace_id": "ws-c"}}

	snap := source.Snapshot{
		FocusedWorkspaceID: "ws-c",
		Workspaces: []source.Workspace{
			{ID: "ws-a", Label: "ws-a"},
			{ID: "ws-b", Label: "ws-b"},
			{ID: "ws-c", Label: "ws-c"},
		},
	}

	msgHerdr := SourceResultMsg{
		Source:     config.SourceHerdr,
		Candidates: []source.Candidate{candA, candB, candC},
		Snapshot:   &snap,
	}

	rankingSnap := ranking.Snapshot{}.
		WithAdaptiveEnabled(true).
		WithWorkspaceMRU([]string{"ws-c", "ws-b", "ws-a"})

	msgRanking := SourceResultMsg{
		Source:          "ranking",
		RankingSnapshot: &rankingSnap,
	}

	layout := Layout{SourceOrder: []string{config.SourceHerdr}}

	// Case 1: Herdr arrives first, then Ranking
	{
		m := NewModelWithProducers([]SourceProducer{nil, nil}, "", nil, context.Background(), layout)
		msgH := msgHerdr
		msgH.producerID = 0
		next, _ := m.Update(msgH)
		m = next.(Model)

		msgR := msgRanking
		msgR.producerID = 1
		next, _ = m.Update(msgR)
		m = next.(Model)

		if len(m.rows) != 3 {
			t.Fatalf("case 1: got %d rows, want 3", len(m.rows))
		}
		want := []string{"ws-b", "ws-a", "ws-c"}
		for i, label := range want {
			if m.rows[i].Candidate.Label != label {
				t.Errorf("case 1: row[%d] = %q, want %q", i, m.rows[i].Candidate.Label, label)
			}
		}
	}

	// Case 2: Ranking arrives first, then Herdr
	{
		m := NewModelWithProducers([]SourceProducer{nil, nil}, "", nil, context.Background(), layout)
		msgR := msgRanking
		msgR.producerID = 1
		next, _ := m.Update(msgR)
		m = next.(Model)

		msgH := msgHerdr
		msgH.producerID = 0
		next, _ = m.Update(msgH)
		m = next.(Model)

		if len(m.rows) != 3 {
			t.Fatalf("case 2: got %d rows, want 3", len(m.rows))
		}
		want := []string{"ws-b", "ws-a", "ws-c"}
		for i, label := range want {
			if m.rows[i].Candidate.Label != label {
				t.Errorf("case 2: row[%d] = %q, want %q", i, m.rows[i].Candidate.Label, label)
			}
		}
	}
}
