package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

func TestPreview_RowTabDispatch(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		panes     []source.Pane
		readText  string
		readErr   error
		wantText  string
		wantErr   bool
		wantReads int
	}{
		{
			name: "captures selected tabs focused pane",
			panes: []source.Pane{
				{ID: "p1", WorkspaceID: "w1", TabID: "t1"},
				{ID: "p2", WorkspaceID: "w1", TabID: "t1", Focused: true},
			},
			readText:  "focused capture",
			wantText:  "focused capture",
			wantReads: 1,
		},
		{
			name:      "no pane produces unavailable preview",
			wantErr:   true,
			wantReads: 0,
		},
		{
			name: "pane read failure produces unavailable preview",
			panes: []source.Pane{
				{ID: "p1", WorkspaceID: "w1", TabID: "t1", Focused: true},
			},
			readErr:   errors.New("herdr pane read: boom"),
			wantErr:   true,
			wantReads: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			driver := &fakeTreeDriver{
				tabs:     []source.Tab{{ID: "t1", WorkspaceID: "w1"}},
				panes:    tt.panes,
				readText: tt.readText,
				readErr:  tt.readErr,
			}
			snapshot := source.Snapshot{
				Workspaces: []source.Workspace{{ID: "w1"}},
				Tabs:       driver.tabs,
				Panes:      tt.panes,
			}
			m := NewModel(nil, nil).WithSnapshotRefresh(driver, snapshot, nil, "")
			m.renderCtx = context.Background()
			m.rows = []Row{{
				Kind: RowTab,
				Candidate: source.Candidate{Meta: map[string]string{
					"workspace_id": "w1",
					"tab_id":       "t1",
				}},
			}}
			m.previewSeq = 4

			dispatched, _ := m.dispatchPreviewForRow(m.previewSeq)
			if !dispatched || !m.previewLoading {
				t.Fatalf("dispatchPreviewForRow() = dispatched=%t loading=%t, want both true", dispatched, m.previewLoading)
			}

			msg, ok := m.tabPreviewCmd(m.previewSeq, m.rows[0].Candidate)().(panePreviewMsg)
			if !ok {
				t.Fatalf("tabPreviewCmd() returned %T, want panePreviewMsg", msg)
			}
			if (msg.err != nil) != tt.wantErr {
				t.Fatalf("tabPreviewCmd() error = %v, want error=%t", msg.err, tt.wantErr)
			}
			if msg.text != tt.wantText {
				t.Errorf("tabPreviewCmd() text = %q, want %q", msg.text, tt.wantText)
			}
			if driver.readPaneN != tt.wantReads {
				t.Errorf("ReadPane calls = %d, want %d", driver.readPaneN, tt.wantReads)
			}

			m, _ = update(t, m, msg)
			if m.previewLoading {
				t.Error("previewLoading = true after pane preview response, want false")
			}
			if m.previewText != tt.wantText {
				t.Errorf("previewText = %q, want %q", m.previewText, tt.wantText)
			}
		})
	}
}

func TestPreview_StaleDiscard(t *testing.T) {
	t.Parallel()

	m := NewModel(nil, nil)
	m.previewSeq = 8
	m.previewLoading = true
	m.previewText = "current preview"

	m, _ = update(t, m, panePreviewMsg{seq: 7, text: "stale preview"})
	if m.previewText != "current preview" {
		t.Errorf("previewText = %q, want current preview to remain", m.previewText)
	}
	if !m.previewLoading {
		t.Error("previewLoading = false after stale response, want true")
	}
}
