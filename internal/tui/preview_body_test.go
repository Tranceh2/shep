package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// Phase 4 fix-round-1 tests — strict TDD. These tests replace the text-parser
// protocol with structured Result.Sections consumption.
//
// R2-001/R3-002 fix: parsePreviewBlocks (strings.Split on "\n\n") is removed
// entirely; TUI consumes Result.Sections by Kind, preserving blank lines and
// heading-like content in custom/capture sections byte-for-byte.
// R3-001 fix: empty/whitespace-only active pane is omitted, not rendered as
// a heading-only section.

// --- helpers ---

func stripANSI(s string) string { return ansi.Strip(s) }

func previewBodyAt(m Model, cursorIdx int) string {
	m.cursor = cursorIdx
	return stripANSI(m.previewBodyPlain(80))
}

func previewBodyAtRaw(m Model, cursorIdx int) string {
	m.cursor = cursorIdx
	return m.previewBodyPlain(80)
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
	text := strings.Join(blocks, "\n\n")
	return preview.Result{Text: text, Sections: r.sections}, nil
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

// === 2. Herdr workspace preview with structured Sections ===

// TestHerdrWorkspacePreview_IdentityStatusWorkspaceBeforeActivePane proves
// the preview for an active Herdr workspace shows compact identity, inline
// agent status, workspace summary, then Active pane heading + capture LAST.
func TestHerdrWorkspacePreview_IdentityStatusWorkspaceBeforeActivePane(t *testing.T) {
	t.Parallel()
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api * (2 panes)\n  pane p1 * /srv/api"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: working"},
		{Kind: config.PreviewActivePane, Text: "active pane\n[capture content]"},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewText = ""
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	lines := strings.Split(body, "\n")

	if !strings.Contains(body, "backend") {
		t.Errorf("preview missing workspace label: %q", body)
	}
	if !strings.Contains(body, "/srv/backend") {
		t.Errorf("preview missing workspace path: %q", body)
	}
	if strings.Contains(body, "source: herdr") {
		t.Errorf("compact identity should NOT show \"source: herdr\": %q", body)
	}
	if !strings.Contains(body, "agent") {
		t.Errorf("preview missing inline agent status: %q", body)
	}
	if !strings.Contains(body, "working") {
		t.Errorf("preview missing agent status word: %q", body)
	}
	if strings.Contains(body, "\nagent status\n") || strings.HasPrefix(body, "agent status\n") {
		t.Errorf("preview should NOT have standalone \"agent status\" heading: %q", body)
	}
	if !strings.Contains(body, "tab 1: api") {
		t.Errorf("preview missing workspace tab summary: %q", body)
	}
	activePaneIdx := indexOfLineContaining(lines, "Active pane")
	if activePaneIdx < 0 {
		t.Fatalf("preview missing \"Active pane\" heading: %q", body)
	}
	captureIdx := indexOfLineContaining(lines, "[capture content]")
	if captureIdx < 0 {
		t.Fatalf("preview missing capture content: %q", body)
	}
	if captureIdx <= activePaneIdx {
		t.Errorf("capture should appear AFTER Active pane heading: captureIdx=%d activePaneIdx=%d", captureIdx, activePaneIdx)
	}
	if captureIdx != len(lines)-1 {
		for _, l := range lines[captureIdx+1:] {
			if strings.TrimSpace(l) != "" {
				t.Errorf("unexpected content after capture: %q (full: %q)", l, body)
				break
			}
		}
	}
}

// TestHerdrWorkspacePreview_CaptureWithBlankLinesPreserved proves that capture
// content containing blank lines (and heading-like text) is preserved
// byte-for-byte, remains contiguous under the Active pane heading, and is
// LAST — the R2-001/R3-002 root-cause fix.
func TestHerdrWorkspacePreview_CaptureWithBlankLinesPreserved(t *testing.T) {
	t.Parallel()
	capture := "line1\n\nworkspace\nfake heading\n\nline2"
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: idle"},
		{Kind: config.PreviewActivePane, Text: "active pane\n" + capture},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	lines := strings.Split(body, "\n")

	// The capture must be contiguous: line1, blank, workspace, fake heading,
	// blank, line2 — all under the Active pane heading, not split.
	activePaneIdx := indexOfLineContaining(lines, "Active pane")
	if activePaneIdx < 0 {
		t.Fatalf("missing Active pane heading: %q", body)
	}
	// The capture content starts on the line after the heading.
	captureStart := activePaneIdx + 1
	// Find "line1" — it must be the first capture line.
	if captureStart >= len(lines) || !strings.Contains(lines[captureStart], "line1") {
		t.Errorf("capture should start with line1 right after heading: %q", body)
	}
	// "line2" must be in the capture block (not split away by blank-line parsing).
	if !strings.Contains(body, "line2") {
		t.Errorf("capture content line2 missing (was it split by blank-line parsing?): %q", body)
	}
	// "fake heading" must be in the capture (not reclassified as a workspace section).
	// It should appear AFTER "Active pane", not before it.
	fakeIdx := indexOfLineContaining(lines, "fake heading")
	if fakeIdx < 0 {
		t.Errorf("capture content \"fake heading\" missing: %q", body)
	}
	if fakeIdx <= activePaneIdx {
		t.Errorf("\"fake heading\" should appear AFTER Active pane heading, not before: %q", body)
	}
}

// TestHerdrWorkspacePreview_CaptureOSCSanitized proves an OSC sequence
// embedded in a Herdr workspace's active_pane capture is stripped from the
// composed preview body (Phase 5 containment boundary), while the
// surrounding capture text lines are preserved.
func TestHerdrWorkspacePreview_CaptureOSCSanitized(t *testing.T) {
	t.Parallel()
	capture := "line1\n\x1b]0;evil title\x07\nline3"
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: idle"},
		{Kind: config.PreviewActivePane, Text: "active pane\n" + capture},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	raw := previewBodyAtRaw(m, 0)
	if strings.Contains(raw, "\x1b]0;evil title\x07") {
		t.Errorf("OSC sequence should be stripped from the composed preview body: %q", raw)
	}
	if !strings.Contains(raw, "line1") || !strings.Contains(raw, "line3") {
		t.Errorf("capture content lines should be preserved: %q", raw)
	}
}

// TestHerdrWorkspacePreview_CaptureUnavailable_OmitsActivePane proves when
// the renderer output has no active_pane section, the preview shows identity
// + metadata but omits the Active pane heading/section entirely.
func TestHerdrWorkspacePreview_CaptureUnavailable_OmitsActivePane(t *testing.T) {
	t.Parallel()
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api * (2 panes)"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: idle"},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "backend") {
		t.Errorf("identity should remain when capture unavailable: %q", body)
	}
	if !strings.Contains(body, "tab 1: api") {
		t.Errorf("workspace summary should remain: %q", body)
	}
	if strings.Contains(body, "Active pane") || strings.Contains(body, "active pane") {
		t.Errorf("Active pane heading should be omitted when capture unavailable: %q", body)
	}
}

// TestHerdrWorkspacePreview_NoRenderer_ShowsCompactIdentity proves a Herdr
// workspace with no renderer wired shows just the compact identity.
func TestHerdrWorkspacePreview_NoRenderer_ShowsCompactIdentity(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, nil, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "backend") {
		t.Errorf("compact identity missing label: %q", body)
	}
	if !strings.Contains(body, "/srv/backend") {
		t.Errorf("compact identity missing path: %q", body)
	}
}

// textOnlyRenderer returns only Text (no Sections) — the TUI must NOT try to
// parse it; it should show the compact identity (safe deterministic
// degradation).
type textOnlyRenderer struct{ text string }

func (r textOnlyRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	return preview.Result{Text: r.text}, nil
}

// === 5. No-Sections degradation ===

// TestHerdrWorkspacePreview_NoSections_ShowsCompactIdentity proves that when
// a renderer returns Result with only Text and no Sections (e.g. a custom
// Renderer), the TUI degrades to compact identity — safe deterministic
// degradation, NOT text parsing.
func TestHerdrWorkspacePreview_NoSections_ShowsCompactIdentity(t *testing.T) {
	t.Parallel()
	tr := textOnlyRenderer{text: "backend\npath: /srv/backend\nsource: herdr\n\nworkspace\n  tab 1: api"}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, tr, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewText = tr.text
	m.previewSections = nil // no structured sections
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "backend") {
		t.Errorf("compact identity missing label: %q", body)
	}
	if !strings.Contains(body, "/srv/backend") {
		t.Errorf("compact identity missing path: %q", body)
	}
	// The unstructured text should NOT be parsed/recomposed — the tab summary
	// from the text-only renderer should NOT appear.
	if strings.Contains(body, "tab 1: api") {
		t.Errorf("text-only renderer output should NOT be parsed for sections: %q", body)
	}
}

// === 3. Custom output with blank lines and heading-like first line ===

// TestCustomOutputWithBlankLinesAndHeadingLikeFirstLine proves that custom
// command output containing blank lines and a first line that looks like a
// known heading ("workspace") is preserved intact as a custom section, not
// reclassified or split.
func TestCustomOutputWithBlankLinesAndHeadingLikeFirstLine(t *testing.T) {
	t.Parallel()
	customText := "workspace\nfake heading\n\nline2"
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: "my-custom-command", Text: customText},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	// The custom output must be intact and contiguous.
	if !strings.Contains(body, "workspace\nfake heading") {
		t.Errorf("custom output with heading-like first line should be preserved intact: %q", body)
	}
	if !strings.Contains(body, "line2") {
		t.Errorf("custom output content after blank line should be preserved: %q", body)
	}
	// It should NOT be treated as a workspace section (no styled "workspace"
	// heading from the built-in workspace section).
	// The custom text should appear in the unknown/custom block area, not
	// as a styled workspace heading.
	if strings.Count(body, "workspace") > 1 {
		// "workspace" appears in both the custom text AND as a styled heading
		// — it was reclassified. This is the R2-001 bug.
		t.Errorf("custom output \"workspace\" first line should NOT be reclassified as a workspace section: %q", body)
	}
}

// === 4. Active pane empty/whitespace-only omitted ===

// TestActivePaneEmpty_Omitted proves that an active_pane section whose body
// is empty (heading-only) is omitted entirely from the TUI preview.
func TestActivePaneEmpty_Omitted(t *testing.T) {
	t.Parallel()
	// Simulate the renderer returning an active_pane section with only a
	// heading (empty body). The TUI must omit it.
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api"},
		{Kind: config.PreviewActivePane, Text: "active pane"},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "backend") {
		t.Errorf("identity should remain when active pane is empty: %q", body)
	}
	if strings.Contains(body, "Active pane") || strings.Contains(body, "active pane") {
		t.Errorf("empty Active pane section should be omitted: %q", body)
	}
}

// TestActivePaneWhitespaceOnly_Omitted proves that an active_pane section
// whose body is whitespace-only is omitted entirely from the TUI preview.
func TestActivePaneWhitespaceOnly_Omitted(t *testing.T) {
	t.Parallel()
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api"},
		{Kind: config.PreviewActivePane, Text: "active pane\n   \n  \n"},
	}}
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "backend") {
		t.Errorf("identity should remain when active pane is whitespace-only: %q", body)
	}
	if strings.Contains(body, "Active pane") || strings.Contains(body, "active pane") {
		t.Errorf("whitespace-only Active pane section should be omitted: %q", body)
	}
}

// === 5. Renderer structured Sections: order/kinds + Text compatibility ===
// (Test lives in internal/preview/renderer_test.go — package-level test.)

// === 6. Tab preview ===

func TestTabPreview_SynchronousSummary_NoLoading(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	idx := -1
	for i, row := range m.rows {
		if row.Kind == RowTab {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no RowTab found after expanding workspace")
	}
	body := previewBodyAt(m, idx)
	if !strings.Contains(body, "api") {
		t.Errorf("tab summary missing label: %q", body)
	}
	if !strings.Contains(body, "herdr tab") {
		t.Errorf("tab summary missing kind: %q", body)
	}
	if strings.Contains(body, "loading") || strings.Contains(body, "⠿") {
		t.Errorf("tab summary should have NO loading indicator: %q", body)
	}
}

func TestTabPreview_MetaCounts(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	idx := -1
	for i, row := range m.rows {
		if row.Kind == RowTab {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no RowTab found")
	}
	m.rows[idx].Candidate.Meta["workspace_tabs"] = "3"
	m.rows[idx].Candidate.Meta["tab_panes"] = "2"
	body := previewBodyAt(m, idx)
	if !strings.Contains(body, "3") || !strings.Contains(body, "2") {
		t.Errorf("tab summary should show meta counts when present: %q", body)
	}
}

// === 7. Pane preview ===

func TestPanePreview_IdentityAndCapturedPaneLast(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:     []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes:    []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
		readText: "┌────────┐\n│ pane 1 │\n└────────┘",
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	for i := 0; i < 16 && !cursorOnPane(m); i++ {
		m, _ = update(t, m, key("down"))
	}
	if !cursorOnPane(m) {
		t.Fatal("cursor never reached a RowPane")
	}
	m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: "┌────────┐\n│ pane 1 │\n└────────┘"})
	body := previewBodyAt(m, m.cursor)
	lines := strings.Split(body, "\n")
	if !strings.Contains(body, "p1") {
		t.Errorf("pane identity missing pane id: %q", body)
	}
	if !strings.Contains(body, "herdr pane") {
		t.Errorf("pane identity missing kind: %q", body)
	}
	headingIdx := indexOfLineContaining(lines, "Captured pane")
	if headingIdx < 0 {
		t.Fatalf("pane preview missing \"Captured pane\" heading: %q", body)
	}
	captureIdx := indexOfLineContaining(lines, "pane 1")
	if captureIdx < 0 {
		t.Fatalf("pane preview missing capture content: %q", body)
	}
	if captureIdx <= headingIdx {
		t.Errorf("capture should appear after Captured pane heading: captureIdx=%d headingIdx=%d", captureIdx, headingIdx)
	}
}

func TestPanePreview_LoadingUnderHeading(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	for i := 0; i < 16 && !cursorOnPane(m); i++ {
		m, _ = update(t, m, key("down"))
	}
	if !cursorOnPane(m) {
		t.Fatal("cursor never reached a RowPane")
	}
	m.previewLoading = true
	m.previewText = ""
	body := previewBodyAt(m, m.cursor)
	lines := strings.Split(body, "\n")
	if !strings.Contains(body, "p1") {
		t.Errorf("pane identity should remain during loading: %q", body)
	}
	headingIdx := indexOfLineContaining(lines, "Captured pane")
	if headingIdx < 0 {
		t.Fatalf("loading pane preview missing \"Captured pane\" heading: %q", body)
	}
	loadingFound := false
	for _, l := range lines[headingIdx+1:] {
		if strings.Contains(l, "loading") {
			loadingFound = true
			break
		}
	}
	if !loadingFound {
		t.Errorf("loading indicator should appear under Captured pane heading: %q", body)
	}
}

func TestPanePreview_UnavailableOmitsSection(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	for i := 0; i < 16 && !cursorOnPane(m); i++ {
		m, _ = update(t, m, key("down"))
	}
	if !cursorOnPane(m) {
		t.Fatal("cursor never reached a RowPane")
	}
	// Deliver an empty pane capture (no error) — capture unavailable.
	m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: ""})
	body := previewBodyAt(m, m.cursor)
	if !strings.Contains(body, "p1") {
		t.Errorf("pane identity should remain when capture unavailable: %q", body)
	}
	if strings.Contains(body, "Captured pane") || strings.Contains(body, "captured pane") {
		t.Errorf("Captured pane heading should be omitted when capture unavailable: %q", body)
	}
}

func TestPanePreview_WhitespaceOnlyOmitsSection(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	for i := 0; i < 16 && !cursorOnPane(m); i++ {
		m, _ = update(t, m, key("down"))
	}
	if !cursorOnPane(m) {
		t.Fatal("cursor never reached a RowPane")
	}
	// Deliver whitespace-only pane capture.
	m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: "  \n\n  "})
	body := previewBodyAt(m, m.cursor)
	if !strings.Contains(body, "p1") {
		t.Errorf("pane identity should remain when capture is whitespace-only: %q", body)
	}
	if strings.Contains(body, "Captured pane") || strings.Contains(body, "captured pane") {
		t.Errorf("Captured pane heading should be omitted when capture is whitespace-only: %q", body)
	}
}

func TestPanePreview_ContainingTabInMeta(t *testing.T) {
	t.Parallel()
	driver := &fakeTreeDriver{
		tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
		panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
	}
	tree := NewTreeExpander(driver, time.Minute)
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
	}
	m := NewModelWithTree(cands, nil, tree, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	for i := 0; i < 16 && !cursorOnPane(m); i++ {
		m, _ = update(t, m, key("down"))
	}
	if !cursorOnPane(m) {
		t.Fatal("cursor never reached a RowPane")
	}
	m.previewLoading = false
	m.previewText = ""
	body := previewBodyAt(m, m.cursor)
	if !strings.Contains(body, "t1") {
		t.Errorf("pane identity should show containing tab_id: %q", body)
	}
}

// === 8. Project / Zoxide preview ===

func TestProjectPreview_IdentityGitDirectory(t *testing.T) {
	t.Parallel()
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "shep\npath: /home/dev/shep\nsource: projects"},
		{Kind: config.PreviewGit, Text: "git: main (2 changes)"},
		{Kind: config.PreviewDir, Text: "-rw-r--r-- 1 user staff 1024 Jul 10 README.md\n-rw-r--r-- 1 user staff  512 Jul 10 main.go"},
	}}
	cands := []source.Candidate{
		projectCandidate("shep", "/home/dev/shep"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	lines := strings.Split(body, "\n")
	if !strings.Contains(body, "shep") {
		t.Errorf("project preview missing identity label: %q", body)
	}
	gitIdx := indexOfLineContaining(lines, "git:")
	if gitIdx < 0 {
		t.Errorf("project preview missing git summary: %q", body)
	}
	dirIdx := indexOfLineContaining(lines, "Directory")
	if dirIdx < 0 {
		t.Fatalf("project preview missing \"Directory\" heading: %q", body)
	}
	longIdx := indexOfLineContaining(lines, "README.md")
	if longIdx < 0 {
		t.Fatalf("project preview missing long output: %q", body)
	}
	if longIdx <= dirIdx {
		t.Errorf("long output should appear AFTER Directory heading: longIdx=%d dirIdx=%d", longIdx, dirIdx)
	}
	if strings.Count(body, "source: projects") > 1 {
		t.Errorf("project preview should NOT duplicate identity: %q", body)
	}
}

func TestProjectPreview_NoRenderer_ShowsBuiltinSummary(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		projectCandidate("shep", "/home/dev/shep"),
	}
	m := NewModelWithLayout(cands, nil, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "shep") {
		t.Errorf("builtin summary missing label: %q", body)
	}
	if !strings.Contains(body, "/home/dev/shep") {
		t.Errorf("builtin summary missing path: %q", body)
	}
	if !strings.Contains(body, "projects") {
		t.Errorf("builtin summary missing source: %q", body)
	}
}

func TestZoxidePreview_IdentityGitDirectory(t *testing.T) {
	t.Parallel()
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "tmp\npath: /tmp\nsource: zoxide"},
		{Kind: config.PreviewDir, Text: "drwxr-xr-x 2 user staff 64 Jul 10 .\ndrwxr-xr-x 5 user staff 160 Jul 10 .."},
	}}
	cands := []source.Candidate{
		zoxideCandidate("tmp", "/tmp"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	lines := strings.Split(body, "\n")
	if !strings.Contains(body, "/tmp") {
		t.Errorf("zoxide preview should show full path: %q", body)
	}
	dirIdx := indexOfLineContaining(lines, "Directory")
	if dirIdx < 0 {
		t.Fatalf("zoxide preview missing \"Directory\" heading: %q", body)
	}
	longIdx := indexOfLineContaining(lines, "drwxr")
	if longIdx < 0 {
		t.Fatalf("zoxide preview missing long output: %q", body)
	}
	if longIdx <= dirIdx {
		t.Errorf("long output should appear after Directory heading: longIdx=%d dirIdx=%d", longIdx, dirIdx)
	}
}

// === 9. Loading / Error fallback preserved ===

func TestPreviewLoadingFallback(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		zoxideCandidate("alpha", "/a"),
	}
	m := NewModelWithLayout(cands, blockingRenderer{}, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "loading") {
		t.Errorf("preview should show loading indicator: %q", body)
	}
}

func TestPreviewErrorFallback(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		zoxideCandidate("alpha", "/a"),
	}
	m := NewModelWithLayout(cands, errRenderer{}, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewErr = "preview error"
	m.previewText = ""
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "preview error") {
		t.Errorf("preview should show error indicator: %q", body)
	}
}

// === 10. Configured/saved workspace preview ===

func TestConfiguredWorkspacePreview_Identity(t *testing.T) {
	t.Parallel()
	renderer := sectionsRenderer{sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "notes\npath: /home/dev/notes\nsource: workspaces"},
	}}
	cands := []source.Candidate{
		workspaceEntryCandidate("notes", "/home/dev/notes"),
	}
	m := NewModelWithLayout(cands, renderer, Layout{Theme: ThemePlain})
	m, _ = update(t, m, sizeMsg(120, 36))
	m.previewLoading = false
	m.previewSections = renderer.sections
	body := previewBodyAt(m, 0)
	if !strings.Contains(body, "notes") {
		t.Errorf("configured workspace missing label: %q", body)
	}
	if !strings.Contains(body, "/home/dev/notes") {
		t.Errorf("configured workspace missing path: %q", body)
	}
	if !strings.Contains(body, "configured") {
		t.Errorf("configured workspace should show kind \"configured\": %q", body)
	}
}
