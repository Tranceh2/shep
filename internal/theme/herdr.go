package theme

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// maxHerdrConfigBytes bounds the Herdr configuration read to 1 MiB.
const maxHerdrConfigBytes = 1 << 20

// HerdrTheme is the theme part of Herdr's config.toml. Nil string pointers
// are settings Herdr's file leaves unset (Herdr distinguishes unset from
// empty: an unset dark_name follows name, an empty one is an unknown theme).
// Color maps are keyed by token name and hold the values as written; keys
// that are not tokens are ignored, like Herdr does.
type HerdrTheme struct {
	// Name is [theme].name.
	Name *string
	// AutoSwitch is [theme].auto_switch.
	AutoSwitch bool
	// DarkName is [theme].dark_name.
	DarkName *string
	// LightName is [theme].light_name.
	LightName *string
	// Custom is [theme.custom].
	Custom map[string]string
	// CustomLight is [theme.custom.light].
	CustomLight map[string]string
	// CustomDark is [theme.custom.dark].
	CustomDark map[string]string
	// UIAccent is Herdr's legacy [ui].accent, which Herdr still applies to
	// the accent token when it is not "cyan" (its default) and
	// [theme.custom] does not set accent.
	UIAccent *string
}

// DefaultHerdrConfigPath returns the Herdr configuration file shep inherits
// from: $HERDR_CONFIG_PATH when set to an absolute path (Herdr's own
// override), else $XDG_CONFIG_HOME/herdr/config.toml when XDG_CONFIG_HOME is
// absolute, else <home>/.config/herdr/config.toml. Relative environment
// values are ignored because they would resolve against shep's working
// directory, not Herdr's. It returns "" when home is empty and no variable
// applies.
func DefaultHerdrConfigPath(getenv func(string) string, home string) string {
	if p := getenv("HERDR_CONFIG_PATH"); filepath.IsAbs(p) {
		return p
	}
	if xdg := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "herdr", "config.toml")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "config.toml")
}

// herdrFile is the subset of Herdr's config.toml that affects its colors.
// Every other key is ignored: Herdr owns that file.
type herdrFile struct {
	Theme struct {
		Name       *string        `toml:"name"`
		AutoSwitch bool           `toml:"auto_switch"`
		DarkName   *string        `toml:"dark_name"`
		LightName  *string        `toml:"light_name"`
		Custom     map[string]any `toml:"custom"`
	} `toml:"theme"`
	UI struct {
		Accent *string `toml:"accent"`
	} `toml:"ui"`
}

// ReadHerdrTheme reads the theme settings of the Herdr configuration at path
// (at most 1 MiB). A missing file is reported with an error wrapping
// fs.ErrNotExist.
func ReadHerdrTheme(path string) (HerdrTheme, error) {
	f, err := os.Open(path)
	if err != nil {
		return HerdrTheme{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxHerdrConfigBytes+1))
	if err != nil {
		return HerdrTheme{}, fmt.Errorf("read herdr config %s: %w", path, err)
	}
	if len(data) > maxHerdrConfigBytes {
		return HerdrTheme{}, fmt.Errorf("herdr config %s is larger than 1 MiB", path)
	}
	var file herdrFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return HerdrTheme{}, fmt.Errorf("parse herdr config %s: %w", path, err)
	}
	h := HerdrTheme{
		Name:       file.Theme.Name,
		AutoSwitch: file.Theme.AutoSwitch,
		DarkName:   file.Theme.DarkName,
		LightName:  file.Theme.LightName,
		UIAccent:   file.UI.Accent,
	}
	custom := file.Theme.Custom
	if h.Custom, err = herdrColors(custom, "theme.custom"); err != nil {
		return HerdrTheme{}, fmt.Errorf("herdr config %s: %w", path, err)
	}
	for _, mode := range []struct {
		key string
		dst *map[string]string
	}{{"light", &h.CustomLight}, {"dark", &h.CustomDark}} {
		v, ok := custom[mode.key]
		if !ok {
			continue
		}
		table, ok := v.(map[string]any)
		if !ok {
			return HerdrTheme{}, fmt.Errorf("herdr config %s: theme.custom.%s: want a table, got %T", path, mode.key, v)
		}
		if *mode.dst, err = herdrColors(table, "theme.custom."+mode.key); err != nil {
			return HerdrTheme{}, fmt.Errorf("herdr config %s: %w", path, err)
		}
	}
	return h, nil
}

// herdrColors keeps the token entries of a decoded [theme.custom]-style
// table. A token set to a non-string is an error, as it is for Herdr.
func herdrColors(table map[string]any, key string) (map[string]string, error) {
	var out map[string]string
	for k, v := range table {
		if _, ok := ParseToken(k); !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s: want a color string, got %T", key, k, v)
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[k] = s
	}
	return out, nil
}

// herdrSiblingNames is Herdr's sibling_theme_names: the default dark and
// light theme names auto_switch uses when dark_name or light_name is unset.
// A theme without a light/dark sibling uses its own name for both.
func herdrSiblingNames(name string) (dark, light string) {
	switch normalizeName(name) {
	case "catppuccin", "catppuccin-mocha", "catppuccin-latte", "latte", "light":
		return "catppuccin", "catppuccin-latte"
	case "tokyo-night", "tokyonight", "tokyo-night-day", "tokyo-day", "tokyonight-day":
		return "tokyo-night", "tokyo-night-day"
	case "gruvbox", "gruvbox-dark", "gruvbox-light":
		return "gruvbox", "gruvbox-light"
	case "one-dark", "onedark", "one-light", "onelight":
		return "one-dark", "one-light"
	case "solarized", "solarized-dark", "solarized-light":
		return "solarized", "solarized-light"
	case "kanagawa", "kanagawa-lotus", "lotus":
		return "kanagawa", "kanagawa-lotus"
	case "rose-pine", "rosepine", "rose-pine-dawn", "rosepine-dawn", "dawn":
		return "rose-pine", "rose-pine-dawn"
	}
	return name, name
}

// selection returns the theme name Herdr would pick, its fallback when the
// name is unknown, and the appearance-specific overrides to layer last
// (Herdr's resolve_effective_theme).
func (h HerdrTheme) selection(dark bool) (name, fallback string, mode map[string]string) {
	manual := NameDefault
	if h.Name != nil {
		manual = *h.Name
	}
	if !h.AutoSwitch {
		return manual, NameDefault, nil
	}
	siblingDark, siblingLight := herdrSiblingNames(manual)
	if dark {
		name = siblingDark
		if h.DarkName != nil {
			name = *h.DarkName
		}
		return name, NameDefault, h.CustomDark
	}
	name = siblingLight
	if h.LightName != nil {
		name = *h.LightName
	}
	return name, "catppuccin-latte", h.CustomLight
}

// ThemeName returns the canonical name of the Herdr built-in theme the
// palette is based on for the given appearance (dark is only consulted when
// auto_switch is on), after Herdr's fallback for unknown names.
func (h HerdrTheme) ThemeName(dark bool) string {
	name, fallback, _ := h.selection(dark)
	if c, ok := herdrCanonicalName(name); ok {
		return c
	}
	return fallback
}

// Resolve returns the palette Herdr renders with, using Herdr's own
// algorithm: the base theme from [theme].name (or, with auto_switch,
// dark_name/light_name for the given appearance, each defaulting to name's
// dark/light sibling), falling back to catppuccin (catppuccin-latte for the
// light appearance) when the name is unknown; then [theme.custom]; then the
// legacy [ui].accent; then [theme.custom.dark] or [theme.custom.light] when
// auto_switch is on. An unparsable color renders as cyan, as in Herdr.
func (h HerdrTheme) Resolve(dark bool) Palette {
	_, _, mode := h.selection(dark)
	p := builtinPalettes[h.ThemeName(dark)]
	p = applyHerdrColors(p, h.Custom)
	if h.UIAccent != nil && *h.UIAccent != "cyan" {
		if _, set := h.Custom[TokenAccent.String()]; !set {
			p = p.With(TokenAccent, herdrColor(*h.UIAccent))
		}
	}
	return applyHerdrColors(p, mode)
}

func applyHerdrColors(p Palette, colors map[string]string) Palette {
	for k, v := range colors {
		if t, ok := ParseToken(k); ok {
			p = p.With(t, herdrColor(v))
		}
	}
	return p
}

// herdrColor parses a color the way Herdr does: invalid colors become cyan.
func herdrColor(s string) Color {
	c, err := ParseColor(s)
	if err != nil {
		return Color{kind: kindANSI, ansi: namedColors["cyan"]}
	}
	return c
}

// Diagnostics returns Herdr's own diagnostics for these settings (unknown
// theme names, in Herdr's wording) followed by every color Herdr would
// replace with cyan, sorted, for shep doctor.
func (h HerdrTheme) Diagnostics() []string {
	var out []string
	valid := strings.Join(herdrNames, ", ")
	for _, f := range []struct {
		field    string
		value    *string
		fallback string
	}{
		{"theme.name", h.Name, NameDefault},
		{"theme.dark_name", h.DarkName, NameDefault},
		{"theme.light_name", h.LightName, "catppuccin-latte"},
	} {
		if f.value == nil {
			continue
		}
		if _, ok := herdrCanonicalName(*f.value); !ok {
			out = append(out, fmt.Sprintf("unknown theme name %s = %q; using %q; valid themes: %s", f.field, *f.value, f.fallback, valid))
		}
	}
	var colors []string
	for _, table := range []struct {
		key    string
		colors map[string]string
	}{
		{"theme.custom", h.Custom},
		{"theme.custom.light", h.CustomLight},
		{"theme.custom.dark", h.CustomDark},
	} {
		for k, v := range table.colors {
			if _, err := ParseColor(v); err != nil {
				colors = append(colors, fmt.Sprintf("%s.%s: %v; Herdr uses cyan", table.key, k, err))
			}
		}
	}
	if h.UIAccent != nil {
		if _, err := ParseColor(*h.UIAccent); err != nil {
			colors = append(colors, fmt.Sprintf("ui.accent: %v; Herdr uses cyan", err))
		}
	}
	sort.Strings(colors)
	return append(out, colors...)
}

// herdrNames are Herdr's built-in theme names (its THEME_NAMES), which
// lead builtinSpecs.
var herdrNames = func() []string {
	var out []string
	for _, spec := range builtinSpecs {
		if _, ok := herdrCanonicalName(spec.name); ok {
			out = append(out, spec.name)
		}
	}
	return out
}()
