package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
)

// === Footer/help KeyMap unification ===
//
// These tests prove the footer's compact hints and the full "?" help
// overlay render their key-chord notation from the SAME keyBinding values
// (keyBindingEnter, keyBindingTab, keyBindingCtrlT, keyBindingCtrlP,
// keyBindingEsc, keyBindingHelp) instead of two independently hand-typed
// string literals, so the two can never drift apart.

// TestFooterHints_MatchSharedKeyBindingValues proves footerHints() builds
// each hint from the shared keyBinding vars' footerChord/footerLabel fields,
// not independent literals, and that the footer row renders them as
// "key label" pairs joined by the shared separator.
func TestFooterHints_MatchSharedKeyBindingValues(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))

	want := []footerHint{
		{keyBindingEnter.footerChord, keyBindingEnter.footerLabel, hintPriorityEnter},
		{keyBindingTab.footerChord, keyBindingTab.footerLabel, hintPriorityTab},
		{keyBindingHelp.footerChord, keyBindingHelp.footerLabel, hintPriorityHelp},
		{keyBindingEsc.footerChord, keyBindingEsc.footerLabel, hintPriorityEsc},
	}
	if got := m.footerHints(); !slices.Equal(got, want) {
		t.Errorf("footerHints() = %+v, want %+v", got, want)
	}
	wantText := "enter open · tab agents · ? help · esc quit"
	if got := footerText(m); got != wantText {
		t.Errorf("footer row = %q, want %q", got, wantText)
	}
}

// TestFooterHints_HerdrSegmentsMatchSharedKeyBindingValues proves the
// conditional ctrl+t/ctrl+p footer hints (shown only when the current pane +
// candidate support a workspace target) also come from the shared
// keyBindingCtrlT/keyBindingCtrlP values.
func TestFooterHints_HerdrSegmentsMatchSharedKeyBindingValues(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	pane := source.Pane{ID: "p0"}
	m = m.WithCurrentPane(&pane)

	hints := m.footerHints()
	if !hasHint(hints, keyBindingCtrlT.footerChord, keyBindingCtrlT.footerLabel) {
		t.Errorf("footerHints() = %+v, missing the shared ctrl+t hint", hints)
	}
	if !hasHint(hints, keyBindingCtrlP.footerChord, keyBindingCtrlP.footerLabel) {
		t.Errorf("footerHints() = %+v, missing the shared ctrl+p hint", hints)
	}
}

// helpLines renders the cheat sheet at width as plain lines.
func helpLines(m Model, width int) []string {
	return strings.Split(ansi.Strip(m.helpBodyText(width)), "\n")
}

// TestKeyMap_FooterBindingsAppearInHelpBody is the drift guard: every
// cheat-sheet binding that also appears in the footer is shown in the help
// with its chord and description, both read from the same keyBinding value
// the footer uses, so a shared chord can only be edited in one place.
func TestKeyMap_FooterBindingsAppearInHelpBody(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	body := strings.Join(helpLines(m, 80), "\n")
	var checked int
	for _, section := range keyMap {
		for _, b := range section.bindings {
			if b.footerChord == "" {
				continue
			}
			checked++
			help := b.help
			if b.chord == keyChordEnter {
				help = m.enterHelpText()
			}
			if !strings.Contains(body, b.chord) || !strings.Contains(body, help) {
				t.Errorf("help body is missing %q / %q", b.chord, help)
			}
		}
	}
	if checked == 0 {
		t.Fatal("setup: expected at least one keyMap binding with a footerChord")
	}
}

// TestHelpColumn_KeyWidthFromContent proves every description of a column
// starts at the same cell: the indent, the column's widest keys, two spaces.
func TestHelpColumn_KeyWidthFromContent(t *testing.T) {
	t.Parallel()
	m := NewModel(nil, nil)
	sections := []helpSection{{"A", []keyBinding{{chord: "x", help: "one"}, {chord: "ctrl+long", help: "two"}}}}
	lines := m.helpColumn(sections, 40, false)
	want := []string{"A", "  x          one", "  ctrl+long  two"}
	for i, line := range lines {
		if got := strings.TrimRight(ansi.Strip(line), " "); got != want[i] {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}
}

// TestHelpBodyText_CheatSheet proves the cheat sheet's content and layout:
// the five shortcut sections and the search syntax, side by side from 100
// columns and stacked below; arrows under the Unicode tier, spelled out
// under ASCII; the Enter line following the highlighted row; and no
// unreachable preview-focus or meta help sections.
func TestHelpBodyText_CheatSheet(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))

	wide := helpLines(m, 110)
	if !strings.HasPrefix(wide[0], "Navigate") || !strings.Contains(wide[0], "Search syntax") {
		t.Errorf("wide first line = %q, want Navigate beside Search syntax", wide[0])
	}
	narrow := helpLines(m, 90)
	body := strings.Join(narrow, "\n")
	order := []string{"Navigate", "Act", "Search", "Layout", "Session", "Search syntax"}
	at := -1
	for _, title := range order {
		i := slices.Index(narrow, title)
		if i <= at {
			t.Errorf("section %q at line %d, want it after line %d (one column, shortcuts then syntax)", title, i, at)
		}
		at = i
	}
	for _, want := range []string{"↑/↓, ctrl+k/ctrl+j", "←/→", "enter", "open the highlighted row", "ctrl+t", "open as a tab here", "esc", "clear search, then quit", "ctrl+c, ctrl+g"} {
		if !strings.Contains(body, want) {
			t.Errorf("help is missing %q", want)
		}
	}
	for _, gone := range []string{"Preview (while focused)", "Help (this screen)", "any letter"} {
		if strings.Contains(body, gone) {
			t.Errorf("help still shows %q", gone)
		}
	}

	m.rows = []Row{{Kind: RowTab, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}}
	if body := strings.Join(helpLines(m, 90), "\n"); !strings.Contains(body, helpLabelFocusTab) {
		t.Errorf("help Enter line does not follow the tab row: %q", body)
	}

	ascii := NewModelWithLayout(nil, nil, Layout{Icons: IconsASCII})
	asciiBody := strings.Join(helpLines(ascii, 110), "\n")
	if reNonASCII.MatchString(asciiBody) || !strings.Contains(asciiBody, "up/down, ctrl+k/ctrl+j") || !strings.Contains(asciiBody, "left/right") {
		t.Errorf("ASCII help = %q, want spelled-out arrows and ASCII only", asciiBody)
	}
}

// TestSearchSyntax_DocumentedExamplesParse parses every documented search
// syntax example with the real parser and checks it means what the help
// says, so the cheat sheet can never lie about the parser.
func TestSearchSyntax_DocumentedExamplesParse(t *testing.T) {
	t.Parallel()
	type want struct {
		clauses, alternatives int
		kind                  fuzzy.TermKind
		inverse, exactFull    bool
		field, pattern        string
	}
	expect := map[string]want{
		"api web":        {clauses: 2, alternatives: 1, kind: fuzzy.TermFuzzy, pattern: "api"},
		"api|web":        {clauses: 1, alternatives: 2, kind: fuzzy.TermFuzzy, pattern: "api"},
		"'api":           {clauses: 1, alternatives: 1, kind: fuzzy.TermExact, pattern: "api"},
		`"api gw"`:       {clauses: 1, alternatives: 1, kind: fuzzy.TermExact, pattern: "api gw"},
		"^back":          {clauses: 1, alternatives: 1, kind: fuzzy.TermPrefix, pattern: "back"},
		"end$":           {clauses: 1, alternatives: 1, kind: fuzzy.TermSuffix, pattern: "end"},
		"^shep$":         {clauses: 1, alternatives: 1, kind: fuzzy.TermExact, exactFull: true, pattern: "shep"},
		"/v[0-9]+/":      {clauses: 1, alternatives: 1, kind: fuzzy.TermRegex, pattern: "v[0-9]+"},
		"!test":          {clauses: 1, alternatives: 1, kind: fuzzy.TermFuzzy, inverse: true, pattern: "test"},
		"!^tmp":          {clauses: 1, alternatives: 1, kind: fuzzy.TermPrefix, inverse: true, pattern: "tmp"},
		"status:working": {clauses: 1, alternatives: 1, kind: fuzzy.TermField, field: "status"},
		"agent:claude":   {clauses: 1, alternatives: 1, kind: fuzzy.TermField, field: "agent"},
		"source:zoxide":  {clauses: 1, alternatives: 1, kind: fuzzy.TermField, field: "source"},
		"path:allsafe":   {clauses: 1, alternatives: 1, kind: fuzzy.TermField, field: "path"},
		"s:blocked":      {clauses: 1, alternatives: 1, kind: fuzzy.TermField, field: "status"},
	}
	for _, b := range searchSyntax.bindings {
		w, ok := expect[b.chord]
		if !ok {
			t.Errorf("documented example %q has no parse expectation: add one", b.chord)
			continue
		}
		q := fuzzy.ParseExtendedQuery(b.chord)
		if len(q.Clauses) != w.clauses || len(q.Clauses[0].Alternatives) != w.alternatives {
			t.Errorf("%q: %d clauses / %d alternatives, want %d / %d", b.chord, len(q.Clauses), len(q.Clauses[0].Alternatives), w.clauses, w.alternatives)
			continue
		}
		term := q.Clauses[0].Alternatives[0]
		if term.Kind != w.kind || term.Inverse != w.inverse || term.ExactFull != w.exactFull || term.Field != w.field || (w.pattern != "" && term.Pattern != w.pattern) {
			t.Errorf("%q parsed as %+v, want %+v", b.chord, term, w)
		}
	}
	// The other short forms the help names parse to their fields too, and
	// the 'phrase' alternative to the double quotes is an exact phrase.
	for raw, field := range map[string]string{"a:x": "agent", "src:x": "source", "p:x": "path"} {
		if term := fuzzy.ParseExtendedQuery(raw).Terms[0]; term.Kind != fuzzy.TermField || term.Field != field {
			t.Errorf("%q parsed as %+v, want the %s field", raw, term, field)
		}
	}
	if term := fuzzy.ParseExtendedQuery("'api gw'").Terms[0]; term.Kind != fuzzy.TermExact || term.Pattern != "api gw" {
		t.Errorf("'api gw' parsed as %+v, want an exact phrase", term)
	}
}

// === SPEC-NAV-1: descriptor-driven footer/help Enter copy ===

// TestFooterHints_TabRowShowsFocusTabLabel proves SPEC-NAV-1.5: when a
// synthesized tab row is highlighted, the footer's Enter hint reads
// "focus tab" (matching driver.FocusTab), not the generic "open" label.
func TestFooterHints_TabRowShowsFocusTabLabel(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	m.rows = []Row{{Kind: RowTab, Action: RowActionFocusTab, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"tab_id": "t1"}}}}
	m.cursor = 0
	hints := m.footerHints()
	if !hasHint(hints, keyChordEnter, footerLabelFocusTab) {
		t.Errorf("footerHints() = %+v, want the tab-row Enter hint \"enter focus tab\"", hints)
	}
	if hasHint(hints, keyChordEnter, footerLabelOpen) {
		t.Errorf("footerHints() = %+v, must not show the generic \"open\" label for a tab row", hints)
	}
}

// TestFooterHints_CandidateRowKeepsOpenLabel proves SPEC-NAV-1.1/1.3: a
// top-level candidate row keeps the truthful generic "open" Enter label and
// never promises create/focus-existing.
func TestFooterHints_CandidateRowKeepsOpenLabel(t *testing.T) {
	t.Parallel()
	m := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	if !hasHint(m.footerHints(), keyChordEnter, footerLabelOpen) {
		t.Errorf("footerHints() = %+v, want the candidate-row \"open\" Enter hint", m.footerHints())
	}
	if got := footerText(m); strings.Contains(strings.ToLower(got), "create") {
		t.Errorf("footer = %q, must not promise \"create\"", got)
	}
}

// TestFooterHints_NoHighlightInertEnter proves SPEC-NAV-1.7: with no row
// highlighted (empty rows), the footer has no Enter hint at all — it must
// not describe opening/focusing anything — and no target hints appear.
func TestFooterHints_NoHighlightInertEnter(t *testing.T) {
	t.Parallel()
	m := NewModel(nil, nil)
	m, _ = update(t, m, sizeMsg(120, 36))
	if _, ok := m.currentRow(); ok {
		t.Fatalf("setup: expected no highlighted row with a nil candidate set")
	}
	hints := m.footerHints()
	for _, chord := range []string{keyChordCtrlT, keyChordCtrlP, keyChordEnter} {
		if hasHintKey(hints, chord) {
			t.Errorf("footerHints() = %+v, must not offer %s with no highlight", hints, chord)
		}
	}
}

// TestFooterHints_TargetHintsOnlyWhenEligibleAndPane proves SPEC-NAV-1.4/4.2:
// ctrl+t/ctrl+p target hints appear only when a current pane exists AND the
// highlighted candidate supports a current-workspace target (eligible), and
// are suppressed for an ineligible row (session) even with a pane present.
func TestFooterHints_TargetHintsOnlyWhenEligibleAndPane(t *testing.T) {
	t.Parallel()
	pane := source.Pane{ID: "p0"}

	// Eligible zoxide candidate + pane: hints present.
	eligible := NewModel([]source.Candidate{zoxideCandidate("a", "/a")}, nil)
	eligible, _ = update(t, eligible, sizeMsg(120, 36))
	eligible = eligible.WithCurrentPane(&pane)
	if hints := eligible.footerHints(); !hasHintKey(hints, keyChordCtrlT) || !hasHintKey(hints, keyChordCtrlP) {
		t.Errorf("eligible+pane footerHints() = %+v, want ctrl+t and ctrl+p hints", hints)
	}

	// Ineligible session candidate + pane: hints suppressed.
	sess := source.Candidate{Label: "alpha", Source: config.SourceSessions, Meta: map[string]string{"session_name": "alpha"}}
	ineligible := NewModel([]source.Candidate{sess}, nil)
	ineligible, _ = update(t, ineligible, sizeMsg(120, 36))
	ineligible = ineligible.WithCurrentPane(&pane)
	if hints := ineligible.footerHints(); hasHintKey(hints, keyChordCtrlT) || hasHintKey(hints, keyChordCtrlP) {
		t.Errorf("ineligible session row footerHints() = %+v, must not show target hints", hints)
	}
}
