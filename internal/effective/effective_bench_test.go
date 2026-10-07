package effective

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// benchFixture is a realistic picker load: 650 candidates across the
// directory sources (half of them real directories, so normalization and
// directory identity do real work), a few wildcards and workspaces.
func benchFixture(b *testing.B) (*config.Config, []source.Candidate) {
	b.Helper()
	root, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Templates["svc"] = config.TemplateConfig{Command: "nvim"}
	cfg.Wildcards = []config.WildcardConfig{
		{Pattern: root + "/work/**", Presentation: config.Presentation{Icon: strPtr("W "), IconColor: strPtr("peach")}},
		{Pattern: "svc-*", Template: "svc", WorkspaceName: "{{ .Path | base }}"},
		{Pattern: "~/notes/**", Preview: []string{config.PreviewDir}},
		{Pattern: "**/vendor/*", Presentation: config.Presentation{MarkerFormat: strPtr("vendor")}},
	}
	var cands []source.Candidate
	sources := []string{config.SourceProjects, config.SourceZoxide, config.SourceHerdr, config.SourceAgents}
	for i := range 650 {
		dir := filepath.Join(root, []string{"work", "home", "srv"}[i%3], fmt.Sprintf("svc-%03d", i))
		if i%2 == 0 {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
		}
		cands = append(cands, source.Candidate{Path: dir, Label: "~/" + filepath.Base(dir), Source: sources[i%len(sources)]})
	}
	for i := range 6 {
		cfg.Workspaces = append(cfg.Workspaces, config.WorkspaceConfig{
			Name: fmt.Sprintf("ws%d", i), Path: cands[i*100].Path,
			Presentation: config.Presentation{Icon: strPtr("E ")},
		})
	}
	return cfg, cands
}

// BenchmarkResolver_For resolves every setting of 650 candidates per
// operation with the per-path cache warm (the picker's steady state).
func BenchmarkResolver_For(b *testing.B) {
	cfg, cands := benchFixture(b)
	r := New(cfg)
	for _, c := range cands {
		r.For(c)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, c := range cands {
			r.For(c)
		}
	}
}

// BenchmarkResolver_ForCold is BenchmarkResolver_For with a fresh resolver
// per operation: every path is normalized and stat'd once.
func BenchmarkResolver_ForCold(b *testing.B) {
	cfg, cands := benchFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := New(cfg)
		for _, c := range cands {
			r.For(c)
		}
	}
}

// BenchmarkResolver_Attach attaches the presentations of 650 candidates per
// operation (what each producer does), cache warm.
func BenchmarkResolver_Attach(b *testing.B) {
	cfg, cands := benchFixture(b)
	r := New(cfg)
	r.Attach(cands)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r.Attach(cands)
	}
}
