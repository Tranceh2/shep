package tui

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/theme"
)

// NewModel builds a model over candidates with the zero Layout. renderer
// may be nil (the preview pane then shows the built-in summary).
func NewModel(candidates []source.Candidate, renderer preview.Renderer) Model {
	return newModelWithLayout(candidates, renderer, context.TODO(), Layout{})
}

// NewModelWithTree builds a tree-wired model: tree supplies each open Herdr
// workspace's tabs and panes (nil degrades to flat rows), as RunWithSnapshot
// does from its startup generation.
func NewModelWithTree(candidates []source.Candidate, renderer preview.Renderer, tree *TreeExpander, layout Layout) Model {
	return newModelWithTreeLayout(candidates, renderer, context.TODO(), tree, layout)
}

// withActiveTab returns m switched to the tab id with the cursor reset, as
// the tab keys switch it (the rows are rebuilt by the next filter pass).
func withActiveTab(m Model, id string) Model {
	m.activeTab = id
	m.cursor = 0
	m.cursorTouched = false
	return m
}

// defaultTabIcon is the default icon of a Herdr tab row under the icons tier
// (config.DefaultPresentations).
func defaultTabIcon(tier string) string { return config.DefaultPresentations(tier).HerdrTab.Icon }

// kindPrefix is the tree prefix drawn before a row's icon (see treePrefix).
func (m Model) kindPrefix(row Row) string {
	indent, tree := m.treePrefix(row)
	return indent + tree
}

// matchRow scores one row against an unparsed query (see queryMatcher).
func matchRow(query string, c source.Candidate, kind RowKind) (score int, indexes []int, matched bool, original bool) {
	return newQueryMatcher(query).matchRow(c, kind)
}

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
	return strings.TrimSpace(ansi.Strip(m.renderFooter(m.geometry().ContentWidth)))
}

// promptText is the prompt row (list column) as plain text without padding.
func promptText(m Model) string {
	return strings.TrimSpace(ansi.Strip(m.renderPromptRow(m.geometry().ListWidth)))
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
	return strings.Split(ansi.Strip(m.View().Content), "\n")
}

// renderRowLine renders row as one list line of width cells, isCursor
// selecting it: a single-row entry point into rowRenderer for tests.
func (m Model) renderRowLine(row Row, isCursor bool, width int) string {
	v := m.buildRowView(row)
	r := m.newRowRenderer()
	return r.render(&v, isCursor, width)
}

// partText draws p the way a plain (unselected) row does, as plain text: a
// working status glyph shows the spinner's current frame.
func (m Model) partText(p *part) string {
	var b strings.Builder
	r := m.newRowRenderer()
	r.writePart(&b, p, r.styles.rowPlain.label, &r.styles.rowPlain)
	return ansi.Strip(b.String())
}

// rowPrimaryText returns the row's fixed prefix (tree prefix and icon)
// followed by its label as drawn (a status glyph included), and the prefix's
// rune length.
func (m Model) rowPrimaryText(row Row) (string, int) {
	v := m.buildRowView(row)
	prefix := v.indent + v.tree
	if v.icon.width > 0 {
		prefix += m.partText(&v.icon) + " "
	}
	return prefix + m.partText(&v.label), utf8.RuneCountInString(prefix)
}

// rowDisplayText returns rowPrimaryText's text and the row's detail text,
// without the marker.
func (m Model) rowDisplayText(row Row) (primary, detail string) {
	primary, _ = m.rowPrimaryText(row)
	v := m.buildRowView(row)
	return primary, m.partText(&v.detail)
}

// rowAccessoryText returns the row's marker part as plain text (status words
// are shown as their glyphs).
func (m Model) rowAccessoryText(row Row) string {
	v := m.buildRowView(row)
	return m.partText(&v.marker)
}

// withPresentation returns m drawing its rows with the built-in
// presentations for its icon tier, as edited by edit.
func (m Model) withPresentation(edit func(*config.Presentations)) Model {
	p := config.DefaultPresentations(m.layout.Icons)
	if edit != nil {
		edit(&p)
	}
	m.layout.Presentation = &p
	m.formats = newRowFormats(p, m.layout.IconColors)
	m.styles = newPalette(m.theme, m.formats.iconRefs)
	m.invalidateRowWindow()
	return m
}

// Theme names the tests build themes from (see testTheme): Herdr's default
// Catppuccin Mocha, and the no-color theme. They also name the golden
// fixtures.
const (
	ThemeMocha = "catppuccin"
	ThemePlain = "plain"
)

// testTheme builds the built-in theme called name (no Herdr inheritance).
func testTheme(name string) theme.Theme {
	t, err := theme.Build(name, nil, theme.HerdrTheme{}, true)
	if err != nil {
		panic(err)
	}
	return t
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

// reSGRParams captures the parameters of each SGR sequence (\x1b[...m).
var reSGRParams = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

// hasColor reports whether s sets a foreground or background color (SGR
// 30-49, 90-107); attributes such as bold, faint or underline are not
// colors.
func hasColor(s string) bool {
	for _, m := range reSGRParams.FindAllStringSubmatch(s, -1) {
		for _, p := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ';' || r == ':' }) {
			if n, err := strconv.Atoi(p); err == nil && (n >= 30 && n <= 49 || n >= 90 && n <= 107) {
				return true
			}
		}
	}
	return false
}
