package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// maxHerdrConfigFileBytes bounds the Herdr config read to 1 MiB (matching
// maxEventLineBytes).
const maxHerdrConfigFileBytes = 1024 * 1024

// herdrConfig represents the minimal two-field subset of Herdr's config.toml
// needed to discover its active theme name safely.
type herdrConfig struct {
	Theme struct {
		Name string `toml:"name"`
	} `toml:"theme"`
}

// defaultHerdrConfigPath resolves the host Herdr configuration file path:
// $XDG_CONFIG_HOME/herdr/config.toml when set to an absolute path, otherwise
// ~/.config/herdr/config.toml (mirroring config.configHome's XDG order).
func defaultHerdrConfigPath() (string, error) {
	if xdg := envLookup("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "herdr", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve user home dir: %w", err)
	}
	return filepath.Join(home, ".config", "herdr", "config.toml"), nil
}

// herdrConfigPathFn is the injectable seam for resolving the Herdr config
// path, overridable in tests to guarantee hermetic execution.
var herdrConfigPathFn = defaultHerdrConfigPath

// herdrThemeName reads [theme].name from Herdr's config.toml via a safe,
// bounded (1 MiB) TOML decode. It returns (name, true) only if the config
// exists, is readable, well-formed, within the size bound, and specifies a
// recognized Shep theme name (one of mocha, macchiato, frappe, latte, plain).
// Unrecognized values (including flavourless "catppuccin", per #7144) and any
// read/parse errors return ("", false) without emitting error noise.
func herdrThemeName() (string, bool) {
	if herdrConfigPathFn == nil {
		return "", false
	}
	path, err := herdrConfigPathFn()
	if err != nil || path == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	lr := io.LimitReader(f, maxHerdrConfigFileBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil || len(data) > maxHerdrConfigFileBytes {
		return "", false
	}

	var cfg herdrConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return "", false
	}

	name := cfg.Theme.Name
	if _, ok := themes[name]; ok && name != "" {
		return name, true
	}
	return "", false
}
