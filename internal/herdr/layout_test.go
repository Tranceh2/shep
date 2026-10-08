package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockHerdrServer is an in-process Unix domain socket server standing in for
// the Herdr daemon (R9.1). It never shells out and never touches a live
// daemon, so the whole layout suite stays hermetic under -race.
type mockHerdrServer struct {
	path string

	mu       sync.Mutex
	requests [][]byte

	closed chan struct{}
}

// respondFn receives the raw request line the client sent and returns the raw
// bytes to write back. Returning nil writes nothing, which lets a test model a
// daemon that accepts a connection and then goes silent.
type respondFn func(raw []byte) []byte

// newMockHerdrServer starts a listener under a short temporary directory.
// Darwin caps sun_path at 104 bytes, so t.TempDir() (which embeds the test
// name) is not usable for sockets here.
func newMockHerdrServer(t *testing.T, respond respondFn) *mockHerdrServer {
	t.Helper()

	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "h.sock")
	if len(path) > 104 {
		t.Fatalf("socket path too long for Darwin: %d chars (%s)", len(path), path)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	s := &mockHerdrServer{path: path, closed: make(chan struct{})}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()

				_ = c.SetDeadline(time.Now().Add(5 * time.Second))

				buf := make([]byte, 0, 4096)
				chunk := make([]byte, 1024)
				for {
					n, err := c.Read(chunk)
					buf = append(buf, chunk[:n]...)
					if err != nil {
						return
					}
					if idx := strings.IndexByte(string(buf), '\n'); idx >= 0 {
						break
					}
				}

				s.mu.Lock()
				s.requests = append(s.requests, append([]byte(nil), buf...))
				s.mu.Unlock()

				if resp := respond(buf); resp != nil {
					_, _ = c.Write(resp)
					return
				}
				// No reply: hold the connection open so the client must rely
				// on its own deadline to give up.
				<-s.closed
			}(conn)
		}
	}()

	t.Cleanup(func() {
		close(s.closed)
		_ = ln.Close()
		wg.Wait()
	})

	return s
}

// gotRequests returns a copy of every request line the server observed.
func (s *mockHerdrServer) gotRequests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.requests))
	copy(out, s.requests)
	return out
}

// successBody builds the daemon's real layout.apply success envelope
// (SuccessResponse -> ResponseResult::LayoutApply -> LayoutDescription).
func successBody(id string) []byte {
	return []byte(`{"id":"` + id + `","result":{"type":"layout_apply","layout":{` +
		`"workspace_id":"ws-1","tab_id":"tab-7","zoomed":false,` +
		`"focused_pane_id":"pane-3","root":{"type":"pane","pane_id":"pane-3"}}}}` + "\n")
}

// twoPaneParams is a representative split layout used across several cases.
func twoPaneParams() LayoutApplyParams {
	return LayoutApplyParams{
		WorkspaceID: "ws-1",
		TabLabel:    "editor",
		Focus:       true,
		Root: LayoutNode{
			Type:      NodeTypeSplit,
			Direction: DirectionRight,
			Ratio:     0.25,
			First:     &LayoutNode{Type: NodeTypePane, Command: []string{"nvim", "."}, Cwd: "/repo", Label: "edit", PaneID: "shep:t0:n1"},
			Second:    &LayoutNode{Type: NodeTypePane, Cwd: "/repo", PaneID: "shep:t0:n2", Env: map[string]string{"FOO": "bar"}},
		},
	}
}

func TestApplyLayout_SuccessOverUnixSocket(t *testing.T) {
	// R1.1: a valid request produces a decoded LayoutApplyResult and nil error.
	srv := newMockHerdrServer(t, func(raw []byte) []byte {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return []byte(`{"id":"x","error":{"code":"bad","message":"unparsable"}}` + "\n")
		}
		return successBody(req.ID)
	})

	client := NewLayoutClient()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.ApplyLayout(ctx, srv.path, twoPaneParams())
	if err != nil {
		t.Fatalf("ApplyLayout returned error: %v", err)
	}
	if res.WorkspaceID != "ws-1" {
		t.Errorf("WorkspaceID = %q, want %q", res.WorkspaceID, "ws-1")
	}
	if res.TabID != "tab-7" {
		t.Errorf("TabID = %q, want %q", res.TabID, "tab-7")
	}
	if res.FocusedPaneID != "pane-3" {
		t.Errorf("FocusedPaneID = %q, want %q", res.FocusedPaneID, "pane-3")
	}
	if res.Zoomed {
		t.Errorf("Zoomed = true, want false")
	}
	if res.Root == nil || res.Root.PaneID != "pane-3" {
		t.Errorf("Root = %+v, want decoded pane node pane-3", res.Root)
	}
}

func TestApplyLayout_RequestMatchesProtocol22Envelope(t *testing.T) {
	// R1.1: the bytes on the wire must match the daemon's byte-verified
	// Protocol 22 request shape: a newline-terminated {id, method, params}
	// frame whose params is LayoutApplyParams.
	srv := newMockHerdrServer(t, func(raw []byte) []byte {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &req)
		return successBody(req.ID)
	})

	client := NewLayoutClient(WithLayoutIDFunc(func() string { return "shep:layout:test" }))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if _, err := client.ApplyLayout(ctx, srv.path, twoPaneParams()); err != nil {
		t.Fatalf("ApplyLayout returned error: %v", err)
	}

	got := srv.gotRequests()
	if len(got) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1", len(got))
	}
	line := got[0]
	if line[len(line)-1] != '\n' {
		t.Errorf("request frame is not newline terminated: %q", string(line))
	}

	var env map[string]json.RawMessage
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("request is not valid JSON: %v (%q)", err, string(line))
	}
	for _, key := range []string{"id", "method", "params"} {
		if _, ok := env[key]; !ok {
			t.Errorf("request envelope missing %q key: %q", key, string(line))
		}
	}
	if string(env["id"]) != `"shep:layout:test"` {
		t.Errorf("id = %s, want %q", env["id"], "shep:layout:test")
	}
	if string(env["method"]) != `"layout.apply"` {
		t.Errorf("method = %s, want %q", env["method"], "layout.apply")
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(env["params"], &params); err != nil {
		t.Fatalf("params is not an object: %v", err)
	}
	if string(params["workspace_id"]) != `"ws-1"` {
		t.Errorf("params.workspace_id = %s, want %q", params["workspace_id"], "ws-1")
	}
	if string(params["tab_label"]) != `"editor"` {
		t.Errorf("params.tab_label = %s, want %q", params["tab_label"], "editor")
	}
	if string(params["focus"]) != "true" {
		t.Errorf("params.focus = %s, want true", params["focus"])
	}

	// The split variant must carry direction/ratio/first/second, and the leaf
	// pane variant must carry argv-shaped command with no split keys.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(params["root"], &root); err != nil {
		t.Fatalf("params.root is not an object: %v", err)
	}
	if string(root["type"]) != `"split"` {
		t.Errorf("root.type = %s, want %q", root["type"], "split")
	}
	if string(root["direction"]) != `"right"` {
		t.Errorf("root.direction = %s, want %q", root["direction"], "right")
	}
	if string(root["ratio"]) != "0.25" {
		t.Errorf("root.ratio = %s, want 0.25", root["ratio"])
	}

	var first map[string]json.RawMessage
	if err := json.Unmarshal(root["first"], &first); err != nil {
		t.Fatalf("root.first is not an object: %v", err)
	}
	if string(first["type"]) != `"pane"` {
		t.Errorf("first.type = %s, want %q", first["type"], "pane")
	}
	if string(first["command"]) != `["nvim","."]` {
		t.Errorf("first.command = %s, want argv array [\"nvim\",\".\"]", first["command"])
	}
	if _, ok := first["ratio"]; ok {
		t.Errorf("pane node must not emit a ratio key: %s", string(root["first"]))
	}
	if _, ok := first["direction"]; ok {
		t.Errorf("pane node must not emit a direction key: %s", string(root["first"]))
	}
}

func TestApplyLayout_SocketAbsentFailsClosed(t *testing.T) {
	// R1.2: an absent socket fails closed with ErrHerdrSocketUnavailable.
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	missing := filepath.Join(dir, "absent.sock")

	client := NewLayoutClient()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.ApplyLayout(ctx, missing, twoPaneParams())
	if err == nil {
		t.Fatalf("ApplyLayout succeeded against an absent socket, result %+v", res)
	}
	if !errors.Is(err, ErrHerdrSocketUnavailable) {
		t.Errorf("error %v does not wrap ErrHerdrSocketUnavailable", err)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil on failure", res)
	}
}

func TestApplyLayout_EmptySocketPathFailsBeforeDial(t *testing.T) {
	// R8.1: an unusable target is rejected without touching the network.
	client := NewLayoutClient()

	res, err := client.ApplyLayout(context.Background(), "", twoPaneParams())
	if err == nil {
		t.Fatalf("ApplyLayout succeeded with an empty socket path, result %+v", res)
	}
	if !errors.Is(err, ErrHerdrSocketUnavailable) {
		t.Errorf("error %v does not wrap ErrHerdrSocketUnavailable", err)
	}
}

func TestApplyLayout_DaemonErrorResponse(t *testing.T) {
	// R1.3: a daemon error body becomes a typed HerdrRPCError that preserves
	// the remote code and message.
	srv := newMockHerdrServer(t, func(raw []byte) []byte {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &req)
		return []byte(`{"id":"` + req.ID + `","error":{"code":"invalid_layout",` +
			`"message":"layout target not found"}}` + "\n")
	})

	client := NewLayoutClient()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.ApplyLayout(ctx, srv.path, twoPaneParams())
	if err == nil {
		t.Fatalf("ApplyLayout succeeded on a daemon error, result %+v", res)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil on daemon error", res)
	}

	var rpcErr *HerdrRPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error %v is not a *HerdrRPCError", err)
	}
	if rpcErr.Code != "invalid_layout" {
		t.Errorf("Code = %q, want %q", rpcErr.Code, "invalid_layout")
	}
	if rpcErr.Message != "layout target not found" {
		t.Errorf("Message = %q, want %q", rpcErr.Message, "layout target not found")
	}
	if !strings.Contains(rpcErr.Error(), "invalid_layout") ||
		!strings.Contains(rpcErr.Error(), "layout target not found") {
		t.Errorf("Error() = %q, want it to surface both code and message", rpcErr.Error())
	}
	if errors.Is(err, ErrHerdrSocketUnavailable) {
		t.Errorf("a daemon rejection must not be reported as a socket failure: %v", err)
	}
}

func TestApplyLayout_ContextDeadlineOnSilentDaemon(t *testing.T) {
	// R1.2 / threat matrix: a daemon that accepts and never replies must not
	// hang the caller past its context deadline.
	srv := newMockHerdrServer(t, func([]byte) []byte { return nil })

	client := NewLayoutClient()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := client.ApplyLayout(ctx, srv.path, twoPaneParams())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("ApplyLayout succeeded against a silent daemon, result %+v", res)
	}
	if elapsed > 2*time.Second {
		t.Errorf("ApplyLayout blocked for %v, want it bounded by the context deadline", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not wrap context.DeadlineExceeded", err)
	}
	if !errors.Is(err, ErrHerdrSocketUnavailable) {
		t.Errorf("error %v does not wrap ErrHerdrSocketUnavailable", err)
	}
}

func TestApplyLayout_ContextCancellationOnSilentDaemon(t *testing.T) {
	// A caller-side cancellation must unblock the read and surface as
	// context.Canceled rather than a generic timeout.
	srv := newMockHerdrServer(t, func([]byte) []byte { return nil })

	client := NewLayoutClient()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	res, err := client.ApplyLayout(ctx, srv.path, twoPaneParams())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("ApplyLayout succeeded after cancellation, result %+v", res)
	}
	// Cancellation must actually interrupt the blocked read. Without that,
	// the call would only unblock at the fallback timeout, which is a hang
	// from the caller's point of view even though the error looks correct.
	if elapsed > 2*time.Second {
		t.Errorf("ApplyLayout kept blocking for %v after cancellation; cancel must interrupt the read", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not wrap context.Canceled", err)
	}
}

func TestApplyLayout_RejectsUnusableResponses(t *testing.T) {
	// Triangulation across every response shape the daemon must never make
	// the client trust (R8.1, R9.1).
	oversized := func() []byte {
		filler := strings.Repeat("A", (1<<20)+512)
		return []byte(`{"id":"x","result":{"type":"layout_apply","layout":{"workspace_id":"` +
			filler + `"}}}` + "\n")
	}()

	for _, tt := range []struct {
		name     string
		body     []byte
		wantErr  error
		wantKind string
	}{
		{
			name:     "malformed json",
			body:     []byte("this is not json at all\n"),
			wantErr:  ErrHerdrMalformedResponse,
			wantKind: "malformed",
		},
		{
			name:     "truncated frame",
			body:     []byte(`{"id":"x","result":{"type":"layout_ap`),
			wantErr:  ErrHerdrMalformedResponse,
			wantKind: "malformed",
		},
		{
			name:     "success envelope without a layout body",
			body:     []byte(`{"id":"x","result":{"type":"layout_apply"}}` + "\n"),
			wantErr:  ErrHerdrMalformedResponse,
			wantKind: "malformed",
		},
		{
			name:     "response id does not match the request id",
			body:     []byte(`{"id":"someone-elses-request","result":{"type":"layout_apply","layout":{"workspace_id":"ws-9","tab_id":"t","zoomed":false,"focused_pane_id":"p","root":{"type":"pane"}}}}` + "\n"),
			wantErr:  ErrHerdrMalformedResponse,
			wantKind: "malformed",
		},
		{
			name:     "oversized frame beyond the 1 MiB bound",
			body:     oversized,
			wantErr:  ErrHerdrResponseTooLarge,
			wantKind: "oversized",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			srv := newMockHerdrServer(t, func([]byte) []byte { return body })

			client := NewLayoutClient(WithLayoutIDFunc(func() string { return "x" }))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			res, err := client.ApplyLayout(ctx, srv.path, twoPaneParams())
			if err == nil {
				t.Fatalf("ApplyLayout accepted a %s response, result %+v", tt.wantKind, res)
			}
			if res != nil {
				t.Errorf("result = %+v, want nil for a %s response", res, tt.wantKind)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error %v does not wrap %v", err, tt.wantErr)
			}
		})
	}
}

func TestApplyLayout_InvalidNodeTypeFailsBeforeSocketWrite(t *testing.T) {
	// R8.1: a structurally invalid layout is rejected before any byte reaches
	// the socket, so a bad compile can never half-apply a layout.
	srv := newMockHerdrServer(t, func([]byte) []byte {
		return successBody("unused")
	})

	client := NewLayoutClient()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	params := LayoutApplyParams{
		WorkspaceID: "ws-1",
		Root:        LayoutNode{Type: "definitely-not-a-node-type"},
	}

	res, err := client.ApplyLayout(ctx, srv.path, params)
	if err == nil {
		t.Fatalf("ApplyLayout accepted an invalid node type, result %+v", res)
	}
	if got := len(srv.gotRequests()); got != 0 {
		t.Errorf("server saw %d requests, want 0 — validation must precede the socket write", got)
	}
}

func TestApplyLayout_ClosesConnectionOnEveryOutcome(t *testing.T) {
	// R9.1 / D1: the client must own and release its connection on both the
	// success and the failure path, leaving no descriptor behind.
	for _, tt := range []struct {
		name    string
		respond respondFn
		wantErr bool
	}{
		{
			name: "success path",
			respond: func(raw []byte) []byte {
				var req struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(raw, &req)
				return successBody(req.ID)
			},
			wantErr: false,
		},
		{
			name:    "daemon error path",
			respond: func([]byte) []byte { return []byte(`{"id":"x","error":{"code":"boom","message":"no"}}` + "\n") },
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newMockHerdrServer(t, tt.respond)
			client := NewLayoutClient(WithLayoutIDFunc(func() string { return "x" }))

			const iterations = 25
			for i := 0; i < iterations; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, err := client.ApplyLayout(ctx, srv.path, twoPaneParams())
				cancel()
				if tt.wantErr != (err != nil) {
					t.Fatalf("iteration %d: err = %v, wantErr = %v", i, err, tt.wantErr)
				}
			}

			// Every iteration must have completed a full round trip. A leaked
			// or reused connection would show up as a missing request line.
			if got := len(srv.gotRequests()); got != iterations {
				t.Errorf("server saw %d requests over %d calls, want %d", got, iterations, iterations)
			}
		})
	}
}

func TestLayoutClient_UsableThroughLayoutApplierSeam(t *testing.T) {
	// D2: the seam consumed by internal/templates must be satisfied by the
	// concrete client, and calling through the interface must reach the same
	// working transport rather than merely type-check.
	srv := newMockHerdrServer(t, func(raw []byte) []byte {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &req)
		return successBody(req.ID)
	})

	var applier LayoutApplier = NewLayoutClient()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := applier.ApplyLayout(ctx, srv.path, twoPaneParams())
	if err != nil {
		t.Fatalf("ApplyLayout through the seam returned error: %v", err)
	}
	if res.TabID != "tab-7" {
		t.Errorf("TabID = %q, want %q", res.TabID, "tab-7")
	}
}
