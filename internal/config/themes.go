package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tranceh2/shep/internal/theme"
)

// ThemeTable is one [themes.<name>] table as written:
//
//	[themes.mine]
//	base = "nord"            # a built-in theme, "inherit" or another custom theme
//	accent = "#ff79c6"       # any of the 19 palette tokens
//	[themes.mine.roles]
//	row.detail = "subtext0"  # role = token, role or color
//
// Role names contain dots, so TOML reads an unquoted role key as nested
// tables; both that spelling and a quoted key ("row.detail") are accepted.
type ThemeTable map[string]any

// CustomThemes converts the [themes.<name>] tables into theme definitions.
// It reports the first malformed value (a non-string color, a roles key that
// is not a table) with its field path; unknown tokens and roles, bad colors
// and base cycles are reported by theme.Validate.
func (c *Config) CustomThemes() (map[string]theme.Custom, error) {
	if len(c.Themes) == 0 {
		return nil, nil
	}
	out := make(map[string]theme.Custom, len(c.Themes))
	for _, name := range slices.Sorted(maps.Keys(c.Themes)) {
		custom, err := c.Themes[name].custom("themes." + name)
		if err != nil {
			return nil, err
		}
		out[name] = custom
	}
	return out, nil
}

func (t ThemeTable) custom(field string) (theme.Custom, error) {
	var out theme.Custom
	for _, key := range slices.Sorted(maps.Keys(t)) {
		value := t[key]
		switch key {
		case "base":
			s, ok := value.(string)
			if !ok {
				return theme.Custom{}, fmt.Errorf("%s.base: must be a theme name string", field)
			}
			out.Base = s
		case "roles":
			table, ok := value.(map[string]any)
			if !ok {
				return theme.Custom{}, fmt.Errorf("%s.roles: must be a table of role = token, role or color", field)
			}
			out.Roles = make(map[string]string)
			if err := flattenRoles(field+".roles", "", table, out.Roles); err != nil {
				return theme.Custom{}, err
			}
		default:
			s, ok := value.(string)
			if !ok {
				return theme.Custom{}, fmt.Errorf("%s.%s: must be a color string", field, key)
			}
			if out.Tokens == nil {
				out.Tokens = make(map[string]string)
			}
			out.Tokens[key] = s
		}
	}
	return out, nil
}

// flattenRoles collects table's string values under their dotted role names
// (a nested table {row = {detail = "x"}} is the role "row.detail").
func flattenRoles(field, prefix string, table map[string]any, out map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(table)) {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		switch v := table[key].(type) {
		case string:
			out[name] = v
		case map[string]any:
			if err := flattenRoles(field, name, v, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.%s: must be a token, role or color string", field, name)
		}
	}
	return nil
}

// validateThemes checks every [themes.<name>] table and that [tui].theme
// names inherit, a built-in theme or alias, plain, or a declared custom
// theme.
func validateThemes(cfg *Config) error {
	customs, err := cfg.CustomThemes()
	if err != nil {
		return err
	}
	if err := theme.Validate(customs); err != nil {
		return err
	}
	if name := strings.TrimSpace(cfg.TUI.Theme); cfg.TUI.Theme != "" {
		if name != cfg.TUI.Theme {
			return fmt.Errorf("tui.theme: %q must not have surrounding whitespace", cfg.TUI.Theme)
		}
		if _, err := theme.Build(name, customs, theme.HerdrTheme{}, true); err != nil {
			return fmt.Errorf("tui.theme: %w", err)
		}
	}
	return nil
}
