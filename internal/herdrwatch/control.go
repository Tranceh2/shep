package herdrwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	maxRequestSizeBytes  = 16384 // 16 KB
	maxResponseSizeBytes = 65536 // 64 KB
	defaultTimeout       = 3 * time.Second
)

// StateProvider defines the contract for supplying MRU history state to the control socket.
type StateProvider interface {
	State(ctx context.Context, sessionKey string) (StateResp, error)
}

// Server hosts the Unix control socket server for supplying MRU readiness and state to CLI clients.
type Server struct {
	provider StateProvider
	maxConns chan struct{}
	wg       sync.WaitGroup
}

// NewServer constructs a new control Server.
func NewServer(provider StateProvider) *Server {
	return &Server{
		provider: provider,
		maxConns: make(chan struct{}, 16),
	}
}

// Serve runs the control socket server loop accepting client connections on l.
// Exits cleanly when ctx is cancelled or l is closed.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	defer func() { _ = l.Close() }()

	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				s.wg.Wait()
				return nil
			default:
				s.wg.Wait()
				return fmt.Errorf("accept control connection: %w", err)
			}
		}

		select {
		case s.maxConns <- struct{}{}:
			s.wg.Add(1)
			go func(c net.Conn) {
				defer func() {
					<-s.maxConns
					_ = c.Close()
					s.wg.Done()
				}()
				_ = s.HandleConn(ctx, c)
			}(conn)
		case <-ctx.Done():
			_ = conn.Close()
			s.wg.Wait()
			return nil
		}
	}
}

// HandleConn handles a single client control socket connection.
func (s *Server) HandleConn(ctx context.Context, conn net.Conn) error {
	_ = conn.SetDeadline(time.Now().Add(defaultTimeout))

	var req StateReq
	limitedReader := io.LimitReader(conn, maxRequestSizeBytes)
	if err := json.NewDecoder(limitedReader).Decode(&req); err != nil {
		resp := StateResp{
			Ready:           false,
			Epoch:           0,
			MRU:             []string{},
			ClassifiedError: "malformed or oversized control request",
		}
		_ = json.NewEncoder(conn).Encode(resp)
		return fmt.Errorf("decode request: %w", err)
	}

	connCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	resp, err := s.provider.State(connCtx, req.SessionKey)
	if err != nil {
		resp = StateResp{
			Ready:           false,
			Epoch:           0,
			MRU:             []string{},
			ClassifiedError: "history store error",
		}
	}

	// Defensive copy of MRU slice to prevent data races
	if resp.MRU != nil {
		copied := make([]string, len(resp.MRU))
		copy(copied, resp.MRU)
		resp.MRU = copied
	} else {
		resp.MRU = []string{}
	}

	return json.NewEncoder(conn).Encode(resp)
}

// QueryState issues a StateReq over conn and decodes the StateResp response.
func QueryState(ctx context.Context, conn net.Conn, sessionKey string) (StateResp, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultTimeout)
	}
	_ = conn.SetDeadline(deadline)

	req := StateReq{SessionKey: sessionKey}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return StateResp{Ready: false, ClassifiedError: "control connection failed"}, fmt.Errorf("encode request: %w", err)
	}

	var resp StateResp
	if err := DecodeResp(conn, &resp); err != nil {
		return StateResp{Ready: false, ClassifiedError: "control response read failed"}, fmt.Errorf("decode response: %w", err)
	}

	return resp, nil
}

// DecodeResp decodes a StateResp from r with an upper size bound.
func DecodeResp(r io.Reader, resp *StateResp) error {
	if resp == nil {
		return errors.New("nil response target")
	}
	limited := io.LimitReader(r, maxResponseSizeBytes)
	return json.NewDecoder(limited).Decode(resp)
}
