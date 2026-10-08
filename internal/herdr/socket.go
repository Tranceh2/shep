package herdr

// socket.go is the request/response client for the Herdr server socket
// (HERDR_SOCKET_PATH), the same transport the herdr CLI uses underneath.
// Talking to the socket directly skips launching the CLI: that launch costs
// 15-20 ms when the binary is cached and around 200 ms once the system has
// evicted it, while one socket round trip takes a few milliseconds.
//
// The framing (Herdr protocol 22) was captured from the CLI itself: one
// connection carries one request, written as a newline-terminated frame; the
// server answers with one frame and closes the connection.
//
//	request  {"id":"<id>","method":"<method>","params":{...}}\n
//	success  {"id":"<id>","result":{"type":"<type>",...}}
//	error    {"id":"<id>","error":{"code":"<string>","message":"<string>"}}
//
// A request the server cannot parse, such as a method an older Herdr does not
// know, is answered with code "invalid_request" and an empty id.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	// maxResponseBytes bounds one response frame, so a wedged or hostile
	// server cannot grow the client's heap without limit.
	maxResponseBytes = 16 << 20

	// defaultSocketTimeout applies when the caller's context carries no
	// deadline. Without it a silent server would block the caller forever.
	defaultSocketTimeout = 10 * time.Second

	// codeInvalidRequest is the error code of a request the server could not
	// parse: an older Herdr answers it for a method it does not know.
	codeInvalidRequest = "invalid_request"
)

// Sentinel errors let callers branch on the failure class without matching on
// message text.
var (
	// ErrHerdrSocketUnavailable means the request never reached the server:
	// the socket was missing, the dial failed, or the connection died before
	// a complete answer arrived.
	ErrHerdrSocketUnavailable = errors.New("herdr socket unavailable")

	// ErrHerdrMalformedResponse means the server answered with something this
	// client cannot trust: invalid JSON, a truncated frame, an answer to a
	// different request id, or a success envelope without the expected body.
	ErrHerdrMalformedResponse = errors.New("herdr: malformed response")

	// ErrHerdrResponseTooLarge means the response exceeded the frame bound and
	// was refused rather than buffered.
	ErrHerdrResponseTooLarge = errors.New("herdr: response exceeds size limit")
)

// HerdrRPCError is a rejection produced by the server itself: it was reached,
// understood the request, and declined it. Code is a symbolic string such as
// "workspace_not_found" or "invalid_layout".
type HerdrRPCError struct {
	Method  string
	Code    string
	Message string
}

func (e *HerdrRPCError) Error() string {
	return fmt.Sprintf("herdr %s: %s: %s", e.Method, e.Code, e.Message)
}

// unknownMethod reports a rejection that means "this Herdr does not speak
// that method": the caller can fall back to the CLI of the same Herdr, which
// knows its own protocol.
func unknownMethod(err error) bool {
	var rpcErr *HerdrRPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == codeInvalidRequest
}

// requestSeq backs nextRequestID. Ids only need to be unique per connection,
// but a process-wide counter keeps them distinguishable in server logs.
var requestSeq atomic.Uint64

func nextRequestID(method string) string {
	return "shep:" + method + ":" + strconv.FormatUint(requestSeq.Add(1), 10)
}

// socketRequest is the outgoing frame.
type socketRequest struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// socketResponse is the union of the server's success and error envelopes.
type socketResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// callSocket sends one request to the server listening on path and returns
// the response's result. limit bounds the response frame. The connection is
// owned by the call: it carries ctx's deadline (or defaultSocketTimeout),
// closes when ctx ends, and is closed on every return path.
func callSocket(ctx context.Context, path, id, method string, params any, limit int64) (json.RawMessage, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty socket path (HERDR_SOCKET_PATH unset?)", ErrHerdrSocketUnavailable)
	}
	payload, err := json.Marshal(socketRequest{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", method, err)
	}
	payload = append(payload, '\n')

	deadline, ctxHasDeadline := ctx.Deadline()
	if !ctxHasDeadline {
		deadline = time.Now().Add(defaultSocketTimeout)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w: dial %s: %w", ErrHerdrSocketUnavailable, path, err)
	}
	// The close error is dropped: the outcome is decided by the response.
	defer func() { _ = conn.Close() }()
	// A deadline does not cover mid-call cancellation, so ctx ending closes
	// the connection too.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("%w: set deadline: %w", ErrHerdrSocketUnavailable, err)
	}

	if _, err := conn.Write(payload); err != nil {
		return nil, classifyIOError(ctx, ctxHasDeadline, fmt.Errorf("write %s request: %w", method, err))
	}
	raw, err := readFrame(conn, limit)
	if err != nil {
		return nil, classifyIOError(ctx, ctxHasDeadline, fmt.Errorf("read %s response: %w", method, err))
	}

	var resp socketResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrHerdrMalformedResponse, method, err)
	}
	// An unparsable request is answered with an empty id; any other mismatch
	// answers some other request, and trusting it would attribute an
	// unrelated outcome to this one.
	if resp.Error != nil && (resp.ID == id || resp.ID == "") {
		return nil, &HerdrRPCError{Method: method, Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if resp.ID != id {
		return nil, fmt.Errorf("%w: %s: response id %q does not match request id %q", ErrHerdrMalformedResponse, method, resp.ID, id)
	}
	if len(resp.Result) == 0 {
		return nil, fmt.Errorf("%w: %s: success envelope carries no result", ErrHerdrMalformedResponse, method)
	}
	return resp.Result, nil
}

// readFrame reads one response frame: up to the newline, or to the end of
// the stream when the server closes without one. A frame larger than limit is
// refused.
func readFrame(r io.Reader, limit int64) ([]byte, error) {
	// One byte past the limit makes an oversized frame detectable rather than
	// silently truncated into a "malformed" verdict.
	raw, err := bufio.NewReader(io.LimitReader(r, limit+1)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: exceeded %d bytes", ErrHerdrResponseTooLarge, limit)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: the server closed the connection without a response", ErrHerdrMalformedResponse)
	}
	return raw, nil
}

// classifyIOError maps a transport fault onto the sentinel taxonomy. Context
// faults are wrapped alongside ErrHerdrSocketUnavailable so callers can match
// either the cause (why it stopped) or the class (nothing reached the server
// in full).
//
// ctxHasDeadline records whether the caller's context supplied the deadline
// installed on the connection: the socket and context deadlines expire at the
// same instant, and the kernel can surface os.ErrDeadlineExceeded a hair
// before ctx.Err() becomes non-nil, which would otherwise make an ordinary
// caller timeout report as an unclassified socket fault.
func classifyIOError(ctx context.Context, ctxHasDeadline bool, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%w: %w: %w", ErrHerdrSocketUnavailable, ctxErr, err)
	}
	if ctxHasDeadline && errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %w: %w", ErrHerdrSocketUnavailable, context.DeadlineExceeded, err)
	}
	if errors.Is(err, ErrHerdrMalformedResponse) || errors.Is(err, ErrHerdrResponseTooLarge) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrHerdrSocketUnavailable, err)
}
