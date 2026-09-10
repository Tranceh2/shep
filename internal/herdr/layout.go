package herdr

// layout.go implements the dedicated JSON-RPC client for Herdr Protocol 22's
// `layout.apply` method over HERDR_SOCKET_PATH.
//
// This is deliberately a separate transport from the CommandRunner driver in
// driver.go. `layout.apply` is the only path that applies a whole pane tree
// atomically, and atomicity is the entire point: a piecemeal sequence of
// `herdr pane split` subprocesses can fail halfway and leave visible pane
// clutter behind, while one socket round trip either applies the layout or
// changes nothing.
//
// The wire framing was byte-verified against the bundled schema of Herdr
// 0.8.2 (`herdr api schema --json`, protocol 22, schema_version 1):
//
//	request  {"id":"<id>","method":"layout.apply","params":<LayoutApplyParams>}\n
//	success  {"id":"<id>","result":{"type":"layout_apply","layout":<LayoutDescription>}}
//	error    {"id":"<id>","error":{"code":"<string>","message":"<string>"}}
//
// Two details of that schema are easy to get wrong and are pinned by tests:
// `LayoutNode.command` is an argv ARRAY (not a shell string), and the error
// `code` is a STRING (not a JSON-RPC integer).

import (
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

// Node types and split directions accepted by Protocol 22. LayoutNode is an
// internally tagged union: `type` selects the variant, and each variant owns a
// disjoint set of fields.
const (
	// NodeTypePane is a leaf terminal.
	NodeTypePane = "pane"
	// NodeTypeSplit is a binary division of the space between two children.
	NodeTypeSplit = "split"

	// DirectionRight divides columns; `first` takes the left share.
	DirectionRight = "right"
	// DirectionDown divides rows; `first` takes the top share.
	DirectionDown = "down"
)

const (
	// maxLayoutResponseBytes bounds a single response frame. A hostile or
	// wedged daemon must not be able to grow the client's heap without limit,
	// so the decoder reads through an io.LimitReader (the same defense
	// herdrwatch applies to its control socket).
	maxLayoutResponseBytes = 1 << 20 // 1 MiB

	// defaultLayoutTimeout applies when the caller's context carries no
	// deadline. Without it a silent daemon would block the caller forever.
	defaultLayoutTimeout = 10 * time.Second
)

// Sentinel errors let callers branch on the failure class without matching on
// message text.
var (
	// ErrHerdrSocketUnavailable means the layout never reached the daemon:
	// the socket was missing, the dial failed, or the connection died before
	// a complete answer arrived. It always means "nothing was applied", which
	// is what makes fail-closed degradation safe.
	ErrHerdrSocketUnavailable = errors.New("herdr layout: socket unavailable")

	// ErrHerdrMalformedResponse means the daemon answered with something this
	// client cannot trust: invalid JSON, a truncated frame, an answer to a
	// different request id, or a success envelope with no layout body.
	ErrHerdrMalformedResponse = errors.New("herdr layout: malformed response")

	// ErrHerdrResponseTooLarge means the response exceeded
	// maxLayoutResponseBytes and was refused rather than buffered.
	ErrHerdrResponseTooLarge = errors.New("herdr layout: response exceeds size limit")

	// ErrInvalidLayout means the request was rejected locally, before any
	// byte reached the socket.
	ErrInvalidLayout = errors.New("herdr layout: invalid layout")
)

// HerdrRPCError is a rejection produced by the daemon itself. It is distinct
// from a transport failure: the daemon was reached, understood the request,
// and declined it. Code is a string because Protocol 22 uses symbolic codes
// such as "invalid_layout" or "workspace_not_found".
type HerdrRPCError struct {
	Code    string
	Message string
}

func (e *HerdrRPCError) Error() string {
	return fmt.Sprintf("herdr layout: daemon rejected layout.apply: %s: %s", e.Code, e.Message)
}

// LayoutNode is one node of the layout tree, mirroring Protocol 22's
// internally tagged LayoutNode union.
//
// Type == NodeTypePane uses Command/Cwd/Env/Label/PaneID.
// Type == NodeTypeSplit uses Direction/Ratio/First/Second.
//
// Command is an argv slice, NOT a shell string: the daemon executes it
// directly without a shell, so no quoting or metacharacter escaping applies.
type LayoutNode struct {
	Type string `json:"type"`

	// Split variant.
	Direction string      `json:"direction,omitempty"`
	Ratio     float64     `json:"ratio,omitempty"`
	First     *LayoutNode `json:"first,omitempty"`
	Second    *LayoutNode `json:"second,omitempty"`

	// Pane variant.
	Command []string          `json:"command,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Label   string            `json:"label,omitempty"`
	PaneID  string            `json:"pane_id,omitempty"`
}

// LayoutApplyParams is the `params` object of a layout.apply request. Only
// Root is required by the daemon; TabID targets an existing tab while
// TabLabel creates a new one.
type LayoutApplyParams struct {
	WorkspaceID string     `json:"workspace_id,omitempty"`
	TabID       string     `json:"tab_id,omitempty"`
	TabLabel    string     `json:"tab_label,omitempty"`
	Focus       bool       `json:"focus"`
	Root        LayoutNode `json:"root"`
}

// LayoutApplyResult is the applied layout the daemon reports back
// (LayoutDescription in the schema). Root is the resulting tree as the daemon
// materialized it, which may differ from the request once real pane ids are
// assigned.
type LayoutApplyResult struct {
	WorkspaceID   string      `json:"workspace_id"`
	TabID         string      `json:"tab_id"`
	Zoomed        bool        `json:"zoomed"`
	FocusedPaneID string      `json:"focused_pane_id"`
	Root          *LayoutNode `json:"root"`
}

// LayoutApplier is the seam consumed by callers that provision layouts. It
// exists so those callers can be tested by capturing the params they build,
// with no socket and no daemon in the loop.
type LayoutApplier interface {
	ApplyLayout(ctx context.Context, socketPath string, params LayoutApplyParams) (*LayoutApplyResult, error)
}

// layoutIDFunc produces the correlation id carried by a request and echoed in
// the response.
type layoutIDFunc func() string

// LayoutClient dispatches layout.apply over a Unix domain socket. It holds no
// connection between calls: each ApplyLayout dials, exchanges one frame, and
// closes. That keeps the client free of shared mutable state and safe for
// concurrent use.
type LayoutClient struct {
	dialer net.Dialer
	nextID layoutIDFunc
}

// LayoutOption configures a LayoutClient at construction.
type LayoutOption func(*LayoutClient)

// WithLayoutIDFunc injects the request id generator (intended for tests that
// assert on the exact bytes of a request frame).
func WithLayoutIDFunc(f layoutIDFunc) LayoutOption {
	return func(c *LayoutClient) {
		if f != nil {
			c.nextID = f
		}
	}
}

// layoutRequestSeq backs the default id generator. Ids only need to be unique
// within a connection, but a process-wide counter keeps them distinguishable
// in daemon logs across concurrent calls.
var layoutRequestSeq atomic.Uint64

func defaultLayoutID() string {
	return "shep:layout:" + strconv.FormatUint(layoutRequestSeq.Add(1), 10)
}

// NewLayoutClient builds a LayoutClient with the default id generator.
func NewLayoutClient(opts ...LayoutOption) *LayoutClient {
	c := &LayoutClient{nextID: defaultLayoutID}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Compile-time proof that the concrete client satisfies the seam.
var _ LayoutApplier = (*LayoutClient)(nil)

// layoutRequest is the outgoing JSON-RPC frame.
type layoutRequest struct {
	ID     string            `json:"id"`
	Method string            `json:"method"`
	Params LayoutApplyParams `json:"params"`
}

// layoutResponse is the union of the daemon's success and error envelopes.
// Exactly one of Result or Error is populated.
type layoutResponse struct {
	ID     string `json:"id"`
	Result *struct {
		Type   string             `json:"type"`
		Layout *LayoutApplyResult `json:"layout"`
	} `json:"result"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ApplyLayout applies params atomically through the daemon listening on
// socketPath.
//
// The call is fail-closed at every stage: the layout is validated before the
// dial, the connection carries a hard deadline derived from ctx, and any
// transport or protocol fault returns a nil result. A non-nil error therefore
// always means "no layout was applied", never "partially applied".
func (c *LayoutClient) ApplyLayout(ctx context.Context, socketPath string, params LayoutApplyParams) (*LayoutApplyResult, error) {
	// Validate first. A malformed tree must never produce a socket write,
	// because a half-understood layout is worse than no layout at all.
	if err := validateLayoutParams(socketPath, params); err != nil {
		return nil, err
	}

	deadline, ctxHasDeadline := ctx.Deadline()
	if !ctxHasDeadline {
		deadline = time.Now().Add(defaultLayoutTimeout)
	}

	conn, err := c.dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("%w: dial %s: %w", ErrHerdrSocketUnavailable, socketPath, err)
	}
	// The connection is owned entirely by this call, on every return path.
	// The close error is intentionally dropped: the layout outcome is already
	// decided by the response, and a close fault must not mask it.
	defer func() { _ = conn.Close() }()

	// A deadline alone does not cover mid-call cancellation, so a watcher
	// closes the connection when ctx ends. done stops that watcher before the
	// function returns so it cannot outlive the connection it guards.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("%w: set deadline: %w", ErrHerdrSocketUnavailable, err)
	}

	id := c.nextID()
	if err := writeLayoutRequest(conn, layoutRequest{ID: id, Method: "layout.apply", Params: params}); err != nil {
		return nil, classifyLayoutIOError(ctx, ctxHasDeadline, err)
	}

	res, err := readLayoutResponse(conn, id)
	if err != nil {
		return nil, classifyLayoutIOError(ctx, ctxHasDeadline, err)
	}
	return res, nil
}

// writeLayoutRequest encodes req as a single newline-terminated frame. The
// daemon reads one line per request, so the terminator is part of the
// protocol, not cosmetic.
func writeLayoutRequest(w io.Writer, req layoutRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode layout.apply request: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write layout.apply request: %w", err)
	}
	return nil
}

// readLayoutResponse decodes one bounded response frame and maps it onto the
// error taxonomy. wantID is the id the daemon must echo.
func readLayoutResponse(r io.Reader, wantID string) (*LayoutApplyResult, error) {
	// Read one byte past the limit so an oversized frame is detectable rather
	// than silently truncated into a "malformed" verdict.
	limited := io.LimitReader(r, maxLayoutResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read layout.apply response: %w", err)
	}
	if len(raw) > maxLayoutResponseBytes {
		return nil, fmt.Errorf("%w: exceeded %d bytes", ErrHerdrResponseTooLarge, maxLayoutResponseBytes)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: daemon closed the connection without a response", ErrHerdrMalformedResponse)
	}

	var resp layoutResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHerdrMalformedResponse, err)
	}

	// A mismatched id means this frame answers some other request; trusting it
	// would attribute an unrelated outcome to this layout.
	if resp.ID != wantID {
		return nil, fmt.Errorf("%w: response id %q does not match request id %q",
			ErrHerdrMalformedResponse, resp.ID, wantID)
	}

	// The daemon reached a verdict and declined. This is not a transport
	// failure and must not be reported as one.
	if resp.Error != nil {
		return nil, &HerdrRPCError{Code: resp.Error.Code, Message: resp.Error.Message}
	}

	if resp.Result == nil || resp.Result.Layout == nil {
		return nil, fmt.Errorf("%w: success envelope carries no layout body", ErrHerdrMalformedResponse)
	}
	return resp.Result.Layout, nil
}

// classifyLayoutIOError maps a transport fault onto the sentinel taxonomy.
// Context faults are wrapped alongside ErrHerdrSocketUnavailable so callers
// can match either the cause (why it stopped) or the class (nothing applied).
//
// ctxHasDeadline records whether the caller's context supplied the deadline
// installed on the connection. It is needed because the socket deadline and
// the context deadline expire at the same instant: the kernel can surface
// os.ErrDeadlineExceeded a hair before ctx.Err() becomes non-nil, which would
// otherwise make an ordinary caller timeout report as an unclassified socket
// fault in a timing-dependent way.
func classifyLayoutIOError(ctx context.Context, ctxHasDeadline bool, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%w: %w: %w", ErrHerdrSocketUnavailable, ctxErr, err)
	}
	if ctxHasDeadline && errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %w: %w", ErrHerdrSocketUnavailable, context.DeadlineExceeded, err)
	}
	if errors.Is(err, ErrHerdrMalformedResponse) || errors.Is(err, ErrHerdrResponseTooLarge) {
		return err
	}
	var rpcErr *HerdrRPCError
	if errors.As(err, &rpcErr) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrHerdrSocketUnavailable, err)
}

// validateLayoutParams enforces every precondition that can be checked
// locally. Running before the dial is what makes an invalid layout a pure
// no-op rather than a partially applied one (R8.1).
func validateLayoutParams(socketPath string, params LayoutApplyParams) error {
	if socketPath == "" {
		return fmt.Errorf("%w: empty socket path (HERDR_SOCKET_PATH unset?)", ErrHerdrSocketUnavailable)
	}
	return validateLayoutNode(&params.Root, 0)
}

// maxLayoutDepth bounds recursion so a cyclic or pathologically deep tree is
// refused instead of exhausting the stack.
const maxLayoutDepth = 64

func validateLayoutNode(n *LayoutNode, depth int) error {
	if n == nil {
		return fmt.Errorf("%w: nil layout node", ErrInvalidLayout)
	}
	if depth > maxLayoutDepth {
		return fmt.Errorf("%w: layout nests deeper than %d levels", ErrInvalidLayout, maxLayoutDepth)
	}

	switch n.Type {
	case NodeTypePane:
		return nil
	case NodeTypeSplit:
		if n.Direction != DirectionRight && n.Direction != DirectionDown {
			return fmt.Errorf("%w: split direction %q must be %q or %q",
				ErrInvalidLayout, n.Direction, DirectionRight, DirectionDown)
		}
		if err := validateLayoutNode(n.First, depth+1); err != nil {
			return err
		}
		return validateLayoutNode(n.Second, depth+1)
	default:
		return fmt.Errorf("%w: node type %q must be %q or %q",
			ErrInvalidLayout, n.Type, NodeTypePane, NodeTypeSplit)
	}
}
