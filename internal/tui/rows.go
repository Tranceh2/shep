package tui

import (
	"github.com/sahilm/fuzzy"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// defaultSourceOrder mirrors config.defaultSourceOrder (unexported in
// package config), used when the caller supplies no explicit source order —
// same fallback config.Load()/source.Registry.Enabled() already apply when
// general.sources is empty/absent. Kept as the exported config.Source*
// constants rather than reaching into the unexported config var.
var defaultSourceOrder = []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects}

// tabChildren is one synthesized tab row plus its own synthesized pane rows,
// both already built as source.Candidate (Source == config.SourceHerdrTab /
// config.SourceHerdrPane respectively) by synthesizeWorkspaceTree.
type tabChildren struct {
	Tab   source.Candidate
	Panes []source.Candidate
}

// workspaceChildren is the full synthesized child tree for one Herdr
// workspace: one tabChildren per open tab, in tab order.
type workspaceChildren struct {
	Tabs []tabChildren
}

// rowBuildInput gathers every external input buildRows needs so the function
// itself stays pure (no ctx, no driver calls, no clock) and is unit-testable
// with plain literals. Model.applyFilter is responsible for fetching
// `children` (via TreeExpander, cached) before calling buildRows.
type rowBuildInput struct {
	// candidates is the full flat candidate set in original provider order
	// (Model.baseCandidates for a tree-wired model, Model.candidates
	// otherwise).
	candidates []source.Candidate
	query      string
	// children maps a Herdr workspace's Meta["workspace_id"] to its
	// synthesized tab/pane tree. A missing entry means "no children fetched
	// for this workspace this pass" (degrades to zero children, never a
	// crash — mirrors the old TreeExpander.Fetch degrade-gracefully
	// contract).
	children map[string]workspaceChildren
	// expandedWorkspaces is the set of workspace_ids the user has manually
	// expanded (Left/Right/Enter on a RowCandidate, progressive disclosure
	// at an EMPTY query — see Model.toggleExpand). A non-empty query expands
	// a workspace automatically regardless of this set (so a matching
	// descendant is always visible), but this set is what keeps a workspace
	// expanded once the query is cleared back to "".
	expandedWorkspaces map[string]bool
	// sourceOrder is the configured group iteration order (config's
	// general.sources, in declaration order — the same order
	// source.Registry.Enabled() already collects candidates in). Empty
	// falls back to defaultSourceOrder, mirroring config.Load()'s own
	// empty-sources fallback.
	sourceOrder []string
}

// effectiveSourceOrder returns in.sourceOrder when non-empty, else
// defaultSourceOrder — the iteration order buildRows walks to flatten
// candidates into rows. A source with zero visible members for the current
// query simply contributes no rows (there is no header to omit anymore).
func effectiveSourceOrder(in rowBuildInput) []string {
	if len(in.sourceOrder) > 0 {
		return in.sourceOrder
	}
	return defaultSourceOrder
}

// rowIdentity returns a stable identity key for c, independent of its
// position in any slice, used for selection retention across a rebuild and
// for expand/collapse state lookups.
func rowIdentity(c source.Candidate) string {
	switch c.Source {
	case config.SourceHerdrPane:
		return "pane:" + c.Meta["pane_id"]
	case config.SourceHerdrTab:
		return "tab:" + c.Meta["tab_id"]
	case config.SourceHerdr:
		return "ws:" + c.Meta["workspace_id"]
	default:
		key := c.NormalizedPath
		if key == "" {
			key = c.Path
		}
		return c.Source + ":" + key
	}
}

// fuzzyMatches reports whether query fuzzy-matches haystack (sahilm/fuzzy,
// case-insensitive via its own equalFold, camelCase/separator-boundary aware
// — the same matcher the old flat picker used). An empty query always
// matches (callers gate the empty-query case separately; this helper is
// only ever invoked from a non-empty-query path in practice, but stays
// total so it is safe to call unconditionally).
func fuzzyMatches(query, haystack string) bool {
	if query == "" {
		return true
	}
	return len(fuzzy.Find(query, []string{haystack})) > 0
}

// candidateHaystack is the searchable "label path" text for one candidate —
// identical shape to the old flat picker's candidateSource.String, kept as a
// free function so both the top-level and tree-expand matching paths share
// one haystack contract.
func candidateHaystack(c source.Candidate) string {
	return c.Label + " " + c.Path
}

// buildRows is the pure core of the redesigned picker: it flattens
// in.candidates in effectiveSourceOrder(in), filters each member by
// direct-or-descendant fuzzy match, expands Herdr workspace children (tabs
// then their panes) when relevant, and returns the final ordered []Row to
// render — a single flat list, source-differentiated by icon/color only
// (see rowDisplayText), with no divider/header rows at all.
//
// Ordering contract (non-negotiable, see skill task): within a source,
// candidates are kept in their ORIGINAL PROVIDER ORDER — never re-sorted by
// fuzzy score. A query only decides VISIBILITY (in vs. out), never
// re-ranks who's in. This is what keeps "stable group/parent order as query
// evolves" and "child fuzzy scores must never reorder workspace parents"
// true by construction: a child's score is never even computed for
// ordering purposes, only for the boolean visibility decision.
func buildRows(in rowBuildInput) []Row {
	var out []Row
	for _, src := range effectiveSourceOrder(in) {
		out = append(out, visibleGroupRows(in, src)...)
	}
	return out
}

// visibleGroupRows returns the visible RowCandidate (+ nested RowTab/RowPane)
// rows for one source, in original provider order, per buildRows' ordering
// contract.
func visibleGroupRows(in rowBuildInput, groupSource string) []Row {
	var out []Row
	for _, c := range in.candidates {
		if c.Source != groupSource {
			continue
		}
		row, ok := buildCandidateRow(in, c)
		if !ok {
			continue
		}
		out = append(out, row...)
	}
	return out
}

// buildCandidateRow builds the RowCandidate row for c (plus any expanded
// RowTab/RowPane children) and reports ok=false when c is not visible at
// all for the current query (matches neither itself nor any descendant).
func buildCandidateRow(in rowBuildInput, c source.Candidate) ([]Row, bool) {
	selfMatch := in.query == "" || fuzzyMatches(in.query, candidateHaystack(c))
	children, descMatch := expandedChildren(in, c)

	if in.query != "" && !selfMatch && !descMatch {
		return nil, false
	}

	match := MatchNone
	if in.query != "" {
		if selfMatch {
			match = MatchDirect
		} else {
			match = MatchDescendant
		}
	}
	row := Row{
		Kind:       RowCandidate,
		Candidate:  c,
		Depth:      0,
		Match:      match,
		ID:         rowIdentity(c),
		Expandable: c.Source == config.SourceHerdr,
		Expanded:   len(children) > 0,
	}
	return append([]Row{row}, children...), true
}

// expandedChildren returns the RowTab/RowPane rows to nest under a
// SourceHerdr candidate, and whether any descendant matched the query
// (which is also what makes the candidate itself visible when its own
// label/path did not match — the "single tab/pane match expands only that
// branch" contract). Every other candidate source returns (nil, false)
// immediately: only Herdr workspaces can have tree-expand children.
//
// A workspace's children are shown when EITHER the query is non-empty
// (matching descendants must always be reachable) OR the user has manually
// expanded it via toggleExpand at an empty query (progressive disclosure:
// an empty query never dumps every workspace's tabs/panes by default).
func expandedChildren(in rowBuildInput, c source.Candidate) ([]Row, bool) {
	if c.Source != config.SourceHerdr {
		return nil, false
	}
	wsID := c.Meta["workspace_id"]
	wc, ok := in.children[wsID]
	if !ok {
		return nil, false
	}
	expand := in.query != "" || in.expandedWorkspaces[wsID]
	if !expand {
		return nil, false
	}

	var out []Row
	descMatch := false
	for _, tc := range wc.Tabs {
		tabSelf := in.query == "" || fuzzyMatches(in.query, candidateHaystack(tc.Tab))
		var paneRows []Row
		tabDescMatch := false
		for _, p := range tc.Panes {
			paneSelf := in.query == "" || fuzzyMatches(in.query, candidateHaystack(p))
			if in.query != "" && !paneSelf {
				continue // only matching descendants are shown (non-negotiable)
			}
			paneMatch := MatchNone
			if in.query != "" {
				paneMatch = MatchDirect
				tabDescMatch = true
			}
			paneRows = append(paneRows, Row{
				Kind: RowPane, Candidate: p, Depth: 2,
				Match: paneMatch, ID: rowIdentity(p),
			})
		}
		if in.query != "" && !tabSelf && !tabDescMatch {
			continue // sibling tab with no matching pane and no self match: excluded
		}
		tabMatch := MatchNone
		if in.query != "" {
			if tabSelf {
				tabMatch = MatchDirect
			} else {
				tabMatch = MatchDescendant
			}
		}
		out = append(out, Row{
			Kind: RowTab, Candidate: tc.Tab, Depth: 1,
			Match: tabMatch, ID: rowIdentity(tc.Tab),
		})
		out = append(out, paneRows...)
		if tabSelf || tabDescMatch {
			descMatch = true
		}
	}
	return out, descMatch
}
