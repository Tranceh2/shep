package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
)

// testTemplates is the engine every renderer under test renders preview
// command arguments with; its home matches the paths the tests use.
var testTemplates = tmpl.New("/home/user")

// fakeGit is a deterministic GitProvider for renderer tests; no real processes.
type fakeGit struct {
	summary GitSummary
	err     error
	calls   int
}

func (f *fakeGit) Summary(_ context.Context, _ string) (GitSummary, error) {
	f.calls++
	if f.err != nil {
		return GitSummary{}, f.err
	}
	return f.summary, nil
}

// fakeRunner is a scriptable CommandRunner for renderer tests.
type fakeRunner struct {
	out      map[string]string // argv[0] -> output
	err      map[string]error
	argv     []string
	calls    int
	maxLines int
	contexts []context.Context
}

func (f *fakeRunner) Run(ctx context.Context, argv []string, _ string, maxLines int) (string, error) {
	f.calls++
	if len(argv) == 0 {
		return "", errors.New("empty argv")
	}
	f.argv = append([]string(nil), argv...)
	f.maxLines = maxLines
	f.contexts = append(f.contexts, ctx)
	if err, ok := f.err[argv[0]]; ok {
		return "", err
	}
	return f.out[argv[0]], nil
}

func candidate(label, path, src, template string) source.Candidate {
	c := source.Candidate{
		Path:   path,
		Label:  label,
		Source: src,
	}
	if template != "" {
		c.Meta = map[string]string{"template": template}
	}
	return c
}

func mustRender(t *testing.T, r Renderer, cand source.Candidate) string {
	t.Helper()
	res, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return res.Text
}

// cfgWithDefault builds a config whose preview sections are exactly names, for
// every source.
//
// It clears the per-source lists as well as setting Preview.Default, because
// those lists outrank Preview.Default in the renderer — Defaults() supplies them
// and Load() suppresses them whenever a document writes preview.default, so
// setting only Preview.Default here would describe a state no real config can
// reach and would leave these tests exercising the per-source list instead of
// the sections they name.
func cfgWithDefault(names ...string) *config.Config {
	cfg := config.Defaults()
	cfg.Preview.Default = names
	cfg.Sources.Herdr.Preview = nil
	cfg.Sources.Sessions.Preview = nil
	cfg.Sources.Workspaces.Preview = nil
	cfg.Sources.Zoxide.Preview = nil
	cfg.Sources.Projects.Preview = nil
	return cfg
}

// TestRender_IdentityLayout confirms the built-in "identity" section shows
// label, path, source and omits template when absent.
func TestRender_IdentityLayout(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewIdentity), testTemplates, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("identity layout:\n got %q\nwant %q", got, want)
	}
}

// TestRender_IdentityWithTemplate includes a matched template line.
func TestRender_IdentityWithTemplate(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewIdentity), testTemplates, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", "dev"))
	want := "foo\npath: /p/foo\nsource: workspaces\ntemplate: dev"
	if got != want {
		t.Errorf("identity+template:\n got %q\nwant %q", got, want)
	}
}

// TestRender_SessionInfoRendersOnlyCandidateMetadata ensures session previews
// require no filesystem, socket, pane, or command boundary and make missing
// optional metadata explicit.
func TestRender_SessionInfoRendersOnlyCandidateMetadata(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		meta map[string]string
		want string
	}{
		{
			name: "running default with metadata",
			meta: map[string]string{
				"session_name": "default", "running": "true", "default": "true", "session_dir": "/work/default", "socket_path": "/tmp/default.sock",
			},
			want: "session\n  name: default\n  state: running\n  default: true\n  session dir: /work/default\n  socket path: /tmp/default.sock",
		},
		{
			name: "stopped with unavailable optional metadata",
			meta: map[string]string{
				"session_name": "stopped", "running": "false", "default": "false", "session_dir": "", "socket_path": "",
			},
			want: "session\n  name: stopped\n  state: stopped\n  default: false\n  session dir: unavailable\n  socket path: unavailable",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Sources.Sessions.Preview = []string{config.PreviewSessionInfo}
			cand := source.Candidate{Path: "/must-not-be-read", Label: tt.meta["session_name"], Source: config.SourceSessions, Meta: tt.meta}
			got := mustRender(t, NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil), cand)
			if got != tt.want {
				t.Errorf("session preview = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRender_SessionsPreviewOverridesPathBasedPreviews ensures a sessions
// candidate always uses its source preview even when its display-only
// session_dir collides with a configured workspace or wildcard path.
func TestRender_SessionsPreviewOverridesPathBasedPreviews(t *testing.T) {
	t.Parallel()

	const sessionDir = "/sessions/alpha"
	want := "session\n  name: alpha\n  state: running\n  default: false\n  session dir: /sessions/alpha\n  socket path: /tmp/alpha.sock"

	for _, tt := range []struct {
		name   string
		config func(*config.Config)
	}{
		{
			name: "workspace path collision",
			config: func(cfg *config.Config) {
				cfg.Workspaces = []config.WorkspaceConfig{{
					Name:    "alpha",
					Path:    sessionDir,
					Preview: []string{config.PreviewDir, config.PreviewGit, "custom", config.PreviewActivePane},
				}}
			},
		},
		{
			name: "wildcard path collision",
			config: func(cfg *config.Config) {
				cfg.Wildcards = []config.WildcardConfig{{
					Pattern: "/sessions/*",
					Preview: []string{config.PreviewDir, config.PreviewGit, "custom", config.PreviewActivePane},
				}}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Sources.Sessions.Preview = []string{config.PreviewSessionInfo}
			cfg.Preview.Commands = map[string]config.PreviewCommand{
				"custom": {Command: "printf %s {{.Path}}"},
			}
			tt.config(cfg)

			git := &fakeGit{summary: GitSummary{Branch: "main"}}
			runner := &fakeRunner{}
			pane := &fakePreviewDriver{currentPane: source.Pane{ID: "w1:p1", WorkspaceID: "w1", Focused: true}}
			renderer := NewRenderer(cfg, testTemplates, config.Probes{Git: true}, git, runner, withFakeSnapshot(pane))
			cand := source.Candidate{
				Path:   sessionDir,
				Label:  "alpha",
				Source: config.SourceSessions,
				Meta: map[string]string{
					"session_name": "alpha",
					"running":      "true",
					"default":      "false",
					"session_dir":  sessionDir,
					"socket_path":  "/tmp/alpha.sock",
				},
			}

			res, err := renderer.Render(context.Background(), cand)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if res.Text != want {
				t.Errorf("session preview = %q, want %q", res.Text, want)
			}
			if len(res.Sections) != 1 || res.Sections[0].Kind != config.PreviewSessionInfo {
				t.Errorf("sections = %+v, want only session_info", res.Sections)
			}
			if runner.calls != 0 {
				t.Errorf("directory or custom command lookup calls = %d, want 0", runner.calls)
			}
			if git.calls != 0 {
				t.Errorf("Git lookup calls = %d, want 0", git.calls)
			}
			if pane.readCalls != 0 {
				t.Errorf("pane lookup calls = %d, want 0", pane.readCalls)
			}
		})
	}
}

// TestRender_IdentityAndGit confirms identity+git compose with a blank-line
// separator and the git line renders "git: <summary>".
func TestRender_IdentityAndGit(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewIdentity, config.PreviewGit), testTemplates, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "main", Dirty: 2}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces\n\ngit: main (2 changes)"
	if got != want {
		t.Errorf("identity+git:\n got %q\nwant %q", got, want)
	}
}

func TestRender_WorktreeGitBadge(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewGit), testTemplates, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "feat/auth", Dirty: 2}}, nil)
	cand := candidate("api", "/trees/api", config.SourceProjects, "")
	cand.Meta = map[string]string{"is_worktree": "true", "branch": "feat/auth", "head": "9fce23abcdef"}

	got := mustRender(t, r, cand)
	for _, want := range []string{"[worktree: feat/auth]", "9fce23a", "2 changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("worktree preview %q missing %q", got, want)
		}
	}
}

// TestRender_GitBypassed confirms the git section is entirely omitted when
// probes report git missing, or when Summary errors (slow/missing/timeout),
// and that the overall preview falls back to the identity built-in instead
// of a blank success once the only configured section contributes nothing.
func TestRender_WorktreeGitBadgeFallsBackToGitBranch(t *testing.T) {
	t.Parallel()
	r := NewRenderer(cfgWithDefault(config.PreviewGit), testTemplates, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "detached", Dirty: 0}}, nil)
	cand := candidate("api", "/trees/api", config.SourceProjects, "")
	cand.Meta = map[string]string{"is_worktree": "true"}

	if got := mustRender(t, r, cand); !strings.Contains(got, "[worktree: detached]") || !strings.Contains(got, "clean") {
		t.Fatalf("worktree fallback preview = %q", got)
	}
}

func TestRender_GitBypassed(t *testing.T) {
	t.Parallel()

	t.Run("git probe off", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(cfgWithDefault(config.PreviewGit), testTemplates, config.Probes{Git: false},
			&fakeGit{summary: GitSummary{Branch: "main"}}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
		if strings.Contains(got, "git:") {
			t.Errorf("git section must be omitted when probe off: %q", got)
		}
		if !strings.HasPrefix(got, "foo\npath:") {
			t.Errorf("expected identity fallback instead of blank preview, got %q", got)
		}
	})
	t.Run("git summary errors", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(cfgWithDefault(config.PreviewGit), testTemplates, config.Probes{Git: true},
			&fakeGit{err: errors.New("timeout")}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
		if strings.Contains(got, "git:") {
			t.Errorf("git section must be omitted on summary error: %q", got)
		}
		if !strings.HasPrefix(got, "foo\npath:") {
			t.Errorf("expected identity fallback instead of blank preview, got %q", got)
		}
	})
}

// TestRender_NoDefaultFallsBackToIdentity confirms an empty [preview].default
// still renders something (the built-in identity fallback) rather than a
// blank preview.
func TestRender_NoDefaultFallsBackToIdentity(t *testing.T) {
	t.Parallel()
	r := NewRenderer(config.Defaults(), testTemplates, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if !strings.HasPrefix(got, "foo\npath:") {
		t.Errorf("expected identity fallback, got %q", got)
	}
}

// TestRender_AllConfiguredSectionsFailFallsBackToIdentity is the blocking
// case: every configured section is non-applicable/fails/empty (git probed
// off, a custom command erroring, workspace/active_pane with no driver).
// Render must never come back as a blank success; it must fall back to a
// clean identity preview instead, and any command failure must stay hidden
// (never surfaced as an error/warning).
func TestRender_CustomSourceLocalCommandsUseMetadataAndRemainScoped(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Preview.Default = []string{config.PreviewIdentity}
	cfg.Sources.Custom = []config.CustomSourceConfig{
		{Name: "kube-a", Preview: []string{"cluster"}, PreviewCommands: map[string]config.CustomSourcePreviewCommand{
			"cluster": {Command: []string{"kube-preview", "{{ index .Meta \"context\" }}"}, Timeout: config.Duration(time.Second), MaxLines: 12},
		}},
		{Name: "kube-b", Preview: []string{"cluster"}, PreviewCommands: map[string]config.CustomSourcePreviewCommand{
			"cluster": {Command: []string{"other-preview", "{{ index .Meta \"context\" }}"}, Timeout: config.Duration(time.Second), MaxLines: 12},
		}},
	}
	runner := &fakeRunner{out: map[string]string{"kube-preview": "cluster ok", "other-preview": "other ok"}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	cand := source.Candidate{Path: "/tmp", Label: "prod", Source: "kube-a", Meta: map[string]string{"context": "cluster prod west"}}
	if got := mustRender(t, r, cand); got != "cluster ok" {
		t.Fatalf("local preview = %q, want cluster output", got)
	}
	if !strings.EqualFold(strings.Join(runner.argv, "|"), "kube-preview|cluster prod west") {
		t.Fatalf("argv = %#v, want metadata with spaces as one token", runner.argv)
	}
	if runner.maxLines != 12 || len(runner.contexts) != 1 {
		t.Fatalf("local command bounds = max_lines %d contexts %d, want 12 and one context", runner.maxLines, len(runner.contexts))
	}

	runner = &fakeRunner{out: map[string]string{"kube-preview": "cluster ok", "other-preview": "other ok"}}
	r = NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	foreign := source.Candidate{Path: "/tmp", Label: "prod", Source: "unknown", Meta: map[string]string{"context": "cluster prod west"}}
	got := mustRender(t, r, foreign)
	if strings.Contains(got, "cluster ok") || runner.calls != 0 {
		t.Fatalf("foreign candidate used custom source-local command: output=%q calls=%d", got, runner.calls)
	}
}

// TestRender_PreviewCommandsRenderSharedTemplateData proves both global
// [preview.commands] and custom-source preview_commands render their argv
// with the shared template data and functions: .Kind, git fields, .Icon,
// .Meta and the engine's tilde, with .Path being the normalized path.
func TestRender_PreviewCommandsRenderSharedTemplateData(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault("info")
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"info": {Command: `echo {{.Kind}} "{{ .Path | tilde }}" {{.Branch}} {{.Icon}} x{{.Meta.missing}}`},
	}
	runner := &fakeRunner{out: map[string]string{"echo": "ok"}}
	cand := source.Candidate{
		Path: "/link/wt", NormalizedPath: "/home/user/src/shep-wt", Label: "shep (feat)", Source: config.SourceProjects, Icon: "W",
		Meta: map[string]string{"is_worktree": "true", "branch": "feat", "repo": "shep"},
	}
	if got := mustRender(t, NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner), cand); got != "ok" {
		t.Fatalf("preview = %q, want the command output", got)
	}
	if want := []string{"echo", "worktree", "~/src/shep-wt", "feat", "W", "x"}; !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("global argv = %#v, want %#v", runner.argv, want)
	}

	cfg = config.Defaults()
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name: "kube", Preview: []string{"cluster"},
		PreviewCommands: map[string]config.CustomSourcePreviewCommand{
			"cluster": {Command: []string{"show", "{{.Kind}}", "{{.Source}}", "{{ .Meta.context | upper }}", "{{ .Label | trimIcon }}"}, Timeout: config.Duration(time.Second), MaxLines: 5},
		},
	}}
	runner = &fakeRunner{out: map[string]string{"show": "ok"}}
	custom := source.Candidate{Path: "/tmp", Label: "\u2388 prod", Source: "kube", Meta: map[string]string{"custom_source": "true", "context": "prod"}}
	if got := mustRender(t, NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner), custom); got != "ok" {
		t.Fatalf("custom preview = %q, want the command output", got)
	}
	if want := []string{"show", "custom", "kube", "PROD", "prod"}; !reflect.DeepEqual(runner.argv, want) {
		t.Fatalf("custom argv = %#v, want %#v", runner.argv, want)
	}
}

func TestRender_CustomSourceLocalFailureDoesNotHideOtherSections(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Sources.Custom = []config.CustomSourceConfig{{
		Name:    "kube",
		Preview: []string{"cluster", "health"},
		PreviewCommands: map[string]config.CustomSourcePreviewCommand{
			"cluster": {Command: []string{"cluster"}, Timeout: config.Duration(time.Second), MaxLines: 12},
			"health":  {Command: []string{"health"}, Timeout: config.Duration(time.Millisecond), MaxLines: 10},
		},
	}}
	runner := &fakeRunner{
		out: map[string]string{"cluster": "cluster rendered"},
		err: map[string]error{"health": errors.New("timeout")},
	}
	got := mustRender(t, NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner), source.Candidate{Path: "/tmp", Source: "kube"})
	if !strings.Contains(got, "cluster rendered") || strings.Contains(got, "health") {
		t.Fatalf("partial local preview = %q, want cluster only", got)
	}
}

func TestRender_AllConfiguredSectionsFailFallsBackToIdentity(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewGit, "broken", config.PreviewWorkspace, config.PreviewActivePane)
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"broken": {Command: "false"},
	}
	runner := &fakeRunner{err: map[string]error{"false": errors.New("exit 1")}}
	r := NewRenderer(cfg, testTemplates, config.Probes{Git: false}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("expected clean identity fallback, got %q", got)
	}
}

// TestResolvePreviewNames_PerSourceDefaultsAndUserControl locks the whole
// first-run preview contract, which has three layers that are easy to break
// independently.
//
// With nothing configured, each built-in source gets the sections that describe
// its own rows: a Herdr workspace has tabs, panes and an agent; a zoxide
// directory is usually not a repository but always has contents.
//
// The subtle part is the second case. A per-source list outranks
// preview.default in the renderer, so shipping built-in per-source lists would
// make an explicitly written preview.default invisible for every source the
// picker actually shows. Config normalization therefore suppresses the built-in
// lists when the document writes preview.default, leaving one obvious control.
// An explicit [sources.<name>].preview still wins over both.
func TestResolvePreviewNames_PerSourceDefaultsAndUserControl(t *testing.T) {
	t.Parallel()

	load := func(t *testing.T, body string) *config.Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("version = 2\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	sections := func(cfg *config.Config, sourceName string) []string {
		return resolvePreviewNames(cfg, source.Candidate{Source: sourceName, Path: "/tmp", Label: "row"})
	}

	t.Run("no config gives every source its own sections", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "")
		for sourceName, want := range map[string][]string{
			config.SourceHerdr:      {config.PreviewWorkspace, config.PreviewActivePane, config.PreviewAgentStatus},
			config.SourceWorkspaces: {config.PreviewIdentity, config.PreviewDir},
			config.SourceZoxide:     {config.PreviewIdentity, config.PreviewDir},
			config.SourceProjects:   {config.PreviewIdentity, config.PreviewGit, config.PreviewDir},
		} {
			if got := sections(cfg, sourceName); !reflect.DeepEqual(got, want) {
				t.Errorf("%s sections = %v, want %v", sourceName, got, want)
			}
		}
	})

	t.Run("an explicit preview.default reaches every built-in source", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "[preview]\ndefault = [\""+config.PreviewIdentity+"\"]\n")
		for _, sourceName := range []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects} {
			got := sections(cfg, sourceName)
			if len(got) != 1 || got[0] != config.PreviewIdentity {
				t.Errorf("%s sections = %v, want the user's [%s]", sourceName, got, config.PreviewIdentity)
			}
		}
	})

	t.Run("an explicit source list wins over preview.default", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "[preview]\ndefault = [\""+config.PreviewIdentity+"\"]\n[sources.zoxide]\npreview = [\""+config.PreviewGit+"\"]\n")
		if got := sections(cfg, config.SourceZoxide); len(got) != 1 || got[0] != config.PreviewGit {
			t.Errorf("zoxide sections = %v, want [%s]", got, config.PreviewGit)
		}
		if got := sections(cfg, config.SourceProjects); len(got) != 1 || got[0] != config.PreviewIdentity {
			t.Errorf("projects sections = %v, want the user's [%s]", got, config.PreviewIdentity)
		}
	})

	t.Run("an explicit empty source list means no sections", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "[sources.zoxide]\npreview = []\n")
		if got := sections(cfg, config.SourceZoxide); len(got) != 0 {
			t.Errorf("zoxide sections = %v, want none", got)
		}
	})
}

// TestResolvePreviewNames_SessionsKeepTheirOwnFallback locks the outcome that a
// session candidate previews as session_info for an ordinary loaded config.
//
// This is a regression guard with real history: the sessions fallback was first
// placed AFTER Preview.Default, which looked correct in isolation but became
// unreachable the moment Preview.Default gained a built-in value — every loaded
// config then rendered a session as an empty path instead. A session carries no
// path, no git repository and no Herdr pane, so the general sections describe
// nothing; only an explicit sources.sessions.preview may override it.
func TestResolvePreviewNames_SessionsKeepTheirOwnFallback(t *testing.T) {
	t.Parallel()

	load := func(t *testing.T, body string) *config.Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("version = 2\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	session := source.Candidate{Source: config.SourceSessions, Label: "alpha"}

	t.Run("a loaded config with no preview settings", func(t *testing.T) {
		t.Parallel()
		got := resolvePreviewNames(load(t, ""), session)
		if len(got) != 1 || got[0] != config.PreviewSessionInfo {
			t.Fatalf("sections = %v, want [%s]", got, config.PreviewSessionInfo)
		}
	})

	t.Run("an explicit sessions preview still wins", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "[sources.sessions]\npreview = [\""+config.PreviewDir+"\"]\n")
		got := resolvePreviewNames(cfg, session)
		if len(got) != 1 || got[0] != config.PreviewDir {
			t.Fatalf("sections = %v, want [%s]", got, config.PreviewDir)
		}
	})
}

// TestLoadedConfig_ExplicitEmptyPreviewDefaultDisablesTheDefault proves an
// explicit `default = []` survives normalization. TOML decodes an omitted key to
// nil and an empty array to an empty non-nil slice, so normalization must test
// nil rather than length or it silently overwrites a deliberate choice.
func TestLoadedConfig_ExplicitEmptyPreviewDefaultDisablesTheDefault(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 2\n[preview]\ndefault = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Preview.Default) != 0 {
		t.Fatalf("explicit empty preview.default was overwritten with %v", cfg.Preview.Default)
	}
	got := resolvePreviewNames(cfg, source.Candidate{Source: config.SourceZoxide, Path: "/tmp"})
	if len(got) != 1 || got[0] != config.PreviewIdentity {
		t.Fatalf("sections = %v, want the bare [%s] fallback", got, config.PreviewIdentity)
	}
}

// TestResolvePreviewNames_Precedence exercises the documented precedence chain
// end to end: workspace.preview > wildcard.preview > sources.<source>.preview >
// source-specific fallback > preview.default > built-in identity fallback.
func TestResolvePreviewNames_Precedence(t *testing.T) {
	t.Parallel()

	t.Run("workspace preview wins over everything", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Herdr.Preview = []string{config.PreviewDir}
		cfg.Wildcards = []config.WildcardConfig{{Pattern: "/p/*", Preview: []string{config.PreviewIdentity}}}
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "foo", Path: "/p/foo", Preview: []string{"custom"}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/p/foo", Source: config.SourceHerdr})
		want := []string{"custom"}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("got %v want %v", got, want)
		}
	})

	t.Run("wildcard wins over source and default", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Herdr.Preview = []string{config.PreviewDir}
		cfg.Wildcards = []config.WildcardConfig{{Pattern: "/p/*", Preview: []string{config.PreviewIdentity}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/p/foo", Source: config.SourceHerdr})
		if len(got) != 1 || got[0] != config.PreviewIdentity {
			t.Errorf("got %v want [identity]", got)
		}
	})

	t.Run("source preview wins over default", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Herdr.Preview = []string{config.PreviewDir}
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/other/foo", Source: config.SourceHerdr})
		if len(got) != 1 || got[0] != config.PreviewDir {
			t.Errorf("got %v want [dir]", got)
		}
	})

	t.Run("default used when nothing else matches", func(t *testing.T) {
		t.Parallel()
		// cfgWithDefault, not Defaults()+Preview.Default: the per-source lists
		// outrank Preview.Default, and Load() suppresses them precisely when a
		// document writes preview.default, so this is the only state a real
		// config can reach with these sections applying to a zoxide row.
		cfg := cfgWithDefault(config.PreviewGit)
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/other/foo", Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != config.PreviewGit {
			t.Errorf("got %v want [git]", got)
		}
	})

	t.Run("built-in per-source sections apply when nothing else matches", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		got := resolvePreviewNames(cfg, source.Candidate{Path: "/other/foo", Source: config.SourceZoxide})
		want := []string{config.PreviewIdentity, config.PreviewDir}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})

	t.Run("declared custom source preview wins over default", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Preview.Default = []string{config.PreviewGit}
		cfg.Sources.Custom = []config.CustomSourceConfig{{Name: "prs", Preview: []string{config.PreviewIdentity}}}
		got := resolvePreviewNames(cfg, source.Candidate{Label: "PR 42", Source: "prs"})
		if len(got) != 1 || got[0] != config.PreviewIdentity {
			t.Errorf("got %v want [identity]", got)
		}
	})
}

// TestResolvePreviewNames_TildeWorkspacePathExpands is a characterization
// test locking down the workspace-preview path-match tier's tilde
// expansion after resolvePreviewNames was switched from a package-local
// expandTildeLocal duplicate to pathutil.ExpandTilde. A workspace configured
// with a leading "~/" must still match a candidate whose path was already
// resolved to the real home directory.
func TestResolvePreviewNames_TildeWorkspacePathExpands(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no resolvable home dir: %v", err)
	}
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "tilde", Path: "~/shep-tilde-test", Preview: []string{"custom"}}}
	cand := source.Candidate{Path: filepath.Join(home, "shep-tilde-test"), Source: config.SourceZoxide}

	got := resolvePreviewNames(cfg, cand)

	if len(got) != 1 || got[0] != "custom" {
		t.Errorf("got %v, want [custom] (tilde-prefixed workspace path should expand and match)", got)
	}
}

// TestResolvePreviewNames_SymlinkAliasMatches is a regression guard: this
// scenario must keep matching after the pathutil.SameDir swap (os.SameFile
// is the source of truth for case-fold and symlink equivalence, so no
// separate Normalize pass is needed at this call site). A workspace
// configured via a symlink must still match a candidate whose path is the
// symlink's real target (and vice versa).
func TestResolvePreviewNames_SymlinkAliasMatches(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	t.Run("workspace is symlink, candidate is real target", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "aliased", Path: link, Preview: []string{"custom"}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: real, Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != "custom" {
			t.Errorf("got %v, want [custom] (symlink workspace should match its real target)", got)
		}
	})

	t.Run("workspace is real target, candidate is symlink", func(t *testing.T) {
		t.Parallel()
		cfg := config.Defaults()
		cfg.Workspaces = []config.WorkspaceConfig{{Name: "aliased", Path: real, Preview: []string{"custom"}}}
		got := resolvePreviewNames(cfg, source.Candidate{Path: link, Source: config.SourceZoxide})
		if len(got) != 1 || got[0] != "custom" {
			t.Errorf("got %v, want [custom] (real-target workspace should match a symlink candidate)", got)
		}
	})
}

// TestResolvePreviewNames_CaseFoldMatches is a regression guard: this
// scenario must keep matching after the pathutil.SameDir swap (os.SameFile
// is the source of truth for case-fold and symlink equivalence). On a
// case-insensitive filesystem (macOS APFS default, Windows), a workspace
// path differing only in case from the candidate's real directory must
// still match. Gated to darwin/windows because a case-sensitive filesystem
// (most Linux/ext4) genuinely has two distinct directories here.
func TestResolvePreviewNames_CaseFoldMatches(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive match only guaranteed on darwin/windows")
	}
	t.Parallel()
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "ECORP")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(tmp, "ecorp")

	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ecorp", Path: upper, Preview: []string{"custom"}}}
	got := resolvePreviewNames(cfg, source.Candidate{Path: lower, Source: config.SourceZoxide})
	if len(got) != 1 || got[0] != "custom" {
		t.Errorf("got %v, want [custom] (case-insensitive filesystem match)", got)
	}
}

func TestResolvePreviewNames_SamePathWorkspaces_DistinctPreviews(t *testing.T) {
	t.Parallel()
	shared := t.TempDir()
	cfg := config.Defaults()
	cfg.Workspaces = []config.WorkspaceConfig{
		{Name: "Kubernetes", Path: shared, Preview: []string{"cluster", "health"}},
		{Name: "fsociety", Path: shared, Preview: []string{"git", "dir"}},
	}
	gotKube := resolvePreviewNames(cfg, source.Candidate{Path: shared, Label: "Kubernetes", Source: config.SourceWorkspaces})
	if !reflect.DeepEqual(gotKube, []string{"cluster", "health"}) {
		t.Errorf("got %v, want [cluster health]", gotKube)
	}
	gotFsociety := resolvePreviewNames(cfg, source.Candidate{Path: shared, Label: "fsociety", Source: config.SourceWorkspaces})
	if !reflect.DeepEqual(gotFsociety, []string{"git", "dir"}) {
		t.Errorf("got %v, want [git dir]", gotFsociety)
	}
}

// fakePreviewDriver is a controllable snapshot and pane-read fixture for
// workspace/active_pane section tests.
type fakePreviewDriver struct {
	tabs  []source.Tab
	panes []source.Pane
	// tabsByWorkspace/panesByWorkspace, when non-nil, build a snapshot with
	// different tab/pane records per workspace ID for cache-collision coverage.
	tabsByWorkspace  map[string][]source.Tab
	panesByWorkspace map[string][]source.Pane
	readOut          string
	readErr          error
	readCalls        int
	lastLines        int
	lastPaneID       string
	currentPane      source.Pane
	// block switches each herdr query into a ctx-bound blocker that returns
	// ctx.Err() — used for the timeout tests.
	block bool
}

// withFakeSnapshot builds the immutable snapshot and live pane reader used by
// renderer tests. Production uses WithSnapshot plus WithPaneReader directly.
func withFakeSnapshot(driver *fakePreviewDriver) RendererOption {
	snapshot := source.Snapshot{Tabs: append([]source.Tab(nil), driver.tabs...), Panes: append([]source.Pane(nil), driver.panes...)}
	for workspaceID, tabs := range driver.tabsByWorkspace {
		snapshot.Tabs = append(snapshot.Tabs, tabs...)
		snapshot.Workspaces = append(snapshot.Workspaces, source.Workspace{ID: workspaceID})
	}
	for workspaceID, panes := range driver.panesByWorkspace {
		snapshot.Panes = append(snapshot.Panes, panes...)
		if !snapshotHasWorkspace(snapshot, workspaceID) {
			snapshot.Workspaces = append(snapshot.Workspaces, source.Workspace{ID: workspaceID})
		}
	}
	for _, pane := range snapshot.Panes {
		if !snapshotHasWorkspace(snapshot, pane.WorkspaceID) && pane.WorkspaceID != "" {
			snapshot.Workspaces = append(snapshot.Workspaces, source.Workspace{ID: pane.WorkspaceID})
		}
	}
	if driver.currentPane.ID != "" {
		snapshot.Panes = append(snapshot.Panes, driver.currentPane)
		snapshot.FocusedPaneID = driver.currentPane.ID
	}
	return func(renderer *defaultRenderer) {
		WithSnapshot(snapshot)(renderer)
		WithPaneReader(driver)(renderer)
	}
}

func snapshotHasWorkspace(snapshot source.Snapshot, workspaceID string) bool {
	for _, workspace := range snapshot.Workspaces {
		if workspace.ID == workspaceID {
			return true
		}
	}
	return false
}

func (f *fakePreviewDriver) Detect(context.Context) bool { return true }
func (f *fakePreviewDriver) Snapshot(context.Context) (source.Snapshot, error) {
	return source.Snapshot{}, errors.New("fakePreviewDriver does not implement Snapshot")
}
func (f *fakePreviewDriver) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	f.readCalls++
	f.lastPaneID = paneID
	f.lastLines = lines
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.readOut, f.readErr
}

// herdrCandidate builds a candidate carrying a workspace_id meta key, mirroring
// the herdr source provider's output.
func herdrCandidate(label, path, workspaceID string) source.Candidate {
	return source.Candidate{
		Path:   path,
		Label:  label,
		Source: config.SourceHerdr,
		Meta:   map[string]string{"workspace_id": workspaceID},
	}
}

// TestRender_WorkspaceSection renders an indented tabs/panes tree for a
// candidate that carries a workspace_id meta key.
func TestRender_WorkspaceSection(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace)
	driver := &fakePreviewDriver{
		tabs: []source.Tab{
			{ID: "wA:t1", WorkspaceID: "wA", Label: "edit", Focused: true, Number: 1, PaneCount: 2},
			{ID: "wA:t2", WorkspaceID: "wA", Label: "term", Focused: false, Number: 2, PaneCount: 1},
		},
		panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: true},
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
		},
	}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "workspace") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "edit") || !strings.Contains(got, "term") {
		t.Errorf("missing tab labels: %q", got)
	}
	if !strings.Contains(got, "*") {
		t.Errorf("missing focused marker for active tab: %q", got)
	}
	if !strings.Contains(got, "/x") || !strings.Contains(got, "/y") {
		t.Errorf("missing pane cwds: %q", got)
	}
}

// TestRender_ActivePaneSection renders the focused pane's captured terminal
// buffer, capped at cfg.Preview.MaxLines.
func TestRender_ActivePaneSection(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewActivePane)
	cfg.Preview.MaxLines = 42
	driver := &fakePreviewDriver{
		panes: []source.Pane{
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: true},
		},
		readOut: "$ echo hi\nhi\n$ ",
	}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "active pane") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "$ echo hi") {
		t.Errorf("missing pane buffer: %q", got)
	}
	if driver.lastPaneID != "wA:p1" {
		t.Errorf("ReadPane called on %q, want the focused pane wA:p1", driver.lastPaneID)
	}
	if driver.lastLines != 42 {
		t.Errorf("ReadPane lines = %d, want cfg.Preview.MaxLines=42", driver.lastLines)
	}
}

// TestRender_ActivePaneSection_FallsBackToFirstPane reads the first pane
// when none is marked focused.
func TestRender_ActivePaneSection_FallsBackToFirstPane(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewActivePane)
	driver := &fakePreviewDriver{
		panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", CWD: "/x", Focused: false},
			{ID: "wA:p2", WorkspaceID: "wA", CWD: "/y", Focused: false},
		},
		readOut: "buffer",
	}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "buffer") {
		t.Errorf("missing buffer: %q", got)
	}
	if driver.lastPaneID != "wA:p1" {
		t.Errorf("ReadPane called on %q, want first pane wA:p1", driver.lastPaneID)
	}
}

// TestRender_HerdrSections_SkipOnNonHerdrCandidate skips workspace and
// active_pane sections when the candidate has no workspace_id meta key.
func TestRender_HerdrSections_SkipOnNonHerdrCandidate(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace, config.PreviewActivePane, config.PreviewIdentity)
	driver := &fakePreviewDriver{tabs: []source.Tab{{ID: "wA:t1"}}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("non-herdr preview must skip herdr sections:\n got %q\nwant %q", got, want)
	}
	if driver.readCalls != 0 {
		t.Errorf("pane reader must not be queried for non-herdr candidate: reads=%d", driver.readCalls)
	}
}

// TestRender_CacheKey_DoesNotAliasCandidatesSharingPath is the regression
// test for the real-world repro: multiple [[workspaces]] entries pointing at
// the identical path (e.g. "ecorp", "allsafe", "fsociety" all at
// ~/projects) must render their own identity, not a stale cached
// preview bled over from whichever candidate was rendered first.
func TestRender_CacheKey_DoesNotAliasCandidatesSharingPath(t *testing.T) {
	t.Parallel()

	r := NewRenderer(cfgWithDefault(config.PreviewIdentity), testTemplates, config.Probes{}, nil, nil)
	shared := "/tmp/shep-preview/shared"

	gotEcorp := mustRender(t, r, candidate("ecorp", shared, config.SourceWorkspaces, ""))
	gotAllsafe := mustRender(t, r, candidate("allsafe", shared, config.SourceWorkspaces, ""))
	gotFsociety := mustRender(t, r, candidate("fsociety", shared, config.SourceWorkspaces, "fso"))

	if !strings.HasPrefix(gotEcorp, "ecorp\n") {
		t.Errorf("ecorp candidate must show its own identity: %q", gotEcorp)
	}
	if !strings.HasPrefix(gotAllsafe, "allsafe\n") {
		t.Errorf("allsafe candidate must show its own identity, not ecorp's cached preview: %q", gotAllsafe)
	}
	if !strings.HasPrefix(gotFsociety, "fsociety\n") {
		t.Errorf("fsociety candidate must show its own identity, not a cached collision: %q", gotFsociety)
	}
	if gotEcorp == gotAllsafe || gotEcorp == gotFsociety || gotAllsafe == gotFsociety {
		t.Error("distinct workspaces sharing a path must not alias in the preview cache")
	}
}

// TestRender_CacheKey_DoesNotAliasHerdrCandidatesSharingCWD covers the same
// class of bug for Herdr-sourced candidates: multiple tabs/panes commonly
// share the same cwd, and each must render its own workspace section instead
// of whichever workspace happened to populate the cache first.
func TestRender_CacheKey_DoesNotAliasHerdrCandidatesSharingCWD(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace)
	driver := &fakePreviewDriver{
		tabsByWorkspace: map[string][]source.Tab{
			"wA": {{ID: "wA:t1", WorkspaceID: "wA", Label: "editorA", Focused: true, Number: 1, PaneCount: 1}},
			"wB": {{ID: "wB:t1", WorkspaceID: "wB", Label: "editorB", Focused: true, Number: 1, PaneCount: 1}},
		},
		panesByWorkspace: map[string][]source.Pane{
			"wA": {{ID: "wA:p1", WorkspaceID: "wA", CWD: "/shared", Focused: true}},
			"wB": {{ID: "wB:p1", WorkspaceID: "wB", CWD: "/shared", Focused: true}},
		},
	}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))

	gotA := mustRender(t, r, herdrCandidate("tabA", "/shared", "wA"))
	gotB := mustRender(t, r, herdrCandidate("tabB", "/shared", "wB"))

	if !strings.Contains(gotA, "editorA") {
		t.Errorf("candidate A must show its own tab: %q", gotA)
	}
	if !strings.Contains(gotB, "editorB") {
		t.Errorf("candidate B must show its own tab, not A's cached result: %q", gotB)
	}
	if gotA == gotB {
		t.Error("distinct herdr candidates sharing a cwd must not alias in the preview cache")
	}
}

// TestRender_WorkspaceSection_UsesSnapshotWithoutLiveQueries confirms a
// workspace preview is resolved from its immutable generation and never waits
// on a live list call.
func TestRender_WorkspaceSection_TimesOutGracefully(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace)
	driver := &fakePreviewDriver{block: true}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	start := time.Now()
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Fatalf("workspace preview took %v, want bounded by 100ms herdr timeout", elapsed)
	}
	if !strings.Contains(got, "workspace") {
		t.Errorf("section heading should still render on timeout: %q", got)
	}
	if strings.Contains(got, "unavailable") {
		t.Errorf("snapshot-backed workspace preview must not report a live-query timeout: %q", got)
	}
}

// TestRenderAgentStatusSection_KnownStatus renders the agent_status section
// heading plus the current pane's known status text.
func TestRenderAgentStatusSection_KnownStatus(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewAgentStatus)
	driver := &fakePreviewDriver{currentPane: source.Pane{ID: "wA:p1", WorkspaceID: "wA", AgentStatus: "idle"}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "agent status") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "idle") {
		t.Errorf("missing known status text: %q", got)
	}
	if strings.Contains(got, "wA:p1") {
		t.Errorf("rendered output must not leak the raw pane id: %q", got)
	}
}

// TestRenderAgentStatusSection_EmptyStatus confirms an empty AgentStatus
// (older Herdr, or a non-agent shell pane) degrades to a "not detected"/
// "unknown" fallback in the preview — this is the preview-only normalization
// rule; the footer keeps empty and "unknown" distinct (see model_internal_test.go).
func TestRenderAgentStatusSection_EmptyStatus(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewAgentStatus)
	driver := &fakePreviewDriver{currentPane: source.Pane{ID: "wA:p1", WorkspaceID: "wA", AgentStatus: ""}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if !strings.Contains(got, "agent status") {
		t.Errorf("missing section heading: %q", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("expected empty status to render as unknown: %q", got)
	}
}

// TestRenderAgentStatusSection_SectionDisabled confirms the section is
// entirely omitted from the joined output (same omit rule as git/dir) when
// the user's preview list does not include agent_status.
func TestRenderAgentStatusSection_SectionDisabled(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewIdentity)
	driver := &fakePreviewDriver{currentPane: source.Pane{ID: "wA:p1", AgentStatus: "working"}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	if strings.Contains(got, "agent status") || strings.Contains(got, "working") {
		t.Errorf("agent_status section must be omitted when disabled: %q", got)
	}
}

// renderAgentStatus renders only the agent_status section for cand against
// snapshot, so tests can compare the exact section text.
func renderAgentStatus(t *testing.T, snapshot source.Snapshot, cand source.Candidate) string {
	t.Helper()
	r := NewRenderer(cfgWithDefault(config.PreviewAgentStatus), testTemplates, config.Probes{}, nil, nil, WithSnapshot(snapshot))
	return mustRender(t, r, cand)
}

// TestRenderAgentStatusSection_ReportsCandidateWorkspaceNotGlobalFocus is the
// regression test for the real-session bug where every workspace preview said
// "working": the section read the pane focused globally in Herdr (where shep
// was launched) instead of the candidate workspace's own panes.
func TestRenderAgentStatusSection_ReportsCandidateWorkspaceNotGlobalFocus(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "wA"}, {ID: "wB"}},
		Panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", Focused: true, AgentStatus: "working"},
			{ID: "wB:p1", WorkspaceID: "wB", Focused: true, AgentStatus: "idle"},
		},
		FocusedWorkspaceID: "wA",
		FocusedPaneID:      "wA:p1",
	}
	if got, want := renderAgentStatus(t, snapshot, herdrCandidate("b", "/b", "wB")), "agent status\n  status: idle"; got != want {
		t.Errorf("workspace B must report its own idle agent, not the globally focused pane:\n got %q\nwant %q", got, want)
	}
	if got, want := renderAgentStatus(t, snapshot, herdrCandidate("a", "/a", "wA")), "agent status\n  status: working"; got != want {
		t.Errorf("workspace A must report its own working agent:\n got %q\nwant %q", got, want)
	}
}

// TestRenderAgentStatusSection_PrecedenceAcrossPanes confirms a workspace
// reports its most important agent state across all panes — blocked > working
// > done > idle > unknown — independent of pane order or focus, and that
// shell panes ("" status) never mask an agent.
func TestRenderAgentStatusSection_PrecedenceAcrossPanes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"blocked beats working", []string{"working", "blocked", "idle"}, "blocked"},
		{"blocked beats everything", []string{"idle", "done", "unknown", "working", "blocked"}, "blocked"},
		{"working beats done", []string{"done", "working"}, "working"},
		{"done beats idle", []string{"idle", "done"}, "done"},
		{"idle beats unknown", []string{"unknown", "idle"}, "idle"},
		{"shell panes are ignored", []string{"", "idle", ""}, "idle"},
		{"only unknown", []string{"unknown", ""}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snapshot := source.Snapshot{Workspaces: []source.Workspace{{ID: "wA"}, {ID: "wOther"}}}
			for i, status := range tc.statuses {
				snapshot.Panes = append(snapshot.Panes, source.Pane{
					ID:          "wA:p" + string(rune('1'+i)),
					WorkspaceID: "wA",
					Focused:     i == 0,
					AgentStatus: status,
				})
			}
			// A globally focused, blocked pane elsewhere must never leak into
			// wA: every non-blocked case would report it if it did.
			snapshot.Panes = append(snapshot.Panes, source.Pane{ID: "wOther:p1", WorkspaceID: "wOther", Focused: true, AgentStatus: "blocked"})
			snapshot.FocusedPaneID = "wOther:p1"

			got := renderAgentStatus(t, snapshot, herdrCandidate("a", "/a", "wA"))
			if want := "agent status\n  status: " + tc.want; got != want {
				t.Errorf("statuses %q:\n got %q\nwant %q", tc.statuses, got, want)
			}
		})
	}
}

// TestRenderAgentStatusSection_ShellPanesOnlyIsUnknown confirms a workspace
// whose panes carry no agent at all ("" status everywhere) keeps the existing
// "unknown" degradation instead of borrowing the global focus.
func TestRenderAgentStatusSection_ShellPanesOnlyIsUnknown(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "wA"}, {ID: "wB"}},
		Panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", Focused: true},
			{ID: "wA:p2", WorkspaceID: "wA"},
			{ID: "wB:p1", WorkspaceID: "wB", Focused: true, AgentStatus: "working"},
		},
		FocusedPaneID: "wB:p1",
	}
	if got, want := renderAgentStatus(t, snapshot, herdrCandidate("a", "/a", "wA")), "agent status\n  status: unknown"; got != want {
		t.Errorf("shell-only workspace:\n got %q\nwant %q", got, want)
	}
}

// TestRenderAgentStatusSection_EmptyWorkspaceNotesNoActivePane confirms a
// workspace with no panes in the snapshot keeps the "(no active pane)" note,
// even while another workspace has a focused, working pane.
func TestRenderAgentStatusSection_EmptyWorkspaceNotesNoActivePane(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces:    []source.Workspace{{ID: "wA"}, {ID: "wB"}},
		Panes:         []source.Pane{{ID: "wB:p1", WorkspaceID: "wB", Focused: true, AgentStatus: "working"}},
		FocusedPaneID: "wB:p1",
	}
	if got, want := renderAgentStatus(t, snapshot, herdrCandidate("a", "/a", "wA")), "agent status\n(no active pane)"; got != want {
		t.Errorf("empty workspace:\n got %q\nwant %q", got, want)
	}
}

// TestRenderAgentStatusSection_AgentCandidateReportsItsOwnPane confirms an
// agents-source row (which carries pane_id and reaches agent_status through
// preview.default) shows its own pane's state, not the workspace aggregate,
// so an idle agent is never labelled "blocked" because of a sibling. A stale
// pane_id falls back to the workspace aggregate.
func TestRenderAgentStatusSection_AgentCandidateReportsItsOwnPane(t *testing.T) {
	t.Parallel()

	snapshot := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "wA"}},
		Panes: []source.Pane{
			{ID: "wA:p1", WorkspaceID: "wA", Focused: true, Agent: "claude", AgentStatus: "blocked"},
			{ID: "wA:p2", WorkspaceID: "wA", Agent: "codex", AgentStatus: "idle"},
		},
		FocusedPaneID: "wA:p1",
	}
	agent := func(paneID string) source.Candidate {
		return source.Candidate{
			Path:   "/a",
			Label:  "agent " + paneID,
			Source: config.SourceAgents,
			Meta:   map[string]string{"workspace_id": "wA", "pane_id": paneID, "kind": "agent"},
		}
	}
	if got, want := renderAgentStatus(t, snapshot, agent("wA:p2")), "agent status\n  status: idle"; got != want {
		t.Errorf("agent row must report its own pane:\n got %q\nwant %q", got, want)
	}
	if got, want := renderAgentStatus(t, snapshot, agent("wA:gone")), "agent status\n  status: blocked"; got != want {
		t.Errorf("stale pane_id must fall back to the workspace aggregate:\n got %q\nwant %q", got, want)
	}
}

// TestRender_ActivePaneSection_TimesOutGracefully mirrors the workspace
// timeout test for the active_pane section, except active_pane's timeout
// contract is stricter than workspace's: renderActivePaneSection returns
// ok=false on any read error/timeout (R3-001), so the section must be
// omitted entirely — no "(active pane unavailable)" placeholder heading —
// and Render falls back to the built-in identity section since active_pane
// was the only configured section.
func TestRender_ActivePaneSection_TimesOutGracefully(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewActivePane)
	driver := &fakePreviewDriver{block: true}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, withFakeSnapshot(driver))
	start := time.Now()
	res, err := r.Render(context.Background(), herdrCandidate("foo", "/x", "wA"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 400*time.Millisecond {
		t.Fatalf("active_pane preview took %v, want bounded by herdr timeouts", elapsed)
	}
	want := "foo\npath: /x\nsource: herdr"
	if res.Text != want {
		t.Errorf("active_pane section must be omitted on timeout (identity fallback expected), not rendered with a placeholder:\n got %q\nwant %q", res.Text, want)
	}
	for _, s := range res.Sections {
		if s.Kind == config.PreviewActivePane {
			t.Errorf("Sections must not contain an active_pane entry on timeout, got %+v", res.Sections)
		}
	}
	if len(res.Sections) != 1 || res.Sections[0].Kind != config.PreviewIdentity {
		t.Errorf("expected only the identity fallback section, got %+v", res.Sections)
	}
}

// TestRender_HerdrSections_WithoutDriver degrades gracefully when no driver
// is wired (e.g. herdr not installed): herdr sections are skipped.
func TestRender_HerdrSections_WithoutDriver(t *testing.T) {
	t.Parallel()

	cfg := cfgWithDefault(config.PreviewWorkspace, config.PreviewIdentity)
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil)
	got := mustRender(t, r, herdrCandidate("foo", "/x", "wA"))
	want := "foo\npath: /x\nsource: herdr"
	if got != want {
		t.Errorf("no driver must skip herdr sections:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DirSection_PicksLsdFirst confirms lsd wins when present.
func TestRender_DirSection_PicksLsdFirst(t *testing.T) {
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "lsd" {
			return "/usr/bin/lsd", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	runner := &fakeRunner{out: map[string]string{"lsd": "lsd-output"}}
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "lsd-output" {
		t.Errorf("got %q, want lsd-output", got)
	}
}

// TestRender_DirSection_FallsBackToEza confirms eza is used when lsd is
// absent.
func TestRender_DirSection_FallsBackToEza(t *testing.T) {
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "eza" {
			return "/usr/bin/eza", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	runner := &fakeRunner{out: map[string]string{"eza": "eza-output"}}
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "eza-output" {
		t.Errorf("got %q, want eza-output", got)
	}
}

// TestRender_DirSection_FallsBackToLs confirms ls is the last resort when
// neither lsd nor eza is present.
func TestRender_DirSection_FallsBackToLs(t *testing.T) {
	orig := dirLookPath
	dirLookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { dirLookPath = orig }()

	runner := &fakeRunner{out: map[string]string{"ls": "ls-output"}}
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "ls-output" {
		t.Errorf("got %q, want ls-output", got)
	}
}

// TestDirArgv_Lsd_UsesColorAlways confirms dirArgv asks lsd for the compact
// listing (one name per line, directories first, hidden entries without "."
// and "..", no long-format columns) and forces --icon=always and
// --color=always so the "dir" preview section shows lsd's real colored
// listing (the whole point of shelling out to lsd instead of plain ls). This
// is safe because internal/tui/model.go's truncateToWidth is ANSI-aware
// (charmbracelet/x/ansi.Truncate) and never cuts mid-escape sequence — see
// TestTruncateToWidth_ANSIStyledInput_* in internal/tui/model_internal_test.go.
func TestDirArgv_Lsd_UsesColorAlways(t *testing.T) {
	// No t.Parallel(): this test mutates the package-level dirLookPath seam
	// (see dirLookPath in renderer.go), which races under -race against
	// TestDirArgv_Eza_UsesColorAlways if both run concurrently.
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "lsd" {
			return "/usr/bin/lsd", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	got := dirArgv("/p/foo")
	want := []string{"lsd", "--almost-all", "--oneline", "--group-directories-first", "--icon=always", "--color=always", "/p/foo"}
	if !equalArgv(got, want) {
		t.Errorf("dirArgv(lsd) = %v, want %v", got, want)
	}
}

// TestDirArgv_Eza_UsesColorAlways is the eza counterpart of
// TestDirArgv_Lsd_UsesColorAlways. --icons=always is required: a bare
// --icons means "auto" in eza >= 0.18 and prints no icons when piped.
func TestDirArgv_Eza_UsesColorAlways(t *testing.T) {
	// No t.Parallel(): this test mutates the package-level dirLookPath seam
	// (see dirLookPath in renderer.go), which races under -race against
	// TestDirArgv_Lsd_UsesColorAlways if both run concurrently.
	orig := dirLookPath
	dirLookPath = func(name string) (string, error) {
		if name == "eza" {
			return "/usr/bin/eza", nil
		}
		return "", errors.New("not found")
	}
	defer func() { dirLookPath = orig }()

	got := dirArgv("/p/foo")
	want := []string{"eza", "--all", "--oneline", "--group-directories-first", "--icons=always", "--color=always", "/p/foo"}
	if !equalArgv(got, want) {
		t.Errorf("dirArgv(eza) = %v, want %v", got, want)
	}
}

// TestDirArgv_Ls_CompactListing confirms the ls fallback prints the same
// compact shape as lsd/eza (one entry per line, hidden entries without "."
// and "..", directories marked with a trailing "/") using only flags BSD and
// GNU ls share, and no long-format -l.
func TestDirArgv_Ls_CompactListing(t *testing.T) {
	// No t.Parallel(): this test mutates the package-level dirLookPath seam.
	orig := dirLookPath
	dirLookPath = func(string) (string, error) { return "", errors.New("not found") }
	defer func() { dirLookPath = orig }()

	got := dirArgv("/p/foo")
	want := []string{"ls", "-1Ap", "/p/foo"}
	if !equalArgv(got, want) {
		t.Errorf("dirArgv(ls) = %v, want %v", got, want)
	}
}

func equalArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRender_DirSection_NoRunnerSkipsSilently confirms a missing runner
// simply omits the section instead of erroring, falling back to the
// identity built-in rather than a blank preview.
func TestRender_DirSection_NoRunnerSkipsSilently(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewDir)
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if !strings.HasPrefix(got, "foo\npath:") {
		t.Errorf("expected identity fallback instead of blank preview, got %q", got)
	}
}

// TestRender_CustomCommand_RunsAndSubstitutesPath confirms a declared
// [preview.commands.<name>] entry renders a Path template into one argv value.
func TestRender_CustomCommand_RunsAndSubstitutesPath(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault("recent_commits")
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"recent_commits": {Command: "git -C {{.Path}} log -n 3"},
	}
	runner := &fakeRunner{out: map[string]string{"git": "commit-log"}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	if got != "commit-log" {
		t.Errorf("got %q, want commit-log", got)
	}
	if want := []string{"git", "-C", "/p/foo", "log", "-n", "3"}; !equalArgv(runner.argv, want) {
		t.Errorf("argv = %v, want %v", runner.argv, want)
	}
}

// TestRender_CustomCommand_LoadedTemplateExecutes ensures Load validation and
// the real renderer/command-runner path agree on the same command syntax.
func TestRender_CustomCommand_LoadedTemplateExecutes(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the system printf command as the preview runtime harness")
	}

	dir := filepath.Join(t.TempDir(), "dogfood path")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	const doc = `[preview]
default = ["path_probe"]

[preview.commands.path_probe]
command = "printf %s {{.Path}}"
`
	if err := os.WriteFile(configPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, NewCommandRunner())
	got := mustRender(t, r, candidate("dogfood", dir, "workspaces", ""))
	if got != dir {
		t.Errorf("rendered command output = %q, want %q", got, dir)
	}
}

// TestRender_CustomCommand_FailureHiddenFromNormalOutput confirms a failing
// custom command is silently omitted, not surfaced as an error/warning, per
// the "hide command errors from normal preview output" rule.
func TestRender_CustomCommand_FailureHiddenFromNormalOutput(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewIdentity, "broken")
	cfg.Preview.Commands = map[string]config.PreviewCommand{
		"broken": {Command: "git -C {{.Path}} log"},
	}
	runner := &fakeRunner{err: map[string]error{"git": errors.New("exit 1")}}
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, runner)
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "workspaces", ""))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "foo\npath: /p/foo\nsource: workspaces"
	if res.Text != want {
		t.Errorf("got %q want %q (broken command must be silently omitted)", res.Text, want)
	}
}

// TestRender_UnknownSectionNameSkipped confirms a name that is neither a
// built-in nor a declared command is simply omitted (config validation
// already prevents this at Load time, but the renderer stays defensive).
func TestRender_UnknownSectionNameSkipped(t *testing.T) {
	t.Parallel()
	cfg := cfgWithDefault(config.PreviewIdentity, "does-not-exist")
	r := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "workspaces", ""))
	want := "foo\npath: /p/foo\nsource: workspaces"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestRender_CachesResult confirms a second Render call for the same
// candidate/config is served from cache (FromCache=true), avoiding a
// redundant git call.
func TestRender_CachesResult(t *testing.T) {
	t.Parallel()
	git := &fakeGit{summary: GitSummary{Branch: "main"}}
	r := NewRenderer(cfgWithDefault(config.PreviewGit), testTemplates, config.Probes{Git: true}, git, nil)
	cand := candidate("foo", "/p/foo", "workspaces", "")
	first, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if first.FromCache {
		t.Error("first render should not be from cache")
	}
	second, err := r.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !second.FromCache {
		t.Error("second render should be served from cache")
	}
	if git.calls != 1 {
		t.Errorf("git.Summary should only be called once, got %d calls", git.calls)
	}
}

func TestRenderer_WithSnapshotBuildsImmutableGeneration(t *testing.T) {
	cfg := cfgWithDefault(config.PreviewWorkspace, config.PreviewAgentStatus)
	first := source.Snapshot{
		Workspaces:         []source.Workspace{{ID: "w1", Label: "first"}},
		Tabs:               []source.Tab{{ID: "w1:t1", WorkspaceID: "w1", Label: "editor-one", Number: 1, PaneCount: 1}},
		Panes:              []source.Pane{{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: "/one", Focused: true, AgentStatus: "working"}},
		FocusedPaneID:      "w1:p1",
		FocusedWorkspaceID: "w1",
		FocusedTabID:       "w1:t1",
	}
	second := source.Snapshot{
		Workspaces:         []source.Workspace{{ID: "w1", Label: "second"}},
		Tabs:               []source.Tab{{ID: "w1:t1", WorkspaceID: "w1", Label: "editor-two", Number: 1, PaneCount: 1}},
		Panes:              []source.Pane{{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", CWD: "/two", Focused: true, AgentStatus: "idle"}},
		FocusedPaneID:      "w1:p1",
		FocusedWorkspaceID: "w1",
		FocusedTabID:       "w1:t1",
	}

	firstRenderer := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, WithSnapshot(first))
	secondRenderer := NewRenderer(cfg, testTemplates, config.Probes{}, nil, nil, WithSnapshot(second))
	cand := source.Candidate{Label: "workspace", Path: "/workspace", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}}

	firstText := mustRender(t, firstRenderer, cand)
	secondText := mustRender(t, secondRenderer, cand)
	if !strings.Contains(firstText, "editor-one") || !strings.Contains(firstText, "working") || strings.Contains(firstText, "editor-two") {
		t.Errorf("first generation renderer changed after second construction: %q", firstText)
	}
	if !strings.Contains(secondText, "editor-two") || !strings.Contains(secondText, "idle") || strings.Contains(secondText, "editor-one") {
		t.Errorf("second generation renderer did not use its own snapshot: %q", secondText)
	}
}

// TestResolvePreviewNames_AgentsHonorConfiguredPreview proves the agents
// source participates in the preview contract: an explicit
// [sources.agents].preview is applied to agents rows (it was previously
// parsed and validated but silently ignored), and an unconfigured agents
// source keeps falling through to preview.default.
func TestResolvePreviewNames_AgentsHonorConfiguredPreview(t *testing.T) {
	t.Parallel()

	load := func(t *testing.T, body string) *config.Config {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte("version = 2\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	agent := source.Candidate{Source: config.SourceAgents, Path: "/srv/ws1/src", Label: "codegen"}

	t.Run("configured sources.agents.preview is applied", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "[sources.agents]\npreview = [\""+config.PreviewAgentStatus+"\"]\n")
		got := resolvePreviewNames(cfg, agent)
		if len(got) != 1 || got[0] != config.PreviewAgentStatus {
			t.Errorf("agents sections = %v, want [%s]", got, config.PreviewAgentStatus)
		}
	})

	t.Run("unconfigured agents fall back to preview.default", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "")
		got := resolvePreviewNames(cfg, agent)
		want := []string{config.PreviewAgentStatus, config.PreviewIdentity, config.PreviewGit}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("agents sections = %v, want %v", got, want)
		}
	})

	t.Run("explicit empty agents preview means no sections", func(t *testing.T) {
		t.Parallel()
		cfg := load(t, "[sources.agents]\npreview = []\n")
		if got := resolvePreviewNames(cfg, agent); len(got) != 0 {
			t.Errorf("agents sections = %v, want none", got)
		}
	})
}
