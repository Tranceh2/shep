package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// preview_body.go composes the preview column's body for the highlighted
// row, top to bottom:
//
//	~/allsafe/ECORP/whiterose-db                 location (secondary)
//	⠴ working · 3 tabs · 4 panes                 meta line (muted)
//
//	Tabs ──────────────────────────────          headed sections
//	 1  editor   ⠴ claude · 2 panes
//
//	Active pane ───────────────────────          the capture tail, last
//
// The renderer's Result.Sections and the snapshot tree are data sources: the
// TUI never prints the renderer's identity block or its "agent status"
// heading.
//
// The body is composed as ready-to-print lines, each sanitized and fitted to
// the column width, so a frame only copies them. The short texts the TUI
// lays out itself — labels, terminal titles, Meta values, the path — are
// made plain first (see plainText), so none of them can carry a line break
// or a cursor-moving control into the line it is placed on. Heavy text — the
// pane capture, command output — is fitted once and reused across
// compositions (see fitCache): a spinner frame recomposes only what draws the
// spinner (the meta line, the Tabs section, the loading line) and joins the
// cached heavy lines.

// minCaptureLines is the fewest capture lines the "Active pane" section
// keeps even when the sections above it fill the column (the viewport
// scrolls beyond that).
const minCaptureLines = 3

// fittedText is one heavy preview text as ready-to-print lines: sanitized,
// tail-fitted (a capture) and fitted to the column. source is compared
// by ==, which is O(1) for the unchanged string a section or capture keeps
// across compositions.
type fittedText struct {
	source      string
	width, tail int
	lines       []string
}

// fitCache hands a composition the heavy text the previous one already
// fitted (prev, read-only) and collects what this one uses (next), so a
// composition never mutates the memo it reads — View composes with it too.
type fitCache struct {
	prev  []fittedText
	next  []fittedText
	built int // texts fitted anew: the memoization tests' observable
}

// previewComposition is one composed preview body.
type previewComposition struct {
	lines []string
	// animated reports a drawn spinner frame: the composition goes stale
	// with the next frame.
	animated bool
	fits     []fittedText
	built    int
}

// previewBlock is one part of the body: a section (heading included) or
// the head, as fitted lines.
type previewBlock struct {
	lines    []string
	animated bool
}

// composePreview composes the highlighted row's body for a preview column
// of width x height cells, reusing the heavy text prev already fitted;
// nothing when no row is highlighted (the list's empty state says why).
// Every line passes through sanitizePaneCapture — the single containment
// boundary for pane captures, command output and Herdr-reported labels
// alike — so no escape sequence other than SGR, and no control character,
// ever reaches the screen.
func (m Model) composePreview(width, height int, prev []fittedText) previewComposition {
	row, ok := m.currentRow()
	if !ok || width <= 0 {
		return previewComposition{}
	}
	cache := &fitCache{prev: prev}
	head := m.previewHead(row, width)
	blocks := []previewBlock{head}
	if !m.previewLoading && m.previewErr == "" {
		room := height - len(head.lines)
		if len(head.lines) > 0 {
			room-- // the blank line after the head
		}
		blocks = append(blocks, m.previewSectionBlocks(row, width, room, cache)...)
	}
	blank := strings.Repeat(" ", width)
	var c previewComposition
	for _, b := range blocks {
		if len(b.lines) == 0 {
			continue
		}
		if len(c.lines) > 0 {
			c.lines = append(c.lines, blank)
		}
		c.lines = append(c.lines, b.lines...)
		c.animated = c.animated || b.animated
	}
	c.fits, c.built = cache.next, cache.built
	return c
}

// fitText returns text's fitted lines from the cache, fitting them on a
// miss: sanitized (and stripped of all SGR under a no-color theme), cut to
// its newest lines when tail >= 0 (see captureTail), each fitted to width.
func (m Model) fitText(c *fitCache, text string, width, tail int) []string {
	for _, f := range c.prev {
		if f.source == text && f.width == width && f.tail == tail {
			c.next = append(c.next, f)
			return f.lines
		}
	}
	lines := strings.Split(m.previewContent(sanitizePaneCapture(text)), "\n")
	if tail >= 0 {
		lines = captureTail(lines, tail)
	}
	for i, line := range lines {
		lines[i] = fitPreviewLine(line, width)
	}
	c.next = append(c.next, fittedText{source: text, width: width, tail: tail, lines: lines})
	c.built++
	return lines
}

// fitPreviewLine fits one sanitized content line to the column: cut to
// width, a style reset when it carries SGR (so a capture's open colors never
// bleed into the next cell or row), then padding.
func fitPreviewLine(line string, width int) string {
	w := ansi.StringWidth(line)
	if w > width {
		line, w = cutPreviewLine(line, width)
	}
	if strings.IndexByte(line, '\x1b') >= 0 && !strings.HasSuffix(line, "\x1b[0m") && !strings.HasSuffix(line, "\x1b[m") {
		line += "\x1b[m"
	}
	return line + strings.Repeat(" ", max(0, width-w))
}

// cutPreviewLine keeps the prefix of a sanitized line that fills width
// cells, returning it and its width. Unlike ansi.Truncate it drops the
// sequences past the cut: they style nothing, yet a heavily colored capture
// line carries a kilobyte of them, and the frame would print every one of
// them on every spinner tick. The caller appends the reset, so colors left
// open at the cut never bleed.
func cutPreviewLine(line string, width int) (string, int) {
	var state byte
	w, end := 0, 0
	for end < len(line) && w < width {
		_, cells, n, next := ansi.DecodeSequence(line[end:], state, nil)
		if w+cells > width {
			break // a wide glyph straddles the edge; padding fills its cell
		}
		w, end, state = w+cells, end+n, next
	}
	return line[:end], w
}

// fitOwnLines fits lines the TUI rendered itself (headings, the meta line,
// the Tabs table) to width; they carry Herdr-reported labels, so they are
// sanitized too.
func fitOwnLines(lines []string, width int) []string {
	for i, line := range lines {
		lines[i] = fitWidth(sanitizePaneCapture(line), width)
	}
	return lines
}

// previewHead renders the location line, the meta line and, while a render
// is in flight or after it failed, the loading or error line. It is
// animated when it draws a spinner frame.
func (m Model) previewHead(row Row, width int) previewBlock {
	var out []string
	var b previewBlock
	if loc := m.previewLocation(row, width); loc != "" {
		out = append(out, loc)
	}
	if meta, spins := m.previewMeta(row, width); meta != "" {
		out, b.animated = append(out, meta), spins
	}
	switch {
	case m.previewLoading:
		out = append(out, "", m.spinner.View()+" "+m.styles.mutedStyle.Render("Loading preview…"))
		b.animated = true
	case m.previewErr != "":
		out = append(out, "", m.previewErrorLine())
	}
	b.lines = fitOwnLines(out, width)
	return b
}

// previewLocation renders the row's path, home-abbreviated and kept from
// its end when it does not fit; "" for a row without a path.
func (m Model) previewLocation(row Row, width int) string {
	path := m.layout.Templates.Tilde(plainText(row.Candidate.Path))
	if path == "" {
		return ""
	}
	return m.styles.secondaryStyle.Render(truncateFromLeftToWidth(path, width))
}

// previewErrorLine renders a failed preview: a "!" marker that survives the
// plain theme, and the short reason when it says more than "failed".
func (m Model) previewErrorLine() string {
	line := "! Preview unavailable"
	if m.previewErr != "preview error" {
		line += ": " + m.previewErr
	}
	return m.styles.previewErrStyle.Render(line)
}

// --- meta line ---

// previewMeta renders the row's one-line summary, parts joined by " · ":
// agent state, tab/pane counts and placement for Herdr rows; branch and
// working-tree state for directories. spins reports a working status glyph
// (the animated spinner frame).
func (m Model) previewMeta(row Row, width int) (meta string, spins bool) {
	c := row.Candidate
	set := m.icons()
	var parts []string
	status := ""
	switch {
	case c.Source == config.SourceAgents || row.Kind == RowPane:
		status = c.Meta["agent_status"]
		if agent := plainText(c.Meta["agent"]); agent != "" {
			parts = append(parts, m.styles.mutedStyle.Render(agent))
		}
		if place := m.placement(c, true); place != "" {
			parts = append(parts, place)
		}
	case row.Kind == RowTab:
		if n := plainText(c.Meta["tab_number"]); n != "" {
			parts = append(parts, m.styles.mutedStyle.Render("tab "+n))
		}
		if panes := m.tabPanes(c.Meta["workspace_id"], c.Meta["tab_id"]); panes > 0 {
			parts = append(parts, m.styles.mutedStyle.Render(plural(panes, "pane")))
		}
		if place := m.placement(c, false); place != "" {
			parts = append(parts, place)
		}
	case c.Source == config.SourceHerdr:
		wsID := c.Meta["workspace_id"]
		status = m.tree.WorkspaceAgentStatus(wsID)
		if tree, ok := m.tree.Fetch(m.renderCtx, wsID); ok {
			parts = append(parts, m.styles.mutedStyle.Render(plural(len(tree.Tabs), "tab")),
				m.styles.mutedStyle.Render(plural(len(tree.Panes), "pane")))
		}
	default:
		parts = m.directoryMeta(c)
	}
	if hasStatusGlyph(status) {
		style := m.styles.statusStyle(status)
		glyph := statusGlyph(&set, m.spinner, status, style)
		parts = append([]string{glyph + " " + style.Render(status)}, parts...)
		spins = status == "working" && set.StatusWorking == ""
	}
	if len(parts) == 0 {
		return "", false
	}
	sep := m.styles.mutedStyle.Render(" " + set.HintSeparator + " ")
	return truncateToWidth(strings.Join(parts, sep), width), spins
}

// placement renders where a tab or pane lives: "in <workspace> › <tab>".
// withTab adds the tab (a pane's), when it has a label.
func (m Model) placement(c source.Candidate, withTab bool) string {
	ws := m.layout.Templates.Tilde(plainText(c.Meta["workspace_label"]))
	if ws == "" {
		return ""
	}
	place := "in " + ws
	if tab := plainText(c.Meta["tab_label"]); withTab && tab != "" {
		place += " " + m.icons().Group + " " + tab
	}
	return m.styles.mutedStyle.Render(place)
}

// tabPanes counts the panes of tabID in workspaceID's tree.
func (m Model) tabPanes(workspaceID, tabID string) int {
	tree, ok := m.tree.Fetch(m.renderCtx, workspaceID)
	if !ok {
		return 0
	}
	n := 0
	for _, p := range tree.Panes {
		if p.TabID == tabID {
			n++
		}
	}
	return n
}

// directoryMeta summarizes a directory candidate: its git branch and working
// tree state when the git section is present ("on main · clean", or
// "worktree on fix/x · 1a2b3c4 · 2 changes" for a worktree), and a
// configured workspace's template and group role.
func (m Model) directoryMeta(c source.Candidate) []string {
	var parts []string
	muted := m.styles.mutedStyle
	if git := findSection(m.previewSections, config.PreviewGit); git != nil {
		if branch, dirty, ok := parseGitSummary(git.Text); ok {
			on := "on "
			if c.Meta["is_worktree"] == "true" {
				on = "worktree on "
				if b := c.Meta["branch"]; b != "" {
					branch = b
				}
			}
			parts = append(parts, muted.Render(on)+m.styles.gitBranchStyle.Render(plainText(branch)))
			if head := c.Meta["head"]; c.Meta["is_worktree"] == "true" && head != "" {
				parts = append(parts, muted.Render(plainText(head[:min(7, len(head))])))
			}
			if dirty == 0 {
				parts = append(parts, m.styles.gitCleanStyle.Render("clean"))
			} else {
				parts = append(parts, m.styles.gitChangesStyle.Render(plural(dirty, "change")))
			}
		}
	}
	if c.Source == config.SourceWorkspaces {
		if t := plainText(c.Meta["template"]); t != "" {
			parts = append(parts, muted.Render("template "+t))
		}
		if c.Meta["group"] == "true" {
			parts = append(parts, muted.Render("group"))
		}
	}
	return parts
}

// parseGitSummary reads the renderer's git line ("git: main (clean)", "git:
// main (2 changes)", or with a "[worktree: …] <head>" prefix) into its branch
// and number of changes. ok is false for any other shape.
func parseGitSummary(text string) (branch string, dirty int, ok bool) {
	s := strings.TrimPrefix(text, "git: ")
	open := strings.LastIndex(s, " (")
	if open < 0 || !strings.HasSuffix(s, ")") {
		return "", 0, false
	}
	fields := strings.Fields(s[:open])
	if len(fields) == 0 {
		return "", 0, false
	}
	state := s[open+2 : len(s)-1]
	if state == "clean" {
		return fields[len(fields)-1], 0, true
	}
	n, err := strconv.Atoi(strings.TrimSuffix(state, " changes"))
	if err != nil {
		return "", 0, false
	}
	return fields[len(fields)-1], n, true
}

// plural renders "1 tab", "3 tabs".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// --- headed sections ---

// previewSectionBlocks renders the row's headed sections, each a heading
// line plus its body, in the configured order with the capture last: it is
// the longest and keeps only the tail that fits the room left (height cells
// minus everything above it). Sections whose data is missing are omitted.
// Bodies of untrusted text come from the fit cache; the Tabs table is
// rendered every time (it is short, and may draw the spinner).
func (m Model) previewSectionBlocks(row Row, width, room int, cache *fitCache) []previewBlock {
	var blocks []previewBlock
	used := 0
	add := func(title string, body []string, animated bool) {
		if len(blocks) > 0 {
			used++ // the blank line between sections
		}
		lines := make([]string, 0, len(body)+1)
		if title != "" {
			lines = append(lines, fitWidth(m.previewHeading(title, width), width))
		}
		lines = append(lines, body...)
		blocks = append(blocks, previewBlock{lines: lines, animated: animated})
		used += len(lines)
	}
	capture, captureTitle := "", "Active pane"
	switch row.Kind {
	case RowTab:
		capture = m.previewText
	case RowPane:
		capture, captureTitle = m.previewText, "Pane"
	case RowCandidate:
		for _, s := range m.previewSections {
			switch s.Kind {
			case config.PreviewIdentity, config.PreviewGit, config.PreviewAgentStatus:
				// Data sources for the location and meta lines.
			case config.PreviewWorkspace:
				body, animated := m.tabsSection(row.Candidate, s, width, cache)
				add("Tabs", body, animated)
			case config.PreviewSessionInfo:
				add("Session", fitOwnLines(m.keyValueTable(m.previewContent(sectionBodyAfterHeading(s.Text))), width), false)
			case config.PreviewActivePane:
				capture = sectionBodyAfterHeading(s.Text)
				if row.Candidate.Source == config.SourceAgents {
					captureTitle = "Pane"
				}
			case config.PreviewDir:
				add("Files", m.fitText(cache, s.Text, width, -1), false)
			default:
				// A custom command: its configured or humanized title
				// (config.PreviewTitle), no heading for an empty one.
				add(plainText(s.Title), m.fitText(cache, s.Text, width, -1), false)
			}
		}
	}
	if capture != "" {
		if lines := m.fitText(cache, capture, width, room-used-2); len(lines) > 0 {
			add(captureTitle, lines, false)
		}
	}
	return blocks
}

// previewContent prepares sanitized preview text (a pane capture, command
// output such as lsd/eza listings) for composition: under a no-color theme
// every remaining SGR is stripped too, because the user asked for no color
// and those tools force --color=always.
func (m Model) previewContent(text string) string {
	if m.theme.NoColor {
		return ansi.Strip(text)
	}
	return text
}

// captureTail returns the newest lines of a pane capture: trailing blank
// lines trimmed (a shell leaves its cursor below the last output), then the
// last fit lines, never fewer than minCaptureLines. nil when the capture is
// empty or whitespace-only.
func captureTail(lines []string, fit int) []string {
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	return lines[max(0, len(lines)-max(fit, minCaptureLines)):]
}

// previewHeading renders a section heading on one row: the title in the
// heading style, then a rule to the column's edge.
func (m Model) previewHeading(title string, width int) string {
	rule := max(0, width-ansi.StringWidth(title)-1)
	heading := m.styles.previewHeadingStyle.Render(title)
	if rule == 0 {
		return heading
	}
	return heading + " " + m.styles.ruleStyle.Render(strings.Repeat(m.icons().RuleHorizontal, rule))
}

// tabLabelMaxCells caps the label column of the Tabs section.
const tabLabelMaxCells = 24

// tabsSection lists the workspace's tabs from the snapshot tree, one per
// line: the tab number right-aligned, the label padded to a shared column
// (the active tab bold, its number in the accent), each agent pane's status
// glyph and agent name, and the pane count when the tab has several. It is
// animated when it draws a working agent's spinner frame. A workspace the
// tree does not hold falls back to the renderer's text.
func (m Model) tabsSection(c source.Candidate, s preview.Section, width int, cache *fitCache) (lines []string, animated bool) {
	tree, ok := m.tree.Fetch(m.renderCtx, c.Meta["workspace_id"])
	if !ok || len(tree.Tabs) == 0 {
		return m.fitText(cache, sectionBodyAfterHeading(s.Text), width, -1), false
	}
	set := m.icons()
	numbers := make([]string, len(tree.Tabs))
	labels := make([]string, len(tree.Tabs))
	numW, labelW := 0, 0
	for i, tab := range tree.Tabs {
		n := tab.Number
		if n == 0 {
			n = i + 1
		}
		numbers[i] = strconv.Itoa(n)
		numW = max(numW, len(numbers[i]))
		labels[i] = plainText(tab.Label)
		labelW = max(labelW, min(tabLabelMaxCells, ansi.StringWidth(labels[i])))
	}
	active := c.Meta["active_tab_id"]
	sep := m.styles.mutedStyle.Render(" " + set.HintSeparator + " ")
	lines = make([]string, len(tree.Tabs))
	for i, tab := range tree.Tabs {
		isActive := tab.ID == active || (active == "" && tab.Focused)
		number, label := m.styles.mutedStyle, m.styles.rowStyle
		if isActive {
			number, label = m.styles.queryStyle, m.styles.rowStyle.Bold(true)
		}
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", numW-len(numbers[i])))
		b.WriteString(number.Render(numbers[i]))
		b.WriteString("  ")
		b.WriteString(label.Render(fitWidth(truncateToWidth(labels[i], labelW), labelW)))
		panes := 0
		for _, p := range tree.Panes {
			if p.TabID != tab.ID {
				continue
			}
			panes++
			if p.Agent == "" {
				continue
			}
			b.WriteString("  ")
			if hasStatusGlyph(p.AgentStatus) {
				b.WriteString(statusGlyph(&set, m.spinner, p.AgentStatus, m.styles.statusStyle(p.AgentStatus)) + " ")
				animated = animated || (p.AgentStatus == "working" && set.StatusWorking == "")
			}
			b.WriteString(m.styles.mutedStyle.Render(plainText(p.Agent)))
		}
		if panes > 1 {
			b.WriteString(sep + m.styles.mutedStyle.Render(plural(panes, "pane")))
		}
		lines[i] = truncateToWidth(b.String(), width)
	}
	return fitOwnLines(lines, width), animated
}

// keyValueTable renders "key: value" lines as an aligned table: the keys
// muted and padded to the widest key. Lines without a key pass through.
func (m Model) keyValueTable(body string) []string {
	lines := strings.Split(body, "\n")
	keyW := 0
	for _, line := range lines {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), ": "); ok {
			keyW = max(keyW, ansi.StringWidth(k))
		}
	}
	for i, line := range lines {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ": "); ok {
			lines[i] = m.styles.mutedStyle.Render(fitWidth(k, keyW)) + "  " + v
		}
	}
	return lines
}

// --- structured section helpers ---

// findSection returns the first Section of the given Kind, or nil if none.
func findSection(sections []preview.Section, kind string) *preview.Section {
	for i := range sections {
		if sections[i].Kind == kind {
			return &sections[i]
		}
	}
	return nil
}

// sectionBodyAfterHeading extracts the body of a headed section by splitting
// ONCE at the first newline — the heading is the first line, the body is
// everything after it. This preserves any blank lines or heading-like content
// in the body.
func sectionBodyAfterHeading(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[idx+1:]
	}
	return ""
}
