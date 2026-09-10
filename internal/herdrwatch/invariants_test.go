package herdrwatch_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/herdrwatch"
)

// instrumentedStore counts true store transitions over an in-memory MRU model
// with the same 50-cap and newest-first semantics as the real store, so the
// invariant tests assert real counts without a fake always-zero counter.
type instrumentedStore struct {
	mu             chan struct{}
	mru            []string
	focusWrites    atomic.Int64
	noChangeFocus  atomic.Int64
	lifecycleWrite atomic.Int64
}

func newInstrumentedStore() *instrumentedStore {
	s := &instrumentedStore{mu: make(chan struct{}, 1)}
	s.mu <- struct{}{}
	return s
}

func (s *instrumentedStore) lock()   { <-s.mu }
func (s *instrumentedStore) unlock() { s.mu <- struct{}{} }

func (s *instrumentedStore) Record(ctx context.Context, sessionKey, wsID string) (bool, error) {
	s.lock()
	defer s.unlock()
	if len(s.mru) > 0 && s.mru[0] == wsID {
		s.noChangeFocus.Add(1)
		return false, nil
	}
	// Move-to-head with dedupe.
	out := make([]string, 0, len(s.mru)+1)
	out = append(out, wsID)
	for _, id := range s.mru {
		if id != wsID {
			out = append(out, id)
		}
	}
	if len(out) > 50 {
		out = out[:50]
	}
	s.mru = out
	s.focusWrites.Add(1)
	return true, nil
}

func (s *instrumentedStore) Remove(ctx context.Context, sessionKey, wsID string) error {
	s.lock()
	defer s.unlock()
	out := s.mru[:0:0]
	for _, id := range s.mru {
		if id != wsID {
			out = append(out, id)
		}
	}
	s.mru = out
	s.lifecycleWrite.Add(1)
	return nil
}

func (s *instrumentedStore) List(ctx context.Context, sessionKey string) ([]string, error) {
	s.lock()
	defer s.unlock()
	cp := make([]string, len(s.mru))
	copy(cp, s.mru)
	return cp, nil
}

func (s *instrumentedStore) size() int {
	s.lock()
	defer s.unlock()
	return len(s.mru)
}

type noopSnap struct{ calls atomic.Int64 }

func (n *noopSnap) Snapshot(ctx context.Context) (herdrwatch.Membership, error) {
	n.calls.Add(1)
	return herdrwatch.Membership{}, nil
}

func TestInvariant_DistinctFocusWritesTrueDuplicateWritesFalse(t *testing.T) {
	store := newInstrumentedStore()
	snap := &noopSnap{}
	owner := herdrwatch.NewOwner(store, snap, "sess")
	ctx := context.Background()
	if err := owner.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}
	bootstrapSnap := snap.calls.Load()

	// Two distinct focuses => two true writes.
	_ = owner.HandleEventFrame(ctx, benchFocusFrame("ws-a"))
	_ = owner.HandleEventFrame(ctx, benchFocusFrame("ws-b"))
	// Duplicate current focus at head => no-change (false), no new write.
	_ = owner.HandleEventFrame(ctx, benchFocusFrame("ws-b"))

	if store.focusWrites.Load() != 2 {
		t.Errorf("expected 2 distinct focus writes, got %d", store.focusWrites.Load())
	}
	if store.noChangeFocus.Load() != 1 {
		t.Errorf("expected 1 no-change focus, got %d", store.noChangeFocus.Load())
	}
	// Zero snapshot RPCs during steady focus (bootstrap subtracted).
	if steady := snap.calls.Load() - bootstrapSnap; steady != 0 {
		t.Errorf("expected 0 snapshot RPCs during steady focus, got %d", steady)
	}
}

func TestInvariant_MRUBoundedAt50UnderChurn(t *testing.T) {
	store := newInstrumentedStore()
	owner := herdrwatch.NewOwner(store, &noopSnap{}, "sess")
	ctx := context.Background()
	_ = owner.Bootstrap(ctx)

	// Feed 200 distinct focuses; the store MRU must stay bounded at 50.
	for i := 0; i < 200; i++ {
		_ = owner.HandleEventFrame(ctx, benchFocusFrame(fmt.Sprintf("ws-%04d", i)))
	}
	if store.size() != 50 {
		t.Errorf("expected MRU bounded at 50, got %d", store.size())
	}
}

func TestInvariant_CloseChurnDoesNotLeakState(t *testing.T) {
	store := newInstrumentedStore()
	owner := herdrwatch.NewOwner(store, &noopSnap{}, "sess")
	ctx := context.Background()
	_ = owner.Bootstrap(ctx)

	// Focus then close the same set repeatedly; state must not grow unbounded.
	for round := 0; round < 20; round++ {
		for i := 0; i < 10; i++ {
			id := fmt.Sprintf("ws-%02d", i)
			_ = owner.HandleEventFrame(ctx, benchFocusFrame(id))
			_ = owner.HandleEventFrame(ctx, benchCloseFrame(id))
		}
	}
	// After every focus is closed, the store MRU is empty (no leak).
	if store.size() != 0 {
		t.Errorf("expected empty MRU after full close churn, got %d", store.size())
	}
	// Focus writes and lifecycle writes were counted in separate buckets.
	if store.focusWrites.Load() == 0 || store.lifecycleWrite.Load() == 0 {
		t.Errorf("expected both focus and lifecycle buckets populated, got focus=%d lifecycle=%d",
			store.focusWrites.Load(), store.lifecycleWrite.Load())
	}
}

func TestInvariant_StateMRUReturnsImmutableCopy(t *testing.T) {
	store := newInstrumentedStore()
	owner := herdrwatch.NewOwner(store, &noopSnap{}, "sess")
	ctx := context.Background()
	_ = owner.Bootstrap(ctx)
	_ = owner.HandleEventFrame(ctx, benchFocusFrame("ws-a"))
	_ = owner.HandleEventFrame(ctx, benchFocusFrame("ws-b"))

	resp1, err := owner.State(ctx, "sess")
	if err != nil {
		t.Fatalf("State failed: %v", err)
	}
	if len(resp1.MRU) == 0 {
		t.Fatalf("expected non-empty MRU")
	}
	// Mutating the returned slice must not corrupt the owner's next response.
	resp1.MRU[0] = "MUTATED"

	resp2, err := owner.State(ctx, "sess")
	if err != nil {
		t.Fatalf("State second call failed: %v", err)
	}
	for _, id := range resp2.MRU {
		if id == "MUTATED" {
			t.Errorf("owner returned a shared mutable MRU slice; mutation leaked: %v", resp2.MRU)
		}
	}
}

// TestProcessBoundarySpy_PositiveControl proves that the process-boundary spy
// is actually wired through Owner -> SnapshotAdapter -> herdr.Driver -> CommandRunner.
// It asserts:
// 1. Bootstrap calls driver.Snapshot, which executes runner.Run (spawns > 0 baseline).
// 2. Extra Snapshot calls increment spawns.
// 3. Steady HandleEventFrame calls execute 0 extra process boundary calls.
func TestProcessBoundarySpy_PositiveControl(t *testing.T) {
	runner := &spawnCounterRunner{}
	driver := herdr.New("herdr", herdr.WithRunner(runner))
	adapter := herdrwatch.NewSnapshotAdapter(driver)
	store := newInstrumentedStore()

	owner := herdrwatch.NewOwner(store, adapter, "sess-spy")
	ctx := context.Background()

	// Before bootstrap: zero process boundary calls.
	if runner.spawns.Load() != 0 {
		t.Fatalf("expected 0 process boundary calls before bootstrap, got %d", runner.spawns.Load())
	}

	// 1. Bootstrap invokes driver.Snapshot, which calls runner.Run.
	if err := owner.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}
	bootstrapSpawns := runner.spawns.Load()
	if bootstrapSpawns != 1 {
		t.Fatalf("positive control failed: expected bootstrap to invoke runner.Run once (got %d)", bootstrapSpawns)
	}

	// 2. Steady focus processing does NOT invoke runner.Run.
	for i := 0; i < 10; i++ {
		if err := owner.HandleEventFrame(ctx, benchFocusFrame(fmt.Sprintf("ws-%d", i))); err != nil {
			t.Fatalf("HandleEventFrame failed: %v", err)
		}
	}
	steadySpawns := runner.spawns.Load() - bootstrapSpawns
	if steadySpawns != 0 {
		t.Errorf("expected 0 extra process boundary calls during steady focus, got %d", steadySpawns)
	}

	// 3. Positive control check: manually triggering adapter.Snapshot DOES increment spawns.
	if _, err := adapter.Snapshot(ctx); err != nil {
		t.Fatalf("adapter.Snapshot failed: %v", err)
	}
	if runner.spawns.Load() != 2 {
		t.Errorf("positive control check failed: extra adapter.Snapshot should increment spawns to 2, got %d", runner.spawns.Load())
	}
}
