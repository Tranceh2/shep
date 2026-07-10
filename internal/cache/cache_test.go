package cache

import (
	"testing"
	"time"
)

// TestCache_GetPutHit covers Put→Get success and an unknown-key miss, using
// a struct value type to confirm the generic cache round-trips arbitrary T.
func TestCache_GetPutHit(t *testing.T) {
	t.Parallel()

	type payload struct {
		Text string
	}
	c := New[payload](5 * time.Second)

	if _, ok := c.Get("nope"); ok {
		t.Fatal("unknown key must miss")
	}

	want := payload{Text: "hello"}
	c.Put("k", want)
	got, ok := c.Get("k")
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if got.Text != want.Text {
		t.Errorf("value: got %q want %q", got.Text, want.Text)
	}
}

// TestCache_TTLExpiry drops an entry once its TTL elapses.
func TestCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	c := New[string](15 * time.Millisecond)
	c.Put("k", "x")
	if _, ok := c.Get("k"); !ok {
		t.Fatal("expected hit within TTL")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

// TestCache_TTLZeroNeverExpires confirms ttl<=0 disables time-based eviction:
// an entry stays retrievable well past what would be a short TTL window.
func TestCache_TTLZeroNeverExpires(t *testing.T) {
	t.Parallel()

	c := New[int](0)
	c.Put("k", 42)
	time.Sleep(20 * time.Millisecond)
	got, ok := c.Get("k")
	if !ok {
		t.Fatal("expected hit: ttl<=0 must never expire")
	}
	if got != 42 {
		t.Errorf("value: got %d want %d", got, 42)
	}
}
