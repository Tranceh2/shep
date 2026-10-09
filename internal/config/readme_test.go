package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/tranceh2/shep/internal/theme"
	"github.com/tranceh2/shep/internal/tmpl"
)

// readme returns the repository README.
func readme(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	return string(data)
}

// readmeBlock returns the fenced TOML block that follows marker (an HTML
// comment) in the README.
func readmeBlock(t *testing.T, doc, marker string) string {
	t.Helper()
	i := strings.Index(doc, marker+"\n```toml\n")
	if i < 0 {
		t.Fatalf("README.md has no ```toml block after %s", marker)
	}
	body := doc[i+len(marker)+len("\n```toml\n"):]
	end := strings.Index(body, "\n```\n")
	if end < 0 {
		t.Fatalf("README.md block after %s is not closed", marker)
	}
	return body[:end+1]
}

// loadREADMEBlock loads a README block as a configuration file.
func loadREADMEBlock(t *testing.T, block string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(versioned(block)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("README block does not load: %v\n%s", err, block)
	}
	return cfg
}

// TestREADME_RowDefaultsAreTheBuiltInDefaults proves the README's table of
// built-in row defaults states every part of every kind of row exactly as
// presentationDefaults defines it (the unicode tier), and loads as a valid
// configuration.
func TestREADME_RowDefaultsAreTheBuiltInDefaults(t *testing.T) {
	t.Parallel()
	doc := readme(t)
	block := readmeBlock(t, doc, "<!-- row-defaults -->")
	loadREADMEBlock(t, block)
	var parsed map[string]any
	if err := toml.Unmarshal([]byte(block), &parsed); err != nil {
		t.Fatal(err)
	}
	sources, _ := parsed["sources"].(map[string]any)
	table := func(path ...string) map[string]any {
		var cur any = sources
		for _, key := range path {
			m, _ := cur.(map[string]any)
			cur = m[key]
		}
		if list, ok := cur.([]any); ok && len(list) == 1 {
			cur = list[0]
		}
		m, _ := cur.(map[string]any)
		return m
	}
	for _, tc := range []struct {
		name string
		path []string
		kind rowKind
	}{
		{"sources.herdr", []string{"herdr"}, rowHerdr},
		{"sources.herdr.tab", []string{"herdr", "tab"}, rowHerdrTab},
		{"sources.herdr.pane", []string{"herdr", "pane"}, rowHerdrPane},
		{"sources.sessions", []string{"sessions"}, rowSessions},
		{"sources.workspaces", []string{"workspaces"}, rowWorkspaces},
		{"sources.zoxide", []string{"zoxide"}, rowZoxide},
		{"sources.projects", []string{"projects"}, rowProjects},
		{"sources.agents", []string{"agents"}, rowAgents},
		{"sources.custom", []string{"custom"}, rowCustom},
	} {
		got := map[string]any{}
		for _, key := range []string{"icon", "icon_color", "label_format", "detail_format", "marker_format"} {
			if v, ok := table(tc.path...)[key]; ok {
				got[key] = v
			}
		}
		def := presentationDefaults(tc.kind, TUIIconsUnicode)
		want := map[string]any{
			"icon": def.Icon, "icon_color": def.IconColor, "label_format": def.Label,
			"detail_format": def.Detail, "marker_format": def.Marker,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("README [%s] defaults = %v\nwant %v", tc.name, got, want)
		}
	}
	other := presentationDefaults(rowOther, TUIIconsUnicode)
	for _, format := range []string{other.Label, other.Detail} {
		if !strings.Contains(doc, "'"+format+"'") {
			t.Errorf("README does not state the --path row default %q", format)
		}
	}
}

// TestREADME_ThemeTablesListEveryTokenRoleAndTheme proves the README's theme
// reference lists all 19 palette tokens, every role with its default token
// and every built-in theme name.
func TestREADME_ThemeTablesListEveryTokenRoleAndTheme(t *testing.T) {
	t.Parallel()
	doc := readme(t)
	for _, token := range theme.Tokens() {
		if row := "\n| `" + token.String() + "` | "; !strings.Contains(doc, row) {
			t.Errorf("README tokens table has no row for %q", token)
		}
	}
	for _, role := range theme.Roles() {
		if row := "\n| `" + role.String() + "` | `" + theme.DefaultToken(role).String() + "` | "; !strings.Contains(doc, row) {
			t.Errorf("README roles table has no row %q", strings.TrimSpace(row))
		}
	}
	for _, name := range theme.BuiltinNames() {
		if row := "\n| `" + name + "` | "; !strings.Contains(doc, row) {
			t.Errorf("README themes table has no row for %q", name)
		}
	}
}

// TestREADME_HerdrIconsExample proves the worked example of icons inside
// Herdr workspace names loads and draws what it says: the open workspace
// named "<icon> ~/fsociety/stage2" reads "stage2" with "~/fsociety" as its context.
func TestREADME_HerdrIconsExample(t *testing.T) {
	t.Parallel()
	cfg := loadREADMEBlock(t, readmeBlock(t, readme(t), "<!-- example:herdr-icons -->"))
	if len(cfg.Wildcards) != 1 || cfg.Wildcards[0].WorkspaceName == "" || cfg.Wildcards[0].Icon == nil {
		t.Fatalf("example wildcards = %+v, want one rule with workspace_name and icon", cfg.Wildcards)
	}
	engine := tmpl.New(tmpl.SampleHome)
	icon := strings.TrimSpace(*cfg.Wildcards[0].Icon)
	name, err := engine.RenderPlain(cfg.Wildcards[0].WorkspaceName, tmpl.Data{Path: tmpl.SampleHome + "/fsociety/stage2"})
	if err != nil || name != icon+" ~/fsociety/stage2" {
		t.Fatalf("workspace name = %q (%v), want %q", name, err, icon+" ~/fsociety/stage2")
	}
	p := cfg.Presentations().Herdr
	for _, part := range []struct{ format, want string }{{p.Label, "stage2"}, {p.Detail, "~/fsociety"}} {
		if got, err := engine.Render(part.format, tmpl.Data{Label: name}); err != nil || got != part.want {
			t.Errorf("%q on %q = %q (%v), want %q", part.format, name, got, err, part.want)
		}
	}
}
