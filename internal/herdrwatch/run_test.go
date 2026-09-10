package herdrwatch_test

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/herdrwatch"
)

// scriptedDialer returns a sequence of stream connections (or errors) on
// successive dials, letting tests drive reconnect behavior deterministically.
type scriptedDialer struct {
	mu    sync.Mutex
	conns []net.Conn
	errs  []error
	calls int
}

func (d *scriptedDialer) Dial(ctx context.Context) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.calls
	d.calls++
	if i < len(d.errs) && d.errs[i] != nil {
		return nil, d.errs[i]
	}
	if i < len(d.conns) {
		return d.conns[i], nil
	}
	return nil, errors.New("no more scripted dials")
}

func (d *scriptedDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// fakeWaiter records reconnect backoff durations and returns immediately so no
// real 30s wait happens in tests. It honors ctx cancellation.
type fakeWaiter struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (w *fakeWaiter) Wait(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	w.waits = append(w.waits, d)
	w.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (w *fakeWaiter) total() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	var t time.Duration
	for _, d := range w.waits {
		t += d
	}
	return t
}

func testRunConfig(t *testing.T, dialer herdrwatch.StreamDialer) herdrwatch.Config {
	t.Helper()
	dir := shortTempDir(t)
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}
	return herdrwatch.Config{
		SessionKey:  "run-session-key",
		LockPath:    filepath.Join(dir, "own.lock"),
		ControlPath: filepath.Join(dir, "c.sock"),
		Store:       store,
		Snapshotter: snap,
		Dialer:      dialer,
	}
}

func TestRun_CancellationReleasesResources(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()

	go func() {
		serveSubscribeAck(t, srv, `{"id":"sub","result":{"type":"subscription_started"}}`)
		// Hold open; block until the client side is closed.
		io.Copy(io.Discard, srv)
	}()

	dialer := &scriptedDialer{conns: []net.Conn{cli}}
	cfg := testRunConfig(t, dialer)

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- herdrwatch.Run(ctx, cfg)
	}()

	// Give Run time to acquire ownership and subscribe.
	time.Sleep(100 * time.Millisecond)

	cancel()

	select {
	case err := <-runErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("expected clean nil/context.Canceled on cancel, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not exit within 3s after cancel")
	}

	// After Run exits, the flock is released: a new owner must acquire it.
	own, err := herdrwatch.AcquireOwnership(cfg.LockPath)
	if err != nil {
		t.Fatalf("expected lock released after Run exit, got: %v", err)
	}
	own.Release()
}

func TestRun_SecondOwnerNoOpWithoutClearingIncumbent(t *testing.T) {
	dir := shortTempDir(t)
	lockPath := filepath.Join(dir, "own.lock")

	// First owner holds the flock.
	incumbent, err := herdrwatch.AcquireOwnership(lockPath)
	if err != nil {
		t.Fatalf("incumbent AcquireOwnership failed: %v", err)
	}
	defer incumbent.Release()

	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}
	cfg := herdrwatch.Config{
		SessionKey:  "run-session-key",
		LockPath:    lockPath,
		ControlPath: filepath.Join(dir, "c.sock"),
		Store:       store,
		Snapshotter: snap,
		Dialer:      &scriptedDialer{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Second owner must return a no-op / already-running signal without
	// clearing the incumbent, not block forever or hijack the lock.
	err = herdrwatch.Run(ctx, cfg)
	if err == nil {
		t.Fatalf("expected second owner to fail to acquire lock, got nil")
	}
	if !errors.Is(err, herdrwatch.ErrAlreadyRunning) {
		t.Errorf("expected ErrAlreadyRunning, got: %v", err)
	}

	// Incumbent still holds the lock.
	if second, err := herdrwatch.AcquireOwnership(lockPath); err == nil {
		second.Release()
		t.Errorf("incumbent lock was cleared by second owner")
	}
}

func TestRun_ReconnectExhaustionReturnsClassifiedError(t *testing.T) {
	dir := shortTempDir(t)
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}

	// Every dial fails, forcing bounded reconnect then exhaustion.
	failing := make([]error, 40)
	for i := range failing {
		failing[i] = errors.New("dial refused")
	}
	dialer := &scriptedDialer{errs: failing}
	waiter := &fakeWaiter{}

	cfg := herdrwatch.Config{
		SessionKey:  "run-session-key",
		LockPath:    filepath.Join(dir, "own.lock"),
		ControlPath: filepath.Join(dir, "c.sock"),
		Store:       store,
		Snapshotter: snap,
		Dialer:      dialer,
		Waiter:      waiter,
	}

	ctx := context.Background()
	err := herdrwatch.Run(ctx, cfg)
	if !errors.Is(err, herdrwatch.ErrReconnectExhausted) {
		t.Fatalf("expected ErrReconnectExhausted, got: %v", err)
	}

	// Exhaustion must NOT be reported as a clean exit 0 / host-death.
	// Bounded reconnect: total backoff must not exceed the 30s cap.
	if waiter.total() > 30*time.Second {
		t.Errorf("total backoff %v exceeds 30s cap", waiter.total())
	}
	// At least one reconnect attempt (dial) happened.
	if dialer.callCount() < 2 {
		t.Errorf("expected multiple reconnect dials, got %d", dialer.callCount())
	}
}

func TestRun_BackoffProgressionBounded(t *testing.T) {
	dir := shortTempDir(t)
	store := newTestStore(t)
	snap := &fakeSnapshotter{members: map[string]bool{"ws-a": true}, current: "ws-a"}

	failing := make([]error, 20)
	for i := range failing {
		failing[i] = errors.New("dial refused")
	}
	dialer := &scriptedDialer{errs: failing}
	waiter := &fakeWaiter{}

	cfg := herdrwatch.Config{
		SessionKey:  "run-session-key",
		LockPath:    filepath.Join(dir, "own.lock"),
		ControlPath: filepath.Join(dir, "c.sock"),
		Store:       store,
		Snapshotter: snap,
		Dialer:      dialer,
		Waiter:      waiter,
	}

	_ = herdrwatch.Run(context.Background(), cfg)

	waiter.mu.Lock()
	defer waiter.mu.Unlock()
	// First backoff starts at 100ms and each is capped at 5s.
	if len(waiter.waits) == 0 {
		t.Fatalf("expected at least one backoff wait")
	}
	if waiter.waits[0] != 100*time.Millisecond {
		t.Errorf("expected first backoff 100ms, got %v", waiter.waits[0])
	}
	for i, d := range waiter.waits {
		if d > 5*time.Second {
			t.Errorf("backoff[%d]=%v exceeds 5s per-attempt cap", i, d)
		}
	}
}
