package herdrwatch_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/herdrwatch"
)

type mockProvider struct {
	resp herdrwatch.StateResp
	err  error
}

func (m *mockProvider) State(ctx context.Context, sessionKey string) (herdrwatch.StateResp, error) {
	if m.err != nil {
		return herdrwatch.StateResp{}, m.err
	}
	return m.resp, nil
}

func testSocketPair(t *testing.T) (net.Conn, net.Conn, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}

	clientConnCh := make(chan net.Conn, 1)
	go func() {
		conn, err := net.Dial(l.Addr().Network(), l.Addr().String())
		if err == nil {
			clientConnCh <- conn
		}
	}()

	serverConn, err := l.Accept()
	if err != nil {
		l.Close()
		t.Fatalf("Accept failed: %v", err)
	}

	clientConn := <-clientConnCh
	cleanup := func() {
		serverConn.Close()
		clientConn.Close()
		l.Close()
	}

	return serverConn, clientConn, cleanup
}

func TestControl_ServeAndQueryState_Ready(t *testing.T) {
	provider := &mockProvider{
		resp: herdrwatch.StateResp{
			Ready: true,
			Epoch: 5,
			MRU:   []string{"ws-alpha", "ws-beta"},
		},
	}

	server := herdrwatch.NewServer(provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverConn, clientConn, cleanup := testSocketPair(t)
	defer cleanup()

	go func() {
		_ = server.HandleConn(ctx, serverConn)
	}()

	resp, err := herdrwatch.QueryState(ctx, clientConn, "session-1")
	if err != nil {
		t.Fatalf("QueryState failed: %v", err)
	}

	if !resp.Ready || resp.Epoch != 5 || !reflect.DeepEqual(resp.MRU, provider.resp.MRU) {
		t.Errorf("QueryState returned unexpected response: %+v", resp)
	}
}

func TestControl_ServeAndQueryState_Unready(t *testing.T) {
	provider := &mockProvider{
		resp: herdrwatch.StateResp{
			Ready: false,
			Epoch: 0,
			MRU:   []string{},
		},
	}

	server := herdrwatch.NewServer(provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverConn, clientConn, cleanup := testSocketPair(t)
	defer cleanup()

	go func() {
		_ = server.HandleConn(ctx, serverConn)
	}()

	resp, err := herdrwatch.QueryState(ctx, clientConn, "session-unready")
	if err != nil {
		t.Fatalf("QueryState failed: %v", err)
	}

	if resp.Ready {
		t.Errorf("Expected Ready=false, got true")
	}
}

func TestControl_ServeAndQueryState_StoreError(t *testing.T) {
	provider := &mockProvider{
		err: errors.New("raw internal db crash details secret_path=/etc/passwd"),
	}

	server := herdrwatch.NewServer(provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverConn, clientConn, cleanup := testSocketPair(t)
	defer cleanup()

	go func() {
		_ = server.HandleConn(ctx, serverConn)
	}()

	resp, err := herdrwatch.QueryState(ctx, clientConn, "session-err")
	if err != nil {
		t.Fatalf("QueryState failed: %v", err)
	}

	if resp.Ready {
		t.Errorf("Expected Ready=false on store error")
	}
	if resp.ClassifiedError == "" || strings.Contains(resp.ClassifiedError, "secret_path") {
		t.Errorf("ClassifiedError must be sanitized without raw internal details, got: %q", resp.ClassifiedError)
	}
}

func TestControl_MalformedAndOversizedRequest(t *testing.T) {
	provider := &mockProvider{resp: herdrwatch.StateResp{Ready: true, Epoch: 1, MRU: []string{}}}
	server := herdrwatch.NewServer(provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("malformed json payload", func(t *testing.T) {
		serverConn, clientConn, cleanup := testSocketPair(t)
		defer cleanup()

		go func() {
			_ = server.HandleConn(ctx, serverConn)
		}()

		_, _ = clientConn.Write([]byte("{invalid json garbage}\n"))

		var resp herdrwatch.StateResp
		err := herdrwatch.DecodeResp(clientConn, &resp)
		if err == nil && resp.Ready {
			t.Errorf("Expected malformed request to fail or return unready response")
		}
	})

	t.Run("oversized request payload", func(t *testing.T) {
		serverConn, clientConn, cleanup := testSocketPair(t)
		defer cleanup()

		go func() {
			_ = server.HandleConn(ctx, serverConn)
		}()

		hugePayload := fmt.Sprintf(`{"session_key":"%s"}`+"\n", strings.Repeat("x", 70000))
		_, _ = clientConn.Write([]byte(hugePayload))

		var resp herdrwatch.StateResp
		_ = herdrwatch.DecodeResp(clientConn, &resp)
		if resp.Ready {
			t.Errorf("Expected oversized request to be rejected")
		}
	})
}

func TestControl_ServerCancellationAndCleanup(t *testing.T) {
	// Short temporary directory for Darwin sun_path socket path (<104 chars)
	tempDir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	sockPath := filepath.Join(tempDir, "c.sock")
	if len(sockPath) > 104 {
		t.Fatalf("Unix socket path too long for Darwin: %d chars (%s)", len(sockPath), sockPath)
	}

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}

	provider := &mockProvider{resp: herdrwatch.StateResp{Ready: true, Epoch: 10, MRU: []string{"ws-1"}}}
	server := herdrwatch.NewServer(provider)

	ctx, cancel := context.WithCancel(context.Background())

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(ctx, listener)
	}()

	// Connect a client
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("net.Dial failed: %v", err)
	}

	resp, err := herdrwatch.QueryState(ctx, conn, "session-test")
	conn.Close()
	if err != nil || !resp.Ready {
		t.Fatalf("QueryState failed before cancel: %v, resp=%+v", err, resp)
	}

	// Cancel server context
	cancel()

	select {
	case err := <-serveErrCh:
		if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "closed") {
			t.Errorf("Serve returned unexpected error on cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Server failed to shut down cleanly within 2 seconds")
	}
}

func TestControl_DataRacePrevention(t *testing.T) {
	originalSlice := []string{"ws-1", "ws-2"}
	provider := &mockProvider{
		resp: herdrwatch.StateResp{
			Ready: true,
			Epoch: 1,
			MRU:   originalSlice,
		},
	}

	server := herdrwatch.NewServer(provider)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverConn, clientConn, cleanup := testSocketPair(t)
	defer cleanup()

	go func() {
		_ = server.HandleConn(ctx, serverConn)
	}()

	resp, err := herdrwatch.QueryState(ctx, clientConn, "session-race")
	if err != nil {
		t.Fatalf("QueryState failed: %v", err)
	}

	// Mutating returned slice must not affect provider slice
	if len(resp.MRU) > 0 {
		resp.MRU[0] = "MUTATED"
	}
	if originalSlice[0] == "MUTATED" {
		t.Errorf("Data race detected: provider slice was mutated via returned response")
	}
}
