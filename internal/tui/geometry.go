package tui

const paneChromeRows = 2

const (
	chromeRows        = paneChromeRows
	previewChromeRows = paneChromeRows
)

type pickerGeometry struct {
	HeaderLines      int
	FooterLines      int
	PaneOuterHeight  int
	ListInnerRows    int
	PreviewInnerRows int
	ListWidth        int
	PreviewWidth     int
}

func headerLineCountForWidth(width int) int {
	if width >= headerWideBreakpoint {
		return 2
	}
	return 1
}

func computePickerGeometry(width, height int, mode string, layout Layout) pickerGeometry {
	g := pickerGeometry{
		HeaderLines: headerLineCountForWidth(width),
		FooterLines: 1,
	}
	g.PaneOuterHeight = max(0, height-g.HeaderLines-g.FooterLines)
	g.ListInnerRows = max(0, g.PaneOuterHeight-paneChromeRows)
	g.PreviewInnerRows = g.ListInnerRows
	if mode != modeListOnly {
		g.ListWidth, g.PreviewWidth = splitWidths(width, layout)
	}
	return g
}

func (m Model) geometry() pickerGeometry {
	return computePickerGeometry(m.width, m.height, m.mode, m.layout)
}
