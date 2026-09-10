package herdrwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// HistoryStore is the subset of the history.Store contract the owner needs.
// Kept as a local interface so the owner can be tested with the real store or
// a fake without importing test doubles.
type HistoryStore interface {
	Record(ctx context.Context, sessionKey, wsID string) (changed bool, err error)
	Remove(ctx context.Context, sessionKey, wsID string) error
	List(ctx context.Context, sessionKey string) (mru []string, err error)
}

// Membership is the authoritative live workspace set captured once at bootstrap
// (and on reconnect) through the existing CLI snapshot adapter. It validates
// current state and membership, never chronology.
type Membership struct {
	Present            []string
	FocusedWorkspaceID string
}

// Snapshotter provides one-shot authoritative membership at bootstrap/reconnect.
// Implemented by an adapter over the existing herdr.Driver.Snapshot in unit 3
// wiring; the owner never calls it per focus event.
type Snapshotter interface {
	Snapshot(ctx context.Context) (Membership, error)
}

// wireEvent is the decoded NDJSON event frame from the Herdr 0.8.2
// events.subscribe stream. Events arrive with underscored spelling on the wire
// (e.g. "workspace_focused", "workspace_closed").
type wireEvent struct {
	Event string `json:"event"`
	Data  struct {
		Type        string `json:"type"`
		WorkspaceID string `json:"workspace_id"`
	} `json:"data"`
}

const (
	eventWorkspaceFocused = "workspace_focused"
	eventWorkspaceClosed  = "workspace_closed"
)

// Owner serializes event transitions, history-store commits, and control state
// responses behind a single mutex so there is exactly one authority for
// per-session state. It implements StateProvider for the control Server.
//
// Readiness is derived from observed data in the CURRENT epoch only: a fresh
// owner (or one that just lost stream continuity) is not ready even when the
// persisted history database still holds an offline MRU. The observed-ID set
// filters the persisted MRU down to the current epoch's observations, so no
// offline backfill leaks and no store-side Clear API is required.
type Owner struct {
	mu         sync.Mutex
	store      HistoryStore
	snap       Snapshotter
	sessionKey string

	epoch    int64
	ready    bool
	observed map[string]struct{} // current-epoch observed workspace IDs
}

// NewOwner constructs an Owner bound to a history store, a bootstrap
// snapshotter, and the full-SHA session key.
func NewOwner(store HistoryStore, snap Snapshotter, sessionKey string) *Owner {
	return &Owner{
		store:      store,
		snap:       snap,
		sessionKey: sessionKey,
		observed:   make(map[string]struct{}),
	}
}

// Bootstrap captures authoritative membership once, reconciles it against any
// event frames buffered during stream subscription, and opens a new epoch.
func (o *Owner) Bootstrap(ctx context.Context, bufferedFrames ...[]byte) error {
	mem, err := o.snap.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap snapshot: %w", err)
	}
	if err := o.reconcileBootstrap(mem, bufferedFrames); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.epoch++
	o.observed = make(map[string]struct{})
	o.ready = false
	return nil
}

func (o *Owner) reconcileBootstrap(mem Membership, bufferedFrames [][]byte) error {
	if mem.FocusedWorkspaceID != "" && len(mem.Present) > 0 {
		present := false
		for _, id := range mem.Present {
			if id == mem.FocusedWorkspaceID {
				present = true
				break
			}
		}
		if !present {
			return fmt.Errorf("bootstrap snapshot conflict: focused workspace %q not in present set %v", mem.FocusedWorkspaceID, mem.Present)
		}
	}

	for _, raw := range bufferedFrames {
		var ev wireEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		kind := ev.Event
		if kind == "" {
			kind = ev.Data.Type
		}
		if kind == eventWorkspaceClosed && ev.Data.WorkspaceID != "" {
			if ev.Data.WorkspaceID == mem.FocusedWorkspaceID {
				return fmt.Errorf("bootstrap conflict: snapshot focused workspace %q closed in buffered stream", mem.FocusedWorkspaceID)
			}
		}
	}
	return nil
}

// InvalidateReadiness marks the owner not-ready and opens a new epoch, dropping
// all prior unverified observations. Called on stream disconnect, decode error,
// overflow, or EOF so cached DB MRU can never make a reconnecting owner ready.
func (o *Owner) InvalidateReadiness() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.epoch++
	o.observed = make(map[string]struct{})
	o.ready = false
}

// HandleEventFrame decodes one NDJSON event frame and applies it to the owner
// state under the single owner lock. Malformed frames invalidate readiness
// rather than silently dropping.
func (o *Owner) HandleEventFrame(ctx context.Context, raw []byte) error {
	var ev wireEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		o.InvalidateReadiness()
		return fmt.Errorf("decode event frame: %w", err)
	}

	// Resolve the event kind from either the envelope name or the data.type.
	kind := ev.Event
	if kind == "" {
		kind = ev.Data.Type
	}

	switch kind {
	case eventWorkspaceFocused:
		return o.applyFocus(ctx, ev.Data.WorkspaceID)
	case eventWorkspaceClosed:
		return o.applyClose(ctx, ev.Data.WorkspaceID)
	default:
		// Unrelated subscribed event; ignore without affecting readiness.
		return nil
	}
}

func (o *Owner) applyFocus(ctx context.Context, wsID string) error {
	if wsID == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if _, err := o.store.Record(ctx, o.sessionKey, wsID); err != nil {
		// A store write error invalidates readiness and fails bounded.
		o.ready = false
		return fmt.Errorf("record focus: %w", err)
	}
	o.observed[wsID] = struct{}{}
	// Ready once at least two trustworthy observed focus IDs exist, so a
	// previous distinct workspace is resolvable.
	if len(o.observed) >= 2 {
		o.ready = true
	}
	return nil
}

func (o *Owner) applyClose(ctx context.Context, wsID string) error {
	if wsID == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if err := o.store.Remove(ctx, o.sessionKey, wsID); err != nil {
		o.ready = false
		return fmt.Errorf("remove closed workspace: %w", err)
	}
	delete(o.observed, wsID)
	if len(o.observed) < 2 {
		o.ready = false
	}
	return nil
}

// State implements StateProvider. It serializes through the owner lock, rejects
// a session-key mismatch, and returns a persisted MRU filtered to the current
// epoch's observed IDs with an immutable copy.
func (o *Owner) State(ctx context.Context, sessionKey string) (StateResp, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if sessionKey != "" && sessionKey != o.sessionKey {
		return StateResp{Ready: false, Epoch: o.epoch, MRU: []string{}}, errors.New("session key mismatch")
	}

	persisted, err := o.store.List(ctx, o.sessionKey)
	if err != nil {
		return StateResp{Ready: false, Epoch: o.epoch, MRU: []string{}}, fmt.Errorf("list history: %w", err)
	}

	// Filter persisted MRU to current-epoch observations only; this prevents
	// offline backfill from a stale DB from leaking into a fresh owner.
	filtered := make([]string, 0, len(persisted))
	for _, id := range persisted {
		if _, ok := o.observed[id]; ok {
			filtered = append(filtered, id)
		}
	}

	return StateResp{
		Ready: o.ready,
		Epoch: o.epoch,
		MRU:   filtered,
	}, nil
}

var _ StateProvider = (*Owner)(nil)
