package theme

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadHerdrTheme_UserConfigShape(t *testing.T) {
	h, err := ReadHerdrTheme("testdata/herdr-user.toml")
	if err != nil {
		t.Fatal(err)
	}
	if h.Name == nil || *h.Name != "catppuccin" || h.AutoSwitch || h.DarkName != nil || h.LightName != nil {
		t.Errorf("theme settings = %+v", h)
	}
	if len(h.Custom) != 19 {
		t.Errorf("custom has %d tokens, want 19 (unknown keys ignored): %v", len(h.Custom), h.Custom)
	}
	if h.CustomLight != nil || h.CustomDark != nil {
		t.Errorf("mode overrides = %v / %v, want none", h.CustomLight, h.CustomDark)
	}
	if h.UIAccent == nil || *h.UIAccent != "#cba6f7" {
		t.Errorf("ui.accent = %v", h.UIAccent)
	}
	want := map[Token]Color{
		TokenAccent:      RGB(0xcb, 0xa6, 0xf7),
		TokenPanelBg:     {},
		TokenSidebarBg:   {},
		TokenActiveRowBg: RGB(0x38, 0x32, 0x4c),
		TokenSelectionBg: RGB(0x49, 0x40, 0x60),
		TokenSurfaceDim:  RGB(0x18, 0x18, 0x25),
		TokenOverlay1:    RGB(0x93, 0x99, 0xb2),
		TokenText:        RGB(0xcd, 0xd6, 0xf4),
		TokenPeach:       RGB(0xfa, 0xb3, 0x87),
	}
	for _, dark := range []bool{true, false} {
		if got := h.ThemeName(dark); got != "catppuccin" {
			t.Errorf("ThemeName(%v) = %q", dark, got)
		}
		p := h.Resolve(dark)
		for tok, c := range want {
			if p.Get(tok) != c {
				t.Errorf("Resolve(%v).%s = %v, want %v", dark, tok, p.Get(tok), c)
			}
		}
	}
	if d := h.Diagnostics(); len(d) != 0 {
		t.Errorf("Diagnostics = %v, want none", d)
	}
}

// Herdr's theme_auto_switch_layers_active_mode_overrides_last, from a file.
func TestReadHerdrTheme_AutoSwitchModeOverrides(t *testing.T) {
	h, err := ReadHerdrTheme("testdata/herdr-auto.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !h.AutoSwitch || len(h.Custom) != 2 || len(h.CustomLight) != 1 || len(h.CustomDark) != 3 {
		t.Fatalf("parsed = %+v", h)
	}
	dark := h.Resolve(true)
	if h.ThemeName(true) != "gruvbox" {
		t.Errorf("dark theme = %q, want gruvbox", h.ThemeName(true))
	}
	for tok, c := range map[Token]Color{
		TokenAccent:      RGB(1, 2, 3),
		TokenText:        RGB(10, 11, 12),
		TokenSidebarBg:   RGB(13, 14, 15),
		TokenActiveRowBg: RGB(16, 17, 18),
		TokenGreen:       builtinPalettes["gruvbox"].Get(TokenGreen),
	} {
		if dark.Get(tok) != c {
			t.Errorf("dark %s = %v, want %v", tok, dark.Get(tok), c)
		}
	}
	light := h.Resolve(false)
	if h.ThemeName(false) != "gruvbox-light" {
		t.Errorf("light theme = %q, want gruvbox-light", h.ThemeName(false))
	}
	for tok, c := range map[Token]Color{
		TokenAccent:      RGB(7, 8, 9),
		TokenText:        RGB(4, 5, 6),
		TokenSidebarBg:   {},
		TokenActiveRowBg: builtinPalettes["gruvbox-light"].Get(TokenActiveRowBg),
	} {
		if light.Get(tok) != c {
			t.Errorf("light %s = %v, want %v", tok, light.Get(tok), c)
		}
	}
}

func TestReadHerdrTheme_Errors(t *testing.T) {
	_, err := ReadHerdrTheme(filepath.Join(t.TempDir(), "missing.toml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file error = %v, want fs.ErrNotExist", err)
	}

	limit := strings.Repeat("#", maxHerdrConfigBytes-len("[theme]\nname = \"nord\"\n")-1) + "\n"
	h, err := ReadHerdrTheme(writeFile(t, "[theme]\nname = \"nord\"\n"+limit))
	if err != nil || h.Name == nil || *h.Name != "nord" {
		t.Errorf("exactly 1 MiB: %+v, %v", h, err)
	}
	if _, err := ReadHerdrTheme(writeFile(t, "[theme]\nname = \"nord\"\n"+limit+"#")); err == nil || !strings.Contains(err.Error(), "larger than 1 MiB") {
		t.Errorf("1 MiB + 1 error = %v", err)
	}

	for content, want := range map[string]string{
		"[theme\nname = 1":                      "parse herdr config",
		"[theme]\nname = 5":                     "parse herdr config",
		"[theme]\nauto_switch = \"yes\"":        "parse herdr config",
		"[ui]\naccent = 1":                      "parse herdr config",
		"[theme.custom]\naccent = 5":            "theme.custom.accent: want a color string, got int64",
		"[theme.custom]\nlight = \"#fff\"":      "theme.custom.light: want a table, got string",
		"[theme.custom.dark]\nred = [\"#fff\"]": "theme.custom.dark.red: want a color string",
	} {
		_, err := ReadHerdrTheme(writeFile(t, content))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", content, err, want)
		}
	}

	h, err = ReadHerdrTheme(writeFile(t, "[theme.custom]\nnot_a_token = 5\naccent = \"#fff\"\n[theme.custom.light]\nwhatever = true\n"))
	if err != nil || len(h.Custom) != 1 || h.CustomLight != nil {
		t.Errorf("unknown keys: %+v, %v; want them ignored", h, err)
	}
}

func TestHerdrTheme_ResolveAlgorithm(t *testing.T) {
	tests := []struct {
		name  string
		h     HerdrTheme
		dark  bool
		theme string
		want  map[Token]Color // spot checks on top of the base palette
	}{
		{name: "unset is catppuccin", theme: "catppuccin", dark: true},
		{name: "unset is catppuccin in light too", theme: "catppuccin", dark: false},
		{name: "alias", h: HerdrTheme{Name: ptr("Tokyo Night")}, theme: "tokyo-night"},
		{name: "unknown falls back", h: HerdrTheme{Name: ptr("catppucin")}, theme: "catppuccin"},
		{name: "empty name is unknown", h: HerdrTheme{Name: ptr("")}, theme: "catppuccin"},
		{name: "manual ignores appearance and dark_name", h: HerdrTheme{Name: ptr("nord"), DarkName: ptr("dracula"), LightName: ptr("latte")}, theme: "nord"},
		{name: "auto dark uses sibling", h: HerdrTheme{Name: ptr("tokyonight"), AutoSwitch: true}, dark: true, theme: "tokyo-night"},
		{name: "auto light uses sibling", h: HerdrTheme{Name: ptr("tokyonight"), AutoSwitch: true}, dark: false, theme: "tokyo-night-day"},
		{name: "auto light sibling of a light name", h: HerdrTheme{Name: ptr("dawn"), AutoSwitch: true}, dark: true, theme: "rose-pine"},
		{name: "auto without sibling keeps the name", h: HerdrTheme{Name: ptr("dracula"), AutoSwitch: true}, dark: false, theme: "dracula"},
		{name: "auto default name", h: HerdrTheme{AutoSwitch: true}, dark: false, theme: "catppuccin-latte"},
		{name: "auto explicit names", h: HerdrTheme{Name: ptr("nord"), AutoSwitch: true, DarkName: ptr("vesper"), LightName: ptr("one-light")}, dark: false, theme: "one-light"},
		{name: "auto unknown light falls back to latte", h: HerdrTheme{AutoSwitch: true, LightName: ptr("lattee")}, dark: false, theme: "catppuccin-latte"},
		{name: "auto unknown dark falls back to catppuccin", h: HerdrTheme{Name: ptr("gruvbox"), AutoSwitch: true, DarkName: ptr("tokio-night")}, dark: true, theme: "catppuccin"},
		{name: "auto empty dark_name is unknown, not the sibling", h: HerdrTheme{Name: ptr("tokyo-night"), AutoSwitch: true, DarkName: ptr("")}, dark: true, theme: "catppuccin"},
		{name: "auto unknown name without sibling: light fallback", h: HerdrTheme{Name: ptr("nope"), AutoSwitch: true}, dark: false, theme: "catppuccin-latte"},
		{
			name: "mode overrides only with auto_switch",
			h: HerdrTheme{Name: ptr("tokyo-night"), CustomLight: map[string]string{"accent": "#010203"},
				CustomDark: map[string]string{"accent": "#040506"}},
			dark: true, theme: "tokyo-night",
			want: map[Token]Color{TokenAccent: builtinPalettes["tokyo-night"].Get(TokenAccent)},
		},
		{
			name: "custom after the active base",
			h:    HerdrTheme{Name: ptr("gruvbox"), AutoSwitch: true, Custom: map[string]string{"accent": "#010203"}},
			dark: false, theme: "gruvbox-light",
			want: map[Token]Color{TokenAccent: RGB(1, 2, 3)},
		},
		{
			name: "legacy ui.accent without custom accent",
			h:    HerdrTheme{UIAccent: ptr("#ff0000")},
			dark: true, theme: "catppuccin",
			want: map[Token]Color{TokenAccent: RGB(255, 0, 0)},
		},
		{
			name: "legacy ui.accent loses to custom accent",
			h:    HerdrTheme{UIAccent: ptr("#ff0000"), Custom: map[string]string{"accent": "#010203"}},
			dark: true, theme: "catppuccin",
			want: map[Token]Color{TokenAccent: RGB(1, 2, 3)},
		},
		{
			name: "legacy ui.accent cyan is Herdr's default and ignored",
			h:    HerdrTheme{UIAccent: ptr("cyan")},
			dark: true, theme: "catppuccin",
			want: map[Token]Color{TokenAccent: builtinPalettes["catppuccin"].Get(TokenAccent)},
		},
		{
			name: "mode override beats legacy ui.accent",
			h:    HerdrTheme{AutoSwitch: true, UIAccent: ptr("#ff0000"), CustomDark: map[string]string{"accent": "#040506"}},
			dark: true, theme: "catppuccin",
			want: map[Token]Color{TokenAccent: RGB(4, 5, 6)},
		},
		{
			name: "invalid colors render cyan like Herdr",
			h:    HerdrTheme{Custom: map[string]string{"red": "#12", "teal": "orange"}, UIAccent: ptr("bogus")},
			dark: true, theme: "catppuccin",
			want: map[Token]Color{TokenRed: ansi(6), TokenTeal: ansi(6), TokenAccent: ansi(6)},
		},
		{
			name: "reset and named colors",
			h:    HerdrTheme{Name: ptr("terminal"), Custom: map[string]string{"panel_bg": "transparent", "blue": "LightBlue", "unknown": "#000"}},
			dark: true, theme: "terminal",
			want: map[Token]Color{TokenPanelBg: {}, TokenBlue: ansi(12), TokenAccent: ansi(4), TokenRed: ansi(9)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.ThemeName(tt.dark); got != tt.theme {
				t.Fatalf("ThemeName = %q, want %q", got, tt.theme)
			}
			got := tt.h.Resolve(tt.dark)
			base := builtinPalettes[tt.theme]
			for _, tok := range Tokens() {
				want, ok := tt.want[tok]
				if !ok {
					want = base.Get(tok)
				}
				if got.Get(tok) != want {
					t.Errorf("%s = %v, want %v", tok, got.Get(tok), want)
				}
			}
		})
	}
}

// Herdr's unknown_theme_names_are_diagnosed, plus the colors Herdr turns cyan.
func TestHerdrTheme_Diagnostics(t *testing.T) {
	h := HerdrTheme{
		Name:        ptr("catppucin"),
		DarkName:    ptr("tokio-night"),
		LightName:   ptr("lattee"),
		Custom:      map[string]string{"red": "#12", "blue": "#89b4fa"},
		CustomLight: map[string]string{"text": "nope"},
		UIAccent:    ptr("bogus"),
	}
	d := h.Diagnostics()
	if len(d) != 6 {
		t.Fatalf("Diagnostics = %d entries, want 6:\n%s", len(d), strings.Join(d, "\n"))
	}
	for i, want := range []string{
		`unknown theme name theme.name = "catppucin"; using "catppuccin"; valid themes: catppuccin, catppuccin-latte, terminal,`,
		`unknown theme name theme.dark_name = "tokio-night"; using "catppuccin"`,
		`unknown theme name theme.light_name = "lattee"; using "catppuccin-latte"`,
		`theme.custom.light.text: invalid color "nope"`,
		`theme.custom.red: invalid color "#12"`,
		`ui.accent: invalid color "bogus"`,
	} {
		if !strings.HasPrefix(d[i], want) {
			t.Errorf("Diagnostics[%d] = %q, want prefix %q", i, d[i], want)
		}
	}
	if !strings.HasSuffix(d[0], "rose-pine-dawn, vesper") {
		t.Errorf("valid themes list = %q, want Herdr's 18 names only", d[0])
	}
	if !strings.HasSuffix(d[4], "; Herdr uses cyan") {
		t.Errorf("color diagnostic = %q", d[4])
	}
	if d := (HerdrTheme{Name: ptr("Tokyo Night"), Custom: map[string]string{"red": "reset"}}).Diagnostics(); len(d) != 0 {
		t.Errorf("valid settings diagnosed: %v", d)
	}
}

func TestDefaultHerdrConfigPath(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		home string
		want string
	}{
		{"home", nil, "/home/u", "/home/u/.config/herdr/config.toml"},
		{"xdg absolute", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/home/u", "/xdg/herdr/config.toml"},
		{"xdg relative ignored", map[string]string{"XDG_CONFIG_HOME": "xdg"}, "/home/u", "/home/u/.config/herdr/config.toml"},
		{"herdr override", map[string]string{"HERDR_CONFIG_PATH": "/etc/herdr.toml", "XDG_CONFIG_HOME": "/xdg"}, "/home/u", "/etc/herdr.toml"},
		{"herdr override relative ignored", map[string]string{"HERDR_CONFIG_PATH": "herdr.toml", "XDG_CONFIG_HOME": "/xdg"}, "/home/u", "/xdg/herdr/config.toml"},
		{"no home", nil, "", ""},
		{"no home with xdg", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "", "/xdg/herdr/config.toml"},
	}
	for _, tt := range tests {
		getenv := func(k string) string { return tt.env[k] }
		if got := DefaultHerdrConfigPath(getenv, tt.home); got != tt.want {
			t.Errorf("%s: DefaultHerdrConfigPath = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Every arm of Herdr's sibling_theme_names.
func TestHerdrSiblingNames(t *testing.T) {
	tests := map[[2]string][]string{
		{"catppuccin", "catppuccin-latte"}: {"catppuccin", "catppuccin-mocha", "catppuccin-latte", "latte", "light"},
		{"tokyo-night", "tokyo-night-day"}: {"tokyo-night", "tokyonight", "tokyo-night-day", "tokyo-day", "Tokyonight_Day"},
		{"gruvbox", "gruvbox-light"}:       {"gruvbox", "gruvbox-dark", "gruvbox-light"},
		{"one-dark", "one-light"}:          {"one-dark", "onedark", "one-light", "onelight"},
		{"solarized", "solarized-light"}:   {"solarized", "solarized-dark", "solarized-light"},
		{"kanagawa", "kanagawa-lotus"}:     {"kanagawa", "kanagawa-lotus", "lotus"},
		{"rose-pine", "rose-pine-dawn"}:    {"rose-pine", "rosepine", "rose-pine-dawn", "rosepine-dawn", "dawn"},
		{"Dracula", "Dracula"}:             {"Dracula"},
		{"vesper", "vesper"}:               {"vesper"},
		{"no such", "no such"}:             {"no such"},
	}
	for want, names := range tests {
		for _, name := range names {
			if dark, light := herdrSiblingNames(name); dark != want[0] || light != want[1] {
				t.Errorf("herdrSiblingNames(%q) = %q, %q; want %q, %q", name, dark, light, want[0], want[1])
			}
		}
	}
}
