package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
)

func StatePath(parts ...string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			if err == nil {
				err = fmt.Errorf("empty home directory")
			}
			return "", fmt.Errorf("resolve state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(append([]string{base}, parts...)...), nil
}
