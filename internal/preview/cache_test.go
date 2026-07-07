package preview

import (
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
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
// of both the candidate path and the renderer config so two candidates (or two
// configs) cannot alias each other.
func TestCache_Key_DistinguishesPathAndConfig(t *testing.T) {
	t.Parallel()

	cfgA := config.PreviewConfig{Default: []string{"identity"}}
	cfgB := config.PreviewConfig{Default: []string{"git"}, MaxLines: 7}
	candOne := source.Candidate{Path: "/p/one", Label: "One", Source: config.SourceWorkspaces}
	candTwo := source.Candidate{Path: "/p/two", Label: "One", Source: config.SourceWorkspaces}

	keyA1 := PreviewCacheKey(candOne, cfgA)
	keyA2 := PreviewCacheKey(candOne, cfgA)
	keyB := PreviewCacheKey(candTwo, cfgA)
	keyDiffCfg := PreviewCacheKey(candOne, cfgB)

	if keyA1 != keyA2 {
		t.Error("same candidate+config must produce same key")
	}
	if keyA1 == keyB {
		t.Error("different paths must produce different keys")
	}
	if keyA1 == keyDiffCfg {
		t.Error("different configs must produce different keys")
	}
}

// TestCache_Key_DistinguishesCandidatesSharingPath is the regression test for
// the cache-collision bug: distinct candidates that resolve to the SAME
// filesystem path (e.g. multiple [[workspaces]] entries pointing at the same
// dir, a real repro found in production config) must not alias each other's
// cached preview.
func TestCache_Key_DistinguishesCandidatesSharingPath(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Default: []string{"identity"}}
	shared := "/Users/x/Trabajo/ECORP"

	ecorp := source.Candidate{Path: shared, Label: "ECORP", Source: config.SourceWorkspaces, Meta: map[string]string{"group": "true"}}
	allsafe := source.Candidate{Path: shared, Label: "allsafe", Source: config.SourceWorkspaces, Meta: map[string]string{"command": "allsafe start"}}
	k8s := source.Candidate{Path: shared, Label: "k8s-ecorp", Source: config.SourceWorkspaces, Meta: map[string]string{"template": "k8s"}}

	keyLatam := PreviewCacheKey(ecorp, cfg)
	keyallsafe := PreviewCacheKey(allsafe, cfg)
	keyK8s := PreviewCacheKey(k8s, cfg)

	if keyLatam == keyallsafe {
		t.Errorf("ECORP and allsafe share a path but are distinct candidates; keys must differ (got %q for both)", keyLatam)
	}
	if keyLatam == keyK8s {
		t.Errorf("ECORP and k8s-ecorp share a path but are distinct candidates; keys must differ (got %q for both)", keyLatam)
	}
	if keyallsafe == keyK8s {
		t.Errorf("allsafe and k8s-ecorp share a path but are distinct candidates; keys must differ (got %q for both)", keyallsafe)
	}
}

// TestCache_Key_SameCandidateRepeatsHit confirms a genuine revisit of the SAME
// candidate (identical path, label, source, meta) still produces the same
// key, so cursor revisits of the same row keep hitting the cache — the whole
// point of the cache per its doc comment.
func TestCache_Key_SameCandidateRepeatsHit(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Default: []string{"identity"}}
	c := source.Candidate{
		Path:   "/x",
		Label:  "foo",
		Source: config.SourceHerdr,
		Meta:   map[string]string{"workspace_id": "wA", "tab_id": "t1"},
	}
	k1 := PreviewCacheKey(c, cfg)
	k2 := PreviewCacheKey(c.Clone(), cfg)
	if k1 != k2 {
		t.Errorf("same candidate must produce the same key across calls: %q vs %q", k1, k2)
	}
}

// TestCache_Key_MetaOrderIndependent confirms the key does not depend on Go's
// randomised map iteration order: identical Meta content built via different
// insertion orders must hash identically.
func TestCache_Key_MetaOrderIndependent(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Default: []string{"identity"}}
	c1 := source.Candidate{Path: "/x", Meta: map[string]string{"a": "1", "b": "2", "c": "3"}}
	c2 := source.Candidate{Path: "/x", Meta: map[string]string{"c": "3", "a": "1", "b": "2"}}
	if PreviewCacheKey(c1, cfg) != PreviewCacheKey(c2, cfg) {
		t.Error("meta key insertion order must not affect the cache key")
	}
}
