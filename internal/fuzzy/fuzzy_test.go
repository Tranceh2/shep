package fuzzy

import (
	"reflect"
	"testing"
)

func TestScore_EmptyQuery(t *testing.T) {
	if !Match("", "anything") {
		t.Fatal("Match() = false for an empty query, want true")
	}

	score, matchedIndexes := Score("", "anything")
	if score != 0 {
		t.Errorf("Score() score = %d, want 0", score)
	}
	if matchedIndexes != nil {
		t.Errorf("Score() matchedIndexes = %v, want nil", matchedIndexes)
	}
}

func TestMatch_NonSubsequenceReportsFalse(t *testing.T) {
	if Match("xyz", "xay") {
		t.Fatal("Match() = true, want false for a non-subsequence")
	}

	score, matchedIndexes := Score("xyz", "xay")
	if score != 0 {
		t.Errorf("Score() score = %d, want 0 for a non-subsequence", score)
	}
	if matchedIndexes != nil {
		t.Errorf("Score() matchedIndexes = %v, want nil for a non-subsequence", matchedIndexes)
	}
}

func TestScore_ExactMatchOutranksScattered(t *testing.T) {
	contiguousScore, contiguousIndexes := Score("abc", "πabcxx")
	scatteredScore, _ := Score("abc", "πaXbXc")

	if contiguousScore <= scatteredScore {
		t.Errorf("contiguous score = %d, scattered score = %d; want contiguous > scattered", contiguousScore, scatteredScore)
	}
	if want := []int{1, 2, 3}; !reflect.DeepEqual(contiguousIndexes, want) {
		t.Errorf("contiguous indexes = %v, want %v", contiguousIndexes, want)
	}
}

func TestScore_StartOfStringOutranksMidString(t *testing.T) {
	startScore, _ := Score("abc", "abcxxx")
	midScore, _ := Score("abc", "xxxabc")

	if startScore <= midScore {
		t.Errorf("start score = %d, mid score = %d; want start > mid", startScore, midScore)
	}
}

func TestScore_SeparatorBonuses(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		boundary string
		plain    string
	}{
		{name: "slash", query: "x", boundary: "/x", plain: "ax"},
		{name: "word", query: "x", boundary: "_x", plain: "ax"},
		{name: "camel case", query: "x", boundary: "aX", plain: "ax"},
		{name: "dot", query: "x", boundary: ".x", plain: "ax"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			boundaryScore, _ := Score(tt.query, tt.boundary)
			plainScore, _ := Score(tt.query, tt.plain)
			if boundaryScore <= plainScore {
				t.Errorf("boundary score = %d, plain score = %d; want boundary > plain", boundaryScore, plainScore)
			}
		})
	}
}

func TestScore_GapPenaltySeparatesTightFromSparse(t *testing.T) {
	tightScore, _ := Score("abc", "abcxxx")
	sparseScore, _ := Score("abc", "aXXbXXc")

	if tightScore <= sparseScore {
		t.Errorf("tight score = %d, sparse score = %d; want tight > sparse", tightScore, sparseScore)
	}
}

func TestMatch_CaseInsensitiveUnicode(t *testing.T) {
	if !Match("OmP", "~/.config/omp") {
		t.Fatal("Match() = false, want true for a mixed-case Unicode subsequence")
	}
}

// pathScore is an INDEPENDENT reference scorer for the brute-force checks
// below, derived by hand from the Score/traceback recurrence rather than
// sharing any code path with it (other than the exported bonus constants and
// the unexported matchBonuses helper): leading gap chars cost
// PenaltyGapLeading each, a consecutive continuation costs BonusConsecutive
// instead of a fresh match bonus, an inner gap costs its match bonus plus
// PenaltyGapInner per skipped char, and the tail after the last match costs
// PenaltyGapTrailing per char. See fuzzy.go's Score for the DP this mirrors.
func pathScore(haystack []rune, positions []int) int {
	bonuses := matchBonuses(haystack)
	score := positions[0]*PenaltyGapLeading + bonuses[positions[0]]
	for i := 1; i < len(positions); i++ {
		gap := positions[i] - positions[i-1] - 1
		if gap == 0 {
			score += BonusConsecutive
			continue
		}
		score += bonuses[positions[i]] + gap*PenaltyGapInner
	}
	last := positions[len(positions)-1]
	score += (len(haystack) - 1 - last) * PenaltyGapTrailing
	return score
}

// bruteForceBestScore enumerates every valid increasing subsequence position
// set for needle in haystack under sameFold and returns the maximum
// pathScore achievable — an independent ground truth to check Score()'s
// DP+traceback against.
func bruteForceBestScore(needle, haystack []rune) int {
	best := minimumScore
	positions := make([]int, len(needle))
	var rec func(needleIdx, fromHaystackIdx int)
	rec = func(needleIdx, fromHaystackIdx int) {
		if needleIdx == len(needle) {
			if s := pathScore(haystack, positions); s > best {
				best = s
			}
			return
		}
		for j := fromHaystackIdx; j < len(haystack); j++ {
			if sameFold(needle[needleIdx], haystack[j]) {
				positions[needleIdx] = j
				rec(needleIdx+1, j+1)
			}
		}
	}
	rec(0, 0)
	return best
}

// TestScore_MatchesBruteForceOptimum proves the DP/traceback never picks an
// incorrect, non-optimal subsequence (which would color isolated letters
// scattered across a row's text): for every case here — including haystacks
// shaped like a picker's real "label path" search text, where the query can
// be satisfied either by a contiguous run or by scattered word-boundary
// letters — Score's returned score exactly matches an independently
// brute-forced optimum, AND the returned indexes are themselves a valid
// increasing subsequence that achieves that optimum under the same
// independent formula. Choosing between a label match and a duplicate path
// occurrence belongs to internal/tui's label/path haystack handling, not to
// this package's scoring/traceback.
func TestScore_MatchesBruteForceOptimum(t *testing.T) {
	for _, tt := range []struct {
		name            string
		query, haystack string
	}{
		{name: "contiguous beats scattered", query: "abc", haystack: "πabcxx"},
		{name: "word-boundary letters can outscore a mid-word contiguous run", query: "omp", haystack: "top of my papers, comp"},
		{name: "label+path shaped, path duplicates the label", query: "omp", haystack: "components /home/dev/components"},
		{name: "label+path shaped, match spans the boundary", query: "omp", haystack: "om /p/something"},
		{name: "unicode combining with camelCase/slash bonuses", query: "gcm", haystack: "git-Commit /make"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			needle := []rune(tt.query)
			haystack := []rune(tt.haystack)
			want := bruteForceBestScore(needle, haystack)
			gotScore, gotIndexes := Score(tt.query, tt.haystack)
			if gotScore != want {
				t.Fatalf("Score(%q, %q) score = %d, want brute-force optimum %d", tt.query, tt.haystack, gotScore, want)
			}
			if len(gotIndexes) != len(needle) {
				t.Fatalf("matchedIndexes = %v, want %d entries (one per query rune)", gotIndexes, len(needle))
			}
			for i := 1; i < len(gotIndexes); i++ {
				if gotIndexes[i] <= gotIndexes[i-1] {
					t.Fatalf("matchedIndexes = %v, want strictly increasing positions", gotIndexes)
				}
			}
			if s := pathScore(haystack, gotIndexes); s != gotScore {
				t.Errorf("Score()'s own returned indexes %v score %d under the independent formula, want its own reported score %d", gotIndexes, s, gotScore)
			}
		})
	}
}

func TestScore_AccentedCodepointDoesNotSatisfyPlainASCII(t *testing.T) {
	if Match("cafe", "Café") {
		t.Fatal("Match() = true, want false without Unicode normalization")
	}

	score, matchedIndexes := Score("cafe", "Café")
	if score != 0 {
		t.Errorf("Score() score = %d, want 0 without Unicode normalization", score)
	}
	if matchedIndexes != nil {
		t.Errorf("Score() matchedIndexes = %v, want nil without Unicode normalization", matchedIndexes)
	}
}

// Scoring runs for every candidate on every keystroke: in steady state it
// allocates only the returned match indexes, and ScoreOnly nothing.
func TestScore_AllocatesOnlyTheMatchIndexes(t *testing.T) {
	query, haystack := "shep", "~/Proyectos/fsociety/shep-whiterose"
	Score(query, haystack) // warm the scratch pool
	if allocs := testing.AllocsPerRun(200, func() { Score(query, haystack) }); allocs > 1 {
		t.Errorf("Score allocates %.1f times per call, want only its indexes", allocs)
	}
	if allocs := testing.AllocsPerRun(200, func() { ScoreOnly(query, haystack) }); allocs > 0 {
		t.Errorf("ScoreOnly allocates %.1f times per call, want none", allocs)
	}
}
