package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestRenderHeader_MultiSourceMatches_KeepsCandidateCount proves a non-empty
// query with matches from more than one source keeps the palette's compact
// total count. The row icons distinguish sources; the header reports only the
// result total.
func TestRenderHeader_MultiSourceMatches_ShowsPerSourceCounts(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		herdrCandidate("backend", "/srv/backend", "w1"),
		zoxideCandidate("back-alley", "/x/back-alley"),
		projectCandidate("frontend", "/srv/frontend"),
	}
	m := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 36))
	for _, r := range "back" {
		m, _ = update(t, m, key(string(r)))
	}
	plain := stripNonSGRANSI(m.renderHeader(120))
	if !strings.Contains(plain, "2 of 3") {
		t.Errorf("header = %q, want the compact candidate count", plain)
	}
	if strings.Contains(plain, "herdr 1") || strings.Contains(plain, "zoxide 1") {
		t.Errorf("header = %q, source breakdown must not replace the result total", plain)
	}
}

// TestRenderHeader_SingleSourceMatches_KeepsPlainCountFormat proves a query
// whose matches all come from ONE source keeps the plain "M of N" header
// format — the per-source breakdown only appears when it adds information
// (more than one source contributing).
func TestRenderHeader_SingleSourceMatches_KeepsPlainCountFormat(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		zoxideCandidate("alpha-dir", "/home/dev/alpha-dir"),
		zoxideCandidate("alpha-other", "/home/dev/alpha-other"),
		projectCandidate("shep", "/home/dev/shep"),
	}
	m := NewModelWithLayout(cands, nil, Layout{Theme: ThemeMocha})
	m, _ = update(t, m, sizeMsg(120, 36))
	for _, r := range "alpha" {
		m, _ = update(t, m, key(string(r)))
	}
	plain := stripNonSGRANSI(m.renderHeader(120))
	if !strings.Contains(plain, "2 of 3") {
		t.Errorf("header = %q, want the plain \"2 of 3\" count (single source matched)", plain)
	}
	if strings.Contains(plain, "zoxide 2") {
		t.Errorf("header = %q, must not show a per-source breakdown when only one source matched", plain)
	}
}
