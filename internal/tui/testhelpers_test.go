package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// fakeTreeDriver is a controllable source.HerdrDriver scoped to this
// package's tests. Only ListTabs/ListPanes/ReadPane are exercised by
// TreeExpander; every other method returns an error if ever called, so a
// test would fail loudly instead of silently succeeding on an unintended
// code path.
type fakeTreeDriver struct {
	tabs     []source.Tab
	panes    []source.Pane
	tabsErr  error
	panesErr error

	readText string
	readErr  error

	listTabsN  int
	listPanesN int
	readPaneN  int
}

func (f *fakeTreeDriver) Detect(context.Context) bool { return true }
func (f *fakeTreeDriver) ListWorkspaces(context.Context) ([]source.Workspace, error) {
	return nil, errors.New("fakeTreeDriver does not implement ListWorkspaces")
}
func (f *fakeTreeDriver) FocusOrCreate(context.Context, source.Candidate) (source.FocusResult, error) {
	return source.FocusResult{}, errors.New("fakeTreeDriver does not implement FocusOrCreate")
}

func (f *fakeTreeDriver) ListTabs(_ context.Context, _ string) ([]source.Tab, error) {
	f.listTabsN++
	if f.tabsErr != nil {
		return nil, f.tabsErr
	}
	return f.tabs, nil
}

func (f *fakeTreeDriver) ListPanes(_ context.Context, _ string) ([]source.Pane, error) {
	f.listPanesN++
	if f.panesErr != nil {
		return nil, f.panesErr
	}
	return f.panes, nil
}

func (f *fakeTreeDriver) ListAgents(context.Context) ([]source.Agent, error) {
	return nil, errors.New("fakeTreeDriver does not implement ListAgents")
}

func (f *fakeTreeDriver) ReadPane(_ context.Context, _ string, _ int) (string, error) {
	f.readPaneN++
	if f.readErr != nil {
		return "", f.readErr
	}
	return f.readText, nil
}

func (f *fakeTreeDriver) CreateTab(context.Context, string, string, string, bool) (source.Tab, source.Pane, error) {
	return source.Tab{}, source.Pane{}, errors.New("fakeTreeDriver does not implement CreateTab")
}
func (f *fakeTreeDriver) RenameTab(context.Context, string, string) error {
	return errors.New("fakeTreeDriver does not implement RenameTab")
}
func (f *fakeTreeDriver) SplitPane(context.Context, string, string, float64, string, bool) (source.Pane, error) {
	return source.Pane{}, errors.New("fakeTreeDriver does not implement SplitPane")
}
func (f *fakeTreeDriver) RunPane(context.Context, string, string) error {
	return errors.New("fakeTreeDriver does not implement RunPane")
}
func (f *fakeTreeDriver) FocusTab(context.Context, string) error {
	return errors.New("fakeTreeDriver does not implement FocusTab")
}
func (f *fakeTreeDriver) CurrentPane(context.Context) (source.Pane, error) {
	return source.Pane{}, errors.New("fakeTreeDriver does not implement CurrentPane")
}

// stubRenderer returns a fixed Result; used where only "a Renderer is
// wired" matters, not its actual content.
type stubRenderer struct{}

func (stubRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: "stub preview text"}, nil
}

// errRenderer always fails; used to exercise the preview error path.
type errRenderer struct{}

func (errRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{}, errors.New("boom")
}

// blockingRenderer never resolves on its own; used to inspect the model
// while a render is still "in flight" (loading state) without racing a
// goroutine. Tests drive completion manually via previewResponseMsg.
type blockingRenderer struct{}

func (blockingRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: "should never be observed synchronously"}, nil
}

// herdrCandidate builds a minimal SourceHerdr workspace candidate for tests.
func herdrCandidate(label, path, workspaceID string) source.Candidate {
	return source.Candidate{
		Label: label, Path: path, NormalizedPath: path,
		Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": workspaceID},
	}
}

// zoxideCandidate builds a minimal SourceZoxide candidate for tests.
func zoxideCandidate(label, path string) source.Candidate {
	return source.Candidate{Label: label, Path: path, NormalizedPath: path, Source: config.SourceZoxide}
}

// projectCandidate builds a minimal SourceProjects candidate for tests.
func projectCandidate(label, path string) source.Candidate {
	return source.Candidate{Label: label, Path: path, NormalizedPath: path, Source: config.SourceProjects}
}

// workspaceEntryCandidate builds a minimal SourceWorkspaces candidate.
func workspaceEntryCandidate(label, path string) source.Candidate {
	return source.Candidate{Label: label, Path: path, NormalizedPath: path, Source: config.SourceWorkspaces}
}

// --- Phase 4 golden scenario renderers ---

// phase4WorkspaceRenderer emits a deterministic Herdr workspace preview with
// every known section: identity, workspace, agent_status, active_pane. Used
// by the workspace_active_pane golden scenario to exercise the Phase 4
// recomposition (compact identity → inline agent status → workspace summary
// → Active pane + capture LAST).
type phase4WorkspaceRenderer struct{}

func (phase4WorkspaceRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	sections := []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api * (2 panes)\n  pane p1 * /srv/api"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: working"},
		{Kind: config.PreviewActivePane, Text: "active pane\n┌────────┐\n│ pane 1 │\n└────────┘"},
	}
	text := strings.Join([]string{
		"backend", "path: /srv/backend", "source: herdr", "",
		"workspace", "  tab 1: api * (2 panes)", "  pane p1 * /srv/api", "",
		"agent status", "  status: working", "",
		"active pane", "┌────────┐", "│ pane 1 │", "└────────┘",
	}, "\n")
	return preview.Result{Text: text, Sections: sections}, nil
}

// phase4ProjectRenderer emits a deterministic project preview: identity,
// git, and a dir listing. Used by the project_preview golden scenario to
// exercise the Phase 4 recomposition (identity → git → Directory last).
type phase4ProjectRenderer struct{}

func (phase4ProjectRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	sections := []preview.Section{
		{Kind: config.PreviewIdentity, Text: "shep\npath: /home/dev/shep\nsource: projects"},
		{Kind: config.PreviewGit, Text: "git: main (2 changes)"},
		{Kind: config.PreviewDir, Text: "-rw-r--r-- 1 user staff 1024 Jul 10 README.md\n-rw-r--r-- 1 user staff  512 Jul 10 main.go"},
	}
	text := strings.Join([]string{
		"shep", "path: /home/dev/shep", "source: projects", "",
		"git: main (2 changes)", "",
		"-rw-r--r-- 1 user staff 1024 Jul 10 README.md",
		"-rw-r--r-- 1 user staff  512 Jul 10 main.go",
	}, "\n")
	return preview.Result{Text: text, Sections: sections}, nil
}

// phase4ZoxideRenderer emits a deterministic zoxide preview: identity and a
// dir listing (no git). Used by the zoxide_preview golden scenario to
// exercise the Phase 4 recomposition (identity → Directory last, full path).
type phase4ZoxideRenderer struct{}

func (phase4ZoxideRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	sections := []preview.Section{
		{Kind: config.PreviewIdentity, Text: "tmp\npath: /tmp\nsource: zoxide"},
		{Kind: config.PreviewDir, Text: "drwxr-xr-x 2 user staff 64 Jul 10 .\ndrwxr-xr-x 5 user staff 160 Jul 10 .."},
	}
	text := strings.Join([]string{
		"tmp", "path: /tmp", "source: zoxide", "",
		"drwxr-xr-x 2 user staff 64 Jul 10 .",
		"drwxr-xr-x 5 user staff 160 Jul 10 ..",
	}, "\n")
	return preview.Result{Text: text, Sections: sections}, nil
}
