package tui

import "github.com/charmbracelet/bubbles/viewport"

// syncViewport refreshes m.viewport's Width/Height/Content from the current
// pane geometry and preview body — called once at the end of every Update
// (see model.go), so by the time a scroll key is processed the viewport's
// Height (page-size math) and content (line count for AtBottom/maxYOffset)
// already reflect the latest resize/selection/response. Only YOffset (the
// actual scroll position) is meant to persist across renders; Width/Height/
// Content are cheap to recompute and must never drift from the pane's real
// budget, so they are refreshed unconditionally rather than cached.
func (m *Model) syncViewport() {
	width, height := m.previewPaneContentSize()
	m.viewport.Width = width
	m.viewport.Height = height
	m.viewport.SetContent(m.previewBodyPlain(width))
}

// syncHelpViewport refreshes m.helpViewport's Width/Height/Content from the
// current terminal geometry and help body text — called once at the end of
// every Update (see model.go), mirroring syncViewport's contract for the
// preview pane. The help overlay replaces the list+preview body entirely
// (see View), keeping the header hidden and only the footer below it, so
// the available outer height is m.height minus 1 (footer) before
// previewBodyHeight further subtracts the pane border. Only YOffset (the
// scroll position, mutated while focus==FocusHelp) is meant to persist
// across renders.
func (m *Model) syncHelpViewport() {
	outer := m.height
	if outer > 0 {
		outer--
	}
	m.helpViewport.Width = m.paneContentWidth(m.width)
	m.helpViewport.Height = previewBodyHeight(outer)
	m.helpViewport.SetContent(m.helpBodyText())
}

// previewPaneContentSize returns the preview pane's current inner content
// width/height, mirroring exactly what View would compute for the preview
// pane under the model's current mode/layout — the single source of truth
// both View and syncViewport draw from (paneContentWidth/previewPaneHeight)
// so the viewport's scroll math can never disagree with what is actually
// rendered.
func (m Model) previewPaneContentSize() (int, int) {
	// paneHeight mirrors View's own calculation: m.height minus the header
	// (1) and footer (1) that sit above/below the body.
	paneHeight := m.height
	if paneHeight > 0 {
		paneHeight -= 2
	}
	switch m.mode {
	case modeListOnly:
		return 0, 0
	default: // modeWide, or "" (unknown/headless — matches View's own default)
		_, prevW := splitWidths(m.width, m.layout)
		return m.paneContentWidth(prevW), previewBodyHeight(paneHeight)
	}
}

// ensure the viewport package import is exercised even if a future edit
// temporarily removes every direct viewport.Model reference from this file
// (Model.viewport itself is declared in model.go).
var _ viewport.Model
