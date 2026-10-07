package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// fakeTreeDriver supplies immutable snapshot records plus the dedicated live
// pane-read command to TUI tests.
type fakeTreeDriver struct {
	tabs     []source.Tab
	panes    []source.Pane
	readText string
	readErr  error

	readPaneN int
}

func (f *fakeTreeDriver) Snapshot(context.Context) (source.Snapshot, error) {
	snapshot := source.Snapshot{Tabs: append([]source.Tab(nil), f.tabs...), Panes: append([]source.Pane(nil), f.panes...)}
	for _, tab := range snapshot.Tabs {
		snapshot.Workspaces = append(snapshot.Workspaces, source.Workspace{ID: tab.WorkspaceID})
	}
	return snapshot, nil
}

func (f *fakeTreeDriver) ReadPane(_ context.Context, _ string, _ int) (string, error) {
	f.readPaneN++
	if f.readErr != nil {
		return "", f.readErr
	}
	return f.readText, nil
}

func treeFromFake(driver *fakeTreeDriver) *TreeExpander {
	snapshot := source.Snapshot{Tabs: append([]source.Tab(nil), driver.tabs...), Panes: append([]source.Pane(nil), driver.panes...)}
	workspaces := map[string]bool{}
	for _, tab := range snapshot.Tabs {
		if tab.WorkspaceID != "" {
			workspaces[tab.WorkspaceID] = true
		}
	}
	for _, pane := range snapshot.Panes {
		if pane.WorkspaceID != "" {
			workspaces[pane.WorkspaceID] = true
		}
	}
	for workspaceID := range workspaces {
		snapshot.Workspaces = append(snapshot.Workspaces, source.Workspace{ID: workspaceID})
	}
	return NewTreeExpanderFromSnapshot(snapshot)
}

// footerText is the footer row as the user reads it: plain text without the
// row's padding, at the model's own content width.
func footerText(m Model) string {
	return strings.TrimSpace(stripNonSGRANSI(m.renderFooter(m.geometry().ContentWidth)))
}

// promptText is the prompt row (list column) as plain text without padding.
func promptText(m Model) string {
	return strings.TrimSpace(stripNonSGRANSI(m.renderPromptRow(m.geometry().ListWidth)))
}

// hasHint reports whether hints offers key with label.
func hasHint(hints []footerHint, key, label string) bool {
	return slices.ContainsFunc(hints, func(h footerHint) bool { return h.key == key && h.label == label })
}

// hasHintKey reports whether hints offers key under any label.
func hasHintKey(hints []footerHint, key string) bool {
	return slices.ContainsFunc(hints, func(h footerHint) bool { return h.key == key })
}

// viewLines renders m and splits the plain-text frame into its lines.
func viewLines(m Model) []string {
	return strings.Split(stripNonSGRANSI(m.View()), "\n")
}

// renderRowLine renders row as one list line of width cells, isCursor
// selecting it: a single-row entry point into rowRenderer for tests.
func (m Model) renderRowLine(row Row, isCursor bool, width int) string {
	v := m.buildRowView(row)
	r := m.newRowRenderer()
	return r.render(&v, isCursor, width)
}

// rowPrimaryText returns the row's fixed prefix (tree prefix, icon, status
// glyph) followed by its primary text, and the prefix's rune length.
func (m Model) rowPrimaryText(row Row) (string, int) {
	v := m.buildRowView(row)
	prefix := v.indent + v.tree
	if v.icon != "" {
		prefix += v.icon + " "
	}
	if v.statusGlyph {
		prefix += m.agentStatusIcon(v.status) + " "
	}
	return prefix + v.primary, utf8.RuneCountInString(prefix)
}

// rowDisplayText returns rowPrimaryText's text and the row's secondary
// (filename-first parent) text, without accessories.
func (m Model) rowDisplayText(row Row) (primary, secondary string) {
	primary, _ = m.rowPrimaryText(row)
	return primary, m.buildRowView(row).secondary
}

// rowAccessoryText returns the row's accessories as plain text, joined by
// single spaces (status words are shown as their glyphs).
func (m Model) rowAccessoryText(row Row) string {
	v := m.buildRowView(row)
	parts := make([]string, len(v.accessories))
	for i, a := range v.accessories {
		parts[i] = a.text
		if a.role == accessoryStatus {
			parts[i] = m.agentStatusIcon(a.text)
		}
	}
	return stripNonSGRANSI(strings.Join(parts, " "))
}

// agentStatusIcon renders status as its glyph in the plain row's style.
func (m Model) agentStatusIcon(status string) string {
	set := m.icons()
	return statusGlyph(&set, m.spinner, status, m.styles.statusStyle(status))
}

// previewBody composes the highlighted row's preview for a width x height
// column as text: the fitted lines joined, their padding trimmed.
func (m Model) previewBody(width, height int) string {
	lines := m.composePreview(width, height, nil).lines
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(out, "\n")
}

// runBatch runs cmd and, when it is a tea.Batch, each command it carries.
// Use it only for commands that do not block (a spinner's first Tick returns
// at once; its later ticks wait on a timer).
func runBatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			runBatch(c)
		}
	}
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
