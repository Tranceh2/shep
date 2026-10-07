package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// TestView_PreviewTitleUsesLabelOrPath proves the preview column of the
// prompt row names the highlighted candidate — its label, else its path —
// without a "PREVIEW" caption, and stays blank with nothing highlighted.
func TestView_PreviewTitleUsesLabelOrPath(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		candidates []source.Candidate
		want       string
		notWant    string
	}{
		{
			name:       "label title",
			candidates: []source.Candidate{herdrCandidate("api", "/srv/api", "w1")},
			want:       "api",
			notWant:    "/srv/api",
		},
		{
			// The title is the name the row shows: a path label leads with
			// its last directory.
			name:       "no-label falls back to path, filename first",
			candidates: []source.Candidate{herdrCandidate("", "/srv/api", "w1")},
			want:       "api",
		},
		{
			name: "no selection stays empty",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModelWithLayout(tt.candidates, nil, Layout{Theme: testTheme(ThemePlain)})
			m.width = 80
			m.height = 12

			g := m.geometry()
			lines := viewLines(m)
			if len(lines) != 12 {
				t.Fatalf("View() returned %d lines, want 12", len(lines))
			}
			_, title, ok := strings.Cut(lines[1], "│")
			if !ok {
				t.Fatalf("prompt row %q has no divider", lines[1])
			}
			title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(title), "workspace"))
			if tt.want != title {
				t.Errorf("preview title = %q, want %q", title, tt.want)
			}
			if tt.notWant != "" && strings.Contains(title, tt.notWant) {
				t.Errorf("preview title = %q, must not carry %q", title, tt.notWant)
			}
			if strings.Contains(lines[1], "PREVIEW") {
				t.Errorf("prompt row = %q, must not carry a PREVIEW caption", lines[1])
			}
			if got := m.renderPreviewTitle(g.PreviewWidth); len(got) != g.PreviewWidth && tt.want == "" {
				t.Errorf("empty title = %q, want %d blank cells", got, g.PreviewWidth)
			}
		})
	}
}

// TestRenderPreviewTitle_TruncatesByKind proves a title that does not fit
// keeps what identifies it — a path its last directory, any other label its
// start — after the kind label is dropped.
func TestRenderPreviewTitle_TruncatesByKind(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemePlain, FocusList)
	m.rows = []Row{{Kind: RowCandidate, Candidate: source.Candidate{Label: "/srv/a/very/long/path/to/whiterose-db/", Source: config.SourceZoxide}}}
	if got := strings.TrimSpace(m.renderPreviewTitle(20)); got != "whiterose-db  folder" {
		t.Errorf("filename-first title = %q, want the name and its kind", got)
	}
	m.rows = []Row{{Kind: RowCandidate, Candidate: source.Candidate{Label: "Refactor the render path of shep", Source: config.SourceAgents}}}
	if got := strings.TrimSpace(m.renderPreviewTitle(20)); !strings.HasPrefix(got, "Refactor") || !strings.HasSuffix(got, "…") {
		t.Errorf("label title = %q, want the right-truncated start with the kind dropped", got)
	}
	m.rows = []Row{{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Path: "/"}}}
	if got := strings.Fields(m.renderPreviewTitle(20)); len(got) != 2 || got[0] != "/" || got[1] != "pane" {
		t.Errorf("root pane title = %q", got)
	}
}
