package effective

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/source"
)

func strPtr(s string) *string { return &s }

// realDir returns a fresh directory with its symlinks resolved (on macOS
// the temporary directory lives behind /var -> /private/var).
func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// presentationField is one of the five presentation keys, read from a
// config table and from a resolved presentation.
type presentationField struct {
	name string
	set  func(p *config.Presentation, v string)
	get  func(p source.Presentation) string
	def  func(p config.RowPresentation) string
}

var presentationFields = []presentationField{
	{"icon", func(p *config.Presentation, v string) { p.Icon = &v }, func(p source.Presentation) string { return p.Icon }, func(p config.RowPresentation) string { return p.Icon }},
	{"icon_color", func(p *config.Presentation, v string) { p.IconColor = &v }, func(p source.Presentation) string { return p.IconColor }, func(p config.RowPresentation) string { return p.IconColor }},
	{"label_format", func(p *config.Presentation, v string) { p.LabelFormat = &v }, func(p source.Presentation) string { return p.Label }, func(p config.RowPresentation) string { return p.Label }},
	{"detail_format", func(p *config.Presentation, v string) { p.DetailFormat = &v }, func(p source.Presentation) string { return p.Detail }, func(p config.RowPresentation) string { return p.Detail }},
	{"marker_format", func(p *config.Presentation, v string) { p.MarkerFormat = &v }, func(p source.Presentation) string { return p.Marker }, func(p config.RowPresentation) string { return p.Marker }},
}

// TestFor_PresentationPrecedence is the precedence matrix of every
// presentation key: the candidate's own [[workspaces]] entry > the first
// matching wildcard that defines the key > the source's table > the
// built-in default. Each tier is removed in turn to expose the next; the
// other entries in the candidate's directory never apply.
func TestFor_PresentationPrecedence(t *testing.T) {
	t.Parallel()
	dir := realDir(t)
	defaults := config.DefaultPresentations("")
	type tiers struct{ own, entries, wildcards, source bool }
	all := tiers{true, true, true, true}
	for _, field := range presentationFields {
		t.Run(field.name, func(t *testing.T) {
			t.Parallel()
			build := func(on tiers) *config.Config {
				cfg := config.Defaults()
				if on.source {
					field.set(&cfg.Sources.Zoxide.Presentation, "source")
					field.set(&cfg.Sources.Workspaces.Presentation, "source")
				} else {
					cfg.Sources.Zoxide.Presentation = config.Presentation{}
				}
				if on.wildcards {
					// The first match defines nothing and must not stop the
					// scan; the second defines the key; the third is too late.
					first := config.WildcardConfig{Pattern: dir}
					second := config.WildcardConfig{Pattern: filepath.Dir(dir) + "/*"}
					field.set(&second.Presentation, "wildcard")
					third := config.WildcardConfig{Pattern: "**"}
					field.set(&third.Presentation, "late wildcard")
					cfg.Wildcards = []config.WildcardConfig{first, second, third}
				}
				if on.entries {
					// The first entry in the directory defines nothing.
					other := config.WorkspaceConfig{Name: "other", Path: dir}
					field.set(&other.Presentation, "entry")
					later := config.WorkspaceConfig{Name: "later", Path: dir}
					field.set(&later.Presentation, "later entry")
					cfg.Workspaces = append(cfg.Workspaces, config.WorkspaceConfig{Name: "silent", Path: dir}, other, later)
				}
				own := config.WorkspaceConfig{Name: "own", Path: dir}
				if on.own {
					field.set(&own.Presentation, "own")
				}
				cfg.Workspaces = append(cfg.Workspaces, own)
				return cfg
			}
			zoxide := source.Candidate{Path: dir, Label: dir, Source: config.SourceZoxide}
			for _, tc := range []struct {
				name string
				on   tiers
				row  string // a workspaces row by entry name; "" for zoxide
				want string
			}{
				{"own entry", all, "own", "own"},
				{"directory entries never draw other rows", all, "", "wildcard"},
				{"a workspaces row never takes a sibling entry", tiers{false, true, true, true}, "own", "wildcard"},
				{"first defining wildcard", tiers{false, false, true, true}, "", "wildcard"},
				{"source table", tiers{false, false, false, true}, "", "source"},
				{"built-in default", tiers{false, false, false, false}, "", field.def(defaults.Zoxide)},
			} {
				cfg := build(tc.on)
				cand := zoxide
				if tc.row != "" {
					cand = workspacesRow(cfg, tc.row)
				}
				if got := field.get(New(cfg).For(cand).Presentation); got != tc.want {
					t.Errorf("%s: %s = %q, want %q", tc.name, field.name, got, tc.want)
				}
			}
		})
	}
}

// workspacesRow returns the row the workspaces source makes from cfg's
// entry name.
func workspacesRow(cfg *config.Config, name string) source.Candidate {
	for _, ws := range cfg.Workspaces {
		if ws.Name == name {
			return source.Candidate{
				Path: ws.Path, Label: ws.Name, Source: config.SourceWorkspaces,
				Meta: map[string]string{"workspace_name": ws.Name, "entry_id": source.WorkspaceEntryIdentity(ws.Path, ws)},
			}
		}
	}
	panic("no entry " + name)
}

// TestFor_ExplicitEmptyWildcardValueOverridesTheSource proves an explicit ""
// defines a key: a wildcard can blank a part its source sets.
func TestFor_ExplicitEmptyWildcardValueOverridesTheSource(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources.Projects.Icon = strPtr("P ")
	cfg.Sources.Projects.MarkerFormat = strPtr("{{ .Branch }}")
	cfg.Wildcards = []config.WildcardConfig{{Pattern: "/work/**", Presentation: config.Presentation{Icon: strPtr(""), MarkerFormat: strPtr("")}}}
	got := New(cfg).For(source.Candidate{Path: "/work/api", Source: config.SourceProjects}).Presentation
	if got.Icon != "" || got.Marker != "" {
		t.Errorf("icon = %q, marker = %q; want both blanked by the wildcard", got.Icon, got.Marker)
	}
	if want := cfg.Presentations().Projects.Label; got.Label != want {
		t.Errorf("label = %q, want the source's %q (the wildcard does not set it)", got.Label, want)
	}
}

// TestFor_WildcardWithoutTheSettingDoesNotStopTheScan proves the unified
// rule for the settings wildcards had as "first match wins even when it sets
// nothing": preview, template and workspace_name are each taken from the
// first matching wildcard that defines them.
func TestFor_WildcardWithoutTheSettingDoesNotStopTheScan(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Templates["k8s"] = config.TemplateConfig{Command: "k9s"}
	cfg.General.WorkspaceName = "{{ .Path | base }}"
	cfg.Wildcards = []config.WildcardConfig{
		{Pattern: "/srv/**"},
		{Pattern: "/srv/services/*", Preview: []string{config.PreviewGit}},
		{Pattern: "platform-*", Template: "k8s"},
		{Pattern: "**", WorkspaceName: "svc-{{ .Path | base }}", Preview: []string{config.PreviewDir}, Template: "default"},
	}
	got := New(cfg).For(source.Candidate{Path: "/srv/services/platform-api", Source: config.SourceProjects})
	if !reflect.DeepEqual(got.Preview, []string{config.PreviewGit}) {
		t.Errorf("preview = %v, want the second wildcard's", got.Preview)
	}
	if got.Template != "k8s" {
		t.Errorf("template = %q, want the third wildcard's (matched by base name)", got.Template)
	}
	if got.WorkspaceName != "svc-{{ .Path | base }}" {
		t.Errorf("workspace_name = %q, want the last wildcard's", got.WorkspaceName)
	}
	other := New(cfg).For(source.Candidate{Path: "/home/me/notes", Source: config.SourceZoxide})
	if other.WorkspaceName != "svc-{{ .Path | base }}" || other.Template != "default" {
		t.Errorf("unmatched by the narrow patterns: %+v, want the catch-all's", other)
	}
}

// TestFor_PreviewPrecedence covers the preview chain: own entry > directory
// entries > wildcards > source > session_info for sessions >
// [preview].default > identity, with an explicit empty list defining the
// setting and sessions skipping the directory tiers.
func TestFor_PreviewPrecedence(t *testing.T) {
	t.Parallel()
	dir := realDir(t)
	cfg := config.Defaults()
	cfg.Sources.Zoxide.Preview = []string{config.PreviewDir}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "a", Path: dir, Preview: []string{"entry-a"}},
		{Name: "b", Path: dir},
	}
	cfg.Wildcards = []config.WildcardConfig{{Pattern: dir + "/*", Preview: []string{}}, {Pattern: "**", Preview: []string{"all"}}}
	r := New(cfg)
	cases := []struct {
		name string
		cand source.Candidate
		want []string
	}{
		{"own entry", workspacesRow(cfg, "a"), []string{"entry-a"}},
		{"own entry without a list takes the wildcard, not its sibling", workspacesRow(cfg, "b"), []string{"all"}},
		{"directory entry", source.Candidate{Path: dir, Source: config.SourceZoxide}, []string{"entry-a"}},
		{"explicit empty wildcard list", source.Candidate{Path: dir + "/sub", Source: config.SourceZoxide}, []string{}},
		{"sessions skip the directory tiers", source.Candidate{Path: dir, Source: config.SourceSessions}, []string{config.PreviewSessionInfo}},
	}
	for _, tc := range cases {
		if got := r.For(tc.cand).Preview; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: preview = %#v, want %#v", tc.name, got, tc.want)
		}
	}
	cfg.Wildcards = nil
	if got := New(cfg).For(source.Candidate{Path: "/elsewhere", Source: config.SourceZoxide}).Preview; !reflect.DeepEqual(got, []string{config.PreviewDir}) {
		t.Errorf("source list: preview = %v, want [dir]", got)
	}
	bare := &config.Config{Preview: config.PreviewConfig{Default: []string{config.PreviewGit}}}
	if got := New(bare).For(source.Candidate{Path: "/x", Source: config.SourceZoxide}).Preview; !reflect.DeepEqual(got, []string{config.PreviewGit}) {
		t.Errorf("preview.default: preview = %v, want [git]", got)
	}
	if got := New(&config.Config{}).For(source.Candidate{Path: "/x", Source: "prs"}).Preview; !reflect.DeepEqual(got, []string{config.PreviewIdentity}) {
		t.Errorf("fallback: preview = %v, want [identity]", got)
	}
}

// TestFor_TemplatePrecedence covers what a freshly created workspace runs:
// the candidate's own template, then its own command, then its group's
// template, then the first wildcard naming one, then [defaults].template.
// The entries in its directory never apply, and a name without a
// [templates.<name>] defines nothing.
func TestFor_TemplatePrecedence(t *testing.T) {
	t.Parallel()
	dir := realDir(t)
	cfg := &config.Config{
		Templates: map[string]config.TemplateConfig{"own": {}, "group": {}, "entry": {}, "wild": {}, "def": {}},
		Defaults:  config.DefaultsConfig{Template: "def"},
		Workspaces: []config.WorkspaceConfig{
			{Name: "grp", Path: dir, Type: config.WorkspaceTypeGroup, Template: "group"},
			{Name: "cmd", Path: dir, Command: "nvim", CloseOnExit: true},
			{Name: "tpl", Path: dir, Template: "entry"},
		},
		Wildcards: []config.WildcardConfig{{Pattern: "**", Template: "missing"}, {Pattern: "**", Template: "wild"}},
	}
	r := New(cfg)
	type launch struct {
		template, command string
		closeOnExit       bool
	}
	at := func(meta map[string]string) source.Candidate {
		return source.Candidate{Path: dir, Source: config.SourceZoxide, Meta: meta}
	}
	cases := []struct {
		name string
		cand source.Candidate
		want launch
	}{
		{"own template", at(map[string]string{"template": "own", "command": "x", "parent_template": "group"}), launch{template: "own"}},
		{"unknown own template falls through to the own command", at(map[string]string{"template": "nope", "command": "htop"}), launch{command: "htop"}},
		{"own command", at(map[string]string{"command": "htop", "close_on_exit": "true"}), launch{command: "htop", closeOnExit: true}},
		{"group template over the directory and wildcards", at(map[string]string{"parent_template": "group"}), launch{template: "group"}},
		{"directory entries never launch other rows", at(nil), launch{template: "wild"}},
		{"wildcard", source.Candidate{Path: "/elsewhere", Source: config.SourceZoxide}, launch{template: "wild"}},
		{"a workspaces row never takes a sibling's", workspacesRow(cfg, "tpl"), launch{template: "wild"}},
	}
	for _, tc := range cases {
		s := r.For(tc.cand)
		if got := (launch{s.Template, s.Command, s.CloseOnExit}); got != tc.want {
			t.Errorf("%s: launch = %+v, want %+v", tc.name, got, tc.want)
		}
	}
	cfg.Wildcards = nil
	if got := New(cfg).For(source.Candidate{Path: "/elsewhere", Source: config.SourceZoxide}).Template; got != "def" {
		t.Errorf("defaults.template: template = %q, want def", got)
	}
}

// TestFor_EntriesSharingADirectoryOnlyLendTheirPreview models several
// [[workspaces]] entries over one checkout (a group, a command entry and a
// template entry) and a command entry over ~/Downloads: a zoxide row in
// either directory gets neither an entry's command or template nor its
// presentation — opening the directory must not run "elliot start" or yazi —
// but does get the first entry's preview that defines one. Each entry's own
// row keeps its own launch and presentation.
func TestFor_EntriesSharingADirectoryOnlyLendTheirPreview(t *testing.T) {
	t.Parallel()
	ecorp := realDir(t)
	downloads := realDir(t)
	cfg := config.Defaults()
	cfg.Templates["k8s"] = config.TemplateConfig{Command: "k9s"}
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "Kubernetes", Path: ecorp, Type: config.WorkspaceTypeGroup, SourceOrder: []string{config.SourceProjects}, Template: "k8s"},
		{Name: "Elliot", Path: ecorp, Command: "elliot start", CloseOnExit: true, Preview: []string{config.PreviewGit},
			Presentation: config.Presentation{Icon: strPtr("C "), LabelFormat: strPtr("elliot")}},
		{Name: "k8s-ecorp", Path: ecorp, Template: "k8s", Preview: []string{config.PreviewDir},
			Presentation: config.Presentation{Icon: strPtr("K ")}},
		{Name: "Downloads", Path: downloads, Command: "yazi", Presentation: config.Presentation{Icon: strPtr("Y ")}},
	}
	r := New(cfg)
	zoxide := cfg.Presentations().Zoxide
	for _, dir := range []string{ecorp, downloads} {
		s := r.For(source.Candidate{Path: dir, Label: dir, Source: config.SourceZoxide})
		if s.Command != "" || s.CloseOnExit || s.Template != cfg.Defaults.Template {
			t.Errorf("zoxide row in %s launches %+v, want only [defaults].template %q", dir, s, cfg.Defaults.Template)
		}
		if got := s.Presentation; got.Icon != zoxide.Icon || got.Label != zoxide.Label {
			t.Errorf("zoxide row in %s draws %+v, want the zoxide presentation", dir, got)
		}
	}
	if got := r.For(source.Candidate{Path: ecorp, Source: config.SourceZoxide}).Preview; !reflect.DeepEqual(got, []string{config.PreviewGit}) {
		t.Errorf("zoxide row preview = %v, want the first entry defining one ([git])", got)
	}

	// The workspaces source carries each entry's own launch in Meta.
	elliot := workspacesRow(cfg, "Elliot")
	elliot.Meta["command"], elliot.Meta["close_on_exit"] = "elliot start", "true"
	if s := r.For(elliot); s.Command != "elliot start" || !s.CloseOnExit || s.Presentation.Icon != "C " || s.Presentation.Label != "elliot" {
		t.Errorf("Elliot's own row = %+v, want its command and presentation", s)
	}
	k8s := workspacesRow(cfg, "k8s-ecorp")
	k8s.Meta["template"] = "k8s"
	if s := r.For(k8s); s.Template != "k8s" || s.Presentation.Icon != "K " || !reflect.DeepEqual(s.Preview, []string{config.PreviewDir}) {
		t.Errorf("k8s-ecorp's own row = %+v, want its template, icon and preview", s)
	}
}

// TestFor_SymlinkedPathResolvesTheSameInEveryTab proves a candidate resolves
// identically in its source's own tab (raw path) and in the deduplicated all
// tab (normalized path set), and that patterns and entries written against
// either spelling of the directory apply.
func TestFor_SymlinkedPathResolvesTheSameInEveryTab(t *testing.T) {
	t.Parallel()
	root := realDir(t)
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	zoxideIcon := config.DefaultPresentations("").Zoxide.Icon
	for _, tc := range []struct {
		name     string
		edit     func(cfg *config.Config)
		wantIcon string
	}{
		{"pattern on the target", func(cfg *config.Config) {
			cfg.Wildcards = []config.WildcardConfig{{Pattern: real, Presentation: config.Presentation{Icon: strPtr("W")}, Preview: []string{"w"}}}
		}, "W"},
		{"pattern on the link", func(cfg *config.Config) {
			cfg.Wildcards = []config.WildcardConfig{{Pattern: link, Presentation: config.Presentation{Icon: strPtr("W")}, Preview: []string{"w"}}}
		}, "W"},
		// An entry's preview describes its directory; its icon is its own.
		{"entry on the link", func(cfg *config.Config) {
			cfg.Workspaces = []config.WorkspaceConfig{{Name: "e", Path: link, Presentation: config.Presentation{Icon: strPtr("W")}, Preview: []string{"w"}}}
		}, zoxideIcon},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.Defaults()
			tc.edit(cfg)
			r := New(cfg)
			sourceTab := source.Candidate{Path: link, Label: "~/link", Source: config.SourceZoxide}
			allTab := sourceTab
			allTab.NormalizedPath = real
			a, b := r.For(sourceTab), r.For(allTab)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("source tab %+v\nall tab    %+v\nwant identical", a, b)
			}
			if a.Presentation.Icon != tc.wantIcon || !reflect.DeepEqual(a.Preview, []string{"w"}) || a.NormalizedPath != real {
				t.Errorf("settings = %+v, want icon %q, preview [w] and normalized path %q", a, tc.wantIcon, real)
			}
		})
	}
}

// TestResolver_ReadsTheFilesystemOncePerPath proves normalization and
// directory identity are cached per path, and that a configuration without
// entries or wildcards resolves presentations without touching the disk.
func TestResolver_ReadsTheFilesystemOncePerPath(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := map[string]int{}
	count := func(kind, path string) {
		mu.Lock()
		calls[kind+" "+path]++
		mu.Unlock()
	}
	normalize := func(p string) (string, error) { count("normalize", p); return pathutil.Normalize(p) }
	stat := func(p string) (os.FileInfo, error) { count("stat", p); return os.Stat(p) }

	bare := newResolver(config.Defaults(), normalize, stat)
	for range 3 {
		bare.Attach([]source.Candidate{{Path: "/a", Source: config.SourceZoxide}})
	}
	if len(calls) != 0 {
		t.Fatalf("presentation without overrides read the filesystem: %v", calls)
	}

	dir := realDir(t)
	entriesOnly := config.Defaults()
	entriesOnly.Workspaces = []config.WorkspaceConfig{{Name: "e", Path: dir}}
	onlyEntries := newResolver(entriesOnly, normalize, stat)
	clear(calls)
	onlyEntries.Attach([]source.Candidate{{Path: dir, Source: config.SourceZoxide}})
	if len(calls) != 0 {
		t.Fatalf("presentation with entries but no wildcards read the filesystem: %v", calls)
	}

	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "e", Path: dir}}
	cfg.Wildcards = []config.WildcardConfig{{Pattern: "**"}}
	r := newResolver(cfg, normalize, stat)
	clear(calls)
	cand := source.Candidate{Path: dir + "/sub", Source: config.SourceZoxide}
	for range 3 {
		r.For(cand)
		r.Attach([]source.Candidate{cand})
	}
	for key, n := range calls {
		if n != 1 {
			t.Errorf("%s ran %d times, want once", key, n)
		}
	}
	if calls["normalize "+dir+"/sub"] != 1 {
		t.Errorf("calls = %v, want the candidate path normalized once", calls)
	}
}

// TestResolver_ConcurrentUse resolves from many goroutines at once (run with
// -race).
func TestResolver_ConcurrentUse(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Wildcards = []config.WildcardConfig{{Pattern: "/srv/**", Presentation: config.Presentation{Icon: strPtr("S")}}}
	r := New(cfg)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				c := source.Candidate{Path: fmt.Sprintf("/srv/p%d", (g*200+i)%50), Source: config.SourceProjects}
				if got := r.For(c).Presentation.Icon; got != "S" {
					t.Errorf("icon = %q, want S", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestResolver_Workspace returns the entry a workspaces row was made from:
// by identity, the name breaking a tie between identical entries, else by
// name within the row's directory.
func TestResolver_Workspace(t *testing.T) {
	t.Parallel()
	dir := realDir(t)
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "first", Path: dir, Type: config.WorkspaceTypeGroup, SourceOrder: []string{config.SourceProjects}},
		{Name: "second", Path: dir, Type: config.WorkspaceTypeGroup, SourceOrder: []string{config.SourceProjects}},
	}
	r := New(cfg)
	if ws, ok := r.Workspace(workspacesRow(cfg, "second")); !ok || ws.Name != "second" {
		t.Errorf("by identity = (%+v, %v), want second", ws, ok)
	}
	byName := source.Candidate{Path: dir, Label: "second", Source: config.SourceWorkspaces}
	if ws, ok := r.Workspace(byName); !ok || ws.Name != "second" {
		t.Errorf("by name = (%+v, %v), want second", ws, ok)
	}
	if _, ok := r.Workspace(source.Candidate{Path: dir, Label: "second", Source: config.SourceZoxide}); ok {
		t.Error("a zoxide row has no entry of its own")
	}
}

// TestAttach_SharesPresentations proves Attach attaches every candidate's
// resolved presentation and shares one value between equal ones.
func TestAttach_SharesPresentations(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Wildcards = []config.WildcardConfig{{Pattern: "/b", Presentation: config.Presentation{Icon: strPtr("B")}}}
	cands := []source.Candidate{
		{Path: "/a", Source: config.SourceZoxide},
		{Path: "/b", Source: config.SourceZoxide},
		{Path: "/c", Source: config.SourceZoxide},
	}
	New(cfg).Attach(cands)
	for _, c := range cands {
		if c.Presentation == nil {
			t.Fatalf("%s has no presentation", c.Path)
		}
	}
	if cands[0].Presentation != cands[2].Presentation {
		t.Error("equal presentations are not shared")
	}
	if cands[1].Presentation.Icon != "B" || cands[0].Presentation.Icon != cfg.Presentations().Zoxide.Icon {
		t.Errorf("icons = %q, %q", cands[0].Presentation.Icon, cands[1].Presentation.Icon)
	}
}

// TestFor_SourcesAndCustomSources resolves each source's own table: a
// custom source by name (its default icon is the row's own .Icon), anything
// else with the defaults of rows of no source.
func TestFor_SourcesAndCustomSources(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources.Custom = []config.CustomSourceConfig{
		{Name: "prs", Presentation: config.Presentation{LabelFormat: strPtr("PR {{ .Label }}")}},
		{Name: "issues", Presentation: config.Presentation{Icon: strPtr("I ")}},
	}
	p := cfg.Presentations()
	r := New(cfg)
	if got := r.For(source.Candidate{Source: "prs", Icon: "P"}).Presentation; got.Label != "PR {{ .Label }}" || got.Icon != "{{ .Icon }}" {
		t.Errorf("prs = %+v, want its label and the row's own icon", got)
	}
	if got := r.For(source.Candidate{Source: "issues", Icon: "P"}).Presentation; got.Icon != "I " {
		t.Errorf("issues icon = %q, want the configured one over the row's", got.Icon)
	}
	if got := r.For(source.Candidate{Path: "/x", Source: "path"}).Presentation; got.Label != p.Other.Label {
		t.Errorf("path label = %q, want %q", got.Label, p.Other.Label)
	}
}

// TestIconColors lists every icon color a resolution can name, once.
func TestIconColors(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "e", Path: "/e", Presentation: config.Presentation{IconColor: strPtr("peach")}}}
	cfg.Wildcards = []config.WildcardConfig{{Pattern: "**", Presentation: config.Presentation{IconColor: strPtr("#ff0000")}}, {Pattern: "x", Presentation: config.Presentation{IconColor: strPtr("peach")}}}
	got := New(cfg).IconColors()
	seen := map[string]int{}
	for _, ref := range got {
		seen[ref]++
	}
	for _, want := range []string{"source.herdr", "source.zoxide", "text.muted", "peach", "#ff0000"} {
		if seen[want] != 1 {
			t.Errorf("icon colors %v: %q appears %d times, want once", got, want, seen[want])
		}
	}
}

// TestMatchSegments covers wildcard patterns: tilde expansion, "**" as zero
// or more whole segments, single-segment globbing and a malformed pattern
// matching nothing.
func TestMatchSegments(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no resolvable home directory")
	}
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"~/projects/kubernetes/**", filepath.Join(home, "projects", "kubernetes", "myrepo"), true},
		{"~/projects/kubernetes/**", filepath.Join(home, "projects", "kubernetes", "myrepo", "sub", "dir"), true},
		{"~/projects/kubernetes/**", filepath.Join(home, "projects", "kubernetes"), true},
		{"~/projects/kubernetes/**", filepath.Join(home, "projects", "other", "myrepo"), false},
		{"*.go", "main.go", true},
		{"/srv/*", "/srv/a/b", false},
		{"[", "/p/foo", false},
		{"", "/p/foo", false},
	}
	for _, tc := range cases {
		if got := matchSegments(patternSegments(tc.pattern), splitPath(tc.path)); got != tc.want {
			t.Errorf("match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}
