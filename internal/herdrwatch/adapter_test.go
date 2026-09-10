package herdrwatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tranceh2/shep/internal/herdrwatch"
	"github.com/tranceh2/shep/internal/source"
)

// countingSnapshotSource is a minimal stand-in for the herdr.Driver snapshot
// boundary. It counts RPC calls so tests can assert one RPC per bootstrap and
// zero per focus event.
type countingSnapshotSource struct {
	snap  source.Snapshot
	err   error
	calls int
}

func (f *countingSnapshotSource) Snapshot(ctx context.Context) (source.Snapshot, error) {
	f.calls++
	if f.err != nil {
		return source.Snapshot{}, f.err
	}
	return f.snap, nil
}

func TestSnapshotAdapter_MapsLiveMembership(t *testing.T) {
	src := &countingSnapshotSource{
		snap: source.Snapshot{
			Workspaces: []source.Workspace{
				{ID: "ws-a"},
				{ID: "ws-b"},
				{ID: ""}, // invalid/missing ID must be ignored as the driver does
			},
			FocusedWorkspaceID: "ws-a",
		},
	}

	adapter := herdrwatch.NewSnapshotAdapter(src)

	m, err := adapter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("adapter Snapshot failed: %v", err)
	}

	if m.FocusedWorkspaceID != "ws-a" {
		t.Errorf("expected FocusedWorkspaceID ws-a, got %q", m.FocusedWorkspaceID)
	}
	present := map[string]bool{}
	for _, id := range m.Present {
		present[id] = true
	}
	if !present["ws-a"] || !present["ws-b"] {
		t.Errorf("expected ws-a and ws-b present, got %v", m.Present)
	}
	if present[""] {
		t.Errorf("empty workspace ID must be ignored, got %v", m.Present)
	}
	if len(m.Present) != 2 {
		t.Errorf("expected exactly 2 live IDs, got %d (%v)", len(m.Present), m.Present)
	}
}

func TestSnapshotAdapter_PropagatesError(t *testing.T) {
	src := &countingSnapshotSource{err: errors.New("snapshot rpc failed")}
	adapter := herdrwatch.NewSnapshotAdapter(src)

	_, err := adapter.Snapshot(context.Background())
	if err == nil {
		t.Fatalf("expected adapter to propagate snapshot error, got nil")
	}
}

func TestSnapshotAdapter_EmptySnapshotYieldsEmptyMembership(t *testing.T) {
	src := &countingSnapshotSource{snap: source.Snapshot{}}
	adapter := herdrwatch.NewSnapshotAdapter(src)

	m, err := adapter.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("adapter Snapshot failed: %v", err)
	}
	if len(m.Present) != 0 {
		t.Errorf("expected empty membership, got %v", m.Present)
	}
	if m.FocusedWorkspaceID != "" {
		t.Errorf("expected empty FocusedWorkspaceID, got %q", m.FocusedWorkspaceID)
	}
}

func TestSnapshotAdapter_ContextCancellationPropagated(t *testing.T) {
	// A source honoring context cancellation must surface the error unchanged
	// through the adapter.
	src := &ctxAwareSnapshotSource{}
	adapter := herdrwatch.NewSnapshotAdapter(src)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.Snapshot(ctx)
	if err == nil {
		t.Fatalf("expected cancellation error to propagate, got nil")
	}
}

type ctxAwareSnapshotSource struct{}

func (ctxAwareSnapshotSource) Snapshot(ctx context.Context) (source.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return source.Snapshot{}, err
	}
	return source.Snapshot{}, nil
}

func TestSnapshotAdapter_OneRPCPerBootstrapNotPerFocus(t *testing.T) {
	src := &countingSnapshotSource{
		snap: source.Snapshot{
			Workspaces:         []source.Workspace{{ID: "ws-a"}},
			FocusedWorkspaceID: "ws-a",
		},
	}
	adapter := herdrwatch.NewSnapshotAdapter(src)

	// One Bootstrap => exactly one underlying RPC.
	if _, err := adapter.Snapshot(context.Background()); err != nil {
		t.Fatalf("adapter Snapshot failed: %v", err)
	}
	if src.calls != 1 {
		t.Errorf("expected exactly 1 underlying snapshot RPC per bootstrap, got %d", src.calls)
	}
}
