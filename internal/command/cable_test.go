package command

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// cableToml is a minimal projection of cables/shep.toml — only the fields
// this test needs to check the {split:\t:N} column references against
// renderTSV's real emission order (path=0, label=1, icon=2).
type cableToml struct {
	Source struct {
		Command string `toml:"command"`
		Display string `toml:"display"`
		Output  string `toml:"output"`
	} `toml:"source"`
	Preview struct {
		Command string `toml:"command"`
	} `toml:"preview"`
	Actions struct {
		Open struct {
			Command string `toml:"command"`
		} `toml:"open"`
	} `toml:"actions"`
}

// TestCable_ColumnIndicesMatchRenderTSVOrder guards cables/shep.toml against
// silent desync from renderTSV's column order (path=0, label=1, icon=2).
//
// This test only checks the cable's own template strings for the expected
// {split:\t:N} substrings — it cannot, by itself, catch a future reorder of
// renderTSV's columns in list.go, since it never runs renderTSV. The other
// half of the regression guard is TestRender_TSVIncludesIconColumn (and
// TestRender_TSVIconColumnStableWhenEmpty) in list_test.go, which assert
// renderTSV's real output has path/label/icon in that literal index order.
// The cable's indices are manually kept in sync with that order; together,
// these tests are the regression guard: if list.go's column order changes,
// TestRender_TSVIncludesIconColumn fails; if the cable's template strings
// drift from the documented indices, this test fails. Neither alone catches
// both directions of a desync.
func TestCable_ColumnIndicesMatchRenderTSVOrder(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../cables/shep.toml")
	if err != nil {
		t.Fatalf("read cables/shep.toml: %v", err)
	}
	var cable cableToml
	if err := toml.Unmarshal(data, &cable); err != nil {
		t.Fatalf("cables/shep.toml is not valid TOML: %v", err)
	}

	// TOML basic strings decode "\t" to a literal TAB byte, so the parsed
	// template fields contain a real tab, not a backslash-t sequence — build
	// the expected substrings the same way (interpreted string, not raw).
	splitField := func(n int) string { return fmt.Sprintf("{split:\t:%d}", n) }

	// Icon (index 2) and label (index 1) feed the results-list display.
	if !strings.Contains(cable.Source.Display, splitField(2)) {
		t.Errorf("[source].display must reference {split:\\t:2} (icon), got: %q", cable.Source.Display)
	}
	if !strings.Contains(cable.Source.Display, splitField(1)) {
		t.Errorf("[source].display must reference {split:\\t:1} (label), got: %q", cable.Source.Display)
	}

	// path (index 0) is what output/preview/open act on.
	if !strings.Contains(cable.Source.Output, splitField(0)) {
		t.Errorf("[source].output must reference {split:\\t:0} (path), got: %q", cable.Source.Output)
	}
	if !strings.Contains(cable.Preview.Command, splitField(0)) {
		t.Errorf("[preview].command must reference {split:\\t:0} (path), got: %q", cable.Preview.Command)
	}
	if !strings.Contains(cable.Actions.Open.Command, splitField(0)) {
		t.Errorf("[actions.open].command must reference {split:\\t:0} (path), got: %q", cable.Actions.Open.Command)
	}
}
