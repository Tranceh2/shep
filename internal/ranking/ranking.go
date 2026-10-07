package ranking

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/source"
)

const (
	nearTieWindow  = 500
	halfLife       = 7 * 24 * time.Hour
	pathKeyVersion = "v1"
	retention      = 180 * 24 * time.Hour
	maxKeys        = 10000
	maxRecent      = 32
)

type Snapshot struct {
	enabled          bool
	exact            map[string]usage
	resource         map[string]usage
	recent           []string
	workspaceMRU     []string
	pins             map[string]struct{}
	acknowledgements map[string]string
	currentExact     string
	capturedAt       time.Time
	adaptiveDisabled bool
}

func (s Snapshot) WithCurrentExact(identity string) Snapshot {
	s.currentExact = identity
	return s
}

// WithWorkspaceMRU returns a copy of the snapshot with the ordered focus MRU workspace IDs attached.
func (s Snapshot) WithWorkspaceMRU(mru []string) Snapshot {
	out := s
	out.workspaceMRU = make([]string, len(mru))
	copy(out.workspaceMRU, mru)
	return out
}

// WithRecent returns a copy of the snapshot with recent exact launch history identities attached.
func (s Snapshot) WithRecent(identities []string) Snapshot {
	out := s
	out.recent = make([]string, len(identities))
	for i, id := range identities {
		out.recent[i] = exactStorageKey(id)
	}
	return out
}

// WorkspaceMRU returns a defensive copy of the workspace focus MRU IDs.
func (s Snapshot) WorkspaceMRU() []string {
	if len(s.workspaceMRU) == 0 {
		return nil
	}
	out := make([]string, len(s.workspaceMRU))
	copy(out, s.workspaceMRU)
	return out
}

// WithFilteredWorkspaceMRU filters the workspace focus MRU against live workspace membership.
func (s Snapshot) WithFilteredWorkspaceMRU(workspaces []source.Workspace) Snapshot {
	if len(s.workspaceMRU) == 0 || len(workspaces) == 0 {
		return s
	}
	live := make(map[string]struct{}, len(workspaces))
	for _, ws := range workspaces {
		if ws.ID != "" {
			live[ws.ID] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(s.workspaceMRU))
	for _, id := range s.workspaceMRU {
		if _, ok := live[id]; ok {
			filtered = append(filtered, id)
		}
	}
	out := s
	out.workspaceMRU = filtered
	return out
}

func (s Snapshot) Active() bool { return s.enabled && !s.adaptiveDisabled }

// CurrentExact returns the exact identity excluded from previous-target
// promotion for this immutable invocation.
func (s Snapshot) CurrentExact() string { return s.currentExact }

// HasHistory reports whether the immutable snapshot contains learned state.
func (s Snapshot) HasHistory() bool {
	return len(s.exact) > 0 || len(s.resource) > 0 || len(s.recent) > 0 || len(s.workspaceMRU) > 0 || len(s.acknowledgements) > 0
}

// IsPaneAcknowledged reports whether paneID is acknowledged for status.
func (s Snapshot) IsPaneAcknowledged(paneID, status string) bool {
	if paneID == "" || status == "" || len(s.acknowledgements) == 0 {
		return false
	}
	ackedStatus, ok := s.acknowledgements[paneID]
	if !ok {
		return false
	}
	return strings.EqualFold(ackedStatus, status)
}

// WithAcknowledgement returns a copy with an acknowledgement added/updated in memory.
func (s Snapshot) WithAcknowledgement(paneID, status string) Snapshot {
	out := s
	out.acknowledgements = make(map[string]string, len(s.acknowledgements)+1)
	for k, v := range s.acknowledgements {
		out.acknowledgements[k] = v
	}
	if paneID != "" && status != "" {
		out.acknowledgements[paneID] = strings.ToLower(status)
	}
	return out
}

// WithClearedAcknowledgement returns a copy with the acknowledgement for paneID removed in memory.
func (s Snapshot) WithClearedAcknowledgement(paneID string) Snapshot {
	if len(s.acknowledgements) == 0 {
		return s
	}
	if _, ok := s.acknowledgements[paneID]; !ok {
		return s
	}
	out := s
	out.acknowledgements = make(map[string]string, len(s.acknowledgements))
	for k, v := range s.acknowledgements {
		if k != paneID {
			out.acknowledgements[k] = v
		}
	}
	return out
}

// IsPinned reports whether candidate's stable resource or identity key is pinned.
func (s Snapshot) IsPinned(candidate source.Candidate) bool {
	if len(s.pins) == 0 {
		return false // nothing pinned: skip hashing the candidate's key
	}
	key := PinKey(candidate)
	if key == "" {
		return false
	}
	_, ok := s.pins[key]
	return ok
}

// WithAdaptiveEnabled returns a copy whose adaptive ranking state is enabled or
// disabled without changing the independently persisted pin set.
func (s Snapshot) WithAdaptiveEnabled(enabled bool) Snapshot {
	s.enabled = enabled
	s.adaptiveDisabled = !enabled
	return s
}

// WithPinned returns a copy with one pin state changed. The snapshot remains
// immutable to callers even though its internal maps are optimized for reads.
func (s Snapshot) WithPinned(key string, pinned bool) Snapshot {
	key = pinStorageKey(key)
	out := s
	out.pins = make(map[string]struct{}, len(s.pins)+1)
	for existing := range s.pins {
		out.pins[existing] = struct{}{}
	}
	if key != "" {
		if pinned {
			out.pins[key] = struct{}{}
		} else {
			delete(out.pins, key)
		}
	}
	return out
}

// UsageFor returns the snapshot's frecency score for candidate. It is zero
// when ranking is disabled or the candidate has no recorded usage.
func (s Snapshot) UsageFor(candidate source.Candidate) float64 {
	if !s.enabled || s.adaptiveDisabled {
		return 0
	}
	return s.usageFor(candidate)
}

type usage struct {
	count    int64
	lastUsed int64
}

type scored struct {
	candidate  source.Candidate
	textual    textualQuality
	openAction bool
	pinned     bool
	usage      float64
	sourceRank int
	order      int
	// The empty-query keys, computed once per candidate rather than on every
	// comparison (Identity hashes path candidates).
	current   bool
	mruRank   int
	recentIdx int
}

const (
	// LayerPathOrMeta is the lowest textual quality: a fuzzy match on the
	// candidate path or an explicitly permitted row metadata field.
	LayerPathOrMeta = 1
	// LayerAlias is a match on one of the candidate's explicit aliases.
	LayerAlias = 2
	// LayerFuzzyLabel is a fuzzy subsequence match on the candidate label.
	LayerFuzzyLabel = 3
	// LayerPrefix is a word-prefix or start-of-word match on the label.
	LayerPrefix = 4
	// LayerExact is a case-insensitive exact label match.
	LayerExact = 5
)

type textualQuality struct {
	layer int
	score int
}

func aliasQuality(query string, candidate source.Candidate) (textualQuality, bool) {
	kind, score, ok := source.MatchAlias(query, candidate.Aliases)
	if !ok {
		return textualQuality{}, false
	}
	return textualQuality{layer: LayerAlias, score: score + int(kind)}, true
}

func disabledSnapshot(currentExact string) Snapshot {
	return Snapshot{currentExact: currentExact, pins: map[string]struct{}{}, acknowledgements: map[string]string{}}
}

func exactStorageKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if isOpaqueStorageKey("exact", value) {
		return value
	}
	return pathKey("exact", value)
}

func resourceStorageKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if isOpaqueStorageKey("resource", value) {
		return value
	}
	return pathKey("resource", value)
}

func isOpaqueStorageKey(namespace, value string) bool {
	prefix := pathKeyVersion + ":" + namespace + ":"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest := strings.TrimPrefix(value, prefix)
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func Identity(candidate source.Candidate) string {
	if candidate.Meta != nil {
		for _, field := range []struct{ prefix, key string }{
			{"herdr:pane:", "pane_id"},
			{"herdr:tab:", "tab_id"},
			{"herdr:workspace:", "workspace_id"},
			{"sessions:", "session_name"},
			{"workspaces:entry:", "entry_id"},
		} {
			if value := candidate.Meta[field.key]; value != "" {
				return field.prefix + value
			}
		}
		if candidate.Meta["custom_source"] == "true" {
			if id := strings.TrimSpace(candidate.Meta["custom_source_id"]); id != "" {
				return pathKey("custom_source:"+candidate.Source, id)
			}
			return pathKey("custom_source:"+candidate.Source, candidate.Label+"\x00"+candidate.Meta["command"])
		}
	}
	path := candidate.NormalizedPath
	if path == "" {
		path = candidate.Path
	}
	if path == "" {
		return ""
	}
	return pathKey(candidate.Source, path)
}

func Resource(candidate source.Candidate) string {
	path := candidate.NormalizedPath
	if path == "" {
		path = candidate.Path
	}
	if path == "" || candidate.Source == config.SourceSessions {
		return ""
	}
	return pathKey("resource", path)
}

func pathKey(namespace, path string) string {
	digest := sha256.Sum256([]byte(namespace + "\x00" + path))
	return pathKeyVersion + ":" + namespace + ":" + hex.EncodeToString(digest[:])
}

func cloneCandidates(candidates []source.Candidate) []source.Candidate {
	out := make([]source.Candidate, len(candidates))
	for i, candidate := range candidates {
		out[i] = candidate.Clone()
	}
	return out
}

// SortBySourceOrder composes strict source blocks for an empty query. Herdr
// uses adaptive recent ordering with the focused workspace last; projects use
// adaptive ordering only inside their own block. Other listed providers retain
// provider order. Candidates whose source is not part of sourceOrder are
// appended last and keep the global adaptive ranking so they are never left in
// raw provider order. Non-empty queries continue through the global
// label-first ranker.
func SortBySourceOrder(candidates []source.Candidate, query string, sourceOrder []string, snapshot Snapshot) []source.Candidate {
	if query != "" {
		return sortWithSourceOrder(candidates, query, sourceOrder, snapshot)
	}
	bySource := make(map[string][]source.Candidate)
	for _, candidate := range candidates {
		bySource[candidate.Source] = append(bySource[candidate.Source], candidate)
	}
	out := make([]source.Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(sourceOrder))
	for _, sourceName := range sourceOrder {
		if _, duplicate := seen[sourceName]; duplicate {
			continue
		}
		seen[sourceName] = struct{}{}
		block := bySource[sourceName]
		if sourceName == config.SourceHerdr || sourceName == config.SourceProjects || sourceName == config.SourceAgents {
			block = Sort(block, "", snapshot)
		} else {
			block = pinFirst(block, snapshot)
		}
		out = append(out, block...)
	}
	var unlisted []source.Candidate
	for _, candidate := range candidates {
		if _, listed := seen[candidate.Source]; listed {
			continue
		}
		unlisted = append(unlisted, candidate)
	}
	out = append(out, Sort(unlisted, "", snapshot)...)
	return out
}

func pinFirst(candidates []source.Candidate, snapshot Snapshot) []source.Candidate {
	out := cloneCandidates(candidates)
	sort.SliceStable(out, func(i, j int) bool {
		return snapshot.IsPinned(out[i]) && !snapshot.IsPinned(out[j])
	})
	return out
}

func sortWithSourceOrder(candidates []source.Candidate, query string, sourceOrder []string, snapshot Snapshot) []source.Candidate {
	return sortCandidates(candidates, query, snapshot, func(candidate source.Candidate) int {
		for i, name := range sourceOrder {
			if name == candidate.Source {
				return i
			}
		}
		return len(sourceOrder)
	})
}

func Sort(candidates []source.Candidate, query string, snapshot Snapshot) []source.Candidate {
	return sortCandidates(candidates, query, snapshot, func(source.Candidate) int { return 0 })
}

func sortCandidates(candidates []source.Candidate, query string, snapshot Snapshot, rank func(source.Candidate) int) []source.Candidate {
	// Empty-query ordering is intentionally disabled without a ranking
	// snapshot; the caller's configured source blocks and provider order are the
	// contract in that mode. Textual query ordering remains deterministic even
	// when adaptive ranking is unavailable.
	if (!snapshot.enabled || snapshot.adaptiveDisabled) && query == "" {
		if len(snapshot.pins) > 0 {
			return pinFirst(candidates, snapshot)
		}
		return cloneCandidates(candidates)
	}
	if (!snapshot.enabled || snapshot.adaptiveDisabled) && query != "" {
		snapshot.enabled = true
	}
	scoredCandidates := make([]scored, 0, len(candidates))
	for index, candidate := range candidates {
		textual := textualQuality{}
		if query != "" {
			textual = classifyText(query, candidate)
		}
		item := scored{
			candidate:  candidate.Clone(),
			textual:    textual,
			openAction: isOpenAction(candidate),
			pinned:     snapshot.IsPinned(candidate),
			usage:      snapshot.usageFor(candidate),
			sourceRank: rank(candidate),
			order:      index,
		}
		if query == "" {
			identity := Identity(candidate)
			item.current = identity == snapshot.currentExact
			item.mruRank = snapshot.WorkspaceMRURank(candidate)
			item.recentIdx = snapshot.recentRank(identity)
		}
		scoredCandidates = append(scoredCandidates, item)

	}
	sort.SliceStable(scoredCandidates, func(i, j int) bool {
		left, right := scoredCandidates[i], scoredCandidates[j]
		if query == "" {
			if left.pinned != right.pinned {
				return left.pinned
			}
			if left.current != right.current {
				return !left.current
			}
			if !left.current && !right.current {
				if left.mruRank != right.mruRank {
					return left.mruRank < right.mruRank
				}
				if left.recentIdx != right.recentIdx {
					return left.recentIdx < right.recentIdx
				}
			}
			if left.usage != right.usage {
				return left.usage > right.usage
			}
			return left.order < right.order
		}
		if left.textual.layer != right.textual.layer {
			return left.textual.layer > right.textual.layer
		}
		if left.openAction != right.openAction {
			return left.openAction
		}
		if left.pinned != right.pinned {
			return left.pinned
		}
		if left.textual.score != right.textual.score {
			return left.textual.score > right.textual.score
		}
		if left.sourceRank != right.sourceRank {
			return left.sourceRank < right.sourceRank
		}
		if left.usage != right.usage {
			return left.usage > right.usage
		}
		return left.order < right.order
	})
	out := make([]source.Candidate, len(scoredCandidates))
	for i, item := range scoredCandidates {
		out[i] = item.candidate
	}
	return out
}

func classifyText(query string, candidate source.Candidate) textualQuality {
	label := strings.TrimSpace(candidate.Label)
	if strings.EqualFold(query, label) {
		return textualQuality{layer: LayerExact, score: textScore(query, label)}
	}
	if ContainsWordOrPrefix(query, label) {
		return textualQuality{layer: LayerPrefix, score: textScore(query, label)}
	}
	if fuzzy.Match(query, label) {
		score, _ := fuzzy.Score(query, label)
		return textualQuality{layer: LayerFuzzyLabel, score: score}
	}
	if quality, ok := aliasQuality(query, candidate); ok {
		return quality
	}
	path := candidate.NormalizedPath
	if path == "" {
		path = candidate.Path
	}
	if fuzzy.Match(query, path) {
		score, _ := fuzzy.Score(query, path)
		return textualQuality{layer: LayerPathOrMeta, score: score}
	}
	for _, value := range permittedMetadata(candidate) {
		if fuzzy.Match(query, value) {
			score, _ := fuzzy.Score(query, value)
			return textualQuality{layer: LayerPathOrMeta, score: score}
		}
	}
	return textualQuality{}
}

// permittedMetadata mirrors the existing guarded metadata search contract.
// Arbitrary custom-source Meta remains inert for discovery; aliases have their
// own typed field and are handled before this fallback.
func permittedMetadata(candidate source.Candidate) []string {
	if candidate.Meta == nil {
		return nil
	}
	values := make([]string, 0, 5)
	add := func(value string) {
		if value != "" {
			values = append(values, value)
		}
	}
	if candidate.Meta["tab_id"] != "" || candidate.Meta["pane_id"] != "" {
		add(candidate.Meta["tab_label"])
		add(candidate.Meta["pane_id"])
		add(candidate.Meta["agent_status"])
		add(candidate.Meta["tab_number"])
		return values
	}
	if candidate.Source == config.SourceSessions {
		add(candidate.Meta["session_name"])
		add(candidate.Meta["session_dir"])
		return values
	}
	if workspaceLabel := candidate.Meta["workspace_label"]; workspaceLabel != candidate.Label {
		add(workspaceLabel)
	}
	if candidate.Meta["is_worktree"] == "true" {
		add(candidate.Meta["branch"])
	}
	return values
}

func textScore(query, value string) int {
	score, _ := fuzzy.Score(query, value)
	return score
}

// ContainsWordOrPrefix reports whether query, ignoring case, prefixes one of
// label's words (runs between ' ', '-', '_', '/' and '.'). It compares
// rune by rune through unicode.ToLower — exactly what lowering both strings
// with strings.ToLower and testing strings.HasPrefix on each word does —
// without allocating: it runs for every candidate on every keystroke.
func ContainsWordOrPrefix(query, label string) bool {
	for start := 0; start < len(label); {
		r, size := utf8.DecodeRuneInString(label[start:])
		if isWordBreak(r) {
			start += size
			continue
		}
		end := start
		for end < len(label) {
			r, size := utf8.DecodeRuneInString(label[end:])
			if isWordBreak(r) {
				break
			}
			end += size
		}
		if hasLowerPrefix(label[start:end], query) {
			return true
		}
		start = end
	}
	return false
}

// isWordBreak reports the runes ContainsWordOrPrefix splits words at.
func isWordBreak(r rune) bool {
	return r == ' ' || r == '-' || r == '_' || r == '/' || r == '.'
}

// hasLowerPrefix reports whether prefix, lowered rune by rune, is a prefix of
// word, lowered the same way.
func hasLowerPrefix(word, prefix string) bool {
	for _, p := range prefix {
		w, size := utf8.DecodeRuneInString(word)
		if word == "" || unicode.ToLower(w) != unicode.ToLower(p) {
			return false
		}
		word = word[size:]
	}
	return true
}

func isOpenAction(candidate source.Candidate) bool {
	return candidate.Source == config.SourceHerdr || candidate.Meta["tab_id"] != "" || candidate.Meta["pane_id"] != ""
}

func (s Snapshot) WorkspaceMRURank(candidate source.Candidate) int {
	if candidate.Source != config.SourceHerdr || candidate.Meta == nil || len(s.workspaceMRU) == 0 {
		return len(s.workspaceMRU) + 1
	}
	wsID := candidate.Meta["workspace_id"]
	if wsID == "" {
		return len(s.workspaceMRU) + 1
	}
	for index, id := range s.workspaceMRU {
		if id == wsID {
			return index
		}
	}
	return len(s.workspaceMRU) + 1
}

func (s Snapshot) recentRank(identity string) int {
	identity = exactStorageKey(identity)
	for index, recent := range s.recent {
		if recent == identity {
			return index
		}
	}
	return len(s.recent) + 1
}

// PaneRecentRank returns the zero-based recent-selection rank of a pane ID
// (from recent_exact launch history), or len(recent)+1 when no selection history exists.
func (s Snapshot) PaneRecentRank(paneID string) int {
	if paneID == "" {
		return len(s.recent) + 1
	}
	return s.recentRank("herdr:pane:" + paneID)
}

// RecentRank returns the zero-based recent-selection rank of a candidate
// (from recent_exact launch history), or len(recent)+1 when no selection history exists.
func (s Snapshot) RecentRank(candidate source.Candidate) int {
	return s.recentRank(Identity(candidate))
}

func (s Snapshot) usageFor(candidate source.Candidate) float64 {
	if len(s.exact) == 0 && len(s.resource) == 0 {
		return 0 // no recorded usage: skip hashing the candidate's keys
	}
	now := s.capturedAt
	if now.IsZero() {
		now = time.Now()
	}
	return frecency(s.exactUsage(Identity(candidate)), now) + 0.35*frecency(s.resourceUsage(Resource(candidate)), now)
}

func (s Snapshot) exactUsage(identity string) usage {
	if value, ok := s.exact[identity]; ok {
		return value
	}
	return s.exact[exactStorageKey(identity)]
}

func (s Snapshot) resourceUsage(resource string) usage {
	if value, ok := s.resource[resource]; ok {
		return value
	}
	return s.resource[resourceStorageKey(resource)]
}

func frecency(value usage, now time.Time) float64 {
	if value.count <= 0 || value.lastUsed <= 0 {
		return 0
	}
	age := now.Sub(time.Unix(value.lastUsed, 0))
	if age < 0 {
		age = 0
	}
	return float64(value.count) * math.Exp(-age.Hours()/halfLife.Hours())
}

func CandidateKeyParts(candidate source.Candidate) (string, string) {
	return strings.TrimSpace(Identity(candidate)), strings.TrimSpace(Resource(candidate))
}
