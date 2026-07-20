package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/source"
)

// previewBodyPlain builds the preview pane's raw text for the currently
// highlighted row, dispatched by Row.Kind. Phase 4 consumes the structured
// Result.Sections from the renderer (not a text parser) so blank lines and
// heading-like content in custom/capture sections are preserved
// byte-for-byte. A renderer that returns only Text (no Sections) degrades to
// compact identity — no fallback text parsing.
//
// This is also the single Phase 5 containment boundary: the composed body is
// run through sanitizePaneCapture before it is returned, so any raw pane
// capture (RowPane's m.previewText, or a Herdr workspace's active_pane
// Section) has its dangerous escape sequences (OSC, DCS, APC, PM, SOS, and
// every non-SGR CSI — see sanitize.go) stripped exactly once, in one place,
// before the result ever reaches the viewport. SGR and all printable content
// pass through unmodified.
//
// width is accepted for parity with other render.go builders even though
// this function itself does not pad to it (the caller, syncViewport,
// truncates each line before handing content to the viewport).
func (m Model) previewBodyPlain(width int) string {
	_ = width
	row, ok := m.currentRow()
	if !ok {
		return "(no selection)"
	}
	var body string
	switch row.Kind {
	case RowCandidate:
		body = m.candidatePreviewBody(row.Candidate)
	case RowTab:
		body = m.tabPreviewBody(row.Candidate)
	case RowPane:
		body = m.panePreviewBody(row.Candidate)
	default:
		return "(no selection)"
	}
	return sanitizePaneCapture(body)
}

// candidatePreviewBody dispatches by source: SourceHerdr gets a compact
// identity + recomposed structured sections; every other source gets
// identity + renderer content with long output last.
func (m Model) candidatePreviewBody(cand source.Candidate) string {
	switch cand.Source {
	case config.SourceHerdr:
		return m.herdrWorkspacePreview(cand)
	default:
		return m.standardCandidatePreview(cand)
	}
}

// herdrWorkspacePreview builds the active Herdr workspace preview from the
// structured Result.Sections: compact identity (label + path), inline agent
// status, workspace summary (tabs/panes), then "Active pane" heading + capture
// LAST. The standalone "agent status" heading block is removed and its status
// shown inline. If the capture is unavailable (no section or empty body),
// the Active pane section is omitted while identity + metadata remain.
func (m Model) herdrWorkspacePreview(cand source.Candidate) string {
	identity := m.compactIdentity(cand)

	if m.renderer == nil {
		return identity
	}
	if m.previewLoading {
		return identity + "\n\n" + m.spinner.View() + " loading…"
	}
	if m.previewErr != "" {
		return identity + "\n\n" + m.previewErr
	}
	if len(m.previewSections) == 0 {
		return identity
	}

	sections := m.previewSections
	var parts []string
	parts = append(parts, identity)

	// Inline agent status: extract the status word and render inline.
	if ab := findSection(sections, config.PreviewAgentStatus); ab != nil {
		status := extractAgentStatus(sectionBodyAfterHeading(ab.Text))
		if status != "" {
			parts = append(parts, m.styles.labelStyle.Render("agent  ")+m.styles.statusStyle(status).Render(status))
		}
	}

	// Workspace summary (tabs/panes) with styled heading.
	if wb := findSection(sections, config.PreviewWorkspace); wb != nil {
		parts = append(parts, m.styles.previewHeadingStyle.Render("workspace")+"\n"+sectionBodyAfterHeading(wb.Text))
	}

	// Git summary (if present).
	if gb := findSection(sections, config.PreviewGit); gb != nil {
		parts = append(parts, m.styles.previewHeadingStyle.Render("git: ")+strings.TrimPrefix(gb.Text, "git: "))
	}

	// Unknown/custom blocks preserved in their original relative order.
	for _, s := range sections {
		if isKnownSectionKind(s.Kind) {
			continue
		}
		parts = append(parts, s.Text)
	}

	// Active pane (capture) LAST — omit if body is empty/whitespace-only.
	if pb := findSection(sections, config.PreviewActivePane); pb != nil {
		body := sectionBodyAfterHeading(pb.Text)
		if strings.TrimSpace(body) != "" {
			parts = append(parts, m.styles.previewHeadingStyle.Render("Active pane")+"\n"+body)
		}
	}

	return strings.Join(parts, "\n\n")
}

// standardCandidatePreview handles every non-Herdr RowCandidate (projects,
// zoxide, configured workspaces): identity first, then git summary, then
// long output under a "Directory" heading last.
func (m Model) standardCandidatePreview(cand source.Candidate) string {
	identity := m.candidateIdentity(cand)

	if m.renderer == nil {
		return identity
	}
	if m.previewLoading {
		return identity + "\n\n" + m.spinner.View() + " loading…"
	}
	if m.previewErr != "" {
		return identity + "\n\n" + m.previewErr
	}
	if len(m.previewSections) == 0 {
		return identity
	}

	sections := m.previewSections
	var parts []string

	// Always use the TUI's own identity (which knows about enhanced kind
	// labels like "configured" for SourceWorkspaces). Skip the renderer's
	// identity section to avoid duplication.
	parts = append(parts, identity)

	// Git summary next.
	if gb := findSection(sections, config.PreviewGit); gb != nil {
		parts = append(parts, m.styles.previewHeadingStyle.Render("git: ")+strings.TrimPrefix(gb.Text, "git: "))
	}

	// Long output (dir/custom) under a "Directory" heading last.
	var longOutput []string
	for _, s := range sections {
		if s.Kind == config.PreviewIdentity || s.Kind == config.PreviewGit {
			continue
		}
		longOutput = append(longOutput, s.Text)
	}
	if len(longOutput) > 0 {
		parts = append(parts, m.styles.previewHeadingStyle.Render("Directory")+"\n"+strings.Join(longOutput, "\n"))
	}

	return strings.Join(parts, "\n\n")
}

// tabPreviewBody renders a synchronous summary for a RowTab: tab label,
// CWD/path, kind "herdr tab". Includes workspace/tab/pane counts from Meta
// if present (no async work, no fetching).
func (m Model) tabPreviewBody(cand source.Candidate) string {
	lines := []string{
		m.styles.labelStyle.Render("label  ") + cand.Label,
		m.styles.labelStyle.Render("path   ") + cand.Path,
		m.styles.labelStyle.Render("kind   ") + "herdr tab",
	}
	if v := cand.Meta["workspace_tabs"]; v != "" {
		lines = append(lines, m.styles.labelStyle.Render("tabs   ")+v)
	}
	if v := cand.Meta["tab_panes"]; v != "" {
		lines = append(lines, m.styles.labelStyle.Render("panes  ")+v)
	}
	return strings.Join(lines, "\n")
}

// panePreviewBody renders the RowPane preview: the optional real pane label,
// path, kind "herdr pane", and optional human-facing parent tab label from
// Meta. Stable pane/tab IDs stay in explicitly ID-named metadata for actions
// and are never shown as labels. The "Captured pane" heading + raw capture is
// last; it is omitted when the capture is unavailable or whitespace-only.
func (m Model) panePreviewBody(cand source.Candidate) string {
	lines := make([]string, 0, 4)
	if cand.Label != "" {
		lines = append(lines, m.styles.labelStyle.Render("label  ")+cand.Label)
	}
	lines = append(lines,
		m.styles.labelStyle.Render("path   ")+cand.Path,
		m.styles.labelStyle.Render("kind   ")+"herdr pane",
	)
	if v := cand.Meta["tab_label"]; v != "" {
		lines = append(lines, m.styles.labelStyle.Render("tab    ")+v)
	}
	identity := strings.Join(lines, "\n")

	capture := m.panePreviewCaptureBody()
	if capture == "" {
		return identity
	}
	return identity + "\n\n" + m.styles.previewHeadingStyle.Render("Captured pane") + "\n" + capture
}

// panePreviewCaptureBody returns the pane buffer capture's current display
// text: the loading spinner while in flight, the captured text once
// resolved, or "" (omitted entirely) when unavailable/errored/whitespace-only.
func (m Model) panePreviewCaptureBody() string {
	if m.tree == nil {
		return ""
	}
	if m.previewLoading {
		return m.spinner.View() + " loading…"
	}
	if strings.TrimSpace(m.previewText) == "" {
		return ""
	}
	return m.previewText
}

// compactIdentity renders the Herdr workspace identity: label + path only.
func (m Model) compactIdentity(cand source.Candidate) string {
	lines := []string{
		m.styles.labelStyle.Render("label  ") + cand.Label,
		m.styles.labelStyle.Render("path   ") + cand.Path,
	}
	return strings.Join(lines, "\n")
}

// candidateIdentity renders the standard identity for a non-Herdr candidate.
func (m Model) candidateIdentity(cand source.Candidate) string {
	sourceLabel := "source "
	sourceValue := cand.Source
	if cand.Source == config.SourceWorkspaces {
		sourceLabel = "kind   "
		switch {
		case cand.Meta["group"] == "true":
			sourceValue = "group"
		case cand.Meta["template"] != "":
			sourceValue = "template: " + cand.Meta["template"]
		default:
			sourceValue = "configured"
		}
	}
	lines := []string{
		m.styles.labelStyle.Render("label  ") + cand.Label,
		m.styles.labelStyle.Render("path   ") + cand.Path,
		m.styles.labelStyle.Render(sourceLabel) + sourceValue,
	}
	return strings.Join(lines, "\n")
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

// isKnownSectionKind reports whether kind is one of the built-in section
// names (identity, git, workspace, active_pane, agent_status, dir). Used to
// separate known from unknown/custom sections when preserving the latter.
func isKnownSectionKind(kind string) bool {
	switch kind {
	case config.PreviewIdentity, config.PreviewGit, config.PreviewWorkspace,
		config.PreviewActivePane, config.PreviewAgentStatus, config.PreviewDir:
		return true
	}
	return false
}

// sectionBodyAfterHeading extracts the body of a headed section by splitting
// ONCE at the first newline — the heading is the first line, the body is
// everything after it. This preserves any blank lines or heading-like content
// in the body. For headingless sections (identity, git, dir, custom), the
// full text is returned unchanged.
func sectionBodyAfterHeading(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[idx+1:]
	}
	return ""
}

// extractAgentStatus extracts the status word from the agent_status block's
// body (e.g. "  status: working" → "working"). Returns "" when no status
// line is found. The status word is stripped of any existing ANSI before
// return — it is an untrusted, Herdr-reported value decoded straight from
// JSON with no sanitization guarantee.
func extractAgentStatus(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, statusLinePrefix) {
			continue
		}
		status := ansi.Strip(strings.TrimPrefix(line, statusLinePrefix))
		return strings.TrimSpace(status)
	}
	return ""
}

// statusLinePrefix is the literal prefix renderAgentStatusSection
// (internal/preview/renderer.go) writes before the raw status word.
const statusLinePrefix = "  status: "
