package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
)

// --- helpers ---

func stripANSI(s string) string { return ansi.Strip(s) }

// previewLines composes the preview body of the row at cursorIdx for an
// 80x30 column and returns its plain lines.
func previewLines(m Model, cursorIdx int) []string {
	m.cursor = cursorIdx
	return strings.Split(stripANSI(m.previewBody(80, 30)), "\n")
}

func previewBodyAt(m Model, cursorIdx int) string {
	return strings.Join(previewLines(m, cursorIdx), "\n")
}

func previewBodyAtRaw(m Model, cursorIdx int) string {
	m.cursor = cursorIdx
	return m.previewBody(80, 30)
}

// sectionsRenderer returns a fixed Result with both Text and Sections.
type sectionsRenderer struct {
	sections []preview.Section
}

func (r sectionsRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	var blocks []string
	for _, s := range r.sections {
		blocks = append(blocks, s.Text)
	}
	return preview.Result{Text: strings.Join(blocks, "\n\n"), Sections: r.sections}, nil
}

// indexOfLineContaining returns the index of the first line in lines that
// contains substr, or -1 if none.
func indexOfLineContaining(lines []string, substr string) int {
	for i, l := range lines {
		if strings.Contains(l, substr) {
			return i
		}
	}
	return -1
}

// resolvedModel builds a sized model over cands whose preview already holds
// sections (no render in flight).
func resolvedModel(t *testing.T, cands []source.Candidate, tree *TreeExpander, sections ...preview.Section) Model {
	t.Helper()
	m := NewModelWithTree(cands, sectionsRenderer{sections: sections}, tree, Layout{Theme: testTheme(ThemePlain), HomeDir: "/home/dev"})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = sections
	return m
}

// twoTabTree is a two-tab workspace: tab 1 (active) runs a working claude
// beside a shell, tab 2 an idle codex.
func twoTabTree() *TreeExpander {
	return NewTreeExpanderFromSnapshot(source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1"}},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "editor", Number: 1, Focused: true},
			{ID: "t2", WorkspaceID: "w1", Label: "tests", Number: 2},
		},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "working"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t1"},
			{ID: "p3", WorkspaceID: "w1", TabID: "t2", Agent: "codex", AgentStatus: "idle"},
		},
	})
}

// === Title row ===

// TestPreviewTitle_NameAndKindPerRow proves the title row names the row as
// the row shows it and labels its kind on the right; the kind goes first
// when the column is short.
func TestPreviewTitle_NameAndKindPerRow(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	m.layout.Templates = tmpl.New("/home/dev")
	for _, tc := range []struct {
		name       string
		row        Row
		title, kin string
	}{
		{"herdr workspace", Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, "api", "workspace"},
		{"configured", Row{Kind: RowCandidate, Candidate: workspaceEntryCandidate("notes", "/n")}, "notes", "configured"},
		{"group", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "team", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}}, "team", "group"},
		{"zoxide path label, filename first", Row{Kind: RowCandidate, Candidate: zoxideCandidate("~/Proyectos/shep", "/home/dev/Proyectos/shep")}, "shep", "folder"},
		{"project", Row{Kind: RowCandidate, Candidate: projectCandidate("shep", "/p")}, "shep", "project"},
		{"worktree", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "fix", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true"}}}, "fix", "worktree"},
		{"session", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "main", Source: config.SourceSessions}}, "main", "session"},
		{"agent", Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix the parser", Source: config.SourceAgents, Meta: map[string]string{"agent": "claude"}}}, "fix the parser", "claude"},
		{"agent without a name", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "t", Source: config.SourceAgents}}, "t", "agent"},
		{"custom source", Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "PR 42", Source: "prs"}}, "PR 42", "prs"},
		{"tab", Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_number": "2"}}}, "2 api", "tab"},
		{"pane", Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh"}}, "zsh", "pane"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m.rows = []Row{tc.row}
			got := m.renderPreviewTitle(40)
			if ansi.StringWidth(got) != 40 {
				t.Errorf("title width = %d, want 40", ansi.StringWidth(got))
			}
			fields := strings.Fields(got)
			if !strings.HasPrefix(got, tc.title+" ") || fields[len(fields)-1] != tc.kin {
				t.Errorf("title = %q, want %q and kind %q", got, tc.title, tc.kin)
			}
		})
	}
	m.rows = []Row{{Kind: RowCandidate, Candidate: herdrCandidate("a-long-workspace-name", "/x", "w1")}}
	if got := strings.TrimSpace(m.renderPreviewTitle(24)); got != "a-long-workspace-name" {
		t.Errorf("short title = %q, want the kind dropped before the name", got)
	}
}

// === Location and meta lines ===

// TestPreview_LocationIsHomeAbbreviated proves the location line shows the
// path relative to home, and that no key/value identity text is printed for
// any row kind.
func TestPreview_LocationIsHomeAbbreviated(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		zoxideCandidate("shep", "/home/dev/Proyectos/shep"),
		herdrCandidate("api", "/home/dev/allsafe/api", "w1"),
	}
	m := resolvedModel(t, cands, twoTabTree(),
		preview.Section{Kind: config.PreviewIdentity, Text: "shep\npath: /home/dev/Proyectos/shep\nsource: zoxide"})
	for _, tc := range []struct{ label, want string }{{"shep", "~/Proyectos/shep"}, {"api", "~/allsafe/api"}} {
		i := slices.IndexFunc(m.rows, func(r Row) bool { return r.Candidate.Label == tc.label })
		want := tc.want
		lines := previewLines(m, i)
		if want != "" && lines[0] != want {
			t.Errorf("row %d location = %q, want %q", i, lines[0], want)
		}
		body := strings.Join(lines, "\n")
		for _, banned := range []string{"label ", "path ", "path:", "source", "kind ", "/home/dev"} {
			if strings.Contains(body, banned) {
				t.Errorf("row %d preview %q contains identity text %q", i, body, banned)
			}
		}
	}
}

// TestPreviewMeta_PerKind proves each row kind's meta line, singular and
// plural forms included.
func TestPreviewMeta_PerKind(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	m.layout.Templates = tmpl.New("/home/dev")
	m.tree = twoTabTree()
	m.spinner = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	working := m.agentStatusIcon("working")
	single := NewTreeExpanderFromSnapshot(source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w2"}},
		Tabs:       []source.Tab{{ID: "w2:t1", WorkspaceID: "w2"}},
		Panes:      []source.Pane{{ID: "w2:p1", WorkspaceID: "w2", TabID: "w2:t1"}},
	})
	for _, tc := range []struct {
		name     string
		tree     *TreeExpander
		row      Row
		sections []preview.Section
		want     string
	}{
		{"open workspace", nil, Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w1")}, nil, working + " working · 2 tabs · 3 panes"},
		{"open workspace without agents, singular", single, Row{Kind: RowCandidate, Candidate: herdrCandidate("api", "/srv/api", "w2")}, nil, "1 tab · 1 pane"},
		{"agent row", nil, Row{Kind: RowPane, Candidate: source.Candidate{Label: "fix", Source: config.SourceAgents, Meta: map[string]string{
			"agent_status": "blocked", "agent": "claude", "workspace_label": "/home/dev/api", "tab_label": "editor"}}}, nil, m.agentStatusIcon("blocked") + " blocked · claude · in ~/api › editor"},
		{"tree pane", nil, Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"workspace_label": "api", "tab_label": "editor"}}}, nil, "in api › editor"},
		{"tab", nil, Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "editor", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1", "tab_number": "1", "workspace_label": "api"}}}, nil, "tab 1 · 2 panes · in api"},
		{"clean repository", nil, Row{Kind: RowCandidate, Candidate: projectCandidate("shep", "/p")}, []preview.Section{{Kind: config.PreviewGit, Text: "git: main (clean)"}}, "on main · clean"},
		{"one change", nil, Row{Kind: RowCandidate, Candidate: zoxideCandidate("shep", "/p")}, []preview.Section{{Kind: config.PreviewGit, Text: "git: dev (1 changes)"}}, "on dev · 1 change"},
		{"changes", nil, Row{Kind: RowCandidate, Candidate: zoxideCandidate("shep", "/p")}, []preview.Section{{Kind: config.PreviewGit, Text: "git: dev (12 changes)"}}, "on dev · 12 changes"},
		{"worktree", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "fix", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "fix/parser", "head": "1a2b3c4d5e"}}},
			[]preview.Section{{Kind: config.PreviewGit, Text: "git: [worktree: fix/parser] 1a2b3c4 fix/parser (2 changes)"}}, "worktree on fix/parser · 1a2b3c4 · 2 changes"},
		{"configured template group", nil, Row{Kind: RowCandidate, Candidate: source.Candidate{Label: "team", Source: config.SourceWorkspaces, Meta: map[string]string{"template": "go", "group": "true"}}}, nil, "template go · group"},
		{"no git section", nil, Row{Kind: RowCandidate, Candidate: zoxideCandidate("shep", "/p")}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := m
			if tc.tree != nil {
				m.tree = tc.tree
			}
			m.previewSections = tc.sections
			got, _ := m.previewMeta(tc.row, 80)
			if got = stripANSI(got); got != stripANSI(tc.want) {
				t.Errorf("meta = %q, want %q", got, stripANSI(tc.want))
			}
		})
	}
}

// TestParseGitSummary proves the renderer's git line shapes parse.
func TestParseGitSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text   string
		branch string
		dirty  int
		ok     bool
	}{
		{"git: main (clean)", "main", 0, true},
		{"git: main (3 changes)", "main", 3, true},
		{"git: [worktree: fix/x] 1a2b3c4 fix/x (1 changes)", "fix/x", 1, true},
		{"git: weird", "", 0, false},
		{"git: main (lots)", "", 0, false},
	} {
		branch, dirty, ok := parseGitSummary(tc.text)
		if branch != tc.branch || dirty != tc.dirty || ok != tc.ok {
			t.Errorf("parseGitSummary(%q) = %q %d %v, want %q %d %v", tc.text, branch, dirty, ok, tc.branch, tc.dirty, tc.ok)
		}
	}
}

// === Tabs section ===

// TestTabsSection_FromTree proves the Tabs section lists the workspace's tabs
// from the snapshot tree — numbers right-aligned, labels in one column, each
// agent's status glyph and name, the pane count of a multi-pane tab — and
// falls back to the renderer's text when the tree lacks the workspace.
func TestTabsSection_FromTree(t *testing.T) {
	t.Parallel()
	workspace := preview.Section{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: editor * (2 panes)"}
	m := resolvedModel(t, []source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, twoTabTree(), workspace)
	idle, working := stripANSI(m.agentStatusIcon("idle")), stripANSI(m.agentStatusIcon("working"))
	lines := previewLines(m, 0)
	at := indexOfLineContaining(lines, "Tabs ")
	if at < 0 || !strings.HasPrefix(lines[at], "Tabs ──") {
		t.Fatalf("preview = %q, want a Tabs heading with its rule", lines)
	}
	if got, want := lines[at+1], "1  editor  "+working+" claude · 2 panes"; got != want {
		t.Errorf("tab 1 line = %q, want %q", got, want)
	}
	if got, want := lines[at+2], "2  tests   "+idle+" codex"; got != want {
		t.Errorf("tab 2 line = %q, want %q", got, want)
	}

	fallback := resolvedModel(t, []source.Candidate{herdrCandidate("api", "/srv/api", "w9")}, twoTabTree(), workspace)
	if body := previewBodyAt(fallback, 0); !strings.Contains(body, "Tabs ") || !strings.Contains(body, "tab 1: editor * (2 panes)") {
		t.Errorf("fallback preview = %q, want the renderer's workspace text under Tabs", body)
	}
}

// === Capture tail ===

// TestCaptureTail_FitsTheRoomLeft proves the capture keeps its newest lines:
// trailing blank lines trimmed, the last lines that fit, never fewer than
// minCaptureLines.
func TestCaptureTail_FitsTheRoomLeft(t *testing.T) {
	t.Parallel()
	capture := "one\ntwo\nthree\nfour\nfive\n\n  \n\x1b[0m\n"
	for _, tc := range []struct {
		fit  int
		want string
	}{
		{10, "one two three four five"},
		{4, "two three four five"},
		{1, "three four five"},
		{-5, "three four five"},
	} {
		if got := strings.Join(captureTail(strings.Split(capture, "\n"), tc.fit), " "); got != tc.want {
			t.Errorf("captureTail(fit %d) = %q, want %q", tc.fit, got, tc.want)
		}
	}
	if got := captureTail(strings.Split(" \n\n", "\n"), 10); got != nil {
		t.Errorf("whitespace-only capture = %q, want nil", got)
	}

	// End to end: at a 12-row column the capture gets what is left below
	// the location, meta and Tabs blocks.
	var lines []string
	for i := range 40 {
		lines = append(lines, "line-"+string(rune('a'+i%26)))
	}
	m := resolvedModel(t, []source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, twoTabTree(),
		preview.Section{Kind: config.PreviewWorkspace, Text: "workspace"},
		preview.Section{Kind: config.PreviewActivePane, Text: "active pane\n" + strings.Join(lines, "\n") + "\n\n"})
	m.cursor = 0
	body := strings.Split(stripANSI(m.previewBody(80, 12)), "\n")
	// location, meta, blank, Tabs heading, 2 tabs, blank, heading = 8 rows.
	if len(body) != 12 || !strings.HasPrefix(body[7], "Active pane ") || body[11] != lines[39] {
		t.Errorf("12-row preview = %q, want 4 capture lines ending with the newest", body)
	}
}

// === Sections ===

// TestCustomSections_TitledAndSeparate proves each custom section keeps
// its own heading (the title the renderer resolved) and its body unchanged
// (blank lines and a heading-like first line included), in configured order,
// with the dir listing under Files; an empty title draws the body alone.
func TestCustomSections_TitledAndSeparate(t *testing.T) {
	t.Parallel()
	m := resolvedModel(t, []source.Candidate{zoxideCandidate("shep", "/p")}, nil,
		preview.Section{Kind: "cluster", Title: "Cluster", Text: "workspace\nfake heading\n\nline2"},
		preview.Section{Kind: config.PreviewDir, Text: "README.md\nmain.go"},
		preview.Section{Kind: "recent_commits", Title: "Recent commits", Text: "abc fix"},
		preview.Section{Kind: "untitled", Title: "", Text: "bare output"},
	)
	body := previewBodyAt(m, 0)
	for _, want := range []string{"Cluster ─", "workspace\nfake heading\n\nline2", "Files ─", "README.md\nmain.go", "Recent commits ─", "abc fix", "bare output"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview = %q, missing %q", body, want)
		}
	}
	if c, f, r := strings.Index(body, "Cluster"), strings.Index(body, "Files"), strings.Index(body, "Recent commits"); c >= f || f >= r {
		t.Errorf("sections out of configured order: %q", body)
	}
	if strings.Contains(body, "Directory") || strings.Contains(body, "Tabs") || strings.Contains(body, "Untitled") {
		t.Errorf("preview = %q, sections must not merge, be reclassified or gain a heading", body)
	}
	if i := strings.Index(body, "bare output"); i < 0 || !strings.HasSuffix(strings.TrimRight(body[:i], " "), "\n\n") {
		t.Errorf("untitled section must follow a blank line with no heading: %q", body)
	}
}

// TestSessionInfo_AlignedTable proves session_info renders as an aligned
// key/value table under its heading.
func TestSessionInfo_AlignedTable(t *testing.T) {
	t.Parallel()
	section := preview.Section{Kind: config.PreviewSessionInfo, Text: "session\n  name: main\n  state: running\n  session dir: /tmp/s"}
	m := NewModelWithLayout([]source.Candidate{{Label: "main", Source: config.SourceSessions}}, sectionsRenderer{sections: []preview.Section{section}},
		Layout{Theme: testTheme(ThemePlain), SourceOrder: []string{config.SourceSessions}})
	m.previewLoading = false
	m.previewSections = []preview.Section{section}
	body := previewBodyAt(m, 0)
	if lines := strings.Split(body, "\n"); lines[0] != "Session "+strings.Repeat("─", 72) {
		t.Errorf("a session without a path starts with its table: %q", lines[0])
	}
	for _, want := range []string{"Session ─", "name         main", "state        running", "session dir  /tmp/s"} {
		if !strings.Contains(body, want) {
			t.Errorf("session preview = %q, missing %q", body, want)
		}
	}
}

// TestPreview_CaptureOSCSanitized proves the composed body is the
// containment boundary: OSC from a capture never reaches the viewport while
// its printable lines survive.
func TestPreview_CaptureOSCSanitized(t *testing.T) {
	t.Parallel()
	m := resolvedModel(t, []source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}, nil,
		preview.Section{Kind: config.PreviewActivePane, Text: "active pane\nline1\n\x1b]0;evil title\x07\nline3"})
	raw := previewBodyAtRaw(m, 0)
	if strings.Contains(raw, "\x1b]0;evil") {
		t.Errorf("OSC sequence reached the composed body: %q", raw)
	}
	if !strings.Contains(raw, "line1") || !strings.Contains(raw, "line3") {
		t.Errorf("capture lines lost: %q", raw)
	}
}

// TestPreview_TabAndPaneCaptures proves a tab row shows its active pane's
// capture under "Active pane" and a pane row its own under "Pane", both
// omitted when the capture is empty.
func TestPreview_TabAndPaneCaptures(t *testing.T) {
	t.Parallel()
	m := NewModelWithTree(nil, nil, twoTabTree(), Layout{Theme: testTheme(ThemeMocha)})
	m.rows = []Row{
		{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "editor", Path: "/srv/api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}}},
		{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Path: "/srv/api", Meta: map[string]string{"pane_id": "p2"}}},
	}
	m.previewText = "\x1b[31mcaptured\x1b[0m"
	for i, heading := range []string{"Active pane ", "Pane "} {
		lines := previewLines(m, i)
		at := indexOfLineContaining(lines, heading)
		if at < 0 || lines[at+1] != "captured" {
			t.Errorf("row %d preview = %q, want the capture under %q", i, lines, heading)
		}
	}
	if raw := previewBodyAtRaw(m, 0); !strings.Contains(raw, "\x1b[31mcaptured\x1b[0m") {
		t.Errorf("capture SGR must survive: %q", raw)
	}
	m.previewText = " \n "
	if body := previewBodyAt(m, 1); strings.Contains(body, "Pane") {
		t.Errorf("empty capture preview = %q, want no Pane section", body)
	}
}

// === Loading and error ===

// TestPreview_LoadingAndErrorStates proves the location and meta lines show
// at once while the render is in flight, followed by the loading line, and
// that a failure reads "Preview unavailable" with its reason.
func TestPreview_LoadingAndErrorStates(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("shep", "/home/dev/shep")}, blockingRenderer{}, Layout{Theme: testTheme(ThemePlain), HomeDir: "/home/dev"})
	m, _ = update(t, m, sizeMsg(120, 36))
	parts := m.composePreview(80, 30, nil)
	lines := strings.Split(stripANSI(m.previewBody(80, 30)), "\n")
	if lines[0] != "~/shep" || !strings.HasSuffix(lines[len(lines)-1], " Loading preview…") || !parts.animated {
		t.Errorf("loading preview = %q (animated %v), want the location then the loading line", lines, parts.animated)
	}
	m.previewLoading, m.previewErr = false, "preview error"
	if body := previewBodyAt(m, 0); !strings.HasSuffix(body, "! Preview unavailable") {
		t.Errorf("error preview = %q", body)
	}
	m.previewErr = "snapshot refresh failed"
	if body := previewBodyAt(m, 0); !strings.HasSuffix(body, "! Preview unavailable: snapshot refresh failed") {
		t.Errorf("error preview with a reason = %q", body)
	}
}

// === Memoization ===

// TestPreviewMemo_SpinnerTickKeepsTheBody proves a spinner tick neither
// recomposes a preview that draws no spinner nor touches the viewport.
func TestPreviewMemo_SpinnerTickKeepsTheBody(t *testing.T) {
	t.Parallel()
	capture := preview.Section{Kind: config.PreviewActivePane, Text: "active pane\nbuild ok"}
	still := resolvedModel(t, []source.Candidate{herdrCandidate("api", "/srv/api", "w9")}, twoTabTree(), capture)
	still, _ = update(t, still, sizeMsg(120, 36))
	composed, fitted := still.preview.composed, still.preview.fitted
	still, _ = update(t, still, spinner.TickMsg{ID: still.spinner.ID()})
	if still.preview.composed != composed || still.preview.fitted != fitted || still.preview.animated {
		t.Errorf("a tick recomposed a still preview: composed %d -> %d", composed, still.preview.composed)
	}
}

// TestPreviewMemo_AnimatedFrameReusesTheFittedCapture proves a spinner frame
// under an animated preview (a working agent in the Tabs section) redraws
// the frame in Tabs without sanitizing or fitting the capture again and
// without resetting the viewport's content, and that a new capture and a
// new column size invalidate the fitted lines.
func TestPreviewMemo_AnimatedFrameReusesTheFittedCapture(t *testing.T) {
	t.Parallel()
	capture := preview.Section{Kind: config.PreviewActivePane, Text: "active pane\n\x1b[31mbuild ok\x1b[0m\nline two"}
	workspace := preview.Section{Kind: config.PreviewWorkspace, Text: "workspace"}
	m := resolvedModel(t, []source.Candidate{herdrCandidate("api", "/srv/api", "w1")}, twoTabTree(), workspace, capture)
	m.spinner = spinner.New(spinner.WithSpinner(spinner.MiniDot))
	m, _ = update(t, m, sizeMsg(120, 36))
	if !m.preview.animated {
		t.Fatal("setup: a working agent in Tabs must animate the preview")
	}
	tabsLine := func(m Model) string {
		for _, line := range m.preview.lines {
			if strings.Contains(stripANSI(line), "claude") {
				return stripANSI(line)
			}
		}
		return ""
	}
	before, fitted, composed := tabsLine(m), m.preview.fitted, m.preview.composed
	m.viewport.SetContent("sentinel")
	m, _ = update(t, m, spinner.TickMsg{ID: m.spinner.ID()})
	if m.preview.composed != composed+1 {
		t.Fatalf("composed = %d, want one recomposition for the new frame", m.preview.composed)
	}
	if m.preview.fitted != fitted {
		t.Errorf("a frame fitted heavy text again: %d -> %d", fitted, m.preview.fitted)
	}
	if !strings.Contains(m.viewport.View(), "sentinel") {
		t.Error("a frame with an unchanged line count reset the viewport's content")
	}
	if after := tabsLine(m); after == before || !strings.Contains(strings.Join(viewLines(m), "\n"), after) {
		t.Errorf("Tabs line %q -> %q, want the next spinner frame on screen", before, after)
	}

	m.previewSections = []preview.Section{workspace, {Kind: config.PreviewActivePane, Text: "active pane\nfresh capture"}}
	m, _ = update(t, m, spinner.TickMsg{ID: m.spinner.ID()})
	if m.preview.fitted != fitted+1 || !strings.Contains(strings.Join(viewLines(m), "\n"), "fresh capture") {
		t.Errorf("a new capture was not fitted (fitted %d -> %d)", fitted, m.preview.fitted)
	}
	fitted = m.preview.fitted
	m, _ = update(t, m, sizeMsg(100, 30))
	if m.preview.fitted != fitted+1 {
		t.Errorf("a new column size reused lines fitted for the old one (fitted %d -> %d)", fitted, m.preview.fitted)
	}
}

// TestPreview_CaptureLinesFittedAndReset proves capture lines are cut to the
// column, padded to it, and closed with a style reset so their colors never
// bleed into the next cell.
func TestPreview_CaptureLinesFittedAndReset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, plain string
		reset     bool
	}{
		{"plain", "plain     ", false},
		{"\x1b[41mred\x1b[0m", "red       ", true},
		{"\x1b[41mstill open", "still open", true},
		{"0123456789abcdef", "0123456789", false},
		{"\x1b[1mwide line here\x1b[0m", "wide line ", true},
	} {
		got := fitPreviewLine(tc.in, 10)
		body := strings.TrimRight(got, " ")
		reset := strings.HasSuffix(body, "\x1b[m") || strings.HasSuffix(body, "\x1b[0m")
		if ansi.StringWidth(got) != 10 || stripANSI(got) != tc.plain || reset != tc.reset {
			t.Errorf("fitPreviewLine(%q) = %q, want %q (reset %v)", tc.in, got, tc.plain, tc.reset)
		}
	}
}

// TestPreview_CutDropsSequencesPastTheEdge proves a cut capture line keeps
// the colors of its visible cells but none of the sequences past the column
// edge, which would only add bytes to every frame, and that a wide glyph
// straddling the edge is dropped and its cell padded.
func TestPreview_CutDropsSequencesPastTheEdge(t *testing.T) {
	t.Parallel()
	line := "\x1b[31mab\x1b[32mcd\x1b[33mef\x1b[34mgh"
	got, w := cutPreviewLine(line, 4)
	if got != "\x1b[31mab\x1b[32mcd" || w != 4 {
		t.Errorf("cutPreviewLine = %q (%d), want the first four cells and their colors only", got, w)
	}
	if got, w := cutPreviewLine("abc\u4e16d", 4); got != "abc" || w != 3 {
		t.Errorf("cutPreviewLine(wide at edge) = %q (%d), want %q (3)", got, w, "abc")
	}
	if fitted := fitPreviewLine(line, 4); fitted != "\x1b[31mab\x1b[32mcd\x1b[m" {
		t.Errorf("fitPreviewLine = %q, want the cut closed by a reset", fitted)
	}
}

// TestHelpBody_BuiltOnlyWhileVisible proves the help text is not built while
// help is closed, is built when it opens, and is not rebuilt by unrelated
// messages while open.
func TestHelpBody_BuiltOnlyWhileVisible(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 36))
	m, _ = update(t, m, key("down"))
	if m.helpKey.built {
		t.Fatal("help body built while help is hidden")
	}
	m, _ = update(t, m, key("?"))
	if !m.helpKey.built || !strings.Contains(m.helpViewport.View(), "Navigate") {
		t.Fatal("help body not built when help opened")
	}
	built := m.helpKey
	m, _ = update(t, m, spinner.TickMsg{ID: m.spinner.ID()})
	if m.helpKey != built {
		t.Error("an unrelated message rebuilt the help body")
	}
	m, _ = update(t, m, sizeMsg(100, 30))
	if m.helpKey == built || m.helpViewport.Width != m.geometry().ContentWidth {
		t.Error("a resize did not rebuild the help body at the new size")
	}
}
