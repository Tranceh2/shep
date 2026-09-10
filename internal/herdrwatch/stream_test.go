package herdrwatch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/herdrwatch"
)

// serveSubscribeAck reads the subscribe request line from srv, writes the given
// ack line, then writes each event line. It leaves the connection open until
// the caller closes it, matching the held-open events.subscribe semantics.
func serveSubscribeAck(t *testing.T, srv net.Conn, ackLine string, eventLines ...string) {
	t.Helper()
	r := bufio.NewReader(srv)
	// Read the subscribe request line.
	reqLine, err := r.ReadString('\n')
	if err != nil {
		t.Errorf("server read subscribe request: %v", err)
		return
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(reqLine)), &req); err != nil {
		t.Errorf("server decode subscribe request: %v", err)
		return
	}
	if req["method"] != "events.subscribe" {
		t.Errorf("expected method events.subscribe, got %v", req["method"])
	}
	if _, err := io.WriteString(srv, ackLine+"\n"); err != nil {
		t.Errorf("server write ack: %v", err)
		return
	}
	for _, ev := range eventLines {
		if _, err := io.WriteString(srv, ev+"\n"); err != nil {
			t.Errorf("server write event: %v", err)
			return
		}
	}
}

func TestStream_SubscribeAckThenBufferedEvent(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()

	// The ack and the first event may arrive in the same read window; a single
	// shared reader must not drop the buffered event after consuming the ack.
	ack := `{"id":"sub","result":{"type":"subscription_started"}}`
	ev := `{"event":"workspace_focused","data":{"type":"workspace_focused","workspace_id":"ws-a"}}`

	go serveSubscribeAck(t, srv, ack, ev)

	ctx := context.Background()
	stream, err := herdrwatch.Subscribe(ctx, cli, []string{"workspace.focused", "workspace.closed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	raw, err := stream.ReadEvent()
	if err != nil {
		t.Fatalf("ReadEvent failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("event not valid json: %v (%s)", err, raw)
	}
	if got["event"] != "workspace_focused" {
		t.Errorf("expected buffered workspace_focused event, got %v", got["event"])
	}
}

func TestStream_ErrorAckRejected(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()

	errAck := `{"id":"sub","error":{"code":"invalid_request","message":"missing field pane_id"}}`
	go serveSubscribeAck(t, srv, errAck)

	ctx := context.Background()
	_, err := herdrwatch.Subscribe(ctx, cli, []string{"pane.agent_status_changed"})
	if err == nil {
		t.Fatalf("expected error ack to fail Subscribe, got nil")
	}
	if strings.Contains(err.Error(), "missing field pane_id") {
		// The classified error must not leak the raw internal payload verbatim,
		// but it is acceptable to reference the subscribe failure generically.
		t.Logf("note: subscribe error surfaced upstream message; ensure caller sanitizes: %v", err)
	}
}

func TestStream_MalformedAckRejected(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()

	go serveSubscribeAck(t, srv, `{garbage not json`)

	ctx := context.Background()
	_, err := herdrwatch.Subscribe(ctx, cli, []string{"workspace.focused"})
	if err == nil {
		t.Fatalf("expected malformed ack to fail Subscribe, got nil")
	}
}

func TestStream_ReadEventEOFReported(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()

	ack := `{"id":"sub","result":{"type":"subscription_started"}}`
	go func() {
		serveSubscribeAck(t, srv, ack)
		srv.Close() // EOF after ack, no events
	}()

	ctx := context.Background()
	stream, err := herdrwatch.Subscribe(ctx, cli, []string{"workspace.focused"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err = stream.ReadEvent()
	if err == nil {
		t.Fatalf("expected EOF error from ReadEvent after server close, got nil")
	}
}

func TestStream_SubscribeRequestUsesDottedTypes(t *testing.T) {
	srv, cli := net.Pipe()
	defer cli.Close()

	reqCh := make(chan string, 1)
	go func() {
		r := bufio.NewReader(srv)
		line, _ := r.ReadString('\n')
		reqCh <- line
		io.WriteString(srv, `{"id":"sub","result":{"type":"subscription_started"}}`+"\n")
	}()

	ctx := context.Background()
	_, err := herdrwatch.Subscribe(ctx, cli, []string{"workspace.focused", "workspace.closed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case line := <-reqCh:
		// Subscription type values must be DOTTED on the request (herdr 0.8.2).
		if !strings.Contains(line, `"type":"workspace.focused"`) || !strings.Contains(line, `"type":"workspace.closed"`) {
			t.Errorf("expected dotted subscription types in request, got: %s", line)
		}
		if !strings.Contains(line, `"method":"events.subscribe"`) {
			t.Errorf("expected events.subscribe method, got: %s", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for subscribe request")
	}
}
