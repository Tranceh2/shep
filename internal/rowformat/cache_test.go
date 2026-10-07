package rowformat

import (
	"sync"
	"testing"
)

func TestCachedParse_ReusesParsedTemplate(t *testing.T) {
	const format = "{{.Label}} cache-reuse"
	first, err := cachedParse(format)
	if err != nil {
		t.Fatalf("cachedParse: %v", err)
	}
	second, err := cachedParse(format)
	if err != nil {
		t.Fatalf("cachedParse: %v", err)
	}
	if first != second {
		t.Fatal("cachedParse parsed the same format twice; want the cached template")
	}
}

func TestCachedParse_CachesParseErrors(t *testing.T) {
	const format = "{{.Label cache-error"
	if _, err := cachedParse(format); err == nil {
		t.Fatal("cachedParse: want a parse error for an unterminated action")
	}
	if _, err := Render(format, Context{}); err == nil {
		t.Fatal("Render: want the cached parse error on a repeated call")
	}
}

func TestRender_ConcurrentCallsShareOneTemplate(t *testing.T) {
	const format = "{{.Label}} · {{.Path}} concurrent"
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				got, err := Render(format, Context{Label: "shep", Path: "~/shep"})
				if err != nil || got != "shep · ~/shep concurrent" {
					t.Errorf("Render = %q, %v", got, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkRender_RowLabel(b *testing.B) {
	const format = "{{if .Label}}{{.Label}}{{else}}{{.Path}}{{end}}"
	data := Context{Label: "~/Proyectos/shep", Path: "/home/user/Proyectos/shep"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Render(format, data); err != nil {
			b.Fatal(err)
		}
	}
}
