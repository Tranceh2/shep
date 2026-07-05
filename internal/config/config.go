// Package config defines the shep configuration model, discovery rules and
// the binary probe used to gate optional source providers.
//
// Configuration lives at $XDG_CONFIG_HOME/shep/config.toml (falling back to
// ~/.config/shep/config.toml when XDG_CONFIG_HOME is unset), so shep uses the
// same location across Linux and macOS. A missing config is not an error:
// callers fall back to
// Defaults(), which enables Herdr workspaces, zoxide (if installed) and the
// current working directory without any hardcoded user-specific paths.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

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

// Selector values for the [general].selector field. They pick the interactive
// candidate picker used by `shep open` after the direct (exact/single) match.
// SelectorBuiltin skips fzf and always uses the Bubble Tea TUI; SelectorFzf
// prefers fzf and falls back to the TUI when the binary is missing; SelectorAuto
// preserves the v1 cascade (fzf if present else Bubble Tea).
const (
	SelectorBuiltin = "builtin"
	SelectorFzf     = "fzf"
	SelectorAuto    = "auto"
)

// Config is the top-level shep configuration document.
type Config struct {
	General General           `toml:"general,omitempty"`
	Herdr   Herdr             `toml:"herdr,omitempty"`
	Sources map[string]Source `toml:"sources,omitempty"`
	Layouts map[string]Layout `toml:"layouts,omitempty"`
	// Preview configures the workspace preview shown in the selector and by
	// `shep preview <path>`. Absent [preview] is normalized to safe preview
	// defaults while keeping command/sections empty, so the renderer falls back to
	// its built-in default layout.
	Preview PreviewConfig `toml:"preview,omitempty"`
}

// General holds global tweaks; provider order is optional.
type General struct {
	// ProviderOrder overrides the order in which providers are queried. When
	// empty, the registry uses a fixed default order.
	ProviderOrder []string `toml:"provider_order,omitempty"`
	// Selector picks the interactive picker for `shep open` after the direct
	// match. Valid values are builtin, fzf, auto (see the Selector* constants).
	// Absent or empty defaults to builtin (Bubble Tea TUI).
	Selector string `toml:"selector,omitempty"`
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
	Kind    Kind              `toml:"kind"`
	Enabled bool              `toml:"enabled"`
	Options map[string]string `toml:"options,omitempty"`
}

// Layout applies minimal startup behaviour to matching paths by glob.
type Layout struct {
	// Startup is run via `herdr pane run` after focusing/creating a workspace.
	Startup string `toml:"startup,omitempty"`
}

// Preview section type values. A "builtin" section renders the named candidate
// fields directly; a "git" section renders a fast git summary.
const (
	PreviewSectionBuiltin = "builtin"
	PreviewSectionGit     = "git"
)

// Valid builtin section field names drawn from the candidate. Unknown field
// names are rejected at Load so a typo fails fast instead of silently dropping a
// line.
const (
	PreviewFieldPath     = "path"
	PreviewFieldLabel    = "label"
	PreviewFieldSource   = "source"
	PreviewFieldTemplate = "template"
)

// Default preview durations and output cap. They apply when the user omits the
// field (zero-value) so a custom preview.command still gets a safe timeout,
// cache, and line cap without explicit config.
const (
	defaultPreviewTimeout  = 100 * time.Millisecond
	defaultPreviewCacheTTL = 5 * time.Second
	defaultPreviewMaxLines = 50
)

// Duration wraps time.Duration so TOML string values ("100ms", "5s") parse via
// time.ParseDuration. time.Duration itself has no UnmarshalText, so go-toml v2
// cannot decode into it directly.
type Duration time.Duration

// UnmarshalText parses a TOML duration string into a Duration.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// PreviewConfig configures the workspace preview rendered in the Bubble Tea
// selector and `shep preview <path>`. The built-in default (no [preview] table)
// shows label, path, source, a matched template (when present), and a fast git
// summary; [[preview.sections]] override the layout in declaration order; and
// preview.command is an escape hatch executed safely with timeout and output
// caps, falling back to the built-in preview on any failure.
type PreviewConfig struct {
	// Command is a shell-style command with a {path} placeholder. When set, the
	// renderer executes it (argv-parsed, no sh -c) and shows its stdout, capped
	// to MaxLines and cached for CacheTTL.
	Command string `toml:"command,omitempty"`
	// Timeout bounds a custom command's execution. Defaults to 100ms.
	Timeout Duration `toml:"timeout,omitempty"`
	// CacheTTL is the in-memory cache lifetime for a command's stdout.
	// Defaults to 5s.
	CacheTTL Duration `toml:"cache_ttl,omitempty"`
	// MaxLines caps the number of stdout lines kept from a custom command.
	// Defaults to 50.
	MaxLines int `toml:"max_lines,omitempty"`
	// Sections is the ordered declarative preview layout. Empty means the
	// built-in default layout.
	Sections []PreviewSection `toml:"sections,omitempty"`
}

// PreviewSection is one entry in the [[preview.sections]] list. Name is a
// section heading; Type selects the render engine ("builtin" or "git"); Fields
// lists candidate fields for builtin sections (path, label, source, template).
type PreviewSection struct {
	Name   string   `toml:"name,omitempty"`
	Type   string   `toml:"type"`
	Fields []string `toml:"fields,omitempty"`
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
	cfg := &Config{
		General: General{Selector: SelectorBuiltin},
		Herdr:   Herdr{},
		Sources: map[string]Source{},
		Layouts: map[string]Layout{},
	}
	normalizePreview(&cfg.Preview)
	return cfg
}

// DiscoverPath returns the config file path. It prefers the XDG base directory
// ($XDG_CONFIG_HOME, or ~/.config when unset) on every platform so shep lives
// alongside other XDG-style tools, including on macOS. It falls back to
// os.UserConfigDir only when no home directory can be resolved. It never
// creates files; callers (init) are responsible for that.
func DiscoverPath() (string, error) {
	if dir := configHome(); dir != "" {
		return filepath.Join(dir, "shep", "config.toml"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "shep", "config.toml"), nil
}

// configHome resolves the XDG config base directory: $XDG_CONFIG_HOME when set
// to an absolute path, otherwise ~/.config. Returns "" when neither is
// available so the caller can fall back to os.UserConfigDir.
func configHome() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return xdg
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
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
			cfg := Defaults()
			if err := validatePreview(cfg.Preview); err != nil {
				return nil, fmt.Errorf("parse config %q: %w", resolved, err)
			}
			return cfg, nil
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
	if cfg.General.Selector == "" {
		cfg.General.Selector = SelectorBuiltin
	}
	if !isValidSelector(cfg.General.Selector) {
		return nil, fmt.Errorf("invalid general.selector %q (valid: builtin, fzf, auto)", cfg.General.Selector)
	}
	normalizePreview(&cfg.Preview)
	if err := validatePreview(cfg.Preview); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", resolved, err)
	}
	return cfg, nil
}

// normalizePreview fills zero-value durations and max_lines with the documented
// defaults. It runs even when [preview] is absent because those defaults are
// only meaningful once a custom command is configured.
func normalizePreview(p *PreviewConfig) {
	if p.Timeout == 0 {
		p.Timeout = Duration(defaultPreviewTimeout)
	}
	if p.CacheTTL == 0 {
		p.CacheTTL = Duration(defaultPreviewCacheTTL)
	}
	if p.MaxLines == 0 {
		p.MaxLines = defaultPreviewMaxLines
	}
}

// validBuiltinFields is the set of candidate fields a builtin section may name.
var validBuiltinFields = map[string]bool{
	PreviewFieldPath: true, PreviewFieldLabel: true,
	PreviewFieldSource: true, PreviewFieldTemplate: true,
}

// validatePreview enforces the preview schema invariants the renderer relies
// on: each section has a known type, builtin fields come from the supported
// set, and numeric caps are non-negative. It runs after the strict TOML parse.
func validatePreview(p PreviewConfig) error {
	if p.MaxLines < 0 {
		return fmt.Errorf("preview.max_lines must be >= 0, got %d", p.MaxLines)
	}
	if p.Timeout < 0 {
		return fmt.Errorf("preview.timeout must be >= 0, got %v", time.Duration(p.Timeout))
	}
	if p.CacheTTL < 0 {
		return fmt.Errorf("preview.cache_ttl must be >= 0, got %v", time.Duration(p.CacheTTL))
	}
	for i, sec := range p.Sections {
		switch sec.Type {
		case PreviewSectionBuiltin:
			for _, f := range sec.Fields {
				if !validBuiltinFields[f] {
					return fmt.Errorf("preview.sections[%d]: field %q is invalid (valid: %s, %s, %s, %s)",
						i, f, PreviewFieldPath, PreviewFieldLabel, PreviewFieldSource, PreviewFieldTemplate)
				}
			}
		case PreviewSectionGit:
			// git sections render a fixed summary; fields are ignored.
		default:
			return fmt.Errorf("preview.sections[%d]: type %q is invalid (valid: %s, %s)",
				i, sec.Type, PreviewSectionBuiltin, PreviewSectionGit)
		}
	}
	return nil
}

// isValidSelector reports whether s is one of the supported selector values.
func isValidSelector(s string) bool {
	switch s {
	case SelectorBuiltin, SelectorFzf, SelectorAuto:
		return true
	}
	return false
}

// HerdrBinary returns the configured herdr binary name, defaulting to "herdr".
func (c *Config) HerdrBinary() string {
	if c.Herdr.Binary != "" {
		return c.Herdr.Binary
	}
	return "herdr"
}
