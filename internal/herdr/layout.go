package herdr

// layout.go implements Herdr Protocol 22's `layout.apply` method over
// HERDR_SOCKET_PATH (framing in socket.go).
//
// `layout.apply` is the only path that applies a whole pane tree atomically,
// and atomicity is the entire point: a piecemeal sequence of split requests
// can fail halfway and leave visible pane clutter behind, while one round trip
// either applies the layout or changes nothing.
//
// The request and result shapes were byte-verified against the bundled schema
// of Herdr 0.8.2 (`herdr api schema --json`, protocol 22, schema_version 1):
//
//	request  {"id":"<id>","method":"layout.apply","params":<LayoutApplyParams>}\n
//	success  {"id":"<id>","result":{"type":"layout_apply","layout":<LayoutDescription>}}
//
// Two details of that schema are easy to get wrong and are pinned by tests:
// `LayoutNode.command` is an argv ARRAY (not a shell string), and the error
// `code` is a STRING (not a JSON-RPC integer).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
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
)

// ErrInvalidLayout means the request was rejected locally, before any byte
// reached the socket.
var ErrInvalidLayout = errors.New("herdr layout: invalid layout")

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
	raw, err := callSocket(ctx, socketPath, c.nextID(), "layout.apply", params, maxLayoutResponseBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Layout *LayoutApplyResult `json:"layout"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("%w: layout.apply: %w", ErrHerdrMalformedResponse, err)
	}
	if result.Layout == nil {
		return nil, fmt.Errorf("%w: success envelope carries no layout body", ErrHerdrMalformedResponse)
	}
	return result.Layout, nil
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
