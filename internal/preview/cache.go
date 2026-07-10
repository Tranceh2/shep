package preview

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tranceh2/shep/internal/cache"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// Cache is a TTL-bounded in-memory cache of rendered preview Results, keyed by
// the candidate path plus a hash of the renderer config. It keeps the selector
// responsive when the cursor revisits a recently rendered candidate that uses a
// slow preview.command. It wraps the shared internal/cache.Cache[T] generic
// implementation, adding only the FromCache=true marking a cache hit needs.
type Cache struct {
	inner *cache.Cache[Result]
}

// NewCache builds a Cache with the given lifetime. A non-positive ttl means
// entries never expire by time (the cache still de-duplicates within a render).
func NewCache(ttl time.Duration) *Cache {
	return &Cache{inner: cache.New[Result](ttl)}
}

// Get returns the cached Result for key with FromCache=true, or false when the
// key is unknown or its TTL has elapsed (an expired entry is evicted).
func (c *Cache) Get(key string) (Result, bool) {
	r, ok := c.inner.Get(key)
	if !ok {
		return Result{}, false
	}
	r.FromCache = true
	return r, true
}

// Put stores r under key with the configured TTL.
func (c *Cache) Put(key string, r Result) {
	c.inner.Put(key, r)
}

// PreviewCacheKey derives a stable cache key from the candidate's identity
// and the renderer config. Two DISTINCT candidates that happen to resolve to
// the same filesystem path — e.g. multiple [[workspaces]] entries pointing
// at the same directory, or multiple Herdr tabs/panes sharing a cwd — must
// not alias each other, hence the full candidate fingerprint (not just the
// path) feeds the key alongside the config hash.
func PreviewCacheKey(cand source.Candidate, cfg config.PreviewConfig) string {
	return fmt.Sprintf("%s|%x", candidateFingerprint(cand), sha256.Sum256([]byte(configFingerprint(cfg))))
}

// candidateFingerprint serialises the parts of a candidate that influence
// rendered preview output (path, label, source, and metadata such as
// workspace_id/tab_id) into a stable string. Meta keys are sorted before
// serialising because Go map iteration order is randomised — an unsorted
// serialization would itself be a second source of nondeterminism.
//
// Every field is passed through strconv.Quote before joining. Label (from
// [[workspaces]].name) and Meta values (e.g. Meta["command"]) are
// user-controlled and may legitimately contain the "|", ",", "=" characters
// used as delimiters here. Quoting escapes any embedded delimiter or quote
// character, so a field boundary can never shift — two structurally
// different candidates can no longer serialise to the same fingerprint.
func candidateFingerprint(cand source.Candidate) string {
	keys := make([]string, 0, len(cand.Meta))
	for k := range cand.Meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var meta strings.Builder
	for i, k := range keys {
		if i > 0 {
			meta.WriteByte(',')
		}
		meta.WriteString(strconv.Quote(k))
		meta.WriteByte('=')
		meta.WriteString(strconv.Quote(cand.Meta[k]))
	}
	return fmt.Sprintf("%s|%s|%s|%s",
		strconv.Quote(renderPath(cand)),
		strconv.Quote(cand.Label),
		strconv.Quote(string(cand.Source)),
		meta.String())
}

// configFingerprint serialises the renderer-relevant preview config into a
// stable string whose changes invalidate the cache.
func configFingerprint(cfg config.PreviewConfig) string {
	return fmt.Sprintf("timeout=%d|ttl=%d|max=%d|default=%v|commands=%v",
		cfg.Timeout, cfg.CacheTTL, cfg.MaxLines, cfg.Default, cfg.Commands)
}
