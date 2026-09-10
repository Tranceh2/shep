package herdrwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// maxUnixSocketPath is the Darwin AF_UNIX sun_path byte limit. A control socket
// path exceeding this is rejected with a clear error rather than truncated,
// which would silently point at the wrong endpoint.
const maxUnixSocketPath = 104

// Ownership represents an exclusive per-socket owner claim backed by an
// advisory file lock (flock). It is acquired BEFORE any startup state or
// control-path cleanup so a healthy control server never masquerades as proof
// of exclusive process ownership. No PID-kill or --force path exists.
type Ownership struct {
	path string
	file *os.File
}

// AcquireOwnership takes an exclusive non-blocking flock on lockPath. It fails
// (without blocking) when another live owner already holds the lock, which is
// how the singleton collector is enforced. The lock is released either
// explicitly via Release or automatically when the process exits.
func AcquireOwnership(lockPath string) (*Ownership, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire exclusive lock: %w", err)
	}

	return &Ownership{path: lockPath, file: f}, nil
}

// Release drops the flock and closes the lock file, allowing a dead-owner
// restart to reacquire.
func (o *Ownership) Release() error {
	if o == nil || o.file == nil {
		return nil
	}
	err := unlockFile(o.file)
	closeErr := o.file.Close()
	o.file = nil
	if err != nil {
		return fmt.Errorf("release lock: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close lock file: %w", closeErr)
	}
	return nil
}

// held reports whether this Ownership currently holds its lock.
func (o *Ownership) held() bool {
	return o != nil && o.file != nil
}

// PrepareControlPath validates that path fits the Darwin AF_UNIX limit and
// ensures its parent directory exists with private (0700) permissions. It
// returns the validated path unchanged; it never truncates an oversized path.
func PrepareControlPath(path string) (string, error) {
	if len(path) > maxUnixSocketPath {
		return "", fmt.Errorf("control socket path too long: %d bytes exceeds AF_UNIX limit of %d", len(path), maxUnixSocketPath)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create control directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure control directory: %w", err)
	}
	return path, nil
}

// CleanStaleEndpoint removes a stale control endpoint file, but ONLY when the
// caller proves exclusive ownership via a held Ownership. This guarantees an
// incumbent owner's live socket (or an endpoint belonging to another process)
// is never removed. A missing endpoint is a no-op.
func CleanStaleEndpoint(owner *Ownership, endpointPath string) error {
	if !owner.held() {
		return errors.New("refusing to clean control endpoint without exclusive ownership")
	}
	if err := os.Remove(endpointPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale endpoint: %w", err)
	}
	return nil
}
