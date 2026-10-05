package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

func TestWorkspaceLaunchRequestPrecedenceAndBypasses(t *testing.T) {
	t.Parallel()
	base := source.Candidate{Path: "/srv/services/platform-api", NormalizedPath: "/srv/services/platform-api", Label: "display", Source: config.SourceProjects}
	cases := []struct {
		name      string
		candidate source.Candidate
		cfg       *config.Config
		want      string
	}{
		{name: "first wildcard", candidate: base, cfg: namingConfig("general", []config.WildcardConfig{{Pattern: "**/services/*", WorkspaceName: "wildcard-one"}, {Pattern: "**/services/*", WorkspaceName: "wildcard-two"}}), want: "wildcard-one"},
		{name: "zoxide wildcard", candidate: source.Candidate{Path: base.Path, NormalizedPath: base.NormalizedPath, Label: "recent-directory", Source: config.SourceZoxide}, cfg: namingConfig("general-name", []config.WildcardConfig{{Pattern: "**/services/*", WorkspaceName: "wildcard-zoxide"}}), want: "wildcard-zoxide"},
		{name: "general", candidate: base, cfg: namingConfig("general-name", nil), want: "general-name"},
		{name: "normalized fallback", candidate: base, cfg: namingConfig("", nil), want: base.NormalizedPath},
		{name: "explicit workspace", candidate: source.Candidate{Path: base.Path, NormalizedPath: base.NormalizedPath, Label: "explicit", Source: config.SourceWorkspaces}, cfg: namingConfig("general-name", []config.WildcardConfig{{Pattern: "**", WorkspaceName: "wildcard"}}), want: "explicit"},
		{name: "custom source label", candidate: source.Candidate{Path: base.Path, NormalizedPath: base.NormalizedPath, Label: "custom source-label", Source: "kubernetes", Meta: map[string]string{"custom_source": "true"}}, cfg: namingConfig("general-name", []config.WildcardConfig{{Pattern: "**", WorkspaceName: "wildcard"}}), want: "custom source-label"},
		{name: "existing herdr bypass", candidate: source.Candidate{Path: base.Path, Label: "existing", Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}}, cfg: namingConfig("general-name", nil), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := New()
			app.cfg = tc.cfg
			request, err := app.workspaceLaunchRequest(tc.candidate)
			if err != nil {
				t.Fatalf("workspaceLaunchRequest: %v", err)
			}
			if got := string(request.WorkspaceName); got != tc.want {
				t.Fatalf("workspace name = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkspaceLaunchRequest_CustomSourceLabelsRemainDistinct(t *testing.T) {
	t.Parallel()
	app := New()
	app.cfg = namingConfig("general-name", []config.WildcardConfig{{Pattern: "**", WorkspaceName: "cluster-wildcard"}})
	candidates := []source.Candidate{
		{Path: "/srv/clusters", NormalizedPath: "/srv/clusters", Label: "kube-prod", Source: "kubernetes", Meta: map[string]string{"custom_source": "true", "command": "kubectl config get-contexts"}},
		{Path: "/srv/clusters", NormalizedPath: "/srv/clusters", Label: "kube-stage", Source: "kubernetes", Meta: map[string]string{"custom_source": "true", "command": "kubectl config get-contexts"}},
	}
	for _, candidate := range candidates {
		request, err := app.workspaceLaunchRequest(candidate)
		if err != nil {
			t.Fatalf("workspaceLaunchRequest(%q): %v", candidate.Label, err)
		}
		if got := string(request.WorkspaceName); got != candidate.Label {
			t.Errorf("workspace name for %q = %q, want label", candidate.Label, got)
		}
	}
}

func TestWorkspaceLaunchRequest_WorktreeConditionalTemplate(t *testing.T) {
	t.Parallel()
	cfg := namingConfig(`{{ if and .IsWorktree (not .IsMainWorktree) }}{{ .RepoName }}@{{ .Branch }}{{ else }}{{ .Path | osBase }}{{ end }}`, nil)

	cases := []struct {
		name      string
		candidate source.Candidate
		want      string
	}{
		{
			name: "primary checkout",
			candidate: source.Candidate{
				Path:           "/srv/projects/shep",
				NormalizedPath: "/srv/projects/shep",
				Label:          "shep",
				Source:         config.SourceProjects,
				Meta: map[string]string{
					"is_worktree":   "true",
					"main_worktree": "true",
					"repo":          "shep",
					"branch":        "main",
				},
			},
			want: "shep",
		},
		{
			name: "secondary linked worktree with branch",
			candidate: source.Candidate{
				Path:           "/trees/shep-feat",
				NormalizedPath: "/trees/shep-feat",
				Label:          "shep (feature/x)",
				Source:         config.SourceProjects,
				Meta: map[string]string{
					"is_worktree":   "true",
					"main_worktree": "false",
					"repo":          "shep",
					"branch":        "feature/x",
				},
			},
			want: "shep@feature/x",
		},
		{
			name: "secondary linked worktree detached",
			candidate: source.Candidate{
				Path:           "/trees/shep-detached",
				NormalizedPath: "/trees/shep-detached",
				Label:          "shep (1234567)",
				Source:         config.SourceProjects,
				Meta: map[string]string{
					"is_worktree":   "true",
					"main_worktree": "false",
					"repo":          "shep",
					"head":          "1234567890abcdef",
				},
			},
			want: "shep@1234567",
		},
		{
			name: "ordinary project without worktrees",
			candidate: source.Candidate{
				Path:           "/srv/projects/web-app",
				NormalizedPath: "/srv/projects/web-app",
				Label:          "web-app",
				Source:         config.SourceProjects,
			},
			want: "web-app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := New()
			app.cfg = cfg
			request, err := app.workspaceLaunchRequest(tc.candidate)
			if err != nil {
				t.Fatalf("workspaceLaunchRequest: %v", err)
			}
			if got := string(request.WorkspaceName); got != tc.want {
				t.Fatalf("workspace name = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLaunch_PathlessCustomSourceWorkspaceFailsFast(t *testing.T) {
	t.Parallel()
	app := New()
	app.cfg = namingConfig("general-name", nil)
	driver := &openDriver{detect: true}
	app.probes = config.Probes{Herdr: true}
	app.herdrDriver = driver
	var out, errOut strings.Builder
	_, err := app.launch(context.Background(), source.Candidate{
		Label:  "kube-prod",
		Source: "kubernetes",
		Meta:   map[string]string{"custom_source": "true", "command": "kubectl get pods"},
	}, tui.RowActionOpen, "workspace", nil, &out, &errOut)
	if !errors.Is(err, errExitOne) {
		t.Fatalf("launch error = %v, want errExitOne", err)
	}
	if got, want := errOut.String(), "--target=workspace requires a custom source row path\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if driver.lastCand.Path != "" || out.Len() != 0 {
		t.Fatalf("pathless custom source reached Herdr or stdout: candidate=%+v stdout=%q", driver.lastCand, out.String())
	}
}

func TestLaunch_RenderErrorOccursBeforeHerdr(t *testing.T) {
	t.Parallel()
	cfg := namingConfig(`{{ mustRegexMatch "[" .Path }}`, nil)
	driver := &openDriver{detect: true}
	app := New()
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}
	app.herdrDriver = driver
	var out, errOut strings.Builder
	_, err := app.launch(context.Background(), source.Candidate{Path: "/srv/api", Source: config.SourceProjects}, tui.RowActionOpen, "workspace", nil, &out, &errOut)
	if !errors.Is(err, errExitOne) {
		t.Fatalf("launch error = %v, want errExitOne", err)
	}
	if driver.lastCand.Path != "" || strings.Contains(errOut.String(), "herdr unavailable") {
		t.Fatalf("Herdr should not be invoked on render error: candidate=%+v stderr=%q", driver.lastCand, errOut.String())
	}
}

func TestWorkspaceNameIsSeparateFromPresentationAcrossRuntimeBoundaries(t *testing.T) {
	t.Parallel()
	cand := source.Candidate{
		Path:           "/srv/platform-api",
		NormalizedPath: "/srv/platform-api",
		Label:          "display-label",
		Source:         config.SourceProjects,
	}
	cfg := namingConfig("rendered-name", nil)
	app := New()
	app.cfg = cfg

	request, err := app.workspaceLaunchRequest(cand)
	if err != nil {
		t.Fatalf("workspaceLaunchRequest: %v", err)
	}
	if got, want := string(request.WorkspaceName), "rendered-name"; got != want {
		t.Fatalf("workspace name = %q, want %q", got, want)
	}
	matches := resolver.Match([]source.Candidate{cand}, "display-label")
	if len(matches) != 1 || matches[0].Label != cand.Label {
		t.Fatalf("search changed presentation candidate: %+v", matches)
	}
	if got := resolver.Match([]source.Candidate{cand}, "rendered-name"); len(got) != 0 {
		t.Fatalf("rendered name leaked into search: %+v", got)
	}
	tuiRows := tui.NewModelWithLayout([]source.Candidate{cand}, nil, tui.Layout{LabelFormats: tui.LabelFormats{Projects: "{{.Label}}"}})
	if body := tuiRows.View(); strings.Contains(body, "rendered-name") || !strings.Contains(body, cand.Label) {
		t.Fatalf("TUI view changed presentation: %q", body)
	}
	deduped := resolver.Dedup([]source.Candidate{cand})
	if len(deduped) != 1 || deduped[0].Path != cand.Path || deduped[0].Label != cand.Label {
		t.Fatalf("dedup changed candidate identity/presentation: %+v", deduped)
	}

	renderer := preview.NewRenderer(configWithPreviewIdentity(), config.Probes{}, nil, nil)
	previewResult, err := renderer.Render(context.Background(), cand)
	if err != nil {
		t.Fatalf("preview Render: %v", err)
	}
	if !strings.Contains(previewResult.Text, cand.Label) || strings.Contains(previewResult.Text, "rendered-name") {
		t.Fatalf("preview leaked launch name or lost label: %q", previewResult.Text)
	}

	var jsonOut bytes.Buffer
	if err := renderJSON(&jsonOut, []source.Candidate{cand}); err != nil {
		t.Fatalf("renderJSON: %v", err)
	}
	var listed []listCandidate
	if err := json.Unmarshal(jsonOut.Bytes(), &listed); err != nil {
		t.Fatalf("list JSON: %v", err)
	}
	if len(listed) != 1 || listed[0].Label != cand.Label || strings.Contains(jsonOut.String(), "rendered-name") {
		t.Fatalf("list JSON changed presentation: %s", jsonOut.String())
	}
	var tsvOut bytes.Buffer
	if err := renderTSV(&tsvOut, []source.Candidate{cand}); err != nil {
		t.Fatalf("renderTSV: %v", err)
	}
	if got := strings.Split(strings.TrimSuffix(tsvOut.String(), "\n"), "\t")[1]; got != cand.Label || strings.Contains(tsvOut.String(), "rendered-name") {
		t.Fatalf("list TSV changed presentation: %q", tsvOut.String())
	}
}

func TestDistinctCandidatesMayReachCreationWithSameRenderedName(t *testing.T) {
	t.Parallel()
	candidates := []source.Candidate{
		{Path: "/srv/one", NormalizedPath: "/srv/one", Label: "first", Source: config.SourceProjects},
		{Path: "/srv/two", NormalizedPath: "/srv/two", Label: "second", Source: config.SourceProjects},
	}
	app := New()
	app.cfg = namingConfig("shared-workspace", nil)
	for i, cand := range candidates {
		request, err := app.workspaceLaunchRequest(cand)
		if err != nil {
			t.Fatalf("candidate %d request: %v", i, err)
		}
		if got := string(request.WorkspaceName); got != "shared-workspace" {
			t.Fatalf("candidate %d workspace name = %q", i, got)
		}
	}
	if got := resolver.Dedup(candidates); len(got) != 2 || got[0].Path == got[1].Path {
		t.Fatalf("distinct candidate identities were deduplicated: %+v", got)
	}
	driver := &openDriver{lastAction: source.HerdrActionCreated, workspaceID: "created-one", rootTabID: "tab-one", rootPaneID: "pane-one"}
	app.layoutApplier = driver
	for i, cand := range candidates {
		if _, err := app.launchWorkspace(context.Background(), driver, cand, ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("candidate %d launch: %v", i, err)
		}
		if driver.lastWorkspaceName != "shared-workspace" {
			t.Fatalf("candidate %d creation label = %q", i, driver.lastWorkspaceName)
		}
		if driver.lastCand.Path != cand.Path || driver.lastCand.Label != cand.Label {
			t.Fatalf("candidate %d identity/presentation changed: %+v", i, driver.lastCand)
		}
	}
}

// configWithPreviewIdentity keeps the runtime separation test on the smallest
// preview boundary without invoking filesystem, Git, or Herdr probes.
func configWithPreviewIdentity() *config.Config {
	cfg := config.Defaults()
	cfg.Preview.Default = []string{config.PreviewIdentity}
	return cfg
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func namingConfig(general string, wildcards []config.WildcardConfig) *config.Config {
	cfg := config.Defaults()
	cfg.General.WorkspaceName = general
	cfg.Wildcards = wildcards
	return cfg
}
