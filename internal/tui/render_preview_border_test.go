package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/source"
)

func TestRenderPreviewTopBorder(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name        string
		icons       string
		outerWidth  int
		text        string
		wantPrefix  string
		wantSuffix  string
		wantContain string
		wantExact   string
	}{
		{
			name:        "short Unicode path remains verbatim",
			outerWidth:  20,
			text:        "/srv/api",
			wantPrefix:  "┌/srv/api",
			wantSuffix:  "┐",
			wantContain: "/srv/api",
		},
		{
			name:        "long Unicode path truncates from the left",
			outerWidth:  16,
			text:        "/srv/projects/services/catalog/handler.go",
			wantPrefix:  "┌…",
			wantSuffix:  "handler.go┐",
			wantContain: "…",
		},
		{
			name:       "empty selection has no placeholder",
			outerWidth: 12,
			wantExact:  "┌──────────┐",
		},
		{
			name:        "ASCII tier uses ASCII border glyphs",
			icons:       IconsASCII,
			outerWidth:  16,
			text:        "C:/srv/api",
			wantPrefix:  "+C:/srv/api",
			wantSuffix:  "+",
			wantContain: "-",
			wantExact:   "+C:/srv/api----+",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newRenderTestModel(ThemePlain, FocusList)
			m.layout.Icons = tt.icons

			got := m.renderPreviewTopBorder(m.paneBoxStyle(0, false), tt.outerWidth, tt.text)
			if width := lipgloss.Width(got); width != tt.outerWidth {
				t.Errorf("visible width = %d, want %d: %q", width, tt.outerWidth, got)
			}
			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("top border = %q, want %q", got, tt.wantExact)
			}
			if tt.wantPrefix != "" && !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("top border = %q, want prefix %q", got, tt.wantPrefix)
			}
			if tt.wantSuffix != "" && !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("top border = %q, want suffix %q", got, tt.wantSuffix)
			}
			if tt.wantContain != "" && !strings.Contains(got, tt.wantContain) {
				t.Errorf("top border = %q, want to contain %q", got, tt.wantContain)
			}
			if strings.Contains(got, "·") {
				t.Errorf("top border = %q, must not render a placeholder", got)
			}
		})
	}
}

func TestView_PreviewTopBorderUsesCurrentCandidatePathOrLabel(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		candidates []source.Candidate
		want       string
	}{
		{
			name:       "path",
			candidates: []source.Candidate{herdrCandidate("api", "/srv/api", "w1")},
			want:       "/srv/api",
		},
		{
			name:       "label fallback",
			candidates: []source.Candidate{herdrCandidate("scratch-buffer", "", "w1")},
			want:       "scratch-buffer",
		},
		{
			name: "no selection stays empty",
			want: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModelWithLayout(tt.candidates, nil, Layout{Theme: ThemePlain})
			m.width = 80
			m.height = 12

			lines := strings.Split(m.View(), "\n")
			if len(lines) < 2 {
				t.Fatalf("View() returned %d lines, want header and pane row", len(lines))
			}
			top := lines[1]
			if tt.want != "" && !strings.Contains(top, tt.want) {
				t.Errorf("preview top edge = %q, want current selection %q", top, tt.want)
			}
			if tt.want == "" && strings.Contains(top, "·") {
				t.Errorf("preview top edge = %q, must not show an empty-selection placeholder", top)
			}
		})
	}
}
