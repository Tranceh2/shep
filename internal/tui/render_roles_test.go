package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// render_roles_test.go — the visual redesign's render-role contract:
//
//   - sourceBadge maps every source to its badge text, with a declared
//     [[integrations]] source rendering its own NAME uppercased (and
//     truncated sensibly), and no badge for source-less candidates.
//   - badge collapse in narrow terminals: below badgeMinTerminalWidth text
//     source badges disappear (icons/pins/status markers keep the structure).
//   - status label mapping: agent_status -> WORKING/IDLE/DONE/BLOCKED/UNKNOWN.
//   - keycap plain structure: [chord] label — no mouse language anywhere.
//
// Assertions are structural (plain text after stripNonSGRANSI); style-property
// checks live in theme_test.go, forced-profile ANSI checks are avoided
// (headless test runs run a no-color profile — see spinner_style_test.go for
// the single, deliberate exception).

// TestSourceBadge_SourceMappingAndIntegrations proves the badge text per
// source, integration name uppercasing/truncation, and the empty case: a
// direct --path candidate (no source) earns no badge.
func TestSourceBadge_SourceMappingAndIntegrations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"herdr", config.SourceHerdr, "HERDR"},
		{"projects", config.SourceProjects, "PROJECTS"},
		{"zoxide", config.SourceZoxide, "ZOXIDE"},
		{"configured workspaces read CONFIG", config.SourceWorkspaces, "CONFIG"},
		{"sessions", config.SourceSessions, "SESSION"},
		{"integration name uppercased", "hermes", "HERMES"},
		{"integration name truncated sensibly", "a-very-long-integration-name", "A-VERY-LONG-I…"},
		{"direct-path candidate has no badge", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceBadge(tt.source); got != tt.want {
				t.Errorf("sourceBadge(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}

// TestSourceBadgeStyleFor_BindingAndPlain proves each declared source binds a
// dedicated badge style; integration/unknown sources fall back to the
// integration accent badge; plain keeps bold structure with no color and the
// dim variant goes faint. Style identity is proven by GetForeground()
// equality (lipgloss.Style is not comparable — wrap in reflect.DeepEqual).
func TestSourceBadgeStyleFor_BindingAndPlainStructure(t *testing.T) {
	t.Parallel()
	s := newPalette(themes[ThemeMocha])
	if fg := s.sourceBadgeStyleFor(config.SourceHerdr).GetForeground(); !reflect.DeepEqual(fg, s.sourceHerdrStyle.GetForeground()) {
		t.Errorf("herdr badge fg = %#v, want the dedicated herdr role %#v", fg, s.sourceHerdrStyle.GetForeground())
	}
	if fg := s.sourceBadgeStyleFor(config.SourceWorkspaces).GetForeground(); !reflect.DeepEqual(fg, s.sourceWorkspacesStyle.GetForeground()) {
		t.Errorf("workspaces badge fg = %#v, want the dedicated CONFIG role", fg)
	}
	if fg := s.sourceBadgeStyleFor("hermes").GetForeground(); fg != nil && !reflect.DeepEqual(fg, s.sourceBadgeStyle.GetForeground()) {
		t.Errorf("integration badge fg = %#v, want the integration accent role", fg)
	}
	if s.sourceHerdrStyle.GetForeground() == s.sourceProjectsStyle.GetForeground() {
		t.Error("herdr and projects badge colors must differ (color is secondary but present)")
	}

	plain := newPalette(themes[ThemePlain])
	herdr := plain.sourceBadgeStyleFor(config.SourceHerdr)
	if !herdr.GetBold() {
		t.Error("plain herdr badge must be bold (structural emphasis)")
	}
	if fg := herdr.GetForeground(); fg != (lipgloss.NoColor{}) {
		t.Errorf("plain herdr badge fg = %#v, want no color", fg)
	}
	if !herdr.Faint(true).GetFaint() {
		t.Error("plain dim badge must be faint")
	}
}

// TestRowBadgeParts_NarrowCollapsesSourceBadge proves the narrow
// badgeMinTerminalWidth rule: no text source badge at narrow widths, badge
// granted at wide widths, and the badge text width matches its plain text.
func TestRowBadgeParts_NarrowCollapsesSourceBadge(t *testing.T) {
	t.Parallel()
	row := Row{Kind: RowCandidate, Candidate: herdrCandidate("backend", "/srv/backend", "w1")}
	build := func(width int) Model {
		m := newRenderTestModel(ThemeMocha, FocusList)
		m.width = width
		return m
	}

	badges, w := build(64).rowBadgeParts(row, true)
	if badges != "" || w != 0 {
		t.Errorf("narrow (64) badges = %q width %d, want none (text badge hidden)", badges, w)
	}

	badges, w = build(120).rowBadgeParts(row, true)
	if !strings.Contains(stripNonSGRANSI(badges), "HERDR") {
		t.Errorf("wide (120) badges = %q, want the HERDR badge", badges)
	}
	if w != len("HERDR") {
		t.Errorf("wide (120) badge width = %d, want %d", w, len("HERDR"))
	}
}

// TestRowBadgeParts_StatusBadgePerKind proves the pane status text badge
// mapping (panes only; a tab row carries no status).
func TestRowBadgeParts_StatusBadgePerKind(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.width = 120
	pane := Row{
		Kind: RowPane,
		Candidate: source.Candidate{
			Label: "p1", Path: "/srv/api",
			Meta: map[string]string{"agent_status": "working"},
		},
	}
	badges, w := m.rowBadgeParts(pane, true)
	if got := stripNonSGRANSI(badges); got != "PANE WORKING" {
		t.Errorf("pane badges = %q, want the PANE WORKING status text badges", got)
	}
	if w != len("PANE WORKING") {
		t.Errorf("pane badge width = %d, want %d", w, len("PANE WORKING"))
	}
	if badges2, w2 := m.rowBadgeParts(Row{Kind: RowTab}, true); stripNonSGRANSI(badges2) != "TAB" || w2 != len("TAB") {
		t.Errorf("tab badges = %q/%d, want the TAB status text badge", badges2, w2)
	}
}

// TestStatusBadgeLabel_Mapping proves the status labels are uppercase text —
// color is never the signal; every status carries a text label.
func TestStatusBadgeLabel_Mapping(t *testing.T) {
	t.Parallel()
	tests := []struct{ status, want string }{
		{StatusIdle, "IDLE"},
		{StatusWorking, "WORKING"},
		{StatusBlocked, "BLOCKED"},
		{StatusDone, "DONE"},
		{StatusUnknown, "UNKNOWN"},
		{"", ""},
		{"bogus", ""},
	}
	for _, tt := range tests {
		if got := statusBadgeLabel(tt.status); got != tt.want {
			t.Errorf("statusBadgeLabel(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// TestRenderKeycap_PlainStructure proves the keycap shape is [chord] label in
// every theme (plain included): the footer structure is theme-independent.
func TestRenderKeycap_PlainStructure(t *testing.T) {
	t.Parallel()
	for _, theme := range []string{ThemeMocha, ThemePlain} {
		m := newRenderTestModel(theme, FocusList)
		got := renderKeycap(m.styles, "enter", "open")
		if !strings.HasPrefix(got, "[enter] open") {
			t.Errorf("theme %s renderKeycap = %q, want the [enter] open structure", theme, got)
		}
	}
}

// TestFooterHints_NarrowDropsTabFirst proves the narrow footer drops the
// [tab] keycap first and never loses the essential [?]/[esc] keycaps.
func TestFooterHints_NarrowDropsTabFirst(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}
	wide := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	wide, _ = update(t, wide, sizeMsg(120, 36))
	narrowM := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	narrowM, _ = update(t, narrowM, sizeMsg(64, 24))

	narrowPlain := stripNonSGRANSI(narrowM.footerHints())
	if strings.Contains(narrowPlain, "[tab]") {
		t.Errorf("narrow footer = %q, must drop the [tab] keycap first", narrowPlain)
	}
	for _, essential := range []string{"[esc]", "[?]"} {
		if !strings.Contains(narrowPlain, essential) {
			t.Errorf("narrow footer = %q, want %q retained", narrowPlain, essential)
		}
	}
	widePlain := stripNonSGRANSI(wide.footerHints())
	if !strings.Contains(widePlain, "[tab] preview") {
		t.Errorf("wide footer = %q, want the [tab] preview keycap", widePlain)
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
