package herdrwatch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/herdrwatch"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sh")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestOwnership_AcquireExclusive(t *testing.T) {
	dir := shortTempDir(t)
	lockPath := filepath.Join(dir, "own.lock")

	own, err := herdrwatch.AcquireOwnership(lockPath)
	if err != nil {
		t.Fatalf("AcquireOwnership failed: %v", err)
	}
	defer own.Release()

	// A second acquisition of the same lock path must fail (single owner).
	second, err := herdrwatch.AcquireOwnership(lockPath)
	if err == nil {
		second.Release()
		t.Errorf("expected second AcquireOwnership to fail while first holds the lock")
	}
}

func TestOwnership_ReleaseAllowsReacquire(t *testing.T) {
	dir := shortTempDir(t)
	lockPath := filepath.Join(dir, "own.lock")

	own, err := herdrwatch.AcquireOwnership(lockPath)
	if err != nil {
		t.Fatalf("AcquireOwnership failed: %v", err)
	}

	// After release, a new owner (dead-owner restart) must succeed.
	if err := own.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	own2, err := herdrwatch.AcquireOwnership(lockPath)
	if err != nil {
		t.Fatalf("expected reacquire after release to succeed, got: %v", err)
	}
	own2.Release()
}

func TestControlPath_ValidateLengthAndPerms(t *testing.T) {
	dir := shortTempDir(t)

	t.Run("valid short path", func(t *testing.T) {
		p := filepath.Join(dir, "c.sock")
		got, err := herdrwatch.PrepareControlPath(p)
		if err != nil {
			t.Fatalf("PrepareControlPath failed: %v", err)
		}
		if got != p {
			t.Errorf("expected control path %q, got %q", p, got)
		}
		// Parent dir must have private perms.
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat dir failed: %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("expected control dir perms 0700, got %o", info.Mode().Perm())
		}
	})

	t.Run("oversized path rejected with clear error not truncation", func(t *testing.T) {
		long := filepath.Join(dir, strings.Repeat("x", 200)+".sock")
		_, err := herdrwatch.PrepareControlPath(long)
		if err == nil {
			t.Errorf("expected oversized control path to be rejected, got nil error")
		}
		if err != nil && !strings.Contains(err.Error(), "too long") {
			t.Errorf("expected a clear 'too long' error, got: %v", err)
		}
	})
}

func TestControlPath_StaleEndpointRemovedOnlyWhenOwned(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "c.sock")

	// Simulate a stale endpoint file left behind by a dead owner.
	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile stale endpoint failed: %v", err)
	}

	lockPath := filepath.Join(dir, "own.lock")
	own, err := herdrwatch.AcquireOwnership(lockPath)
	if err != nil {
		t.Fatalf("AcquireOwnership failed: %v", err)
	}
	defer own.Release()

	// With exclusive ownership held, cleaning the stale endpoint is safe.
	if err := herdrwatch.CleanStaleEndpoint(own, sockPath); err != nil {
		t.Fatalf("CleanStaleEndpoint failed: %v", err)
	}
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("expected stale endpoint removed under exclusive ownership")
	}
}
