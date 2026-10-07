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

// TestCache_TTLExpiry drops an entry once its TTL elapses, driven by a fake
// clock so the test never depends on scheduler timing.
func TestCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1_000, 0)
	c := NewWithClock[string](15*time.Millisecond, func() time.Time { return clock })
	c.Put("k", "x")
	clock = clock.Add(15 * time.Millisecond)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("expected hit at the TTL boundary")
	}
	clock = clock.Add(time.Nanosecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

// TestCache_TTLZeroNeverExpires confirms ttl<=0 disables time-based eviction:
// an entry stays retrievable however far the clock advances.
func TestCache_TTLZeroNeverExpires(t *testing.T) {
	t.Parallel()

	clock := time.Unix(1_000, 0)
	c := NewWithClock[int](0, func() time.Time { return clock })
	c.Put("k", 42)
	clock = clock.Add(24 * time.Hour)
	got, ok := c.Get("k")
	if !ok {
		t.Fatal("expected hit: ttl<=0 must never expire")
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}
