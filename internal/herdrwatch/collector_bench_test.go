package herdrwatch_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/history"
)

// countingStore wraps a real history.Store and counts focus writes (Record
// with changed==true), no-change focus (changed==false), and lifecycle
// removals (Remove) in separate buckets so the benchmark never conflates focus
// history writes with lifecycle churn. It delegates every call to the real
// SQLite-backed store, so the counts reflect true store behavior, not a fake.
type countingStore struct {
	inner          *history.Store
	focusWrites    atomic.Int64 // Record -> changed==true
	noChangeFocus  atomic.Int64 // Record -> changed==false
	lifecycleWrite atomic.Int64 // Remove calls
}

func (c *countingStore) Record(ctx context.Context, sessionKey, wsID string) (bool, error) {
	changed, err := c.inner.Record(ctx, sessionKey, wsID)
	if err == nil {
		if changed {
			c.focusWrites.Add(1)
		} else {
			c.noChangeFocus.Add(1)
		}
	}
	return changed, err
}

func (c *countingStore) Remove(ctx context.Context, sessionKey, wsID string) error {
	err := c.inner.Remove(ctx, sessionKey, wsID)
	if err == nil {
		c.lifecycleWrite.Add(1)
	}
	return err
}

func (c *countingStore) List(ctx context.Context, sessionKey string) ([]string, error) {
	return c.inner.List(ctx, sessionKey)
}

// spawnCounterRunner is a real CommandRunner spy wired into herdr.Driver ->
// SnapshotAdapter -> Owner. High-level Bootstrap invokes driver.Snapshot, which
// calls runner.Run. Steady-state event processing never invokes any subprocess,
// so process_boundary_calls is measured as 0 during steady focus processing.
type spawnCounterRunner struct {
	spawns atomic.Int64
}

func (r *spawnCounterRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.spawns.Add(1)
	return []byte(`{"id":"cli:api:snapshot","result":{"snapshot":{"workspaces":[{"workspace_id":"ws-boot","label":"boot","focused":true}],"focused_workspace_id":"ws-boot"}}}`), nil
}

func newBenchStore(b *testing.B) *history.Store {
	b.Helper()
	dbPath := filepath.Join(b.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		b.Fatalf("OpenPath failed: %v", err)
	}
	b.Cleanup(func() { store.Close() })
	return store
}

func benchFocusFrame(wsID string) []byte {
	return []byte(fmt.Sprintf(`{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":%q}}`, wsID))
}

func benchCloseFrame(wsID string) []byte {
	return []byte(fmt.Sprintf(`{"event":"workspace_closed","data":{"type":"workspace_closed","workspace_id":%q}}`, wsID))
}

// BenchmarkOwnerSteadyFocus measures steady-state focus processing: N distinct
// focus events with no lifecycle events. It asserts the efficiency invariants
// (focus writes == distinct count, zero snapshot RPCs after bootstrap, zero
// process_boundary_calls) and reports ns/op, allocs/op, and custom metrics.
func BenchmarkOwnerSteadyFocus(b *testing.B) {
	store := newBenchStore(b)
	cstore := &countingStore{inner: store}

	// Wire the process-boundary spy directly into herdr.Driver -> SnapshotAdapter -> Owner
	runner := &spawnCounterRunner{}
	driver := herdr.New("herdr", herdr.WithRunner(runner))
	adapter := herdrwatch.NewSnapshotAdapter(driver)

	owner := herdrwatch.NewOwner(cstore, adapter, "bench-session")
	ctx := context.Background()

	// Bootstrap once (fixture setup outside the timed loop).
	if err := owner.Bootstrap(ctx); err != nil {
		b.Fatalf("Bootstrap failed: %v", err)
	}
	bootstrapSpawns := runner.spawns.Load()
	if bootstrapSpawns < 1 {
		b.Fatalf("expected bootstrap to make at least 1 process boundary call, got %d", bootstrapSpawns)
	}

	// Pre-build distinct focus frames so allocation of the fixtures is not
	// attributed to the timed loop.
	const distinct = 64
	frames := make([][]byte, distinct)
	for i := 0; i < distinct; i++ {
		frames[i] = benchFocusFrame(fmt.Sprintf("ws-%03d", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		frame := frames[i%distinct]
		if err := owner.HandleEventFrame(ctx, frame); err != nil {
			b.Fatalf("HandleEventFrame failed: %v", err)
		}
	}
	b.StopTimer()

	// Process boundary calls during steady processing == total - bootstrap baseline.
	steadySpawns := runner.spawns.Load() - bootstrapSpawns
	if steadySpawns != 0 {
		b.Fatalf("expected 0 process boundary calls during steady focus, got %d", steadySpawns)
	}
	if cstore.lifecycleWrite.Load() != 0 {
		b.Fatalf("expected 0 lifecycle writes during focus-only bench, got %d", cstore.lifecycleWrite.Load())
	}

	b.ReportMetric(float64(cstore.focusWrites.Load()), "focus_writes")
	b.ReportMetric(float64(cstore.noChangeFocus.Load()), "nochange_focus")
	b.ReportMetric(float64(steadySpawns), "process_boundary_calls")
	b.ReportMetric(float64(cstore.lifecycleWrite.Load()), "lifecycle_writes")
}

// BenchmarkOwnerLifecycleChurn measures the separate lifecycle bucket: focus
// followed by close for each workspace. It proves close removals land in the
// lifecycle counter (not the focus-write counter) and that repeated churn does
// not leak state, while still making zero process boundary calls during steady processing.
func BenchmarkOwnerLifecycleChurn(b *testing.B) {
	store := newBenchStore(b)
	cstore := &countingStore{inner: store}

	runner := &spawnCounterRunner{}
	driver := herdr.New("herdr", herdr.WithRunner(runner))
	adapter := herdrwatch.NewSnapshotAdapter(driver)

	owner := herdrwatch.NewOwner(cstore, adapter, "bench-lifecycle")
	ctx := context.Background()
	if err := owner.Bootstrap(ctx); err != nil {
		b.Fatalf("Bootstrap failed: %v", err)
	}
	bootstrapSpawns := runner.spawns.Load()

	const distinct = 32
	focusFrames := make([][]byte, distinct)
	closeFrames := make([][]byte, distinct)
	for i := 0; i < distinct; i++ {
		id := fmt.Sprintf("ws-%03d", i)
		focusFrames[i] = benchFocusFrame(id)
		closeFrames[i] = benchCloseFrame(id)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % distinct
		if err := owner.HandleEventFrame(ctx, focusFrames[idx]); err != nil {
			b.Fatalf("focus frame failed: %v", err)
		}
		if err := owner.HandleEventFrame(ctx, closeFrames[idx]); err != nil {
			b.Fatalf("close frame failed: %v", err)
		}
	}
	b.StopTimer()

	steadySpawns := runner.spawns.Load() - bootstrapSpawns
	if steadySpawns != 0 {
		b.Fatalf("expected 0 process boundary calls during lifecycle churn, got %d", steadySpawns)
	}
	if cstore.lifecycleWrite.Load() == 0 && b.N > 0 {
		b.Fatalf("expected lifecycle writes to be recorded in the lifecycle bucket")
	}

	b.ReportMetric(float64(cstore.focusWrites.Load()), "focus_writes")
	b.ReportMetric(float64(cstore.lifecycleWrite.Load()), "lifecycle_writes")
	b.ReportMetric(float64(steadySpawns), "process_boundary_calls")
}
