package ranking

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// referenceContainsWordOrPrefix is the original allocating implementation,
// the oracle for ContainsWordOrPrefix.
func referenceContainsWordOrPrefix(query, label string) bool {
	query = strings.ToLower(query)
	label = strings.ToLower(label)
	for _, word := range strings.FieldsFunc(label, func(r rune) bool {
		return r == ' ' || r == '-' || r == '_' || r == '/' || r == '.'
	}) {
		if strings.HasPrefix(word, query) {
			return true
		}
	}
	return false
}

// TestContainsWordOrPrefix_MatchesReference compares the allocation-free
// word-prefix test with the original over generated inputs, invalid UTF-8
// and case-changing runes included.
func TestContainsWordOrPrefix_MatchesReference(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 6))
	alphabet := []string{"a", "B", "c", "-", "_", "/", ".", " ", "É", "é", "İ", "i", "K", "\xff", "ß"}
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 50000 {
		q, l := word(rng.IntN(4)), word(rng.IntN(14))
		if got, want := ContainsWordOrPrefix(q, l), referenceContainsWordOrPrefix(q, l); got != want {
			t.Fatalf("ContainsWordOrPrefix(%q, %q) = %v, want %v", q, l, got, want)
		}
	}
}
