// Package cache provides a small generic, TTL-bounded in-memory cache
// shared by call sites (preview rendering, TUI tree-expand fetches) that
// need to remember a recently computed value for a short window without
// implementing their own map+mutex+expiry bookkeeping.
package cache

import (
	"sync"
	"time"
)

// Cache is a TTL-bounded in-memory cache of values of type T, keyed by an
// opaque caller-chosen string. It is safe for concurrent use.
type Cache[T any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]item[T]
}

type item[T any] struct {
	value   T
	expires time.Time
}

// New builds a Cache with the given lifetime. A non-positive ttl disables
// time-based eviction entirely: entries never expire on their own (the
// cache still overwrites a key on a later Put).
func New[T any](ttl time.Duration) *Cache[T] {
	return &Cache[T]{ttl: ttl, items: make(map[string]item[T])}
}

// Get returns the cached value for key, or ok=false when the key is unknown
// or its TTL has elapsed (an expired entry is evicted on read).
func (c *Cache[T]) Get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		var zero T
		return zero, false
	}
	if c.ttl > 0 && time.Now().After(it.expires) {
		delete(c.items, key)
		var zero T
		return zero, false
	}
	return it.value, true
}

// Put stores v under key with the configured TTL, overwriting any existing
// entry for the same key.
func (c *Cache[T]) Put(key string, v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := time.Time{}
	if c.ttl > 0 {
		exp = time.Now().Add(c.ttl)
	}
	c.items[key] = item[T]{value: v, expires: exp}
}
