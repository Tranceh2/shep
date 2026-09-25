package fuzzy

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// TermKind describes how a term matches haystack text or metadata.
type TermKind int

const (
	TermFuzzy TermKind = iota
	TermExact
	TermPrefix
	TermSuffix
	TermRegex
	TermField
)

// Term is one discrete matching unit parsed from a user search query.
type Term struct {
	Raw       string
	Kind      TermKind
	Inverse   bool
	Pattern   string         // for text matching
	Regex     *regexp.Regexp // compiled regex when Kind == TermRegex
	ExactFull bool           // true when anchored with both ^ and $
	Field     string         // for field matching: "status", "agent", "source", "path"
	Value     string         // for field matching
}

// Clause represents one OR-group of alternative terms.
// A candidate satisfies a Clause if at least one of its alternatives matches.
type Clause struct {
	Alternatives []Term
}

// ExtendedQuery holds the parsed terms and clauses of an extended search query.
type ExtendedQuery struct {
	Raw     string
	Clauses []Clause
	Terms   []Term // flat list of all terms across all clauses
}

// CandidateFields provides structured fields for field-based filters.
type CandidateFields struct {
	Source      string
	Path        string
	Agent       string
	AgentStatus string
}

// ParseExtendedQuery parses a query string into structured terms and clauses honoring:
// - space-separated AND
// - pipe | OR groups
// - exact 'term or "term with spaces"
// - prefix ^term (anchored to string start)
// - suffix term$ (anchored to string end)
// - exact full match ^term$
// - regex /pattern/
// - negation !term, !'term, !^term, !term$, !/pattern/, !field:val
// - Snacks-style field filters (status:, agent:, source:, path: and short aliases s:, a:, src:, p:)
func ParseExtendedQuery(raw string) ExtendedQuery {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ExtendedQuery{Raw: raw}
	}

	tokens := tokenizeExtendedQuery(trimmed)
	if len(tokens) == 0 {
		return ExtendedQuery{Raw: raw}
	}

	var clauses []Clause
	var currentClause Clause
	var allTerms []Term
	expectAlternative := false

	for _, tok := range tokens {
		if tok == "|" {
			if len(currentClause.Alternatives) > 0 {
				expectAlternative = true
			}
			continue
		}

		term := parseSingleTerm(tok)
		allTerms = append(allTerms, term)

		if expectAlternative && len(currentClause.Alternatives) > 0 {
			currentClause.Alternatives = append(currentClause.Alternatives, term)
			expectAlternative = false
		} else {
			if len(currentClause.Alternatives) > 0 {
				clauses = append(clauses, currentClause)
			}
			currentClause = Clause{Alternatives: []Term{term}}
			expectAlternative = false
		}
	}

	if len(currentClause.Alternatives) > 0 {
		clauses = append(clauses, currentClause)
	}

	return ExtendedQuery{
		Raw:     raw,
		Clauses: clauses,
		Terms:   allTerms,
	}
}

func tokenizeExtendedQuery(raw string) []string {
	var tokens []string
	var cur strings.Builder
	runes := []rune(raw)
	inQuotes := false
	var quoteChar rune

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if inQuotes {
			cur.WriteRune(r)
			if r == quoteChar {
				inQuotes = false
			}
			continue
		}

		if r == '"' {
			inQuotes = true
			quoteChar = r
			cur.WriteRune(r)
			continue
		}

		if r == '\'' && cur.Len() == 0 {
			hasClosing := false
			for j := i + 1; j < len(runes); j++ {
				if runes[j] == '\'' {
					hasClosing = true
					break
				}
			}
			if hasClosing {
				inQuotes = true
				quoteChar = '\''
				cur.WriteRune(r)
				continue
			}
		}

		if unicode.IsSpace(r) {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}

		if r == '|' {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			tokens = append(tokens, "|")
			continue
		}

		cur.WriteRune(r)
	}

	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}

	return tokens
}

func parseSingleTerm(p string) Term {
	orig := p
	inverse := false
	if strings.HasPrefix(p, "!") && len(p) > 1 {
		inverse = true
		p = p[1:]
	}

	// Double quotes: "exact phrase"
	if strings.HasPrefix(p, "\"") && strings.HasSuffix(p, "\"") && len(p) >= 2 {
		val := p[1 : len(p)-1]
		return Term{
			Raw:     orig,
			Kind:    TermExact,
			Inverse: inverse,
			Pattern: val,
		}
	}

	// Single quotes: 'exact phrase'
	if strings.HasPrefix(p, "'") && strings.HasSuffix(p, "'") && len(p) >= 2 {
		val := p[1 : len(p)-1]
		return Term{
			Raw:     orig,
			Kind:    TermExact,
			Inverse: inverse,
			Pattern: val,
		}
	}

	// Check regex: /pattern/
	if strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") && len(p) >= 2 {
		pattern := p[1 : len(p)-1]
		re, err := regexp.Compile("(?i)" + pattern)
		if err == nil {
			return Term{
				Raw:     orig,
				Kind:    TermRegex,
				Inverse: inverse,
				Pattern: pattern,
				Regex:   re,
			}
		}
		// If invalid regex, fall back to literal pattern
	}

	// Check field filters: field:value
	if colonIdx := strings.IndexByte(p, ':'); colonIdx > 0 && colonIdx < len(p)-1 {
		fieldKey := strings.ToLower(p[:colonIdx])
		fieldVal := p[colonIdx+1:]
		if (strings.HasPrefix(fieldVal, "\"") && strings.HasSuffix(fieldVal, "\"") && len(fieldVal) >= 2) ||
			(strings.HasPrefix(fieldVal, "'") && strings.HasSuffix(fieldVal, "'") && len(fieldVal) >= 2) {
			fieldVal = fieldVal[1 : len(fieldVal)-1]
		}
		var canonicalField string
		switch fieldKey {
		case "status", "s":
			canonicalField = "status"
		case "agent", "a":
			canonicalField = "agent"
		case "source", "src":
			canonicalField = "source"
		case "path", "p":
			canonicalField = "path"
		}
		if canonicalField != "" {
			return Term{
				Raw:     orig,
				Kind:    TermField,
				Inverse: inverse,
				Field:   canonicalField,
				Value:   fieldVal,
			}
		}
	}

	// Exact match: 'term
	if strings.HasPrefix(p, "'") && len(p) > 1 {
		return Term{
			Raw:     orig,
			Kind:    TermExact,
			Inverse: inverse,
			Pattern: p[1:],
		}
	}

	// Anchored match: ^term or ^term$
	if strings.HasPrefix(p, "^") && len(p) > 1 {
		pattern := p[1:]
		if strings.HasSuffix(pattern, "$") && len(pattern) > 1 {
			return Term{
				Raw:       orig,
				Kind:      TermExact,
				Inverse:   inverse,
				ExactFull: true,
				Pattern:   pattern[:len(pattern)-1],
			}
		}
		return Term{
			Raw:     orig,
			Kind:    TermPrefix,
			Inverse: inverse,
			Pattern: pattern,
		}
	}

	// Suffix match: term$
	if strings.HasSuffix(p, "$") && len(p) > 1 {
		return Term{
			Raw:     orig,
			Kind:    TermSuffix,
			Inverse: inverse,
			Pattern: p[:len(p)-1],
		}
	}

	// Standard fuzzy match
	return Term{
		Raw:     orig,
		Kind:    TermFuzzy,
		Inverse: inverse,
		Pattern: p,
	}
}

// MatchCandidate evaluates eq against haystack and optional structured fields.
func (eq ExtendedQuery) MatchCandidate(haystack string, fields CandidateFields) (int, []int, bool) {
	if len(eq.Clauses) == 0 {
		return 0, nil, true
	}

	totalScore := 0
	indexSet := make(map[int]struct{})

	for _, clause := range eq.Clauses {
		clauseMatched := false
		bestScore := 0
		var bestIdxs []int

		for _, term := range clause.Alternatives {
			matched, score, idxs := term.match(haystack, fields)
			if term.Inverse {
				if !matched {
					clauseMatched = true
					invScore := 50
					if invScore > bestScore {
						bestScore = invScore
					}
				}
			} else {
				if matched {
					clauseMatched = true
					if score > bestScore {
						bestScore = score
						bestIdxs = idxs
					}
				}
			}
		}

		if !clauseMatched {
			return 0, nil, false
		}

		totalScore += bestScore
		for _, idx := range bestIdxs {
			indexSet[idx] = struct{}{}
		}
	}

	var allIndexes []int
	if len(indexSet) > 0 {
		allIndexes = make([]int, 0, len(indexSet))
		for idx := range indexSet {
			allIndexes = append(allIndexes, idx)
		}
		sort.Ints(allIndexes)
	}

	return totalScore, allIndexes, true
}

// Match evaluates eq against haystack text only without candidate fields.
func (eq ExtendedQuery) Match(haystack string) (int, []int, bool) {
	return eq.MatchCandidate(haystack, CandidateFields{})
}

func (t Term) match(haystack string, fields CandidateFields) (bool, int, []int) {
	switch t.Kind {
	case TermField:
		return t.matchField(fields)
	case TermRegex:
		return t.matchRegex(haystack)
	case TermExact:
		return t.matchExact(haystack)
	case TermPrefix:
		return t.matchPrefix(haystack)
	case TermSuffix:
		return t.matchSuffix(haystack)
	default: // TermFuzzy
		return t.matchFuzzy(haystack)
	}
}

func (t Term) matchField(fields CandidateFields) (bool, int, []int) {
	val := strings.ToLower(t.Value)
	var target string
	switch t.Field {
	case "status":
		target = strings.ToLower(fields.AgentStatus)
	case "agent":
		target = strings.ToLower(fields.Agent)
	case "source":
		target = strings.ToLower(fields.Source)
	case "path":
		target = strings.ToLower(fields.Path)
	}
	if target == "" {
		return false, 0, nil
	}
	if strings.Contains(target, val) {
		return true, 150, nil
	}
	return false, 0, nil
}

func (t Term) matchRegex(haystack string) (bool, int, []int) {
	if t.Regex == nil {
		return false, 0, nil
	}
	loc := t.Regex.FindStringIndex(haystack)
	if loc == nil {
		return false, 0, nil
	}
	startRune := len([]rune(haystack[:loc[0]]))
	matchedRunes := len([]rune(haystack[loc[0]:loc[1]]))
	idxs := make([]int, matchedRunes)
	for i := 0; i < matchedRunes; i++ {
		idxs[i] = startRune + i
	}
	return true, 250 + matchedRunes*5, idxs
}

func (t Term) matchExact(haystack string) (bool, int, []int) {
	if t.Pattern == "" {
		return true, 0, nil
	}
	lowerHaystack := strings.ToLower(haystack)
	lowerPattern := strings.ToLower(t.Pattern)

	if t.ExactFull {
		if lowerHaystack == lowerPattern {
			patternRunes := len([]rune(t.Pattern))
			idxs := make([]int, patternRunes)
			for i := 0; i < patternRunes; i++ {
				idxs[i] = i
			}
			return true, 400 + patternRunes*10, idxs
		}
		return false, 0, nil
	}

	byteIdx := strings.Index(lowerHaystack, lowerPattern)
	if byteIdx < 0 {
		return false, 0, nil
	}
	runeStart := len([]rune(haystack[:byteIdx]))
	patternRunes := len([]rune(t.Pattern))
	idxs := make([]int, patternRunes)
	for i := 0; i < patternRunes; i++ {
		idxs[i] = runeStart + i
	}
	return true, 200 + patternRunes*10, idxs
}

func (t Term) matchPrefix(haystack string) (bool, int, []int) {
	if t.Pattern == "" {
		return true, 0, nil
	}
	lowerPattern := strings.ToLower(t.Pattern)
	lowerHaystack := strings.ToLower(haystack)

	if strings.HasPrefix(lowerHaystack, lowerPattern) {
		patternRunes := len([]rune(t.Pattern))
		idxs := make([]int, patternRunes)
		for i := 0; i < patternRunes; i++ {
			idxs[i] = i
		}
		return true, 300 + patternRunes*10, idxs
	}

	return false, 0, nil
}

func (t Term) matchSuffix(haystack string) (bool, int, []int) {
	if t.Pattern == "" {
		return true, 0, nil
	}
	lowerPattern := strings.ToLower(t.Pattern)
	lowerHaystack := strings.ToLower(haystack)

	if strings.HasSuffix(lowerHaystack, lowerPattern) {
		runes := []rune(haystack)
		patternRunes := len([]rune(t.Pattern))
		start := len(runes) - patternRunes
		idxs := make([]int, patternRunes)
		for i := 0; i < patternRunes; i++ {
			idxs[i] = start + i
		}
		return true, 280 + patternRunes*10, idxs
	}

	return false, 0, nil
}

func (t Term) matchFuzzy(haystack string) (bool, int, []int) {
	if t.Pattern == "" {
		return true, 0, nil
	}
	score, idxs := Score(t.Pattern, haystack)
	if len(idxs) == 0 {
		return false, 0, nil
	}
	return true, score, idxs
}
