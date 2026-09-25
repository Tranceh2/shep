package tui

import (
	"sort"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/ranking"
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
	sourceOrder     []string
	rankingSnapshot ranking.Snapshot
	ranked          bool
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

func candidateFields(c source.Candidate) fuzzy.CandidateFields {
	agent := ""
	status := ""
	if c.Meta != nil {
		agent = c.Meta["agent"]
		status = c.Meta["agent_status"]
	}
	path := c.NormalizedPath
	if path == "" {
		path = c.Path
	}
	return fuzzy.CandidateFields{
		Source:      c.Source,
		Path:        path,
		Agent:       agent,
		AgentStatus: status,
	}
}

func extendedMatch(query string, c source.Candidate, target string) (int, []int, bool) {
	if query == "" {
		return 0, nil, true
	}
	eq := fuzzy.ParseExtendedQuery(query)
	score, indexes, matched := eq.MatchCandidate(target, candidateFields(c))
	return score, indexes, matched
}

// candidateHaystack is the searchable "label path" text for one candidate.
// It remains the rendered highlight domain, while matchRow evaluates the label
// and path separately so their textual quality layers stay distinguishable.
func candidateHaystack(c source.Candidate) string {
	return c.Label + " " + c.Path
}

// candidateMetadataHaystack is the FALLBACK search text for one row: a guarded
// projection over only the metadata keys Herdr already emits for that row kind
// (see synthesizeWorkspaceChildren and SessionCandidates). It is consulted by
// matchRow only when the original Label+Path domain does not match, so it can
// add rows but never reorder original-domain matches. Missing/empty values
// contribute nothing; the forbidden task/name keys are never read.
func candidateMetadataHaystack(c source.Candidate, kind RowKind) string {
	var fields []string
	add := func(v string) {
		if v != "" {
			fields = append(fields, v)
		}
	}
	switch kind {
	case RowPane:
		add(c.Meta["tab_label"])
		add(c.Meta["pane_id"])
		add(c.Meta["agent_status"])
		add(c.Meta["agent"])
		add(c.Meta["terminal_title"])
	case RowTab:
		add(c.Meta["tab_label"])
		add(c.Meta["tab_number"])
	default: // RowCandidate
		switch c.Source {
		case config.SourceSessions:
			add(c.Meta["session_name"])
			add(c.Meta["session_dir"])
		default:
			// workspace-source rows add workspace_label only when it differs
			// from the already-searched Label.
			if wl := c.Meta["workspace_label"]; wl != "" && wl != c.Label {
				add(wl)
			}
			if c.Meta["is_worktree"] == "true" {
				add(c.Meta["branch"])
			}
		}
	}
	return strings.Join(fields, " ")
}

// matchRow scores one row against the query, preferring the original
// Label+Path domain and falling back to the guarded metadata projection ONLY
// when the original domain fails. It returns the fuzzy score, the label/path
// highlight indexes (populated only for an original-domain match; a
// metadata-only match carries none), whether the row matched at all, and
// whether the match came from the original domain (provenance). Provenance is
// used at score AGGREGATION time (see aggregateScore / expandedChildren) so a
// metadata-only match never inflates an original-domain group's aggregate
// score; it does not impose a global ordering tier.
func matchRow(query string, c source.Candidate, kind RowKind) (score int, indexes []int, matched bool, original bool) {
	if query == "" {
		return 0, nil, true, true
	}
	haystack := candidateHaystack(c)
	if s, idx, ok := extendedMatch(query, c, haystack); ok {
		return s, idx, true, true
	}
	if _, s, ok := source.MatchAlias(query, c.Aliases); ok {
		return s, nil, true, false
	}
	meta := candidateMetadataHaystack(c, kind)
	if meta == "" {
		return 0, nil, false, false
	}
	if s, _, ok := extendedMatch(query, c, meta); ok {
		return s, nil, true, false
	}
	return 0, nil, false, false
}

// buildRows is the pure core of the redesigned picker: it flattens
// in.candidates in effectiveSourceOrder(in), filters each member by
// direct-or-descendant fuzzy match, expands Herdr workspace children (tabs
// then their panes) when relevant, and returns the final ordered []Row to
// render — a single flat list, source-differentiated by icon/color only
// (see rowDisplayText), with no divider/header rows at all.
//
// An empty query uses the caller's pre-ranked candidate order. A non-empty query
// score-sorts visible candidate groups, preserving their tree context, by score
// descending. Score ties retain configured source order and original provider order.
func buildRows(in rowBuildInput) []Row {
	if in.ranked {
		var out []Row
		for _, candidate := range in.candidates {
			rows, _, ok, _ := buildCandidateRow(in, candidate)
			if ok {
				out = append(out, rows...)
			}
		}
		return out
	}
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
// tab plus its matching pane rows. original records match provenance: true
// when the group matches Q on the original Label+Path domain (directly or via
// a descendant original match), false for a group matched only via the
// metadata fallback. Provenance is consumed only by score AGGREGATION (a
// metadata-only descendant never inflates an original-domain group's score);
// sortScoredRowGroups itself sorts purely by score, so metadata groups
// interleave by score while the original-match subset keeps its relative order.
type scoredRowGroup struct {
	rows       []Row
	layer      int
	isOpen     bool
	score      int
	sourceRank int
	pinned     bool
	usage      float64
	original   bool
	order      int
}

// sortScoredRowGroups applies the non-empty query contract:
// textual layer -> open Herdr action -> fuzzy score -> configured source
// order -> usage. Stable sorting preserves provider order for a complete tie.
func sortScoredRowGroups(groups []scoredRowGroup, query string) {
	if query == "" {
		return
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, right := groups[i], groups[j]
		if left.layer != right.layer {
			return left.layer > right.layer
		}
		if left.isOpen != right.isOpen {
			return left.isOpen
		}
		if left.pinned != right.pinned {
			return left.pinned
		}
		if left.score != right.score {
			return left.score > right.score
		}
		if left.sourceRank != right.sourceRank {
			return left.sourceRank < right.sourceRank
		}
		if left.usage != right.usage {
			return left.usage > right.usage
		}
		return left.order < right.order
	})
}

func sourceRank(order []string, sourceName string) int {
	for i, name := range order {
		if name == sourceName {
			return i
		}
	}
	return len(order)
}

func candidateLayer(query string, c source.Candidate, kind RowKind) int {
	if query == "" {
		return 0
	}
	eq := fuzzy.ParseExtendedQuery(query)
	isExt := len(eq.Clauses) > 1 || (len(eq.Clauses) == 1 && len(eq.Clauses[0].Alternatives) > 1) ||
		len(eq.Terms) > 1 || (len(eq.Terms) == 1 && (eq.Terms[0].Kind != fuzzy.TermFuzzy || eq.Terms[0].Inverse))

	label := strings.TrimSpace(c.Label)
	path := c.Path
	if c.NormalizedPath != "" {
		path = c.NormalizedPath
	}

	if isExt {
		fields := candidateFields(c)
		if _, _, ok := eq.MatchCandidate(label, fields); ok {
			return ranking.LayerFuzzyLabel
		}
		if _, _, ok := eq.MatchCandidate(path, fields); ok {
			return ranking.LayerPathOrMeta
		}
		if _, _, ok := eq.MatchCandidate(candidateMetadataHaystack(c, kind), fields); ok {
			return ranking.LayerPathOrMeta
		}
		return 0
	}

	if strings.EqualFold(query, label) {
		return ranking.LayerExact
	}
	if label != "" && ranking.ContainsWordOrPrefix(query, label) {
		return ranking.LayerPrefix
	}
	if fuzzy.Match(query, label) {
		return ranking.LayerFuzzyLabel
	}
	if _, _, ok := source.MatchAlias(query, c.Aliases); ok {
		return ranking.LayerAlias
	}
	if fuzzy.Match(query, path) || fuzzy.Match(query, candidateMetadataHaystack(c, kind)) {
		return ranking.LayerPathOrMeta
	}
	return 0
}

func groupLayer(query string, rows []Row) int {
	layer := 0
	for _, row := range rows {
		if row.Match == MatchNone {
			continue
		}
		rowLayer := candidateLayer(query, row.Candidate, row.Kind)
		if rowLayer > layer {
			layer = rowLayer
		}
	}
	return layer
}

// visibleGroupRows returns visible candidate row groups for one source in
// original provider order.
func visibleGroupRows(in rowBuildInput, groupSource string) []scoredRowGroup {
	var out []scoredRowGroup
	for order, c := range in.candidates {
		if c.Source != groupSource {
			continue
		}
		rows, score, ok, original := buildCandidateRow(in, c)
		if !ok {
			continue
		}
		layer := groupLayer(in.query, rows)
		if layer == 0 && original {
			layer = ranking.LayerPathOrMeta
		}
		out = append(out, scoredRowGroup{
			rows: rows, layer: layer, isOpen: c.Source == config.SourceHerdr,
			score: score, sourceRank: sourceRank(effectiveSourceOrder(in), c.Source),
			pinned: in.rankingSnapshot.IsPinned(c),
			usage:  in.rankingSnapshot.UsageFor(c), original: original, order: order,
		})
	}
	return out
}

// buildCandidateRow builds the RowCandidate row for c (plus any expanded
// RowTab/RowPane children) and reports ok=false when c is not visible at
// all for the current query (matches neither itself nor any descendant). The
// returned original flag records whether the group matched Q on the original
// Label+Path domain (self or any original-domain descendant); a group visible
// only via the metadata fallback returns original=false so it can be shown
// but never reorders an original-domain group.
func buildCandidateRow(in rowBuildInput, c source.Candidate) ([]Row, int, bool, bool) {
	selfScore, selfMatchedIndexes, selfMatch, selfOriginal := matchRow(in.query, c, RowCandidate)
	children, descMatch, descScore, descOriginal := expandedChildren(in, c)

	if in.query != "" && !selfMatch && !descMatch {
		return nil, 0, false, false
	}

	// A metadata-only self match carries no highlight indexes and must not
	// mark the row MatchDirect with label/path highlights it does not have.
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
	// Provenance: the group is original-domain if self or any descendant
	// matched the original domain. Score aggregation only counts a source of
	// the SAME provenance the group ultimately carries, so a high-scoring
	// metadata-only descendant never inflates an original-domain group's rank.
	groupOriginal := (selfMatch && selfOriginal) || descOriginal
	score := aggregateScore(groupOriginal, selfScore, selfMatch, selfOriginal, descScore, descMatch, descOriginal)
	return append([]Row{row}, children...), score, true, groupOriginal
}

// aggregateScore folds a self score and a best-descendant score into one group
// score, honoring provenance: when the group is original-domain, only
// original-domain contributions count; when it is metadata-only, the
// metadata contributions count. This is what lets metadata rows be inserted
// under stable score sorting without ever reordering original-domain matches.
func aggregateScore(groupOriginal bool, selfScore int, selfMatch, selfOriginal bool, descScore int, descMatch, descOriginal bool) int {
	best := 0
	consider := func(match, original bool, score int) {
		if !match {
			return
		}
		if original != groupOriginal {
			return
		}
		if score > best {
			best = score
		}
	}
	consider(selfMatch, selfOriginal, selfScore)
	consider(descMatch, descOriginal, descScore)
	return best
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
func expandedChildren(in rowBuildInput, c source.Candidate) ([]Row, bool, int, bool) {
	if c.Source != config.SourceHerdr {
		return nil, false, 0, false
	}
	wsID := c.Meta["workspace_id"]
	wc, ok := in.children[wsID]
	if !ok {
		return nil, false, 0, false
	}
	expand := in.query != "" || in.expandedWorkspaces[wsID]
	if !expand {
		return nil, false, 0, false
	}

	var tabGroups []scoredRowGroup
	descMatch := false
	descScore := 0
	descOriginal := false
	for _, tc := range wc.Tabs {
		tabScore, tabMatchedIndexes, tabSelf, tabOriginal := matchRow(in.query, tc.Tab, RowTab)
		var paneGroups []scoredRowGroup
		tabDescMatch := false
		tabDescOriginal := false
		for _, p := range tc.Panes {
			paneScore, paneMatchedIndexes, paneSelf, paneOriginal := matchRow(in.query, p, RowPane)
			if in.query != "" && !paneSelf {
				continue // only matching descendants are shown (non-negotiable)
			}
			paneMatch := MatchNone
			if in.query != "" {
				paneMatch = MatchDirect
				tabDescMatch = true
				if paneOriginal {
					tabDescOriginal = true
				}
			}
			paneRows := []Row{{
				Kind: RowPane, Candidate: p, Depth: 2,
				Match: paneMatch, MatchedIndexes: paneMatchedIndexes,
				ID: rowIdentity(p), Action: RowActionFocusTab,
			}}
			paneGroups = append(paneGroups, scoredRowGroup{
				rows: paneRows, layer: groupLayer(in.query, paneRows), isOpen: true,
				score: paneScore, sourceRank: 0, usage: in.rankingSnapshot.UsageFor(p),
				original: in.query == "" || paneOriginal,
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
		// The tab group's provenance is original if the tab itself matched the
		// original domain OR any of its panes did; its aggregate score counts
		// only same-provenance contributions so a metadata-only pane never
		// inflates an original-domain tab's rank.
		tabGroupOriginal := (tabSelf && tabOriginal) || tabDescOriginal
		aggScore := 0
		if tabSelf && tabOriginal == tabGroupOriginal && tabScore > aggScore {
			aggScore = tabScore
		}
		for _, paneGroup := range paneGroups {
			rows = append(rows, paneGroup.rows...)
			if paneGroup.original == tabGroupOriginal && paneGroup.score > aggScore {
				aggScore = paneGroup.score
			}
		}
		tabGroups = append(tabGroups, scoredRowGroup{
			rows: rows, layer: groupLayer(in.query, rows), isOpen: true,
			score: aggScore, sourceRank: 0, usage: in.rankingSnapshot.UsageFor(tc.Tab),
			original: tabGroupOriginal,
		})
		if tabSelf || tabDescMatch {
			descMatch = true
			if tabGroupOriginal {
				descOriginal = true
			}
		}
	}
	// Propagate the best same-provenance tab score up as the workspace's
	// descendant score, after provenance is known.
	wsOriginal := descOriginal
	for i := range tabGroups {
		if tabGroups[i].original == wsOriginal && tabGroups[i].score > descScore {
			descScore = tabGroups[i].score
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
	return out, descMatch, descScore, descOriginal
}
