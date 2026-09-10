package ranking

import (
	"context"
	"database/sql"
	"strings"

	"github.com/tranceh2/shep/internal/source"
)

// PinKey returns the opaque stable storage key for a candidate. Filesystem
// candidates use the cross-provider resource key; pathless candidates use
// their stable action identity.
func PinKey(candidate source.Candidate) string {
	if resource := Resource(candidate); resource != "" {
		return resource
	}
	return exactStorageKey(Identity(candidate))
}

func pinStorageKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if isOpaqueStorageKey("resource", key) || isOpaqueStorageKey("exact", key) {
		return key
	}
	return exactStorageKey(key)
}

// IsPinned reports whether key is currently present in the durable pin set.
func (s *Store) IsPinned(ctx context.Context, key string) bool {
	if s == nil || s.db == nil {
		return false
	}
	key = pinStorageKey(key)
	if key == "" {
		return false
	}
	readCtx, cancel := boundedContext(ctx)
	defer cancel()
	var present int
	if err := s.db.QueryRowContext(readCtx, `SELECT 1 FROM candidate_pins WHERE pin_key=?`, key).Scan(&present); err != nil {
		return false
	}
	return present == 1
}

// SetPinned persists the requested state and returns the resulting state.
// Pin writes share Store's single-writer mutex with adaptive ranking writes.
func (s *Store) SetPinned(ctx context.Context, key string, pinned bool) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	key = pinStorageKey(key)
	if key == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeCtx, cancel := boundedWriteContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(writeCtx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if pinned {
		_, err = tx.ExecContext(writeCtx, `INSERT INTO candidate_pins(pin_key) VALUES (?) ON CONFLICT(pin_key) DO NOTHING`, key)
	} else {
		_, err = tx.ExecContext(writeCtx, `DELETE FROM candidate_pins WHERE pin_key=?`, key)
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return pinned, nil
}

// TogglePin flips key and returns the resulting state in one serialized
// transaction, so concurrent callers cannot lose an update.
func (s *Store) TogglePin(ctx context.Context, key string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	key = pinStorageKey(key)
	if key == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeCtx, cancel := boundedWriteContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(writeCtx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var present int
	err = tx.QueryRowContext(writeCtx, `SELECT 1 FROM candidate_pins WHERE pin_key=?`, key).Scan(&present)
	switch {
	case err == nil:
		if _, err = tx.ExecContext(writeCtx, `DELETE FROM candidate_pins WHERE pin_key=?`, key); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	case err != sql.ErrNoRows:
		return false, err
	default:
		if _, err = tx.ExecContext(writeCtx, `INSERT INTO candidate_pins(pin_key) VALUES (?)`, key); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
}
