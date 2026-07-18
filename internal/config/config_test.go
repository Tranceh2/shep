package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestDefaults_PathAgnostic ensures Defaults() produces no hardcoded absolute
// user paths and ships with every built-in source enabled.
func TestDefaults_PathAgnostic(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg == nil {
		t.Fatal("Defaults returned nil")
	}
	if len(cfg.General.Sources) != 4 {
		t.Fatalf("expected 4 default sources, got %d: %v", len(cfg.General.Sources), cfg.General.Sources)
	}
	if cfg.Workspaces == nil {
		t.Fatal("Defaults Workspaces slice must be non-nil")
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
	got := Defaults().General.Sources
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sources[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

// TestLoad_MissingFileFallsBackToDefaults confirms a missing config path
// resolves to Defaults rather than an error.
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
	if len(cfg.Preview.Default) != 0 {
		t.Errorf("expected no default preview sections, got %v", cfg.Preview.Default)
	}
}

// TestLoad_RejectsUnknownSourceName (requirement 2) fails fast on a typo'd
// general.sources entry instead of silently ignoring it.
func TestLoad_RejectsUnknownSourceName(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	doc := "[general]\nsources = [\"herdr\", \"cwd\"]\n"
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

// TestLoad_ParsesSchema covers general, herdr, sources, defaults, tui,
// workspaces (plain + group), and templates sections of the canonical model.
func TestLoad_ParsesSchema(t *testing.T) {
	t.Parallel()

	const doc = `
version = 1

[general]
sources = ["herdr", "workspaces", "zoxide", "projects"]
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
sources = ["projects", "zoxide"]
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
	if got, want := len(cfg.General.Sources), 4; got != want {
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
	if got, want := len(group.Sources), 2; got != want {
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
	if err := os.WriteFile(path, []byte("[general]\nsources = [\"herdr\"]\n"), 0o600); err != nil {
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
command = "git -C {path} log -n 5"
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
	if got, want := cmd.Command, "git -C {path} log -n 5"; got != want {
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
		name   string
		doc    string
		errSub string
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
		})
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
sources = ["projects", "roots"]
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
		"version = 1", "[general]", "sources = [", "[defaults]", "type = ",
		"template = ", "[tui]", "list_width", "preview_width", `layout = "landscape"`, "[preview]",
		"[preview.commands.", "[sources.herdr]", "[sources.projects]",
		"markers = ", "[templates.default]", "[templates.k8s]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ExampleTOML missing %q", want)
		}
	}
	for _, bad := range []string{"[layouts", "kind =", "provider_order", "/Users/", "Proyectos"} {
		if strings.Contains(got, bad) {
			t.Errorf("ExampleTOML must not contain %q:\n%s", bad, got)
		}
	}

	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("generated example must load cleanly: %v", err)
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

// TestLoad_AcceptsValidTUILayoutValues confirms both documented layout
// values parse and load without error.
func TestLoad_AcceptsValidTUILayoutValues(t *testing.T) {
	t.Parallel()
	for _, val := range []string{"landscape", "portrait"} {
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
// parses and loads without error.
func TestLoad_AcceptsValidTUIThemeValues(t *testing.T) {
	t.Parallel()
	for _, val := range []string{TUIThemeMocha, TUIThemeMacchiato, TUIThemeFrappe, TUIThemeLatte, TUIThemePlain} {
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
// fallback tier name parses and loads without error.
func TestLoad_AcceptsValidTUIIconsValues(t *testing.T) {
	t.Parallel()
	for _, val := range []string{TUIIconsNerd, TUIIconsUnicode, TUIIconsASCII} {
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
sources = ["projects"]
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
