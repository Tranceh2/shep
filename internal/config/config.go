// Package config defines the shep configuration model, discovery rules and
// the binary probe used to gate optional source providers.
//
// Configuration lives at $XDG_CONFIG_HOME/shep/config.toml (resolved through
// os.UserConfigDir so it honours XDG on Linux and Library/Application Support
// on macOS). A missing config is not an error: callers fall back to
// Defaults(), which enables Herdr workspaces, zoxide (if installed) and the
// current working directory without any hardcoded user-specific paths.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Kind is the discriminator for Source entries.
type Kind string

// Supported source kinds. Built-in providers (cwd, herdr, zoxide) activate by
// default when their binary is available; the roots kind scans a user-supplied
// directory and therefore only ever appears in user configuration.
const (
	KindCwd    Kind = "cwd"
	KindHerdr  Kind = "herdr"
	KindZoxide Kind = "zoxide"
	KindRoots  Kind = "roots"
)

// Config is the top-level shep configuration document.
type Config struct {
	General General           `toml:"general,omitempty"`
	Herdr   Herdr            `toml:"herdr,omitempty"`
	Sources map[string]Source `toml:"sources,omitempty"`
	Layouts map[string]Layout `toml:"layouts,omitempty"`
}

// General holds global tweaks; provider order is optional.
type General struct {
	// ProviderOrder overrides the order in which providers are queried. When
	// empty, the registry uses a fixed default order.
	ProviderOrder []string `toml:"provider_order,omitempty"`
}

// Herdr configures how the shep<->Herdr bridge locates the binary.
type Herdr struct {
	// Binary overrides the executable name looked up via exec.LookPath. Empty
	// means "herdr".
	Binary string `toml:"binary,omitempty"`
}

// Source is one entry in the sources map. kind selects the provider; options
// carry provider-specific settings (e.g. roots -> {"path": "<dir>"}).
type Source struct {
	Kind    Kind               `toml:"kind"`
	Enabled bool               `toml:"enabled"`
	Options map[string]string  `toml:"options,omitempty"`
}

// Layout applies minimal startup behaviour to matching paths by glob.
type Layout struct {
	// Startup is run via `herdr pane run` after focusing/creating a workspace.
	Startup string `toml:"startup,omitempty"`
}

// Probes records which optional binaries are available at startup. The source
// registry consults this to skip providers whose binary is missing.
type Probes struct {
	Herdr  bool
	Zoxide bool
	Git    bool
	FD     bool
}

// Probe reports whether name resolves on PATH. It never returns an error:
// a missing binary is a normal, non-fatal state for shep.
func Probe(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// ProbesFor returns a Probes snapshot for the binaries shep cares about,
// honouring an optional herdr binary override from cfg.
func ProbesFor(cfg *Config) Probes {
	herdrBin := "herdr"
	if cfg != nil && cfg.Herdr.Binary != "" {
		herdrBin = cfg.Herdr.Binary
	}
	return Probes{
		Herdr:  Probe(herdrBin),
		Zoxide: Probe("zoxide"),
		Git:    Probe("git"),
		FD:     Probe("fd"),
	}
}

// Defaults returns a path-agnostic Config with built-in providers enabled and
// no user-specific roots. Absent config maps to this so shep works on a
// pristine machine without leaking developer paths into the shipped defaults.
func Defaults() *Config {
	return &Config{
		General: General{},
		Herdr:   Herdr{},
		Sources: map[string]Source{},
		Layouts: map[string]Layout{},
	}
}

// DiscoverPath returns the config file path resolved through the user config
// directory. It never creates files; callers (init) are responsible for that.
func DiscoverPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "shep", "config.toml"), nil
}

// Load reads and parses the config at path. When path is empty, DiscoverPath
// is used. A missing file is not an error and yields Defaults; all other
// read/parse failures are wrapped with their originating step.
func Load(path string) (*Config, error) {
	resolved := path
	if resolved == "" {
		p, err := DiscoverPath()
		if err != nil {
			return nil, err
		}
		resolved = p
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), nil
		}
		return nil, fmt.Errorf("read config %q: %w", resolved, err)
	}

	cfg := Defaults()
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", resolved, err)
	}
	if cfg.Sources == nil {
		cfg.Sources = map[string]Source{}
	}
	if cfg.Layouts == nil {
		cfg.Layouts = map[string]Layout{}
	}
	return cfg, nil
}

// HerdrBinary returns the configured herdr binary name, defaulting to "herdr".
func (c *Config) HerdrBinary() string {
	if c.Herdr.Binary != "" {
		return c.Herdr.Binary
	}
	return "herdr"
}