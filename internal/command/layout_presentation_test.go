package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/theme"
)

// TestLayoutFromConfig_ThreadsPresentations proves the picker receives every
// resolved row presentation from the loaded config: the built-in sources,
// the herdr tab and pane sub-tables and each custom source by name.
func TestLayoutFromConfig_ThreadsPresentations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := `version = 3
[general]
source_order = ["herdr", "prs"]

[sources.herdr]
label_format = "workspace={{.Path}}"

[sources.herdr.tab]
label_format = "tab={{.Label}}"

[sources.herdr.pane]
marker_format = "{{ current }}"

[sources.agents]
icon = "A "

[[sources.custom]]
name = "prs"
command = ["gh"]
label_format = "PR {{.Label}}"
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	layout := layoutFromConfig(cfg, cfg.General.SourceOrder)
	p := layout.Presentation
	if p == nil {
		t.Fatal("layout carries no presentation")
	}
	defaults := config.DefaultPresentations("")
	for _, tc := range []struct{ name, got, want string }{
		{"herdr label", p.Herdr.Label, "workspace={{.Path}}"},
		{"herdr detail", p.Herdr.Detail, defaults.Herdr.Detail},
		{"tab label", p.HerdrTab.Label, "tab={{.Label}}"},
		{"pane marker", p.HerdrPane.Marker, "{{ current }}"},
		{"agents icon", p.Agents.Icon, "A "},
		{"custom label", p.Custom["prs"].Label, "PR {{.Label}}"},
		{"custom icon", p.Custom["prs"].Icon, "{{ .Icon }}"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestSelectedTheme_InheritsHerdrAndHonorsOverrides proves the command layer
// selects the theme once with theme.Select: by default it inherits Herdr's
// own theme from Herdr's config.toml, a custom [themes.<name>] selected by
// [tui].theme applies its role overrides, NO_COLOR forces the no-color theme,
// and only an auto-switching Herdr theme comes with a light variant.
func TestSelectedTheme_InheritsHerdrAndHonorsOverrides(t *testing.T) {
	herdrDir := t.TempDir()
	herdrConfig := filepath.Join(herdrDir, "config.toml")
	if err := os.WriteFile(herdrConfig, []byte("[theme]\nname = \"nord\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newApp := func(vals map[string]string) *App {
		a := New(WithStreams(&bytes.Buffer{}, &bytes.Buffer{}))
		a.themeGetenv = func(k string) string { return vals[k] }
		return a
	}

	got, light := newApp(map[string]string{"HERDR_CONFIG_PATH": herdrConfig}).selectedTheme(config.Defaults())
	if got.Source.Kind != theme.SourceInherit || got.Source.Herdr != "nord" || light != nil {
		t.Errorf("default theme = %s (light variant %v), want inherit:nord without one", got.Source, light != nil)
	}

	cfg := config.Defaults()
	cfg.TUI.Theme = "mine"
	cfg.Themes = map[string]config.ThemeTable{"mine": {"base": "inherit", "roles": map[string]any{"row": map[string]any{"detail": "#ff0000"}}}}
	a := newApp(map[string]string{"HERDR_CONFIG_PATH": herdrConfig})
	mine, _ := a.selectedTheme(cfg)
	if mine.Name != "mine" || mine.Role(theme.RoleRowDetail) != theme.RGB(0xff, 0, 0) || mine.Source.Herdr != "nord" {
		t.Errorf("custom theme = %q (%s), row.detail %v; want mine on inherit:nord with #ff0000", mine.Name, mine.Source, mine.Role(theme.RoleRowDetail))
	}
	if again, _ := a.selectedTheme(config.Defaults()); again.Name != "mine" {
		t.Errorf("second selection = %q, want the theme selected once per process", again.Name)
	}
	if layout := a.pickerLayoutForConfig(cfg, nil, nil); layout.Theme.Name != "mine" {
		t.Errorf("picker layout theme = %q, want the selected theme", layout.Theme.Name)
	}

	plain, _ := newApp(map[string]string{"NO_COLOR": "1", "HERDR_CONFIG_PATH": herdrConfig}).selectedTheme(cfg)
	if !plain.NoColor || plain.Source.Setting != "NO_COLOR" {
		t.Errorf("NO_COLOR theme = %q (no color %v), want the no-color theme", plain.Name, plain.NoColor)
	}

	auto := filepath.Join(herdrDir, "auto.toml")
	if err := os.WriteFile(auto, []byte("[theme]\nname = \"catppuccin\"\nauto_switch = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a = newApp(map[string]string{"HERDR_CONFIG_PATH": auto})
	dark, light := a.selectedTheme(config.Defaults())
	if dark.Source.Herdr != "catppuccin" || light == nil || light.Source.Herdr != "catppuccin-latte" {
		t.Fatalf("auto-switch themes = %s / %v, want inherit:catppuccin with a catppuccin-latte variant", dark.Source, light)
	}
	if layout := a.pickerLayoutForConfig(config.Defaults(), nil, nil); layout.LightTheme == nil || layout.LightTheme.Source.Herdr != "catppuccin-latte" {
		t.Errorf("picker layout light theme = %v, want the light variant", layout.LightTheme)
	}
}

// TestSelectedTheme_ReportsAnInvalidTheme proves a selection error (a
// hand-built config naming an unknown theme) is reported on stderr and the
// picker keeps its default theme instead of failing to start.
func TestSelectedTheme_ReportsAnInvalidTheme(t *testing.T) {
	var stderr bytes.Buffer
	a := New(WithStreams(&bytes.Buffer{}, &stderr))
	a.themeGetenv = func(string) string { return "" }
	cfg := config.Defaults()
	cfg.TUI.Theme = "nope"
	if got, _ := a.selectedTheme(cfg); got.Name != "" {
		t.Errorf("theme = %q, want the zero theme (the picker's default)", got.Name)
	}
	if !strings.Contains(stderr.String(), `unknown theme "nope"`) {
		t.Errorf("stderr = %q, want the selection error", stderr.String())
	}
}

// strPtr returns a pointer to s, for the optional presentation fields.
func strPtr(s string) *string { return &s }
