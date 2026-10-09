package tui

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/effective"
	"github.com/tranceh2/shep/internal/preview"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// Render-path benchmarks for the picker. They model a heavy session (30 open
// Herdr workspaces, 10 agents, ~650 candidates at 140x38) with synthetic,
// deterministic data so before/after numbers are comparable across commits.
// Run with:
//
//	go test -run '^$' -bench . -benchmem ./internal/tui/

// benchHome is the fake home directory every synthetic path lives under, so
// labels carry the "~/..." form real zoxide/projects candidates use.
const benchHome = "/Users/bench"

// benchWords seeds workspace and directory names; a few contain "shep" so
// the typed-query benchmark matches a realistic subset rather than nothing.
var benchWords = []string{
	"shep", "herdr", "api", "frontend", "infra", "docs", "notes",
	"dotfiles", "platform", "contracts", "shell-tools", "billing",
}

// benchWorkspaceRenderer returns a fixed, fully sectioned Herdr workspace
// preview so View renders a realistic preview body instead of the static
// fallback.
type benchWorkspaceRenderer struct{}

func (benchWorkspaceRenderer) Render(context.Context, source.Candidate) (preview.Result, error) {
	capture := make([]string, 0, 24)
	for i := range 24 {
		capture = append(capture, fmt.Sprintf("\x1b[32m%02d\x1b[0m build step %d finished in %dms", i, i, 10*i))
	}
	return preview.Result{Sections: []preview.Section{
		{Kind: config.PreviewIdentity, Text: "shep\npath: " + benchHome + "/Proyectos/shep\nsource: herdr"},
		{Kind: config.PreviewWorkspace, Text: "workspace\n  tab 1: editor * (2 panes)\n  tab 2: tests (1 panes)"},
		{Kind: config.PreviewAgentStatus, Text: "agent status\n  status: working"},
		{Kind: config.PreviewActivePane, Text: "active pane\n" + strings.Join(capture, "\n")},
	}}, nil
}

// heavyCapture is a realistic `herdr pane read --format ansi` capture of an
// editor or agent TUI: 200 lines of ~180 cells — wider than the preview
// column — with a truecolor SGR change every few cells, bold/italic toggles,
// box drawing and multi-byte glyphs.
func heavyCapture() string {
	rng := rand.New(rand.NewPCG(7, 8))
	glyphs := []string{"│", "─", "╭", "╯", "▌", "é", "ñ", "\U000f0cc6", "λ", "→"}
	var b strings.Builder
	for line := range 200 {
		if line > 0 {
			b.WriteByte('\n')
		}
		cells := 0
		for cells < 180 {
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm", rng.IntN(256), rng.IntN(256), rng.IntN(256), rng.IntN(64), rng.IntN(64), rng.IntN(64))
			if rng.IntN(4) == 0 {
				b.WriteString("\x1b[1;3m")
			} else {
				b.WriteString("\x1b[22;23m")
			}
			for range 3 + rng.IntN(3) {
				if rng.IntN(6) == 0 {
					b.WriteString(glyphs[rng.IntN(len(glyphs))])
				} else {
					b.WriteByte(byte('a' + rng.IntN(26)))
				}
				cells++
			}
		}
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// benchHeavyRenderer is benchWorkspaceRenderer with heavyCapture as the
// active pane.
type benchHeavyRenderer struct{ capture string }

func (r benchHeavyRenderer) Render(ctx context.Context, c source.Candidate) (preview.Result, error) {
	res, _ := benchWorkspaceRenderer{}.Render(ctx, c)
	res.Sections[len(res.Sections)-1].Text = "active pane\n" + r.capture
	return res, nil
}

// benchSnapshot builds n Herdr workspaces with 2-4 tabs and 1-2 panes per
// tab. Every third pane runs an agent whose status cycles through working,
// idle and blocked; the rest are plain shells.
func benchSnapshot(n int) source.Snapshot { return benchSnapshotUnder(n, benchHome) }

// benchSnapshotUnder is benchSnapshot with every workspace cwd under home.
func benchSnapshotUnder(n int, home string) source.Snapshot {
	statuses := []string{"working", "idle", "blocked"}
	var snap source.Snapshot
	pane := 0
	for w := range n {
		wsID := fmt.Sprintf("w%02d", w)
		cwd := fmt.Sprintf("%s/Proyectos/%s-%02d", home, benchWords[w%len(benchWords)], w)
		snap.Workspaces = append(snap.Workspaces, source.Workspace{ID: wsID, Label: fmt.Sprintf("%s-%02d", benchWords[w%len(benchWords)], w), CWD: cwd})
		for t := range 2 + w%3 {
			tabID := fmt.Sprintf("%s:t%d", wsID, t)
			panes := 1 + (w+t)%2
			snap.Tabs = append(snap.Tabs, source.Tab{ID: tabID, WorkspaceID: wsID, Label: fmt.Sprintf("tab-%d", t+1), Number: t + 1, PaneCount: panes, Focused: t == 0})
			for p := range panes {
				sp := source.Pane{ID: fmt.Sprintf("%s:p%d", tabID, p), WorkspaceID: wsID, TabID: tabID, CWD: cwd, Focused: p == 0, TerminalTitle: "zsh"}
				if pane%3 == 0 {
					sp.Agent = "claude"
					sp.AgentStatus = statuses[(pane/3)%len(statuses)]
					sp.TerminalTitle = "Refactor the render path of " + snap.Workspaces[w].Label
				}
				snap.Panes = append(snap.Panes, sp)
				pane++
			}
		}
	}
	return snap
}

// benchAllCandidates returns the ~650-candidate all view: one Herdr
// candidate per snapshot workspace, 20 configured workspaces and 600
// zoxide/projects directories under benchHome.
func benchAllCandidates(snap source.Snapshot) []source.Candidate {
	return benchAllCandidatesUnder(snap, benchHome)
}

// benchAllCandidatesUnder is benchAllCandidates with every path under home.
func benchAllCandidatesUnder(snap source.Snapshot, home string) []source.Candidate {
	cands := make([]source.Candidate, 0, len(snap.Workspaces)+620)
	for _, ws := range snap.Workspaces {
		cands = append(cands, herdrCandidate(ws.Label, ws.CWD, ws.ID))
	}
	for i := range 20 {
		cands = append(cands, workspaceEntryCandidate(fmt.Sprintf("configured-%s-%02d", benchWords[i%len(benchWords)], i), fmt.Sprintf("%s/allsafe/%s-%02d", home, benchWords[i%len(benchWords)], i)))
	}
	for i := range 600 {
		rel := fmt.Sprintf("Proyectos/%s/%s-%03d", benchWords[(i/7)%len(benchWords)], benchWords[i%len(benchWords)], i)
		if i%2 == 0 {
			cands = append(cands, zoxideCandidate("~/"+rel, home+"/"+rel))
		} else {
			cands = append(cands, projectCandidate("~/"+rel, home+"/"+rel))
		}
	}
	return cands
}

// benchStep feeds msg through Update and returns the resulting Model.
func benchStep(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

// benchAllModel builds the sized all-view model with a resolved preview.
func benchAllModel(b *testing.B) Model {
	b.Helper()
	snap := benchSnapshot(30)
	m := NewModelWithTree(benchAllCandidates(snap), benchWorkspaceRenderer{}, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha)})
	m = benchStep(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	res, _ := benchWorkspaceRenderer{}.Render(context.Background(), source.Candidate{})
	m = benchStep(m, previewResponseMsg{seq: m.previewSeq, result: res})
	if got := len(m.rows); got < 600 {
		b.Fatalf("bench model has %d rows, want ~650", got)
	}
	return m
}

// BenchmarkView_AllWide measures one full frame of the all view.
func BenchmarkView_AllWide(b *testing.B) {
	m := benchAllModel(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.View().Content
	}
}

// BenchmarkUpdate_TypeQuery measures typing "shep" one rune at a time and
// rendering the resulting frame. Each iteration restarts from the same
// empty-query model value, so every iteration filters the full set.
func BenchmarkUpdate_TypeQuery(b *testing.B) {
	base := benchAllModel(b)
	keys := []tea.KeyPressMsg{
		key("s"),
		key("h"),
		key("e"),
		key("p"),
	}
	b.ReportAllocs()
	for b.Loop() {
		m := base
		for _, k := range keys {
			m = benchStep(m, k)
		}
		_ = m.View().Content
	}
}

// benchHistorySnapshot returns a ranking snapshot with usage recorded for
// every candidate of the all view plus 2,900 unrelated launches: the scale
// of a ranking history after months of use, so a filter pass pays its real
// per-candidate frecency lookups. The store is built once per test binary.
var benchHistorySnapshot = sync.OnceValues(func() (ranking.Snapshot, error) {
	dir, err := os.MkdirTemp("", "shep-bench-ranking-")
	if err != nil {
		return ranking.Snapshot{}, err
	}
	defer os.RemoveAll(dir)
	store, err := ranking.OpenPath(filepath.Join(dir, "ranking.sqlite3"))
	if err != nil {
		return ranking.Snapshot{}, err
	}
	defer store.Close()
	ctx := context.Background()
	for i, c := range benchAllCandidates(benchSnapshot(30)) {
		for range 1 + i%3 {
			if err := store.Record(ctx, c); err != nil {
				return ranking.Snapshot{}, err
			}
		}
	}
	for i := range 2900 {
		gone := zoxideCandidate(fmt.Sprintf("~/gone/%04d", i), fmt.Sprintf("%s/gone/%04d", benchHome, i))
		if err := store.Record(ctx, gone); err != nil {
			return ranking.Snapshot{}, err
		}
	}
	return store.Snapshot(ctx, ""), nil
})

// BenchmarkUpdate_TypeQueryWithHistory is BenchmarkUpdate_TypeQuery with a
// populated ranking history, as every long-lived installation has.
func BenchmarkUpdate_TypeQueryWithHistory(b *testing.B) {
	snapshot, err := benchHistorySnapshot()
	if err != nil {
		b.Fatal(err)
	}
	snap := benchSnapshot(30)
	base := NewModelWithTree(benchAllCandidates(snap), benchWorkspaceRenderer{}, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha), RankingSnapshot: snapshot})
	base = benchStep(base, tea.WindowSizeMsg{Width: 140, Height: 38})
	if got := len(base.rows); got < 600 {
		b.Fatalf("bench model has %d rows, want ~650", got)
	}
	keys := []tea.KeyPressMsg{
		key("s"),
		key("h"),
		key("e"),
		key("p"),
	}
	b.ReportAllocs()
	for b.Loop() {
		m := base
		for _, k := range keys {
			m = benchStep(m, k)
		}
		_ = m.View().Content
	}
}

// BenchmarkUpdate_TypeQueryResolved is BenchmarkUpdate_TypeQuery with every
// candidate carrying its resolved presentation, as the command layer's
// producers attach it (effective.Resolver.Attach), so rows draw from the
// candidate's own presentation instead of their source's.
func BenchmarkUpdate_TypeQueryResolved(b *testing.B) {
	snap := benchSnapshot(30)
	cands := benchAllCandidates(snap)
	effective.New(config.Defaults()).Attach(cands)
	base := NewModelWithTree(cands, benchWorkspaceRenderer{}, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha)})
	base = benchStep(base, tea.WindowSizeMsg{Width: 140, Height: 38})
	keys := []tea.KeyPressMsg{
		key("s"),
		key("h"),
		key("e"),
		key("p"),
	}
	b.ReportAllocs()
	for b.Loop() {
		m := base
		for _, k := range keys {
			m = benchStep(m, k)
		}
		_ = m.View().Content
	}
}

// BenchmarkUpdate_SpinnerTickAll measures one spinner frame on the all view
// while open workspaces have working agents: the highlighted workspace's
// preview (tabs and a 24-line capture) draws the spinner in its meta line,
// so the frame exercises the preview memoization's head-only redraw.
func BenchmarkUpdate_SpinnerTickAll(b *testing.B) {
	m := benchAllModel(b)
	tick := spinner.TickMsg{ID: m.spinner.ID()}
	b.ReportAllocs()
	for b.Loop() {
		m = benchStep(m, tick)
		_ = m.View().Content
	}
}

// BenchmarkUpdate_SpinnerTickWorkspaceHeavyCapture measures one spinner
// frame on the all view when the highlighted open workspace has a working
// agent (its meta line and Tabs section draw the spinner) and a heavy,
// truecolor pane capture: the frame must not re-sanitize, re-fit or re-split
// the capture.
func BenchmarkUpdate_SpinnerTickWorkspaceHeavyCapture(b *testing.B) {
	snap := benchSnapshot(30)
	renderer := benchHeavyRenderer{capture: heavyCapture()}
	m := NewModelWithTree(benchAllCandidates(snap), renderer, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha)})
	m = benchStep(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	res, _ := renderer.Render(context.Background(), source.Candidate{})
	m = benchStep(m, previewResponseMsg{seq: m.previewSeq, result: res})
	if !strings.Contains(m.View().Content, "Active pane") || m.tree.WorkspaceAgentStatus("w00") != "working" {
		b.Fatal("setup: want a working workspace previewing its capture")
	}
	tick := spinner.TickMsg{ID: m.spinner.ID()}
	b.ReportAllocs()
	for b.Loop() {
		m = benchStep(m, tick)
		_ = m.View().Content
	}
}

// benchConfiguredTabsModel builds the all view the way `shep open` does with
// [tui].tabs configured (all, agents and a custom source tab): each source's
// results arrive as their own SourceResultMsg, as the streaming producers
// deliver them, and every path is a real directory under a temporary home.
// zoxide also reports every fifth projects directory through a symlinked
// parent and every seventh with different letter case, the overlaps
// deduplication resolves with EvalSymlinks and Stat in production.
func benchConfiguredTabsModel(b *testing.B) Model {
	b.Helper()
	home := b.TempDir()
	snap := benchSnapshotUnder(30, home)
	cands := benchAllCandidatesUnder(snap, home)
	for _, c := range cands {
		if err := os.MkdirAll(c.Path, 0o755); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, "Proyectos"), filepath.Join(home, "link")); err != nil {
		b.Fatal(err)
	}
	bySource := make(map[string][]source.Candidate)
	for i, c := range cands {
		bySource[c.Source] = append(bySource[c.Source], c)
		if c.Source != config.SourceProjects {
			continue
		}
		switch {
		case i%5 == 0:
			alias := strings.Replace(c.Path, filepath.Join(home, "Proyectos"), filepath.Join(home, "link"), 1)
			bySource[config.SourceZoxide] = append(bySource[config.SourceZoxide], zoxideCandidate(c.Label, alias))
		case i%7 == 0:
			alias := strings.Replace(c.Path, "Proyectos", "proyectos", 1)
			bySource[config.SourceZoxide] = append(bySource[config.SourceZoxide], zoxideCandidate(strings.ToLower(c.Label), alias))
		}
	}
	for i := range 40 {
		bySource["kube-contexts"] = append(bySource["kube-contexts"], source.Candidate{
			Label: fmt.Sprintf("cluster-%02d", i), Source: "kube-contexts",
			Meta: map[string]string{"custom_source": "true", "custom_source_id": fmt.Sprintf("ctx-%02d", i)},
		})
	}
	layout := Layout{
		Theme:       testTheme(ThemeMocha),
		HomeDir:     home,
		SourceOrder: []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects},
		Tabs: []TabDefinition{
			{ID: "all", Kind: TabAll},
			{ID: "agents", Kind: TabAgents},
			{ID: "kube-contexts", Kind: TabCustomSource},
		},
	}
	m := NewModelWithProducers(nil, "", benchWorkspaceRenderer{}, context.Background(), layout)
	m = benchStep(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m = benchStep(m, SourceResultMsg{Source: config.SourceHerdr, Candidates: bySource[config.SourceHerdr], Tree: NewTreeExpanderFromSnapshot(snap), Snapshot: &snap})
	for _, name := range []string{config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects, "kube-contexts"} {
		m = benchStep(m, SourceResultMsg{Source: name, Candidates: bySource[name]})
	}
	res, _ := benchWorkspaceRenderer{}.Render(context.Background(), source.Candidate{})
	m = benchStep(m, previewResponseMsg{seq: m.previewSeq, result: res})
	if m.ActiveTab() != "all" || len(m.rows) < 600 || m.tree.WorkspaceAgentStatus("w00") != "working" {
		b.Fatalf("setup: tab %q with %d rows; want the all view of ~650 rows with a working workspace", m.ActiveTab(), len(m.rows))
	}
	return m
}

// BenchmarkUpdate_SpinnerTickConfiguredTabs is BenchmarkUpdate_SpinnerTickAll
// with configured tabs and on-disk paths: the frame must not deduplicate
// (and so stat) the all tab's candidates to print its count.
func BenchmarkUpdate_SpinnerTickConfiguredTabs(b *testing.B) {
	m := benchConfiguredTabsModel(b)
	tick := spinner.TickMsg{ID: m.spinner.ID()}
	b.ReportAllocs()
	for b.Loop() {
		m = benchStep(m, tick)
		_ = m.View().Content
	}
}

// BenchmarkUpdate_TypeQueryConfiguredTabs is BenchmarkUpdate_TypeQuery with
// configured tabs and on-disk paths: a keystroke must not touch the disk.
func BenchmarkUpdate_TypeQueryConfiguredTabs(b *testing.B) {
	base := benchConfiguredTabsModel(b)
	keys := []tea.KeyPressMsg{
		key("s"),
		key("h"),
		key("e"),
		key("p"),
	}
	b.ReportAllocs()
	for b.Loop() {
		m := base
		for _, k := range keys {
			m = benchStep(m, k)
		}
		_ = m.View().Content
	}
}

// BenchmarkUpdate_SpinnerTickAgents measures one spinner frame on the
// agents view with two working agents: the idle-CPU path the picker pays
// several times per second while it sits open.
func BenchmarkUpdate_SpinnerTickAgents(b *testing.B) {
	snap := benchSnapshot(8)
	// Keep exactly ten agent panes, two of them working.
	agents := 0
	for i := range snap.Panes {
		if snap.Panes[i].Agent == "" {
			continue
		}
		agents++
		switch {
		case agents > 10:
			snap.Panes[i].Agent, snap.Panes[i].AgentStatus = "", ""
		case agents <= 2:
			snap.Panes[i].AgentStatus = "working"
		case agents%2 == 0:
			snap.Panes[i].AgentStatus = "idle"
		default:
			snap.Panes[i].AgentStatus = "blocked"
		}
	}
	cands := make([]source.Candidate, 0, len(snap.Workspaces))
	for _, ws := range snap.Workspaces {
		cands = append(cands, herdrCandidate(ws.Label, ws.CWD, ws.ID))
	}
	m := NewModelWithTree(cands, nil, NewTreeExpanderFromSnapshot(snap), Layout{Theme: testTheme(ThemeMocha), InitialTab: "agents"})
	m = benchStep(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	if got := len(m.rows); got != 10 {
		b.Fatalf("agents view has %d rows, want 10", got)
	}
	tick := spinner.TickMsg{ID: m.spinner.ID()}
	b.ReportAllocs()
	for b.Loop() {
		m = benchStep(m, tick)
		_ = m.View().Content
	}
}
