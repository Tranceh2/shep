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

	"github.com/tranceh2/shep/internal/pathutil"
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

// Dedup normalises each candidate and removes path collisions, keeping the
// first-seen candidate whenever another already-kept candidate matches on
// BOTH filesystem identity (pathutil.SameDir — device+inode, not a string
// comparison) AND label (compared case-insensitively via strings.EqualFold).
// That composite check preserves explicitly named workspaces that target
// the same path (e.g. "ECORP" and "k8s-ecorp" at /srv/ecorp) as distinct
// candidates — EqualFold only ignores case, so genuinely different names
// still stay distinct — while still collapsing true duplicates from
// different providers, including two candidates whose paths and/or labels
// differ only in case on a case-insensitive filesystem (e.g. a Herdr-sourced
// "ECORP" and a zoxide-sourced "ecorp" that are the SAME real directory).
// The returned slice reuses the input order for the survivors so provider
// order from the registry is preserved. Candidates carry a defensive copy
// of Meta from the source package; this function only sets NormalizedPath
// on the survivors.
//
// This is an O(N^2) scan rather than an O(1) map lookup, because SameDir
// cannot be expressed as a map key (it depends on a Stat syscall, not just
// the two normalized strings). Picker-sized candidate lists are well under
// 100 entries and this runs once per `shep open` invocation, so the cost is
// a handful of Stat calls, not a hot path.
func Dedup(candidates []source.Candidate) []source.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]source.Candidate, 0, len(candidates))
	for _, c := range candidates {
		norm, err := Normalize(c.Path)
		if err != nil {
			// Keep unnormalisable candidates distinct by their raw path so a
			// broken candidate does not silently swallow others.
			norm = c.Path
		}
		duplicate := false
		for _, kept := range out {
			if strings.EqualFold(kept.Label, c.Label) && pathutil.SameDir(kept.NormalizedPath, norm) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		clone := c.Clone()
		clone.NormalizedPath = norm
		out = append(out, clone)
	}
	return out
}

// Match performs a case-insensitive substring search against each candidate's
// label and normalised path, returning every match. An empty query returns
// all candidates so the caller can decide how to disambiguate (PL-7).
func Match(candidates []source.Candidate, query string) []source.Candidate {
	if query == "" {
		out := make([]source.Candidate, len(candidates))
		for i, c := range candidates {
			out[i] = c.Clone()
		}
		return out
	}
	needle := strings.ToLower(query)
	out := make([]source.Candidate, 0, len(candidates))
	for _, c := range candidates {
		haystack := strings.ToLower(c.Label + " " + c.NormalizedPath + " " + c.Path)
		if strings.Contains(haystack, needle) {
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
