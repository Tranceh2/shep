package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// render_roles_test.go — the visual feedback contract:
//
//   - source identity is rendered by configured row icons, without source-name
//     badges or reserved badge width.
//   - pane rows retain compact status icons, while no textual TAB/PANE badges
//     are rendered.
//   - footer shortcuts use clean key tokens and action labels.
//
// Assertions are structural (plain text after stripNonSGRANSI); style-property
// checks live in theme_test.go, forced-profile ANSI checks are avoided
// (headless test runs run a no-color profile — see spinner_style_test.go for
// the single, deliberate exception).

// TestRowIcons_AreTheOnlySourceIdentity proves each configured source row
// keeps its icon and does not render a source-name badge.
func TestRowIcons_AreTheOnlySourceIdentity(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Path: "/herdr", Icon: "H"}},
		{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceWorkspaces, Path: "/config", Icon: "C"}},
		{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Path: "/zoxide", Icon: "Z"}},
		{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Path: "/projects", Icon: "P"}},
	}
	m := newRenderTestModel(ThemePlain, FocusList)
	for _, row := range rows {
		primary, _ := m.rowDisplayText(row)
		got := stripNonSGRANSI(primary)
		if !strings.Contains(got, row.Candidate.Icon+" ") {
			t.Errorf("row %q = %q, missing configured icon", row.Candidate.Source, got)
		}
		for _, badge := range []string{"HERDR", "CONFIG", "ZOXIDE", "PROJECTS", "SESSION"} {
			if strings.Contains(got, badge) {
				t.Errorf("row %q = %q, contains redundant source badge %q", row.Candidate.Source, got, badge)
			}
		}
	}
}

// TestRenderKeycap_PlainStructure proves the clean shortcut shape is key label
// in every theme (plain included), with no bracket decoration.
func TestRenderKeycap_PlainStructure(t *testing.T) {
	t.Parallel()
	for _, theme := range []string{ThemeMocha, ThemePlain} {
		m := newRenderTestModel(theme, FocusList)
		got := renderKeycap(m.styles, "enter", "open")
		if !strings.HasPrefix(got, "enter open") {
			t.Errorf("theme %s renderKeycap = %q, want the enter open structure", theme, got)
		}
		if strings.ContainsAny(got, "[]") {
			t.Errorf("theme %s renderKeycap = %q, must not contain brackets", theme, got)
		}
	}
}

// TestFooterHints_NarrowDropsPreviewFirst proves narrow mode drops preview
// first and retains the high-value help/quit hints.
func TestFooterHints_NarrowDropsPreviewFirst(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}
	wide := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	wide, _ = update(t, wide, sizeMsg(120, 36))
	narrowM := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	narrowM, _ = update(t, narrowM, sizeMsg(64, 24))

	narrowPlain := stripNonSGRANSI(narrowM.footerHints())
	if strings.Contains(narrowPlain, "tab preview") {
		t.Errorf("narrow footer = %q, must drop the preview hint first", narrowPlain)
	}
	for _, essential := range []string{"? help", "esc quit"} {
		if !strings.Contains(narrowPlain, essential) {
			t.Errorf("narrow footer = %q, want %q retained", narrowPlain, essential)
		}
	}
	widePlain := stripNonSGRANSI(wide.footerHints())
	if !strings.Contains(widePlain, "tab preview") {
		t.Errorf("wide footer = %q, want the tab preview hint", widePlain)
	}
}

// TestFooterAndHelp_NoMouseLanguage proves no footer/help surface ever leaks
// mouse-adjacent wording — the picker is keyboard-first.
func TestFooterAndHelp_NoMouseLanguage(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 36))
	for _, got := range []string{m.footerHints(), m.helpBodyText(), m.renderFooter()} {
		plain := strings.ToLower(stripNonSGRANSI(got))
		for _, word := range []string{"click", "mouse", "drag"} {
			if strings.Contains(plain, word) {
				t.Errorf("footer/help text = %q, must not use mouse language %q", plain, word)
			}
		}
	}
}

// TestPreviewTopBorderText_IsLabelTitle proves the preview top border title
// is " PREVIEW · <label> " — the label, never the path (the path lives in the
// body's identity section) — with the ASCII separator fallback under
// [tui].icons = "ascii".
func TestPreviewTopBorderText_IsLabelTitle(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.layout.Icons = IconsUnicode
	m.height = 30
	m.rows = []Row{{Kind: RowCandidate, Candidate: herdrCandidate("backend", "/srv/backend", "w1")}}
	m.cursor = 0
	title := m.previewTopBorderText()
	if !strings.Contains(title, "PREVIEW") || !strings.Contains(title, "backend") {
		t.Errorf("preview title = %q, want \" PREVIEW · backend \"", title)
	}
	if strings.Contains(title, "/srv/backend") {
		t.Errorf("preview title = %q, must not carry the path", title)
	}

	// ASCII icon tier: the separator falls back to "-"; no Unicode glyph.
	m.layout.Icons = IconsASCII
	titleASCII := m.previewTopBorderText()
	if !strings.Contains(titleASCII, "PREVIEW -") {
		t.Errorf("ASCII preview title = %q, want the ASCII separator fallback", titleASCII)
	}
	if strings.Contains(titleASCII, "·") {
		t.Errorf("ASCII preview title = %q, must not emit a Unicode-only separator", titleASCII)
	}

	// An integration candidate's preview title uses its own label too.
	m.layout.Icons = IconsUnicode
	m.rows = []Row{{Kind: RowCandidate, Candidate: source.Candidate{Label: "scratch-buffer", Source: "hermes"}}}
	if got := m.previewTopBorderText(); !strings.Contains(got, "scratch-buffer") {
		t.Errorf("integration preview title = %q, want the label", got)
	}
}
