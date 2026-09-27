package ranking

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAcknowledgementsMigrateFromV0V1V2AndPersistAfterReopen(t *testing.T) {
	t.Parallel()

	// 1. Migrate from v0 (raw sqlite database with nothing)
	t.Run("from_v0", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "v0.sqlite3")
		store, err := OpenPath(path)
		if err != nil {
			t.Fatalf("OpenPath failed: %v", err)
		}
		defer store.Close()

		var version int
		if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != schemaVersion {
			t.Fatalf("schema version = %d, want %d", version, schemaVersion)
		}
	})

	// 2. Migrate from v1
	t.Run("from_v1", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "v1.sqlite3")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
CREATE TABLE exact_usage (exact_id TEXT PRIMARY KEY NOT NULL, count INTEGER NOT NULL CHECK (count > 0), last_used INTEGER NOT NULL);
CREATE TABLE resource_usage (resource_id TEXT PRIMARY KEY NOT NULL, count INTEGER NOT NULL CHECK (count > 0), last_used INTEGER NOT NULL);
CREATE TABLE recent_exact (position INTEGER PRIMARY KEY NOT NULL, exact_id TEXT NOT NULL, selected_at INTEGER NOT NULL);
PRAGMA user_version = 1;
`); err != nil {
			t.Fatal(err)
		}
		db.Close()

		store, err := OpenPath(path)
		if err != nil {
			t.Fatalf("OpenPath on v1 database failed: %v", err)
		}
		defer store.Close()

		var version int
		if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != schemaVersion {
			t.Fatalf("schema version = %d, want %d", version, schemaVersion)
		}
	})

	// 3. Migrate from v2
	t.Run("from_v2", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "v2.sqlite3")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
CREATE TABLE exact_usage (exact_id TEXT PRIMARY KEY NOT NULL, count INTEGER NOT NULL CHECK (count > 0), last_used INTEGER NOT NULL);
CREATE TABLE resource_usage (resource_id TEXT PRIMARY KEY NOT NULL, count INTEGER NOT NULL CHECK (count > 0), last_used INTEGER NOT NULL);
CREATE TABLE recent_exact (position INTEGER PRIMARY KEY NOT NULL, exact_id TEXT NOT NULL, selected_at INTEGER NOT NULL);
CREATE TABLE candidate_pins (pin_key TEXT PRIMARY KEY NOT NULL);
PRAGMA user_version = 2;
`); err != nil {
			t.Fatal(err)
		}
		db.Close()

		store, err := OpenPath(path)
		if err != nil {
			t.Fatalf("OpenPath on v2 database failed: %v", err)
		}
		defer store.Close()

		var version int
		if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != schemaVersion {
			t.Fatalf("schema version = %d, want %d", version, schemaVersion)
		}

		// Reopening v3 must be idempotent
		store.Close()
		reopened, err := OpenPath(path)
		if err != nil {
			t.Fatalf("reopening v3 database failed: %v", err)
		}
		defer reopened.Close()
	})
}

func TestAcknowledgementRoundTripAndFiltering(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Record valid attention statuses
	if err := store.RecordAcknowledgement(ctx, "pane-1", "blocked"); err != nil {
		t.Fatalf("RecordAcknowledgement(pane-1, blocked) failed: %v", err)
	}
	if err := store.RecordAcknowledgement(ctx, "pane-2", "DONE"); err != nil {
		t.Fatalf("RecordAcknowledgement(pane-2, DONE) failed: %v", err)
	}

	// Non-attention statuses and invalid inputs must be safe no-ops
	for _, invalid := range []struct {
		paneID string
		status string
	}{
		{"pane-3", "idle"},
		{"pane-4", "working"},
		{"pane-5", "unknown"},
		{"pane-6", "random_status"},
		{"", "blocked"},
		{"pane-7", ""},
	} {
		if err := store.RecordAcknowledgement(ctx, invalid.paneID, invalid.status); err != nil {
			t.Fatalf("RecordAcknowledgement(%q, %q) returned unexpected error: %v", invalid.paneID, invalid.status, err)
		}
	}

	snap := store.Snapshot(ctx, "")
	if !snap.IsPaneAcknowledged("pane-1", "blocked") {
		t.Errorf("pane-1 want acknowledged for blocked, got false")
	}
	if !snap.IsPaneAcknowledged("pane-1", "BLOCKED") {
		t.Errorf("pane-1 want case-insensitive match for BLOCKED, got false")
	}
	if snap.IsPaneAcknowledged("pane-1", "done") {
		t.Errorf("pane-1 acknowledged for done, want false")
	}

	if !snap.IsPaneAcknowledged("pane-2", "done") {
		t.Errorf("pane-2 want acknowledged for done, got false")
	}

	for _, invalidPane := range []string{"pane-3", "pane-4", "pane-5", "pane-6", "pane-7"} {
		if snap.IsPaneAcknowledged(invalidPane, "blocked") || snap.IsPaneAcknowledged(invalidPane, "done") {
			t.Errorf("invalid pane %q was acknowledged, want false", invalidPane)
		}
	}

	// Test ClearAcknowledgement
	if err := store.ClearAcknowledgement(ctx, "pane-1"); err != nil {
		t.Fatalf("ClearAcknowledgement(pane-1) failed: %v", err)
	}
	snap2 := store.Snapshot(ctx, "")
	if snap2.IsPaneAcknowledged("pane-1", "blocked") {
		t.Errorf("pane-1 still acknowledged after ClearAcknowledgement")
	}
	if !snap2.IsPaneAcknowledged("pane-2", "done") {
		t.Errorf("pane-2 should still be acknowledged after pane-1 cleared")
	}

	// Test Store.Clear
	if err := store.Clear(ctx); err != nil {
		t.Fatalf("Clear() failed: %v", err)
	}
	snap3 := store.Snapshot(ctx, "")
	if snap3.IsPaneAcknowledged("pane-2", "done") {
		t.Errorf("pane-2 still acknowledged after Store.Clear()")
	}
}

func TestAcknowledgementRetentionWithInjectedClock(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := func() time.Time { return now }

	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPathWithClock(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.RecordAcknowledgement(ctx, "pane-old", "blocked"); err != nil {
		t.Fatal(err)
	}

	// Advance time past 7-day retention
	now = now.Add(8 * 24 * time.Hour)

	// Snapshot should not return expired row
	snap := store.Snapshot(ctx, "")
	if snap.IsPaneAcknowledged("pane-old", "blocked") {
		t.Fatalf("expired acknowledgement returned in snapshot")
	}

	// Record fresh acknowledgement to trigger write pruning
	if err := store.RecordAcknowledgement(ctx, "pane-fresh", "done"); err != nil {
		t.Fatal(err)
	}

	snapFresh := store.Snapshot(ctx, "")
	if snapFresh.IsPaneAcknowledged("pane-old", "blocked") {
		t.Fatalf("expired acknowledgement still present after pruning")
	}
	if !snapFresh.IsPaneAcknowledged("pane-fresh", "done") {
		t.Fatalf("fresh acknowledgement missing")
	}
}

func TestAcknowledgementNilStoreIsSafe(t *testing.T) {
	t.Parallel()
	var nilStore *Store
	ctx := context.Background()

	if err := nilStore.RecordAcknowledgement(ctx, "p1", "blocked"); err != nil {
		t.Errorf("nilStore.RecordAcknowledgement returned error: %v", err)
	}
	if err := nilStore.ClearAcknowledgement(ctx, "p1"); err != nil {
		t.Errorf("nilStore.ClearAcknowledgement returned error: %v", err)
	}
	snap := nilStore.Snapshot(ctx, "")
	if snap.IsPaneAcknowledged("p1", "blocked") {
		t.Errorf("nil snapshot returned true for IsPaneAcknowledged")
	}
}

func TestOnePaneAckDoesNotAffectAnotherInSameWorkspace(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	_ = store.RecordAcknowledgement(ctx, "pane-ws1-a", "blocked")
	snap := store.Snapshot(ctx, "")

	if !snap.IsPaneAcknowledged("pane-ws1-a", "blocked") {
		t.Errorf("pane-ws1-a should be acknowledged")
	}
	if snap.IsPaneAcknowledged("pane-ws1-b", "blocked") {
		t.Errorf("pane-ws1-b in same workspace should NOT be acknowledged")
	}
}
