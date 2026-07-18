package tui

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestRenderHeader_MultiSourceMatches_ShowsPerSourceCounts proves a non-empty
// query with matches from more than one source shows a per-source match
// count breakdown in the header, so a query spanning multiple providers
// explains WHERE its matches came from at a glance, instead of the plain
// "M of N" total which does not say which sources contributed.
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
	if !strings.Contains(plain, "herdr 1") {
		t.Errorf("header = %q, want a per-source count for herdr (1 match)", plain)
	}
	if !strings.Contains(plain, "zoxide 1") {
		t.Errorf("header = %q, want a per-source count for zoxide (1 match)", plain)
	}
	if strings.Contains(plain, "projects") {
		t.Errorf("header = %q, must not mention projects (0 matches)", plain)
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
