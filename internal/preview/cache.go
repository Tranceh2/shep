package preview

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/tranceh2/shep/internal/config"
)

// Cache is a TTL-bounded in-memory cache of rendered preview Results, keyed by
// the candidate path plus a hash of the renderer config. It keeps the selector
// responsive when the cursor revisits a recently rendered candidate that uses a
// slow preview.command.
type Cache struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]cacheItem
}

type cacheItem struct {
	result  Result
	expires time.Time
}

// NewCache builds a Cache with the given lifetime. A non-positive ttl means
// entries never expire by time (the cache still de-duplicates within a render).
func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, items: make(map[string]cacheItem)}
}

// Get returns the cached Result for key with FromCache=true, or false when the
// key is unknown or its TTL has elapsed (an expired entry is evicted).
func (c *Cache) Get(key string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return Result{}, false
	}
	if c.ttl > 0 && time.Now().After(it.expires) {
		delete(c.items, key)
		return Result{}, false
	}
	it.result.FromCache = true
	return it.result, true
}

// Put stores r under key with the configured TTL.
func (c *Cache) Put(key string, r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := time.Time{}
	if c.ttl > 0 {
		exp = time.Now().Add(c.ttl)
	}
	c.items[key] = cacheItem{result: r, expires: exp}
}

// PreviewCacheKey derives a stable cache key from the candidate path and the
// renderer config. Two candidates with the same path but different command or
// sections must not alias each other, hence the config hash.
func PreviewCacheKey(path string, cfg config.PreviewConfig) string {
	return fmt.Sprintf("%s|%x", path, sha256.Sum256([]byte(configFingerprint(cfg))))
}

// configFingerprint serialises the renderer-relevant preview config into a
// stable string whose changes invalidate the cache.
func configFingerprint(cfg config.PreviewConfig) string {
	return fmt.Sprintf("cmd=%q|timeout=%d|ttl=%d|max=%d|secs=%v",
		cfg.Command, cfg.Timeout, cfg.CacheTTL, cfg.MaxLines, cfg.Sections)
}
