package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// frame_invariant_test.go proves the contract every frame line keeps,
// whatever text reaches the model from outside shep (pane captures, Herdr
// labels, terminal titles, custom source rows): Bubble Tea writes each line
// at its terminal row and skips the lines that did not change, so a line
// that moves the cursor (a "\r", a backspace, an 8-bit CSI) or measures
// other than the terminal's width corrupts rows that later frames never
// rewrite. Live symptom: `herdr pane read` returned CRLF lines, and each
// line's trailing "\r" let the preview's padding blank the list column.

// hostileCapture is a pane capture as `herdr pane read --format ansi`
// returns it — CRLF line endings, an SGR reset after the "\r" — salted with
// every cursor-moving byte a pane can hold: tabs, a bare CR, backspaces,
// C1 CSI/OSC code points, an invalid byte and a wide rune before a tab.
func hostileCapture() string {
	return strings.Join([]string{
		"\x1b[32mPASS\x1b[0m [context_kind] unconfigured\r\x1b[m",
		"\tindented\twith tabs",
		"progress 10%\rprogress 99%",
		"typo\b\bfixed and a bell\x07",
		"\u009b2J\u009d0;8-bit title\x07after C1",
		"\x1b[31mred\x1b[m\ttabbed after SGR",
		"日本\twide then tab",
		"bad \xff byte\x7f",
		"FAIL foo",
	}, "\r\n") + "\r\n"
}

// hostileHerdrModel is the all view at w x h with an expanded Herdr tree
// whose labels carry controls, the cursor on its workspace, and that
// workspace's preview delivered with hostileCapture as its active pane.
func hostileHerdrModel(t *testing.T, w, h int) Model {
	t.Helper()
	snap := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "backend\r"}, {ID: "w2", Label: "front\x1b[2Jend"}},
		Tabs: []source.Tab{
			{ID: "t1", WorkspaceID: "w1", Label: "api\r\n", Number: 1, Focused: true},
			{ID: "t2", WorkspaceID: "w1", Label: "te\tsts\x07", Number: 2},
		},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "claude\r", AgentStatus: "working", TerminalTitle: "build\x1b]0;evil\x07 \u009b2J"},
			{ID: "p2", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/backend"},
			{ID: "p3", WorkspaceID: "w1", TabID: "t2", Agent: "co\bdex", AgentStatus: "idle"},
		},
	}
	ws := herdrCandidate("backend\r", "/srv/backend", "w1")
	ws.Meta["active_tab_id"] = "t1"
	cands := []source.Candidate{
		ws,
		herdrCandidate("front\x1b[2Jend", "/srv/front\tend", "w2"),
		{Label: "api-fix\r\n", Path: "/home/dev/trees/api-fix", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "fix/\r\nparser\x1b[2J"}},
	}
	for i := range 12 {
		cands = append(cands, zoxideCandidate(fmt.Sprintf("~/src/repo-%02d\x07", i), fmt.Sprintf("/home/dev/src/repo-%02d", i)))
	}
	m := NewModelWithTree(cands, stubRenderer{}, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha), HomeDir: "/home/dev"})
	m, _ = update(t, m, sizeMsg(w, h))
	m.expandedWorkspaces["w1"] = true
	m.applyFilter()
	if row, ok := m.currentRow(); !ok || row.Kind != RowCandidate || row.Candidate.Meta["workspace_id"] != "w1" {
		t.Fatalf("setup: the cursor must be on workspace w1, got %+v", row)
	}
	m, _ = update(t, m, previewResponseMsg{seq: m.previewSeq, result: preview.Result{Sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "backend\npath: /srv/backend"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: api"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: working"},
		{Kind: config.PreviewActivePane, Text: "active pane\n" + hostileCapture()},
	}}})
	return m
}

// hostileAgentsModel is the agents view at w x h with agents whose titles,
// programs and workspace labels carry controls, the highlighted agent's
// capture delivered, and a failed pin's error text in the footer.
func hostileAgentsModel(t *testing.T, w, h int) Model {
	t.Helper()
	snap := source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1", Label: "app\r\ncontract"}, {ID: "w2", Label: "/home/dev/billing\t"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "editor\t1", Number: 1}, {ID: "t2", WorkspaceID: "w2", Label: "api\x1b[H", Number: 1}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/home/dev/app", Agent: "claude", AgentStatus: "blocked", TerminalTitle: "Review\r the parser\x1b[2J before release"},
			{ID: "p2", WorkspaceID: "w2", TabID: "t2", CWD: "/home/dev/billing", Agent: "co\x07dex", AgentStatus: "working", TerminalTitle: "Fix\x1b]0;evil\x07 rounding\r\n"},
			{ID: "p3", WorkspaceID: "w2", TabID: "t2", CWD: "/home/dev/billing", Agent: "opencode", AgentStatus: "idle", TerminalTitle: "\u009b2Jidle\b agent"},
		},
	}
	driver := &fakeTreeDriver{tabs: snap.Tabs, panes: snap.Panes}
	m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha), InitialTab: "agents", HomeDir: "/home/dev"}).
		WithSnapshotRefresh(driver, snap, nil, nil)
	m.startupSnapshot = &snap
	m.applyFilter()
	m, _ = update(t, m, sizeMsg(w, h))
	if len(m.rows) == 0 || m.rows[0].Candidate.Source != config.SourceAgents {
		t.Fatalf("setup: want agent rows, got %+v", m.rows)
	}
	m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: hostileCapture()})
	m.pinStatus = errorStatus("pin update failed: disk\r\nfull\x1b[2J")
	return m
}

// hostileCustomModel is the all view at w x h with [[sources.custom]] rows
// whose JSON labels and icons carry controls, and a custom preview section
// with CRLF output.
func hostileCustomModel(t *testing.T, w, h int) Model {
	t.Helper()
	cands := []source.Candidate{
		{Source: "prs", Label: "PR\t42\x07 fix parser", Icon: "\x1b[2JP\r", Meta: map[string]string{"command": "gh pr view 42", "custom_source": "true"}},
		{Source: "prs", Label: "PR 43\r\nrefactor\u0085", Icon: "P", Meta: map[string]string{"command": "gh pr view 43", "custom_source": "true"}},
		{Source: "prs", Label: "PR\x1b]52;c;cGF5bG9hZA==\x07 44 \xff", Icon: "P\t", Meta: map[string]string{"command": "gh pr view 44", "custom_source": "true"}},
	}
	m := NewModelWithLayout(cands, stubRenderer{}, Layout{
		Theme:       testTheme(ThemeMocha),
		SourceOrder: []string{"prs"},
	})
	m, _ = update(t, m, sizeMsg(w, h))
	if len(m.rows) != len(cands) {
		t.Fatalf("setup: want %d custom rows, got %d", len(cands), len(m.rows))
	}
	m, _ = update(t, m, previewResponseMsg{seq: m.previewSeq, result: preview.Result{Sections: []preview.Section{
		{Kind: "pr_details", Text: "PR #42\r\nstatus:\topen\x07\r\nchecks:\t\x1b[32mpassing\x1b[m\r\n"},
	}}})
	return m
}

// TestView_FrameInvariants proves every line of View(), for representative
// models fed hostile external text, at wide and list-only sizes: (a) holds
// no control byte but ESC, and every ESC starts a well-formed SGR sequence;
// (b) measures exactly the terminal width the model was sized to; (c) holds
// no C1 code point (and is valid UTF-8). The frame also has exactly as many
// lines as the terminal has rows.
func TestView_FrameInvariants(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		name  string
		setup func(t *testing.T, w, h int) Model
	}{
		{"all view with an expanded tree and a CRLF capture", hostileHerdrModel},
		{"agents view with hostile titles", hostileAgentsModel},
		{"custom source rows with hostile labels", hostileCustomModel},
		{"help open over the hostile tree", func(t *testing.T, w, h int) Model {
			m, _ := update(t, hostileHerdrModel(t, w, h), key("?"))
			if m.focus != FocusHelp {
				t.Fatalf("setup: help did not open, focus = %v", m.focus)
			}
			return m
		}},
	}
	sizes := []struct{ w, h int }{{120, 36}, {100, 16}, {64, 20}}
	for _, sc := range scenarios {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", sc.name, size.w, size.h), func(t *testing.T) {
				t.Parallel()
				m := sc.setup(t, size.w, size.h)
				assertFrameInvariants(t, m.View(), size.w, size.h)
				// A spinner frame recomposes the animated parts of the preview
				// from the memo: it must keep the contract too.
				m, _ = update(t, m, m.spinner.Tick())
				assertFrameInvariants(t, m.View(), size.w, size.h)
			})
		}
	}
}

// assertFrameInvariants checks each line of frame against the contract
// TestView_FrameInvariants documents.
func assertFrameInvariants(t *testing.T, frame string, width, height int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != height {
		t.Errorf("frame has %d lines, want %d", len(lines), height)
	}
	for i, line := range lines {
		if bad := frameLineControlError(line); bad != "" {
			t.Errorf("line %d: %s\n%q", i, bad, line)
		}
		if !utf8.ValidString(line) {
			t.Errorf("line %d is not valid UTF-8: %q", i, line)
		}
		for _, r := range line {
			if r >= 0x80 && r <= 0x9f {
				t.Errorf("line %d holds C1 control U+%04X: %q", i, r, line)
				break
			}
		}
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("line %d measures %d cells, want %d: %q", i, w, width, line)
		}
	}
}

// frameLineControlError reports the first control byte of line other than
// ESC, or the first ESC that does not start a well-formed SGR sequence
// (ESC [ <digits ; :> m); "" when there is none.
func frameLineControlError(line string) string {
	for j := 0; j < len(line); j++ {
		c := line[j]
		switch {
		case c == esc:
			k := j + 1
			if k >= len(line) || line[k] != '[' {
				return fmt.Sprintf("ESC at byte %d does not start a CSI sequence", j)
			}
			for k++; k < len(line) && (line[k] >= '0' && line[k] <= '9' || line[k] == ';' || line[k] == ':'); k++ {
			}
			if k >= len(line) || line[k] != 'm' {
				return fmt.Sprintf("ESC at byte %d does not start an SGR sequence", j)
			}
			j = k
		case c < 0x20 || c == 0x7f:
			return fmt.Sprintf("control byte %#02x at byte %d", c, j)
		}
	}
	return ""
}

// TestView_CRLFCaptureKeepsListColumn reproduces the live bug's shape: the
// list scrolled (ctrl+d right after opening) and the newly selected
// workspace's active-pane capture arriving with CRLF lines ("PASS foo\r\n"),
// each line ending "\r" + SGR reset once fitted. No "\r" may reach the
// frame — the terminal would return to column 0 and the preview's padding
// would overwrite the list column — and every terminal row that shows a
// capture line also shows its list row's label.
func TestView_CRLFCaptureKeepsListColumn(t *testing.T) {
	t.Parallel()
	var cands []source.Candidate
	for i := range 30 {
		cands = append(cands, herdrCandidate(fmt.Sprintf("ws-%02d", i), fmt.Sprintf("/srv/ws-%02d", i), fmt.Sprintf("w%02d", i)))
	}
	m := NewModelWithLayout(cands, stubRenderer{}, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 24))
	m, _ = update(t, m, key("ctrl+d"))
	m, _ = update(t, m, key("ctrl+d"))
	var capture strings.Builder
	for i := range 40 {
		fmt.Fprintf(&capture, "\x1b[32mPASS\x1b[0m foo/test_%02d [context_kind] unconfigured\r\n", i)
	}
	m, _ = update(t, m, previewResponseMsg{seq: m.previewSeq, result: preview.Result{Sections: []preview.Section{
		{Kind: config.PreviewActivePane, Text: "active pane\n" + capture.String()},
	}}})

	view := m.View()
	if i := strings.IndexByte(view, '\r'); i >= 0 {
		t.Fatalf("View() carries a carriage return at byte %d: %q", i, view[max(0, i-40):min(len(view), i+20)])
	}
	g := m.geometry()
	lines := strings.Split(view, "\n")
	const bodyTop = 3 // tab strip, prompt row, rule
	offset := scrollOffset(m.listOffset, m.cursor, len(m.rows), g.ListInnerRows)
	shared := 0
	for i := range windowLen(len(m.rows), offset, g.ListInnerRows) {
		line := lines[bodyTop+i]
		want := m.buildRowView(m.rows[offset+i]).label.text
		if list := ansi.Strip(ansi.Cut(line, 0, g.Margin+g.ListWidth)); !strings.Contains(list, want) {
			t.Errorf("body row %d: list column %q lost its label %q", i, list, want)
		}
		if strings.Contains(ansi.Strip(line), "PASS foo/test_") {
			shared++
		}
	}
	if shared == 0 {
		t.Fatalf("setup: no list row shares a terminal row with a capture line:\n%s", ansi.Strip(view))
	}
	if offset == 0 {
		t.Fatalf("setup: ctrl+d did not scroll the list (cursor %d)", m.cursor)
	}
}
