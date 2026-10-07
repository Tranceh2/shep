package tui

import "github.com/tranceh2/shep/internal/config"

// The picker is one borderless grid. Herdr's popup frame (or, in a bare
// terminal, the window itself) is the only frame; regions are separated by
// one rule row and a vertical divider instead of nested boxes, which would
// cost four columns of chrome each:
//
//	row 0         tab strip
//	row 1         prompt + count (list column) │ preview title (preview column)
//	row 2         rule, with a junction under the divider
//	rows 3..H-2   list rows │ preview body
//	row H-1       footer
//
// List-only mode keeps the same rows without the preview column.
const chromeRows = 4

// dividerWidth is the vertical divider between the two columns: one rule
// glyph with a space on each side, so neither column's text touches it.
const dividerWidth = 3

// sideMarginMinWidth is the width from which one blank column frames the grid
// on each side. Below it every column goes to content: a narrow terminal
// needs the cells more than the breathing room.
const sideMarginMinWidth = 60

// Column floors for the wide split. minListColumns keeps a list row (gutter,
// icon, a readable name) intact; it only applies while the preview can still
// keep minPreviewColumns, so the floor never turns the preview into a sliver.
// previewColumnFloor stops a large list_width override from squeezing the
// preview below a usable width.
const (
	minListColumns     = 34
	minPreviewColumns  = 30
	previewColumnFloor = 10
)

// defaultListPercent is the list column's share of the split when neither
// list_width nor preview_width is configured.
const defaultListPercent = 55

// headlessWidth is the width the geometry assumes before the first
// tea.WindowSizeMsg (tests, or a program that has not been sized yet).
const headlessWidth = 80

// pickerGeometry is the single source of truth for the grid's cell budget.
// View, the viewports and the half-page keys all read it, so the rendered
// columns and the scroll math can never disagree.
type pickerGeometry struct {
	// Margin is the blank columns on each side of the grid.
	Margin int
	// ContentWidth is the width between the margins: the tab strip, rule
	// and footer span it; in wide mode it holds list + divider + preview.
	ContentWidth int
	// ListWidth is the list column width (ContentWidth in list-only mode).
	ListWidth int
	// PreviewWidth is the preview column width; 0 means no preview column.
	PreviewWidth int
	// ListInnerRows and PreviewInnerRows are the body rows below the chrome.
	// Both columns share the body, so they are always equal.
	ListInnerRows    int
	PreviewInnerRows int
}

// computePickerGeometry derives the grid for a width x height terminal in
// mode. Any mode but list-only splits the content into two columns (the
// unsized "" mode renders wide, matching the headless default).
func computePickerGeometry(width, height int, mode string, layout Layout) pickerGeometry {
	if width <= 0 {
		width = headlessWidth
	}
	var g pickerGeometry
	if width >= sideMarginMinWidth {
		g.Margin = 1
	}
	g.ContentWidth = width - 2*g.Margin
	g.ListWidth = g.ContentWidth
	if mode != modeListOnly {
		g.ListWidth, g.PreviewWidth = splitColumns(g.ContentWidth, layout)
	}
	g.ListInnerRows = max(0, height-chromeRows)
	g.PreviewInnerRows = g.ListInnerRows
	return g
}

func (m Model) geometry() pickerGeometry {
	return computePickerGeometry(m.width, m.height, m.mode, m.layout)
}

// splitColumns divides content columns into list and preview widths around
// the divider. The configured share applies to the columns left after the
// divider; the floors then protect both columns (see minListColumns). A
// content width too small for two columns returns no preview at all.
func splitColumns(content int, layout Layout) (list, preview int) {
	avail := content - dividerWidth
	if avail < 2 {
		return content, 0
	}
	list = configuredListShare(avail, layout)
	list = max(list, min(minListColumns, avail-minPreviewColumns))
	list = min(list, avail-previewColumnFloor)
	list = max(list, 1)
	return list, avail - list
}

// configuredListShare applies list_width/preview_width (percentages or
// "auto") to avail columns. When both are set and sum past 100% the list
// share is scaled down proportionally; the preview always takes the rest.
func configuredListShare(avail int, layout Layout) int {
	listFrac, listOK := config.PercentOrAuto(layout.ListWidth)
	prevFrac, prevOK := config.PercentOrAuto(layout.PreviewWidth)
	switch {
	case listOK && prevOK:
		if total := listFrac + prevFrac; total > 1 {
			listFrac /= total
		}
		return int(float64(avail) * listFrac)
	case listOK:
		return int(float64(avail) * listFrac)
	case prevOK:
		return avail - int(float64(avail)*prevFrac)
	default:
		return avail * defaultListPercent / 100
	}
}
