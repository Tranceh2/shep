package theme

import (
	"strings"
	"testing"
)

// roleDefaultTable is the documented role → token table (package doc), kept
// independent of defaultRoleTokens so a change to either is caught.
var roleDefaultTable = map[string]string{
	"text":              "text",
	"text.secondary":    "subtext0",
	"text.muted":        "overlay0",
	"accent":            "accent",
	"rule":              "surface1",
	"selection":         "selection_bg",
	"tab.active":        "selection_bg",
	"tab.active.fg":     "accent",
	"prompt":            "accent",
	"cursor":            "accent",
	"match":             "accent",
	"heading":           "accent",
	"row.label":         "text",
	"row.detail":        "overlay0",
	"row.marker":        "overlay0",
	"row.descendant":    "overlay0",
	"status.working":    "yellow",
	"status.blocked":    "red",
	"status.done":       "teal",
	"status.idle":       "green",
	"status.unknown":    "overlay0",
	"source.herdr":      "green",
	"source.workspaces": "mauve",
	"source.zoxide":     "blue",
	"source.projects":   "peach",
	"source.sessions":   "yellow",
	"source.agents":     "accent",
	"source.custom":     "teal",
	"pin":               "yellow",
	"git.branch":        "mauve",
	"git.clean":         "green",
	"git.changes":       "yellow",
	"error":             "red",
	"warning":           "yellow",
	"success":           "green",
}

func TestRoles_NamesRoundTripAndMatchTable(t *testing.T) {
	roles := Roles()
	if len(roles) != len(roleDefaultTable) {
		t.Fatalf("len(Roles()) = %d, want %d", len(roles), len(roleDefaultTable))
	}
	for _, r := range roles {
		got, ok := ParseRole(r.String())
		if !ok || got != r {
			t.Errorf("ParseRole(%q) = %v, %v; want %v", r, got, ok, r)
		}
		want, ok := roleDefaultTable[r.String()]
		if !ok {
			t.Errorf("role %q is not in the documented table", r)
			continue
		}
		if DefaultToken(r).String() != want {
			t.Errorf("DefaultToken(%s) = %s, want %s", r, DefaultToken(r), want)
		}
	}
	for _, bad := range []string{"", "row", "Row.Label", "source.path", "status"} {
		if _, ok := ParseRole(bad); ok {
			t.Errorf("ParseRole(%q) succeeded", bad)
		}
	}
	if got := Role(200).String(); got != "Role(200)" {
		t.Errorf("Role(200).String() = %q", got)
	}
}

func TestRoleDefaults_ResolveOnEveryBuiltin(t *testing.T) {
	for _, name := range BuiltinNames() {
		th, err := Build(name, nil, HerdrTheme{}, true)
		if err != nil {
			t.Fatalf("Build(%q): %v", name, err)
		}
		if name == NamePlain {
			for _, r := range Roles() {
				if !th.Role(r).IsReset() {
					t.Errorf("plain role %s = %v, want reset", r, th.Role(r))
				}
			}
			continue
		}
		p := builtinPalettes[name]
		if th.Palette() != p {
			t.Errorf("%s: Palette() differs from the built-in data", name)
		}
		for _, r := range Roles() {
			tok, _ := ParseToken(roleDefaultTable[r.String()])
			if got, want := th.Role(r), p.Get(tok); got != want {
				t.Errorf("%s role %s = %v, want %s = %v", name, r, got, tok, want)
			}
		}
	}
}

func TestThemeResolve_References(t *testing.T) {
	th, err := Build("catppuccin", map[string]Custom{}, HerdrTheme{}, true)
	if err != nil {
		t.Fatal(err)
	}
	p := builtinPalettes["catppuccin"]
	tests := []struct {
		ref  string
		want Color
	}{
		{"blue", p.Get(TokenBlue)},
		{"Blue", p.Get(TokenBlue)},
		{" panel_bg ", p.Get(TokenPanelBg)},
		{"text", p.Get(TokenText)},
		{"accent", p.Get(TokenAccent)},
		{"source.zoxide", p.Get(TokenBlue)},
		{"row.detail", p.Get(TokenOverlay0)},
		{"status.working", p.Get(TokenYellow)},
		{"#89b4fa", RGB(0x89, 0xb4, 0xfa)},
		{"rgb(1,2,3)", RGB(1, 2, 3)},
		{"magenta", ansi(5)},
		{"reset", Color{}},
	}
	for _, tt := range tests {
		got, err := th.Resolve(tt.ref)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tt.ref, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Resolve(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
	for ref, want := range map[string]string{
		"x":            `unknown token or role "x" (want a palette token such as "blue", a role such as "source.zoxide", or a color such as "#89b4fa")`,
		"source.path":  `unknown token or role "source.path"`,
		"#12":          `invalid color "#12": want #rrggbb, #rgb, rgb(r,g,b), a named color or reset`,
		"rgb(1,2)":     `invalid color "rgb(1,2)"`,
		"":             `unknown token or role ""`,
		"surface2":     `unknown token or role "surface2"`,
		"RGB(300,0,0)": `invalid color "RGB(300,0,0)"`,
	} {
		_, err := th.Resolve(ref)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("Resolve(%q) error = %v, want prefix %q", ref, err, want)
		}
		if verr := ValidateRef(ref); verr == nil || verr.Error() != err.Error() {
			t.Errorf("ValidateRef(%q) = %v, want %v", ref, verr, err)
		}
	}
	for _, ref := range []string{"blue", "row.label", "#fff", "lightcyan"} {
		if err := ValidateRef(ref); err != nil {
			t.Errorf("ValidateRef(%q): %v", ref, err)
		}
	}

	plain, err := Build("plain", nil, HerdrTheme{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := plain.Resolve("#ff0000"); err != nil || !c.IsReset() {
		t.Errorf("plain Resolve(#ff0000) = %v, %v; want reset", c, err)
	}
	if _, err := plain.Resolve("x"); err == nil {
		t.Errorf("plain Resolve(x) succeeded; invalid references are errors without color too")
	}
	var zero Theme
	if !zero.Role(RoleAccent).IsReset() || !th.Role(Role(200)).IsReset() {
		t.Errorf("zero theme or out-of-range role is not reset")
	}
}

func TestBuild_RoleOverridesAndCycles(t *testing.T) {
	customs := map[string]Custom{
		"mine": {
			Base:   "nord",
			Tokens: map[string]string{"teal": "#010203"},
			Roles: map[string]string{
				"row.detail":     "row.marker",
				"row.marker":     "teal",
				"source.zoxide":  "#040506",
				"source.custom":  "status.done",
				"status.done":    "lightred",
				"tab.active.fg":  "text.secondary",
				"text.secondary": "Mauve",
			},
		},
	}
	th, err := Build("mine", customs, HerdrTheme{}, true)
	if err != nil {
		t.Fatal(err)
	}
	nord := builtinPalettes["nord"]
	for role, want := range map[Role]Color{
		RoleRowDetail:     RGB(1, 2, 3), // role -> role -> overridden token
		RoleRowMarker:     RGB(1, 2, 3),
		RoleSourceZoxide:  RGB(4, 5, 6),
		RoleSourceCustom:  ansi(9),
		RoleStatusDone:    ansi(9),
		RoleTabActiveFg:   nord.Get(TokenMauve),
		RoleTextSecondary: nord.Get(TokenMauve),
		RoleText:          nord.Get(TokenText),
	} {
		if got := th.Role(role); got != want {
			t.Errorf("role %s = %v, want %v", role, got, want)
		}
	}

	for _, tt := range []struct {
		roles map[string]string
		want  string
	}{
		{map[string]string{"row.detail": "row.marker", "row.marker": "row.detail"},
			"themes.loop.roles: cycle row.detail -> row.marker -> row.detail"},
		{map[string]string{"pin": "pin"}, "themes.loop.roles: cycle pin -> pin"},
		{map[string]string{"error": "warning", "warning": "success", "success": "warning"},
			"themes.loop.roles: cycle warning -> success -> warning"},
	} {
		_, err := Build("loop", map[string]Custom{"loop": {Roles: tt.roles}}, HerdrTheme{}, true)
		if err == nil || err.Error() != tt.want {
			t.Errorf("roles %v: error = %v, want %q", tt.roles, err, tt.want)
		}
	}
}
