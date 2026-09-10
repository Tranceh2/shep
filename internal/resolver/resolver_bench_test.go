package resolver

import (
	"fmt"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

func generateBenchmarkCandidates(b *testing.B, count int) []source.Candidate {
	b.Helper()
	cands := make([]source.Candidate, 0, count)
	// Add some herdr
	for i := 0; i < count/6; i++ {
		cands = append(cands, source.Candidate{
			Path:   fmt.Sprintf("/tmp/shep-bench/herdr-ws-%d", i),
			Label:  fmt.Sprintf("herdr-ws-%d", i),
			Source: config.SourceHerdr,
			Meta:   map[string]string{"workspace_id": fmt.Sprintf("ws-%d", i)},
		})
	}
	// Add workspaces
	for i := 0; i < count/6; i++ {
		cands = append(cands, source.Candidate{
			Path:   fmt.Sprintf("/tmp/shep-bench/workspace-%d", i),
			Label:  fmt.Sprintf("workspace-%d", i),
			Source: config.SourceWorkspaces,
		})
	}
	// Add zoxide (with some overlapping paths/labels with workspaces/projects)
	for i := 0; i < count/3; i++ {
		cands = append(cands, source.Candidate{
			Path:   fmt.Sprintf("/tmp/shep-bench/project-%d", i),
			Label:  fmt.Sprintf("project-%d", i),
			Source: config.SourceZoxide,
		})
	}
	// Add projects (with duplicates of zoxide)
	for i := 0; i < count/3; i++ {
		cands = append(cands, source.Candidate{
			Path:   fmt.Sprintf("/tmp/shep-bench/project-%d", i),
			Label:  fmt.Sprintf("project-%d", i),
			Source: config.SourceProjects,
		})
	}
	return cands
}

func BenchmarkDedup_600(b *testing.B) {
	cands := generateBenchmarkCandidates(b, 600)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Dedup(cands)
	}
}
