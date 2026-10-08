package sourcecache

import (
	"reflect"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// TestSaveLoad_RoundTripsUnderTheSameConfiguration proves a saved result
// comes back as saved (without a resolved presentation), and that another
// configuration of the source, another source or no entry at all loads
// nothing.
func TestSaveLoad_RoundTripsUnderTheSameConfiguration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := Fingerprint(map[string]any{"roots": []string{"~/code"}, "max_depth": 3})
	saved := Result{
		Candidates: []source.Candidate{
			{Path: "/code/allsafe", NormalizedPath: "/code/allsafe", Label: "allsafe", Source: "projects", Meta: map[string]string{"branch": "main"}},
			{Path: "/code/ecorp", Label: "e corp", Source: "kube-contexts", Aliases: []string{"evil"}, Presentation: &source.Presentation{}},
		},
		NormalizedPaths: map[string]string{"/code/allsafe": "/code/allsafe"},
	}
	if err := Save(dir, "projects", fp, saved); err != nil {
		t.Fatal(err)
	}
	got, ok := Load(dir, "projects", fp)
	want := saved
	want.Candidates = append([]source.Candidate(nil), saved.Candidates...)
	want.Candidates[1].Presentation = nil
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, %v; want %+v", got, ok, want)
	}
	if _, ok := Load(dir, "projects", Fingerprint(map[string]any{"max_depth": 5})); ok {
		t.Error("loaded a result saved under another configuration")
	}
	if _, ok := Load(dir, "kube-contexts", fp); ok {
		t.Error("loaded another source's result")
	}
	if _, ok := Load(t.TempDir(), "projects", fp); ok {
		t.Error("loaded a result that was never saved")
	}
}
