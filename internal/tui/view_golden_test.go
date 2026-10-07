// Package tui test: golden render harness for the shep TUI redesign.
//
// EVIDENCE, NOT TARGETS. The fixtures under testdata/view/before/ are
// approval-test snapshots of the CURRENT (pre-redesign) candidate output for
// each named scenario, theme, and terminal dimension. They are evidence of
// what the picker rendered before the redesign — including any current
// shortcomings (e.g. OSC pass-through, wide-render at a size the redesign
// intends to be list-only) — NOT approved design targets. The before/
// directory is IMMUTABLE: never modify or delete anything in it.
//
// TARGETS. As each redesign phase changes a scenario's intended output, the
// NEW approved output is written as a TARGET fixture under
// testdata/view/target/. The harness compares a scenario against its target
// fixture when one exists, else falls back to the before/ evidence fixture —
// so a passing golden test proves "the render matches the approved target
// (or, pending one, did not change from the evidence)".
//
// Updating fixtures is per-scenario and opt-in. To (re)generate one
// scenario's TARGET fixture, run ONLY that scenario with -update-golden,
// e.g.:
//
//	go test -run '^TestViewGolden/preview_loading$' ./internal/tui/ -update-golden
//
// -update-golden ALWAYS writes to target/ (never before/). Never run
// -update-golden without a -run filter that scopes it to the intended
// scenario(s); the default (no -update-golden) is compare-only and never
// writes.
//
// Two Go-test gotchas worth knowing: (1) the flag is named -update-golden
// (not -update) because the transitive teatest ->
// github.com/charmbracelet/x/exp/golden dependency already registers a
// package-level -update flag at init, so -update would panic with "flag
// redefined" at test binary startup; (2) -update-golden is a test-binary
// flag, so it MUST come AFTER the package selector — placing it before the
// package makes 'go test' try to build "." and fail with "no Go files in .".
package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// updateGolden, when set via -update-golden, regenerates the target/ fixture
// for each scenario selected by -run (before/ stays immutable). Compare-only
// by default.
var updateGolden = flag.Bool("update-golden", false, "regenerate the target/ golden fixtures for the scenarios selected by -run")

// beforeDir is the root for the immutable pre-redesign EVIDENCE fixtures.
const beforeDir = "testdata/view/before"

// targetDir is the root for the approved redesign TARGET fixtures. A scenario
// compares against its target fixture when one exists, else falls back to
// before/. -update-golden writes here (never before/).
const targetDir = "testdata/view/target"

// --- normalization helpers (unit-tested below; defined later in this file) ---

// ANSI escape classifiers used by the fixture normalization pipeline.
// Compiled once at package load; all are read-only and safe to share across
// tests. Non-SGR ANSI stripping (OSC, DCS, APC, PM, SOS, and every non-SGR
// CSI final byte) is NOT duplicated here at all — the production
// sanitizePaneCapture scanner (sanitize.go) is now the single, provably
// closure-safe containment boundary and fully covers that scope (fix round
// 1, R1-001/R1-002), including fail-closed handling of any ESC-introduced
// sequence it cannot classify. Only what remains genuinely test-only
// (SGR-dedup for fixture noise, spinner-frame freezing) has its own regex
// here.
var (
	// reSGR matches a single SGR sequence (\x1b[...m). Used by collapseDupSGR;
	// Go's RE2 regexp has no backreferences, so consecutive-identical collapse
	// is done with a manual scan rather than a (\1)+ pattern.
	reSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// reSpinner matches any single braille spinner frame (spinner.MiniDot's
	// full frame set) so a loading indicator's animation frame can never
	// make a golden fixture flake.
	reSpinner = regexp.MustCompile(`[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]`)
)

// stripNonSGRANSI removes every ANSI escape sequence that is not an SGR
// (\x1b[...m) by delegating entirely to the production sanitizePaneCapture
// scanner — the same security boundary the TUI applies to real pane
// captures, so the golden-fixture normalization and the production
// containment can never drift apart. Kept as a named step in the
// normalization pipeline (see normalizeView) for readability/discoverability
// rather than because it adds behavior of its own.
func stripNonSGRANSI(s string) string {
	return sanitizePaneCapture(s)
}

// normalizeSpinnerFrames replaces any braille spinner frame rune with the
// stable placeholder ⠿ so a loading indicator's current animation frame can
// never differ between two runs of the same scenario.
func normalizeSpinnerFrames(s string) string {
	return reSpinner.ReplaceAllString(s, "⠿")
}

// collapseDupSGR collapses runs of byte-identical, immediately adjacent SGR
// sequences to a single occurrence, taming lipgloss's tendency to emit the
// same reset/style twice in a row without changing what the eye sees. Go's
// RE2 regexp has no backreferences, so this walks the SGR matches by hand:
// an SGR is dropped only when the previously emitted token was the exact same
// SGR with no text in between.
func collapseDupSGR(s string) string {
	matches := reSGR.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	cursor := 0
	prevSGR := ""
	prevWasSGR := false
	for _, idx := range matches {
		start, end := idx[0], idx[1]
		if start > cursor {
			b.WriteString(s[cursor:start])
			prevWasSGR = false
		}
		seq := s[start:end]
		if prevWasSGR && seq == prevSGR {
			// Immediately adjacent duplicate: drop it.
		} else {
			b.WriteString(seq)
			prevSGR = seq
			prevWasSGR = true
		}
		cursor = end
	}
	if cursor < len(s) {
		b.WriteString(s[cursor:])
	}
	return b.String()
}

// trimTrailingWSPerLine removes trailing spaces, tabs, and CR from every
// line (split on \n), preserving leading indentation. Lipgloss width-padded
// lines otherwise carry a non-meaningful, width-dependent number of trailing
// spaces that would make fixtures noisier than the evidence needs to be.
func trimTrailingWSPerLine(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t\r")
	}
	return strings.Join(lines, "\n")
}

// normalizeView is the full golden-fixture pipeline: strip non-SGR ANSI,
// freeze the spinner frame, collapse duplicate SGR, trim per-line trailing
// whitespace, and drop the final trailing newline. Applied to View() output
// before comparison and before writing a fixture.
func normalizeView(s string) string {
	s = stripNonSGRANSI(s)
	s = normalizeSpinnerFrames(s)
	s = collapseDupSGR(s)
	s = trimTrailingWSPerLine(s)
	return strings.TrimRight(s, "\n")
}

// TestNormalizeStripNonSGR proves non-SGR ANSI (OSC, DCS, cursor/other CSI)
// is stripped while SGR (\x1b[...m) is preserved, via the production
// sanitizePaneCapture scanner stripNonSGRANSI now delegates to entirely.
//
// The last two cases (a lone ESC control, and a charset designation) changed
// expected values in Phase 5 fix round 1 (R1-002): the production scanner
// fails closed on any ESC-introduced sequence it cannot classify as
// CSI/OSC/DCS/APC/PM/SOS, dropping the REST of the input rather than just
// that one unrecognized sequence — an intentionally more conservative
// posture than the old test-only regex, which only stripped the specific
// lone-control bytes it recognized and left the rest of the string alone.
func TestNormalizeStripNonSGR(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"OSC ended by BEL removed", "before\x1b]0;title\x07after", "beforeafter"},
		{"OSC ended by ST removed", "x\x1b]2;win\x1b\\y", "xy"},
		{"DCS removed", "x\x1bPfoo\x1b\\y", "xy"},
		{"cursor CSI removed", "x\x1b[2Jy\x1b[H z", "xy z"},
		{"private-mode CSI removed", "\x1b[?25hvisible", "visible"},
		{"SGR kept", "x\x1b[31mred\x1b[0my", "x\x1b[31mred\x1b[0my"},
		// Unrecognized ESC introducer ('7' is not [, ], P, _, ^, or X):
		// fails closed, dropping everything from that ESC to end-of-input —
		// "save\x1b8" is dropped along with the ESC 7 itself.
		{"lone ESC control fails closed (drops rest of input)", "\x1b7save\x1b8", ""},
		// Same fail-closed rule: '(' is not a recognized introducer, so
		// everything from that ESC onward ("\x1b(Bb") is dropped; only the
		// leading "a" (already copied through before the ESC) survives.
		{"charset designation fails closed (drops rest of input)", "a\x1b(Bb", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripNonSGRANSI(tt.in); got != tt.want {
				t.Errorf("stripNonSGRANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeSpinnerFrames proves every braille spinner frame rune collapses
// to the stable placeholder ⠿ while non-spinner text is untouched.
func TestNormalizeSpinnerFrames(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"single frame", "⠋ loading…", "⠿ loading…"},
		{"all frames", "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", "⠿⠿⠿⠿⠿⠿⠿⠿⠿⠿"},
		{"no frames", "no spinner here", "no spinner here"},
		{"frame next to SGR kept", "⠋\x1b[31m⠙", "⠿\x1b[31m⠿"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeSpinnerFrames(tt.in); got != tt.want {
				t.Errorf("normalizeSpinnerFrames(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCollapseDupSGR proves runs of byte-identical consecutive SGR sequences
// collapse to one, while distinct SGR sequences are left intact.
func TestCollapseDupSGR(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"two resets to one", "\x1b[0m\x1b[0m", "\x1b[0m"},
		{"three identical", "\x1b[31m\x1b[31m\x1b[31m", "\x1b[31m"},
		{"around text", "a\x1b[31m\x1b[31mb", "a\x1b[31mb"},
		{"distinct SGR untouched", "\x1b[31m\x1b[0m", "\x1b[31m\x1b[0m"},
		{"no SGR", "plain", "plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := collapseDupSGR(tt.in); got != tt.want {
				t.Errorf("collapseDupSGR(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestTrimTrailingWSPerLine proves trailing spaces/tabs/CR are removed per
// line while leading indentation is preserved.
func TestTrimTrailingWSPerLine(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"trailing spaces", "a  \nb\t\nc", "a\nb\nc"},
		{"no trailing", "no trail", "no trail"},
		{"leading kept", "  leading kept  ", "  leading kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimTrailingWSPerLine(tt.in); got != tt.want {
				t.Errorf("trimTrailingWSPerLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeView proves the full pipeline composes: non-SGR stripped,
// spinner normalized, duplicate SGR collapsed, per-line trailing ws trimmed,
// and the final trailing newline removed.
func TestNormalizeView(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			"osc + spinner + trailing newline",
			"⠋ loading\x1b]0;x\x07\n",
			"⠿ loading",
		},
		{
			"dup sgr + trailing spaces + newline",
			"\x1b[0m\x1b[0mhi  \n",
			"\x1b[0mhi",
		},
		{
			"multiline preserves internal newlines",
			"a  \nb\n\n",
			"a\nb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeView(tt.in); got != tt.want {
				t.Errorf("normalizeView(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// sizeMsg is the fixed tea.WindowSizeMsg every scenario sends to settle the
// model's responsive mode before View().
func sizeMsg(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}

// goldenPresentation is the scenarios' row presentation: the defaults, with
// every icon drawn from the candidate's own Icon (the worktree glyph kept).
// The scenarios were written when the picker drew the icon stamped on each
// candidate, so their candidates carry exactly the icons the fixtures pin;
// the default presentations' own icons are pinned by
// TestDefaultPresentations_ReproduceTheRowTexts.
func goldenPresentation() *config.Presentations {
	p := config.DefaultPresentations("")
	for _, rp := range []*config.RowPresentation{&p.Herdr, &p.Sessions, &p.Workspaces, &p.Zoxide, &p.Agents} {
		rp.Icon = "{{ .Icon }}"
	}
	p.Projects.Icon = "{{ if .IsWorktree }}\ue725 {{ else }}{{ .Icon }}{{ end }}"
	return &p
}

// goldenCandidates is the deterministic candidate set spanning every group
// (Herdr workspaces, discovered projects, zoxide dirs, configured
// [[workspaces]] entries) used by the all-groups / plain / narrow / short
// scenarios. Fixed labels and paths so the fixture never depends on the
// real Herdr daemon, the real filesystem, or zoxide's frecency.
func goldenCandidates() []source.Candidate {
	return []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
		herdrCandidate("frontend", "/srv/frontend", "w2"),
		projectCandidate("shep", "/home/dev/shep"),
		projectCandidate("dotfiles", "/home/dev/dotfiles"),
		zoxideCandidate("tmp", "/tmp"),
		zoxideCandidate("Downloads", "/home/dev/Downloads"),
		workspaceEntryCandidate("notes", "/home/dev/notes"),
	}
}

// accessoryCandidates is the rows_accessories_scrolled candidate set: two
// open workspaces whose panes report agent states, a pinned group
// workspace, a running default session, a worktree, a missing project and
// enough zoxide directories under /home/dev to overflow the list.
func accessoryCandidates() ([]source.Candidate, *TreeExpander, ranking.Snapshot) {
	const (
		herdrIcon     = "\U000f0cc6 "
		workspaceIcon = "\ue615 "
		zoxideIcon    = "\uf114 "
		projectIcon   = "\ue702 "
		sessionIcon   = "\uf120 "
	)
	api := herdrCandidate("api", "/home/dev/allsafe/api", "w1")
	billing := herdrCandidate("billing", "/home/dev/allsafe/billing", "w2")
	api.Icon, billing.Icon = herdrIcon, herdrIcon
	team := source.Candidate{Label: "team", Path: "/home/dev/teams/platform", Icon: workspaceIcon, Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	cands := []source.Candidate{
		api, billing, team,
		{Label: "main", Icon: sessionIcon, Source: config.SourceSessions, Meta: map[string]string{"session_name": "main", "running": "true", "default": "true"}},
		{Label: "~/Proyectos/shep", Path: "/home/dev/Proyectos/shep", Icon: zoxideIcon, Source: config.SourceZoxide},
		{Label: "~/allsafe/ECORP/platforms/whiterose-db", Path: "/home/dev/allsafe/ECORP/platforms/whiterose-db", Icon: zoxideIcon, Source: config.SourceZoxide},
		{Label: "api-fix", Path: "/home/dev/trees/api-fix", Source: config.SourceProjects, Meta: map[string]string{"is_worktree": "true", "branch": "fix/parser"}},
		{Label: "~/old/archive", Path: "/home/dev/old/archive", Icon: projectIcon, Source: config.SourceProjects, Missing: true},
	}
	for i := range 12 {
		name := fmt.Sprintf("~/src/repo-%02d", i)
		cands = append(cands, source.Candidate{Label: name, Path: "/home/dev" + name[1:], Icon: zoxideIcon, Source: config.SourceZoxide})
	}
	tree := NewTreeExpanderFromSnapshot(source.Snapshot{
		Workspaces: []source.Workspace{{ID: "w1"}, {ID: "w2"}},
		Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Number: 1}, {ID: "t2", WorkspaceID: "w2", Number: 1}},
		Panes: []source.Pane{
			{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "working"},
			{ID: "p2", WorkspaceID: "w2", TabID: "t2", Agent: "codex", AgentStatus: "blocked"},
		},
	})
	return cands, tree, ranking.Snapshot{}.WithPinned(ranking.PinKey(team), true)
}

// paneCaptureBuffer is the deterministic multi-line Herdr pane capture used
// by pane_capture_present: printable box-drawing characters, one SGR color
// sequence, one over-width long line (exercises wrap/overflow), and one
// unsafe OSC sequence (\x1b]0;evil title\x07) so the fixture and the
// evidence assertion together prove the Phase 5 containment boundary strips
// OSC/DCS while leaving everything else (including the SGR sequence) intact.
func paneCaptureBuffer() string {
	return strings.Join([]string{
		"┌────────┐",
		"│ pane 1 │",
		"└────────┘",
		"\x1b[31mred error line\x1b[0m",
		"this is a very long line that is far wider than the preview pane content width so it wraps or overflows under the current render behavior",
		"\x1b]0;evil title\x07",
	}, "\n")
}

// cursorOnPane reports whether the cursor currently sits on a synthesized
// RowPane, used by pane_capture_present to walk the cursor into position.
func cursorOnPane(m Model) bool {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return false
	}
	return m.rows[m.cursor].Kind == RowPane
}

func cursorOnTab(m Model) bool {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return false
	}
	return m.rows[m.cursor].Kind == RowTab
}

// goldenScenario describes one golden render: a named, deterministic model
// state at a fixed terminal dimension and theme, plus the fixture path to
// compare its normalized View() against.
type goldenScenario struct {
	name   string
	width  int
	height int
	theme  string // the theme the scenario's Layout uses; also the fixture filename suffix
	// setup builds the model and drives it through its WindowSizeMsg and any
	// state-advancing messages, returning the model ready to View().
	setup func(t *testing.T) Model
	// evidence, when non-nil, is called with the raw (pre-normalization)
	// View() output to assert a current-behavior observation the normalized
	// fixture cannot itself carry (e.g. OSC pass-through). It is an approval
	// assertion of TODAY's behavior: when the redesign changes that behavior,
	// it should fail RED and be updated alongside the fixture.
	evidence func(t *testing.T, raw string)
}

// goldenScenarios is the fixed set of pre-redesign EVIDENCE scenarios. Each
// is driven purely through the existing constructors and Update/View, with
// no production changes. Dimensions and names are taken verbatim from the
// approved Phase 1 plan.
func goldenScenarios() []goldenScenario {
	return []goldenScenario{
		{
			name: "empty_query_all_groups", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				return m
			},
		},
		{
			name: "mid_query_descendant_match", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				driver := &fakeTreeDriver{
					tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}, {ID: "t2", WorkspaceID: "w2", Label: "deploy"}},
				}
				tree := treeFromFake(driver)
				m := NewModelWithTree(
					[]source.Candidate{herdrCandidate("backend", "/srv/backend", "w1"), herdrCandidate("api-gateway", "/srv/gateway", "w2")},
					nil, tree, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()},
				)
				m, _ = update(t, m, sizeMsg(120, 36))
				// "api" matches the synthesized tab but not the workspace
				// itself -> the workspace is shown as a descendant-only match
				// opened along that branch; "api-gateway" matches by itself
				// and stays collapsed.
				for _, r := range "api" {
					m, _ = update(t, m, key(string(r)))
				}
				return m
			},
		},
		{
			name: "mid_query_dir_match", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				cands := []source.Candidate{
					herdrCandidate("backend", "/srv/backend", "w1"),
					zoxideCandidate("alpha-dir", "/home/dev/alpha-dir"),
					zoxideCandidate("beta-dir", "/home/dev/beta-dir"),
				}
				m := NewModelWithLayout(cands, nil, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				// "alpha" matches the zoxide candidate but not the herdr
				// workspace, which drops out of the filtered list entirely
				// (no group headers to auto-expand — that mechanism was
				// removed in the corrective round).
				for _, r := range "alpha" {
					m, _ = update(t, m, key(string(r)))
				}
				return m
			},
		},
		{
			name: "preview_loading", width: 88, height: 30, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(
					[]source.Candidate{zoxideCandidate("alpha", "/a"), zoxideCandidate("beta", "/b")},
					blockingRenderer{}, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()},
				)
				m, _ = update(t, m, sizeMsg(88, 30))
				// Deliberately do NOT deliver any previewResponseMsg: the
				// pane must stay in the loading state (spinner + "loading…").
				return m
			},
		},
		{
			name: "preview_error", width: 88, height: 30, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(
					[]source.Candidate{zoxideCandidate("alpha", "/a"), zoxideCandidate("beta", "/b")},
					errRenderer{}, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()},
				)
				m, _ = update(t, m, sizeMsg(88, 30))
				// Init dispatches the first render at seq 0; execute that Cmd
				// and deliver its (erroring) response so the preview shows
				// the error indicator. previewSeq is still 0 (no selection
				// change has bumped it), so the response matches.
				initCmd := m.Init()
				if initCmd == nil {
					t.Fatal("expected Init to dispatch an async preview render with a renderer wired")
				}
				m, _ = update(t, m, initCmd())
				return m
			},
		},
		{
			name: "narrow_list_only", width: 64, height: 24, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(64, 24))
				return m
			},
		},
		{
			name: "short_list_only", width: 100, height: 10, theme: ThemeMocha,
			// NOTE: at 100x10 the CURRENT code resolves modeWide (width >=
			// wideBreakpoint=100 and height 10 >= minPreviewHeight=8), NOT
			// list-only. The fixture is therefore EVIDENCE that the current
			// breakpoints do not force list-only at this size — a finding for
			// the redesign, not a test failure. See the Phase 1 report.
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(100, 10))
				return m
			},
		},
		{
			name: "pane_capture_present", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				driver := &fakeTreeDriver{
					tabs:     []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
					panes:    []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
					readText: paneCaptureBuffer(),
				}
				tree := treeFromFake(driver)
				m := NewModelWithTree(
					[]source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")},
					nil, tree, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()},
				)
				m, _ = update(t, m, sizeMsg(120, 36))
				// Manually expand the workspace (progressive disclosure at an
				// empty query) so the synthesized tab+pane rows appear.
				m.expandedWorkspaces["w1"] = true
				m.applyFilter()
				// Walk the cursor down onto the synthesized pane row. Each
				// Down bumps previewSeq; stop once the cursor is on a pane.
				for i := 0; i < 16 && !cursorOnPane(m); i++ {
					m, _ = update(t, m, key("down"))
				}
				if !cursorOnPane(m) {
					t.Fatalf("setup: cursor never reached a RowPane, rows=%d cursor=%d", len(m.rows), m.cursor)
				}
				// Deliver the captured pane buffer tagged with the model's
				// current seq (bumped by the Down onto the pane row).
				m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: paneCaptureBuffer()})
				return m
			},
			// Phase 5: the preview path now sanitizes OSC at the point the
			// raw capture is composed into the preview body, so the raw
			// View() output must never carry the OSC sequence through to the
			// terminal.
			evidence: func(t *testing.T, raw string) {
				if strings.Contains(raw, "\x1b]0;evil") {
					t.Errorf("OSC containment: raw View() output must not pass the OSC sequence through, but it was present: %q", raw)
				}
			},
		},
		{
			name: "tab_active_pane", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				driver := &fakeTreeDriver{
					tabs: []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
					panes: []source.Pane{
						{ID: "p1", WorkspaceID: "w1", TabID: "t1", Focused: true},
					},
					readText: "tab active capture",
				}
				snapshot := source.Snapshot{Workspaces: []source.Workspace{{ID: "w1"}}, Tabs: driver.tabs, Panes: driver.panes}
				m := NewModelWithTree(
					[]source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")},
					nil, treeFromFake(driver), Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()},
				).WithSnapshotRefresh(driver, snapshot, nil)
				m, _ = update(t, m, sizeMsg(120, 36))
				m.expandedWorkspaces["w1"] = true
				m.applyFilter()
				m, cmd := update(t, m, key("down"))
				if !cursorOnTab(m) {
					t.Fatalf("setup: cursor never reached a RowTab, rows=%d cursor=%d", len(m.rows), m.cursor)
				}
				if cmd == nil {
					t.Fatal("expected RowTab selection to dispatch an active-pane preview")
				}
				// syncPreviewAfterSelectionChange returns a Batch containing the
				// tab request and spinner tick. Drive the tab request directly so
				// this deterministic fixture receives its panePreviewMsg rather
				// than the BatchMsg wrapper managed by Bubble Tea's runtime.
				m, _ = update(t, m, m.tabPreviewCmd(m.previewSeq, m.rows[m.cursor].Candidate)())
				return m
			},
		},
		{
			name: "plain_no_color", width: 120, height: 36, theme: ThemePlain,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemePlain), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				return m
			},
		},
		// --- Phase 4 scenarios ---
		{
			// workspace_active_pane: Herdr workspace with a renderer that
			// emits identity + workspace + agent_status + active_pane. Phase 4
			// recomposes: compact identity (no source) → inline agent status →
			// workspace summary → Active pane + capture LAST.
			name: "workspace_active_pane", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				renderer := phase4WorkspaceRenderer{}
				cands := []source.Candidate{
					herdrCandidate("backend", "/srv/backend", "w1"),
				}
				cands[0].Meta["active_tab_id"] = "t1"
				tree := NewTreeExpanderFromSnapshot(source.Snapshot{
					Workspaces: []source.Workspace{{ID: "w1", Label: "backend"}},
					Tabs: []source.Tab{
						{ID: "t1", WorkspaceID: "w1", Label: "api", Number: 1, Focused: true},
						{ID: "t2", WorkspaceID: "w1", Label: "tests", Number: 2},
					},
					Panes: []source.Pane{
						{ID: "p1", WorkspaceID: "w1", TabID: "t1", Agent: "claude", AgentStatus: "working"},
						{ID: "p2", WorkspaceID: "w1", TabID: "t1"},
						{ID: "p3", WorkspaceID: "w1", TabID: "t2", Agent: "codex", AgentStatus: "idle"},
					},
				})
				m := NewModelWithTree(cands, renderer, tree, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				// Init dispatches the first render at seq 0; execute the Cmd
				// and deliver its response.
				initCmd := m.Init()
				if initCmd != nil {
					m, _ = update(t, m, initCmd())
				}
				return m
			},
		},
		{
			// pane_capture_unavailable: a pane row with no capture delivered.
			// The preview omits the "Pane" section; location and meta remain.
			name: "pane_capture_unavailable", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				driver := &fakeTreeDriver{
					tabs:  []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "api"}},
					panes: []source.Pane{{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/srv/api"}},
				}
				tree := treeFromFake(driver)
				m := NewModelWithTree(
					[]source.Candidate{herdrCandidate("backend", "/srv/backend", "w1")},
					nil, tree, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()},
				)
				m, _ = update(t, m, sizeMsg(120, 36))
				m.expandedWorkspaces["w1"] = true
				m.applyFilter()
				for i := 0; i < 16 && !cursorOnPane(m); i++ {
					m, _ = update(t, m, key("down"))
				}
				if !cursorOnPane(m) {
					t.Fatalf("setup: cursor never reached a RowPane")
				}
				// Deliver an empty (no text, no error) panePreviewMsg so the
				// capture resolves to nothing — the pane shows identity only
				// (no Pane section). Going through Update ensures
				// syncViewport refreshes the viewport content.
				m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: ""})
				return m
			},
		},
		{
			// project_preview: a SourceProjects candidate with a renderer
			// that emits identity + git + dir listing. Phase 4 recomposes:
			// location → git meta line → Files section.
			name: "project_preview", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				renderer := phase4ProjectRenderer{}
				cands := []source.Candidate{
					projectCandidate("shep", "/home/dev/shep"),
				}
				m := NewModelWithLayout(cands, renderer, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				initCmd := m.Init()
				if initCmd != nil {
					m, _ = update(t, m, initCmd())
				}
				return m
			},
		},
		{
			// zoxide_preview: a SourceZoxide candidate with a renderer that
			// emits identity + dir listing. Phase 4 recomposes: identity →
			// Files section; full path visible.
			name: "zoxide_preview", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				renderer := phase4ZoxideRenderer{}
				cands := []source.Candidate{
					zoxideCandidate("tmp", "/tmp"),
				}
				m := NewModelWithLayout(cands, renderer, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				initCmd := m.Init()
				if initCmd != nil {
					m, _ = update(t, m, initCmd())
				}
				return m
			},
		},
		// --- Phase 6 scenarios: the help overlay ---
		{
			// help_from_list: "?" opened from the list. Proves the help
			// overlay itself renders correctly; opening and closing it is
			// covered by keys_test.go, not by this visual fixture.
			name: "help_from_list", width: 120, height: 36, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(120, 36))
				m, _ = update(t, m, key("?"))
				return m
			},
		},
		{
			// rows_accessories_scrolled: every row presentation at once —
			// filename-first paths under a fake home, per-source icons, the
			// aggregate agent status of open workspaces, pin/group/worktree/
			// session/missing accessories — in a list long enough to scroll,
			// with the cursor moved past the scroll-off margin so the window
			// and the divider's scroll thumb have moved.
			name: "rows_accessories_scrolled", width: 100, height: 16, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				cands, tree, pins := accessoryCandidates()
				m := NewModelWithTree(cands, nil, tree, Layout{
					Theme: testTheme(ThemeMocha), Presentation: goldenPresentation(), HomeDir: "/home/dev", RankingSnapshot: pins,
					SourceOrder: []string{config.SourceHerdr, config.SourceSessions, config.SourceWorkspaces, config.SourceProjects, config.SourceZoxide},
				})
				m, _ = update(t, m, sizeMsg(100, 16))
				for range 9 {
					m, _ = update(t, m, key("down"))
				}
				return m
			},
		},
		{
			// agents_view_pane: the agents tab with agents in several states;
			// the highlighted agent's preview shows its placement, its status
			// and the newest lines of its pane capture.
			name: "agents_view_pane", width: 120, height: 24, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				snapshot := source.Snapshot{
					Workspaces: []source.Workspace{{ID: "w1", Label: "whiterose-db"}, {ID: "w2", Label: "billing"}},
					Tabs:       []source.Tab{{ID: "t1", WorkspaceID: "w1", Label: "editor", Number: 1}, {ID: "t2", WorkspaceID: "w2", Label: "api", Number: 1}},
					Panes: []source.Pane{
						{ID: "p1", WorkspaceID: "w1", TabID: "t1", CWD: "/home/dev/allsafe/whiterose-db", Agent: "claude", AgentStatus: "blocked", TerminalTitle: "Review the contract parser before release"},
						{ID: "p2", WorkspaceID: "w2", TabID: "t2", CWD: "/home/dev/allsafe/billing", Agent: "codex", AgentStatus: "working", TerminalTitle: "Fix invoice rounding"},
						{ID: "p3", WorkspaceID: "w2", TabID: "t2", CWD: "/home/dev/allsafe/billing", Agent: "opencode", AgentStatus: "idle", TerminalTitle: "opencode"},
					},
				}
				driver := &fakeTreeDriver{tabs: snapshot.Tabs, panes: snapshot.Panes}
				m := NewModelWithTree(nil, nil, NewTreeExpanderFromSnapshot(snapshot), Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation(), InitialTab: "agents", HomeDir: "/home/dev"}).
					WithSnapshotRefresh(driver, snapshot, nil)
				m.startupSnapshot = &snapshot // as the streaming producer delivers it
				m.applyFilter()
				m, _ = update(t, m, sizeMsg(120, 24))
				var capture []string
				for i := range 30 {
					capture = append(capture, fmt.Sprintf("step %02d: parsed clause %d", i, i*3))
				}
				m, _ = update(t, m, panePreviewMsg{seq: m.previewSeq, text: strings.Join(capture, "\n") + "\n\n"})
				return m
			},
		},
		{
			// help_scrolled_short: help opened at the existing short
			// dimension (100x10 — same as short_list_only), then scrolled
			// down enough to reveal content that was clipped at the top of
			// a viewport this small. Proves helpViewport actually scrolls
			// instead of silently truncating the bottom of the help text.
			name: "help_scrolled_short", width: 100, height: 10, theme: ThemeMocha,
			setup: func(t *testing.T) Model {
				m := NewModelWithLayout(goldenCandidates(), nil, Layout{Theme: testTheme(ThemeMocha), Presentation: goldenPresentation()})
				m, _ = update(t, m, sizeMsg(100, 10))
				m, _ = update(t, m, key("?"))
				for i := 0; i < 30; i++ {
					m, _ = update(t, m, key("down"))
				}
				return m
			},
		},
	}
}

// beforeFixturePath is the immutable before/ evidence fixture path for sc:
// testdata/view/before/<scenario>__<WxH>__<theme>.txt
func beforeFixturePath(sc goldenScenario) string {
	return filepath.Join(beforeDir, fmt.Sprintf("%s__%dx%d__%s.txt", sc.name, sc.width, sc.height, sc.theme))
}

// targetFixturePath is the approved target/ fixture path for sc:
// testdata/view/target/<scenario>__<WxH>__<theme>.txt
func targetFixturePath(sc goldenScenario) string {
	return filepath.Join(targetDir, fmt.Sprintf("%s__%dx%d__%s.txt", sc.name, sc.width, sc.height, sc.theme))
}

// writeFixture writes content to path (creating directories as needed). Used
// only by the -update-golden path.
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	t.Logf("updated golden fixture %s", path)
}

// TestViewGolden is the approval-test harness for the pre-redesign picker
// render. Each scenario builds a deterministic model, drives it through a
// fixed tea.WindowSizeMsg (and any state-advancing messages), renders
// View(), normalizes, and compares against its target fixture when one
// exists, else its immutable before/ evidence fixture.
//
// It is deliberately NOT t.Parallel, which keeps fixture writes (only under
// -update-golden) race-free.
func TestViewGolden(t *testing.T) {
	for _, sc := range goldenScenarios() {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			m := sc.setup(t)
			raw := m.View()
			if sc.evidence != nil {
				sc.evidence(t, raw)
			}
			got := normalizeView(raw)

			if *updateGolden {
				writeFixture(t, targetFixturePath(sc), got)
				return
			}

			// Prefer an approved target fixture; fall back to the immutable
			// before/ evidence fixture until a target is written for sc.
			path := targetFixturePath(sc)
			if _, err := os.Stat(path); err != nil {
				path = beforeFixturePath(sc)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					t.Fatalf("golden fixture missing: %s\n\nTo generate a target fixture, run:\n  go test -run '^TestViewGolden/%s$' ./internal/tui/ -update-golden",
						path, sc.name)
				}
				t.Fatalf("read fixture %s: %v", path, err)
			}
			if got != string(want) {
				if werr := os.WriteFile(path+".actual", []byte(got), 0o644); werr == nil {
					t.Logf("wrote actual output to %s for diffing", path+".actual")
				}
				t.Fatalf("golden mismatch for %s (fixture %s)\n--- want ---\n%s\n--- got ---\n%s",
					sc.name, path, want, got)
			}
		})
	}
}
