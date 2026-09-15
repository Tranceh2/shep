package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tranceh2/shep/internal/pathutil"
	_ "modernc.org/sqlite"
)

// Standard history store errors.
var (
	ErrUnsupportedVersion = errors.New("unsupported schema_version in history database")
	ErrCorrupt            = errors.New("corrupt history database")
)

const SchemaVersion = 1

var (
	globalSeq atomic.Int64
	ChmodPath = os.Chmod
)

func init() {
	globalSeq.Store(time.Now().UnixNano())
}

func nextUniqueSeq() int64 {
	return globalSeq.Add(1)
}

// Store manages the per-session workspace MRU history in a dedicated SQLite database.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// DefaultDBPath returns the canonical path to jump_history.sqlite3 in shep's state directory.
func DefaultDBPath() (string, error) {
	return pathutil.StatePath("shep", "jump_history.sqlite3")
}

// OpenDefault opens the history store at the default state location.
func OpenDefault() (*Store, error) {
	path, err := DefaultDBPath()
	if err != nil {
		return nil, err
	}
	return OpenPath(path)
}

// ReadWorkspaceMRU reads the workspace MRU for socketPath from the default history store.
// If socketPath is empty or the database file cannot be read, it returns an empty slice and nil error.
func ReadWorkspaceMRU(ctx context.Context, socketPath string) ([]string, error) {
	if socketPath == "" {
		return []string{}, nil
	}
	dbPath, err := DefaultDBPath()
	if err != nil {
		return []string{}, nil
	}
	return ReadWorkspaceMRUFromPath(ctx, socketPath, dbPath)
}

// ReadWorkspaceMRUFromPath reads the workspace MRU for socketPath from the SQLite store at dbPath.
// If socketPath is empty or the database file does not exist, it returns an empty slice and nil error.
func ReadWorkspaceMRUFromPath(ctx context.Context, socketPath, dbPath string) ([]string, error) {
	if socketPath == "" || dbPath == "" {
		return []string{}, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return []string{}, nil
	}
	sessionKey, err := CanonicalSessionKey(socketPath)
	if err != nil {
		return nil, fmt.Errorf("derive session key: %w", err)
	}
	store, err := OpenPath(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open history store: %w", err)
	}
	defer func() { _ = store.Close() }()

	return store.List(ctx, sessionKey)
}

// CanonicalSessionKey computes SHA-256(canonical socket path) as a hex string.
func CanonicalSessionKey(socketPath string) (string, error) {
	if socketPath == "" || strings.Contains(socketPath, "\x00") {
		return "", errors.New("invalid socket path")
	}

	absPath, err := filepath.Abs(socketPath)
	if err != nil {
		return "", fmt.Errorf("failed to make path absolute: %w", err)
	}

	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		realPath = absPath
	}
	canonical := filepath.Clean(realPath)

	hash := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf("%x", hash[:]), nil
}

// OpenPath opens or creates the SQLite store at path with strict permissions (0700 dir, 0600 file).
func OpenPath(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create history directory: %w", err)
	}
	if err := ChmodPath(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to protect history directory: %w", err)
	}

	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping history database: %w", err)
	}

	if err := ChmodPath(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to protect history database: %w", err)
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) initSchema() error {
	var pragmaVal string
	if err := s.db.QueryRow("PRAGMA quick_check;").Scan(&pragmaVal); err != nil || pragmaVal != "ok" {
		return ErrCorrupt
	}

	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY);`)
	if err != nil {
		return fmt.Errorf("%w: failed to create schema_version table: %v", ErrCorrupt, err)
	}

	var version int
	err = s.db.QueryRow("SELECT version FROM schema_version LIMIT 1;").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec("INSERT INTO schema_version (version) VALUES (?);", SchemaVersion); err != nil {
			return fmt.Errorf("failed to record schema version: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	} else if version > SchemaVersion {
		return ErrUnsupportedVersion
	}

	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS mru_history (
			session_key TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			PRIMARY KEY (session_key, workspace_id)
		);
		CREATE INDEX IF NOT EXISTS idx_mru_seq ON mru_history(session_key, seq DESC);
	`)
	if err != nil {
		return fmt.Errorf("%w: failed to create mru_history table: %v", ErrCorrupt, err)
	}

	return nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// Record records a workspace focus event for sessionKey and wsID.
// If wsID is already at the head of sessionKey's MRU, returns changed=false without writing SQL data.
// Otherwise records/moves wsID to head, prunes history > 50, and returns changed=true.
func (s *Store) Record(ctx context.Context, sessionKey, wsID string) (bool, error) {
	if sessionKey == "" || wsID == "" {
		return false, errors.New("sessionKey and wsID cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var headWSID string
	err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM mru_history WHERE session_key = ? ORDER BY seq DESC LIMIT 1", sessionKey).Scan(&headWSID)
	if err == nil && headWSID == wsID {
		return false, nil
	}

	seq := nextUniqueSeq()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO mru_history (session_key, workspace_id, seq)
		VALUES (?, ?, ?)
		ON CONFLICT(session_key, workspace_id) DO UPDATE SET seq = excluded.seq
	`, sessionKey, wsID, seq)
	if err != nil {
		return false, fmt.Errorf("failed to upsert mru entry: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		DELETE FROM mru_history
		WHERE session_key = ? AND workspace_id NOT IN (
			SELECT workspace_id FROM mru_history
			WHERE session_key = ?
			ORDER BY seq DESC
			LIMIT 50
		)
	`, sessionKey, sessionKey)
	if err != nil {
		return false, fmt.Errorf("failed to prune mru history: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return true, nil
}

// Remove deletes wsID from sessionKey's MRU. Missing wsID is a no-op returning nil.
func (s *Store) Remove(ctx context.Context, sessionKey, wsID string) error {
	if sessionKey == "" || wsID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM mru_history WHERE session_key = ? AND workspace_id = ?", sessionKey, wsID)
	if err != nil {
		return fmt.Errorf("failed to remove mru entry: %w", err)
	}
	return nil
}

// List returns the workspace IDs in MRU order (newest first), capped at 50.
func (s *Store) List(ctx context.Context, sessionKey string) ([]string, error) {
	if sessionKey == "" {
		return []string{}, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.QueryContext(ctx, "SELECT workspace_id FROM mru_history WHERE session_key = ? ORDER BY seq DESC LIMIT 50", sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to query mru history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	mru := make([]string, 0, 50)
	for rows.Next() {
		var wsID string
		if err := rows.Scan(&wsID); err != nil {
			return nil, fmt.Errorf("failed to scan mru row: %w", err)
		}
		mru = append(mru, wsID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating mru rows: %w", err)
	}

	return mru, nil
}
