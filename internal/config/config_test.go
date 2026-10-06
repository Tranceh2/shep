package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestLoadConfirmClose(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
		err   string
	}{
		{"", nil, ""}, {`confirm_close = []`, nil, ""},
		{`confirm_close = ["workspace", "tab", "pane"]`, []string{"workspace", "tab", "pane"}, ""},
		{`confirm_close = ["tab", "tab"]`, nil, "duplicate"},
		{`confirm_close = ["other"]`, nil, "tui.confirm_close"},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("version = 2\n[tui]\n"+tc.value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("%q: error=%v", tc.value, err)
			}
			continue
		}
		if err != nil || (len(cfg.TUI.ConfirmClose) != 0 || len(tc.want) != 0) && !reflect.DeepEqual(cfg.TUI.ConfirmClose, tc.want) {
			t.Fatalf("%q: config=%+v error=%v", tc.value, cfg, err)
		}
	}
}

// TestDefaults_PathAgnostic ensures Defaults() produces no hardcoded absolute
// user paths and ships with every built-in source enabled.
func TestDefaults_EnableRanking(t *testing.T) {
	t.Parallel()
	if !Defaults().Ranking.Enabled {
		t.Fatal("ranking should be enabled by default")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ranking]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ranking.Enabled {
		t.Fatal("explicit ranking.enabled=false was ignored")
	}
}

func TestLoad_RejectsControlAliases(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := "version = 2\n[[workspaces]]\nname = \"x\"\npath = \"/tmp/x\"\naliases = [\"safe\", \"bad\\nvalue\"]\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Workspaces[0].Aliases; !reflect.DeepEqual(got, []string{"safe"}) {
		t.Fatalf("aliases = %v, want control characters removed", got)
	}
}

func TestLoad_NormalizesAliases(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := `[[workspaces]]
name = "Kubernetes"
path = "/tmp/kube"
aliases = [" k8s ", "KUBE", "k8s", " "]

[[sources.custom]]
name = "kube-contexts"
command = ["printf", "[]"]
aliases = [" k8s ", "K8S", "kube"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Workspaces[0].Aliases, []string{"k8s", "KUBE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace aliases = %v, want %v", got, want)
	}
	if got, want := cfg.Sources.Custom[0].Aliases, []string{"k8s", "kube"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("custom source aliases = %v, want %v", got, want)
	}
}

func TestDefaults_PathAgnostic(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg == nil {
		t.Fatal("Defaults returned nil")
	}
	if len(cfg.General.SourceOrder) != 4 {
		t.Fatalf("expected 4 default sources, got %d: %v", len(cfg.General.SourceOrder), cfg.General.SourceOrder)
	}
	if cfg.Workspaces == nil {
		t.Fatal("Defaults Workspaces slice must be non-nil")
	}
	if cfg.Sources.Custom == nil {
		t.Fatal("Defaults custom sources slice must be non-nil")
	}
	if cfg.Wildcards == nil {
		t.Fatal("Defaults Wildcards slice must be non-nil")
	}
	if cfg.Templates == nil {
		t.Fatal("Defaults Templates map must be non-nil")
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	got := string(b)
	for _, bad := range []string{"/Users/", "/home/trance", "Proyectos"} {
		if strings.Contains(got, bad) {
			t.Errorf("defaults contain hardcoded %q:\n%s", bad, got)
		}
	}
}

// TestDefaults_SourcesOrder confirms the canonical default source order.
func TestDefaults_SourcesOrder(t *testing.T) {
	t.Parallel()
	want := []string{SourceHerdr, SourceWorkspaces, SourceZoxide, SourceProjects}
	got := Defaults().General.SourceOrder
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sources[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestLoad_TemplateNodeLabelPresence pins the TOML decoder contract for the
// optional leaf label: omitted preserves, empty clears, and non-empty renames.
func TestLoad_TemplateNodeLabelPresence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		labelLine string
		want      *string
	}{
		{name: "omitted", want: nil},
		{name: "explicit empty", labelLine: `label = ""`, want: stringPtr("")},
		{name: "non-empty", labelLine: `label = "x"`, want: stringPtr("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := "[[templates.dev.tabs]]\nname = \"code\"\nroot = \"shell\"\n\n[[templates.dev.tabs.nodes]]\nid = \"shell\"\ncommand = \"\"\n"
			if tc.labelLine != "" {
				doc += tc.labelLine + "\n"
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			got := cfg.Templates["dev"].Tabs[0].Nodes[0].Label
			if tc.want == nil {
				if got != nil {
					t.Fatalf("label = %q, want omitted nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("label is nil, want pointer to %q", *tc.want)
			}
			if *got != *tc.want {
				t.Errorf("label = %q, want %q", *got, *tc.want)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }

// TestLoad_UsesStructuredSourceOrderAndTypedProjectOverrides defines the clean
// schema contract before the production model is migrated.
func TestLoad_UsesStructuredSourceOrderAndTypedProjectOverrides(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[general]
source_order = ["workspaces", "projects"]

[sources.projects]
roots = ["~/code", "~/code/../code"]
recursive = true
max_depth = 4
markers = ["go.mod"]

[[workspaces]]
name = "projects"
type = "group"
path = "~/code"
source_order = ["projects"]

[workspaces.sources.projects]
recursive = false
max_depth = 0
markers = []
ignore = []
preview = []
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := cfg.General.SourceOrder, []string{SourceWorkspaces, SourceProjects}; !reflect.DeepEqual(got, want) {
		t.Fatalf("source order = %v, want %v", got, want)
	}
	if got, want := cfg.Sources.Projects.Roots, []string{"~/code", "~/code/../code"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project roots = %v, want %v", got, want)
	}
	group := cfg.Workspaces[0]
	if got, want := group.SourceOrder, []string{SourceProjects}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group source order = %v, want %v", got, want)
	}
	if group.Sources.Projects == nil || group.Sources.Projects.Recursive == nil || *group.Sources.Projects.Recursive {
		t.Fatalf("explicit false recursive override was not preserved: %+v", group.Sources.Projects)
	}
	if group.Sources.Projects.MaxDepth == nil || *group.Sources.Projects.MaxDepth != 0 {
		t.Fatalf("explicit zero max_depth override was not preserved: %+v", group.Sources.Projects)
	}
	if group.Sources.Projects.Markers == nil || len(*group.Sources.Projects.Markers) != 0 {
		t.Fatalf("explicit empty markers override was not preserved: %+v", group.Sources.Projects)
	}
}

// TestLoad_RejectsLegacyListShapedSources protects the intentional clean schema
// break: sources is reserved for structured provider tables.
func TestLoad_RejectsLegacyListShapedSources(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n[general]\nsources = [\"herdr\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("legacy general.sources list should be rejected")
	}
}

func TestLoad_ValidatesConfigSchemaVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{name: "current accepted", doc: "version = 2\n", want: ""},
		{name: "old rejected with migration", doc: "version = 1\n", want: "migrate to version = 2"},
		{name: "future rejected", doc: "version = 3\n", want: "newer than supported version 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Load() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestMergeProjectsSourceConfigHonorsExplicitZeroValues verifies field-wise
// inheritance and replacement semantics independently from TOML decoding.
func TestMergeProjectsSourceConfigHonorsExplicitZeroValues(t *testing.T) {
	t.Parallel()
	markers := []string{}
	ignore := []string{}
	preview := []string{}
	override := &ProjectsSourceOverride{
		Recursive: &[]bool{false}[0],
		MaxDepth:  &[]int{0}[0],
		Markers:   &markers,
		Ignore:    &ignore,
		Preview:   &preview,
	}
	global := ProjectsSourceConfig{Recursive: true, MaxDepth: 5, Markers: []string{"go.mod"}, Ignore: []string{"vendor"}, Preview: []string{"git"}}
	got := MergeProjectsSourceConfig(global, override)
	if got.Recursive || got.MaxDepth != 0 || len(got.Markers) != 0 || len(got.Ignore) != 0 || len(got.Preview) != 0 {
		t.Fatalf("merged project config did not honor explicit replacements: %+v", got)
	}
}

// TestMergeProjectsSourceConfigInheritsOmittedFields proves omitted local
// fields retain their global values while one present field replaces only that
// field. Explicit false, zero, and empty-list replacement is covered above.
func TestMergeProjectsSourceConfigInheritsOmittedFields(t *testing.T) {
	t.Parallel()
	global := ProjectsSourceConfig{
		Recursive: true,
		MaxDepth:  5,
		Markers:   []string{"go.mod"},
		Ignore:    []string{"vendor"},
		Preview:   []string{"git"},
	}
	override := &ProjectsSourceOverride{MaxDepth: &[]int{2}[0]}

	got := MergeProjectsSourceConfig(global, override)
	if got.Recursive != global.Recursive {
		t.Errorf("recursive = %v, want inherited %v", got.Recursive, global.Recursive)
	}
	if got.MaxDepth != 2 {
		t.Errorf("max_depth = %d, want explicit override 2", got.MaxDepth)
	}
	if !reflect.DeepEqual(got.Markers, global.Markers) {
		t.Errorf("markers = %v, want inherited %v", got.Markers, global.Markers)
	}
	if !reflect.DeepEqual(got.Ignore, global.Ignore) {
		t.Errorf("ignore = %v, want inherited %v", got.Ignore, global.Ignore)
	}
	if !reflect.DeepEqual(got.Preview, global.Preview) {
		t.Errorf("preview = %v, want inherited %v", got.Preview, global.Preview)
	}

	got.Markers[0] = "changed"
	if global.Markers[0] != "go.mod" {
		t.Fatal("merged markers share backing storage with global configuration")
	}
}

// TestSessionsSource_OptInRegistration verifies sessions is accepted when
// configured, carries its source presentation defaults, and never joins the
// default source order.
func TestSessionsSource_OptInRegistration(t *testing.T) {
	t.Parallel()

	defaults := Defaults()
	for _, name := range defaults.General.SourceOrder {
		if name == SourceSessions {
			t.Fatalf("sessions must be opt-in, default sources = %v", defaults.General.SourceOrder)
		}
	}
	if err := validateSources([]string{SourceSessions}); err != nil {
		t.Fatalf("sessions must be a valid source: %v", err)
	}
	if got, want := defaults.Sources.Sessions.LabelFormat, "{{.Label}}"; got != want {
		t.Errorf("sessions label format = %q, want %q", got, want)
	}
	if got := defaults.Sources.Sessions.Preview; len(got) != 0 {
		t.Errorf("sessions preview = %v, want unset", got)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[general]
source_order = ["sessions"]

[sources.sessions]
icon = "S"
label_format = "session {{.Label}}"
preview = ["session_info"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load sessions config: %v", err)
	}
	if got, want := cfg.General.SourceOrder, []string{SourceSessions}; !reflect.DeepEqual(got, want) {
		t.Errorf("configured sources = %v, want %v", got, want)
	}
	if got, want := cfg.Sources.Sessions.Icon, "S"; got != want {
		t.Errorf("sessions icon = %q, want %q", got, want)
	}
	if got, want := cfg.Sources.Sessions.LabelFormat, "session {{.Label}}"; got != want {
		t.Errorf("sessions label format = %q, want %q", got, want)
	}
}

func TestDefaults_VisualDefaults(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: SourceHerdr, got: cfg.Sources.Herdr.Icon, want: "\U000f0cc6 "},
		{name: SourceWorkspaces, got: cfg.Sources.Workspaces.Icon, want: "\ue615 "},
		{name: SourceZoxide, got: cfg.Sources.Zoxide.Icon, want: "\uf114 "},
		{name: SourceProjects, got: cfg.Sources.Projects.Icon, want: "\ue702 "},
	} {
		if tc.got != tc.want {
			t.Errorf("%s icon = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if got := cfg.Sources.Sessions.Icon; got != "" {
		t.Errorf("sessions icon = %q, want empty (sessions has no default icon)", got)
	}
	if got, want := cfg.Preview.Default, []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}; !reflect.DeepEqual(got, want) {
		t.Fatalf("preview default sections = %v, want %v", got, want)
	}
	if got, want := cfg.TUI.ListWidth, "35%"; got != want {
		t.Fatalf("tui list_width = %q, want %q", got, want)
	}
	if got, want := cfg.TUI.PreviewWidth, "65%"; got != want {
		t.Fatalf("tui preview_width = %q, want %q", got, want)
	}
}

// TestLoad_MissingFileFallsBackToDefaults confirms a missing config path
// resolves to Defaults rather than an error, including the visual defaults.
func TestLoad_MissingFileFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg, err := Load(filepath.Join(tmp, "nope.toml"))
	if err != nil {
		t.Fatalf("expected nil error on missing file, got %v", err)
	}
	if cfg == nil {
		t.Fatal("expected defaults, got nil")
	}
	if got, want := time.Duration(cfg.Preview.Timeout), 150*time.Millisecond; got != want {
		t.Errorf("preview timeout: got %v want %v", got, want)
	}
	if got, want := time.Duration(cfg.Preview.CacheTTL), 5*time.Second; got != want {
		t.Errorf("preview cache_ttl: got %v want %v", got, want)
	}
	if got, want := cfg.Preview.MaxLines, 50; got != want {
		t.Errorf("preview max_lines: got %d want %d", got, want)
	}
	if got, want := cfg.Preview.Default, []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}; !reflect.DeepEqual(got, want) {
		t.Errorf("preview default sections: got %v want %v", got, want)
	}
	if got, want := cfg.TUI.ListWidth, "35%"; got != want {
		t.Errorf("tui list_width: got %q want %q", got, want)
	}
	if got, want := cfg.TUI.PreviewWidth, "65%"; got != want {
		t.Errorf("tui preview_width: got %q want %q", got, want)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: SourceHerdr, got: cfg.Sources.Herdr.Icon, want: "\U000f0cc6 "},
		{name: SourceWorkspaces, got: cfg.Sources.Workspaces.Icon, want: "\ue615 "},
		{name: SourceZoxide, got: cfg.Sources.Zoxide.Icon, want: "\uf114 "},
		{name: SourceProjects, got: cfg.Sources.Projects.Icon, want: "\ue702 "},
	} {
		if tc.got != tc.want {
			t.Errorf("%s icon: got %q want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoad_PresentConfigWithoutSourcesUsesSourceIconDefaults(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: SourceHerdr, got: cfg.Sources.Herdr.Icon, want: "\U000f0cc6 "},
		{name: SourceWorkspaces, got: cfg.Sources.Workspaces.Icon, want: "\ue615 "},
		{name: SourceZoxide, got: cfg.Sources.Zoxide.Icon, want: "\uf114 "},
		{name: SourceProjects, got: cfg.Sources.Projects.Icon, want: "\ue702 "},
	} {
		if tc.got != tc.want {
			t.Errorf("%s icon = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestLoad_PartialVisualDefaultsAndExplicitOverrides covers how the first-run
// visual defaults interact with a partial document.
//
// The pane split is one decision: validateTUI rejects a pair summing past
// 100%, so naming one side must NOT pair it with the built-in default for the
// other. A config carrying only `list_width = "50%"` was valid before these
// defaults existed and must stay valid — hence the regression case below.
func TestLoad_PartialVisualDefaultsAndExplicitOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		doc                   string
		wantList, wantPreview string
		wantSections          []string
	}{
		{name: "partial tui keeps the named side only", doc: "version = 2\n[tui]\nlist_width = \"30%\"\n", wantList: "30%", wantPreview: "", wantSections: []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}},
		{name: "a majority list width stays loadable", doc: "version = 2\n[tui]\nlist_width = \"50%\"\n", wantList: "50%", wantPreview: "", wantSections: []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}},
		{name: "naming only the preview side", doc: "version = 2\n[tui]\npreview_width = \"80%\"\n", wantList: "", wantPreview: "80%", wantSections: []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}},
		{name: "both named", doc: "version = 2\n[tui]\nlist_width = \"45%\"\npreview_width = \"55%\"\n", wantList: "45%", wantPreview: "55%", wantSections: []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}},
		{name: "explicit preview", doc: "version = 2\n[preview]\ndefault = [\"git\"]\n", wantList: "35%", wantPreview: "65%", wantSections: []string{"git"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TUI.ListWidth != tt.wantList || cfg.TUI.PreviewWidth != tt.wantPreview {
				t.Fatalf("widths = %q/%q, want %q/%q", cfg.TUI.ListWidth, cfg.TUI.PreviewWidth, tt.wantList, tt.wantPreview)
			}
			if !reflect.DeepEqual(cfg.Preview.Default, tt.wantSections) {
				t.Fatalf("preview.default = %v, want %v", cfg.Preview.Default, tt.wantSections)
			}
		})
	}
}

func TestLoad_ExplicitSourceIconsWin(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[sources.herdr]
icon = "H "

[sources.workspaces]
icon = "W "

[sources.zoxide]
icon = "Z "

[sources.projects]
icon = "P "
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: SourceHerdr, got: cfg.Sources.Herdr.Icon, want: "H "},
		{name: SourceWorkspaces, got: cfg.Sources.Workspaces.Icon, want: "W "},
		{name: SourceZoxide, got: cfg.Sources.Zoxide.Icon, want: "Z "},
		{name: SourceProjects, got: cfg.Sources.Projects.Icon, want: "P "},
	} {
		if tc.got != tc.want {
			t.Errorf("%s icon = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoad_ExplicitLabelFormatsWin(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = "[sources.zoxide]\nlabel_format = \"custom {{.Path}}\"\n[sources.projects]\nlabel_format = \"project {{.Label}}\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources.Zoxide.LabelFormat != "custom {{.Path}}" || cfg.Sources.Projects.LabelFormat != "project {{.Label}}" {
		t.Fatalf("explicit label formats were replaced: zoxide=%q projects=%q", cfg.Sources.Zoxide.LabelFormat, cfg.Sources.Projects.LabelFormat)
	}
}

func TestLoad_RejectsUnknownSourceName(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	doc := "[general]\nsource_order = [\"herdr\", \"cwd\"]\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown source name")
	}
	if !strings.Contains(err.Error(), "cwd") {
		t.Errorf("error %q should name the bad entry", err.Error())
	}
}

func TestLoad_CustomSourceSchema(t *testing.T) {
	for _, tc := range []struct {
		name, doc, want string
	}{
		{name: "new declaration", doc: "[[sources.custom]]\nname = \"prs\"\ncommand = [\"printf\", \"[]\"]\n", want: ""},
		{name: "obsolete declaration", doc: "[[integrations]]\nname = \"prs\"\ncommand = [\"printf\", \"[]\"]\n", want: "[[integrations]] was removed; declare command-backed sources under [[sources.custom]]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tc.want == "" && err != nil {
				t.Fatalf("new source rejected: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoad_ParsesAndEnablesCommandCustomSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	doc := `[general]
source_order = ["herdr", "prs", "projects"]

[[sources.custom]]
name = "prs"
command = ["gh", "pr", "list", "--json", "number,title"]
icon = "PR"
timeout = "3s"
label_format = "PR {{.Label}}"
preview = ["identity"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load custom source config: %v", err)
	}
	if got, want := len(cfg.Sources.Custom), 1; got != want {
		t.Fatalf("custom sources = %d, want %d", got, want)
	}
	got := cfg.Sources.Custom[0]
	if got.Name != "prs" || len(got.Command) != 5 || got.Icon != "PR" || time.Duration(got.Timeout) != 3*time.Second {
		t.Errorf("custom source = %+v", got)
	}
	if got.LabelFormat != "PR {{.Label}}" || !reflect.DeepEqual(got.Preview, []string{"identity"}) {
		t.Errorf("custom source presentation = %+v", got)
	}
}

func TestLoad_CustomSourceValidationFailsFast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{name: "empty name", doc: "[[sources.custom]]\nname = \" \"\ncommand = [\"printf\"]\n", want: "name is required"},
		{name: "duplicate name", doc: "[[sources.custom]]\nname = \"prs\"\ncommand = [\"printf\"]\n\n[[sources.custom]]\nname = \"prs\"\ncommand = [\"printf\"]\n", want: "duplicate"},
		{name: "built-in collision", doc: "[[sources.custom]]\nname = \"projects\"\ncommand = [\"printf\"]\n", want: "built-in source"},
		{name: "empty command", doc: "[[sources.custom]]\nname = \"prs\"\ncommand = []\n", want: "command is required"},
		{name: "invalid timeout", doc: "[[sources.custom]]\nname = \"prs\"\ncommand = [\"printf\"]\ntimeout = \"-1s\"\n", want: "timeout"},
		{name: "unknown source order", doc: "[general]\nsource_order = [\"prs\"]\n", want: "invalid source_order"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoad_CustomSourceTimeoutDefaultsToThreeSeconds(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[sources.custom]]\nname = \"prs\"\ncommand = [\"printf\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := time.Duration(cfg.Sources.Custom[0].Timeout), 3*time.Second; got != want {
		t.Errorf("custom source timeout = %v, want %v", got, want)
	}
}

func TestLoad_ParsesCustomSourceScopedPreviewCommandsAndInheritsDefaults(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[preview]
 timeout = "250ms"
 max_lines = 25
 commands.shared = { command = "printf global" }

[[sources.custom]]
name = "kube-contexts"
command = ["/path/kube-contexts"]
preview = ["identity", "cluster", "health"]

[sources.custom.preview_commands.cluster]
command = ["/path/kube-preview", "cluster", "{{ index .Meta \"context\" }}"]
max_lines = 12

[sources.custom.preview_commands.health]
command = ["/path/kube-preview", "health", "{{ index .Meta \"context\" }}"]
timeout = "1s"
max_lines = 10
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	customSource := cfg.Sources.Custom[0]
	cluster, ok := customSource.PreviewCommands["cluster"]
	if !ok {
		t.Fatal("cluster preview command was not decoded")
	}
	if !reflect.DeepEqual(cluster.Command, []string{"/path/kube-preview", "cluster", `{{ index .Meta "context" }}`}) {
		t.Errorf("cluster argv = %#v", cluster.Command)
	}
	if got, want := time.Duration(cluster.Timeout), 250*time.Millisecond; got != want {
		t.Errorf("cluster timeout = %v, want inherited %v", got, want)
	}
	if cluster.MaxLines != 12 {
		t.Errorf("cluster max_lines = %d, want 12", cluster.MaxLines)
	}
	health := customSource.PreviewCommands["health"]
	if got, want := time.Duration(health.Timeout), time.Second; got != want {
		t.Errorf("health timeout = %v, want %v", got, want)
	}
	if health.MaxLines != 10 {
		t.Errorf("health max_lines = %d, want 10", health.MaxLines)
	}
}

func TestLoad_CustomSourcePreviewNamespaceValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{name: "empty argv", doc: "[[sources.custom]]\nname=\"kube\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.cluster]\ncommand=[]\n", want: "preview_commands.cluster: command is required"},
		{name: "empty argv zero", doc: "[[sources.custom]]\nname=\"kube\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.cluster]\ncommand=[\"\"]\n", want: "preview_commands.cluster: command[0] is required"},
		{name: "negative timeout", doc: "[[sources.custom]]\nname=\"kube\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.cluster]\ncommand=[\"printf\"]\ntimeout=\"-1s\"\n", want: "preview_commands.cluster: timeout"},
		{name: "negative max lines", doc: "[[sources.custom]]\nname=\"kube\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.cluster]\ncommand=[\"printf\"]\nmax_lines=-1\n", want: "preview_commands.cluster: max_lines"},
		{name: "built-in collision", doc: "[[sources.custom]]\nname=\"kube\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.identity]\ncommand=[\"printf\"]\n", want: "preview_commands.identity: collides with built-in"},
		{name: "global collision", doc: "[preview.commands.cluster]\ncommand=\"printf\"\n\n[[sources.custom]]\nname=\"kube\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.cluster]\ncommand=[\"printf\"]\n", want: "preview_commands.cluster: collides with global"},
		{name: "foreign local", doc: "[[sources.custom]]\nname=\"kube-a\"\ncommand=[\"printf\"]\npreview=[\"cluster\"]\n[sources.custom.preview_commands.other]\ncommand=[\"printf\"]\n\n[[sources.custom]]\nname=\"kube-b\"\ncommand=[\"printf\"]\n[sources.custom.preview_commands.cluster]\ncommand=[\"printf\"]\n", want: "sources.custom[0] (\"kube-a\").preview"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tt.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoad_CustomSourceLocalNamesMayRepeatAcrossCustomSources(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[[sources.custom]]
name = "kube-a"
command = ["printf"]
preview = ["cluster"]
[sources.custom.preview_commands.cluster]
command = ["printf", "a"]

[[sources.custom]]
name = "kube-b"
command = ["printf"]
preview = ["cluster"]
[sources.custom.preview_commands.cluster]
command = ["printf", "b"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("same local name across custom sources should load: %v", err)
	}
}

func TestLoad_GroupSourceOrderAllowsDeclaredCustomSourceWithoutGlobalSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[[sources.custom]]
name = "kube-contexts"
command = ["printf", "[]"]

[general]
source_order = ["workspaces"]

[[workspaces]]
name = "Kubernetes"
type = "group"
path = "~/projects"
source_order = ["kube-contexts"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load group custom source config: %v", err)
	}
	if got, want := cfg.Workspaces[0].SourceOrder, []string{"kube-contexts"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group source_order = %v, want %v", got, want)
	}
}

func TestLoad_GroupSourceOrderRejectsUnknownSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `version = 2

[[workspaces]]
name = "Kubernetes"
type = "group"
path = "~/projects"
source_order = ["not-declared"]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "not-declared") {
		t.Fatalf("Load error = %v, want unknown group source", err)
	}
}

// TestLoad_ParsesSchema covers general, herdr, sources, defaults, tui,
// workspaces (plain + group), and templates sections of the canonical model.
func TestLoad_ParsesSchema(t *testing.T) {
	t.Parallel()

	const doc = `
version = 2

[general]
source_order = ["herdr", "workspaces", "zoxide", "projects"]
selector = "fzf"

[herdr]
binary = "/usr/local/bin/herdr"

[defaults]
type = "shell"
template = "default"

[tui]
list_width = "auto"
preview_width = "60%"

[sources.herdr]
icon = "H"
preview = ["workspace", "active_pane"]

[sources.projects]
recursive = true
max_depth = 3
markers = [".git", "go.mod"]
ignore = ["node_modules"]

[[workspaces]]
name = "dotfiles"
path = "~/dotfiles"

[[workspaces]]
name = "main-app"
path = "~/projects/main-app"
template = "dev"

[[workspaces]]
name = "projects"
type = "group"
path = "~/projects"
source_order = ["projects", "zoxide"]
template = "dev"

[[wildcards]]
pattern = "**/*.go"
template = "dev"

[templates.default]
command = ""

[templates.dev]
description = "development workspace"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := len(cfg.General.SourceOrder), 4; got != want {
		t.Errorf("sources len: got %d want %d", got, want)
	}
	if got, want := cfg.Herdr.Binary, "/usr/local/bin/herdr"; got != want {
		t.Errorf("herdr binary: got %q want %q", got, want)
	}
	if got, want := cfg.Defaults.Type, "shell"; got != want {
		t.Errorf("defaults type: got %q want %q", got, want)
	}
	if got, want := cfg.Defaults.Template, "default"; got != want {
		t.Errorf("defaults template: got %q want %q", got, want)
	}
	if got, want := cfg.TUI.ListWidth, "auto"; got != want {
		t.Errorf("tui.list_width: got %q want %q", got, want)
	}
	if got, want := cfg.TUI.PreviewWidth, "60%"; got != want {
		t.Errorf("tui.preview_width: got %q want %q", got, want)
	}
	if got, want := cfg.Sources.Herdr.Icon, "H"; got != want {
		t.Errorf("sources.herdr.icon: got %q want %q", got, want)
	}
	if got, want := len(cfg.Sources.Herdr.Preview), 2; got != want {
		t.Errorf("sources.herdr.preview len: got %d want %d", got, want)
	}
	if !cfg.Sources.Projects.Recursive {
		t.Error("sources.projects.recursive should be true")
	}
	if got, want := cfg.Sources.Projects.MaxDepth, 3; got != want {
		t.Errorf("sources.projects.max_depth: got %d want %d", got, want)
	}
	if got, want := len(cfg.Sources.Projects.Markers), 2; got != want {
		t.Errorf("sources.projects.markers len: got %d want %d", got, want)
	}
	if got, want := len(cfg.Workspaces), 3; got != want {
		t.Fatalf("workspaces len: got %d want %d", got, want)
	}
	if got, want := cfg.Workspaces[0].Name, "dotfiles"; got != want {
		t.Errorf("workspace0 name: got %q want %q", got, want)
	}
	if got, want := cfg.Workspaces[1].Template, "dev"; got != want {
		t.Errorf("workspace1 template: got %q want %q", got, want)
	}
	group := cfg.Workspaces[2]
	if got, want := group.Type, WorkspaceTypeGroup; got != want {
		t.Errorf("workspace2 type: got %q want %q", got, want)
	}
	if got, want := len(group.SourceOrder), 2; got != want {
		t.Fatalf("workspace2 sources len: got %d want %d", got, want)
	}
	if got, want := len(cfg.Wildcards), 1; got != want {
		t.Fatalf("wildcards len: got %d want %d", got, want)
	}
	if got, want := cfg.Wildcards[0].Template, "dev"; got != want {
		t.Errorf("wildcard0 template: got %q want %q", got, want)
	}
	if _, ok := cfg.Templates["dev"]; !ok {
		t.Error("missing templates.dev")
	}
}

// TestLoad_LabelFormatsRoundTrip confirms every source-specific label template
// decodes from TOML without being replaced by a default.
func TestLoad_LabelFormatsRoundTrip(t *testing.T) {
	t.Parallel()

	const doc = `
[sources.herdr]
label_format = "{{.Path}} / {{.Label}}"
tab_label_format = "tab {{.TabNumber}}: {{.Label}}"
pane_label_format = "pane {{.Path}}"

[sources.workspaces]
label_format = "workspace {{.Path}}"

[sources.zoxide]
label_format = "zoxide {{.Path}}"

[sources.projects]
label_format = "project {{.Path}}"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := []struct {
		field string
		got   string
		want  string
	}{
		{"sources.herdr.label_format", cfg.Sources.Herdr.LabelFormat, "{{.Path}} / {{.Label}}"},
		{"sources.herdr.tab_label_format", cfg.Sources.Herdr.TabLabelFormat, "tab {{.TabNumber}}: {{.Label}}"},
		{"sources.herdr.pane_label_format", cfg.Sources.Herdr.PaneLabelFormat, "pane {{.Path}}"},
		{"sources.workspaces.label_format", cfg.Sources.Workspaces.LabelFormat, "workspace {{.Path}}"},
		{"sources.zoxide.label_format", cfg.Sources.Zoxide.LabelFormat, "zoxide {{.Path}}"},
		{"sources.projects.label_format", cfg.Sources.Projects.LabelFormat, "project {{.Path}}"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q want %q", tc.field, tc.got, tc.want)
		}
	}
}

// TestLoad_LabelFormatsDefault verifies empty label format fields resolve to
// the first-run label-first rendering defaults during Load.
func TestLoad_LabelFormatsDefault(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("[general]\nsource_order = [\"herdr\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	const (
		labelWithPath         = "{{if .Label}}{{.Label}} · {{end}}{{.Path}}"
		labelWithPathFallback = "{{if .Label}}{{.Label}}{{else}}{{.Path}}{{end}}"
	)
	cases := []struct {
		field string
		got   string
		want  string
	}{
		{"sources.herdr.label_format", cfg.Sources.Herdr.LabelFormat, "{{.Label}}"},
		{"sources.herdr.tab_label_format", cfg.Sources.Herdr.TabLabelFormat, labelWithPath},
		{"sources.herdr.pane_label_format", cfg.Sources.Herdr.PaneLabelFormat, labelWithPath},
		{"sources.sessions.label_format", cfg.Sources.Sessions.LabelFormat, "{{.Label}}"},
		{"sources.workspaces.label_format", cfg.Sources.Workspaces.LabelFormat, "{{.Label}}"},
		{"sources.zoxide.label_format", cfg.Sources.Zoxide.LabelFormat, labelWithPathFallback},
		{"sources.projects.label_format", cfg.Sources.Projects.LabelFormat, labelWithPathFallback},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got %q want %q", tc.field, tc.got, tc.want)
		}
	}
}

// TestLoad_RejectsInvalidLabelFormats ensures every supported source field
// fails Load with its own field path when a template cannot render safely.
func TestLoad_RejectsInvalidLabelFormats(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		doc    string
		field  string
		detail string
	}{
		{
			name:   "malformed herdr label format",
			doc:    "[sources.herdr]\nlabel_format = \"{{if .Label}}\"\n",
			field:  "sources.herdr.label_format",
			detail: "invalid template",
		},
		{
			name:   "unknown herdr tab field",
			doc:    "[sources.herdr]\ntab_label_format = \"{{.Unknown}}\"\n",
			field:  "sources.herdr.tab_label_format",
			detail: "invalid template",
		},
		{
			name:   "legacy path in herdr pane format",
			doc:    "[sources.herdr]\npane_label_format = \"pane " + legacyTemplateSyntax("path") + "\"\n",
			field:  "sources.herdr.pane_label_format",
			detail: "legacy placeholder",
		},
		{
			name:   "legacy label in workspaces format",
			doc:    "[sources.workspaces]\nlabel_format = \"" + legacyTemplateSyntax("label") + "\"\n",
			field:  "sources.workspaces.label_format",
			detail: "legacy placeholder",
		},
		{
			name:   "malformed zoxide label format",
			doc:    "[sources.zoxide]\nlabel_format = \"{{.Path\"\n",
			field:  "sources.zoxide.label_format",
			detail: "invalid template",
		},
		{
			name:   "unknown projects label field",
			doc:    "[sources.projects]\nlabel_format = \"{{.Unknown}}\"\n",
			field:  "sources.projects.label_format",
			detail: "invalid template",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatal("expected Load to reject invalid label format")
			}
			for _, want := range []string{tc.field, tc.detail} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

// TestLoad_RejectsInvalidPreviewCommandTemplates validates preview commands
// using the same template engine after tokenizing their argv-shaped input.
func TestLoad_RejectsInvalidPreviewCommandTemplates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		command string
		detail  string
	}{
		{name: "legacy path", command: "git -C " + legacyTemplateSyntax("path") + " status", detail: "legacy placeholder"},
		{name: "legacy label", command: "echo " + legacyTemplateSyntax("label"), detail: "legacy placeholder"},
		{name: "malformed action", command: "echo {{.Path", detail: "invalid template"},
		{name: "unknown context field", command: "echo {{.Unknown}}", detail: "invalid template"},
		{name: "unterminated quote", command: "echo \"{{.Path}}", detail: "unterminated quote"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			doc := "[preview.commands.check]\ncommand = " + strconv.Quote(tc.command) + "\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := Load(path)
			if err == nil {
				t.Fatal("expected Load to reject invalid preview command template")
			}
			for _, want := range []string{"preview.commands.check.command", tc.detail} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

// TestLoad_MalformedReturnsError wraps the parse error so callers can surface
// it without losing the originating file path.
func TestLoad_MalformedReturnsError(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.toml")
	if err := os.WriteFile(path, []byte("not = = valid toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error parsing malformed toml, got nil")
	}
}

// TestDiscoverPath_UnderUserConfigDir checks the path lives under the user
// config directory and ends with the shep-relative suffix.
func TestDiscoverPath_UnderUserConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	p, err := DiscoverPath()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("shep", "config.toml")) {
		t.Errorf("discover path suffix mismatch: %q", p)
	}
}

// TestDiscoverPath_XDGOverride honours XDG_CONFIG_HOME on platforms where
// os.UserConfigDir reads it (Linux). On darwin UserConfigDir ignores XDG, so
// we only assert the suffix and that the call does not error.
func TestDiscoverPath_XDGOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p, err := DiscoverPath()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("shep", "config.toml")) {
		t.Errorf("discover suffix mismatch: %q", p)
	}
}

// TestProbe reports presence of guaranteed-present and guaranteed-absent
// binaries so the probe helper cannot silently regress.
func TestProbe(t *testing.T) {
	t.Parallel()
	if !Probe("sh") && !Probe("go") {
		t.Error("expected at least one of sh/go to be found on PATH")
	}
	if Probe("definitely-not-a-binary-xyz-shep") {
		t.Error("probe should report false for missing binary")
	}
}

// TestDefaults_SelectorIsBuiltin confirms Defaults() ships the builtin
// selector so commands that read Defaults() (no file loaded) route to the
// Bubble Tea TUI instead of relying on a configured file.
func TestDefaults_SelectorIsBuiltin(t *testing.T) {
	t.Parallel()
	if got, want := Defaults().General.Selector, SelectorBuiltin; got != want {
		t.Errorf("defaults selector: got %q want %q", got, want)
	}
}

// TestLoad_SelectorDefaultsToBuiltin confirms an absent selector field
// resolves to the builtin default at load time.
func TestLoad_SelectorDefaultsToBuiltin(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("[general]\nsource_order = [\"herdr\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := cfg.General.Selector, SelectorBuiltin; got != want {
		t.Errorf("default selector: got %q want %q", got, want)
	}
}

// TestLoad_SelectorTable covers valid values accepted, empty->default, and
// the descriptive rejection of an unknown value listing valid options.
func TestLoad_SelectorTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		doc     string
		want    string
		wantErr bool
		errSub  string
	}{
		{name: "builtin accepted", doc: "[general]\nselector = \"builtin\"\n", want: SelectorBuiltin},
		{name: "fzf accepted", doc: "[general]\nselector = \"fzf\"\n", want: SelectorFzf},
		{name: "auto accepted", doc: "[general]\nselector = \"auto\"\n", want: SelectorAuto},
		{name: "empty defaults to builtin", doc: "[general]\nselector = \"\"\n", want: SelectorBuiltin},
		{name: "invalid rejected", doc: "[general]\nselector = \"invalid\"\n", wantErr: true, errSub: "invalid"},
		{name: "garbage value rejected", doc: "[general]\nselector = \"browser\"\n", wantErr: true, errSub: "browser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("error %q missing substring %q", err.Error(), tc.errSub)
				}
				if !strings.Contains(err.Error(), "valid:") {
					t.Errorf("error should list valid options: %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if cfg.General.Selector != tc.want {
				t.Errorf("selector: got %q want %q", cfg.General.Selector, tc.want)
			}
		})
	}
}

func TestHerdrBinaryWith_PrecedenceAndValidation(t *testing.T) {
	t.Parallel()
	validEnv := filepath.Join(t.TempDir(), "herdr-env")
	if err := os.WriteFile(validEnv, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	validConfig := filepath.Join(t.TempDir(), "herdr-config")
	if err := os.WriteFile(validConfig, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	invalidEnv := filepath.Join(t.TempDir(), "herdr-invalid")
	if err := os.WriteFile(invalidEnv, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}

	lookupEnv := func(value string) func(string) (string, bool) {
		return func(string) (string, bool) { return value, true }
	}
	missing := func(string) (string, error) { return "", os.ErrNotExist }
	resolvable := func(name string) (string, error) {
		if name == "configured-herdr" {
			return name, nil
		}
		return "", os.ErrNotExist
	}

	t.Run("usable configured absolute path wins over env", func(t *testing.T) {
		cfg := Defaults()
		cfg.Herdr.Binary = validConfig
		if got := HerdrBinaryWith(cfg, lookupEnv(validEnv), missing); got != validConfig {
			t.Fatalf("HerdrBinaryWith = %q, want %q", got, validConfig)
		}
		if probes := ProbesForWith(cfg, lookupEnv(validEnv), missing); !probes.Herdr {
			t.Fatal("usable configured absolute path should enable Herdr")
		}
	})

	t.Run("resolvable configured name wins over env", func(t *testing.T) {
		cfg := Defaults()
		cfg.Herdr.Binary = "configured-herdr"
		if got := HerdrBinaryWith(cfg, lookupEnv(validEnv), resolvable); got != "configured-herdr" {
			t.Fatalf("HerdrBinaryWith = %q, want configured-herdr", got)
		}
		if probes := ProbesForWith(cfg, lookupEnv(validEnv), resolvable); !probes.Herdr {
			t.Fatal("resolvable configured name should enable Herdr")
		}
	})

	t.Run("unusable configured name falls back to valid env path", func(t *testing.T) {
		cfg := Defaults()
		cfg.Herdr.Binary = "stale-configured-herdr"
		if got := HerdrBinaryWith(cfg, lookupEnv(validEnv), missing); got != validEnv {
			t.Fatalf("HerdrBinaryWith = %q, want %q", got, validEnv)
		}
		if probes := ProbesForWith(cfg, lookupEnv(validEnv), missing); !probes.Herdr {
			t.Fatal("valid env path should keep Herdr enabled")
		}
	})

	t.Run("unusable configured name is preserved without valid env", func(t *testing.T) {
		cfg := Defaults()
		cfg.Herdr.Binary = "stale-configured-herdr"
		if got := HerdrBinaryWith(cfg, lookupEnv(invalidEnv), missing); got != cfg.Herdr.Binary {
			t.Fatalf("HerdrBinaryWith = %q, want configured value", got)
		}
		if probes := ProbesForWith(cfg, lookupEnv(invalidEnv), missing); probes.Herdr {
			t.Fatal("unusable configured value should remain unavailable")
		}
	})

	t.Run("invalid env path is ignored", func(t *testing.T) {
		if got := HerdrBinaryWith(Defaults(), lookupEnv(invalidEnv), missing); got != "herdr" {
			t.Fatalf("HerdrBinaryWith = %q, want herdr", got)
		}
		if probes := ProbesForWith(Defaults(), lookupEnv(invalidEnv), missing); probes.Herdr {
			t.Fatal("invalid env path should not enable Herdr")
		}
	})
}

// TestHerdrBinary_Default checks the empty-config default.
func TestHerdrBinary_Default(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if got, want := cfg.HerdrBinary(), "herdr"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	cfg.Herdr.Binary = "custom-herdr"
	if got, want := cfg.HerdrBinary(), "custom-herdr"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestLoad_ParsesPreview covers [preview], [preview.commands.<name>]
// parsing and duration defaults.
func TestLoad_ParsesPreview(t *testing.T) {
	t.Parallel()

	const doc = `
[preview]
timeout = "250ms"
cache_ttl = "10s"
max_lines = 7
default = ["identity", "git"]

[preview.commands.recent_commits]
command = "git -C {{.Path}} log -n 5"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	pv := cfg.Preview
	if got, want := time.Duration(pv.Timeout), 250*time.Millisecond; got != want {
		t.Errorf("preview timeout: got %v want %v", got, want)
	}
	if got, want := time.Duration(pv.CacheTTL), 10*time.Second; got != want {
		t.Errorf("preview cache_ttl: got %v want %v", got, want)
	}
	if got, want := pv.MaxLines, 7; got != want {
		t.Errorf("preview max_lines: got %d want %d", got, want)
	}
	if got, want := len(pv.Default), 2; got != want {
		t.Fatalf("default len: got %d want %d", got, want)
	}
	cmd, ok := pv.Commands["recent_commits"]
	if !ok {
		t.Fatal("missing preview.commands.recent_commits")
	}
	if got, want := cmd.Command, "git -C {{.Path}} log -n 5"; got != want {
		t.Errorf("command: got %q want %q", got, want)
	}
}

// TestLoad_PreviewDefaultsApplied confirms omitted preview durations and
// max_lines resolve to the documented defaults (150ms / 5s / 50).
func TestLoad_PreviewDefaultsApplied(t *testing.T) {
	t.Parallel()

	const doc = `
[preview.commands.x]
command = "echo hi"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := time.Duration(cfg.Preview.Timeout), 150*time.Millisecond; got != want {
		t.Errorf("default timeout: got %v want %v", got, want)
	}
	if got, want := time.Duration(cfg.Preview.CacheTTL), 5*time.Second; got != want {
		t.Errorf("default cache_ttl: got %v want %v", got, want)
	}
	if got, want := cfg.Preview.MaxLines, 50; got != want {
		t.Errorf("default max_lines: got %d want %d", got, want)
	}
}

// TestLoad_InvalidPreviewRejected confirms an unknown default section name
// and negative caps fail fast with helpful substrings.
func TestLoad_InvalidPreviewRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		doc    string
		errSub string
	}{
		{
			name:   "unknown default name",
			doc:    "[preview]\ndefault = [\"nope\"]\n",
			errSub: "nope",
		},
		{
			name:   "negative max_lines",
			doc:    "[preview]\nmax_lines = -1\n",
			errSub: "max_lines",
		},
		{
			name:   "negative timeout",
			doc:    "[preview]\ntimeout = \"-5s\"\n",
			errSub: "timeout",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("error %q must contain %q", err.Error(), tc.errSub)
			}
		})
	}
}

// TestLoad_RejectsUnknownPreviewNameAnywhere (requirement 9) confirms an
// unknown preview name fails fast no matter where it is declared: a source,
// a workspace, or a wildcard.
func TestLoad_RejectsUnknownPreviewNameAnywhere(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
	}{
		{name: "source", doc: "[sources.herdr]\npreview = [\"nope\"]\n"},
		{name: "workspace", doc: "[[workspaces]]\nname = \"x\"\npath = \"~/x\"\npreview = [\"nope\"]\n"},
		{name: "wildcard", doc: "[[wildcards]]\npattern = \"*.go\"\npreview = [\"nope\"]\n"},
		{name: "custom source", doc: "[[sources.custom]]\nname = \"prs\"\ncommand = [\"printf\"]\npreview = [\"nope\"]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected error for unknown preview name")
			}
			if !strings.Contains(err.Error(), "nope") {
				t.Errorf("error %q should name the bad entry", err.Error())
			}
		})
	}
}

// TestLoad_PreviewDefaultAcceptsBuiltinsAndCommands confirms every hardcoded
// built-in name plus a declared command name is accepted in [preview].default.
func TestLoad_PreviewDefaultAcceptsBuiltinsAndCommands(t *testing.T) {
	t.Parallel()
	const doc = `
[preview]
default = ["identity", "git", "workspace", "active_pane", "dir", "custom"]

[preview.commands.custom]
command = "echo hi"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// TestLoad_TemplatesTabsAndCommandMutuallyExclusive confirms a template
// cannot set both command and tabs.
func TestLoad_TemplatesTabsAndCommandMutuallyExclusive(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.bad]
command = "echo hi"

[[templates.bad.tabs]]
name = "x"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for command+tabs template")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should name the template: %q", err.Error())
	}
}

// TestLoad_TemplateTabsValidation covers the node-graph invariants: a tab
// with nodes requires root, root/children must reference real node ids,
// split must be rows/cols, sizes must match children length, and a branch
// node cannot also set command.
func TestLoad_TemplateTabsValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		doc            string
		errSub         string
		wantErrSubstrs []string
	}{
		{
			name: "missing root",
			doc: `[[templates.dev.tabs]]
name = "code"
[[templates.dev.tabs.nodes]]
id = "main"
command = "nvim"
`,
			errSub: "root",
		},
		{
			name: "root references unknown node",
			doc: `[[templates.dev.tabs]]
name = "code"
root = "ghost"
[[templates.dev.tabs.nodes]]
id = "main"
command = "nvim"
`,
			errSub: "ghost",
		},
		{
			name: "invalid split value",
			doc: `[[templates.dev.tabs]]
name = "code"
root = "main"
[[templates.dev.tabs.nodes]]
id = "main"
split = "diagonal"
children = ["a", "b"]
[[templates.dev.tabs.nodes]]
id = "a"
command = ""
[[templates.dev.tabs.nodes]]
id = "b"
command = ""
`,
			errSub: "diagonal",
		},
		{
			name: "sizes length mismatch",
			doc: `[[templates.dev.tabs]]
name = "code"
root = "main"
[[templates.dev.tabs.nodes]]
id = "main"
split = "rows"
children = ["a", "b"]
sizes = [80]
[[templates.dev.tabs.nodes]]
id = "a"
command = ""
[[templates.dev.tabs.nodes]]
id = "b"
command = ""
`,
			errSub: "sizes",
		},
		{
			name: "branch cannot set command",
			doc: `[[templates.dev.tabs]]
name = "code"
root = "main"
[[templates.dev.tabs.nodes]]
id = "main"
split = "rows"
children = ["a", "b"]
command = "oops"
[[templates.dev.tabs.nodes]]
id = "a"
command = ""
[[templates.dev.tabs.nodes]]
id = "b"
command = ""
`,
			errSub: "branch",
		},
		{
			name: "branch cannot set label",
			doc: `[[templates.dev.tabs]]
name = "code"
root = "layout"
[[templates.dev.tabs.nodes]]
id = "layout"
split = "rows"
children = ["shell", "logs"]
label = "group"
[[templates.dev.tabs.nodes]]
id = "shell"
command = ""
[[templates.dev.tabs.nodes]]
id = "logs"
command = "tail -f app.log"
`,
			errSub:         "layout",
			wantErrSubstrs: []string{"label"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("error %q must contain %q", err.Error(), tc.errSub)
			}
			for _, want := range tc.wantErrSubstrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q must mention %q", err.Error(), want)
				}
			}
		})
	}
}

// TestLoad_RejectsExplicitEmptyBranchLabel confirms an explicitly empty label
// is still presence-aware and rejected on a branch at config load.
func TestLoad_RejectsExplicitEmptyBranchLabel(t *testing.T) {
	t.Parallel()
	const doc = `
[[templates.dev.tabs]]
name = "code"
root = "empty-layout"
[[templates.dev.tabs.nodes]]
id = "empty-layout"
split = "rows"
children = ["shell", "logs"]
label = ""
[[templates.dev.tabs.nodes]]
id = "shell"
command = ""
[[templates.dev.tabs.nodes]]
id = "logs"
command = "tail -f app.log"
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected explicit empty branch label to fail at load")
	}
	for _, want := range []string{"templates.dev.tabs[0]", "code", "empty-layout", "label"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must contain %q", err.Error(), want)
		}
	}
}

// TestLoad_TemplateTabsValid confirms the canonical dev template from the
// spec parses and validates cleanly.
func TestLoad_TemplateTabsValid(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
description = "development workspace"

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  split = "rows"
  children = ["editor", "terminal"]
  sizes = [80, 20]

  [[templates.dev.tabs.nodes]]
  id = "editor"
  command = "nvim"

  [[templates.dev.tabs.nodes]]
  id = "terminal"
  command = ""
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tpl, ok := cfg.Templates["dev"]
	if !ok {
		t.Fatal("missing templates.dev")
	}
	if got, want := len(tpl.Tabs), 1; got != want {
		t.Fatalf("tabs len: got %d want %d", got, want)
	}
	tab := tpl.Tabs[0]
	if got, want := tab.Name, "code"; got != want {
		t.Errorf("tab name: got %q want %q", got, want)
	}
	if got, want := tab.Root, "main"; got != want {
		t.Errorf("tab root: got %q want %q", got, want)
	}
	if got, want := len(tab.Nodes), 3; got != want {
		t.Fatalf("nodes len: got %d want %d", got, want)
	}
	for _, node := range tab.Nodes {
		if node.Label != nil {
			t.Errorf("legacy node %q label = %q, want nil", node.ID, *node.Label)
		}
	}
}

// TestLoad_RejectsUnknownWorkspaceType fails fast on an invalid
// [[workspaces]].type value.
func TestLoad_RejectsUnknownWorkspaceType(t *testing.T) {
	t.Parallel()
	const doc = `
[[workspaces]]
name = "x"
path = "~/x"
type = "bogus"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid workspace type")
	}
}

// TestLoad_RejectsUnknownTemplateReference fails fast when a workspace,
// wildcard or defaults.template names a template that does not exist.
func TestLoad_RejectsUnknownTemplateReference(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
	}{
		{name: "workspace", doc: "[[workspaces]]\nname = \"x\"\npath = \"~/x\"\ntemplate = \"ghost\"\n"},
		{name: "wildcard", doc: "[[wildcards]]\npattern = \"*.go\"\ntemplate = \"ghost\"\n"},
		{name: "defaults", doc: "[defaults]\ntemplate = \"ghost\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected error for unknown template reference")
			}
		})
	}
}

// TestLoad_RejectsGroupWorkspaceWithInvalidSource fails fast when a group
// workspace's sources list names an unsupported source.
func TestLoad_RejectsGroupWorkspaceWithInvalidSource(t *testing.T) {
	t.Parallel()
	const doc = `
[[workspaces]]
name = "g"
path = "~/g"
type = "group"
source_order = ["projects", "roots"]
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for invalid group source")
	}
}

// TestParsePercent covers valid and invalid percentage strings.
func TestParsePercent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    float64
		wantOK  bool
		comment string
	}{
		{in: "60%", want: 0.6, wantOK: true},
		{in: "0%", want: 0, wantOK: true},
		{in: "100%", want: 1, wantOK: true},
		{in: "60", wantOK: false, comment: "missing % suffix"},
		{in: "abc%", wantOK: false, comment: "not a number"},
		{in: "150%", wantOK: false, comment: "out of range"},
		{in: "-10%", wantOK: false, comment: "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, ok := ParsePercent(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("%s: ok = %v want %v", tc.in, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("%s: got %v want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestLoad_RejectsInvalidTUIWidth fails fast on a malformed tui width value.
func TestLoad_RejectsInvalidTUIWidth(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[tui]\nlist_width = \"wide\"\n",
		"[tui]\npreview_width = \"200%\"\n",
	}
	for _, doc := range cases {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "config.toml")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("expected error for doc:\n%s", doc)
		}
	}
}

// TestExampleTOML_MatchesCanonicalModel exercises the generated config
// through Load and confirms no legacy/removed constructs and no leaked user
// paths.
func TestExampleTOML_MatchesCanonicalModel(t *testing.T) {
	t.Parallel()
	got := ExampleTOML()
	for _, want := range []string{
		"version = 2", "[general]", "source_order = [", "[defaults]", "type = ",
		"template = ", "[tui]", "list_width", "preview_width", `layout = "landscape"`, "[preview]",
		"[preview.commands.", "[sources.herdr]", "[sources.projects]",
		"markers = ", "[templates.default]", "[templates.k8s]", "[[sources.custom]]",
		`theme accepts "mocha", "macchiato", "frappe",`,
		`"latte", "plain", or "inherit". "inherit" delegates explicitly to the`,
		`Exact precedence is NO_COLOR > SHEP_THEME > explicit config theme`,
		`(except inherit) > Herdr theme > mocha.`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExampleTOML missing %q", want)
		}
	}
	for _, bad := range []string{"[layouts", "kind =", "provider_order", "/Users/", "Proyectos", "default = ["} {
		if strings.Contains(got, bad) {
			t.Errorf("ExampleTOML must not contain %q:\n%s", bad, got)
		}
	}

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("generated example must load cleanly: %v", err)
	}
	if !cfg.Ranking.Enabled {
		t.Error("example config must load with ranking.enabled = true")
	}
	// Load() normalizes an absent preview.default to the generic fallback list,
	// so nil/absent must be checked against the raw document, not the loaded
	// Config. Decoding into a bare map[string]any surfaces exactly what the
	// example document itself declared.
	var raw map[string]any
	if err := toml.Unmarshal([]byte(got), &raw); err != nil {
		t.Fatalf("decode raw example toml: %v", err)
	}
	if preview, ok := raw["preview"].(map[string]any); ok {
		if _, hasDefault := preview["default"]; hasDefault {
			t.Errorf("example config must not set [preview].default, got %v", preview["default"])
		}
	}
	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "herdr", got: cfg.Sources.Herdr.Preview, want: []string{PreviewWorkspace, PreviewActivePane, PreviewAgentStatus}},
		{name: "workspaces", got: cfg.Sources.Workspaces.Preview, want: []string{PreviewIdentity, PreviewDir}},
		{name: "zoxide", got: cfg.Sources.Zoxide.Preview, want: []string{PreviewIdentity, PreviewDir}},
		{name: "projects", got: cfg.Sources.Projects.Preview, want: []string{PreviewIdentity, PreviewGit, PreviewDir}},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("sources.%s.preview = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestTrackedExamplesConfigTOML_LoadsWithIntendedContract proves the tracked
// examples/config.toml (the practical first-run example README links to, as
// distinct from the canonical document ExampleTOML() generates for shep init)
// both parses and carries its documented contract: adaptive ranking on, a
// responsive TUI (no explicit layout) with the shipped 35/65 split, no
// [preview].default global override, and the exact shipped per-source preview
// lists spelled out per source.
func TestTrackedExamplesConfigTOML_LoadsWithIntendedContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "config.toml"))
	if err != nil {
		t.Fatalf("read tracked examples/config.toml: %v", err)
	}
	// Load with an explicit path never consults DiscoverPath, so loading the
	// isolated temporary copy exercises exactly this document and cannot touch
	// the operator's real HOME, XDG state, or ranking store.
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("tracked examples/config.toml must load cleanly: %v", err)
	}
	if !cfg.Ranking.Enabled {
		t.Error("examples/config.toml must enable ranking.enabled")
	}
	if cfg.TUI.Layout != "" {
		t.Errorf(`examples/config.toml must leave [tui].layout empty for the responsive default, got %q`, cfg.TUI.Layout)
	}
	if cfg.TUI.ListWidth != "35%" || cfg.TUI.PreviewWidth != "65%" {
		t.Errorf("examples/config.toml must ship the 35/65 split, got list_width %q preview_width %q", cfg.TUI.ListWidth, cfg.TUI.PreviewWidth)
	}
	// Load() normalizes an absent preview.default to the generic fallback list,
	// so the absence contract is pinned against the raw document bytes, not the
	// loaded Config.
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode raw examples/config.toml: %v", err)
	}
	if preview, ok := doc["preview"].(map[string]any); ok {
		if v, present := preview["default"]; present {
			t.Errorf("examples/config.toml must not set [preview].default, got %v", v)
		}
	}
	for _, tc := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "herdr", got: cfg.Sources.Herdr.Preview, want: []string{PreviewWorkspace, PreviewActivePane, PreviewAgentStatus}},
		{name: "workspaces", got: cfg.Sources.Workspaces.Preview, want: []string{PreviewIdentity, PreviewDir}},
		{name: "zoxide", got: cfg.Sources.Zoxide.Preview, want: []string{PreviewIdentity, PreviewDir}},
		{name: "projects", got: cfg.Sources.Projects.Preview, want: []string{PreviewIdentity, PreviewGit, PreviewDir}},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("examples/config.toml sources.%s.preview = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// TestLoad_RejectsTemplateNodeCycle confirms a cyclic node graph (a branch
// node reachable from itself through its children) fails Load fast instead
// of letting templates.Apply recurse forever splitting/creating Herdr panes.
func TestLoad_RejectsTemplateNodeCycle(t *testing.T) {
	t.Parallel()
	const doc = `
[[templates.dev.tabs]]
name = "code"
root = "a"

[[templates.dev.tabs.nodes]]
id = "a"
split = "rows"
children = ["b", "leaf"]

[[templates.dev.tabs.nodes]]
id = "b"
split = "rows"
children = ["a", "leaf2"]

[[templates.dev.tabs.nodes]]
id = "leaf"
command = ""

[[templates.dev.tabs.nodes]]
id = "leaf2"
command = ""
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for a cyclic template node graph")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should mention the cycle", err.Error())
	}
}

// TestLoad_AcceptsAcyclicDiamondSharedLeaf confirms two branches referencing
// the same leaf id is not itself flagged as a cycle (only a node reachable
// from its own ancestor chain is).
func TestLoad_AcceptsAcyclicDiamondSharedLeaf(t *testing.T) {
	t.Parallel()
	const doc = `
[[templates.dev.tabs]]
name = "code"
root = "main"

[[templates.dev.tabs.nodes]]
id = "main"
split = "rows"
children = ["a", "b"]

[[templates.dev.tabs.nodes]]
id = "a"
command = ""

[[templates.dev.tabs.nodes]]
id = "b"
command = ""
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected acyclic template to load cleanly, got %v", err)
	}
}

// TestLoad_RejectsUnknownAndLegacyKeys (requirement: strict config) fails
// fast on any unrecognised or legacy/removed key — top-level, nested tables,
// and arbitrary [sources.<name>] entries alike — instead of silently
// ignoring it.
func TestLoad_RejectsUnknownAndLegacyKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
	}{
		{name: "top-level source_order", doc: "source_order = [\"herdr\"]\n"},
		{name: "top-level discovery table", doc: "[discovery]\nfoo = 1\n"},
		{name: "top-level views table", doc: "[views]\nfoo = 1\n"},
		{name: "preview sections table", doc: "[preview.sections.identity]\nfoo = 1\n"},
		{name: "top-level preview_command", doc: "preview_command = \"ls\"\n"},
		{name: "top-level preview_sections", doc: "preview_sections = [\"identity\"]\n"},
		{name: "top-level default_sections", doc: "default_sections = [\"identity\"]\n"},
		{name: "workspace legacy view field", doc: "[[workspaces]]\nname = \"x\"\npath = \"~/x\"\nview = \"grid\"\n"},
		{name: "arbitrary sources table", doc: "[sources.foo]\nbar = 1\n"},
		{name: "top-level layouts table", doc: "[layouts]\nfoo = 1\n"},
		{name: "workspace legacy kind field", doc: "[[workspaces]]\nname = \"x\"\npath = \"~/x\"\nkind = \"shell\"\n"},
		{name: "top-level provider_order", doc: "provider_order = [\"herdr\"]\n"},
		{name: "template preview field", doc: "[templates.dev]\npreview = [\"identity\"]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected error for doc:\n%s", tc.doc)
			}
		})
	}
}

// TestMatchWildcard covers tilde expansion, "**" recursive segment matching
// (the documented "~/projects/kubernetes/**" pattern), plain single-segment
// globbing, and a malformed pattern degrading to "no match" rather than a
// crash.
func TestMatchWildcard(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory")
	}
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{
			name:    "double star matches direct child",
			pattern: "~/projects/kubernetes/**",
			path:    filepath.Join(home, "projects", "kubernetes", "myrepo"),
			want:    true,
		},
		{
			name:    "double star matches deeply nested descendant",
			pattern: "~/projects/kubernetes/**",
			path:    filepath.Join(home, "projects", "kubernetes", "myrepo", "sub", "dir"),
			want:    true,
		},
		{
			name:    "double star does not match sibling directory",
			pattern: "~/projects/kubernetes/**",
			path:    filepath.Join(home, "projects", "other", "myrepo"),
			want:    false,
		},
		{
			name:    "plain glob matches basename",
			pattern: "*.go",
			path:    "main.go",
			want:    true,
		},
		{
			name:    "malformed pattern is no match, not an error",
			pattern: "[",
			path:    "/p/foo",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchWildcard(tc.pattern, tc.path); got != tc.want {
				t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// TestLoad_RejectsTUIWidthSumOverflow confirms list_width + preview_width
// configured as percentages that sum past 100% fails Load fast rather than
// letting the picker render an overflowing layout.
func TestLoad_RejectsTUIWidthSumOverflow(t *testing.T) {
	t.Parallel()
	const doc = "[tui]\nlist_width = \"60%\"\npreview_width = \"60%\"\n"
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for list_width + preview_width summing past 100%")
	}
}

// TestLoad_AcceptsTUIWidthSumAtOrBelow100Percent confirms percentages that
// sum to exactly 100% (or less) are accepted; only exceeding 100% fails.
func TestLoad_AcceptsTUIWidthSumAtOrBelow100Percent(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[tui]\nlist_width = \"60%\"\npreview_width = \"40%\"\n",
		"[tui]\nlist_width = \"30%\"\npreview_width = \"30%\"\n",
	}
	for _, doc := range cases {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "config.toml")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err != nil {
			t.Errorf("doc:\n%s\nunexpected error: %v", doc, err)
		}
	}
}

// TestLoad_TUILayout_DefaultsEmptyAndAccepted confirms an absent
// [tui].layout parses to the empty string (Model.View treats empty the same
// as "landscape", the default) without failing validation.
func TestLoad_TUILayout_DefaultsEmptyAndAccepted(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("[tui]\nlist_width = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.TUI.Layout; got != "" {
		t.Errorf("tui.layout default: got %q, want empty", got)
	}
}

// TestLoad_AcceptsValidTUILayoutValues confirms the only documented layout
// value ("landscape"; empty means auto) parses and loads without error.
// "portrait" is no longer accepted — see TestLoad_RejectsPortraitTUILayout.
func TestLoad_AcceptsValidTUILayoutValues(t *testing.T) {
	t.Parallel()
	for _, val := range []string{"landscape"} {
		t.Run(val, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			doc := "[tui]\nlayout = \"" + val + "\"\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("load %q: %v", val, err)
			}
			if got := cfg.TUI.Layout; got != val {
				t.Errorf("tui.layout: got %q want %q", got, val)
			}
		})
	}
}

// TestLoad_RejectsPortraitTUILayout confirms the removed "portrait" layout is
// rejected at config validation with a clear error (the stacked/portrait
// layout was deleted; only wide and list-only modes remain).
func TestLoad_RejectsPortraitTUILayout(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	const doc = "[tui]\nlayout = \"portrait\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for the removed portrait tui.layout value")
	}
	if !strings.Contains(err.Error(), "portrait") {
		t.Errorf("error must name the removed value %q, got: %v", "portrait", err)
	}
}

// TestLoad_RejectsInvalidTUILayoutValue confirms an unknown [tui].layout
// value fails Load fast with an error naming the bad value, consistent with
// this project's established fail-fast convention (mirrors
// TestLoad_RejectsInvalidTUIWidth).
func TestLoad_RejectsInvalidTUILayoutValue(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	const doc = "[tui]\nlayout = \"diagonal\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid tui.layout value")
	}
	if !strings.Contains(err.Error(), "diagonal") {
		t.Errorf("error must name the bad value %q, got: %v", "diagonal", err)
	}
}

// TestLoad_TUITheme_DefaultsEmptyAndAccepted confirms an absent
// [tui].theme parses to the empty string (internal/tui.resolveTheme treats
// empty as "defer to $SHEP_THEME, then mocha") without failing validation.
func TestLoad_TUITheme_DefaultsEmptyAndAccepted(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("[tui]\nlist_width = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.TUI.Theme; got != "" {
		t.Errorf("tui.theme default: got %q, want empty", got)
	}
}

// TestLoad_AcceptsValidTUIThemeValues confirms every documented theme name
// (including "inherit") parses and loads without error.
func TestLoad_AcceptsValidTUIThemeValues(t *testing.T) {
	t.Parallel()
	for _, val := range []string{TUIThemeMocha, TUIThemeMacchiato, TUIThemeFrappe, TUIThemeLatte, TUIThemePlain, TUIThemeInherit} {
		t.Run(val, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			doc := "[tui]\ntheme = \"" + val + "\"\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("load %q: %v", val, err)
			}
			if got := cfg.TUI.Theme; got != val {
				t.Errorf("tui.theme: got %q want %q", got, val)
			}
		})
	}
}

// TestLoad_RejectsInvalidTUIThemeValue confirms an unknown [tui].theme
// value fails Load fast with an error naming the bad value.
func TestLoad_RejectsInvalidTUIThemeValue(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	const doc = "[tui]\ntheme = \"solarized\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid tui.theme value")
	}
	if !strings.Contains(err.Error(), "solarized") {
		t.Errorf("error must name the bad value %q, got: %v", "solarized", err)
	}
}

// TestLoad_TUIIcons_DefaultsEmptyAndAccepted confirms an absent [tui].icons
// parses to the empty string (internal/tui.resolveIconSet treats empty as
// "unicode", the picker's original hardcoded glyphs) without failing
// validation.
func TestLoad_TUIIcons_DefaultsEmptyAndAccepted(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("[tui]\nlist_width = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.TUI.Icons; got != "" {
		t.Errorf("tui.icons default: got %q, want empty", got)
	}
}

// TestLoad_AcceptsValidTUIIconsValues confirms every documented icon
// fallback tier name (unicode, ascii) parses and loads without error. The
// "nerd" tier was removed — see TestLoad_RejectsNerdTUIIcons.
func TestLoad_AcceptsValidTUIIconsValues(t *testing.T) {
	t.Parallel()
	for _, val := range []string{TUIIconsUnicode, TUIIconsASCII} {
		t.Run(val, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			path := filepath.Join(tmp, "config.toml")
			doc := "[tui]\nicons = \"" + val + "\"\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("load %q: %v", val, err)
			}
			if got := cfg.TUI.Icons; got != val {
				t.Errorf("tui.icons: got %q want %q", got, val)
			}
		})
	}
}

// TestLoad_RejectsNerdTUIIcons confirms the removed "nerd" icon tier is
// rejected at config validation with a clear error rather than silently
// falling back to another tier.
func TestLoad_RejectsNerdTUIIcons(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	const doc = "[tui]\nicons = \"nerd\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for the removed nerd tui.icons value")
	}
	if !strings.Contains(err.Error(), "nerd") {
		t.Errorf("error must name the removed value %q, got: %v", "nerd", err)
	}
}

// TestLoad_RejectsInvalidTUIIconsValue confirms an unknown [tui].icons value
// fails Load fast with an error naming the bad value.
func TestLoad_RejectsInvalidTUIIconsValue(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	const doc = "[tui]\nicons = \"emoji\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid tui.icons value")
	}
	if !strings.Contains(err.Error(), "emoji") {
		t.Errorf("error must name the bad value %q, got: %v", "emoji", err)
	}
}

// TestLoad_TemplateFocusTabNode_Parses confirms the new top-level focus schema
// `focus = { tab = "...", node = "..." }` parses into TemplateConfig.Focus.
// focus.tab refers to [[templates.<name>.tabs]].name; focus.node refers to a
// TemplateNode.ID scoped to that same tab.
func TestLoad_TemplateFocusTabNode_Parses(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
description = "development workspace"
focus = { tab = "AI", node = "opencode" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  split = "rows"
  children = ["editor", "terminal"]
  sizes = [80, 20]

  [[templates.dev.tabs.nodes]]
  id = "editor"
  command = "nvim"

  [[templates.dev.tabs.nodes]]
  id = "terminal"
  command = ""

[[templates.dev.tabs]]
name = "AI"
root = "opencode"

  [[templates.dev.tabs.nodes]]
  id = "opencode"
  command = "opencode"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tpl, ok := cfg.Templates["dev"]
	if !ok {
		t.Fatal("missing templates.dev")
	}
	if tpl.Focus == nil {
		t.Fatal("expected Focus non-nil")
	}
	if tpl.Focus.Tab != "AI" {
		t.Errorf("Focus.Tab = %q, want %q", tpl.Focus.Tab, "AI")
	}
	if tpl.Focus.Node != "opencode" {
		t.Errorf("Focus.Node = %q, want %q", tpl.Focus.Node, "opencode")
	}
}

// TestLoad_TemplateFocusTabOnly_Parses confirms focus.node may be omitted
// (focus just the tab, i.e. its root pane).
func TestLoad_TemplateFocusTabOnly_Parses(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
focus = { tab = "term" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"

[[templates.dev.tabs]]
name = "term"
root = "shell"

  [[templates.dev.tabs.nodes]]
  id = "shell"
  command = ""
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tpl := cfg.Templates["dev"]
	if tpl.Focus == nil || tpl.Focus.Tab != "term" || tpl.Focus.Node != "" {
		t.Fatalf("Focus = %+v, want {Tab:term Node:}", tpl.Focus)
	}
}

// TestLoad_TemplateFocusOmitted_DefaultsNil confirms a template without a
// focus field has Focus == nil (meaning: apply the default — first tab stays
// focused).
func TestLoad_TemplateFocusOmitted_DefaultsNil(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Templates["dev"].Focus != nil {
		t.Errorf("Focus = %+v, want nil when omitted", cfg.Templates["dev"].Focus)
	}
}

// TestLoad_TemplateFocusTabInvalid_Rejected confirms focus.tab referencing a
// non-existent tab name fails Load fast with a clear error.
func TestLoad_TemplateFocusTabInvalid_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
focus = { tab = "nope" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for focus.tab not matching any tab name")
	}
	if !strings.Contains(err.Error(), "focus.tab") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("error %q should name focus.tab and the bad value %q", err.Error(), "nope")
	}
}

// TestLoad_TemplateFocusNodeInvalid_Rejected confirms focus.node referencing a
// non-existent node id within the focused tab fails Load fast.
func TestLoad_TemplateFocusNodeInvalid_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
focus = { tab = "code", node = "ghost" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for focus.node not matching any node id in the tab")
	}
	if !strings.Contains(err.Error(), "focus.node") || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should name focus.node and the bad value %q", err.Error(), "ghost")
	}
}

// TestLoad_TemplateFocusNodeCrossTab_Rejected confirms focus.node is scoped to
// the focused tab ONLY: a node id that exists in a different tab but not in
// the focused tab must be rejected.
func TestLoad_TemplateFocusNodeCrossTab_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
focus = { tab = "AI", node = "editor" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"

[[templates.dev.tabs]]
name = "AI"
root = "ai"

  [[templates.dev.tabs.nodes]]
  id = "ai"
  command = "opencode"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: focus.node 'editor' exists in no tab named 'AI'")
	}
}

// TestLoad_TemplateNodeCloseOnExit_Parses confirms the close_on_exit field on
// a template node parses into TemplateNode.CloseOnExit.
func TestLoad_TemplateNodeCloseOnExit_Parses(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
  close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	node := cfg.Templates["dev"].Tabs[0].Nodes[0]
	if !node.CloseOnExit {
		t.Errorf("CloseOnExit = false, want true")
	}
}

// TestLoad_TemplateOldNodeFocus_Rejected confirms the removed per-node
// `focus = true` field is now rejected as an unknown field (the schema moved
// focus to the top-level [templates.<name>].focus table).
func TestLoad_TemplateOldNodeFocus_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
  focus = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: per-node focus = true is no longer a valid field")
	}
}

// TestLoad_TemplateOldTabFocus_Rejected confirms the removed per-tab
// `focus = true` field is now rejected as an unknown field.
func TestLoad_TemplateOldTabFocus_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]

[[templates.dev.tabs]]
name = "code"
root = "main"
focus = true

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: per-tab focus = true is no longer a valid field")
	}
}

// TestLoad_TemplateFocusNodeSetTabEmpty_Rejected confirms the existing
// validateTemplateFocus branch that rejects focus.node set while focus.tab
// is empty (ambiguous: node ids are scoped per-tab, so there is no tab to
// resolve the node against). This exercises the "focus.node %q set but
// focus.tab is empty" error path, which previously had no test coverage.
func TestLoad_TemplateFocusNodeSetTabEmpty_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
focus = { node = "main" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: focus.node set while focus.tab is empty")
	}
	if !strings.Contains(err.Error(), "focus.node") || !strings.Contains(err.Error(), "focus.tab is empty") {
		t.Errorf("error %q should name focus.node and explain focus.tab is empty", err.Error())
	}
}

// TestLoad_TemplateBranchWithCloseOnExit_Rejected confirms that a BRANCH
// node (Split/Children set) cannot also set close_on_exit: true. It is
// meaningless on a layout-only node (there is no command/pane to close), so
// Load must fail fast with a clear error naming the tab/node instead of
// silently accepting a no-op field.
func TestLoad_TemplateBranchWithCloseOnExit_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  split = "cols"
  children = ["a", "b"]
  close_on_exit = true

  [[templates.dev.tabs.nodes]]
  id = "a"
  command = "nvim"

  [[templates.dev.tabs.nodes]]
  id = "b"
  command = "htop"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: branch node cannot set close_on_exit")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "main") {
		t.Errorf("error %q should name close_on_exit and the node id %q", err.Error(), "main")
	}
	if !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "code") {
		t.Errorf("error %q should name the template %q and the tab %q", err.Error(), "dev", "code")
	}
}

// TestLoad_WorkspaceCloseOnExit_Parses confirms the close_on_exit field on a
// [[workspaces]] entry with a command parses into WorkspaceConfig.CloseOnExit.
func TestLoad_WorkspaceCloseOnExit_Parses(t *testing.T) {
	t.Parallel()
	const doc = `
[[workspaces]]
name = "yazi"
path = "~/Downloads"
command = "yazi"
close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(cfg.Workspaces))
	}
	if !cfg.Workspaces[0].CloseOnExit {
		t.Errorf("CloseOnExit = false, want true")
	}
}

// TestLoad_WorkspaceCloseOnExit_Group_Rejected confirms that a type=group
// workspace cannot set close_on_exit: true. A group is a nested picker source
// (no pane/command of its own), so close_on_exit would be a silent no-op.
// Load must fail fast, mirroring the branch-node close_on_exit rejection.
func TestLoad_WorkspaceCloseOnExit_Group_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[[workspaces]]
name = "projects"
type = "group"
path = "~/projects"
source_order = ["projects"]
close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: group workspace cannot set close_on_exit")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "projects") {
		t.Errorf("error %q should name close_on_exit and the workspace name %q", err.Error(), "projects")
	}
}

// TestLoad_WorkspaceCloseOnExit_WithTemplate_Rejected confirms that a
// workspace with template set cannot also set close_on_exit: true. Per-tab
// close-on-exit is already the node-level feature owned by the template, so a
// workspace-level close_on_exit would be ambiguous/ignored. Load fails fast.
func TestLoad_WorkspaceCloseOnExit_WithTemplate_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
command = "nvim"

[[workspaces]]
name = "app"
path = "~/projects/app"
template = "dev"
close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: workspace with template cannot set close_on_exit")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "app") {
		t.Errorf("error %q should name close_on_exit and the workspace name %q", err.Error(), "app")
	}
}

// TestLoad_WorkspaceCloseOnExit_EmptyCommand_Rejected confirms that a
// workspace cannot set close_on_exit: true with an empty command. Without a
// command there is nothing whose exit closes the pane, so it would silently
// never trigger — mirroring the leaf-node empty-command rule.
func TestLoad_WorkspaceCloseOnExit_EmptyCommand_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[[workspaces]]
name = "shell"
path = "~/projects/shell"
close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: workspace with close_on_exit=true but empty command")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "shell") {
		t.Errorf("error %q should name close_on_exit and the workspace name %q", err.Error(), "shell")
	}
}

// TestLoad_TemplateConfigCloseOnExit_Parses confirms the close_on_exit field
// on a top-level [templates.<name>] with a command parses into
// TemplateConfig.CloseOnExit.
func TestLoad_TemplateConfigCloseOnExit_Parses(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.k9s]
command = "k9s"
close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tpl, ok := cfg.Templates["k9s"]
	if !ok {
		t.Fatal("template k9s missing")
	}
	if !tpl.CloseOnExit {
		t.Errorf("CloseOnExit = false, want true")
	}
}

// TestLoad_TemplateConfigCloseOnExit_WithTabs_Rejected confirms that a
// top-level [templates.<name>] cannot set close_on_exit: true together with
// tabs. Per-tab/per-pane close-on-exit is already the node-level feature, so
// a template-level close_on_exit over tabs would be ambiguous. Load fails
// fast, mirroring the workspace template rejection.
func TestLoad_TemplateConfigCloseOnExit_WithTabs_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
close_on_exit = true

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  command = "nvim"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: top-level template with close_on_exit and tabs")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "dev") {
		t.Errorf("error %q should name close_on_exit and the template %q", err.Error(), "dev")
	}
}

// TestLoad_TemplateConfigCloseOnExit_EmptyCommand_Rejected confirms that a
// top-level [templates.<name>] cannot set close_on_exit: true without a
// command. Without a command there is nothing whose exit closes the pane, so
// it would silently never trigger — mirroring the leaf-node rule.
func TestLoad_TemplateConfigCloseOnExit_EmptyCommand_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]
close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: top-level template with close_on_exit but no command")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "dev") {
		t.Errorf("error %q should name close_on_exit and the template %q", err.Error(), "dev")
	}
}

// TestLoad_TemplateLeafCloseOnExitEmptyCommand_Rejected confirms that a LEAF
// node cannot set close_on_exit: true without a non-empty command. Without a
// command there is nothing whose exit closes the pane, so it would silently
// never trigger (the pane stays open forever) — contradicting the documented
// contract. Load must fail fast instead of allowing this silent no-op,
// consistent with the project's "fail fast, no silent footguns" convention.
func TestLoad_TemplateLeafCloseOnExitEmptyCommand_Rejected(t *testing.T) {
	t.Parallel()
	const doc = `
[templates.dev]

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  close_on_exit = true
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error: leaf node with close_on_exit=true but empty command")
	}
	if !strings.Contains(err.Error(), "close_on_exit") || !strings.Contains(err.Error(), "main") {
		t.Errorf("error %q should name close_on_exit and the node id %q", err.Error(), "main")
	}
	if !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "code") {
		t.Errorf("error %q should name the template %q and the tab %q", err.Error(), "dev", "code")
	}
}

func TestLoad_WorkspaceNameFieldsAndOrderedWildcardSelector(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[general]
workspace_name = '{{ .Path | osBase }}'

[[wildcards]]
pattern = "**/services/*"
workspace_name = "first"

[[wildcards]]
pattern = "**/services/platform-*"
workspace_name = "second"

[[workspaces]]
name = "explicit"
path = "/srv/services/platform-api"
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := cfg.General.WorkspaceName, "{{ .Path | osBase }}"; got != want {
		t.Errorf("general.workspace_name = %q, want %q", got, want)
	}
	if got, want := cfg.Wildcards[0].WorkspaceName, "first"; got != want {
		t.Errorf("wildcards[0].workspace_name = %q, want %q", got, want)
	}
	if got, ok := FirstMatchingWildcard(cfg.Wildcards, "/srv/services/platform-api"); !ok || got.WorkspaceName != "first" {
		t.Fatalf("first wildcard = (%+v, %v), want first match", got, ok)
	}
	if got, ok := FirstMatchingWildcard(cfg.Wildcards, "/srv/other"); ok || got.WorkspaceName != "" {
		t.Fatalf("unmatched wildcard = (%+v, %v), want no match", got, ok)
	}
}

func TestLoad_RejectsInvalidWorkspaceNameFieldsWithScope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{name: "general parse", doc: "[general]\nworkspace_name = \"{{ .Unknown }}\"\n", want: "general.workspace_name"},
		{name: "wildcard execute", doc: "[[wildcards]]\npattern = \"**\"\nworkspace_name = " + strconv.Quote(`{{ mustRegexMatch "[" .Path }}`) + "\n", want: "wildcards[0].workspace_name"},
		{name: "wildcard blank", doc: "[[wildcards]]\npattern = \"**\"\nworkspace_name = " + strconv.Quote(`{{ "   " }}`) + "\n", want: "wildcards[0].workspace_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load error = %v, want field scope %q", err, tc.want)
			}
		})
	}
}

func TestConfig_AgentsSource_DefaultsAndLoad(t *testing.T) {
	t.Parallel()

	// 1. Defaults: general.source_order does NOT include agents
	cfg := Defaults()
	for _, s := range cfg.General.SourceOrder {
		if s == SourceAgents {
			t.Errorf("default source_order contains %q, want excluded", SourceAgents)
		}
	}
	if got := cfg.Sources.Agents.Icon; got != "" {
		t.Errorf("agents icon = %q, want empty by default", got)
	}
	if got, want := cfg.Sources.Agents.LabelFormat, "{{.Label}}"; got != want {
		t.Errorf("agents label_format = %q, want %q", got, want)
	}

	// 2. Load with valid [sources.agents] and source_order containing "agents"
	const doc = `
[general]
source_order = ["agents", "herdr"]

[sources.agents]
icon = "🤖 "
label_format = "{{.Label}} [{{.AgentStatus}}]"
preview = ["identity", "active_pane"]
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load valid agents config: %v", err)
	}
	if got, want := loaded.General.SourceOrder, []string{SourceAgents, SourceHerdr}; !reflect.DeepEqual(got, want) {
		t.Errorf("source_order = %v, want %v", got, want)
	}
	if got, want := loaded.Sources.Agents.Icon, "🤖 "; got != want {
		t.Errorf("sources.agents.icon = %q, want %q", got, want)
	}
	if got, want := loaded.Sources.Agents.LabelFormat, "{{.Label}} [{{.AgentStatus}}]"; got != want {
		t.Errorf("sources.agents.label_format = %q, want %q", got, want)
	}
	if got, want := loaded.Sources.Agents.Preview, []string{"identity", "active_pane"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sources.agents.preview = %v, want %v", got, want)
	}
}

func TestConfig_AgentsSource_RejectsInvalid(t *testing.T) {
	t.Parallel()

	// Invalid label format
	const docBadLabel = `
[sources.agents]
label_format = "{{ .InvalidSyntax "
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad_label.toml")
	if err := os.WriteFile(path, []byte(docBadLabel), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "sources.agents.label_format") {
		t.Errorf("expected error naming sources.agents.label_format, got %v", err)
	}

	// Invalid preview section
	const docBadPreview = `
[sources.agents]
preview = ["nonexistent_section"]
`
	path2 := filepath.Join(tmp, "bad_preview.toml")
	if err := os.WriteFile(path2, []byte(docBadPreview), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path2); err == nil || !strings.Contains(err.Error(), "sources.agents.preview") {
		t.Errorf("expected error naming sources.agents.preview, got %v", err)
	}
}

func TestConfig_AgentsSource_GroupRequiresRoot(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "omitted path"},
		{name: "blank path", path: `path = "   "`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			doc := "[[workspaces]]\nname = \"agents-group\"\ntype = \"group\"\nsource_order = [\"agents\"]\n" + tt.path + "\n"
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "workspaces[0]") || !strings.Contains(err.Error(), "path") {
				t.Fatalf("Load error = %v, want indexed group path error", err)
			}
		})
	}
}

func TestConfig_AgentsSource_GroupWorkspaceSourceOrder(t *testing.T) {
	t.Parallel()

	const doc = `
[[workspaces]]
name = "agents-group"
type = "group"
path = "~/agents"
source_order = ["agents", "herdr"]
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "group_agents.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load group workspace with agents: %v", err)
	}
	if len(cfg.Workspaces) != 1 {
		t.Fatalf("workspaces count = %d, want 1", len(cfg.Workspaces))
	}
	if got, want := cfg.Workspaces[0].SourceOrder, []string{SourceAgents, SourceHerdr}; !reflect.DeepEqual(got, want) {
		t.Errorf("group source_order = %v, want %v", got, want)
	}
}

func TestLoad_TabsValidation(t *testing.T) {
	cases := []struct {
		name, doc, wantErr string
		want               []string
	}{
		{name: "default", want: []string{"all", "agents"}},
		{name: "empty", doc: "[tui]\ntabs = []\n", want: []string{"all", "agents"}},
		{name: "ordered", doc: "[tui]\ntabs = [\"projects\", \"all\", \"agents\"]\n", want: []string{"projects", "all", "agents"}},
		{name: "custom source and group", doc: "[tui]\ntabs = [\"review\", \"team\"]\n[[sources.custom]]\nname = \"review\"\ncommand = [\"echo\"]\n[[workspaces]]\nid = \"team\"\nname = \"Team\"\ntype = \"group\"\npath = \"~/team\"\n", want: []string{"review", "team"}},
		{name: "unknown", doc: "[tui]\ntabs = [\"missing\"]\n", wantErr: "tui.tabs"},
		{name: "duplicate", doc: "[tui]\ntabs = [\"all\", \"all\"]\n", wantErr: "duplicate"},
		{name: "non group", doc: "[tui]\ntabs = [\"team\"]\n[[workspaces]]\nid = \"team\"\nname = \"Team\"\npath = \"~/team\"\n", wantErr: "group"},
		{name: "ambiguous IDs", doc: "[tui]\ntabs = [\"team\"]\n[[workspaces]]\nid = \"team\"\nname = \"One\"\ntype = \"group\"\npath = \"~/one\"\n[[workspaces]]\nid = \"team\"\nname = \"Two\"\ntype = \"group\"\npath = \"~/two\"\n", wantErr: "ambiguous"},
		{name: "custom source collision", doc: "[tui]\ntabs = [\"review\"]\n[[sources.custom]]\nname = \"review\"\ncommand = [\"echo\"]\n[[workspaces]]\nid = \"review\"\nname = \"Team\"\ntype = \"group\"\npath = \"~/team\"\n", wantErr: "ambiguous"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.TUI.Tabs, tc.want) {
				t.Fatalf("tabs = %v, want %v", cfg.TUI.Tabs, tc.want)
			}
		})
	}
}
