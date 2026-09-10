// Package resolver normalises candidate paths, deduplicates by normalised
// path and resolves a user query against the resulting candidate set.
//
// Normalisation order: expand a leading ~, make absolute, trim a trailing
// separator, then filepath.EvalSymlinks. If symlink resolution fails the
// caller keeps the cleaned absolute path so two unresolved-but-distinct paths
// are not collapsed (PL-4). Query matching is a case-insensitive substring
// match against both label and path (PL-7).
package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/source"
)

// Normalize returns the canonical path for input. The steps are applied in a
// fixed order so dedup is stable across providers:
//  1. expand a leading ~ to the user home dir
//  2. make the path absolute (anchored at cwd when needed)
//  3. clean redundant separators via filepath.Clean
//  4. trim a single trailing separator (keep root "/" untouched)
//  5. EvalSymlinks; on failure keep the cleaned absolute path so unresolved
//     paths remain distinct rather than collapsing to an empty string
//
// A returned error only happens when the initial expansion or absolute
// resolution itself fails (e.g. cwd unobtainable). Symlink failures never
// produce an error — they degrade to the cleaned path.
//
// The implementation lives in pathutil.Normalize (a dependency-free leaf
// package) so herdr can share it without importing resolver -> source and
// creating a cycle. This export is kept for callers that already depend on
// it (e.g. internal/command/open.go).
func Normalize(input string) (string, error) {
	return pathutil.Normalize(input)
}

// Dedup normalises each candidate and removes duplicate candidates. Integration
// candidates are actionable routes, so their stable ranking.Identity is the
// deduplication key and filesystem path/label collisions are irrelevant. Other
// non-Herdr candidates retain the established path+case-insensitive-label rule.
// Herdr- and sessions-sourced candidates are exempt from non-integration
// collapse: each is an independently actionable daemon target and may
// legitimately share a label+path. The returned slice preserves input order;
// survivors carry a defensive Meta copy and their NormalizedPath.
//
// This is an O(N^2) scan for path-backed non-integration candidates because
// SameDir depends on a Stat syscall. Integration identity lookup is O(1).
func Dedup(candidates []source.Candidate) []source.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]source.Candidate, 0, len(candidates))
	nonIntegrationLabelBuckets := make(map[string][]int, len(candidates))
	integrationIdentities := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		norm, err := Normalize(c.Path)
		if err != nil {
			// Keep unnormalisable candidates distinct by their raw path so a
			// broken candidate does not silently swallow others.
			norm = c.Path
		}
		clone := c.Clone()
		clone.NormalizedPath = norm

		if c.Meta["integration"] == "true" {
			identity := ranking.Identity(clone)
			if identity != "" {
				if _, duplicate := integrationIdentities[identity]; duplicate {
					continue
				}
				integrationIdentities[identity] = struct{}{}
			}
			out = append(out, clone)
			continue
		}

		// Herdr- and sessions-sourced candidates model already-open daemon
		// targets. Any pair touching either source is exempt, so a resume or
		// attach option is never hidden behind another source.
		if c.Source == config.SourceHerdr || c.Source == config.SourceSessions {
			out = append(out, clone)
			continue
		}

		labelKey := strings.ToLower(c.Label)
		duplicate := false
		for _, keptIdx := range nonIntegrationLabelBuckets[labelKey] {
			kept := out[keptIdx]
			if kept.NormalizedPath == norm || pathutil.SameDir(kept.NormalizedPath, norm) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		out = append(out, clone)
		nonIntegrationLabelBuckets[labelKey] = append(nonIntegrationLabelBuckets[labelKey], len(out)-1)
	}
	return out
}

// Match performs a case-insensitive substring search against each candidate's
// label and normalised path, plus explicit aliases as discrete search terms.
// An empty query returns all candidates so the caller can decide how to
// disambiguate (PL-7). Arbitrary Meta is intentionally excluded.
func Match(candidates []source.Candidate, query string) []source.Candidate {
	if query == "" {
		out := make([]source.Candidate, len(candidates))
		for i, c := range candidates {
			out[i] = c.Clone()
		}
		return out
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	out := make([]source.Candidate, 0, len(candidates))
	for _, c := range candidates {
		haystack := strings.ToLower(c.Label + " " + c.NormalizedPath + " " + c.Path)
		matched := strings.Contains(haystack, needle)
		if !matched {
			_, _, matched = source.MatchAlias(query, c.Aliases)
		}
		if matched {
			out = append(out, c.Clone())
		}
	}
	return out
}

// Resolve returns the single candidate matching query, or false when zero or
// more than one match exists. It is the non-interactive v1 entry point used
// by commands that want exact resolution without a TUI cascade.
func Resolve(candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	matches := Match(candidates, query)
	switch len(matches) {
	case 0:
		return source.Candidate{}, false, fmt.Errorf("no match: %s", query)
	case 1:
		return matches[0], true, nil
	default:
		return source.Candidate{}, false, fmt.Errorf("ambiguous: %s (%d matches)", query, len(matches))
	}
}

// ResolveFromSources is the full pipeline used by commands: collect from the
// registry, dedup, then match the query. It returns the final candidate set
// (after dedup) plus the matches for the query so callers can render "no
// match" or "ambiguous" summaries from one trip.
func ResolveFromSources(ctx context.Context, registry *source.Registry, query string) (all []source.Candidate, matches []source.Candidate, err error) {
	if registry == nil {
		return nil, nil, errors.New("resolve: nil registry")
	}
	raw, collectErr := registry.Collect(ctx)
	all = Dedup(raw)
	matches = Match(all, query)
	if collectErr != nil {
		return all, matches, collectErr
	}
	return all, matches, nil
}
