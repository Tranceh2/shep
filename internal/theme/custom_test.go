package theme

import (
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestBuild_BuiltinInheritAndPlain(t *testing.T) {
	th, err := Build("Tokyo Night", nil, HerdrTheme{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "tokyo-night" || th.NoColor || th.Source.Kind != SourceBuiltin || th.Palette() != builtinPalettes["tokyo-night"] {
		t.Errorf("Build(Tokyo Night) = %+v", th)
	}

	th, err = Build("inherit", nil, HerdrTheme{Name: ptr("nord"), Custom: map[string]string{"accent": "#010203"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "inherit" || th.Source.Kind != SourceInherit || th.Source.Herdr != "nord" {
		t.Errorf("Build(inherit) = %+v", th)
	}
	if got := th.Role(RolePrompt); got != RGB(1, 2, 3) {
		t.Errorf("inherit prompt = %v, want Herdr's custom accent", got)
	}
	if got := th.Role(RoleText); got != builtinPalettes["nord"].Get(TokenText) {
		t.Errorf("inherit text = %v, want nord's", got)
	}

	th, err = Build("plain", nil, HerdrTheme{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if th.Name != "plain" || !th.NoColor || th.Source.Kind != SourceBuiltin || th.Palette() != (Palette{}) {
		t.Errorf("Build(plain) = %+v", th)
	}
}

func TestBuild_CustomBaseChains(t *testing.T) {
	customs := map[string]Custom{
		"base":    {Base: "nord", Tokens: map[string]string{"accent": "#010203", "red": "red"}},
		"child":   {Base: "base", Tokens: map[string]string{"text": "#040506"}, Roles: map[string]string{"row.label": "accent"}},
		"grand":   {Base: "child", Roles: map[string]string{"row.label": "peach"}},
		"herdr":   {Base: "inherit", Tokens: map[string]string{"blue": "rgb(7,8,9)"}},
		"onherdr": {Base: "herdr"},
		"default": {},
		"alias":   {Base: "Tokyo_Night_Day"},
	}
	herdr := HerdrTheme{Name: ptr("dracula")}
	nord := builtinPalettes["nord"]

	child, err := Build("child", customs, herdr, true)
	if err != nil {
		t.Fatal(err)
	}
	if child.Name != "child" || child.Source.Kind != SourceCustom || child.Source.Herdr != "" {
		t.Errorf("child = %+v", child)
	}
	want := nord.With(TokenAccent, RGB(1, 2, 3)).With(TokenRed, ansi(1)).With(TokenText, RGB(4, 5, 6))
	if child.Palette() != want {
		t.Errorf("child palette = %+v, want nord with base and child overrides", child.Palette())
	}
	if got := child.Role(RoleRowLabel); got != RGB(1, 2, 3) {
		t.Errorf("child row.label = %v, want the base's accent", got)
	}
	if got := child.Role(RoleError); got != ansi(1) {
		t.Errorf("child error = %v, want the overridden red token", got)
	}

	grand, err := Build("grand", customs, herdr, true)
	if err != nil {
		t.Fatal(err)
	}
	if grand.Palette() != want || grand.Role(RoleRowLabel) != nord.Get(TokenPeach) {
		t.Errorf("grand: palette or row.label override not layered over child")
	}

	for _, name := range []string{"herdr", "onherdr"} {
		th, err := Build(name, customs, herdr, true)
		if err != nil {
			t.Fatal(err)
		}
		if th.Source.Kind != SourceCustom || th.Source.Herdr != "dracula" {
			t.Errorf("%s source = %+v, want custom on inherit:dracula", name, th.Source)
		}
		if th.Palette() != builtinPalettes["dracula"].With(TokenBlue, RGB(7, 8, 9)) {
			t.Errorf("%s palette is not Herdr's dracula with the blue override", name)
		}
		if got := th.Role(RoleSourceZoxide); got != RGB(7, 8, 9) {
			t.Errorf("%s source.zoxide = %v", name, got)
		}
		if got := th.Source.String(); got != "custom on inherit:dracula" {
			t.Errorf("%s Source.String() = %q", name, got)
		}
	}

	for name, base := range map[string]string{"default": "catppuccin", "alias": "tokyo-night-day"} {
		th, err := Build(name, customs, herdr, true)
		if err != nil {
			t.Fatal(err)
		}
		if th.Palette() != builtinPalettes[base] {
			t.Errorf("%s palette is not %s", name, base)
		}
	}
}

func TestBuild_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		build   string
		customs map[string]Custom
		want    string // exact unless it ends with "…"
	}{
		{"unknown theme", "nope", nil,
			`unknown theme "nope"; want inherit, a built-in theme (catppuccin, catppuccin-latte, terminal, …`},
		{"unknown theme lists customs", "nope", map[string]Custom{"b": {}, "a": {}},
			`unknown theme "nope"; want inherit, a built-in theme (catppuccin, catppuccin-latte, terminal, tokyo-night, tokyo-night-day, dracula, nord, gruvbox, gruvbox-light, one-dark, one-light, solarized, solarized-light, kanagawa, kanagawa-lotus, rose-pine, rose-pine-dawn, vesper, catppuccin-frappe, catppuccin-macchiato, plain) or a custom theme (a, b)`},
		{"invalid token color", "mine", map[string]Custom{"mine": {Tokens: map[string]string{"accent": "#12"}}},
			`themes.mine.accent: invalid color "#12": want #rrggbb, #rgb, rgb(r,g,b), a named color or reset`},
		{"unknown token", "mine", map[string]Custom{"mine": {Tokens: map[string]string{"acent": "#fff"}}},
			`themes.mine.acent: unknown token "acent" (want one of accent, panel_bg, sidebar_bg, active_row_bg, selection_bg, surface0, surface1, surface_dim, overlay0, overlay1, text, subtext0, mauve, green, yellow, red, blue, teal, peach)`},
		{"unknown role", "mine", map[string]Custom{"mine": {Roles: map[string]string{"row.detial": "text"}}},
			`themes.mine.roles.row.detial: unknown role "row.detial"`},
		{"unknown role value", "mine", map[string]Custom{"mine": {Roles: map[string]string{"row.detail": "x"}}},
			`themes.mine.roles.row.detail: unknown token or role "x" …`},
		{"invalid role color", "mine", map[string]Custom{"mine": {Roles: map[string]string{"row.detail": "#12"}}},
			`themes.mine.roles.row.detail: invalid color "#12": want #rrggbb, #rgb, rgb(r,g,b), a named color or reset`},
		{"unknown base", "mine", map[string]Custom{"mine": {Base: "x"}},
			`themes.mine.base: unknown theme "x"; want inherit, …`},
		{"plain base", "mine", map[string]Custom{"mine": {Base: "Plain"}},
			`themes.mine.base: "Plain" has no colors to extend; use "terminal" for the terminal's own colors`},
		{"base cycle", "a", map[string]Custom{"a": {Base: "b"}, "b": {Base: "a"}},
			`themes.b.base: cycle a -> b -> a`},
		{"self base", "a", map[string]Custom{"a": {Base: "a"}},
			`themes.a.base: cycle a -> a`},
		{"long cycle from outside", "x", map[string]Custom{"x": {Base: "a"}, "a": {Base: "b"}, "b": {Base: "c"}, "c": {Base: "a"}},
			`themes.c.base: cycle a -> b -> c -> a`},
		{"error in a base names the base", "a", map[string]Custom{"a": {Base: "b"}, "b": {Tokens: map[string]string{"red": "bogus"}}},
			`themes.b.red: invalid color "bogus": want #rrggbb, #rgb, rgb(r,g,b), a named color or reset`},
		{"reserved built-in name", "nord", map[string]Custom{"nord": {}},
			`themes.nord: the name is reserved by the built-in theme "nord"`},
		{"reserved alias as base", "mine", map[string]Custom{"mine": {Base: "Dawn"}, "Dawn": {}},
			`themes.Dawn: the name is reserved by the built-in theme "rose-pine-dawn"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(tt.build, tt.customs, HerdrTheme{}, true)
			if err == nil {
				t.Fatalf("Build succeeded, want %q", tt.want)
			}
			if prefix, ok := strings.CutSuffix(tt.want, "…"); ok {
				if !strings.HasPrefix(err.Error(), prefix) {
					t.Fatalf("error = %q, want prefix %q", err, prefix)
				}
				return
			}
			if err.Error() != tt.want {
				t.Fatalf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestValidate_ReportsEveryProblemOnce(t *testing.T) {
	customs := map[string]Custom{
		"good":    {Base: "inherit", Roles: map[string]string{"pin": "red"}},
		"bad1":    {Tokens: map[string]string{"accent": "nope"}},
		"bad2":    {Base: "bad1"},
		"inherit": {},
		"Dracula": {},
		"":        {},
	}
	err := Validate(customs)
	if err == nil {
		t.Fatal("Validate succeeded")
	}
	want := strings.Join([]string{
		`themes: a theme name must not be empty`,
		`themes.Dracula: the name is reserved by the built-in theme "dracula"`,
		`themes.bad1.accent: invalid color "nope": want #rrggbb, #rgb, rgb(r,g,b), a named color or reset`,
		`themes.inherit: the name is reserved for Herdr's theme`,
	}, "\n")
	if err.Error() != want {
		t.Errorf("Validate =\n%s\nwant\n%s", err, want)
	}
	if err := Validate(map[string]Custom{"good": customs["good"]}); err != nil {
		t.Errorf("Validate(good) = %v", err)
	}
	if err := Validate(nil); err != nil {
		t.Errorf("Validate(nil) = %v", err)
	}
}
