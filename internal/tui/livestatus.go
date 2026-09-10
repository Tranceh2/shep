package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

const (
	subscriptionPaneAgentStatus = "pane.agent_status_changed"
	eventPaneAgentStatusChanged = "pane_agent_status_changed"
	maxEventLineBytes           = 1 << 20 // 1 MiB
	liveStatusStartupTimeout    = 250 * time.Millisecond

	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// StatusEvent represents a normalized agent status event from the wire.
type StatusEvent struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	Status      string `json:"status"`
	Seq         int64  `json:"seq,omitempty"`
}

// StatusStream is a held-open status subscription stream.
type StatusStream interface {
	ReadEvent() (StatusEvent, error)
	ReadBuffered() []StatusEvent
	Close() error
}

// StatusDialer connects to the Herdr events endpoint and initiates a status stream.
type StatusDialer interface {
	Dial(ctx context.Context) (StatusStream, error)
}

type unixStatusDialer struct {
	socketPath string
}

// NewUnixStatusDialer constructs a StatusDialer for a Unix domain socket path.
func NewUnixStatusDialer(socketPath string) StatusDialer {
	return &unixStatusDialer{socketPath: socketPath}
}

func (d *unixStatusDialer) Dial(ctx context.Context) (StatusStream, error) {
	if strings.TrimSpace(d.socketPath) == "" {
		return nil, errors.New("empty socket path")
	}
	startupCtx, cancel := context.WithTimeout(ctx, liveStatusStartupTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(startupCtx, "unix", d.socketPath)
	if err != nil {
		return nil, err
	}
	stream, err := SubscribeStatus(startupCtx, conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return stream, nil
}

// FuncStatusDialer adapts a function into a StatusDialer.
type FuncStatusDialer func(ctx context.Context) (StatusStream, error)

func (f FuncStatusDialer) Dial(ctx context.Context) (StatusStream, error) { return f(ctx) }

type statusSubscribeReq struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params struct {
		Subscriptions []struct {
			Type string `json:"type"`
		} `json:"subscriptions"`
	} `json:"params"`
}

type statusAckEnvelope struct {
	ID     string `json:"id"`
	Result *struct {
		Type string `json:"type"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type statusWireEvent struct {
	Event string `json:"event"`
	Data  struct {
		PaneID      string `json:"pane_id"`
		WorkspaceID string `json:"workspace_id"`
		TabID       string `json:"tab_id"`
		AgentStatus string `json:"agent_status"`
		Status      string `json:"status"`
	} `json:"data"`
}

type statusStreamImpl struct {
	conn net.Conn
	r    *bufio.Reader
}

// SubscribeStatus writes events.subscribe on conn, verifies ack, and returns a StatusStream.
func SubscribeStatus(ctx context.Context, conn net.Conn) (StatusStream, error) {
	if conn == nil {
		return nil, errors.New("nil status connection")
	}
	var req statusSubscribeReq
	req.ID = "shep-live-status"
	req.Method = "events.subscribe"
	req.Params.Subscriptions = []struct {
		Type string `json:"type"`
	}{{Type: subscriptionPaneAgentStatus}}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal subscribe request: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, fmt.Errorf("write subscribe request: %w", err)
	}

	r := bufio.NewReader(conn)
	ackLine, err := readBoundedLine(r)
	if err != nil {
		return nil, fmt.Errorf("read subscribe ack: %w", err)
	}

	var ack statusAckEnvelope
	if err := json.Unmarshal(ackLine, &ack); err != nil {
		return nil, fmt.Errorf("decode subscribe ack: %w", err)
	}
	if ack.Error != nil {
		return nil, fmt.Errorf("subscribe rejected by server")
	}
	if ack.Result == nil || ack.Result.Type != "subscription_started" {
		return nil, errors.New("subscribe ack missing subscription_started")
	}
	return &statusStreamImpl{conn: conn, r: r}, nil
}

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case StatusIdle:
		return StatusIdle
	case StatusWorking:
		return StatusWorking
	case StatusBlocked:
		return StatusBlocked
	case StatusDone:
		return StatusDone
	default:
		return StatusUnknown
	}
}

func parseWireEvent(line []byte) (StatusEvent, bool) {
	var wire statusWireEvent
	if err := json.Unmarshal(line, &wire); err != nil || wire.Event != eventPaneAgentStatusChanged {
		return StatusEvent{}, false
	}
	status := wire.Data.AgentStatus
	if status == "" {
		status = wire.Data.Status
	}
	return StatusEvent{
		PaneID:      wire.Data.PaneID,
		WorkspaceID: wire.Data.WorkspaceID,
		TabID:       wire.Data.TabID,
		Status:      normalizeStatus(status),
	}, true
}

func (s *statusStreamImpl) ReadEvent() (StatusEvent, error) {
	for {
		line, err := readBoundedLine(s.r)
		if err != nil {
			return StatusEvent{}, err
		}
		if ev, ok := parseWireEvent(line); ok {
			return ev, nil
		}
	}
}

func (s *statusStreamImpl) ReadBuffered() []StatusEvent {
	var events []StatusEvent
	for s != nil && s.r != nil && s.r.Buffered() > 0 {
		line, err := readBoundedLine(s.r)
		if err != nil {
			break
		}
		if ev, ok := parseWireEvent(line); ok {
			events = append(events, ev)
		}
	}
	return events
}

func (s *statusStreamImpl) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func readBoundedLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		if err == io.EOF && strings.TrimSpace(line) != "" {
			if len(line) > maxEventLineBytes {
				return nil, errors.New("stream line exceeds maximum size")
			}
			return []byte(strings.TrimSpace(line)), nil
		}
		return nil, err
	}
	if len(line) > maxEventLineBytes {
		return nil, errors.New("stream line exceeds maximum size")
	}
	return []byte(strings.TrimSpace(line)), nil
}

type liveStatus struct {
	events chan StatusEvent
	cancel context.CancelFunc
	stream StatusStream
	done   chan struct{}
	closed uint32
}

func startLiveStatus(ctx context.Context, dialer StatusDialer) *liveStatus {
	if dialer == nil {
		return nil
	}
	subCtx, cancel := context.WithCancel(ctx)
	dialCtx, cancelStartup := context.WithTimeout(subCtx, liveStatusStartupTimeout)
	stream, err := dialer.Dial(dialCtx)
	cancelStartup()
	if err != nil {
		cancel()
		return nil
	}
	ls := &liveStatus{
		events: make(chan StatusEvent, 32),
		cancel: cancel,
		stream: stream,
		done:   make(chan struct{}),
	}
	go ls.readLoop(subCtx)
	return ls
}

func (ls *liveStatus) readLoop(ctx context.Context) {
	defer close(ls.done)
	defer close(ls.events)
	for {
		ev, err := ls.stream.ReadEvent()
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case ls.events <- ev:
		default:
			select {
			case <-ls.events:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case ls.events <- ev:
			default:
			}
		}
	}
}

func (ls *liveStatus) Events() <-chan StatusEvent {
	if ls == nil {
		return nil
	}
	return ls.events
}

func (ls *liveStatus) Close() error {
	if ls == nil || !atomic.CompareAndSwapUint32(&ls.closed, 0, 1) {
		return nil
	}
	if ls.cancel != nil {
		ls.cancel()
	}
	var err error
	if ls.stream != nil {
		err = ls.stream.Close()
	}
	if ls.done != nil {
		<-ls.done
	}
	return err
}
