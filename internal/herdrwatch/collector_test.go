package herdrwatch_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/history"
)

// fakeSnapshotter returns a canned bootstrap membership set. It records how
// many times Snapshot is called so tests can assert no per-event snapshot RPCs.
type fakeSnapshotter struct {
	members    map[string]bool
	current    string
	callCount  int
	failReturn error
}

func (f *fakeSnapshotter) Snapshot(ctx context.Context) (herdrwatch.Membership, error) {
	f.callCount++
	if f.failReturn != nil {
		return herdrwatch.Membership{}, f.failReturn
	}
	present := make([]string, 0, len(f.members))
	for id := range f.members {
		present = append(present, id)
	}
	return herdrwatch.Membership{
		Present:            present,
		FocusedWorkspaceID: f.current,
	}, nil
}

func newTestStore(t *testing.T) *history.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func focusEvent(t *testing.T, wsID string) []byte {
	t.Helper()
	// Herdr 0.8.2 wire spelling: underscored "workspace_focused"
	frame := map[string]any{
		"event": "workspace_focused",
		"data": map[string]any{
			"type":         "workspace_focused",
			"workspace_id": wsID,
		},
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal focus event: %v", err)
	}
	return b
}

func closeEvent(t *testing.T, wsID string) []byte {
	t.Helper()
	frame := map[string]any{
		"event": "workspace_closed",
		"data": map[string]any{
			"type":         "workspace_closed",
			"workspace_id": wsID,
		},
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal close event: %v", err)
	}
	return b
}

func TestOwner_BootstrapThenFocusRecordsHistory(t *testing.T) {
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true, "ws-b": true}, current: "ws-a"}

	owner := herdrwatch.NewOwner(store, snap, "test-session-key")

	ctx := context.Background()

	// Cold start: not ready, epoch 0
	resp, err := owner.State(ctx, "test-session-key")
	if err != nil {
		t.Fatalf("State cold start failed: %v", err)
	}
	if resp.Ready {
		t.Errorf("expected not ready on cold start")
	}

	// Simulate the subscription ack raising the epoch and bootstrap membership
	if err := owner.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	// Two trustworthy observed focus events (ws-a then ws-b)
	if err := owner.HandleEventFrame(ctx, focusEvent(t, "ws-a")); err != nil {
		t.Fatalf("HandleEventFrame ws-a failed: %v", err)
	}
	if err := owner.HandleEventFrame(ctx, focusEvent(t, "ws-b")); err != nil {
		t.Fatalf("HandleEventFrame ws-b failed: %v", err)
	}

	resp, err = owner.State(ctx, "test-session-key")
	if err != nil {
		t.Fatalf("State after focus failed: %v", err)
	}
	if !resp.Ready {
		t.Errorf("expected ready after two observed focus events")
	}
	// MRU newest-first: ws-b then ws-a
	if len(resp.MRU) != 2 || resp.MRU[0] != "ws-b" || resp.MRU[1] != "ws-a" {
		t.Errorf("expected MRU [ws-b, ws-a], got %v", resp.MRU)
	}

	// No per-event Snapshot calls: only bootstrap called it once
	if snap.callCount != 1 {
		t.Errorf("expected exactly 1 Snapshot call (bootstrap), got %d", snap.callCount)
	}
}

func TestOwner_SessionKeyMismatchRejected(t *testing.T) {
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}
	owner := herdrwatch.NewOwner(store, snap, "correct-key")

	ctx := context.Background()
	_ = owner.Bootstrap(ctx)
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))

	resp, err := owner.State(ctx, "WRONG-key")
	if err == nil && resp.Ready {
		t.Errorf("expected session key mismatch to be rejected, got ready response")
	}
}

func TestOwner_StaleCachedMRUNotReadyOnColdStart(t *testing.T) {
	// Pre-populate the store with offline history under the session key.
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	sessionKey := "stale-session"
	store.Record(ctx, sessionKey, "old-offline-ws")

	snap := &fakeSnapshotter{members: map[string]bool{"old-offline-ws": true}, current: "old-offline-ws"}
	owner := herdrwatch.NewOwner(store, snap, sessionKey)

	// Fresh owner, no observed events yet — cached DB MRU must NOT make it ready.
	resp, err := owner.State(ctx, sessionKey)
	if err != nil {
		t.Fatalf("State failed: %v", err)
	}
	if resp.Ready {
		t.Errorf("cached DB MRU must not make a fresh owner ready")
	}
	// The stale offline ID must not leak into the current-epoch MRU.
	for _, id := range resp.MRU {
		if id == "old-offline-ws" {
			t.Errorf("stale offline observation leaked into current epoch MRU: %v", resp.MRU)
		}
	}
}

func TestOwner_CloseRemovesFromHistory(t *testing.T) {
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true, "ws-b": true}, current: "ws-a"}
	owner := herdrwatch.NewOwner(store, snap, "sess")

	ctx := context.Background()
	_ = owner.Bootstrap(ctx)
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-b"))

	// Close ws-a: should be removed from history and epoch set.
	if err := owner.HandleEventFrame(ctx, closeEvent(t, "ws-a")); err != nil {
		t.Fatalf("HandleEventFrame close ws-a failed: %v", err)
	}

	resp, err := owner.State(ctx, "sess")
	if err != nil {
		t.Fatalf("State failed: %v", err)
	}
	for _, id := range resp.MRU {
		if id == "ws-a" {
			t.Errorf("closed workspace ws-a should be removed from MRU, got %v", resp.MRU)
		}
	}
}

func TestOwner_DuplicateFocusNoDoubleWrite(t *testing.T) {
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}
	owner := herdrwatch.NewOwner(store, snap, "sess")

	ctx := context.Background()
	_ = owner.Bootstrap(ctx)

	// Repeated focus of the same workspace at head should not grow MRU.
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))

	resp, err := owner.State(ctx, "sess")
	if err != nil {
		t.Fatalf("State failed: %v", err)
	}
	if len(resp.MRU) != 1 || resp.MRU[0] != "ws-a" {
		t.Errorf("duplicate focus should collapse to single MRU entry, got %v", resp.MRU)
	}
}

func TestOwner_FocusForWorkspaceCreatedAfterBootstrap(t *testing.T) {
	store := newTestStore(t)
	// Bootstrap membership does NOT include ws-new.
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}
	owner := herdrwatch.NewOwner(store, snap, "sess")

	ctx := context.Background()
	_ = owner.Bootstrap(ctx)
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))

	// A valid focus for a workspace created AFTER bootstrap must enter history
	// WITHOUT any per-event Snapshot call.
	before := snap.callCount
	if err := owner.HandleEventFrame(ctx, focusEvent(t, "ws-new")); err != nil {
		t.Fatalf("HandleEventFrame ws-new failed: %v", err)
	}
	if snap.callCount != before {
		t.Errorf("post-bootstrap focus must not trigger a Snapshot call, got %d extra", snap.callCount-before)
	}

	resp, _ := owner.State(ctx, "sess")
	if len(resp.MRU) == 0 || resp.MRU[0] != "ws-new" {
		t.Errorf("expected ws-new at MRU head, got %v", resp.MRU)
	}
}

func TestOwner_ContinuityLossInvalidatesReadiness(t *testing.T) {
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true, "ws-b": true}, current: "ws-a"}
	owner := herdrwatch.NewOwner(store, snap, "sess")

	ctx := context.Background()
	_ = owner.Bootstrap(ctx)
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-a"))
	_ = owner.HandleEventFrame(ctx, focusEvent(t, "ws-b"))

	resp, _ := owner.State(ctx, "sess")
	if !resp.Ready {
		t.Fatalf("precondition: expected ready before continuity loss")
	}

	// Continuity loss (stream disconnect) must invalidate readiness and bump epoch.
	owner.InvalidateReadiness()

	resp, _ = owner.State(ctx, "sess")
	if resp.Ready {
		t.Errorf("expected not ready after continuity loss")
	}
}
