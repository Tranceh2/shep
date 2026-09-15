package herdrwatch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Sentinel errors surfaced by Run.
var (
	// ErrAlreadyRunning means another owner already holds the per-socket flock;
	// this invocation is a no-op and must not disturb the incumbent.
	ErrAlreadyRunning = errors.New("watch-history: collector already running")
	// ErrReconnectExhausted means the bounded reconnect budget was spent
	// without re-establishing the stream. It is a classified non-zero failure,
	// NOT proof of host death, and must never be reported as a clean exit.
	ErrReconnectExhausted = errors.New("watch-history: stream reconnect budget exhausted")
	// These classifications are safe to use for terminal diagnostics without
	// exposing socket paths, workspace ids, command output, or environment.
	ErrSubscribeUnavailable = errors.New("watch-history: subscription unavailable")
	ErrBootstrapUnavailable = errors.New("watch-history: bootstrap snapshot unavailable")
	ErrStreamRead           = errors.New("watch-history: stream read failed")
	ErrStreamHandling       = errors.New("watch-history: stream event handling failed")
)

// Subscribed workspace lifecycle events (DOTTED request spelling).
var subscribedTypes = []string{"workspace.focused", "workspace.closed"}

// Reconnect budget: bounded backoff 100ms -> 5s per attempt, 30s total cap.
const (
	reconnectInitialBackoff = 100 * time.Millisecond
	reconnectMaxBackoff     = 5 * time.Second
	reconnectTotalCap       = 30 * time.Second
)

// StreamDialer opens a dedicated raw connection to the Herdr events socket. A
// seam so tests drive reconnect deterministically without a live daemon.
type StreamDialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// Waiter waits for a backoff duration, honoring context cancellation. A seam so
// tests inject a fake clock and never sleep the real 30s budget.
type Waiter interface {
	Wait(ctx context.Context, d time.Duration) error
}

// realWaiter is the production Waiter backed by a timer.
type realWaiter struct{}

func (realWaiter) Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Config carries the dependencies and paths for a collector Run.
type Config struct {
	SessionKey  string
	LockPath    string
	ControlPath string
	Store       HistoryStore
	Snapshotter Snapshotter
	Dialer      StreamDialer
	// Waiter is optional; a real timer-backed waiter is used when nil.
	Waiter Waiter
}

// Run is the high-level collector owner loop:
//
//	acquire ownership (flock) BEFORE any endpoint cleanup
//	-> prepare + clean the owned control endpoint (only under exclusive ownership)
//	-> listen + serve the control Server
//	-> dial the stream, subscribe, ack, bootstrap (separate Snapshotter)
//	-> read events -> Owner.HandleEventFrame
//	-> on stream loss: invalidate readiness, bounded reconnect
//	-> on ctx cancel: close owned IO, join workers, release flock, remove endpoint
//
// A second invocation while an incumbent holds the flock returns
// ErrAlreadyRunning without disturbing the incumbent. Reconnect exhaustion
// returns ErrReconnectExhausted (classified non-zero, never a clean exit).
func Run(ctx context.Context, cfg Config) error {
	waiter := cfg.Waiter
	if waiter == nil {
		waiter = realWaiter{}
	}

	// 1. Exclusive ownership BEFORE any endpoint cleanup.
	own, err := AcquireOwnership(cfg.LockPath)
	if err != nil {
		return ErrAlreadyRunning
	}
	// Release's own doc contract says the flock is dropped "explicitly via
	// Release or automatically when the process exits" — the OS releases the
	// advisory lock on fd close either way, so a failed explicit Release here
	// cannot leave a stuck lock; not worth clobbering Run's real return value.
	defer func() { _ = own.Release() }()

	// 2. Prepare + clean the owned control endpoint (only under exclusive
	// ownership, never touching an incumbent's endpoint).
	controlPath, err := PrepareControlPath(cfg.ControlPath)
	if err != nil {
		return fmt.Errorf("prepare control path: %w", err)
	}
	if err := CleanStaleEndpoint(own, controlPath); err != nil {
		return fmt.Errorf("clean stale endpoint: %w", err)
	}

	owner := NewOwner(cfg.Store, cfg.Snapshotter, cfg.SessionKey)

	// 3. Listen + serve the control Server. Serve exits when ctx is cancelled.
	listener, err := net.Listen("unix", controlPath)
	if err != nil {
		return fmt.Errorf("listen control socket: %w", err)
	}
	server := NewServer(owner)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(runCtx, listener)
	}()

	// Ensure the owned endpoint is removed on exit (best effort; only ours).
	defer func() {
		_ = CleanStaleEndpoint(own, controlPath)
	}()

	// 4. Stream + reconnect loop.
	streamErr := runStreamLoop(runCtx, cfg, owner, waiter)

	// Stop the control server and join it before returning.
	cancel()
	_ = listener.Close()
	<-serveErrCh

	if streamErr != nil {
		return streamErr
	}
	if err := ctx.Err(); err != nil {
		// Normal parent cancellation is a graceful exit.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// runStreamLoop dials, subscribes, bootstraps, and reads events, reconnecting
// with bounded backoff on stream loss. It fails closed on reconnect exhaustion.
func runStreamLoop(ctx context.Context, cfg Config, owner *Owner, waiter Waiter) error {
	backoff := reconnectInitialBackoff
	var spent time.Duration
	var lastErr error
	var lastStreamErr bool

	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := cfg.Dialer.Dial(ctx)
		if err == nil {
			// A raw dial is not a healthy stream. Only a stream that completed
			// subscribe, bootstrap, and handled at least one event may reset the
			// reconnect budget; bootstrap failures therefore remain bounded.
			streamErr := serveStream(ctx, conn, owner, cfg.Snapshotter)
			_ = conn.Close()
			if ctx.Err() != nil {
				return nil
			}
			owner.InvalidateReadiness()
			if streamErr == nil {
				backoff = reconnectInitialBackoff
				spent = 0
				lastStreamErr = false
			} else {
				err = streamErr
				lastStreamErr = true
			}
		}
		if err != nil {
			// Stream unavailable: readiness invalid, dial/handshake/read again
			// after bounded backoff. Preserve the classified cause on exhaustion.
			lastErr = err
			owner.InvalidateReadiness()
		}

		// Bounded backoff before the next dial; exhaustion fails closed.
		if spent >= reconnectTotalCap {
			if lastStreamErr {
				return fmt.Errorf("%w: %w", ErrReconnectExhausted, lastErr)
			}
			return ErrReconnectExhausted
		}
		wait := backoff
		if spent+wait > reconnectTotalCap {
			wait = reconnectTotalCap - spent
		}
		if werr := waiter.Wait(ctx, wait); werr != nil {
			return nil // ctx cancelled during backoff: graceful.
		}
		spent += wait
		backoff = nextBackoff(backoff)
	}
}

// serveStream subscribes on conn, bootstraps membership once via the
// Snapshotter, then reads events into the owner until the stream drops or ctx
// is cancelled. Close conn to unblock a blocked ReadEvent on cancellation.
func serveStream(ctx context.Context, conn net.Conn, owner *Owner, snap Snapshotter) error {
	stream, err := Subscribe(ctx, conn, subscribedTypes)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSubscribeUnavailable, err)
	}

	buffered := stream.ReadBuffered()
	if err := owner.Bootstrap(ctx, buffered...); err != nil {
		return fmt.Errorf("%w: %w", ErrBootstrapUnavailable, err)
	}

	// Close the stream promptly when ctx is cancelled so a blocked ReadEvent
	// returns instead of leaking.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	for _, raw := range buffered {
		if herr := owner.HandleEventFrame(ctx, raw); herr != nil {
			return fmt.Errorf("%w: %w", ErrStreamHandling, herr)
		}
	}

	for {
		raw, err := stream.ReadEvent()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStreamRead, err)
		}
		if herr := owner.HandleEventFrame(ctx, raw); herr != nil {
			// Malformed frame or store write error: readiness already
			// invalidated by the owner; drop this stream and reconnect rather
			// than retrying the same write forever.
			return fmt.Errorf("%w: %w", ErrStreamHandling, herr)
		}
	}
}

// nextBackoff doubles the backoff, capped at reconnectMaxBackoff.
func nextBackoff(d time.Duration) time.Duration {
	n := d * 2
	if n > reconnectMaxBackoff {
		return reconnectMaxBackoff
	}
	return n
}
