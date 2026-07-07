package pathutil

import "os"

// SameDir reports whether a and b refer to the exact same directory or file
// on disk, using the filesystem's own notion of identity (device + inode via
// os.SameFile) rather than assuming case sensitivity from the running OS.
// This correctly treats differently-cased paths as identical on a
// case-insensitive filesystem (macOS APFS default, Windows) and as genuinely
// distinct on a case-sensitive one (most Linux setups, e.g. ext4) — the
// kernel/filesystem itself resolves the lookup, so shep never has to guess
// by runtime.GOOS.
//
// Fast path: byte-identical strings return true without any syscall.
// Otherwise both paths are Stat'd; if either Stat fails (path does not
// exist, permission denied, or any other error), SameDir returns false — a
// path that cannot be verified on disk is never treated as matching
// another, which preserves existing behavior for not-yet-created/missing
// candidates. When both Stats succeed, the device+inode pair is compared via
// os.SameFile.
func SameDir(a, b string) bool {
	return sameDirWith(a, b, os.Stat)
}

// sameDirWith is SameDir's algorithm with an injectable stat function, so
// tests can exercise the comparison logic (both-exist-same-inode,
// both-exist-different-inode, one-or-both-missing) without depending on the
// test runner's case sensitivity for every case.
func sameDirWith(a, b string, stat func(string) (os.FileInfo, error)) bool {
	if a == b {
		return true
	}
	fiA, errA := stat(a)
	if errA != nil {
		return false
	}
	fiB, errB := stat(b)
	if errB != nil {
		return false
	}
	return os.SameFile(fiA, fiB)
}
