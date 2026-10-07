package theme

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// herdrThemeNames is Herdr v0.9.3's THEME_NAMES, in order.
var herdrThemeNames = []string{
	"catppuccin", "catppuccin-latte", "terminal", "tokyo-night", "tokyo-night-day",
	"dracula", "nord", "gruvbox", "gruvbox-light", "one-dark", "one-light",
	"solarized", "solarized-light", "kanagawa", "kanagawa-lotus", "rose-pine",
	"rose-pine-dawn", "vesper",
}

// herdrTokenOrder is the field order of Herdr's Palette struct.
var herdrTokenOrder = []string{
	"accent", "panel_bg", "sidebar_bg", "active_row_bg", "selection_bg",
	"surface0", "surface1", "surface_dim", "overlay0", "overlay1", "text",
	"subtext0", "mauve", "green", "yellow", "red", "blue", "teal", "peach",
}

func TestTokens_HerdrNamesAndOrder(t *testing.T) {
	tokens := Tokens()
	if len(tokens) != len(herdrTokenOrder) {
		t.Fatalf("len(Tokens()) = %d, want %d", len(tokens), len(herdrTokenOrder))
	}
	for i, tok := range tokens {
		if tok.String() != herdrTokenOrder[i] {
			t.Errorf("token %d = %q, want %q", i, tok, herdrTokenOrder[i])
		}
		got, ok := ParseToken(herdrTokenOrder[i])
		if !ok || got != tok {
			t.Errorf("ParseToken(%q) = %v, %v; want %v", herdrTokenOrder[i], got, ok, tok)
		}
	}
	for _, bad := range []string{"", "Accent", "panel-bg", "surface2", "row.label"} {
		if _, ok := ParseToken(bad); ok {
			t.Errorf("ParseToken(%q) succeeded", bad)
		}
	}
}

// TestBuiltins_MatchHerdrPalettes compares the Go palette data with the
// palettes extracted from Herdr v0.9.3 src/app/state.rs (testdata), token by
// token.
func TestBuiltins_MatchHerdrPalettes(t *testing.T) {
	data, err := os.ReadFile("testdata/herdr-palettes.json")
	if err != nil {
		t.Fatal(err)
	}
	var herdr map[string]map[string]string
	if err := json.Unmarshal(data, &herdr); err != nil {
		t.Fatal(err)
	}
	if len(herdr) != len(herdrThemeNames) {
		t.Fatalf("testdata has %d themes, want %d", len(herdr), len(herdrThemeNames))
	}
	for i, name := range herdrThemeNames {
		if builtinSpecs[i].name != name {
			t.Errorf("builtinSpecs[%d] = %q, want Herdr's %q", i, builtinSpecs[i].name, name)
		}
		want, ok := herdr[name]
		if !ok || len(want) != len(herdrTokenOrder) {
			t.Fatalf("testdata %q: %d tokens", name, len(want))
		}
		p := builtinPalettes[name]
		for _, tok := range Tokens() {
			if got, w := p.Get(tok), mustColor(t, want[tok.String()]); got != w {
				t.Errorf("%s.%s = %v, want Herdr's %v", name, tok, got, w)
			}
		}
	}
}

func TestBuiltins_EveryTokenSetAndParses(t *testing.T) {
	if len(builtinSpecs) != len(herdrThemeNames)+2 {
		t.Fatalf("len(builtinSpecs) = %d, want Herdr's 18 + 2", len(builtinSpecs))
	}
	for _, spec := range builtinSpecs {
		for i, s := range spec.colors {
			if s == "" {
				t.Errorf("%s.%s is not set", spec.name, Token(i))
				continue
			}
			if _, err := ParseColor(s); err != nil {
				t.Errorf("%s.%s: %v", spec.name, Token(i), err)
			}
		}
		if _, ok := builtinPalettes[spec.name]; !ok {
			t.Errorf("%s missing from builtinPalettes", spec.name)
		}
	}
}

// Herdr's built_in_themes_leave_sidebar_background_unset, extended to shep's
// additions.
func TestBuiltins_LeaveSidebarBackgroundUnset(t *testing.T) {
	for name, p := range builtinPalettes {
		if !p.Get(TokenSidebarBg).IsReset() {
			t.Errorf("%s.sidebar_bg = %v, want reset", name, p.Get(TokenSidebarBg))
		}
	}
}

func TestBuiltinNames(t *testing.T) {
	got := BuiltinNames()
	want := append(slices.Clone(herdrThemeNames), "catppuccin-frappe", "catppuccin-macchiato", "plain")
	if !slices.Equal(got, want) {
		t.Errorf("BuiltinNames() = %v, want %v", got, want)
	}
	for _, name := range got {
		if c, ok := CanonicalName(name); !ok || c != name {
			t.Errorf("CanonicalName(%q) = %q, %v", name, c, ok)
		}
	}
}

// TestCanonicalName_HerdrAliases covers every arm of Herdr's
// canonical_theme_name plus its normalization (case, spaces, underscores).
func TestCanonicalName_HerdrAliases(t *testing.T) {
	// One entry per arm of canonical_theme_name; the last aliases of some
	// entries exercise Herdr's normalization (case, spaces, underscores).
	tests := map[string][]string{
		"catppuccin":       {"catppuccin", "catppuccin-mocha", "Catppuccin Mocha"},
		"catppuccin-latte": {"catppuccin-latte", "latte", "light", "LIGHT"},
		"terminal":         {"terminal"},
		"tokyo-night":      {"tokyo-night", "tokyonight", "Tokyo Night"},
		"tokyo-night-day":  {"tokyo-night-day", "tokyo-day", "tokyonight-day", "TOKYO_NIGHT_DAY"},
		"dracula":          {"dracula"},
		"nord":             {"nord"},
		"gruvbox":          {"gruvbox", "gruvbox-dark"},
		"gruvbox-light":    {"gruvbox-light"},
		"one-dark":         {"one-dark", "onedark"},
		"one-light":        {"one-light", "onelight"},
		"solarized":        {"solarized", "solarized-dark"},
		"solarized-light":  {"solarized-light"},
		"kanagawa":         {"kanagawa"},
		"kanagawa-lotus":   {"kanagawa-lotus", "lotus"},
		"rose-pine":        {"rose-pine", "rosepine"},
		"rose-pine-dawn":   {"rose-pine-dawn", "rosepine-dawn", "dawn", "Rose_Pine Dawn"},
		"vesper":           {"vesper"},
	}
	if len(tests) != len(herdrThemeNames) {
		t.Fatalf("table covers %d themes, want %d", len(tests), len(herdrThemeNames))
	}
	for want, aliases := range tests {
		for _, in := range aliases {
			if got, ok := herdrCanonicalName(in); !ok || got != want {
				t.Errorf("herdrCanonicalName(%q) = %q, %v; want %q", in, got, ok, want)
			}
			if got, ok := CanonicalName(in); !ok || got != want {
				t.Errorf("CanonicalName(%q) = %q, %v; want %q", in, got, ok, want)
			}
		}
	}
	for _, in := range []string{"", "catppucin", "tokio-night", "inherit", "mine", " nord", "nord "} {
		if got, ok := CanonicalName(in); ok {
			t.Errorf("CanonicalName(%q) = %q, want unknown", in, got)
		}
	}
}

func TestCanonicalName_ShepAdditionsAreNotHerdrNames(t *testing.T) {
	tests := map[string]string{
		"mocha":                "catppuccin",
		"frappe":               "catppuccin-frappe",
		"catppuccin-frappe":    "catppuccin-frappe",
		"Catppuccin_Frappe":    "catppuccin-frappe",
		"macchiato":            "catppuccin-macchiato",
		"catppuccin-macchiato": "catppuccin-macchiato",
		"plain":                "plain",
		"Plain":                "plain",
	}
	for in, want := range tests {
		if got, ok := CanonicalName(in); !ok || got != want {
			t.Errorf("CanonicalName(%q) = %q, %v; want %q", in, got, ok, want)
		}
		if got, ok := herdrCanonicalName(in); ok {
			t.Errorf("herdrCanonicalName(%q) = %q; Herdr does not know it", in, got)
		}
	}
}

// TestCatppuccinFlavors_UseHerdrMochaMapping checks the official Frappé and
// Macchiato values and that both follow the structure of Herdr's Mocha
// mapping.
func TestCatppuccinFlavors_UseHerdrMochaMapping(t *testing.T) {
	spot := map[string]map[Token]string{
		"catppuccin-frappe": {
			TokenAccent: "#8caaee", TokenPanelBg: "#292c3c", TokenActiveRowBg: "#303446",
			TokenSelectionBg: "#414559", TokenSurface1: "#51576d", TokenOverlay0: "#737994",
			TokenOverlay1: "#838ba7", TokenText: "#c6d0f5", TokenSubtext0: "#a5adce",
			TokenMauve: "#ca9ee6", TokenGreen: "#a6d189", TokenYellow: "#e5c890", TokenRed: "#e78284",
			TokenTeal: "#81c8be", TokenPeach: "#ef9f76",
		},
		"catppuccin-macchiato": {
			TokenAccent: "#8aadf4", TokenPanelBg: "#1e2030", TokenActiveRowBg: "#24273a",
			TokenSelectionBg: "#363a4f", TokenSurface1: "#494d64", TokenOverlay0: "#6e738d",
			TokenOverlay1: "#8087a2", TokenText: "#cad3f5", TokenSubtext0: "#a5adcb",
			TokenMauve: "#c6a0f6", TokenGreen: "#a6da95", TokenYellow: "#eed49f", TokenRed: "#ed8796",
			TokenTeal: "#8bd5ca", TokenPeach: "#f5a97f",
		},
	}
	for name, want := range spot {
		p := builtinPalettes[name]
		for tok, hex := range want {
			if got := p.Get(tok); got != mustColor(t, hex) {
				t.Errorf("%s.%s = %v, want %s", name, tok, got, hex)
			}
		}
	}
	for _, name := range []string{"catppuccin", "catppuccin-frappe", "catppuccin-macchiato"} {
		p := builtinPalettes[name]
		for _, pair := range [][2]Token{
			{TokenAccent, TokenBlue},
			{TokenSelectionBg, TokenSurface0},
			{TokenSurfaceDim, TokenActiveRowBg},
		} {
			if p.Get(pair[0]) != p.Get(pair[1]) {
				t.Errorf("%s: %s = %v, want it equal to %s = %v", name, pair[0], p.Get(pair[0]), pair[1], p.Get(pair[1]))
			}
		}
	}
}

func TestPalette_GetWith(t *testing.T) {
	base := builtinPalettes["nord"]
	red := RGB(255, 0, 0)
	changed := base.With(TokenAccent, red)
	if changed.Get(TokenAccent) != red {
		t.Errorf("With did not set accent")
	}
	if base.Get(TokenAccent) == red {
		t.Errorf("With modified the receiver")
	}
	if changed.Get(TokenText) != base.Get(TokenText) {
		t.Errorf("With changed another token")
	}
	if got := base.With(Token(numTokens), red); got != base {
		t.Errorf("With(out of range) changed the palette")
	}
	if !base.Get(Token(200)).IsReset() {
		t.Errorf("Get(out of range) is not reset")
	}
	if got := Token(200).String(); got != "Token(200)" {
		t.Errorf("Token(200).String() = %q", got)
	}
}
