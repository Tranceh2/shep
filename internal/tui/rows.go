package tui

import (
	"sort"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
)

// defaultSourceOrder mirrors config.defaultSourceOrder (unexported in
// package config), used when the caller supplies no explicit source order —
// same fallback config.Load()/source.Registry.Enabled() already apply when
// general.sources is empty/absent. Kept as the exported config.Source*
// constants rather than reaching into the unexported config var.
var defaultSourceOrder = []string{config.SourceHerdr, config.SourceWorkspaces, config.SourceZoxide, config.SourceProjects}

// tabChildren is one synthesized tab row plus its own synthesized pane rows,
// both built as source.Candidate by synthesizeWorkspaceChildren (internal/
// tui/tree.go) and tagged RowActionFocusTab when flattened into Rows by
// expandedChildren below.
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
// for expand/collapse state lookups. It derives the key from the candidate's
// Meta ids (pane_id, then tab_id, then workspace_id) for synthesized tree
// children, falling back to a source+path key for a normal provider
// candidate — so it no longer depends on the removed SourceHerdrTab /
// SourceHerdrPane string constants (a child candidate's identity is its own
// most-specific id, not a fake Source value).
func rowIdentity(c source.Candidate) string {
	if id := c.Meta["pane_id"]; id != "" {
		return "pane:" + id
	}
	if id := c.Meta["tab_id"]; id != "" {
		return "tab:" + id
	}
	if id := c.Meta["workspace_id"]; id != "" {
		return "ws:" + id
	}
	if name := c.Meta["session_name"]; name != "" {
		return "session:" + name
	}
	key := c.NormalizedPath
	if key == "" {
		key = c.Path
	}
	return c.Source + ":" + key
}

// fuzzyMatch returns the score and codepoint indexes for a matching query.
func fuzzyMatch(query, haystack string) (int, []int, bool) {
	if query == "" {
		return 0, nil, true
	}
	if !fuzzy.Match(query, haystack) {
		return 0, nil, false
	}
	score, matchedIndexes := fuzzy.Score(query, haystack)
	return score, matchedIndexes, true
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
// An empty query preserves provider order. A non-empty query score-sorts
// visible candidate groups, preserving their tree context, by score descending.
// Score ties retain configured source order and original provider order.
func buildRows(in rowBuildInput) []Row {
	var groups []scoredRowGroup
	for _, src := range effectiveSourceOrder(in) {
		groups = append(groups, visibleGroupRows(in, src)...)
	}
	sortScoredRowGroups(groups, in.query)

	var out []Row
	for _, group := range groups {
		out = append(out, group.rows...)
	}
	return out
}

// scoredRowGroup keeps a structural row group intact while it is sorted. A
// candidate group is a workspace parent plus its child rows; a tab group is a
// tab plus its matching pane rows.
type scoredRowGroup struct {
	rows  []Row
	score int
}

// sortScoredRowGroups applies fuzzy ranking only for non-empty queries.
func sortScoredRowGroups(groups []scoredRowGroup, query string) {
	if query == "" {
		return
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].score > groups[j].score
	})
}

// visibleGroupRows returns visible candidate row groups for one source in
// original provider order.
func visibleGroupRows(in rowBuildInput, groupSource string) []scoredRowGroup {
	var out []scoredRowGroup
	for _, c := range in.candidates {
		if c.Source != groupSource {
			continue
		}
		rows, score, ok := buildCandidateRow(in, c)
		if !ok {
			continue
		}
		out = append(out, scoredRowGroup{rows: rows, score: score})
	}
	return out
}

// buildCandidateRow builds the RowCandidate row for c (plus any expanded
// RowTab/RowPane children) and reports ok=false when c is not visible at
// all for the current query (matches neither itself nor any descendant).
func buildCandidateRow(in rowBuildInput, c source.Candidate) ([]Row, int, bool) {
	selfScore, selfMatchedIndexes, selfMatch := fuzzyMatch(in.query, candidateHaystack(c))
	children, descMatch, descScore := expandedChildren(in, c)

	if in.query != "" && !selfMatch && !descMatch {
		return nil, 0, false
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
		Kind:           RowCandidate,
		Candidate:      c,
		Depth:          0,
		Match:          match,
		MatchedIndexes: selfMatchedIndexes,
		ID:             rowIdentity(c),
		Expandable:     c.Source == config.SourceHerdr,
		Expanded:       len(children) > 0,
	}
	if descScore > selfScore {
		selfScore = descScore
	}
	return append([]Row{row}, children...), selfScore, true
}

// expandedChildren returns the RowTab/RowPane rows to nest under a
// SourceHerdr candidate, and whether any descendant matched the query
// (which is also what makes the candidate itself visible when its own
// label/path did not match — the "single tab/pane match expands only that
// branch" contract). Every other candidate source returns (nil, false, 0)
// immediately: only Herdr workspaces can have tree-expand children.
//
// A workspace's children are shown when EITHER the query is non-empty
// (matching descendants must always be reachable) OR the user has manually
// expanded it via toggleExpand at an empty query (progressive disclosure:
// an empty query never dumps every workspace's tabs/panes by default).
func expandedChildren(in rowBuildInput, c source.Candidate) ([]Row, bool, int) {
	if c.Source != config.SourceHerdr {
		return nil, false, 0
	}
	wsID := c.Meta["workspace_id"]
	wc, ok := in.children[wsID]
	if !ok {
		return nil, false, 0
	}
	expand := in.query != "" || in.expandedWorkspaces[wsID]
	if !expand {
		return nil, false, 0
	}

	var tabGroups []scoredRowGroup
	descMatch := false
	descScore := 0
	for _, tc := range wc.Tabs {
		tabScore, tabMatchedIndexes, tabSelf := fuzzyMatch(in.query, candidateHaystack(tc.Tab))
		var paneGroups []scoredRowGroup
		tabDescMatch := false
		for _, p := range tc.Panes {
			paneScore, paneMatchedIndexes, paneSelf := fuzzyMatch(in.query, candidateHaystack(p))
			if in.query != "" && !paneSelf {
				continue // only matching descendants are shown (non-negotiable)
			}
			paneMatch := MatchNone
			if in.query != "" {
				paneMatch = MatchDirect
				tabDescMatch = true
			}
			paneGroups = append(paneGroups, scoredRowGroup{
				rows: []Row{{
					Kind: RowPane, Candidate: p, Depth: 2,
					Match: paneMatch, MatchedIndexes: paneMatchedIndexes,
					ID: rowIdentity(p), Action: RowActionFocusTab,
				}},
				score: paneScore,
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
		sortScoredRowGroups(paneGroups, in.query)
		if len(paneGroups) > 0 {
			paneGroups[len(paneGroups)-1].rows[0].IsLast = true
		}
		var rows []Row
		rows = append(rows, Row{
			Kind: RowTab, Candidate: tc.Tab, Depth: 1,
			Match: tabMatch, MatchedIndexes: tabMatchedIndexes,
			ID: rowIdentity(tc.Tab), Action: RowActionFocusTab,
		})
		for _, paneGroup := range paneGroups {
			rows = append(rows, paneGroup.rows...)
			if paneGroup.score > tabScore {
				tabScore = paneGroup.score
			}
		}
		tabGroups = append(tabGroups, scoredRowGroup{rows: rows, score: tabScore})
		if tabSelf || tabDescMatch {
			descMatch = true
			if tabScore > descScore {
				descScore = tabScore
			}
		}
	}
	sortScoredRowGroups(tabGroups, in.query)
	var out []Row
	for i := range tabGroups {
		tabGroup := &tabGroups[i]
		tabIsLast := i == len(tabGroups)-1
		tabGroup.rows[0].IsLast = tabIsLast
		for paneIndex := 1; paneIndex < len(tabGroup.rows); paneIndex++ {
			tabGroup.rows[paneIndex].AncestorIsLast = tabIsLast
		}
		out = append(out, tabGroup.rows...)
	}
	return out, descMatch, descScore
}
