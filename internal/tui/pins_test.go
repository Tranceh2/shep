package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

func pinKeyMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyCtrlF}
}

func plainKeyMsg(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestPrintableFAndPWithPinTogglerAppendQueryAndDoNotPersist(t *testing.T) {
	for _, r := range []rune{'f', 'p'} {
		t.Run(string(r), func(t *testing.T) {
			candidate := source.Candidate{Source: "projects", Path: "/palm", Label: "palm"}
			called := false
			m := NewModelWithLayout([]source.Candidate{
				candidate,
				{Source: "projects", Path: "/other", Label: "other"},
			}, nil, Layout{
				PinToggler: func(context.Context, source.Candidate) PinToggleResultMsg {
					called = true
					return PinToggleResultMsg{Pinned: true}
				},
			})
			m, _ = update(t, m, sizeMsg(120, 36))
			m, _ = update(t, m, plainKeyMsg(r))
			if m.query != string(r) {
				t.Fatalf("plain %c produced query=%q, want %q", r, m.query, string(r))
			}
			if called {
				t.Fatalf("plain %c invoked pin persistence", r)
			}
		})
	}
}

func TestCtrlFWithPinTogglerInvokesPersistence(t *testing.T) {

	candidate := source.Candidate{Source: "projects", Path: "/repo", Label: "repo"}
	called := false
	m := NewModelWithLayout([]source.Candidate{candidate}, nil, Layout{
		PinToggler: func(_ context.Context, got source.Candidate) PinToggleResultMsg {
			called = true
			return PinToggleResultMsg{Key: ranking.PinKey(got), Candidate: got, Pinned: true}
		},
	})
	m, _ = update(t, m, sizeMsg(120, 36))
	m, cmd := update(t, m, pinKeyMsg())
	if cmd == nil {
		t.Fatal("ctrl+f did not return a persistence command")
	}
	if m.query != "" {
		t.Fatalf("ctrl+f changed query to %q", m.query)
	}
	if called {
		t.Fatal("pin callback ran synchronously; expected a tea.Cmd")
	}
	result := cmd()
	if _, ok := result.(PinToggleResultMsg); !ok {
		t.Fatalf("ctrl+f command returned %T, want PinToggleResultMsg", result)
	}
	if !called {
		t.Fatal("ctrl+f did not invoke pin persistence")
	}
}

func TestPinTopLevelRowUpdatesStateMarkerAndFooter(t *testing.T) {
	candidate := source.Candidate{Source: "projects", Path: "/repo", Label: "repo"}
	called := false
	m := NewModelWithLayout([]source.Candidate{candidate}, nil, Layout{
		Theme: ThemePlain,
		PinToggler: func(_ context.Context, got source.Candidate) PinToggleResultMsg {
			called = true
			return PinToggleResultMsg{Key: ranking.PinKey(got), Candidate: got, Pinned: true}
		},
	})
	m, _ = update(t, m, sizeMsg(120, 36))
	next, cmd := m.Update(pinKeyMsg())
	m = next.(Model)
	if called {
		t.Fatal("pin callback ran synchronously; expected a tea.Cmd")
	}
	if cmd == nil {
		t.Fatal("pin key did not return a command")
	}
	result := cmd()
	m, _ = update(t, m, result)
	if !m.rankingSnapshot.IsPinned(candidate) {
		t.Fatal("successful pin did not update the immutable snapshot")
	}
	primary, _ := m.rowDisplayText(m.rows[0])
	if !strings.Contains(primary, "•") {
		t.Fatal("pinned row did not render the pin marker")
	}
	if !strings.Contains(m.footerHints(), "ctrl+f unpin") {
		t.Fatalf("footer = %q, want contextual unpin hint", m.footerHints())
	}
}

func TestPinChildRowIsTruthfulNoOp(t *testing.T) {
	called := false
	m := NewModelWithLayout(nil, nil, Layout{
		Theme: ThemePlain,
		PinToggler: func(context.Context, source.Candidate) PinToggleResultMsg {
			called = true
			return PinToggleResultMsg{Pinned: true}
		},
	})
	m.rows = []Row{{Kind: RowTab, Candidate: source.Candidate{Label: "tab"}, ID: "tab:t1"}}
	m.cursor = 0
	next, cmd := m.Update(pinKeyMsg())
	m = next.(Model)
	if cmd != nil || called {
		t.Fatal("child pin unexpectedly invoked persistence")
	}
	if !strings.Contains(m.pinStatus, "child rows") || !strings.Contains(m.footerHints(), "ctrl+f unavailable") {
		t.Fatalf("child feedback was not truthful: footer=%q status=%q", m.footerHints(), m.pinStatus)
	}
}

func TestPinPersistenceErrorPreservesStateAndShowsStatus(t *testing.T) {
	candidate := source.Candidate{Source: "projects", Path: "/repo", Label: "repo"}
	m := NewModelWithLayout([]source.Candidate{candidate}, nil, Layout{
		PinToggler: func(context.Context, source.Candidate) PinToggleResultMsg {
			return PinToggleResultMsg{Err: errors.New("database unavailable")}
		},
	})
	m, _ = update(t, m, sizeMsg(120, 36))
	next, cmd := m.Update(pinKeyMsg())
	m = next.(Model)
	m, _ = update(t, m, cmd())
	if m.rankingSnapshot.IsPinned(candidate) {
		t.Fatal("failed pin changed visible state")
	}
	if !strings.Contains(m.pinStatus, "pin update failed") {
		t.Fatalf("status = %q, want visible persistence error", m.pinStatus)
	}
}

func TestPinResultRetainsCursorIdentityAfterReordering(t *testing.T) {
	first := source.Candidate{Source: "projects", Path: "/first", Label: "first"}
	second := source.Candidate{Source: "projects", Path: "/second", Label: "second"}
	m := NewModelWithLayout([]source.Candidate{first, second}, nil, Layout{
		PinToggler: func(context.Context, source.Candidate) PinToggleResultMsg {
			return PinToggleResultMsg{Pinned: true}
		},
	})
	m, _ = update(t, m, sizeMsg(120, 36))
	m, _ = update(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.rows[m.cursor].Candidate.Label != "second" {
		t.Fatal("setup did not move cursor to second row")
	}
	next, cmd := m.Update(pinKeyMsg())
	m = next.(Model)
	result := cmd().(PinToggleResultMsg)
	result.Key = ranking.PinKey(second)
	m, _ = update(t, m, result)
	if m.rows[m.cursor].Candidate.Label != "second" {
		t.Fatalf("cursor moved after pin reorder: row=%q", m.rows[m.cursor].Candidate.Label)
	}
}
