package herdrwatch

import (
	"context"
	"fmt"

	"github.com/tranceh2/shep/internal/source"
)

// SnapshotSource is the narrow snapshot boundary the adapter needs. It is
// satisfied by the real *herdr.Driver (whose Snapshot has this exact
// signature) without depending on the full ~18-method driver surface, and is
// trivially faked in tests.
type SnapshotSource interface {
	Snapshot(ctx context.Context) (source.Snapshot, error)
}

// SnapshotAdapter projects a source.Snapshot onto the owner's Membership. It
// performs exactly one underlying snapshot RPC per call and is invoked only at
// bootstrap/reconnect, never per focus event. The production caller (Unit 3
// watch-history wiring) constructs the driver against the same
// HERDR_SOCKET_PATH used by the raw event stream.
type SnapshotAdapter struct {
	src SnapshotSource
}

// NewSnapshotAdapter wraps a SnapshotSource so the owner can obtain
// authoritative bootstrap membership without importing the driver package.
func NewSnapshotAdapter(src SnapshotSource) *SnapshotAdapter {
	return &SnapshotAdapter{src: src}
}

// Snapshot obtains one coherent state generation and maps its live workspace
// IDs and focused ID onto Membership. Records without a workspace ID are
// ignored, mirroring the driver's own partial-response handling, so an
// incomplete daemon response never pollutes membership.
func (a *SnapshotAdapter) Snapshot(ctx context.Context) (Membership, error) {
	snap, err := a.src.Snapshot(ctx)
	if err != nil {
		return Membership{}, fmt.Errorf("bootstrap snapshot: %w", err)
	}
	present := make([]string, 0, len(snap.Workspaces))
	for _, ws := range snap.Workspaces {
		if ws.ID == "" {
			continue
		}
		present = append(present, ws.ID)
	}
	return Membership{
		Present:            present,
		FocusedWorkspaceID: snap.FocusedWorkspaceID,
	}, nil
}

var _ Snapshotter = (*SnapshotAdapter)(nil)
