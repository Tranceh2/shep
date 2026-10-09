package pathutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSameDir_FastPathByteEqual confirms byte-identical strings return true
// without ever calling the injected stat function. A stat function that
// panics proves the fast path really skips the syscall.
func TestSameDir_FastPathByteEqual(t *testing.T) {
	t.Parallel()
	panics := func(string) (os.FileInfo, error) {
		panic("stat should not be called for byte-identical paths")
	}
	if !sameDirWith("/same/path", "/same/path", panics) {
		t.Error("expected true for byte-identical paths")
	}
}

// TestSameDir_MissingPathReturnsFalse confirms that when either path cannot
// be stat'd (does not exist, permission denied, etc.), SameDir reports false
// rather than guessing, so a not-yet-created candidate never matches.
func TestSameDir_MissingPathReturnsFalse(t *testing.T) {
	t.Parallel()
	errStat := func(string) (os.FileInfo, error) {
		return nil, errors.New("no such file")
	}
	if sameDirWith("/nonexistent/a", "/nonexistent/b", errStat) {
		t.Error("expected false when stat fails for both paths")
	}
}

// TestSameDir_OneMissingReturnsFalse confirms one side failing to stat is
// enough to report false, even if the other side would succeed.
func TestSameDir_OneMissingReturnsFalse(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	stat := func(p string) (os.FileInfo, error) {
		if p == tmp {
			return os.Stat(p)
		}
		return nil, errors.New("no such file")
	}
	if sameDirWith(tmp, "/nonexistent", stat) {
		t.Error("expected false when one side cannot be stat'd")
	}
}

// TestSameDir_RealSameInode stats the SAME real path twice (trivially the
// same inode) via a real os.Stat, proving the true SameDir codepath (not
// just the byte-equal fast path — the two calls use different string forms
// of the same directory so the fast path is skipped).
func TestSameDir_RealSameInode(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	// Two different (but equivalent) string forms of the same real path so
	// the byte-equal fast path is not what's under test.
	withSlash := tmp + string(filepath.Separator)
	if !SameDir(tmp, withSlash) {
		t.Errorf("expected SameDir(%q, %q) = true (same real directory)", tmp, withSlash)
	}
}

// TestSameDir_RealDifferentInode uses two different real temp dirs,
// guaranteed to have different inodes, to prove SameDir correctly reports
// false for genuinely distinct directories.
func TestSameDir_RealDifferentInode(t *testing.T) {
	t.Parallel()
	a := t.TempDir()
	b := t.TempDir()
	if SameDir(a, b) {
		t.Errorf("expected SameDir(%q, %q) = false (different real directories)", a, b)
	}
}

// TestSameDir_CaseInsensitiveFilesystem proves that on a
// case-insensitive-but-case-preserving filesystem (macOS APFS default,
// Windows), a path that differs only in case from a real directory
// resolves via Stat to the SAME inode, so SameDir must report true. On a
// case-sensitive filesystem (most Linux/ext4), the differently-cased lookup
// fails outright, so this test is gated to darwin/windows — mirroring the
// existing runtime.GOOS gate in pathutil_test.go's TestExpandTilde.
func TestSameDir_CaseInsensitiveFilesystem(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive lookup only guaranteed on darwin/windows")
	}
	t.Parallel()
	tmp := t.TempDir()
	original := filepath.Join(tmp, "Foo")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	differentCase := filepath.Join(tmp, "foo")
	if !SameDir(original, differentCase) {
		t.Errorf("expected SameDir(%q, %q) = true on case-insensitive filesystem", original, differentCase)
	}
}

// TestSameDir_CaseSensitiveFilesystemStaysDistinct documents the opposite
// side of the contract on genuinely case-sensitive filesystems: without a
// real directory at the differently-cased path, Stat fails and SameDir must
// report false rather than assuming identity. This runs on every platform
// (it never creates the differently-cased path, so it holds regardless of
// filesystem case sensitivity).
func TestSameDir_CaseSensitiveFilesystemStaysDistinct(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	original := filepath.Join(tmp, "Bar")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// "baz" was never created anywhere, so Stat must fail regardless of
	// filesystem case sensitivity.
	neverCreated := filepath.Join(tmp, "baz")
	if SameDir(original, neverCreated) {
		t.Errorf("expected SameDir(%q, %q) = false (second path never created)", original, neverCreated)
	}
}
