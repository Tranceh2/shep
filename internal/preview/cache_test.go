package preview

import (
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
)

// TestCache_HitAndMiss covers Put→Get success and unknown-key miss.
func TestCache_HitAndMiss(t *testing.T) {
	t.Parallel()

	c := NewCache(5 * time.Second)
	if _, ok := c.Get("nope"); ok {
		t.Fatal("unknown key must miss")
	}
	want := Result{Text: "hello"}
	c.Put("k", want)
	got, ok := c.Get("k")
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if got.Text != want.Text {
		t.Errorf("text: got %q want %q", got.Text, want.Text)
	}
	if !got.FromCache {
		t.Error("cached result must report FromCache=true")
	}
}

// TestCache_TTLExpiry drops an entry once its TTL elapses.
func TestCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	c := NewCache(15 * time.Millisecond)
	c.Put("k", Result{Text: "x"})
	if _, ok := c.Get("k"); !ok {
		t.Fatal("expected hit within TTL")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

// TestCache_Key_DistinguishesPathAndConfig confirms the cache key is a function
// of both the normalized path and the renderer config so two candidates (or two
// configs) cannot alias each other.
func TestCache_Key_DistinguishesPathAndConfig(t *testing.T) {
	t.Parallel()

	cfgA := config.PreviewConfig{Command: "echo a"}
	cfgB := config.PreviewConfig{Command: "echo b", MaxLines: 7}
	keyA1 := PreviewCacheKey("/p/one", cfgA)
	keyA2 := PreviewCacheKey("/p/one", cfgA)
	keyB := PreviewCacheKey("/p/two", cfgA)
	keyDiffCfg := PreviewCacheKey("/p/one", cfgB)

	if keyA1 != keyA2 {
		t.Error("same path+config must produce same key")
	}
	if keyA1 == keyB {
		t.Error("different paths must produce different keys")
	}
	if keyA1 == keyDiffCfg {
		t.Error("different configs must produce different keys")
	}
}
