package fuzzy

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// matchBonuses returns each haystack rune's match bonus (see fillBonuses).
func matchBonuses(haystack []rune) []int {
	return fillBonuses(make([]int, len(haystack)), string(haystack))
}

// isSubsequence is the original []rune subsequence test, the oracle for
// isSubsequenceString.
func isSubsequence(query, haystack []rune) bool {
	if len(query) == 0 {
		return true
	}

	queryIndex := 0
	for _, haystackRune := range haystack {
		if sameFold(query[queryIndex], haystackRune) {
			queryIndex++
			if queryIndex == len(query) {
				return true
			}
		}
	}
	return false
}

// referenceScore is the original full-matrix implementation of Score, kept
// as the oracle proving the two-row rewrite returns identical results.
func referenceScore(query, haystack string) (int, []int) {
	needle := []rune(query)
	hay := []rune(haystack)
	if len(needle) == 0 || !isSubsequence(needle, hay) {
		return 0, nil
	}
	matrix := func() [][]int {
		out := make([][]int, len(needle))
		for i := range out {
			out[i] = make([]int, len(hay))
		}
		return out
	}
	flags := func() [][]bool {
		out := make([][]bool, len(needle))
		for i := range out {
			out[i] = make([]bool, len(hay))
		}
		return out
	}
	bonuses := matchBonuses(hay)
	dScores, mScores, mFromD, dFromD := matrix(), matrix(), flags(), flags()
	for i, nr := range needle {
		gap := PenaltyGapInner
		if i == len(needle)-1 {
			gap = PenaltyGapTrailing
		}
		prev := minimumScore
		for j, hr := range hay {
			d := minimumScore
			if strings.EqualFold(string(nr), string(hr)) {
				if i == 0 {
					d = j*PenaltyGapLeading + bonuses[j]
				} else if j > 0 {
					fromM := addPenalty(mScores[i-1][j-1], bonuses[j])
					fromD := addPenalty(dScores[i-1][j-1], BonusConsecutive)
					if fromD >= fromM {
						d = fromD
						dFromD[i][j] = true
					} else {
						d = fromM
					}
				}
			}
			dScores[i][j] = d
			g := addPenalty(prev, gap)
			if d >= g {
				mScores[i][j] = d
				mFromD[i][j] = true
			} else {
				mScores[i][j] = g
			}
			prev = mScores[i][j]
		}
	}
	idx := make([]int, len(needle))
	i, j, inM := len(needle)-1, len(hay)-1, true
	for {
		if inM && !mFromD[i][j] {
			j--
			continue
		}
		idx[i] = j
		if i == 0 {
			return mScores[len(needle)-1][len(hay)-1], idx
		}
		inM = !dFromD[i][j]
		i--
		j--
	}
}

// TestScore_MatchesReferenceImplementation compares Score and ScoreOnly with
// the original implementation over thousands of generated query/haystack
// pairs drawn from a small alphabet (so matches, ties and separators are
// frequent). Lengths vary widely, so pooled scratch memory is reused across
// larger and smaller programs.
func TestScore_MatchesReferenceImplementation(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := []string{"a", "b", "A", "B", "/", "-", "_", ".", " ", "c", "D", "é", "\xff"}
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for i := range 20000 {
		q, h := word(1+rng.IntN(4)), word(rng.IntN(24))
		if i%10 == 0 {
			q, h = word(1+rng.IntN(12)), word(rng.IntN(300))
		}
		gotScore, gotIdx := Score(q, h)
		wantScore, wantIdx := referenceScore(q, h)
		if gotScore != wantScore || !slices.Equal(gotIdx, wantIdx) {
			t.Fatalf("Score(%q, %q) = %d %v, want %d %v", q, h, gotScore, gotIdx, wantScore, wantIdx)
		}
		if only := ScoreOnly(q, h); only != wantScore {
			t.Fatalf("ScoreOnly(%q, %q) = %d, want %d", q, h, only, wantScore)
		}
	}
}

// TestMatch_MatchesRuneSliceSubsequence proves the string-decoding
// subsequence test agrees with the []rune one, invalid UTF-8 included.
func TestMatch_MatchesRuneSliceSubsequence(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 4))
	alphabet := []string{"a", "A", "b", "é", "É", "/", " ", "\xff", "ß", "k", "K"}
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 20000 {
		q, h := word(rng.IntN(4)), word(rng.IntN(16))
		if got, want := Match(q, h), isSubsequence([]rune(q), []rune(h)); got != want {
			t.Fatalf("Match(%q, %q) = %v, want %v", q, h, got, want)
		}
	}
}

// TestSameFold_MatchesEqualFold proves the allocation-free rune folding
// agrees with strings.EqualFold for every rune a []rune conversion can yield
// — all scalar values up to U+1FFFF (surrogates are not scalar values: a
// conversion turns their bytes into U+FFFD) — against fold-heavy partners.
func TestSameFold_MatchesEqualFold(t *testing.T) {
	t.Parallel()
	partners := []rune{'a', 'A', 'k', 'K', 's', 'S', 'ſ', 'K', 'é', 'É', 'ß', 'ẞ', 'σ', 'ς', 'Σ', 'İ', 'ı', 'i', 'I', utf8.RuneError}
	for r := rune(0); r <= 0x1FFFF; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		for _, p := range append(partners, r, r+1) {
			if got, want := sameFold(r, p), strings.EqualFold(string(r), string(p)); got != want {
				t.Fatalf("sameFold(%U, %U) = %v, want %v", r, p, got, want)
			}
		}
	}
}

// TestMatchCandidate_SingleTermFastPathMatchesTheGeneralForm proves the
// one-term shortcut returns exactly what merging every clause returns, for
// every term kind, negative fuzzy scores included.
func TestMatchCandidate_SingleTermFastPathMatchesTheGeneralForm(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(3, 4))
	alphabet := []string{"a", "b", "A", "/", "-", " ", "c", "é", "z"}
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	shapes := []func(string) string{
		func(w string) string { return w },
		func(w string) string { return "'" + w + "'" },
		func(w string) string { return "^" + w },
		func(w string) string { return w + "$" },
		func(w string) string { return "/" + w + "/" },
		func(w string) string { return "source:" + w },
		func(w string) string { return "path:" + w },
	}
	fields := CandidateFields{Source: "zoxide", Path: "/home/elliot/fsociety", Agent: "claude", AgentStatus: "working"}
	// A match scoring exactly zero: 160 leading-gap penalties cancel the
	// word bonus of the only matched rune.
	zero := strings.Repeat("x", 159) + " a"
	if score, _ := Score("a", zero); score != 0 {
		t.Fatalf("Score(%q, zero-score haystack) = %d, want 0", "a", score)
	}
	if score, idx, ok := ParseExtendedQuery("a").MatchCandidate(zero, fields); score != 0 || idx != nil || !ok {
		t.Fatalf("zero-score match = %d %v %v, want 0 [] true", score, idx, ok)
	}
	for range 20000 {
		raw := shapes[rng.IntN(len(shapes))](word(1 + rng.IntN(3)))
		eq := ParseExtendedQuery(raw)
		if len(eq.Clauses) != 1 || len(eq.Clauses[0].Alternatives) != 1 || eq.Clauses[0].Alternatives[0].Inverse {
			continue
		}
		haystack := word(rng.IntN(90))
		gotScore, gotIdx, gotOK := eq.MatchCandidate(haystack, fields)
		wantScore, wantIdx, wantOK := eq.matchClauses(haystack, fields)
		if gotScore != wantScore || gotOK != wantOK || !slices.Equal(gotIdx, wantIdx) || (gotIdx == nil) != (wantIdx == nil) {
			t.Fatalf("MatchCandidate(%q, %q) = %d %v %v, want %d %v %v", raw, haystack, gotScore, gotIdx, gotOK, wantScore, wantIdx, wantOK)
		}
	}
}
