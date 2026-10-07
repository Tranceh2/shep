package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

func closeKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlX} }

func TestCloseFooterUsesSharedKeyBinding(t *testing.T) {
	m := NewModelWithLayout([]source.Candidate{{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}}}, nil, Layout{Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }})
	m, _ = update(t, m, sizeMsg(120, 36))
	help := stripNonSGRANSI(m.helpBodyText(160))
	if !hasHint(m.footerHints(), keyBindingClose.footerChord, keyBindingClose.footerLabel) || !strings.Contains(help, keyBindingClose.chord) || !strings.Contains(help, keyBindingClose.help) {
		t.Fatalf("footer=%q help=%q", footerText(m), help)
	}
}

func TestCloseFeedbackClearsOnNextKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		closer Closer
		row    Row
		want   string
	}{
		{"success", func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }, Row{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}, "closed tab"},
		{"failure", func(context.Context, string, string) CloseResultMsg {
			return CloseResultMsg{Err: errors.New("workspace_group_close_required")}
		}, Row{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}, "close failed: workspace_group_close_required"},
		{"not open", func(context.Context, string, string) CloseResultMsg {
			t.Fatal("non-open row invoked closer")
			return CloseResultMsg{}
		}, Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceProjects}}, "not an open Herdr item"},
		{"unavailable", nil, Row{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}, "Herdr close unavailable"},
	} {
		for _, action := range []struct {
			name string
			key  tea.KeyMsg
		}{
			{"move", tea.KeyMsg{Type: tea.KeyDown}},
			{"type", plainKeyMsg('z')},
			{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		} {
			t.Run(tc.name+"/"+action.name, func(t *testing.T) {
				m := NewModelWithLayout(nil, nil, Layout{Closer: tc.closer})
				m.rows = []Row{tc.row}
				m, cmd := update(t, m, closeKey())
				if cmd != nil {
					m, _ = update(t, m, cmd())
				}
				// Problems replace the hints; success is right-aligned beside them.
				if got := footerText(m); !strings.HasSuffix(got, tc.want) {
					t.Fatalf("status = %q, want it to end with %q", got, tc.want)
				}
				m, _ = update(t, m, action.key)
				if got := footerText(m); strings.Contains(got, tc.want) || !strings.Contains(got, "? help") {
					t.Fatalf("next key left status in footer: %q", got)
				}
				if action.name == "type" && m.query != "z" {
					t.Fatalf("typing did not search: %q", m.query)
				}
				if action.name == "esc" && !m.cancelled {
					t.Fatal("esc did not cancel picker")
				}
			})
		}
	}
}

func TestClosePendingStatusSurvivesNonKeyUpdatesAndOverlappingKey(t *testing.T) {
	row := Row{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}
	m := NewModelWithLayout(nil, nil, Layout{Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }})
	m.rows = []Row{row}
	m, cmd := update(t, m, closeKey())
	if cmd == nil {
		t.Fatal("no close command")
	}
	m, _ = update(t, m, sizeMsg(120, 36))
	if got := footerText(m); got != "closing tab..." {
		t.Fatalf("in flight: %q", got)
	}
	m, second := update(t, m, closeKey())
	if second != nil || footerText(m) != "closing tab..." {
		t.Fatal("overlapping key cleared in-flight feedback")
	}
	m, _ = update(t, m, cmd())
	if !strings.HasSuffix(footerText(m), "closed tab") {
		t.Fatalf("completion message missing: %q", footerText(m))
	}

	m = NewModelWithLayout(nil, nil, Layout{ConfirmClose: []string{"tab"}, Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }})
	m.rows = []Row{row}
	m, _ = update(t, m, closeKey())
	prompt := footerText(m)
	m, _ = update(t, m, sizeMsg(120, 36))
	if footerText(m) != prompt || m.closeConfirm == nil {
		t.Fatal("resize cleared confirmation")
	}
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.cancelled || m.closeConfirm != nil || footerText(m) != "close cancelled" {
		t.Fatal("esc did not only cancel prompt")
	}
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if strings.Contains(footerText(m), "close cancelled") {
		t.Fatal("cancellation feedback persisted")
	}
}

func TestCloseFromPreviewFocus(t *testing.T) {
	m := NewModelWithLayout(nil, nil, Layout{Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }})
	m.rows = []Row{{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}}
	m.focus = FocusPreview
	m, cmd := update(t, m, closeKey())
	if cmd == nil || !m.closePending || m.focus != FocusPreview {
		t.Fatal("preview focus did not close selected row")
	}
}

func TestCloseRowKindsAndNoOps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		row      Row
		kind, id string
	}{
		{"agent", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceAgents, Meta: map[string]string{"pane_id": "p1"}}}, "pane", "p1"},
		{"tree pane", Row{Kind: RowPane, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"pane_id": "p2"}}}, "pane", "p2"},
		{"tab", Row{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}, "tab", "t1"},
		{"workspace", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Label: "editor", Meta: map[string]string{"workspace_id": "w1"}}}, "workspace", "w1"},
		{"project", Row{Candidate: source.Candidate{Source: config.SourceProjects, Meta: map[string]string{"workspace_id": "w1"}}}, "", ""},
		{"missing id", Row{Candidate: source.Candidate{Source: config.SourceAgents}}, "", ""},
		{"session", Row{Candidate: source.Candidate{Source: config.SourceSessions, Meta: map[string]string{"pane_id": "p1"}}}, "", ""},
		{"custom", Row{Candidate: source.Candidate{Source: "custom", Meta: map[string]string{"tab_id": "t1"}}}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			m := NewModelWithLayout(nil, nil, Layout{Closer: func(_ context.Context, kind, id string) CloseResultMsg {
				calls++
				return CloseResultMsg{Kind: kind, ID: id}
			}})
			m.rows = []Row{tc.row}
			m.cursor = 0
			m, cmd := update(t, m, closeKey())
			if tc.kind == "" {
				if cmd != nil || calls != 0 || !strings.Contains(footerText(m), "not an open Herdr item") {
					t.Fatalf("no-op cmd=%v calls=%d footer=%q", cmd, calls, footerText(m))
				}
				return
			}
			if cmd == nil || calls != 0 || !m.closePending {
				t.Fatalf("close not async: cmd=%v calls=%d", cmd, calls)
			}
			result, ok := cmd().(CloseResultMsg)
			if !ok || result.Kind != tc.kind || result.ID != tc.id || calls != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls)
			}
			m, cmd2 := update(t, m, closeKey())
			if cmd2 != nil || calls != 1 {
				t.Fatal("overlapping close")
			}
			m, _ = update(t, m, result)
			if m.closePending || !strings.Contains(footerText(m), "closed "+tc.kind) {
				t.Fatalf("status=%q", footerText(m))
			}
		})
	}
}

func TestCloseRefreshRemovesAgentFromAgentsAndSourceTabs(t *testing.T) {
	initial := snapshotGeneration("w1", "p1", "working")
	initial.Panes[0].Agent = "pi"
	for _, tc := range []struct {
		name     string
		tabs     []TabDefinition
		selected string
	}{
		{"agents", nil, "agents"},
		{"source", []TabDefinition{{ID: "agents", Kind: TabSource}}, "agents"},
		{"group", []TabDefinition{{ID: "group", Kind: TabGroup, SourceOrder: []string{config.SourceAgents}, Load: func(_ context.Context, snap *source.Snapshot) ([]source.Candidate, error) {
			if snap == nil {
				return nil, nil
			}
			return source.AgentCandidates(*snap), nil
		}}}, "group"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: source.Snapshot{}}}}
			layout := Layout{SourceOrder: []string{config.SourceAgents}, Tabs: tc.tabs, InitialTab: tc.selected, Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }}
			m := NewModelWithTree(source.AgentCandidates(initial), nil, NewTreeExpanderFromSnapshot(initial), layout).WithSnapshotRefresh(driver, initial, nil, nil)
			if tc.name == "group" {
				m.groupCandidates["group"] = source.AgentCandidates(initial)
				m.applyFilter()
			}
			if len(m.rows) == 0 || m.rows[0].Candidate.Source != config.SourceAgents {
				t.Fatalf("agent not displayed: %+v", m.rows)
			}
			m, cmd := update(t, m, closeKey())
			if cmd == nil {
				t.Fatalf("close unavailable: row=%+v status=%q", m.rows[m.cursor], m.closeStatus.text)
			}
			m, refresh := update(t, m, cmd())
			m, next := update(t, m, refresh())
			if next != nil {
				if batch, ok := next().(tea.BatchMsg); ok {
					for _, cmd := range batch {
						if cmd == nil {
							continue
						}
						if result, ok := cmd().(groupResultMsg); ok {
							m, _ = update(t, m, result)
						}
					}
				}
			}
			if len(m.rows) != 0 || m.cursor != 0 || m.cancelled {
				t.Fatalf("rows=%+v cursor=%d cancelled=%v", m.rows, m.cursor, m.cancelled)
			}
		})
	}
}

func TestCloseConfirmationIsPerKind(t *testing.T) {
	for _, tc := range []struct {
		kind string
		row  Row
	}{
		{"pane", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceAgents, Meta: map[string]string{"pane_id": "p1"}}}},
		{"tab", Row{Kind: RowTab, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"tab_id": "t1"}}}},
		{"workspace", Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Meta: map[string]string{"workspace_id": "w1"}}}},
	} {
		for _, listed := range []bool{true, false} {
			confirm := []string{"workspace", "tab", "pane"}
			if !listed {
				confirm = slices.DeleteFunc(confirm, func(kind string) bool { return kind == tc.kind })
			}
			m := NewModelWithLayout(nil, nil, Layout{ConfirmClose: confirm, Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} }})
			m.rows = []Row{tc.row}
			m, cmd := update(t, m, closeKey())
			if listed != (m.closeConfirm != nil) || listed == (cmd != nil) {
				t.Fatalf("kind=%s listed=%v confirm=%v cmd=%v", tc.kind, listed, m.closeConfirm, cmd)
			}
		}
	}
}

func TestCloseWhileSnapshotAlreadyInFlightRefreshesAfterIt(t *testing.T) {
	initial := snapshotGeneration("w1", "p1", "working")
	driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: initial}, {snapshot: source.Snapshot{}}}}
	m := NewModelWithTree(source.HerdrCandidates(initial), nil, NewTreeExpanderFromSnapshot(initial), Layout{
		Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{} },
	}).WithSnapshotRefresh(driver, initial, nil, nil)
	m.lastSnapshotAt = time.Now().Add(-snapshotTTL)
	old := m.maybeRefreshSnapshot()
	m, cmd := update(t, m, closeKey())
	m, refresh := update(t, m, cmd())
	if refresh != nil {
		t.Fatal("started overlapping snapshot")
	}
	m, refresh = update(t, m, old())
	if refresh == nil {
		t.Fatal("did not schedule post-close generation")
	}
	m, _ = update(t, m, refresh())
	if driver.calls != 2 || len(m.rows) != 0 {
		t.Fatalf("calls=%d rows=%d", driver.calls, len(m.rows))
	}
}

func TestCloseConfirmationAndUnavailable(t *testing.T) {
	row := Row{Kind: RowCandidate, Candidate: source.Candidate{Source: config.SourceHerdr, Label: "work", Meta: map[string]string{"workspace_id": "w1"}}}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, plainKeyMsg('n'), closeKey()} {
		m := NewModelWithLayout(nil, nil, Layout{ConfirmClose: []string{"workspace"}, Closer: func(context.Context, string, string) CloseResultMsg {
			t.Fatal("cancel ran closer")
			return CloseResultMsg{}
		}})
		m.rows = []Row{row}
		m, cmd := update(t, m, closeKey())
		if cmd != nil || !strings.Contains(footerText(m), "close workspace") {
			t.Fatal("missing prompt")
		}
		m, cmd = update(t, m, key)
		if cmd != nil || m.cancelled || m.closePending || m.closeConfirm != nil {
			t.Fatalf("cancel: %+v cmd=%v", m, cmd)
		}
	}
	m := NewModelWithLayout(nil, nil, Layout{ConfirmClose: []string{"workspace"}, Closer: func(context.Context, string, string) CloseResultMsg {
		return CloseResultMsg{Kind: "workspace", ID: "w1"}
	}})
	m.rows = []Row{row}
	m, _ = update(t, m, closeKey())
	m, cmd := update(t, m, plainKeyMsg('y'))
	if cmd == nil || !m.closePending {
		t.Fatal("y did not confirm")
	}
	m = NewModelWithLayout(nil, nil, Layout{})
	m.rows = []Row{row}
	m, cmd = update(t, m, closeKey())
	if cmd != nil || !strings.Contains(footerText(m), "unavailable") {
		t.Fatalf("nil closer: %q", footerText(m))
	}
}

func TestCloseSuccessForcesSnapshotRefreshAndFailureDoesNot(t *testing.T) {
	initial := snapshotGeneration("w1", "p1", "working")
	initial.Panes[0].Agent = "pi"
	for _, fail := range []bool{false, true} {
		driver := &scriptedSnapshotDriver{responses: []snapshotDriverResponse{{snapshot: source.Snapshot{}}}}
		err := error(nil)
		if fail {
			err = errors.New("workspace_group_close_required")
		}
		m := NewModelWithTree(source.HerdrCandidates(initial), nil, NewTreeExpanderFromSnapshot(initial), Layout{Closer: func(context.Context, string, string) CloseResultMsg { return CloseResultMsg{Err: err} }}).WithSnapshotRefresh(driver, initial, nil, nil)
		m.lastSnapshotAt = time.Now()
		m, cmd := update(t, m, closeKey())
		if cmd == nil {
			t.Fatal("missing close cmd")
		}
		m, refresh := update(t, m, cmd())
		if fail {
			if refresh != nil || driver.calls != 0 || !strings.Contains(footerText(m), "workspace_group_close_required") || len(m.rows) == 0 {
				t.Fatal("error changed list or refreshed")
			}
			continue
		}
		if refresh == nil {
			t.Fatal("no forced refresh")
		}
		m, _ = update(t, m, refresh())
		if driver.calls != 1 || len(m.rows) != 0 || m.cancelled {
			t.Fatalf("snapshot calls=%d rows=%d cancelled=%v", driver.calls, len(m.rows), m.cancelled)
		}
	}
}

// TestCloseTargetFor_TreeRows proves expanded tree children, which are
// synthesized under an open workspace without a Source of their own, close
// as their tab or pane; rows that are not open Herdr items stay refused.
func TestCloseTargetFor_TreeRows(t *testing.T) {
	t.Parallel()
	tab := Row{Kind: RowTab, Depth: 1, Candidate: source.Candidate{Label: "api", Meta: map[string]string{"workspace_id": "w1", "tab_id": "w1:t1"}}}
	pane := Row{Kind: RowPane, Depth: 2, Candidate: source.Candidate{Label: "zsh", Meta: map[string]string{"workspace_id": "w1", "tab_id": "w1:t1", "pane_id": "w1:p1"}}}
	for _, tc := range []struct {
		name     string
		row      Row
		kind, id string
	}{
		{"tree tab", tab, "tab", "w1:t1"},
		{"tree pane", pane, "pane", "w1:p1"},
		{"zoxide row", Row{Kind: RowCandidate, Candidate: zoxideCandidate("a", "/a")}, "", ""},
		{"tab without a workspace", Row{Kind: RowTab, Candidate: source.Candidate{Meta: map[string]string{"tab_id": "t9"}}}, "", ""},
		{"custom source tab-like row", Row{Kind: RowTab, Candidate: source.Candidate{Source: "prs", Meta: map[string]string{"workspace_id": "w1", "tab_id": "t1"}}}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := closeTargetFor(tc.row)
			if ok != (tc.kind != "") || target.kind != tc.kind || target.id != tc.id {
				t.Errorf("closeTargetFor = %+v, %v; want %s %s", target, ok, tc.kind, tc.id)
			}
		})
	}

	closer := func(_ context.Context, kind, id string) CloseResultMsg { return CloseResultMsg{Kind: kind, ID: id} }
	m := NewModelWithLayout(nil, nil, Layout{Closer: closer, ConfirmClose: []string{"tab"}})
	m.rows = []Row{tab}
	if !hasHint(m.footerHints(), keyChordClose, keyBindingClose.footerLabel) {
		t.Errorf("tree tab footer = %q, want ctrl+x close", footerText(m))
	}
	m, cmd := update(t, m, closeKey())
	if cmd != nil || m.closeConfirm == nil || !strings.Contains(footerText(m), `close tab "api"?`) {
		t.Fatalf("tree tab close must ask first: cmd=%v footer=%q", cmd != nil, footerText(m))
	}
	m, cmd = update(t, m, plainKeyMsg('y'))
	if cmd == nil {
		t.Fatal("confirming did not close the tab")
	}
	if got := cmd().(CloseResultMsg); got.Kind != "tab" || got.ID != "w1:t1" {
		t.Errorf("close result = %+v, want tab w1:t1", got)
	}

	m = NewModelWithLayout(nil, nil, Layout{Closer: closer})
	m.rows = []Row{pane}
	if _, cmd = update(t, m, closeKey()); cmd == nil {
		t.Fatal("tree pane close did not start")
	} else if got := cmd().(CloseResultMsg); got.Kind != "pane" || got.ID != "w1:p1" {
		t.Errorf("close result = %+v, want pane w1:p1", got)
	}
}
