package source

import (
	"strings"

	"github.com/tranceh2/shep/internal/fuzzy"
)

// AliasMatchKind describes the strongest match found in a candidate's
// discrete aliases. It is deliberately separate from arbitrary Meta fields.
type AliasMatchKind int

const (
	AliasNoMatch AliasMatchKind = iota
	AliasFuzzy
	AliasPrefix
	AliasExact
)

// MatchAlias returns the strongest alias match and its fuzzy score. Aliases are
// examined independently, so one alias cannot match by joining neighboring
// declarations. The caller supplies the ranking layer for this result.
func MatchAlias(query string, aliases []string) (AliasMatchKind, int, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return AliasNoMatch, 0, false
	}
	bestKind := AliasNoMatch
	bestScore := 0
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		kind := AliasNoMatch
		score := 0
		switch {
		case strings.EqualFold(query, alias):
			kind = AliasExact
			score, _ = fuzzy.Score(query, alias)
		case containsWordOrPrefix(query, alias):
			kind = AliasPrefix
			score, _ = fuzzy.Score(query, alias)
		case fuzzy.Match(query, alias):
			kind = AliasFuzzy
			score, _ = fuzzy.Score(query, alias)
		}
		if kind > bestKind || (kind == bestKind && score > bestScore) {
			bestKind = kind
			bestScore = score
		}
	}
	if bestKind == AliasNoMatch {
		return bestKind, 0, false
	}
	// Keep exact > prefix > fuzzy inside the single user-facing alias layer.
	// The large separation is only an intra-alias tie-breaker; the caller still
	// compares the alias layer against label and path/metadata layers first.
	return bestKind, int(bestKind)*1_000_000 + bestScore, true
}

func containsWordOrPrefix(query, value string) bool {
	query = strings.ToLower(query)
	for _, word := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '/' || r == '.'
	}) {
		if strings.HasPrefix(word, query) {
			return true
		}
	}
	return false
}
