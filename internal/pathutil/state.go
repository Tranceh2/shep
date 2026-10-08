package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// StatePath joins parts under the XDG state directory: $XDG_STATE_HOME when
// it is absolute, ~/.local/state otherwise.
func StatePath(parts ...string) (string, error) {
	return xdgPath("XDG_STATE_HOME", "state", filepath.Join(".local", "state"), parts)
}

// CachePath joins parts under the XDG cache directory: $XDG_CACHE_HOME when
// it is absolute, ~/.cache otherwise. What lives there can be rebuilt.
func CachePath(parts ...string) (string, error) {
	return xdgPath("XDG_CACHE_HOME", "cache", ".cache", parts)
}

// xdgPath joins parts under the directory the XDG variable env names when it
// is absolute, else under fallback in the home directory.
func xdgPath(env, kind, fallback string, parts []string) (string, error) {
	base := os.Getenv(env)
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			if err == nil {
				err = fmt.Errorf("empty home directory")
			}
			return "", fmt.Errorf("resolve %s directory: %w", kind, err)
		}
		base = filepath.Join(home, fallback)
	}
	return filepath.Join(append([]string{base}, parts...)...), nil
}
