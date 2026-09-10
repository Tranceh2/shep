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
	"github.com/tranceh2/shep/internal/history"
	"github.com/tranceh2/shep/internal/source"
)

type scriptedServer struct {
	lines []string
	hold  bool
}

func (s scriptedServer) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	if _, err := r.ReadString('\n'); err != nil {
		return
	}
	var buf strings.Builder
	buf.WriteString(`{"id":"shep-watch-history","result":{"type":"subscription_started"}}` + "\n")
	for _, line := range s.lines {
		buf.WriteString(line + "\n")
	}
	if _, err := io.WriteString(conn, buf.String()); err != nil {
		return
	}
	if s.hold {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	_ = conn.Close()
}

func focusLine(wsID string) string {
	return `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"` + wsID + `"}}`
}

func closeLine(wsID string) string {
	return `{"event":"workspace_closed","data":{"type":"workspace_closed","workspace_id":"` + wsID + `"}}`
}

type staticSnapshotSource struct {
	snap source.Snapshot
}

func (s *staticSnapshotSource) Snapshot(context.Context) (source.Snapshot, error) {
	return s.snap, nil
}

type oneShotDialer struct {
	srv  scriptedServer
	used bool
}

func (d *oneShotDialer) Dial(ctx context.Context) (net.Conn, error) {
	if d.used {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d.used = true
	server, client := net.Pipe()
	go d.srv.serve(server)
	return client, nil
}

type parkingWaiter struct{}

func (parkingWaiter) Wait(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func queryUntil(t *testing.T, ctx context.Context, controlPath, sessionKey string, want func(herdrwatch.StateResp) bool) herdrwatch.StateResp {
	t.Helper()
	var last herdrwatch.StateResp
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", controlPath)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		qctx, cancel := context.WithTimeout(ctx, time.Second)
		resp, qerr := herdrwatch.QueryState(qctx, conn, sessionKey)
		cancel()
		_ = conn.Close()
		if qerr == nil {
			last = resp
			if want(resp) {
				return resp
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last
}

type acceptanceCollector struct {
	ControlPath string
	DBPath      string
	cancel      context.CancelFunc
	done        chan error
}

func startCollector(t *testing.T, dir, sessionKey string, snap source.Snapshot, srv scriptedServer) *acceptanceCollector {
	t.Helper()
	return startCollectorWith(t, dir, sessionKey, &countingSnapshotSource{snap: snap}, &oneShotDialer{srv: srv}, parkingWaiter{})
}

func startCollectorWith(t *testing.T, dir, sessionKey string, src herdrwatch.SnapshotSource, dialer herdrwatch.StreamDialer, waiter herdrwatch.Waiter) *acceptanceCollector {
	t.Helper()
	dbPath := filepath.Join(dir, "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	controlPath := filepath.Join(dir, "c.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- herdrwatch.Run(ctx, herdrwatch.Config{
			SessionKey:  sessionKey,
			LockPath:    filepath.Join(dir, "own.lock"),
			ControlPath: controlPath,
			Store:       store,
			Snapshotter: herdrwatch.NewSnapshotAdapter(src),
			Dialer:      dialer,
			Waiter:      waiter,
		})
	}()

	c := &acceptanceCollector{ControlPath: controlPath, DBPath: dbPath, cancel: cancel, done: done}
	t.Cleanup(func() { c.Stop(t) })
	return c
}

func (c *acceptanceCollector) Stop(t *testing.T) error {
	t.Helper()
	if c.cancel == nil {
		return nil
	}
	c.cancel()
	c.cancel = nil
	select {
	case err := <-c.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not exit after cancellation")
		return nil
	}
}

func TestAcceptance_R3S1_TwoConcurrentCollectorsKeepHistoriesIsolated(t *testing.T) {
	dirA, dirB := shortTempDir(t), shortTempDir(t)
	keyA, errA := history.CanonicalSessionKey(filepath.Join(dirA, "herdr-a.sock"))
	keyB, errB := history.CanonicalSessionKey(filepath.Join(dirB, "herdr-b.sock"))
	if errA != nil || errB != nil || keyA == keyB {
		t.Fatalf("session key derivation failed: %v %v (same=%v)", errA, errB, keyA == keyB)
	}

	snapA := source.Snapshot{Workspaces: []source.Workspace{{ID: "a1"}, {ID: "a2"}}, FocusedWorkspaceID: "a1"}
	snapB := source.Snapshot{Workspaces: []source.Workspace{{ID: "b1"}, {ID: "b2"}}, FocusedWorkspaceID: "b1"}

	colA := startCollector(t, dirA, keyA, snapA, scriptedServer{lines: []string{focusLine("a1"), focusLine("a2")}, hold: true})
	colB := startCollector(t, dirB, keyB, snapB, scriptedServer{lines: []string{focusLine("b1"), focusLine("b2")}, hold: true})

	ctx := context.Background()
	ready := func(r herdrwatch.StateResp) bool { return r.Ready && len(r.MRU) == 2 }

	respA := queryUntil(t, ctx, colA.ControlPath, keyA, ready)
	respB := queryUntil(t, ctx, colB.ControlPath, keyB, ready)

	if !respA.Ready || len(respA.MRU) != 2 || respA.MRU[0] != "a2" || respA.MRU[1] != "a1" {
		t.Fatalf("collector A failed: %+v", respA)
	}
	if !respB.Ready || len(respB.MRU) != 2 || respB.MRU[0] != "b2" || respB.MRU[1] != "b1" {
		t.Fatalf("collector B failed: %+v", respB)
	}
	for _, id := range respA.MRU {
		if strings.HasPrefix(id, "b") {
			t.Fatalf("collector A leaked socket B workspace: %v", respA.MRU)
		}
	}
	for _, id := range respB.MRU {
		if strings.HasPrefix(id, "a") {
			t.Fatalf("collector B leaked socket A workspace: %v", respB.MRU)
		}
	}
}

func TestAcceptance_R4S2_SnapshotStreamConflictInvalidatesReadinessAndFailsClosed(t *testing.T) {
	dir := shortTempDir(t)
	key := "snapshot-conflict-session"

	coherentSnap := source.Snapshot{
		Workspaces:         []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}},
		FocusedWorkspaceID: "ws-a",
	}
	conflictingServer := scriptedServer{
		lines: []string{focusLine("ws-b"), closeLine("ws-a"), focusLine("ws-b")},
		hold:  true,
	}

	col := startCollectorWith(t, dir, key, &staticSnapshotSource{snap: coherentSnap}, &oneShotDialer{srv: conflictingServer}, parkingWaiter{})
	ctx := context.Background()

	time.Sleep(100 * time.Millisecond)

	conn, err := net.Dial("unix", col.ControlPath)
	if err != nil {
		t.Fatalf("dial control socket: %v", err)
	}
	defer conn.Close()

	resp, err := herdrwatch.QueryState(ctx, conn, key)
	if err != nil {
		t.Fatalf("query state: %v", err)
	}
	if resp.Ready || len(resp.MRU) != 0 {
		t.Fatalf("owner must not become ready on snapshot/stream conflict: %+v", resp)
	}
}

func TestAcceptance_R4S2_CausalControl_SameSnapshotWithCompatibleStreamSucceeds(t *testing.T) {
	dir := shortTempDir(t)
	key := "causal-control-session"

	coherentSnap := source.Snapshot{
		Workspaces:         []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}},
		FocusedWorkspaceID: "ws-a",
	}
	compatibleServer := scriptedServer{
		lines: []string{focusLine("ws-b"), focusLine("ws-a")},
		hold:  true,
	}

	col := startCollectorWith(t, dir, key, &staticSnapshotSource{snap: coherentSnap}, &oneShotDialer{srv: compatibleServer}, parkingWaiter{})
	ctx := context.Background()

	got := queryUntil(t, ctx, col.ControlPath, key, func(r herdrwatch.StateResp) bool {
		return r.Ready && len(r.MRU) == 2
	})
	if !got.Ready || got.MRU[0] != "ws-a" || got.MRU[1] != "ws-b" {
		t.Fatalf("compatible stream with same snapshot must become ready: %+v", got)
	}
}

func TestAcceptance_R4S2_PostBootstrapWorkspacesStillAccepted(t *testing.T) {
	dir := shortTempDir(t)
	key := "post-bootstrap-session"

	snap := source.Snapshot{Workspaces: []source.Workspace{{ID: "ws-a"}}, FocusedWorkspaceID: "ws-a"}
	src := &countingSnapshotSource{snap: snap}
	dialer := &oneShotDialer{srv: scriptedServer{
		lines: []string{focusLine("ws-a"), focusLine("ws-new")},
		hold:  true,
	}}

	col := startCollectorWith(t, dir, key, src, dialer, parkingWaiter{})
	ctx := context.Background()

	got := queryUntil(t, ctx, col.ControlPath, key, func(r herdrwatch.StateResp) bool {
		return r.Ready && len(r.MRU) == 2
	})
	if !got.Ready || got.MRU[0] != "ws-new" || src.calls != 1 {
		t.Fatalf("post-bootstrap workspace not accepted correctly: %+v (calls=%d)", got, src.calls)
	}
}

func TestAcceptance_R2S2_RestartAfterOwnerExitReacquires(t *testing.T) {
	dir := shortTempDir(t)
	key := "restart-session"
	snap := source.Snapshot{Workspaces: []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}}, FocusedWorkspaceID: "ws-a"}
	lines := []string{focusLine("ws-a"), focusLine("ws-b")}

	first := startCollector(t, dir, key, snap, scriptedServer{lines: lines, hold: true})
	ctx := context.Background()

	got := queryUntil(t, ctx, first.ControlPath, key, func(r herdrwatch.StateResp) bool {
		return r.Ready && len(r.MRU) == 2
	})
	if !got.Ready {
		t.Fatalf("first owner failed to become ready: %+v", got)
	}
	if err := first.Stop(t); err != nil {
		t.Fatalf("first owner stop: %v", err)
	}

	second := startCollector(t, dir, key, snap, scriptedServer{lines: lines, hold: true})
	restarted := queryUntil(t, ctx, second.ControlPath, key, func(r herdrwatch.StateResp) bool {
		return r.Ready && len(r.MRU) == 2
	})
	if !restarted.Ready || len(restarted.MRU) != 2 || restarted.MRU[0] != "ws-b" {
		t.Fatalf("restarted collector failed: %+v", restarted)
	}
}

func TestAcceptance_R2S2_RestartDoesNotInheritStaleDatabaseReadiness(t *testing.T) {
	dir := shortTempDir(t)
	key := "stale-db-session"
	snap := source.Snapshot{Workspaces: []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}}, FocusedWorkspaceID: "ws-a"}

	dbPath := filepath.Join(dir, "jump_history.sqlite3")
	seed, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	for _, id := range []string{"ws-a", "ws-b"} {
		if _, err := seed.Record(context.Background(), key, id); err != nil {
			t.Fatalf("seed record: %v", err)
		}
	}
	_ = seed.Close()

	col := startCollector(t, dir, key, snap, scriptedServer{hold: true})
	got := queryUntil(t, context.Background(), col.ControlPath, key, func(r herdrwatch.StateResp) bool {
		return r.Epoch > 0
	})
	if got.Ready || len(got.MRU) != 0 {
		t.Fatalf("fresh owner must not inherit stale DB readiness: %+v", got)
	}
}

func TestAcceptance_R5S1_TransientDropReconnectsAndInvalidatesUntilReverified(t *testing.T) {
	dir := shortTempDir(t)
	key := "reconnect-session"
	snap := source.Snapshot{Workspaces: []source.Workspace{{ID: "ws-a"}, {ID: "ws-b"}}, FocusedWorkspaceID: "ws-a"}

	dialer := &sequenceDialer{servers: []scriptedServer{
		{lines: []string{focusLine("ws-a"), focusLine("ws-b")}},
		{lines: []string{focusLine("ws-a"), focusLine("ws-b")}, hold: true},
	}}

	col := startCollectorWith(t, dir, key, &countingSnapshotSource{snap: snap}, dialer, immediateWaiter{})
	got := queryUntil(t, context.Background(), col.ControlPath, key, func(r herdrwatch.StateResp) bool {
		return r.Ready && len(r.MRU) == 2 && r.Epoch >= 2
	})
	if !got.Ready || dialer.dials < 2 || got.Epoch < 2 {
		t.Fatalf("reconnect failed: %+v (dials=%d)", got, dialer.dials)
	}
	_ = col.Stop(t)
}

type sequenceDialer struct {
	servers []scriptedServer
	dials   int
}

func (d *sequenceDialer) Dial(ctx context.Context) (net.Conn, error) {
	if d.dials >= len(d.servers) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	srv := d.servers[d.dials]
	d.dials++
	server, client := net.Pipe()
	go srv.serve(server)
	return client, nil
}

type immediateWaiter struct{}

func (immediateWaiter) Wait(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

func TestAcceptance_R5S2_UnreachableHostExitsBoundedWithoutRespawn(t *testing.T) {
	dir := shortTempDir(t)
	dialer := &alwaysFailDialer{}
	store, err := history.OpenPath(filepath.Join(dir, "jump_history.sqlite3"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	start := time.Now()
	err = herdrwatch.Run(context.Background(), herdrwatch.Config{
		SessionKey:  "gone-session",
		LockPath:    filepath.Join(dir, "own.lock"),
		ControlPath: filepath.Join(dir, "c.sock"),
		Store:       store,
		Snapshotter: herdrwatch.NewSnapshotAdapter(&countingSnapshotSource{}),
		Dialer:      dialer,
		Waiter:      immediateWaiter{},
	})
	elapsed := time.Since(start)

	if err != herdrwatch.ErrReconnectExhausted || elapsed > 30*time.Second || dialer.dials < 2 {
		t.Fatalf("unreachable host exit error=%v elapsed=%s dials=%d", err, elapsed, dialer.dials)
	}
	if strings.Contains(err.Error(), "host death") {
		t.Fatalf("exhaustion must not claim host death: %v", err)
	}
	if _, err := net.Dial("unix", filepath.Join(dir, "c.sock")); err == nil {
		t.Fatal("control endpoint remained served after exit")
	}
}

type alwaysFailDialer struct{ dials int }

func (d *alwaysFailDialer) Dial(context.Context) (net.Conn, error) {
	d.dials++
	return nil, io.ErrUnexpectedEOF
}
