package history_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tranceh2/shep/internal/history"
)

func TestCanonicalSessionKey(t *testing.T) {
	tempDir := t.TempDir()
	sockPath := filepath.Join(tempDir, "herdr.sock")
	if err := os.WriteFile(sockPath, []byte("sock"), 0o600); err != nil {
		t.Fatalf("failed to write test sock file: %v", err)
	}

	key1, err := history.CanonicalSessionKey(sockPath)
	if err != nil {
		t.Fatalf("CanonicalSessionKey unexpected error: %v", err)
	}
	if key1 == "" {
		t.Fatalf("CanonicalSessionKey returned empty key")
	}

	// Calling again on same path returns identical key
	key2, err := history.CanonicalSessionKey(sockPath)
	if err != nil {
		t.Fatalf("CanonicalSessionKey second call error: %v", err)
	}
	if key1 != key2 {
		t.Errorf("CanonicalSessionKey mismatch: got %q, want %q", key2, key1)
	}

	// Invalid input returns error
	_, errBad := history.CanonicalSessionKey("")
	if errBad == nil {
		t.Errorf("CanonicalSessionKey(\"\") expected error, got nil")
	}

	_, errNull := history.CanonicalSessionKey("path\x00withnull")
	if errNull == nil {
		t.Errorf("CanonicalSessionKey with null byte expected error, got nil")
	}
}

func TestStore_RecordAndList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	key := "session-1"

	// 1. Initial list empty
	mru, err := store.List(ctx, key)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(mru) != 0 {
		t.Fatalf("expected empty MRU, got %v", mru)
	}

	// 2. Record first item -> changed=true
	changed, err := store.Record(ctx, key, "ws-A")
	if err != nil {
		t.Fatalf("Record ws-A failed: %v", err)
	}
	if !changed {
		t.Errorf("expected Record ws-A changed=true, got false")
	}

	// 3. Record same item at head -> changed=false (duplicate head)
	changed, err = store.Record(ctx, key, "ws-A")
	if err != nil {
		t.Fatalf("Record duplicate ws-A failed: %v", err)
	}
	if changed {
		t.Errorf("expected Record duplicate ws-A changed=false, got true")
	}

	// 4. Record second item ws-B -> changed=true
	changed, err = store.Record(ctx, key, "ws-B")
	if err != nil {
		t.Fatalf("Record ws-B failed: %v", err)
	}
	if !changed {
		t.Errorf("expected Record ws-B changed=true, got false")
	}

	// List should be newest-first: ["ws-B", "ws-A"]
	mru, err = store.List(ctx, key)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(mru) != 2 || mru[0] != "ws-B" || mru[1] != "ws-A" {
		t.Fatalf("expected [ws-B, ws-A], got %v", mru)
	}

	// 5. Re-record ws-A (moving ws-A back to head) -> changed=true
	changed, err = store.Record(ctx, key, "ws-A")
	if err != nil {
		t.Fatalf("Re-record ws-A failed: %v", err)
	}
	if !changed {
		t.Errorf("expected Re-record ws-A changed=true, got false")
	}

	mru, err = store.List(ctx, key)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(mru) != 2 || mru[0] != "ws-A" || mru[1] != "ws-B" {
		t.Fatalf("expected [ws-A, ws-B], got %v", mru)
	}
}

func TestStore_Remove(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	key := "session-1"

	// Removing missing ID is a no-op (returns nil error)
	if err := store.Remove(ctx, key, "ws-nonexistent"); err != nil {
		t.Fatalf("Remove missing ID returned error: %v", err)
	}

	store.Record(ctx, key, "ws-1")
	store.Record(ctx, key, "ws-2")

	if err := store.Remove(ctx, key, "ws-1"); err != nil {
		t.Fatalf("Remove ws-1 failed: %v", err)
	}

	mru, err := store.List(ctx, key)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(mru) != 1 || mru[0] != "ws-2" {
		t.Fatalf("expected [ws-2], got %v", mru)
	}
}

func TestStore_Cap50Eviction(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	key := "session-cap"

	// Record 51 distinct workspace IDs
	for i := 1; i <= 51; i++ {
		wsID := fmt.Sprintf("ws-%02d", i)
		changed, err := store.Record(ctx, key, wsID)
		if err != nil {
			t.Fatalf("Record %s failed: %v", wsID, err)
		}
		if !changed {
			t.Errorf("expected Record %s changed=true", wsID)
		}
	}

	mru, err := store.List(ctx, key)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(mru) != 50 {
		t.Fatalf("expected MRU length 50, got %d", len(mru))
	}

	// Newest should be ws-51, oldest remaining should be ws-02 (ws-01 evicted)
	if mru[0] != "ws-51" {
		t.Errorf("expected newest ws-51, got %s", mru[0])
	}
	if mru[49] != "ws-02" {
		t.Errorf("expected oldest remaining ws-02, got %s", mru[49])
	}
}

func TestStore_ConcurrencyAndIsolation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	var wg sync.WaitGroup

	key1 := "key-concurrent-1"
	key2 := "key-concurrent-2"

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			wsID := fmt.Sprintf("ws1-%d", i)
			store.Record(ctx, key1, wsID)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			wsID := fmt.Sprintf("ws2-%d", i)
			store.Record(ctx, key2, wsID)
		}
	}()

	wg.Wait()

	mru1, err1 := store.List(ctx, key1)
	mru2, err2 := store.List(ctx, key2)
	if err1 != nil || err2 != nil {
		t.Fatalf("List failed: %v, %v", err1, err2)
	}

	if len(mru1) != 20 || len(mru2) != 20 {
		t.Errorf("expected 20 items in both sessions, got len1=%d len2=%d", len(mru1), len(mru2))
	}
}

func TestStore_CorruptDBAndUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()

	// 1. Corrupt file header
	corruptPath := filepath.Join(dir, "corrupt.sqlite3")
	if err := os.WriteFile(corruptPath, []byte("NOT A SQLITE FILE HEADER DATA"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, errCorrupt := history.OpenPath(corruptPath)
	if errCorrupt == nil {
		t.Fatalf("OpenPath corrupt DB expected error, got nil")
	}

	// Fail-closed: corrupt file must NOT be deleted
	if _, statErr := os.Stat(corruptPath); statErr != nil {
		t.Errorf("corrupt DB file was deleted: %v", statErr)
	}

	// 2. Unsupported schema version (version 999)
	futurePath := filepath.Join(dir, "future.sqlite3")
	db, err := sql.Open("sqlite", futurePath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	_, err = db.Exec("CREATE TABLE schema_version (version INTEGER PRIMARY KEY); INSERT INTO schema_version VALUES (999);")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	db.Close()

	_, errFuture := history.OpenPath(futurePath)
	if !errors.Is(errFuture, history.ErrUnsupportedVersion) {
		t.Errorf("expected ErrUnsupportedVersion, got %v", errFuture)
	}
}

func TestStore_DuplicateHeadNoWrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	key := "session-dup"

	changed1, err := store.Record(ctx, key, "ws-head")
	if err != nil || !changed1 {
		t.Fatalf("Record 1 failed: %v, changed=%v", err, changed1)
	}

	// Second record of same head returns changed=false
	changed2, err := store.Record(ctx, key, "ws-head")
	if err != nil {
		t.Fatalf("Record 2 unexpected error: %v", err)
	}
	if changed2 {
		t.Errorf("expected changed=false on duplicate head, got true")
	}
}

func TestStore_ContextCancellation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jump_history.sqlite3")
	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Pre-cancel context

	_, errRecord := store.Record(ctx, "session-cancel", "ws-cancel")
	if errRecord == nil {
		t.Errorf("Record with cancelled context expected error, got nil")
	}

	_, errList := store.List(ctx, "session-cancel")
	if errList == nil {
		t.Errorf("List with cancelled context expected error, got nil")
	}
}

func TestStore_ProtectionFailureFailsClosed(t *testing.T) {
	old := history.ChmodPath
	history.ChmodPath = func(string, os.FileMode) error { return errors.New("chmod denied") }
	t.Cleanup(func() { history.ChmodPath = old })
	_, err := history.OpenPath(filepath.Join(t.TempDir(), "history.sqlite3"))
	if err == nil || !strings.Contains(err.Error(), "protect history directory") {
		t.Fatalf("OpenPath error = %v, want protection failure", err)
	}
}

func TestStore_Permissions(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "sub")
	dbPath := filepath.Join(parent, "jump_history.sqlite3")

	store, err := history.OpenPath(dbPath)
	if err != nil {
		t.Fatalf("OpenPath failed: %v", err)
	}
	defer store.Close()

	infoDir, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("Stat parent dir failed: %v", err)
	}
	if infoDir.Mode().Perm() != 0o700 {
		t.Errorf("expected dir perms 0700, got %o", infoDir.Mode().Perm())
	}

	infoFile, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat db file failed: %v", err)
	}
	if infoFile.Mode().Perm() != 0o600 {
		t.Errorf("expected file perms 0600, got %o", infoFile.Mode().Perm())
	}
}
