package ranking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tranceh2/shep/internal/source"
)

func TestOpenPathMigratesVersionedPrivateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{"exact_usage", "resource_usage", "recent_exact", "candidate_pins", "pane_acknowledgements"} {
		var count int
		if err := store.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s was not migrated", table)
		}
	}
	if mode := fileMode(filepath.Dir(path)); mode != 0o700 {
		t.Fatalf("state directory mode = %o, want 700", mode)
	}
	if mode := fileMode(path); mode != 0o600 {
		t.Fatalf("database mode = %o, want 600", mode)
	}
}

func TestMigrateIsAtomicOnFailure(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE exact_usage (wrong TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("migrate should reject an incompatible existing object")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed migration changed schema version to %d", version)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('resource_usage', 'recent_exact')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("failed migration left partial tables: %d", tables)
	}
}

func TestOpenPathRejectsFutureSchemaWithoutRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = OpenPath(path)
	if !errors.Is(err, ErrFutureVersion) {
		t.Fatalf("future schema error = %v, want ErrFutureVersion", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("future-version database was moved: %v", statErr)
	}
}

func TestOpenPathDoesNotQuarantineMigrationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE exact_usage (wrong TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenPath(path); err == nil {
		t.Fatal("incompatible migration unexpectedly succeeded")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("migration failure removed healthy state: %v", err)
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("migration failure quarantined state: %v", matches)
	}
}

func TestOpenPathRecoveryHonorsCancellationBeforeQuarantine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	<-recoveryMu
	defer func() { recoveryMu <- struct{}{} }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openPathWithContext(ctx, path, time.Now, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("openPathWithContext error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("canceled recovery removed state: %v", err)
	}
}

func TestQuarantineHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := quarantine(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("quarantine error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("canceled quarantine removed state: %v", err)
	}
}

func TestRecordSuccessUsesCoherentSnapshotAndClearRetainsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.RecordSuccess(ctx, Keys{Exact: "action-a", Resource: "/repo/a", CurrentExact: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccess(ctx, Keys{Exact: "action-b", Resource: "/repo/b", CurrentExact: "action-a"}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(ctx, "action-a")
	if !snapshot.Active() || snapshot.exact[pathKey("exact", "action-a")].count != 1 || snapshot.exact[pathKey("exact", "action-b")].count != 1 {
		t.Fatalf("snapshot did not contain one committed generation: %+v", snapshot.exact)
	}
	if len(snapshot.recent) != 2 || snapshot.recent[0] != pathKey("exact", "action-b") || snapshot.recent[1] != pathKey("exact", "action-a") {
		t.Fatalf("recent snapshot = %v, want [action-b action-a]", snapshot.recent)
	}
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	cleared := store.Snapshot(ctx, "")
	if !cleared.Active() || len(cleared.exact) != 0 || len(cleared.resource) != 0 || len(cleared.recent) != 0 {
		t.Fatalf("clear left ranking data: %+v %+v %v", cleared.exact, cleared.resource, cleared.recent)
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("clear changed schema version to %d", version)
	}
}

// TestRecordSuccessOnNilStoreIsSafe pins the nil-receiver contract that the
// guard clauses in RecordSuccess exist to provide: a nil *Store (or one with
// a nil db, e.g. after Close) must return nil without panicking, both when
// called directly and via the exported Record wrapper. This is the
// regression test for the SA5011 "possible nil pointer dereference" finding
// at store.go:261-263, which staticcheck raised because a nil check was
// duplicated after s.mu.Lock() — implying (falsely, per this test) that s
// could still be nil at that point.
func TestRecordSuccessOnNilStoreIsSafe(t *testing.T) {
	var nilStore *Store
	if err := nilStore.RecordSuccess(context.Background(), Keys{Exact: "x"}); err != nil {
		t.Fatalf("RecordSuccess on nil *Store returned %v, want nil", err)
	}
	if err := nilStore.Record(context.Background(), source.Candidate{Path: "/x"}); err != nil {
		t.Fatalf("Record on nil *Store returned %v, want nil", err)
	}
}

func TestRecordSuccessDoesNotPromoteRepeatedCurrentTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.RecordSuccess(ctx, Keys{Exact: "previous", Resource: "/previous", CurrentExact: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccess(ctx, Keys{Exact: "current", Resource: "/current", CurrentExact: "previous"}); err != nil {
		t.Fatal(err)
	}
	before := store.Snapshot(ctx, "current")
	if err := store.RecordSuccess(ctx, Keys{Exact: "current", Resource: "/current", CurrentExact: "current"}); err != nil {
		t.Fatal(err)
	}
	after := store.Snapshot(ctx, "current")
	if strings.Join(before.recent, ",") != strings.Join(after.recent, ",") {
		t.Fatalf("repeated current changed recent order from %v to %v", before.recent, after.recent)
	}
	if after.exact[exactStorageKey("current")].count != before.exact[exactStorageKey("current")].count {
		t.Fatalf("repeated current changed exact usage from %v to %v", before.exact, after.exact)
	}
}

func TestRecordSuccessHonorsRetentionAndKeyCap(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPathWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := now.Add(-retention - time.Second).Unix()
	if _, err := store.db.Exec("INSERT INTO exact_usage(exact_id, count, last_used) VALUES (?, ?, ?)", "old", 2, old); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccess(context.Background(), Keys{Exact: "fresh", Resource: "/fresh"}); err != nil {
		t.Fatal(err)
	}
	var oldCount int
	if err := store.db.QueryRow("SELECT count(*) FROM exact_usage WHERE exact_id='old'").Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatal("expired usage was retained")
	}
	if _, err := store.db.Exec("DELETE FROM exact_usage"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("BEGIN"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxKeys+2; i++ {
		if _, err := store.db.Exec("INSERT INTO exact_usage(exact_id, count, last_used) VALUES (?, 1, ?)", uniqueKey(i), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSuccess(context.Background(), Keys{Exact: "cap-marker"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow("SELECT count(*) FROM exact_usage").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != maxKeys {
		t.Fatalf("exact usage count = %d, want %d", count, maxKeys)
	}
}

func TestConcurrentSnapshotsRemainCoherent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	done := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			if err := store.RecordSuccess(context.Background(), Keys{Exact: fmt.Sprintf("generation-%d", i), Resource: fmt.Sprintf("/generation-%d", i)}); err == nil && i == 0 {
				close(ready)
			}
		}
	}()
	<-ready
	for {
		snapshot := store.Snapshot(context.Background(), "")
		if len(snapshot.exact) == 0 {
			t.Fatal("concurrent snapshot contained no exact generations")
		}
		visited := 0
		for i := 0; i < 20; i++ {
			exact := exactStorageKey(fmt.Sprintf("generation-%d", i))
			if _, ok := snapshot.exact[exact]; !ok {
				continue
			}
			visited++
			resource := resourceStorageKey(fmt.Sprintf("/generation-%d", i))
			if snapshot.resource[resource].count == 0 {
				t.Fatalf("snapshot split exact/resource generation: %q", exact)
			}
		}
		if visited == 0 {
			t.Fatalf("snapshot exact keys were not recognized as generated entries: %v", snapshot.exact)
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func TestRecordSuccessRetainsOnlyRecentAgeAndSupportsConcurrentWriters(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	storeA, err := OpenPathWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := OpenPathWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	if _, err := storeA.db.Exec("INSERT INTO recent_exact(position, exact_id, selected_at) VALUES (0, 'expired', ?)", now.Add(-retention-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := storeA.RecordSuccess(context.Background(), Keys{Exact: "fresh"}); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := storeA.db.QueryRow("SELECT count(*) FROM recent_exact WHERE exact_id='expired'").Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatal("expired recent selection was retained")
	}

	const writers = 4
	const recordsPerWriter = 8
	errs := make(chan error, writers*recordsPerWriter)
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		store := storeA
		if writer%2 == 1 {
			store = storeB
		}
		wg.Add(1)
		go func(writer int, store *Store) {
			defer wg.Done()
			for record := 0; record < recordsPerWriter; record++ {
				if err := store.RecordSuccess(context.Background(), Keys{Exact: fmt.Sprintf("writer-%d-%d", writer, record)}); err != nil {
					errs <- err
				}
			}
		}(writer, store)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent record failed: %v", err)
	}
	var count int
	if err := storeA.db.QueryRow("SELECT count(*) FROM exact_usage").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < writers*recordsPerWriter {
		t.Fatalf("concurrent records stored %d, want at least %d", count, writers*recordsPerWriter)
	}
}

func TestRecordSuccessHonorsBoundedLockWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer locker.Exec("ROLLBACK")

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = store.RecordSuccess(ctx, Keys{Exact: "blocked"})
	if err == nil {
		t.Fatal("locked write unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("locked write waited %v, want bounded wait", elapsed)
	}
}

func TestSnapshotFiltersExpiredRecentEntries(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPathWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec("INSERT INTO recent_exact(position, exact_id, selected_at) VALUES (0, 'expired', ?), (1, 'fresh', ?)", now.Add(-retention-time.Second).Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(context.Background(), "")
	if strings.Join(snapshot.recent, ",") != exactStorageKey("fresh") {
		t.Fatalf("recent snapshot = %v, want [%s]", snapshot.recent, exactStorageKey("fresh"))
	}
}

func TestSnapshotUsesOneCapturedTimeForRecentExpiry(t *testing.T) {
	first := time.Unix(2_000_000_000, 0)
	current := first
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPathWithClock(path, func() time.Time {
		captured := current
		current = current.Add(retention + time.Second)
		return captured
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec("INSERT INTO recent_exact(position, exact_id, selected_at) VALUES (0, 'boundary', ?)", first.Unix()); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(context.Background(), "")
	if strings.Join(snapshot.recent, ",") != exactStorageKey("boundary") {
		t.Fatalf("recent snapshot changed time during read: %v", snapshot.recent)
	}
}

func TestSnapshotFiltersExpiredUsageWithCapturedTime(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPathWithClock(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := now.Add(-retention - time.Second).Unix()
	if _, err := store.db.Exec("INSERT INTO exact_usage(exact_id, count, last_used) VALUES ('old-exact', 4, ?)", old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO resource_usage(resource_id, count, last_used) VALUES ('old-resource', 4, ?)", old); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(context.Background(), "")
	if _, ok := snapshot.exact[exactStorageKey("old-exact")]; ok {
		t.Fatal("expired exact usage was exposed in the snapshot")
	}
	if _, ok := snapshot.resource[resourceStorageKey("old-resource")]; ok {
		t.Fatal("expired resource usage was exposed in the snapshot")
	}
}

func TestOpenPathConcurrentStartupIsSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	const openers = 8
	start := make(chan struct{})
	errs := make(chan error, openers)
	var wg sync.WaitGroup
	for i := 0; i < openers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store, err := OpenPath(path)
			if err != nil {
				errs <- err
				return
			}
			_ = store.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent OpenPath failed: %v", err)
	}
}

func TestOpenPathDoesNotQuarantineLockedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	locker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer locker.Exec("ROLLBACK")
	if _, err := OpenPath(path); err == nil {
		t.Fatal("locked database unexpectedly opened")
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("locked database was quarantined: %v", matches)
	}
}

func TestCorruptStateIsQuarantinedAndRecreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ranking.sqlite3")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.Snapshot(context.Background(), "").Active() {
		t.Fatal("recreated state is inactive")
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("quarantine files = %v, want one", matches)
	}
}

func TestRecordSuccessWriteTimeoutFailsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer locker.Exec("ROLLBACK")
	started := time.Now()
	err = store.RecordSuccess(context.Background(), Keys{Exact: "slow"})
	if err == nil {
		t.Fatal("locked write unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > writeTimeout+500*time.Millisecond {
		t.Fatalf("locked write took %v, want fail-open timeout", elapsed)
	}
}

func TestRankingDoesNotUseOutboundHTTP(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = outboundFailTransport{}
	defer func() { http.DefaultTransport = old }()
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordSuccess(context.Background(), Keys{Exact: "opaque"}); err != nil {
		t.Fatal(err)
	}
	if !store.Snapshot(context.Background(), "").Active() {
		t.Fatal("ranking became inactive during local-only operation")
	}
}

type outboundFailTransport struct{}

func (outboundFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected outbound ranking request")
}

func TestRecordSuccessPersistsOpaqueVersionedKeysAtBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	const rawExact = "/private/action"
	const rawResource = "/private/project"
	if err := store.RecordSuccess(context.Background(), Keys{Exact: rawExact, Resource: rawResource}); err != nil {
		t.Fatal(err)
	}

	rows, err := store.db.Query("SELECT exact_id FROM exact_usage UNION ALL SELECT resource_id FROM resource_usage UNION ALL SELECT exact_id FROM recent_exact")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, value)
		if strings.Contains(value, rawExact) || strings.Contains(value, rawResource) {
			t.Fatalf("stored value exposes raw path: %q", value)
		}
		if !strings.HasPrefix(value, "v1:") {
			t.Fatalf("stored value is not versioned and opaque: %q", value)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored keys = %v, want exact, resource, and recent entries", stored)
	}

	snapshot := store.Snapshot(context.Background(), "")
	if snapshot.exact[pathKey("exact", rawExact)].count != 1 {
		t.Fatalf("snapshot lost normalized exact identity: %+v", snapshot.exact)
	}
	if snapshot.resource[pathKey("resource", rawResource)].count != 1 {
		t.Fatalf("snapshot lost normalized resource identity: %+v", snapshot.resource)
	}
}

func TestStoredRankingDataExcludesPresentationQueriesTemplatesAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ranking.sqlite3")
	store, err := OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	candidate := struct {
		Exact, Resource string
	}{"opaque-action-id", "/private/project"}
	if err := store.RecordSuccess(context.Background(), Keys{Exact: candidate.Exact, Resource: candidate.Resource}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query("SELECT exact_id FROM exact_usage UNION ALL SELECT resource_id FROM resource_usage UNION ALL SELECT exact_id FROM recent_exact")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"Pretty Label", "search query", "template-body", "SECRET_ENV"} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("stored value %q contains forbidden presentation data %q", value, forbidden)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func fileMode(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Mode().Perm()
}

func uniqueKey(i int) string {
	return "key-" + time.Duration(i).String()
}
