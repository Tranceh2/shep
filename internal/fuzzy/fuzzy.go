// Package fuzzy provides a Unicode-aware fzy-style subsequence matcher.
package fuzzy

import (
	"sync"
	"unicode"
	"unicode/utf8"
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
// under simple case folding. It compares runes as strings.EqualFold does, so
// it follows Go's Unicode case-folding semantics without NFC/NFD normalization.
// An empty query always matches.
func Match(query, haystack string) bool {
	return isSubsequenceString(query, haystack)
}

// isSubsequenceString is isSubsequence over the strings themselves: ranging
// over a string decodes the same runes a []rune conversion yields (invalid
// bytes as U+FFFD), without allocating either slice.
func isSubsequenceString(query, haystack string) bool {
	if query == "" {
		return true
	}
	want, size := utf8.DecodeRuneInString(query)
	for _, r := range haystack {
		if sameFold(want, r) {
			query = query[size:]
			if query == "" {
				return true
			}
			want, size = utf8.DecodeRuneInString(query)
		}
	}
	return false
}

// Score computes the fzy-style affine-gap score for query in haystack and the
// codepoint indexes of the optimal match path in haystack. Empty and
// non-subsequence queries return (0, nil). Callers that pass "Label Path" as
// haystack can receive indexes in Path; those positions may have no rendered
// Label rune to highlight.
//
// Every cell of the dynamic program reads only the previous needle row at
// the previous haystack column, so one row per score matrix suffices: the
// value about to be overwritten is carried to the next column as its
// diagonal. The traceback needs every cell's two direction flags, kept in a
// bit set. A query is scored against every candidate on every keystroke, so
// the rows and flags live in pooled scratch memory: scoring allocates only
// the returned indexes.
func Score(query, haystack string) (score int, matchedIndexes []int) {
	return scoreMatch(query, haystack, true)
}

// ScoreOnly is Score without the match path: it keeps no direction flags and
// runs no traceback, for callers that only rank.
func ScoreOnly(query, haystack string) int {
	score, _ := scoreMatch(query, haystack, false)
	return score
}

// scratch is one scoring's reusable memory: the bonus and score rows, and
// the direction flags.
type scratch struct {
	rows []int
	bits []uint64
}

var scratchPool = sync.Pool{New: func() any { return new(scratch) }}

func scoreMatch(query, haystack string, withPath bool) (int, []int) {
	if query == "" || !isSubsequenceString(query, haystack) {
		return 0, nil
	}
	needle := []rune(query)
	n, m := len(needle), utf8.RuneCountInString(haystack)
	buf := scratchPool.Get().(*scratch)
	defer scratchPool.Put(buf)
	buf.rows = grow(buf.rows, 3*m)
	// Every cell of the three rows is written before it is read (the first
	// needle row reads no diagonal), so they need no clearing.
	bonuses := fillBonuses(buf.rows[:m], haystack)
	dRow, mRow := buf.rows[m:2*m], buf.rows[2*m:3*m]
	var dirs directions
	if withPath {
		dirs = newDirections(n, m, buf)
	}

	for needleIndex, needleRune := range needle {
		gapPenalty := PenaltyGapInner
		if needleIndex == n-1 {
			gapPenalty = PenaltyGapTrailing
		}

		previousScore := minimumScore
		diagD, diagM := minimumScore, minimumScore
		haystackIndex := -1
		for _, haystackRune := range haystack {
			haystackIndex++ // rune index: ranging over the string yields byte offsets
			upD, upM := dRow[haystackIndex], mRow[haystackIndex]
			dScore := minimumScore
			if sameFold(needleRune, haystackRune) {
				if needleIndex == 0 {
					dScore = haystackIndex*PenaltyGapLeading + bonuses[haystackIndex]
				} else if haystackIndex > 0 {
					fromM := addPenalty(diagM, bonuses[haystackIndex])
					fromD := addPenalty(diagD, BonusConsecutive)
					if fromD >= fromM {
						dScore = fromD
						if withPath {
							dirs.set(dirs.dFromD(needleIndex, haystackIndex))
						}
					} else {
						dScore = fromM
					}
				}
			}

			dRow[haystackIndex] = dScore
			gapScore := addPenalty(previousScore, gapPenalty)
			if dScore >= gapScore {
				mRow[haystackIndex] = dScore
				if withPath {
					dirs.set(dirs.mFromD(needleIndex, haystackIndex))
				}
			} else {
				mRow[haystackIndex] = gapScore
			}
			previousScore = mRow[haystackIndex]
			diagD, diagM = upD, upM
		}
	}

	if !withPath {
		return mRow[m-1], nil
	}
	return mRow[m-1], traceback(dirs, n, m)
}

// grow returns s resliced to n elements, reallocating only when its capacity
// is short; the contents are unspecified.
func grow[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

// directions holds the dynamic program's two direction flags per cell —
// whether M took its value from D, and whether D extended a consecutive
// run — as bits, row-major over the needle.
type directions struct {
	bits    []uint64
	columns int
	plane   int // bit offset of the dFromD plane
}

// newDirections returns cleared direction flags for a rows×columns program,
// backed by buf's flag memory.
func newDirections(rows, columns int, buf *scratch) directions {
	plane := rows * columns
	buf.bits = grow(buf.bits, (2*plane+63)/64)
	clear(buf.bits)
	return directions{bits: buf.bits, columns: columns, plane: plane}
}

func (d directions) mFromD(row, column int) int { return row*d.columns + column }
func (d directions) dFromD(row, column int) int { return d.plane + row*d.columns + column }
func (d directions) set(bit int)                { d.bits[bit/64] |= 1 << (bit % 64) }
func (d directions) get(bit int) bool           { return d.bits[bit/64]&(1<<(bit%64)) != 0 }

// sameFold reports whether a and b are equal under simple Unicode case
// folding: exactly strings.EqualFold(string(a), string(b)) for the valid
// runes a []rune conversion yields, without building two strings per
// comparison (the scorer's innermost loop).
func sameFold(a, b rune) bool {
	if a == b {
		return true
	}
	if a < b {
		a, b = b, a
	}
	if a < utf8.RuneSelf {
		return 'A' <= b && b <= 'Z' && a == b+'a'-'A'
	}
	r := unicode.SimpleFold(b)
	for r != b && r < a {
		r = unicode.SimpleFold(r)
	}
	return r == a
}

// fillBonuses writes the match bonus of each haystack rune (by rune index)
// into every cell of bonuses, which holds one cell per rune, and returns it.
// Ranging over the string decodes the runes a []rune conversion would
// (invalid bytes as U+FFFD) without allocating one.
func fillBonuses(bonuses []int, haystack string) []int {
	index, previous := 0, rune(0)
	for _, current := range haystack {
		bonuses[index] = 0
		switch {
		case index == 0:
			bonuses[index] = BonusWord
		case previous == '/':
			bonuses[index] = BonusSlash
		case isWordSeparator(previous):
			bonuses[index] = BonusWord
		case previous == '.':
			bonuses[index] = BonusDot
		case unicode.IsLower(previous) && unicode.IsUpper(current):
			bonuses[index] = BonusCamelCase
		}
		index, previous = index+1, current
	}
	return bonuses
}

func isWordSeparator(r rune) bool {
	return r == '-' || r == '_' || r == ' '
}

func addPenalty(score, penalty int) int {
	if score == minimumScore {
		return minimumScore
	}
	return score + penalty
}

// traceback walks the direction flags back from the last cell to recover
// the optimal match path.
func traceback(dirs directions, needleLength, haystackLength int) []int {
	matchedIndexes := make([]int, needleLength)
	needleIndex := needleLength - 1
	haystackIndex := haystackLength - 1
	inMatchMatrix := true

	for {
		if inMatchMatrix {
			if !dirs.get(dirs.mFromD(needleIndex, haystackIndex)) {
				haystackIndex--
				continue
			}
		}

		matchedIndexes[needleIndex] = haystackIndex
		if needleIndex == 0 {
			return matchedIndexes
		}

		inMatchMatrix = !dirs.get(dirs.dFromD(needleIndex, haystackIndex))
		needleIndex--
		haystackIndex--
	}
}
