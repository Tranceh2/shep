// Package pathutil holds the single canonical implementation of path
// shorthand expansion shared across config, source, resolver and command.
//
// It is a leaf package: it depends only on the standard library so that any
// internal package can import it without creating a cycle.
package pathutil

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandTilde replaces a leading "~" (the user's home directory) or "~/..."
// (or "~" followed by the OS path separator) in p with the resolved home
// directory, joined via filepath.Join so separators are normalised.
//
// A path without a leading "~" is returned verbatim with a nil error. An
// unresolvable home directory (no HOME / no user record) is reported as an
// error rather than a silent pass-through: callers that want best-effort
// degradation should fall back to the original input themselves, e.g.
//
//	expanded, err := pathutil.ExpandTilde(p)
//	if err != nil {
//	    expanded = p
//	}
//
// This contract matches the strictest of the previously-duplicated helpers
// (internal/resolver), which needed a hard error to keep dedup deterministic.
func ExpandTilde(p string) (string, error) {
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
