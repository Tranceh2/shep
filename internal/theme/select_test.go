package theme

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSelect_Precedence(t *testing.T) {
	nordHerdr := "[theme]\nname = \"nord\"\n"
	autoHerdr := "[theme]\nname = \"tokyo-night\"\nauto_switch = true\n"
	customs := map[string]Custom{
		"mine":     {Base: "dracula", Tokens: map[string]string{"accent": "#010203"}},
		"onherdr":  {Base: "inherit", Tokens: map[string]string{"accent": "#040506"}},
		"broken":   {Tokens: map[string]string{"accent": "#12"}},
		"loopback": {Base: "loopback"},
		"badherdr": {Base: "inherit", Tokens: map[string]string{"accent": "#12"}},
	}
	const missing = "\x00missing" // HerdrConfigPath placeholder: a file that does not exist
	tests := []struct {
		name      string
		env       map[string]string
		config    string
		herdr     string // Herdr config content; "" with herdrPath unset means none
		herdrPath string // "" writes herdr (if any) to a temp file; missing = absent file; "-" = no path
		dark      bool
		wantErr   string
		wantName  string
		noColor   bool
		kind      SourceKind
		setting   string
		herdrName string
		accent    Color
		notes     []string // substrings the notes must contain
		follows   bool     // Source.FollowsAppearance
	}{
		{
			name: "NO_COLOR beats everything", env: map[string]string{"NO_COLOR": "1", "SHEP_THEME": "dracula"},
			config: "nord", herdr: autoHerdr,
			wantName: "plain", noColor: true, kind: SourceNoColor, setting: "NO_COLOR",
		},
		{
			name: "empty NO_COLOR is ignored", env: map[string]string{"NO_COLOR": ""},
			config: "nord", wantName: "nord", kind: SourceBuiltin, setting: "tui.theme",
			accent: builtinPalettes["nord"].Get(TokenAccent),
		},
		{
			name: "SHEP_THEME built-in beats config", env: map[string]string{"SHEP_THEME": "Dracula"},
			config: "nord", wantName: "dracula", kind: SourceBuiltin, setting: "SHEP_THEME",
			accent: builtinPalettes["dracula"].Get(TokenAccent),
		},
		{
			name: "SHEP_THEME custom", env: map[string]string{"SHEP_THEME": "mine"},
			config: "nord", wantName: "mine", kind: SourceCustom, setting: "SHEP_THEME", accent: RGB(1, 2, 3),
		},
		{
			name: "SHEP_THEME inherit", env: map[string]string{"SHEP_THEME": "inherit"},
			config: "dracula", herdr: nordHerdr,
			wantName: "inherit", kind: SourceInherit, setting: "SHEP_THEME", herdrName: "nord",
			accent: builtinPalettes["nord"].Get(TokenAccent),
		},
		{
			name: "unknown SHEP_THEME is ignored with a note", env: map[string]string{"SHEP_THEME": "bogus"},
			config: "nord", wantName: "nord", kind: SourceBuiltin, setting: "tui.theme",
			accent: builtinPalettes["nord"].Get(TokenAccent),
			notes:  []string{`SHEP_THEME="bogus" ignored: unknown theme "bogus"`},
		},
		{
			name: "invalid custom SHEP_THEME is ignored with a note", env: map[string]string{"SHEP_THEME": "broken"},
			config: "", herdr: nordHerdr,
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "nord",
			accent: builtinPalettes["nord"].Get(TokenAccent),
			notes:  []string{`SHEP_THEME="broken" ignored: themes.broken.accent: invalid color "#12"`},
		},
		{
			name: "SHEP_THEME base cycle is ignored", env: map[string]string{"SHEP_THEME": "loopback"},
			config: "nord", wantName: "nord", kind: SourceBuiltin, setting: "tui.theme",
			accent: builtinPalettes["nord"].Get(TokenAccent),
			notes:  []string{`SHEP_THEME="loopback" ignored: themes.loopback.base: cycle loopback -> loopback`},
		},
		{
			name: "default is inherit", herdr: nordHerdr,
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "nord",
			accent: builtinPalettes["nord"].Get(TokenAccent),
		},
		{
			name: "explicit inherit", config: "inherit", herdr: "[theme]\nname = \"vesper\"\n[theme.custom]\naccent = \"#070809\"\n",
			wantName: "inherit", kind: SourceInherit, setting: "tui.theme", herdrName: "vesper", accent: RGB(7, 8, 9),
		},
		{
			name: "config built-in alias", config: "Tokyo Night", herdr: autoHerdr,
			wantName: "tokyo-night", kind: SourceBuiltin, setting: "tui.theme",
			accent: builtinPalettes["tokyo-night"].Get(TokenAccent),
		},
		{
			name: "config plain", config: "plain",
			wantName: "plain", noColor: true, kind: SourceBuiltin, setting: "tui.theme",
		},
		{
			name: "config custom", config: "mine", herdr: autoHerdr,
			wantName: "mine", kind: SourceCustom, setting: "tui.theme", accent: RGB(1, 2, 3),
		},
		{
			name: "config custom on inherit", config: "onherdr", herdr: nordHerdr,
			wantName: "onherdr", kind: SourceCustom, setting: "tui.theme", herdrName: "nord", accent: RGB(4, 5, 6),
		},
		{name: "config unknown is an error", config: "nope", wantErr: `tui.theme: unknown theme "nope"`},
		{name: "config invalid custom is an error", config: "broken", wantErr: `tui.theme: themes.broken.accent: invalid color "#12"`},
		{
			name: "missing Herdr config", herdrPath: missing,
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "catppuccin",
			accent: builtinPalettes["catppuccin"].Get(TokenAccent),
			notes:  []string{"not found; using Herdr's default theme catppuccin"},
		},
		{
			name: "unreadable Herdr config", herdr: "[theme\n",
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "catppuccin",
			accent: builtinPalettes["catppuccin"].Get(TokenAccent),
			notes:  []string{"parse herdr config", "using Herdr's default theme catppuccin"},
		},
		{
			name: "no Herdr config path", herdrPath: "-",
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "catppuccin",
			accent: builtinPalettes["catppuccin"].Get(TokenAccent),
			notes:  []string{"no Herdr configuration path"},
		},
		{
			name: "Herdr diagnostics become notes", herdr: "[theme]\nname = \"catppucin\"\n",
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "catppuccin",
			accent: builtinPalettes["catppuccin"].Get(TokenAccent),
			notes:  []string{`unknown theme name theme.name = "catppucin"`},
		},
		{
			name: "auto_switch light variant", herdr: autoHerdr, dark: false,
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "tokyo-night-day",
			accent: builtinPalettes["tokyo-night-day"].Get(TokenAccent), follows: true,
		},
		{
			name: "auto_switch dark variant", herdr: autoHerdr, dark: true,
			wantName: "inherit", kind: SourceInherit, setting: "default", herdrName: "tokyo-night",
			accent: builtinPalettes["tokyo-night"].Get(TokenAccent), follows: true,
		},
		{
			name: "auto_switch variant after an ignored SHEP_THEME", env: map[string]string{"SHEP_THEME": "badherdr"},
			config: "onherdr", herdr: autoHerdr, dark: false,
			wantName: "onherdr", kind: SourceCustom, setting: "tui.theme", herdrName: "tokyo-night-day",
			accent: RGB(4, 5, 6), follows: true,
			notes: []string{`SHEP_THEME="badherdr" ignored: themes.badherdr.accent`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.herdrPath
			switch path {
			case "":
				if tt.herdr != "" {
					path = writeFile(t, tt.herdr)
				} else {
					path = filepath.Join(t.TempDir(), "absent.toml")
				}
			case missing:
				path = filepath.Join(t.TempDir(), "missing.toml")
			case "-":
				path = ""
			}
			got, err := Select(Options{
				ConfigTheme:     tt.config,
				Customs:         customs,
				Getenv:          func(k string) string { return tt.env[k] },
				HerdrConfigPath: path,
				Light:           !tt.dark,
			})
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want prefix %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.wantName || got.NoColor != tt.noColor {
				t.Errorf("theme = %q (NoColor %v), want %q (NoColor %v)", got.Name, got.NoColor, tt.wantName, tt.noColor)
			}
			src := got.Source
			if src.Kind != tt.kind || src.Setting != tt.setting || src.Herdr != tt.herdrName {
				t.Errorf("source = %+v, want kind %q setting %q herdr %q", src, tt.kind, tt.setting, tt.herdrName)
			}
			if tt.herdrName != "" && src.HerdrPath != path {
				t.Errorf("HerdrPath = %q, want %q", src.HerdrPath, path)
			}
			if tt.herdrName == "" && src.HerdrPath != "" {
				t.Errorf("HerdrPath = %q for a theme that does not inherit", src.HerdrPath)
			}
			if got.Source.FollowsAppearance != tt.follows {
				t.Errorf("FollowsAppearance = %v, want %v", got.Source.FollowsAppearance, tt.follows)
			}
			if c := got.Role(RoleAccent); c != tt.accent {
				t.Errorf("accent = %v, want %v", c, tt.accent)
			}
			joined := strings.Join(src.Notes, "\n")
			for _, want := range tt.notes {
				if !strings.Contains(joined, want) {
					t.Errorf("notes = %q, want one containing %q", src.Notes, want)
				}
			}
			if len(tt.notes) == 0 && len(src.Notes) != 0 {
				t.Errorf("notes = %q, want none", src.Notes)
			}
		})
	}
}

func TestSource_String(t *testing.T) {
	for src, want := range map[*Source]string{
		{Kind: SourceInherit, Herdr: "catppuccin", Setting: "default"}: "inherit:catppuccin (default)",
		{Kind: SourceBuiltin, Setting: "SHEP_THEME"}:                   "builtin (SHEP_THEME)",
		{Kind: SourceCustom, Herdr: "nord", Setting: "tui.theme"}:      "custom on inherit:nord (tui.theme)",
		{Kind: SourceNoColor, Setting: "NO_COLOR"}:                     "no-color (NO_COLOR)",
		{Kind: SourceBuiltin}:                                          "builtin",
	} {
		if got := src.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

func TestSelect_NilGetenvUsesProcessEnvironment(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("SHEP_THEME", "vesper")
	got, err := Select(Options{ConfigTheme: "nord"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "vesper" || got.Source.Setting != "SHEP_THEME" {
		t.Errorf("Select with os.Getenv = %q via %q", got.Name, got.Source.Setting)
	}
}
