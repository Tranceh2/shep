package ranking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/source"
)

const (
	schemaVersion    = 3
	ackRetention     = 7 * 24 * time.Hour
	migrationTimeout = 5 * time.Second
	busyTimeout      = 100 * time.Millisecond
	operationTimeout = 5 * time.Second
	writeTimeout     = 1 * time.Second
	recoveryTimeout  = 1 * time.Second
)

var (
	ErrFutureVersion = errors.New("ranking database schema is newer than supported")
	recoveryMu       = make(chan struct{}, 1)
	migrationMu      sync.Mutex
)

func init() {
	recoveryMu <- struct{}{}
}

// Keys is the minimal success event persisted by ranking. It deliberately
// excludes candidate presentation, query, template, and environment data.
type Keys struct {
	Exact        string
	Resource     string
	CurrentExact string
}

// Store owns the durable ranking state. All reads are copied into Snapshot
// values before the database transaction is closed.
type Store struct {
	db   *sql.DB
	now  func() time.Time
	path string
	mu   sync.Mutex
}

func Open() (*Store, error) {
	path, err := pathutil.StatePath("shep", "ranking.sqlite3")
	if err != nil {
		return nil, err
	}
	return OpenPath(path)
}

func OpenPath(path string) (*Store, error) {
	return OpenPathWithClock(path, time.Now)
}

func OpenPathWithClock(path string, now func() time.Time) (*Store, error) {
	return openPath(path, now, true)
}

func openPath(path string, now func() time.Time, recoverCorruption bool) (*Store, error) {
	return openPathWithContext(context.Background(), path, now, recoverCorruption)
}

func openPathWithContext(ctx context.Context, path string, now func() time.Time, recoverCorruption bool) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create ranking state directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("protect ranking state directory: %w", err)
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path, migrationTimeout/time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("open ranking database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: now, path: path}
	if err := migrate(db); err != nil {
		_ = db.Close()
		if errors.Is(err, ErrFutureVersion) || !isCorruption(err) {
			return nil, err
		}
		if !recoverCorruption {
			return nil, err
		}
		recoveryCtx, cancel := context.WithTimeout(ctx, recoveryTimeout)
		defer cancel()
		if err := acquireRecovery(recoveryCtx); err != nil {
			return nil, err
		}
		defer releaseRecovery()
		if err := quarantine(recoveryCtx, path); err != nil {
			return nil, err
		}
		return openPathWithContext(ctx, path, now, false)

	}

	if _, err := db.Exec("PRAGMA busy_timeout = 100"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure ranking busy timeout: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("protect ranking database: %w", err)
	}
	return store, nil
}

func isCorruption(err error) bool {
	var sqliteErr interface{ Code() int }
	if !errors.As(err, &sqliteErr) {
		return strings.Contains(strings.ToLower(err.Error()), "not a database")
	}
	return sqliteErr.Code() == 11 || sqliteErr.Code() == 26
}

func migrate(db *sql.DB) error {
	migrationMu.Lock()
	defer migrationMu.Unlock()

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin ranking migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read ranking schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("%w: %d", ErrFutureVersion, version)
	}
	if version == 0 {
		if _, err := tx.Exec(`
  CREATE TABLE exact_usage (
  exact_id TEXT PRIMARY KEY NOT NULL,
 count INTEGER NOT NULL CHECK (count > 0),
 last_used INTEGER NOT NULL
 );
 CREATE INDEX IF NOT EXISTS exact_usage_last_used ON exact_usage(last_used);
  CREATE TABLE resource_usage (
  resource_id TEXT PRIMARY KEY NOT NULL,
 count INTEGER NOT NULL CHECK (count > 0),
 last_used INTEGER NOT NULL
 );
 CREATE INDEX IF NOT EXISTS resource_usage_last_used ON resource_usage(last_used);
  CREATE TABLE recent_exact (
  position INTEGER PRIMARY KEY NOT NULL,
 exact_id TEXT NOT NULL,
 selected_at INTEGER NOT NULL
 );`); err != nil {
			return fmt.Errorf("migrate ranking schema: %w", err)
		}
		version = 1
	}
	if version == 1 {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS candidate_pins (
 pin_key TEXT PRIMARY KEY NOT NULL
 );`); err != nil {
			return fmt.Errorf("migrate ranking pins: %w", err)
		}
		version = 2
	}
	if version == 2 {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS pane_acknowledgements (
 pane_id TEXT PRIMARY KEY NOT NULL,
 status TEXT NOT NULL,
 acked_at INTEGER NOT NULL
 );`); err != nil {
			return fmt.Errorf("migrate ranking pane acknowledgements: %w", err)
		}
		version = 3
	}
	if _, err := tx.Exec("PRAGMA user_version = " + fmt.Sprint(version)); err != nil {
		return fmt.Errorf("set ranking schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ranking migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Snapshot(ctx context.Context, currentExact string) Snapshot {
	now := s.clock()
	snapshot := Snapshot{enabled: true, exact: map[string]usage{}, resource: map[string]usage{}, pins: map[string]struct{}{}, acknowledgements: map[string]string{}, currentExact: currentExact, capturedAt: now}

	if s == nil || s.db == nil {
		return disabledSnapshot(currentExact)
	}
	readCtx, cancel := boundedContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(readCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return disabledSnapshot(currentExact)
	}
	defer func() { _ = tx.Rollback() }()
	cutoff := now.Unix() - int64(retention/time.Second)
	rows, err := tx.QueryContext(readCtx, `SELECT exact_id, count, last_used FROM exact_usage WHERE last_used >= ?`, cutoff)
	if err != nil {
		return disabledSnapshot(currentExact)
	}
	for rows.Next() {
		var key string
		var value usage
		if err := rows.Scan(&key, &value.count, &value.lastUsed); err != nil {
			_ = rows.Close()
			return disabledSnapshot(currentExact)
		}
		snapshot.exact[exactStorageKey(key)] = value

	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return disabledSnapshot(currentExact)
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(readCtx, `SELECT pin_key FROM candidate_pins`)
	if err != nil {
		return disabledSnapshot(currentExact)
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return disabledSnapshot(currentExact)
		}
		snapshot.pins[pinStorageKey(key)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return disabledSnapshot(currentExact)
	}
	_ = rows.Close()
	ackCutoff := now.Unix() - int64(ackRetention/time.Second)
	rows, err = tx.QueryContext(readCtx, `SELECT pane_id, status FROM pane_acknowledgements WHERE acked_at >= ?`, ackCutoff)
	if err != nil {
		return disabledSnapshot(currentExact)
	}
	for rows.Next() {
		var paneID, status string
		if err := rows.Scan(&paneID, &status); err != nil {
			_ = rows.Close()
			return disabledSnapshot(currentExact)
		}
		snapshot.acknowledgements[paneID] = strings.ToLower(status)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return disabledSnapshot(currentExact)
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(readCtx, `SELECT resource_id, count, last_used FROM resource_usage WHERE last_used >= ?`, cutoff)
	if err != nil {
		return disabledSnapshot(currentExact)
	}
	for rows.Next() {
		var key string
		var value usage
		if err := rows.Scan(&key, &value.count, &value.lastUsed); err != nil {
			_ = rows.Close()
			return disabledSnapshot(currentExact)
		}
		snapshot.resource[resourceStorageKey(key)] = value

	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return disabledSnapshot(currentExact)
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(readCtx, `SELECT exact_id FROM recent_exact WHERE selected_at >= ? ORDER BY position ASC`, cutoff)
	if err != nil {
		return disabledSnapshot(currentExact)
	}
	for rows.Next() {
		var exact string
		if err := rows.Scan(&exact); err != nil {
			_ = rows.Close()
			return disabledSnapshot(currentExact)
		}
		snapshot.recent = append(snapshot.recent, exactStorageKey(exact))
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return disabledSnapshot(currentExact)
	}
	_ = rows.Close()
	return snapshot
}

func (s *Store) Record(ctx context.Context, candidate source.Candidate) error {
	exact, resource := CandidateKeyParts(candidate)
	return s.RecordSuccess(ctx, Keys{Exact: exact, Resource: resource})
}

func (s *Store) RecordSuccess(ctx context.Context, keys Keys) error {
	if s == nil || s.db == nil || strings.TrimSpace(keys.Exact) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys.Exact = exactStorageKey(keys.Exact)
	keys.Resource = resourceStorageKey(keys.Resource)
	keys.CurrentExact = exactStorageKey(keys.CurrentExact)
	writeCtx, cancel := boundedWriteContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(writeCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock().Unix()
	if keys.Exact != keys.CurrentExact {
		if _, err := tx.ExecContext(writeCtx, `INSERT INTO exact_usage(exact_id, count, last_used) VALUES (?, 1, ?) ON CONFLICT(exact_id) DO UPDATE SET count=count+1, last_used=excluded.last_used`, keys.Exact, now); err != nil {
			return err
		}
	}
	if keys.Resource != "" && keys.Exact != keys.CurrentExact {
		if _, err := tx.ExecContext(writeCtx, `INSERT INTO resource_usage(resource_id, count, last_used) VALUES (?, 1, ?) ON CONFLICT(resource_id) DO UPDATE SET count=count+1, last_used=excluded.last_used`, keys.Resource, now); err != nil {
			return err
		}
	}
	if keys.Exact != keys.CurrentExact {
		if _, err := tx.ExecContext(writeCtx, `UPDATE recent_exact SET position=-position-1`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(writeCtx, `UPDATE recent_exact SET position=-position`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(writeCtx, `DELETE FROM recent_exact WHERE exact_id=? OR position >= ?`, keys.Exact, maxRecent); err != nil {
			return err
		}
		if _, err := tx.ExecContext(writeCtx, `INSERT INTO recent_exact(position, exact_id, selected_at) VALUES (0, ?, ?)`, keys.Exact, now); err != nil {
			return err
		}
	}
	cutoff := now - int64(retention/time.Second)
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM exact_usage WHERE last_used < ?`, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM resource_usage WHERE last_used < ?`, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM recent_exact WHERE selected_at < ?`, cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM exact_usage WHERE exact_id IN (SELECT exact_id FROM exact_usage ORDER BY last_used DESC, count DESC, exact_id ASC LIMIT -1 OFFSET ?)`, maxKeys); err != nil {
		return err
	}
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM resource_usage WHERE resource_id IN (SELECT resource_id FROM resource_usage ORDER BY last_used DESC, count DESC, resource_id ASC LIMIT -1 OFFSET ?)`, maxKeys); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Clear(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeCtx, cancel := boundedWriteContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(writeCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"exact_usage", "resource_usage", "recent_exact", "candidate_pins", "pane_acknowledgements"} {
		if _, err := tx.ExecContext(writeCtx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecordAcknowledgement persists an acknowledgement for paneID at the observed status.
// Only attention statuses ("blocked", "done") are persisted; non-attention or empty inputs are no-ops.
func (s *Store) RecordAcknowledgement(ctx context.Context, paneID, status string) error {
	if s == nil || s.db == nil {
		return nil
	}
	paneID = strings.TrimSpace(paneID)
	status = strings.ToLower(strings.TrimSpace(status))
	if paneID == "" || (status != "blocked" && status != "done") {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeCtx, cancel := boundedWriteContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(writeCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock().Unix()
	if _, err := tx.ExecContext(writeCtx, `INSERT INTO pane_acknowledgements(pane_id, status, acked_at) VALUES (?, ?, ?) ON CONFLICT(pane_id) DO UPDATE SET status=excluded.status, acked_at=excluded.acked_at`, paneID, status, now); err != nil {
		return err
	}
	ackCutoff := now - int64(ackRetention/time.Second)
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM pane_acknowledgements WHERE acked_at < ?`, ackCutoff); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearAcknowledgement removes any persisted acknowledgement for paneID.
func (s *Store) ClearAcknowledgement(ctx context.Context, paneID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeCtx, cancel := boundedWriteContext(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(writeCtx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock().Unix()
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM pane_acknowledgements WHERE pane_id=?`, paneID); err != nil {
		return err
	}
	ackCutoff := now - int64(ackRetention/time.Second)
	if _, err := tx.ExecContext(writeCtx, `DELETE FROM pane_acknowledgements WHERE acked_at < ?`, ackCutoff); err != nil {
		return err
	}
	return tx.Commit()
}

func acquireRecovery(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-recoveryMu:
		return nil
	}
}

func releaseRecovery() {
	recoveryMu <- struct{}{}
}

func quarantine(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	if err := os.Rename(path, path+".corrupt-"+stamp); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Rename(path+suffix, path+suffix+".corrupt-"+stamp)
	}
	return nil
}

func (s *Store) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func boundedContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, operationTimeout)
}

func boundedWriteContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, writeTimeout)
}
