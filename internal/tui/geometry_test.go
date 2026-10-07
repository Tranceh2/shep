package tui

import "testing"

// TestComputePickerGeometry_MarginsAndChrome proves the grid budget: one
// side margin from sideMarginMinWidth, four chrome rows, and in list-only
// mode the list taking the whole content width with no preview column.
func TestComputePickerGeometry_MarginsAndChrome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		width, height int
		mode          string
		wantMargin    int
		wantList      int
		wantPreview   bool
	}{
		{"narrow list-only has no margin", 59, 20, modeListOnly, 0, 59, false},
		{"margin from 60 columns", 60, 20, modeListOnly, 1, 58, false},
		{"72x20 list-only", 72, 20, modeListOnly, 1, 70, false},
		{"wide splits the content", 140, 38, modeWide, 1, 74, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := computePickerGeometry(tc.width, tc.height, tc.mode, Layout{})
			if g.Margin != tc.wantMargin || g.ContentWidth != tc.width-2*tc.wantMargin {
				t.Errorf("margin/content = %d/%d, want %d/%d", g.Margin, g.ContentWidth, tc.wantMargin, tc.width-2*tc.wantMargin)
			}
			if g.ListWidth != tc.wantList || (g.PreviewWidth > 0) != tc.wantPreview {
				t.Errorf("list/preview = %d/%d, want list %d, preview shown %v", g.ListWidth, g.PreviewWidth, tc.wantList, tc.wantPreview)
			}
			if g.ListInnerRows != tc.height-chromeRows || g.PreviewInnerRows != g.ListInnerRows {
				t.Errorf("body rows = %d/%d, want %d", g.ListInnerRows, g.PreviewInnerRows, tc.height-chromeRows)
			}
		})
	}
}

// TestComputePickerGeometry_UnsizedIsHeadlessWide proves a model that has
// not received a size yet lays out at headlessWidth, wide, with no body
// rows (the list then renders every row).
func TestComputePickerGeometry_UnsizedIsHeadlessWide(t *testing.T) {
	t.Parallel()
	g := computePickerGeometry(0, 0, "", Layout{})
	if g.ContentWidth != headlessWidth-2 || g.PreviewWidth == 0 || g.ListInnerRows != 0 {
		t.Errorf("unsized geometry = %+v, want headless wide with zero body rows", g)
	}
}

// TestSplitColumns proves the width split: the configured share applies to
// the columns left after the divider, the list floor holds only while the
// preview keeps minPreviewColumns, a large list share never squeezes the
// preview below previewColumnFloor, and a content width too small for two
// columns yields no preview.
func TestSplitColumns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		content     int
		layout      Layout
		wantList    int
		wantPreview int
	}{
		{"default share", 138, Layout{}, 74, 61},
		{"popup inner width", 116, Layout{}, 62, 51},
		{"list floor at the breakpoint", 78, Layout{}, 41, 34},
		{"list floor applies while the preview keeps 30", 70, Layout{ListWidth: "30%"}, 34, 33},
		{"list floor yields to the preview minimum", 60, Layout{ListWidth: "30%"}, 27, 30},
		{"list_width", 103, Layout{ListWidth: "40%"}, 40, 60},
		{"preview_width", 103, Layout{PreviewWidth: "40%"}, 60, 40},
		{"both scale when over 100%", 103, Layout{ListWidth: "80%", PreviewWidth: "80%"}, 50, 50},
		{"preview floor", 103, Layout{ListWidth: "98%"}, 90, 10},
		{"too narrow for two columns", 4, Layout{}, 4, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, preview := splitColumns(tc.content, tc.layout)
			if list != tc.wantList || preview != tc.wantPreview {
				t.Errorf("splitColumns(%d, %+v) = (%d, %d), want (%d, %d)", tc.content, tc.layout, list, preview, tc.wantList, tc.wantPreview)
			}
			if preview > 0 && list+dividerWidth+preview != tc.content {
				t.Errorf("list+divider+preview = %d, want the content width %d", list+dividerWidth+preview, tc.content)
			}
		})
	}
}
