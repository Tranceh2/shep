package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeUnixServer struct {
	t        *testing.T
	listener net.Listener
	sockPath string
	conns    []net.Conn
	mu       sync.Mutex
	connCh   chan net.Conn
	closed   chan struct{}
}

func newFakeUnixServer(t *testing.T) *fakeUnixServer {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "sh_*.sock")
	if err != nil {
		t.Fatalf("failed to create temp socket: %v", err)
	}
	sockPath := f.Name()
	_ = f.Close()
	_ = os.Remove(sockPath)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	s := &fakeUnixServer{
		t:        t,
		listener: l,
		sockPath: sockPath,
		connCh:   make(chan net.Conn, 10),
		closed:   make(chan struct{}),
	}
	go func() {
		for {
			conn, err := s.listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			select {
			case s.connCh <- conn:
			case <-s.closed:
				_ = conn.Close()
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = s.listener.Close()
		s.mu.Lock()
		select {
		case <-s.closed:
		default:
			close(s.closed)
		}
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.mu.Unlock()
		_ = os.Remove(sockPath)
	})
	return s
}

func (s *fakeUnixServer) NextConn(timeout time.Duration) net.Conn {
	select {
	case conn := <-s.connCh:
		return conn
	case <-s.closed:
		return nil
	case <-time.After(timeout):
		select {
		case <-s.closed:
			return nil
		default:
			s.t.Fatalf("timed out waiting for incoming connection on %s", s.sockPath)
			return nil
		}
	}
}

func testSubscribe(t *testing.T, serverFn func(conn net.Conn)) StatusStream {
	t.Helper()
	srv := newFakeUnixServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := net.Dial("unix", srv.sockPath)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	serverConn := srv.NextConn(2 * time.Second)
	go func() {
		r := bufio.NewReader(serverConn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(serverConn, `{"id":"shep-live-status","result":{"type":"subscription_started"}}`+"\n")
		serverFn(serverConn)
	}()

	stream, err := SubscribeStatus(ctx, conn)
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func TestSubscribeStatus_Handshake(t *testing.T) {
	cases := []struct {
		name, ack, wantError string
	}{
		{name: "success ack", ack: `{"id":"shep-live-status","result":{"type":"subscription_started"}}` + "\n"},
		{name: "rejected ack", ack: `{"id":"shep-live-status","error":{"code":"ERR"}}` + "\n", wantError: "rejected"},
		{name: "invalid ack type", ack: `{"id":"shep-live-status","result":{"type":"other"}}` + "\n", wantError: "missing subscription_started"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeUnixServer(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			conn, err := net.Dial("unix", srv.sockPath)
			if err != nil {
				t.Fatalf("dial failed: %v", err)
			}
			defer conn.Close()

			serverConn := srv.NextConn(2 * time.Second)
			go func() {
				r := bufio.NewReader(serverConn)
				reqLine, _ := r.ReadString('\n')
				if !strings.Contains(reqLine, "pane.agent_status_changed") {
					t.Errorf("expected dotted subscription in %s", reqLine)
				}
				_, _ = io.WriteString(serverConn, tc.ack)
			}()

			stream, err := SubscribeStatus(ctx, conn)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				_ = stream.Close()
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestSubscribeStatus_ReadStream(t *testing.T) {
	cases := []struct {
		name      string
		serverFn  func(conn net.Conn)
		wantEv    StatusEvent
		wantError string
	}{
		{
			name: "malformed and unrelated events discarded",
			serverFn: func(conn net.Conn) {
				_, _ = io.WriteString(conn, "NOT JSON\n"+`{"event":"other"}`+"\n"+`{"event":"pane_agent_status_changed","data":{"pane_id":"p1","agent_status":"working"}}`+"\n")
			},
			wantEv: StatusEvent{PaneID: "p1", Status: StatusWorking},
		},
		{
			name:      "oversized line errors",
			serverFn:  func(conn net.Conn) { _, _ = io.WriteString(conn, strings.Repeat("A", (1<<20)+100)+"\n") },
			wantError: "exceeds maximum size",
		},
		{
			name:      "eof returned cleanly",
			serverFn:  func(conn net.Conn) { _ = conn.Close() },
			wantError: "EOF",
		},
		{
			name: "idle status and workspace/tab ids",
			serverFn: func(conn net.Conn) {
				_, _ = io.WriteString(conn, `{"event":"pane_agent_status_changed","data":{"pane_id":"p2","workspace_id":"w1","tab_id":"t1","agent_status":"idle"}}`+"\n")
			},
			wantEv: StatusEvent{PaneID: "p2", WorkspaceID: "w1", TabID: "t1", Status: StatusIdle},
		},
		{
			name: "status field fallback",
			serverFn: func(conn net.Conn) {
				_, _ = io.WriteString(conn, `{"event":"pane_agent_status_changed","data":{"pane_id":"p3","status":"blocked"}}`+"\n")
			},
			wantEv: StatusEvent{PaneID: "p3", Status: StatusBlocked},
		},
		{
			name: "done status",
			serverFn: func(conn net.Conn) {
				_, _ = io.WriteString(conn, `{"event":"pane_agent_status_changed","data":{"pane_id":"p4","agent_status":"done"}}`+"\n")
			},
			wantEv: StatusEvent{PaneID: "p4", Status: StatusDone},
		},
		{
			name: "unrecognized status normalizes to unknown",
			serverFn: func(conn net.Conn) {
				_, _ = io.WriteString(conn, `{"event":"pane_agent_status_changed","data":{"pane_id":"p5","agent_status":"busy"}}`+"\n")
			},
			wantEv: StatusEvent{PaneID: "p5", Status: StatusUnknown},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := testSubscribe(t, tc.serverFn)
			ev, err := stream.ReadEvent()
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("unexpected read error: %v", err)
				}
				if ev.PaneID != tc.wantEv.PaneID || ev.Status != tc.wantEv.Status || ev.WorkspaceID != tc.wantEv.WorkspaceID || ev.TabID != tc.wantEv.TabID {
					t.Errorf("expected %+v, got %+v", tc.wantEv, ev)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestSubscribeStatus_ReadBuffered(t *testing.T) {
	stream := testSubscribe(t, func(conn net.Conn) {
		events := `{"event":"pane_agent_status_changed","data":{"pane_id":"p1","agent_status":"working"}}` + "\n" +
			`{"event":"pane_agent_status_changed","data":{"pane_id":"p2","agent_status":"done"}}` + "\n"
		_, _ = io.WriteString(conn, events)
	})

	time.Sleep(50 * time.Millisecond)
	ev1, err := stream.ReadEvent()
	if err != nil || ev1.PaneID != "p1" {
		t.Fatalf("read first event failed: %v, %+v", err, ev1)
	}

	buf := stream.ReadBuffered()
	if len(buf) != 1 || buf[0].PaneID != "p2" || buf[0].Status != StatusDone {
		t.Fatalf("unexpected buffered events: %+v", buf)
	}
}

func TestLiveStatus_TeardownOrdering(t *testing.T) {
	srv := newFakeUnixServer(t)
	serverConnCh := make(chan net.Conn, 1)
	go func() {
		conn := srv.NextConn(2 * time.Second)
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(conn, `{"id":"shep-live-status","result":{"type":"subscription_started"}}`+"\n")
		serverConnCh <- conn
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	beforeGoroutines := runtime.NumGoroutine()
	ls := startLiveStatus(ctx, NewUnixStatusDialer(srv.sockPath))
	if ls == nil {
		t.Fatal("startLiveStatus returned nil")
	}

	serverConn := <-serverConnCh
	defer serverConn.Close()

	select {
	case <-ls.done:
		t.Fatal("reader should still be active")
	case <-time.After(50 * time.Millisecond):
	}

	if err := ls.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	select {
	case <-ls.done:
	default:
		t.Fatal("done channel not closed after Close()")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= beforeGoroutines+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveStatus_CancelPath(t *testing.T) {
	srv := newFakeUnixServer(t)
	go func() {
		conn := srv.NextConn(2 * time.Second)
		if conn == nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(conn, `{"id":"shep-live-status","result":{"type":"subscription_started"}}`+"\n")
		b := make([]byte, 1024)
		for {
			if _, err := conn.Read(b); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	ls := startLiveStatus(ctx, NewUnixStatusDialer(srv.sockPath))
	if ls == nil {
		t.Fatal("startLiveStatus returned nil")
	}

	cancel()
	_ = ls.Close()

	select {
	case <-ls.done:
	case <-time.After(1 * time.Second):
		t.Fatal("reader failed to exit on cancel")
	}
}

func TestLiveStatus_StartupTimeoutDoesNotBlock(t *testing.T) {
	srv := newFakeUnixServer(t)
	started := time.Now()
	if ls := startLiveStatus(context.Background(), NewUnixStatusDialer(srv.sockPath)); ls != nil {
		_ = ls.Close()
		t.Fatal("expected hanging subscription to degrade to nil")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("startup took %v, want bounded fallback", elapsed)
	}
}

func TestLiveStatus_SubscribeFailureClosesConnection(t *testing.T) {
	srv := newFakeUnixServer(t)
	serverConn := make(chan net.Conn, 1)
	go func() {
		conn := srv.NextConn(2 * time.Second)
		serverConn <- conn
		if conn == nil {
			return
		}
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(conn, `{"id":"shep-live-status","error":{"code":"ERR"}}`+"\n")
	}()
	if ls := startLiveStatus(context.Background(), NewUnixStatusDialer(srv.sockPath)); ls != nil {
		_ = ls.Close()
		t.Fatal("expected rejected subscription to degrade to nil")
	}
	conn := <-serverConn
	if conn == nil {
		t.Fatal("server did not observe connection")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("subscription failure did not close the client socket")
	}
}

func TestLiveStatus_Degradation(t *testing.T) {
	if ls := startLiveStatus(context.Background(), nil); ls != nil {
		t.Errorf("expected nil for nil dialer, got %+v", ls)
	}
	if ls := startLiveStatus(context.Background(), NewUnixStatusDialer("")); ls != nil {
		t.Errorf("expected nil for empty socket path, got %+v", ls)
	}
	if ls := startLiveStatus(context.Background(), NewUnixStatusDialer(filepath.Join(t.TempDir(), "nonexistent.sock"))); ls != nil {
		t.Errorf("expected nil for missing socket path, got %+v", ls)
	}

	srv := newFakeUnixServer(t)
	go func() {
		conn := srv.NextConn(2 * time.Second)
		if conn == nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(conn, `{"id":"shep-live-status","error":{"code":"ERR"}}`+"\n")
	}()

	if ls := startLiveStatus(context.Background(), NewUnixStatusDialer(srv.sockPath)); ls != nil {
		t.Errorf("expected nil for rejected subscribe, got %+v", ls)
	}
}

func TestLiveStatus_DropOldestOnOverflow(t *testing.T) {
	srv := newFakeUnixServer(t)
	serverConnCh := make(chan net.Conn, 1)
	go func() {
		conn := srv.NextConn(2 * time.Second)
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(conn, `{"id":"shep-live-status","result":{"type":"subscription_started"}}`+"\n")
		serverConnCh <- conn
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ls := startLiveStatus(ctx, NewUnixStatusDialer(srv.sockPath))
	if ls == nil {
		t.Fatal("startLiveStatus returned nil")
	}
	defer ls.Close()

	serverConn := <-serverConnCh
	defer serverConn.Close()

	for i := 0; i < 40; i++ {
		ev := fmt.Sprintf(`{"event":"pane_agent_status_changed","data":{"pane_id":"p%d","agent_status":"working"}}`+"\n", i)
		_, _ = io.WriteString(serverConn, ev)
	}

	time.Sleep(100 * time.Millisecond)

	var received []StatusEvent
	draining := true
	for draining {
		select {
		case ev, ok := <-ls.Events():
			if !ok {
				draining = false
				break
			}
			received = append(received, ev)
		default:
			draining = false
		}
	}

	if len(received) == 0 || len(received) > 32 {
		t.Fatalf("expected 1-32 events, got %d", len(received))
	}
	if last := received[len(received)-1]; last.PaneID != "p39" {
		t.Errorf("expected newest event 'p39', got '%s'", last.PaneID)
	}
}

func TestLiveStatus_ExactlyOneConnection(t *testing.T) {
	srv := newFakeUnixServer(t)
	var connCount int32
	go func() {
		for {
			conn := srv.NextConn(2 * time.Second)
			if conn == nil {
				return
			}
			atomic.AddInt32(&connCount, 1)
			r := bufio.NewReader(conn)
			_, _ = r.ReadString('\n')
			_, _ = io.WriteString(conn, `{"id":"shep-live-status","result":{"type":"subscription_started"}}`+"\n")
			time.Sleep(50 * time.Millisecond)
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ls := startLiveStatus(ctx, NewUnixStatusDialer(srv.sockPath))
	if ls == nil {
		t.Fatal("startLiveStatus returned nil")
	}

	select {
	case <-ls.done:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for EOF")
	}
	_ = ls.Close()

	if count := atomic.LoadInt32(&connCount); count != 1 {
		t.Errorf("expected 1 connection, got %d", count)
	}
}
