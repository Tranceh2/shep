package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

func pinKeyMsg() tea.KeyPressMsg {
	return key("ctrl+f")
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
			m, _ = update(t, m, key(string(r)))
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
	t.Parallel()
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
		Theme: testTheme(ThemePlain),
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
	if acc := m.rowAccessoryText(m.rows[0]); !strings.Contains(acc, "★") {
		t.Fatalf("pinned row accessories = %q, want the ★ pin", acc)
	}
	if primary, _ := m.rowDisplayText(m.rows[0]); strings.Contains(primary, "★") || strings.Contains(primary, "•") {
		t.Fatalf("pinned row primary = %q, the pin must not prefix the label", primary)
	}
	if !strings.Contains(footerText(m), "ctrl+f unpin") {
		t.Fatalf("footer = %q, want contextual unpin hint", footerText(m))
	}
}

func TestPinChildRowIsTruthfulNoOp(t *testing.T) {
	called := false
	m := NewModelWithLayout(nil, nil, Layout{
		Theme: testTheme(ThemePlain),
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
	// The refusal is reported, and the footer never offers ctrl+f on a row
	// it cannot pin (no "ctrl+f unavailable" hint).
	footer := footerText(m)
	if !strings.Contains(m.pinStatus.text, "child rows") || !strings.Contains(footer, m.pinStatus.text) || strings.Contains(footer, keyChordPin) {
		t.Fatalf("child feedback was not truthful: footer=%q status=%q", footer, m.pinStatus.text)
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
	if !strings.Contains(m.pinStatus.text, "pin update failed") {
		t.Fatalf("status = %q, want visible persistence error", m.pinStatus.text)
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
	m, _ = update(t, m, key("down"))
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
