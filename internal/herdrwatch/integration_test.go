package herdrwatch_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/source"
)

// pipeDialer hands Run a single pre-wired stream connection (the client half of
// a net.Pipe) so the full Subscribe->ack->bootstrap->event path runs without a
// real Herdr socket.
type pipeDialer struct {
	conn net.Conn
	used bool
}

func (d *pipeDialer) Dial(ctx context.Context) (net.Conn, error) {
	if d.used {
		// Block until cancellation so Run does not busy-reconnect after the
		// single scripted stream drops.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d.used = true
	return d.conn, nil
}

// blockingWaiter blocks until ctx is cancelled, preventing tight reconnect
// loops during the integration window.
type blockingWaiter struct{}

func (blockingWaiter) Wait(ctx context.Context, d time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestIntegration_RunWireBootstrapFocusControl drives the REAL wire path end to
// end: Run dials the (piped) stream, sends events.subscribe, the fake server
// acks with subscription_started and pushes two focus events; the
// SnapshotAdapter supplies bootstrap membership; then a control client queries
// state over the owned Unix control socket and observes the two focused
// workspaces newest-first. This proves the wired path, not a manual
// HandleEventFrame call.
func TestIntegration_RunWireBootstrapFocusControl(t *testing.T) {
	srv, cli := net.Pipe()

	// Fake Herdr event server: read the subscribe request, ack, push events,
	// then hold the connection open until closed.
	go func() {
		r := bufio.NewReader(srv)
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		io.WriteString(srv, `{"id":"shep-watch-history","result":{"type":"subscription_started"}}`+"\n")
		io.WriteString(srv, `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"ws-a"}}`+"\n")
		io.WriteString(srv, `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"ws-b"}}`+"\n")
		io.Copy(io.Discard, srv) // keep open
	}()

	dir := shortTempDir(t)
	store := newTestStore(t)
	// Real SnapshotAdapter over a fake snapshot source (bootstrap membership).
	adapter := herdrwatch.NewSnapshotAdapter(&countingSnapshotSource{
		snap: source.Snapshot{
			Workspaces:         []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}},
			FocusedWorkspaceID: "ws-a",
		},
	})

	controlPath := filepath.Join(dir, "c.sock")
	cfg := herdrwatch.Config{
		SessionKey:  "integration-session",
		LockPath:    filepath.Join(dir, "own.lock"),
		ControlPath: controlPath,
		Store:       store,
		Snapshotter: adapter,
		Dialer:      &pipeDialer{conn: cli},
		Waiter:      blockingWaiter{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- herdrwatch.Run(ctx, cfg) }()

	// Poll the control socket until the owner reports both workspaces ready.
	var final herdrwatch.StateResp
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", controlPath)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		qctx, qcancel := context.WithTimeout(ctx, time.Second)
		resp, qerr := herdrwatch.QueryState(qctx, conn, "integration-session")
		qcancel()
		conn.Close()
		if qerr == nil && resp.Ready && len(resp.MRU) == 2 {
			final = resp
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !final.Ready {
		t.Fatalf("expected owner ready with 2 observed focuses over the wired path, got %+v", final)
	}
	// Newest-first: ws-b then ws-a.
	if len(final.MRU) != 2 || final.MRU[0] != "ws-b" || final.MRU[1] != "ws-a" {
		t.Errorf("expected MRU [ws-b, ws-a] via wire, got %v", final.MRU)
	}

	cancel()
	select {
	case err := <-runErrCh:
		if err != nil && !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("Run returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not exit after cancel")
	}
	srv.Close()
}
