package herdrwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// maxEventLineBytes bounds a single NDJSON event/ack line so a hostile or
// runaway server cannot force unbounded allocation. Lines longer than this are
// treated as a stream error (readiness invalidated by the caller).
const maxEventLineBytes = 1 << 20 // 1 MiB

// subscribeRequest is the events.subscribe request sent on a dedicated stream
// connection. Subscription type values are DOTTED (herdr 0.8.2), e.g.
// "workspace.focused". The wire-level pushed events use underscored spelling
// ("workspace_focused"); that asymmetry is handled by the Owner parser.
type subscribeRequest struct {
	ID     string             `json:"id"`
	Method string             `json:"method"`
	Params subscribeReqParams `json:"params"`
}

type subscribeReqParams struct {
	Subscriptions []subscriptionType `json:"subscriptions"`
}

type subscriptionType struct {
	Type string `json:"type"`
}

// ackEnvelope is the first response line to events.subscribe. A successful ack
// carries result.type == "subscription_started"; a failure carries error.
type ackEnvelope struct {
	ID     string `json:"id"`
	Result *struct {
		Type string `json:"type"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const (
	subscribeID    = "shep-watch-history"
	ackTypeStarted = "subscription_started"
)

// Stream is a held-open events.subscribe connection. It owns a single bufio
// reader shared across the ack and the subsequent event lines so a buffered
// first event cannot be lost to reader read-ahead.
type Stream struct {
	conn net.Conn
	r    *bufio.Reader
}

// Subscribe writes an events.subscribe request for the given DOTTED
// subscription types on conn, reads and validates the subscription ack using a
// single shared reader, and returns a Stream positioned to read event lines.
func Subscribe(ctx context.Context, conn net.Conn, dottedTypes []string) (*Stream, error) {
	subs := make([]subscriptionType, 0, len(dottedTypes))
	for _, t := range dottedTypes {
		subs = append(subs, subscriptionType{Type: t})
	}
	req := subscribeRequest{
		ID:     subscribeID,
		Method: "events.subscribe",
		Params: subscribeReqParams{Subscriptions: subs},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal subscribe request: %w", err)
	}
	// Honor a caller deadline for the subscribe+ack handshake; cleared once the
	// held-open stream begins so ReadEvent blocks on live events.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}

	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, fmt.Errorf("write subscribe request: %w", err)
	}

	r := bufio.NewReader(conn)
	ackLine, err := readLine(r)
	if err != nil {
		return nil, fmt.Errorf("read subscribe ack: %w", err)
	}

	var ack ackEnvelope
	if err := json.Unmarshal(ackLine, &ack); err != nil {
		return nil, fmt.Errorf("decode subscribe ack: %w", err)
	}
	if ack.Error != nil {
		// Classified, sanitized: do not surface the raw internal payload as the
		// primary message; the caller logs safely.
		return nil, fmt.Errorf("subscribe rejected by server")
	}
	if ack.Result == nil || ack.Result.Type != ackTypeStarted {
		return nil, errors.New("subscribe ack missing subscription_started")
	}

	return &Stream{conn: conn, r: r}, nil
}

// ReadEvent reads the next NDJSON event line from the held-open stream using the
// same shared reader that consumed the ack, so a buffered first event is never
// dropped. An io.EOF (or any read error) is returned so the caller invalidates
// readiness and reconnects.
func (s *Stream) ReadEvent() ([]byte, error) {
	return readLine(s.r)
}

// ReadBuffered drains any complete event lines already present in the stream's
// reader buffer without performing a blocking network read.
func (s *Stream) ReadBuffered() [][]byte {
	var lines [][]byte
	for s != nil && s.r != nil && s.r.Buffered() > 0 {
		line, err := readLine(s.r)
		if err != nil {
			break
		}
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// Close closes the underlying stream connection.
func (s *Stream) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

// readLine reads one newline-delimited line with a bounded length. A line
// exceeding maxEventLineBytes is a stream error rather than an unbounded read.
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		if err == io.EOF && strings.TrimSpace(line) != "" {
			// Final line without a trailing newline.
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
