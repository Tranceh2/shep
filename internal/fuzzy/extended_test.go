package fuzzy

import (
	"reflect"
	"testing"
)

func TestParseExtendedQuery(t *testing.T) {
	tests := []struct {
		input string
		want  []Term
	}{
		{
			input: "elliot fsociety",
			want: []Term{
				{Raw: "elliot", Kind: TermFuzzy, Pattern: "elliot"},
				{Raw: "fsociety", Kind: TermFuzzy, Pattern: "fsociety"},
			},
		},
		{
			input: "'exact ^prefix suffix$ !exclude",
			want: []Term{
				{Raw: "'exact", Kind: TermExact, Pattern: "exact"},
				{Raw: "^prefix", Kind: TermPrefix, Pattern: "prefix"},
				{Raw: "suffix$", Kind: TermSuffix, Pattern: "suffix"},
				{Raw: "!exclude", Kind: TermFuzzy, Inverse: true, Pattern: "exclude"},
			},
		},
		{
			input: "status:blocked agent:opencode !source:zoxide",
			want: []Term{
				{Raw: "status:blocked", Kind: TermField, Field: "status", Value: "blocked"},
				{Raw: "agent:opencode", Kind: TermField, Field: "agent", Value: "opencode"},
				{Raw: "!source:zoxide", Kind: TermField, Inverse: true, Field: "source", Value: "zoxide"},
			},
		},
		{
			input: "s:idle a:pi p:/tmp",
			want: []Term{
				{Raw: "s:idle", Kind: TermField, Field: "status", Value: "idle"},
				{Raw: "a:pi", Kind: TermField, Field: "agent", Value: "pi"},
				{Raw: "p:/tmp", Kind: TermField, Field: "path", Value: "/tmp"},
			},
		},
		{
			input: "/^arcade.*/ !/error/",
			want: []Term{
				{Raw: "/^arcade.*/", Kind: TermRegex, Pattern: "^arcade.*"},
				{Raw: "!/error/", Kind: TermRegex, Inverse: true, Pattern: "error"},
			},
		},
		{
			input: "\"fsociety arcade\"",
			want: []Term{
				{Raw: "\"fsociety arcade\"", Kind: TermExact, Pattern: "fsociety arcade"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			eq := ParseExtendedQuery(tt.input)
			if len(eq.Terms) != len(tt.want) {
				t.Fatalf("ParseExtendedQuery(%q) terms len = %d, want %d", tt.input, len(eq.Terms), len(tt.want))
			}
			for i := range eq.Terms {
				got := eq.Terms[i]
				want := tt.want[i]
				if got.Raw != want.Raw || got.Kind != want.Kind || got.Inverse != want.Inverse ||
					got.Pattern != want.Pattern || got.Field != want.Field || got.Value != want.Value {
					t.Errorf("term[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestParseExtendedQuery_Clauses(t *testing.T) {
	eq := ParseExtendedQuery("fsociety | ecorp arcade")
	if len(eq.Clauses) != 2 {
		t.Fatalf("expected 2 clauses, got %d", len(eq.Clauses))
	}
	// Clause 0: fsociety | ecorp
	if len(eq.Clauses[0].Alternatives) != 2 {
		t.Fatalf("clause 0 expected 2 alternatives, got %d", len(eq.Clauses[0].Alternatives))
	}
	if eq.Clauses[0].Alternatives[0].Pattern != "fsociety" || eq.Clauses[0].Alternatives[1].Pattern != "ecorp" {
		t.Errorf("unexpected alternatives in clause 0: %+v", eq.Clauses[0].Alternatives)
	}
	// Clause 1: arcade
	if len(eq.Clauses[1].Alternatives) != 1 {
		t.Fatalf("clause 1 expected 1 alternative, got %d", len(eq.Clauses[1].Alternatives))
	}
	if eq.Clauses[1].Alternatives[0].Pattern != "arcade" {
		t.Errorf("unexpected alternative in clause 1: %+v", eq.Clauses[1].Alternatives[0])
	}
}

func TestExtendedQuery_MatchCandidate(t *testing.T) {
	haystack := "fsociety /srv/fsociety/arcade/payload"
	fields := CandidateFields{
		Source:      "herdr",
		Path:        "/srv/fsociety/arcade/payload",
		Agent:       "opencode",
		AgentStatus: "blocked",
	}

	tests := []struct {
		name    string
		query   string
		fields  CandidateFields
		matched bool
	}{
		{
			name:    "empty query matches",
			query:   "",
			fields:  fields,
			matched: true,
		},
		{
			name:    "single fuzzy term matches",
			query:   "fsociety",
			fields:  fields,
			matched: true,
		},
		{
			name:    "space separated AND terms both match",
			query:   "fsociety payload",
			fields:  fields,
			matched: true,
		},
		{
			name:    "space separated AND terms one missing fails",
			query:   "fsociety missing",
			fields:  fields,
			matched: false,
		},
		{
			name:    "exact match succeeds",
			query:   "'arcade/payload",
			fields:  fields,
			matched: true,
		},
		{
			name:    "exact match fails on typo",
			query:   "'arcadepayload",
			fields:  fields,
			matched: false,
		},
		{
			name:    "prefix match anchored to string start succeeds",
			query:   "^fsociety",
			fields:  fields,
			matched: true,
		},
		{
			name:    "prefix match anchored to string start fails on internal word",
			query:   "^arcade",
			fields:  fields,
			matched: false,
		},
		{
			name:    "suffix match anchored to string end succeeds",
			query:   "payload$",
			fields:  fields,
			matched: true,
		},
		{
			name:    "suffix match anchored to string end fails on internal word",
			query:   "arcade$",
			fields:  fields,
			matched: false,
		},
		{
			name:    "exact full match fails on partial",
			query:   "^fsociety$",
			fields:  fields,
			matched: false,
		},
		{
			name:    "exact full match succeeds on full string",
			query:   "^fsociety /srv/fsociety/arcade/payload$",
			fields:  fields,
			matched: true,
		},
		{
			name:    "negation inverse match succeeds when term absent",
			query:   "!ecorp",
			fields:  fields,
			matched: true,
		},
		{
			name:    "negation inverse match fails when term present",
			query:   "!payload",
			fields:  fields,
			matched: false,
		},
		{
			name:    "negation with exact prefix",
			query:   "!^ecorp",
			fields:  fields,
			matched: true,
		},
		{
			name:    "field filter status:blocked matches",
			query:   "status:blocked",
			fields:  fields,
			matched: true,
		},
		{
			name:    "field filter s:idle fails",
			query:   "s:idle",
			fields:  fields,
			matched: false,
		},
		{
			name:    "field filter agent:opencode matches",
			query:   "agent:opencode",
			fields:  fields,
			matched: true,
		},
		{
			name:    "field filter source:herdr matches",
			query:   "source:herdr",
			fields:  fields,
			matched: true,
		},
		{
			name:    "field filter path:/srv/fsociety matches",
			query:   "path:/srv/fsociety",
			fields:  fields,
			matched: true,
		},
		{
			name:    "or pipe matches if first alternative matches",
			query:   "fsociety | ecorp",
			fields:  fields,
			matched: true,
		},
		{
			name:    "or pipe matches if second alternative matches",
			query:   "ecorp | arcade",
			fields:  fields,
			matched: true,
		},
		{
			name:    "or pipe fails if neither alternative matches",
			query:   "ecorp | allsafe",
			fields:  fields,
			matched: false,
		},
		{
			name:    "regex pattern matches",
			query:   "/arcade.*payload/",
			fields:  fields,
			matched: true,
		},
		{
			name:    "regex pattern fails on mismatch",
			query:   "/^arcade/",
			fields:  fields,
			matched: false,
		},
		{
			name:    "quoted string with spaces matches exactly",
			query:   "\"arcade/payload\"",
			fields:  fields,
			matched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq := ParseExtendedQuery(tt.query)
			_, _, matched := eq.MatchCandidate(haystack, tt.fields)
			if matched != tt.matched {
				t.Errorf("MatchCandidate(%q) = %v, want %v", tt.query, matched, tt.matched)
			}
		})
	}
}

func TestExtendedQuery_HighlightIndices(t *testing.T) {
	haystack := "elliot alderson"
	eq := ParseExtendedQuery("'elliot")
	score, idxs, matched := eq.Match(haystack)
	if !matched {
		t.Fatalf("expected match, got false")
	}
	if score <= 0 {
		t.Errorf("expected score > 0, got %d", score)
	}
	wantIdxs := []int{0, 1, 2, 3, 4, 5}
	if !reflect.DeepEqual(idxs, wantIdxs) {
		t.Errorf("idxs = %v, want %v", idxs, wantIdxs)
	}
}
