package pathutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Normalize returns the canonical form of input, consolidating the logic
// previously duplicated between internal/resolver.Normalize and
// internal/herdr's package-local normalizePath (the herdr copy existed only
// to avoid a herdr -> resolver -> source import cycle; pathutil is a
// dependency-free leaf package so it can hold this logic once for both
// callers).
//
// The steps are applied in a fixed order so callers get a stable key:
//  1. expand a leading ~ to the user home dir
//  2. make the path absolute (anchored at cwd when needed)
//  3. clean redundant separators via filepath.Clean
//  4. trim a single trailing separator (keep root "/" untouched)
//  5. EvalSymlinks; on failure keep the cleaned absolute path so unresolved
//     paths remain distinct rather than collapsing to an empty string
//
// A returned error only happens when the initial expansion or absolute
// resolution itself fails (e.g. cwd unobtainable). Symlink failures never
// produce an error — they degrade to the cleaned path.
func Normalize(input string) (string, error) {
	if input == "" {
		return "", errors.New("normalize: empty path")
	}
	expanded, err := ExpandTilde(input)
	if err != nil {
		return "", fmt.Errorf("normalize %q: %w", input, err)
	}
	absolute, err := absPath(expanded)
	if err != nil {
		return "", fmt.Errorf("normalize %q: %w", input, err)
	}
	cleaned := filepath.Clean(absolute)
	cleaned = trimTrailingSep(cleaned)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved, nil
	}
	// Unresolved symlinks are not fatal: callers need a stable key for dedup.
	return cleaned, nil
}

// absPath makes a path absolute. Relative paths are anchored at the process
// cwd; an unobtainable cwd is a hard error because absolute forms underpin
// the rest of the normalisation pipeline.
func absPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, p), nil
}

// trimTrailingSep removes a single trailing separator while preserving the
// root "/" so POSIX semantics hold on normalized keys.
func trimTrailingSep(p string) string {
	if p == string(filepath.Separator) {
		return p
	}
	return strings.TrimRight(p, string(filepath.Separator))
}
