package tui

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestStatusStyle_Mapping proves statusStyle maps each known agent_status
// value to its dedicated Catppuccin Mocha style (idle/working/blocked/done),
// and that any other value — including the explicit "unknown" status and any
// unrecognized string — falls back to statusUnknownStyle. This is the single
// source of truth both the preview "status:" line and the footer "focused:"
// segment style through (see statusStyle callers in model.go).
func TestStatusStyle_Mapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status string
		want   lipgloss.Style
	}{
		{"idle", "idle", palette.statusIdleStyle},
		{"working", "working", palette.statusWorkingStyle},
		{"blocked", "blocked", palette.statusBlockedStyle},
		{"done", "done", palette.statusDoneStyle},
		{"explicit unknown", "unknown", palette.statusUnknownStyle},
		{"unrecognized status falls back to unknown", "totally-bogus", palette.statusUnknownStyle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := statusStyle(tt.status)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("statusStyle(%q) = %#v, want %#v", tt.status, got, tt.want)
			}
		})
	}
}
