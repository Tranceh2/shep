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
	"os"
	"path/filepath"
	"strings"

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
func Normalize(input string) (string, error) {
	if input == "" {
		return "", errors.New("normalize: empty path")
	}
	expanded, err := expandTilde(input)
	if err != nil {
		return "", fmt.Errorf("normalize %q: %w", input, err)
	}
	absolute, err := absPath(expanded)
	if err != nil {
		return "", fmt.Errorf("normalize %q: %w", input, err)
	}
	cleaned := filepath.Clean(absolute)
	cleaned = trimTrailingSep(cleaned)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved, nil
	}
	// Unresolved symlinks are not fatal: callers need a stable key for dedup.
	return cleaned, nil
}

// Dedup normalises each candidate and removes path collisions, keeping the
// first-seen candidate for each normalised path. The returned slice reuses
// the input order for the survivors so provider order from the registry is
// preserved. Candidates carry a defensive copy of Meta from the source
// package; this function only sets NormalizedPath on the survivors.
func Dedup(candidates []source.Candidate) []source.Candidate {
	if len(candidates) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]source.Candidate, 0, len(candidates))
	for _, c := range candidates {
		norm, err := Normalize(c.Path)
		if err != nil {
			// Keep unnormalisable candidates distinct by their raw path so a
			// broken candidate does not silently swallow others.
			norm = c.Path
		}
		if _, exists := seen[norm]; exists {
			continue
		}
		seen[norm] = struct{}{}
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

// expandTilde replaces a leading ~ with the user's home directory. A missing
// HOME is an error rather than a silent pass-through, because normalisation
// must be deterministic to keep dedup correct.
func expandTilde(p string) (string, error) {
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// absPath makes a path absolute. Relative paths are anchored at the process
// cwd; an unobtainable cwd is a hard error because absolute forms underpin
// the rest of the normalisation pipeline.
func absPath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, p), nil
}

// trimTrailingSep removes a single trailing separator while preserving the
// root "/" so POSIX semantics hold on dedup keys.
func trimTrailingSep(p string) string {
	if p == string(filepath.Separator) {
		return p
	}
	return strings.TrimRight(p, string(filepath.Separator))
}
