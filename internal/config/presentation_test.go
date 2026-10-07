package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/theme"
)

// deref reads an optional presentation field, "<nil>" when it is unset.
func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func loadDoc(t *testing.T, doc string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// TestPresentationDefaults_Table pins the single table of built-in row
// defaults: the name-first layout, the markers each kind of row shows and
// their order, and the source icons and icon colors.
func TestPresentationDefaults_Table(t *testing.T) {
	t.Parallel()
	got := DefaultPresentations("")
	name, parent := "{{ or .Label .Path | tilde | name }}", "{{ or .Label .Path | tilde | parent }}"
	want := map[string]RowPresentation{
		"herdr": {Icon: "\U000f0cc6 ", IconColor: "source.herdr", Label: name, Detail: parent,
			Marker: "{{ current }} {{ missing }} {{ status }} {{ pin }}"},
		"herdr.tab": {Icon: "◫", IconColor: "text.muted",
			Label:  "{{ muted .TabNumber }} {{ if ne .Label .TabNumber }}{{ .Label | tilde }}{{ end }}",
			Marker: "{{ current }}"},
		"herdr.pane": {IconColor: "text.muted", Label: "{{ status }} " + name, Detail: parent,
			Marker: "{{ current }} {{ if and .Agent (not (contains (lower .Agent) (lower .Label))) }}{{ .Agent }}{{ end }}"},
		"sessions": {IconColor: "source.sessions", Label: name, Detail: parent,
			Marker: `{{ if eq .Meta.running "true" }}running{{ else }}stopped{{ end }}{{ if eq .Meta.default "true" }} · default{{ end }} {{ missing }} {{ pin }}`},
		"workspaces": {Icon: "\ue615 ", IconColor: "source.workspaces", Label: name, Detail: parent,
			Marker: "{{ missing }} {{ pin }} {{ group }}"},
		"zoxide": {Icon: "\uf114 ", IconColor: "source.zoxide", Label: name, Detail: parent,
			Marker: "{{ missing }} {{ pin }}"},
		"projects": {Icon: "{{ if .IsWorktree }}\ue725 {{ else }}\ue702 {{ end }}", IconColor: "source.projects", Label: name, Detail: parent,
			Marker: "{{ if .IsWorktree }}{{ .Branch }}{{ end }} {{ missing }} {{ pin }}"},
		"agents": {IconColor: "source.agents", Label: "{{ status }} {{ or .Label .Path | tilde }}",
			Marker: "{{ .Workspace | trimIcon | name }}"},
		"other": {Icon: "{{ .Icon }}", IconColor: "source.custom", Label: "{{ .Path | tilde | name }}", Detail: "{{ .Path | tilde | parent }}",
			Marker: "{{ missing }} {{ pin }}"},
	}
	for key, have := range map[string]RowPresentation{
		"herdr": got.Herdr, "herdr.tab": got.HerdrTab, "herdr.pane": got.HerdrPane, "sessions": got.Sessions,
		"workspaces": got.Workspaces, "zoxide": got.Zoxide, "projects": got.Projects, "agents": got.Agents, "other": got.Other,
	} {
		if have != want[key] {
			t.Errorf("%s defaults =\n %+v\nwant\n %+v", key, have, want[key])
		}
	}
	if got.Custom != nil {
		t.Errorf("default presentations carry custom sources: %v", got.Custom)
	}
}

// TestPresentationDefaults_ASCIITier proves the defaults follow [tui].icons:
// the ASCII tier writes ASCII glyphs (the tab icon, the session separator)
// and has no default source icons, so its output stays 7-bit.
func TestPresentationDefaults_ASCIITier(t *testing.T) {
	t.Parallel()
	ascii := DefaultPresentations(TUIIconsASCII)
	if ascii.HerdrTab.Icon != "t" {
		t.Errorf("ascii tab icon = %q, want t", ascii.HerdrTab.Icon)
	}
	if !strings.Contains(ascii.Sessions.Marker, " - default") || strings.Contains(ascii.Sessions.Marker, "·") {
		t.Errorf("ascii sessions marker = %q, want the ASCII separator", ascii.Sessions.Marker)
	}
	for name, icon := range map[string]string{"herdr": ascii.Herdr.Icon, "workspaces": ascii.Workspaces.Icon, "zoxide": ascii.Zoxide.Icon, "projects": ascii.Projects.Icon} {
		if icon != "" {
			t.Errorf("ascii %s icon = %q, want none", name, icon)
		}
	}
	cfg, err := loadDoc(t, "[tui]\nicons = \"ascii\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := deref(cfg.Sources.Herdr.Tab.Icon); got != "t" {
		t.Errorf("loaded ascii tab icon = %q, want t", got)
	}
}

// TestLoad_PresentationExplicitValuesWin proves normalization fills unset
// fields only: an explicit value is kept, "" included, and the herdr tab and
// pane rows are configured in their own sub-tables.
func TestLoad_PresentationExplicitValuesWin(t *testing.T) {
	t.Parallel()
	cfg, err := loadDoc(t, `
[sources.herdr]
icon = ""
marker_format = "{{ status }}"

[sources.herdr.tab]
label_format = "{{ .TabLabel }}"
icon_color = "blue"

[sources.herdr.pane]
detail_format = ""

[sources.zoxide]
detail_format = ""
icon_color = "#89b4fa"
`)
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultPresentations("")
	p := cfg.Presentations()
	for _, tc := range []struct{ name, got, want string }{
		{"herdr.icon", p.Herdr.Icon, ""},
		{"herdr.marker", p.Herdr.Marker, "{{ status }}"},
		{"herdr.label", p.Herdr.Label, defaults.Herdr.Label},
		{"herdr.tab.label", p.HerdrTab.Label, "{{ .TabLabel }}"},
		{"herdr.tab.icon_color", p.HerdrTab.IconColor, "blue"},
		{"herdr.tab.icon", p.HerdrTab.Icon, defaults.HerdrTab.Icon},
		{"herdr.pane.detail", p.HerdrPane.Detail, ""},
		{"herdr.pane.label", p.HerdrPane.Label, defaults.HerdrPane.Label},
		{"zoxide.detail", p.Zoxide.Detail, ""},
		{"zoxide.icon_color", p.Zoxide.IconColor, "#89b4fa"},
		{"zoxide.label", p.Zoxide.Label, defaults.Zoxide.Label},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if cfg.Sources.Herdr.Icon == nil || *cfg.Sources.Herdr.Icon != "" {
		t.Errorf("explicit empty icon = %q, want \"\" kept", deref(cfg.Sources.Herdr.Icon))
	}
	if cfg.Sources.Projects.MarkerFormat == nil {
		t.Error("an unset field was not filled by normalization")
	}
}

// TestLoad_CustomSourcePresentation proves a custom source takes the custom
// defaults for every unset field and keeps its own values.
func TestLoad_CustomSourcePresentation(t *testing.T) {
	t.Parallel()
	cfg, err := loadDoc(t, `
[general]
source_order = ["prs"]

[[sources.custom]]
name = "prs"
command = ["gh"]
icon = "PR "
marker_format = "{{ .Meta.author }}"
`)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Presentations()
	want := RowPresentation{
		Icon: "PR ", IconColor: "source.custom",
		Label: "{{ or .Label .Path | tilde | name }}", Detail: "{{ or .Label .Path | tilde | parent }}",
		Marker: "{{ .Meta.author }}",
	}
	if got := p.Custom["prs"]; got != want {
		t.Errorf("prs presentation = %+v, want %+v", got, want)
	}
}

// TestLoad_RemovedLabelFormatKeysFail proves the herdr tab and pane formats
// moved to their sub-tables: the old keys are unknown fields.
func TestLoad_RemovedLabelFormatKeysFail(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"tab_label_format", "pane_label_format"} {
		if _, err := loadDoc(t, "[sources.herdr]\n"+key+" = \"{{ .Label }}\"\n"); err == nil {
			t.Errorf("Load accepted the removed key sources.herdr.%s", key)
		}
	}
}

// TestLoad_RejectsInvalidPresentation proves every presentation field is
// validated with its field path: each template against the kinds of rows it
// draws, the icon color as a color reference.
func TestLoad_RejectsInvalidPresentation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"icon template", "[sources.zoxide]\nicon = \"{{ .Nope }}\"\n", "sources.zoxide.icon: "},
		{"detail template", "[sources.herdr]\ndetail_format = \"{{ if }}\"\n", "sources.herdr.detail_format: "},
		{"marker template", "[sources.projects]\nmarker_format = \"{{ pin 1 }}\"\n", "sources.projects.marker_format: "},
		{"tab marker", "[sources.herdr.tab]\nmarker_format = \"{{ .Unknown }}\"\n", "sources.herdr.tab.marker_format: "},
		{"pane icon", "[sources.herdr.pane]\nicon = \"{{ nope }}\"\n", "sources.herdr.pane.icon: "},
		{"agents label kind", "[sources.agents]\nlabel_format = \"{{ slice .Branch 0 3 }}\"\n", "sources.agents.label_format: "},
		{"sessions detail", "[sources.sessions]\ndetail_format = \"{{ .Nope }}\"\n", "sources.sessions.detail_format: "},
		{"workspaces marker", "[sources.workspaces]\nmarker_format = \"{{ .Nope }}\"\n", "sources.workspaces.marker_format: "},
		{"icon color", "[sources.zoxide]\nicon_color = \"not-a-color\"\n", `sources.zoxide.icon_color: unknown token or role "not-a-color"`},
		{"tab icon color", "[sources.herdr.tab]\nicon_color = \"#12\"\n", "sources.herdr.tab.icon_color: invalid color"},
		{"custom marker", "[[sources.custom]]\nname = \"prs\"\ncommand = [\"gh\"]\nmarker_format = \"{{ .Nope }}\"\n", "sources.custom[0].marker_format: "},
		{"custom icon color", "[[sources.custom]]\nname = \"prs\"\ncommand = [\"gh\"]\nicon_color = \"nope\"\n", "sources.custom[0].icon_color: "},
		{"wildcard label", "[[wildcards]]\npattern = \"**\"\nlabel_format = \"{{ .Nope }}\"\n", "wildcards[0].label_format: "},
		// A wildcard may apply to any directory-like row, so a template
		// that fails for one of those kinds is rejected.
		{"wildcard icon for every kind", "[[wildcards]]\npattern = \"**\"\nicon = \"{{ slice .Branch 0 3 }}\"\n", "wildcards[0].icon: "},
		{"workspace marker", "[[workspaces]]\nname = \"api\"\npath = \"/api\"\nmarker_format = \"{{ if }}\"\n", "workspaces[0].marker_format: "},
		{"workspace icon color", "[[workspaces]]\nname = \"api\"\npath = \"/api\"\nicon_color = \"nope\"\n", "workspaces[0].icon_color: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadDoc(t, tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestLoad_PresentationAcceptsStyleAndLiveFunctions proves row templates may
// call the style and live functions, and icon_color takes tokens, roles and
// literals.
func TestLoad_PresentationAcceptsStyleAndLiveFunctions(t *testing.T) {
	t.Parallel()
	_, err := loadDoc(t, `
[sources.zoxide]
icon = "{{ accent \"Z\" }} "
icon_color = "blue"
label_format = "{{ bold (.Label | tilde | name) }}"
detail_format = "{{ muted (.Label | tilde | parent) }}"
marker_format = "{{ current }} {{ status }} {{ pin }} {{ group }} {{ missing }}"

[sources.herdr]
icon_color = "row.marker"

[sources.projects]
icon_color = "rgb(250, 179, 135)"
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// TestLoad_PlainTemplatesRejectRowFunctions proves templates that render
// plain text (workspace names, preview command arguments) reject the style
// and live functions of row templates, by name.
func TestLoad_PlainTemplatesRejectRowFunctions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ doc, want string }{
		{"[general]\nworkspace_name = \"{{ muted .Label }}\"\n", "general.workspace_name: muted is a row presentation function"},
		{"[[wildcards]]\npattern = \"*\"\nworkspace_name = \"{{ .Label }}{{ pin }}\"\n", "wildcards[0].workspace_name: pin is a row presentation function"},
		{"[preview.commands.x]\ncommand = \"echo {{status}}\"\n", "preview.commands.x.command: status is a row presentation function"},
		{"[[sources.custom]]\nname = \"prs\"\ncommand = [\"gh\"]\npreview = [\"x\"]\n[sources.custom.preview_commands.x]\ncommand = [\"echo\", \"{{ bold .Label }}\"]\n", "preview_commands.x.command[1]: bold is a row presentation function"},
	} {
		if _, err := loadDoc(t, tc.doc); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Load error = %v, want it to contain %q", err, tc.want)
		}
	}
}

// TestLoad_CustomThemes proves [themes.<name>] tables load with tokens, a
// base and roles written either as unquoted dotted keys (which TOML reads as
// nested tables) or as quoted keys, and that [tui].theme may select them.
func TestLoad_CustomThemes(t *testing.T) {
	t.Parallel()
	cfg, err := loadDoc(t, `
[tui]
theme = "mine"

[themes.mine]
base = "nord"
accent = "#ff79c6"

[themes.mine.roles]
row.detail = "subtext0"
"row.marker" = "red"
status.working = "accent"

[themes.second]
base = "mine"
roles = { pin = "#ffffff" }
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	customs, err := cfg.CustomThemes()
	if err != nil {
		t.Fatalf("CustomThemes: %v", err)
	}
	want := map[string]theme.Custom{
		"mine": {
			Base:   "nord",
			Tokens: map[string]string{"accent": "#ff79c6"},
			Roles:  map[string]string{"row.detail": "subtext0", "row.marker": "red", "status.working": "accent"},
		},
		"second": {Base: "mine", Roles: map[string]string{"pin": "#ffffff"}},
	}
	if !reflect.DeepEqual(customs, want) {
		t.Fatalf("CustomThemes = %#v, want %#v", customs, want)
	}
	th, err := theme.Build("mine", customs, theme.HerdrTheme{}, true)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := th.Role(theme.RoleRowDetail); got != th.Palette().Get(theme.TokenSubtext0) {
		t.Errorf("row.detail = %v, want the subtext0 token", got)
	}
}

// TestLoad_RejectsInvalidThemes proves malformed theme tables fail with the
// field path of the offending key.
func TestLoad_RejectsInvalidThemes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, doc, want string }{
		{"unknown token", "[themes.mine]\nnope = \"#fff\"\n", `themes.mine.nope: unknown token "nope"`},
		{"unknown role", "[themes.mine.roles]\nrow.nope = \"red\"\n", `themes.mine.roles.row.nope: unknown role "row.nope"`},
		{"bad color", "[themes.mine]\naccent = \"#12\"\n", "themes.mine.accent: invalid color"},
		{"bad role reference", "[themes.mine.roles]\npin = \"nope\"\n", "themes.mine.roles.pin: unknown token or role"},
		{"non-string token", "[themes.mine]\naccent = 3\n", "themes.mine.accent: must be a color string"},
		{"non-string base", "[themes.mine]\nbase = 3\n", "themes.mine.base: must be a theme name string"},
		{"roles not a table", "[themes.mine]\nroles = \"red\"\n", "themes.mine.roles: must be a table"},
		{"non-string role", "[themes.mine.roles]\npin = 1\n", "themes.mine.roles.pin: must be a token, role or color string"},
		{"unknown base", "[themes.mine]\nbase = \"nope\"\n", `themes.mine.base: unknown theme "nope"`},
		{"base cycle", "[themes.a]\nbase = \"b\"\n[themes.b]\nbase = \"a\"\n", "cycle"},
		{"reserved name", "[themes.nord]\naccent = \"#fff\"\n", "themes.nord: the name is reserved"},
		{"role cycle", "[themes.mine.roles]\npin = \"warning\"\nwarning = \"pin\"\n", "themes.mine.roles: cycle"},
		{"unknown selected theme", "[tui]\ntheme = \"mine\"\n", `tui.theme: unknown theme "mine"`},
		{"padded selected theme", "[tui]\ntheme = \" nord\"\n", "tui.theme: \" nord\" must not have surrounding whitespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := loadDoc(t, tc.doc); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestLoad_OverridePresentations proves [[workspaces]] and [[wildcards]]
// entries carry the presentation keys, an unset key staying distinct from an
// explicit empty one.
func TestLoad_OverridePresentations(t *testing.T) {
	t.Parallel()
	cfg, err := loadDoc(t, `
[[workspaces]]
name = "api"
path = "/srv/api"
icon = "A "
icon_color = "peach"
marker_format = ""

[[wildcards]]
pattern = "~/work/**"
label_format = "{{ .Label | name }}"
detail_format = "{{ muted .Branch }}"
`)
	if err != nil {
		t.Fatal(err)
	}
	ws, w := cfg.Workspaces[0].Presentation, cfg.Wildcards[0].Presentation
	if got := []string{deref(ws.Icon), deref(ws.IconColor), deref(ws.LabelFormat), deref(ws.DetailFormat), deref(ws.MarkerFormat)}; !reflect.DeepEqual(got, []string{"A ", "peach", "<nil>", "<nil>", ""}) {
		t.Errorf("workspace presentation = %q", got)
	}
	if got := []string{deref(w.Icon), deref(w.LabelFormat), deref(w.DetailFormat)}; !reflect.DeepEqual(got, []string{"<nil>", "{{ .Label | name }}", "{{ muted .Branch }}"}) {
		t.Errorf("wildcard presentation = %q", got)
	}
}
