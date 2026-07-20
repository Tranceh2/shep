// Package fuzzy provides a Unicode-aware fzy-style subsequence matcher.
package fuzzy

import (
	"strings"
	"unicode"
)

// Bonus and penalty constants are jhawthorn/fzy's bonus.h ratios, int-scaled
// by 1000. They are kept textual rather than retuned.
const (
	BonusConsecutive   = 1000 // contiguous match continuation
	BonusSlash         = 900  // match follows '/'
	BonusWord          = 800  // match follows a word separator or string start
	BonusCamelCase     = 700  // lowercase-to-uppercase transition
	BonusDot           = 600  // match follows '.'
	PenaltyGapLeading  = -5   // unmatched runes before the first match
	PenaltyGapTrailing = -5   // unmatched runes after the last match
	PenaltyGapInner    = -10  // unmatched runes between two matches
)

const minimumScore = -1 << 30

// Match reports whether query is a Unicode-codepoint subsequence of haystack
// under simple case folding. It compares each rune with strings.EqualFold, so
// it follows Go's Unicode case-folding semantics without NFC/NFD normalization.
// An empty query always matches.
func Match(query, haystack string) bool {
	return isSubsequence([]rune(query), []rune(haystack))
}

// Score computes the fzy-style affine-gap score for query in haystack and the
// codepoint indexes of the optimal match path in haystack. Empty and
// non-subsequence queries return (0, nil). Callers that pass "Label Path" as
// haystack can receive indexes in Path; those positions may have no rendered
// Label rune to highlight.
func Score(query, haystack string) (score int, matchedIndexes []int) {
	needle := []rune(query)
	haystackRunes := []rune(haystack)
	if len(needle) == 0 || !isSubsequence(needle, haystackRunes) {
		return 0, nil
	}

	bonuses := matchBonuses(haystackRunes)
	dScores := makeScoreMatrix(len(needle), len(haystackRunes))
	mScores := makeScoreMatrix(len(needle), len(haystackRunes))
	mFromD := makeBoolMatrix(len(needle), len(haystackRunes))
	dFromD := makeBoolMatrix(len(needle), len(haystackRunes))

	for needleIndex, needleRune := range needle {
		gapPenalty := PenaltyGapInner
		if needleIndex == len(needle)-1 {
			gapPenalty = PenaltyGapTrailing
		}

		previousScore := minimumScore
		for haystackIndex, haystackRune := range haystackRunes {
			dScore := minimumScore
			if sameFold(needleRune, haystackRune) {
				if needleIndex == 0 {
					dScore = haystackIndex*PenaltyGapLeading + bonuses[haystackIndex]
				} else if haystackIndex > 0 {
					fromM := addPenalty(mScores[needleIndex-1][haystackIndex-1], bonuses[haystackIndex])
					fromD := addPenalty(dScores[needleIndex-1][haystackIndex-1], BonusConsecutive)
					if fromD >= fromM {
						dScore = fromD
						dFromD[needleIndex][haystackIndex] = true
					} else {
						dScore = fromM
					}
				}
			}

			dScores[needleIndex][haystackIndex] = dScore
			gapScore := addPenalty(previousScore, gapPenalty)
			if dScore >= gapScore {
				mScores[needleIndex][haystackIndex] = dScore
				mFromD[needleIndex][haystackIndex] = true
			} else {
				mScores[needleIndex][haystackIndex] = gapScore
			}
			previousScore = mScores[needleIndex][haystackIndex]
		}
	}

	return mScores[len(needle)-1][len(haystackRunes)-1], traceback(mFromD, dFromD, len(needle), len(haystackRunes))
}

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

func sameFold(a, b rune) bool {
	return strings.EqualFold(string(a), string(b))
}

func matchBonuses(haystack []rune) []int {
	bonuses := make([]int, len(haystack))
	for index, current := range haystack {
		switch {
		case index == 0:
			bonuses[index] = BonusWord
		case haystack[index-1] == '/':
			bonuses[index] = BonusSlash
		case isWordSeparator(haystack[index-1]):
			bonuses[index] = BonusWord
		case haystack[index-1] == '.':
			bonuses[index] = BonusDot
		case unicode.IsLower(haystack[index-1]) && unicode.IsUpper(current):
			bonuses[index] = BonusCamelCase
		}
	}
	return bonuses
}

func isWordSeparator(r rune) bool {
	return r == '-' || r == '_' || r == ' '
}

func makeScoreMatrix(rows, columns int) [][]int {
	matrix := make([][]int, rows)
	for row := range matrix {
		matrix[row] = make([]int, columns)
	}
	return matrix
}

func makeBoolMatrix(rows, columns int) [][]bool {
	matrix := make([][]bool, rows)
	for row := range matrix {
		matrix[row] = make([]bool, columns)
	}
	return matrix
}

func addPenalty(score, penalty int) int {
	if score == minimumScore {
		return minimumScore
	}
	return score + penalty
}

func traceback(mFromD, dFromD [][]bool, needleLength, haystackLength int) []int {
	matchedIndexes := make([]int, needleLength)
	needleIndex := needleLength - 1
	haystackIndex := haystackLength - 1
	inMatchMatrix := true

	for {
		if inMatchMatrix {
			if !mFromD[needleIndex][haystackIndex] {
				haystackIndex--
				continue
			}
			inMatchMatrix = false
		}

		matchedIndexes[needleIndex] = haystackIndex
		if needleIndex == 0 {
			return matchedIndexes
		}

		inMatchMatrix = !dFromD[needleIndex][haystackIndex]
		needleIndex--
		haystackIndex--
	}
}
