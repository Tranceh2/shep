package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
// Assertions are structural (plain text after ansi.Strip); style-property
// checks live in theme_test.go.

// TestRowIcons_AreTheOnlySourceIdentity proves each source's row draws its
// presentation icon and no source-name badge.
func TestRowIcons_AreTheOnlySourceIdentity(t *testing.T) {
	t.Parallel()
	p := config.DefaultPresentations("")
	for _, tc := range []struct {
		row  Row
		icon string
	}{
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Path: "/herdr"}}, p.Herdr.Icon},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceWorkspaces, Path: "/config"}}, p.Workspaces.Icon},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceZoxide, Path: "/zoxide"}}, p.Zoxide.Icon},
		{Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects, Path: "/projects"}}, "\ue702 "},
	} {
		m := newRenderTestModel(ThemePlain, FocusList)
		primary, _ := m.rowDisplayText(tc.row)
		got := ansi.Strip(primary)
		if !strings.HasPrefix(got, tc.icon+" ") {
			t.Errorf("row %q = %q, missing its icon %q", tc.row.Candidate.Source, got, tc.icon)
		}
		for _, badge := range []string{"HERDR", "CONFIG", "ZOXIDE", "PROJECTS", "SESSION"} {
			if strings.Contains(got, badge) {
				t.Errorf("row %q = %q, contains redundant source badge %q", tc.row.Candidate.Source, got, badge)
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
		got := ansi.Strip(renderKeycap(m.styles, "enter", "open"))
		if !strings.HasPrefix(got, "enter open") {
			t.Errorf("theme %s renderKeycap = %q, want the enter open structure", theme, got)
		}
		if strings.ContainsAny(got, "[]") {
			t.Errorf("theme %s renderKeycap = %q, must not contain brackets", theme, got)
		}
	}
}

// TestFooter_NarrowKeepsHighValueHints proves a narrow footer drops hints
// by priority and keeps the high-value ones (tab, help, quit) instead of
// truncating mid-hint.
func TestFooter_NarrowKeepsHighValueHints(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{zoxideCandidate("alpha", "/home/dev/alpha")}
	pane := source.Pane{ID: "p0"}
	toggler := func(context.Context, source.Candidate) PinToggleResultMsg { return PinToggleResultMsg{} }
	wide := NewModelWithLayout(cands, nil, Layout{Theme: testTheme(ThemeMocha), PinToggler: toggler}).WithCurrentPane(&pane)
	wide, _ = update(t, wide, sizeMsg(120, 36))
	narrowM := NewModelWithLayout(cands, nil, Layout{Theme: testTheme(ThemeMocha), PinToggler: toggler}).WithCurrentPane(&pane)
	narrowM, _ = update(t, narrowM, sizeMsg(44, 24))

	if got := footerText(wide); got != "enter open · tab agents · ctrl+f pin · ctrl+t tab · ctrl+p pane · ? help · esc quit" {
		t.Errorf("wide footer = %q, want every available hint in display order", got)
	}
	if got := footerText(narrowM); got != "enter open · tab agents · ? help · esc quit" {
		t.Errorf("narrow footer = %q, want the lower-priority hints dropped whole", got)
	}
}

// TestFitHints_DropsByPriorityKeepingOrder proves the footer's drop rule:
// the highest priority number goes first (ctrl+p, then ctrl+t, ctrl+x,
// ctrl+f, esc, ?, tab, enter), and the survivors keep display order.
func TestFitHints_DropsByPriorityKeepingOrder(t *testing.T) {
	t.Parallel()
	all := []footerHint{
		{"enter", "open", hintPriorityEnter},
		{"tab", "agents", hintPriorityTab},
		{"ctrl+f", "pin", hintPriorityPin},
		{"ctrl+t", "tab", hintPriorityNewTab},
		{"ctrl+p", "pane", hintPriorityNewPane},
		{"ctrl+x", "close", hintPriorityClose},
		{"?", "help", hintPriorityHelp},
		{"esc", "quit", hintPriorityEsc},
	}
	keys := func(hints []footerHint) string {
		var out []string
		for _, h := range hints {
			out = append(out, h.key)
		}
		return strings.Join(out, ",")
	}
	const sepW = 3
	full := 0
	for _, h := range all {
		full += len(h.key) + 1 + len(h.label) + sepW
	}
	full -= sepW
	for _, tc := range []struct {
		budget int
		want   string
	}{
		{full, "enter,tab,ctrl+f,ctrl+t,ctrl+p,ctrl+x,?,esc"},
		{full - 1, "enter,tab,ctrl+f,ctrl+t,ctrl+x,?,esc"},
		{full - 15, "enter,tab,ctrl+f,ctrl+x,?,esc"},
		{43, "enter,tab,?,esc"},
		{42, "enter,tab,?"},
		{24, "enter,tab"},
		{9, ""},
	} {
		if got := keys(fitHints(all, tc.budget, sepW)); got != tc.want {
			t.Errorf("fitHints(budget %d) = %q, want %q", tc.budget, got, tc.want)
		}
	}
	if got := keys(all); got != "enter,tab,ctrl+f,ctrl+t,ctrl+p,ctrl+x,?,esc" {
		t.Errorf("fitHints mutated its input: %q", got)
	}
}

// TestFooter_EscLabelClearsOrQuits proves the esc hint names what esc does
// now: "clear" while a query is typed, "quit" otherwise.
func TestFooter_EscLabelClearsOrQuits(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 36))
	if !hasHint(m.footerHints(), keyChordEsc, "quit") {
		t.Errorf("empty query footer = %q, want esc quit", footerText(m))
	}
	m, _ = update(t, m, key("a"))
	if !hasHint(m.footerHints(), keyChordEsc, "clear") || hasHint(m.footerHints(), keyChordEsc, "quit") {
		t.Errorf("typed query footer = %q, want esc clear", footerText(m))
	}
}

// TestFooter_OmitsUnavailableActions proves the footer lists only what works
// on the highlighted row: no pin hint for a child row (no "unavailable"
// placeholder), no close hint for a row that is not an open Herdr item, and
// no tab hint when there is only one tab.
func TestFooter_OmitsUnavailableActions(t *testing.T) {
	t.Parallel()
	toggler := func(context.Context, source.Candidate) PinToggleResultMsg { return PinToggleResultMsg{} }
	closer := func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }
	m := NewModelWithLayout(nil, nil, Layout{Theme: testTheme(ThemeMocha), PinToggler: toggler, Closer: closer})
	m, _ = update(t, m, sizeMsg(120, 36))

	m.rows = []Row{{Kind: RowTab, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}}
	if hints := m.footerHints(); hasHintKey(hints, keyChordPin) || strings.Contains(footerText(m), "unavailable") {
		t.Errorf("child row footer = %q, must not offer pin", footerText(m))
	}
	m.rows = []Row{{Kind: RowCandidate, Candidate: zoxideCandidate("alpha", "/a")}}
	hints := m.footerHints()
	if !hasHint(hints, keyChordPin, "pin") {
		t.Errorf("candidate footer = %q, want ctrl+f pin", footerText(m))
	}
	if hasHintKey(hints, keyChordClose) {
		t.Errorf("zoxide row footer = %q, must not offer close for a row that is not open in Herdr", footerText(m))
	}

	single := NewModelWithLayout([]source.Candidate{zoxideCandidate("alpha", "/a")}, nil, Layout{Theme: testTheme(ThemeMocha), Tabs: []TabDefinition{{ID: "all", Kind: TabAll}}})
	if hasHintKey(single.footerHints(), keyChordTab) {
		t.Errorf("single-tab footer = %q, must not offer tab", footerText(single))
	}
}

// TestFooter_StatusPlacement proves how footer messages read: a close
// confirmation replaces the hints with the question and y highlighted; a
// close problem replaces them in the error role; success sits right-aligned
// beside the hints when it fits.
func TestFooter_StatusPlacement(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout([]source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")}, nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 36))

	confirm := m
	confirm.closeConfirm = &herdrItem{kind: "workspace", id: "w1", label: "backend"}
	if got := footerText(confirm); got != `close workspace "backend"?  y confirm · any other key cancels` {
		t.Errorf("confirmation footer = %q", got)
	}

	failed := m
	failed.actionStatus = errorStatus("close failed: boom")
	if got := footerText(failed); got != "close failed: boom" {
		t.Errorf("error footer = %q, want the error alone", got)
	}

	closed := m
	closed.actionStatus = successStatus("closed tab")
	line := ansi.Strip(closed.renderFooter(closed.geometry().ContentWidth))
	if !strings.HasPrefix(line, "enter open") || !strings.HasSuffix(line, "  closed tab") || ansi.StringWidth(line) != closed.geometry().ContentWidth {
		t.Errorf("success footer = %q, want hints with the status right-aligned", line)
	}

	narrow := closed
	narrow, _ = update(t, narrow, sizeMsg(40, 20))
	narrow.actionStatus = successStatus("closed workspace")
	if got := footerText(narrow); !strings.HasSuffix(got, "closed workspace") || !strings.HasPrefix(got, "enter open") || strings.Contains(got, "esc") {
		t.Errorf("narrow success footer = %q, want low-priority hints replaced by the status", got)
	}
}

// TestFooterAndHelp_NoMouseLanguage proves no footer/help surface ever leaks
// mouse-adjacent wording — the picker is keyboard-first.
func TestFooterAndHelp_NoMouseLanguage(t *testing.T) {
	t.Parallel()
	m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha)})
	m, _ = update(t, m, sizeMsg(120, 36))
	for _, got := range []string{footerText(m), m.helpBodyText(80), m.helpBodyText(120)} {
		plain := strings.ToLower(ansi.Strip(got))
		for _, word := range []string{"click", "mouse", "drag"} {
			if strings.Contains(plain, word) {
				t.Errorf("footer/help text = %q, must not use mouse language %q", plain, word)
			}
		}
	}
}

// TestPreviewTitle_IsLabelNotPath proves the preview title row names the
// candidate by its label — never the path, which the body shows — with its
// kind on the right, for built-in and custom sources alike.
func TestPreviewTitle_IsLabelNotPath(t *testing.T) {
	t.Parallel()
	m := newRenderTestModel(ThemeMocha, FocusList)
	m.rows = []Row{{Kind: RowCandidate, Candidate: herdrCandidate("backend", "/srv/backend", "w1")}}
	if title := strings.Fields(ansi.Strip(m.renderPreviewTitle(40))); len(title) != 2 || title[0] != "backend" || title[1] != "workspace" {
		t.Errorf("preview title = %q, want the label and its kind", title)
	}

	m.rows = []Row{{Kind: RowCandidate, Candidate: source.Candidate{Label: "scratch-buffer", Source: "hermes"}}}
	if title := strings.Fields(ansi.Strip(m.renderPreviewTitle(40))); len(title) != 2 || title[0] != "scratch-buffer" || title[1] != "hermes" {
		t.Errorf("custom source preview title = %q, want the label and the source name", title)
	}
}
