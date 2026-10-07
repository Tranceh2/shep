package tmpl

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRender(t *testing.T) {
	e := New("/home/me")
	data := Data{
		Path:        "/workspace/shep",
		Label:       "shep",
		Icon:        "󰊢",
		TabNumber:   "3",
		AgentStatus: "working",
		Kind:        KindFolder,
	}
	cases := []struct {
		name     string
		format   string
		data     Data
		want     string
		errMatch string
	}{
		{name: "literal text", format: "workspace", data: data, want: "workspace"},
		{name: "empty format", format: "", data: data, want: ""},
		{name: "path field", format: "{{.Path}}", data: data, want: "/workspace/shep"},
		{name: "label field", format: "{{.Label}}", data: data, want: "shep"},
		{name: "icon field", format: "{{.Icon}}", data: data, want: "󰊢"},
		{name: "tab number field", format: "{{.TabNumber}}", data: data, want: "3"},
		{name: "agent status field", format: "{{.AgentStatus}}", data: data, want: "working"},
		{name: "kind field", format: `{{ if eq .Kind "folder" }}dir{{ end }}`, data: data, want: "dir"},
		{
			name:   "metadata dot field and absent key",
			format: "{{.Label}} {{.Meta.agent}}/{{.Meta.missing}}",
			data:   Data{Label: "shep", Meta: map[string]string{"agent": "pi"}},
			want:   "shep pi/",
		},
		{name: "nil metadata renders empty", format: "{{.Label}}/{{.Meta.missing}}", data: data, want: "shep/"},
		{
			name:   "metadata index",
			format: `{{ index .Meta "context" }}`,
			data:   Data{Meta: map[string]string{"context": "cluster prod west"}},
			want:   "cluster prod west",
		},
		{
			name:   "conditional label collapses when empty",
			format: "{{if .Label}}{{.Label}} · {{end}}{{.Path}}",
			data:   Data{Path: "/workspace/shep"},
			want:   "/workspace/shep",
		},
		{name: "sprig and shep functions together", format: "{{ .Path | tilde | upper }}", data: Data{Path: "/home/me/x"}, want: "~/X"},
		{name: "malformed template", format: "{{if .Path}}", data: data, errMatch: "unexpected EOF"},
		{name: "unknown field fails during execution", format: "{{.Unknown}}", data: data, errMatch: "can't evaluate field Unknown"},
		{name: "unknown function fails at parse", format: "{{ nope .Path }}", data: data, errMatch: `function "nope" not defined`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Render(tc.format, tc.data)
			if tc.errMatch != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("Render(%q) error = %v, want it to contain %q", tc.format, err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render(%q): %v", tc.format, err)
			}
			if got != tc.want {
				t.Errorf("Render(%q) = %q, want %q", tc.format, got, tc.want)
			}
		})
	}
}

func TestParse_ReusesParsedTemplate(t *testing.T) {
	e := New("")
	const format = "{{.Label}} cache-reuse"
	first, err := e.Parse(format)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	second, err := e.Parse(format)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if first != second {
		t.Fatal("Parse parsed the same format twice; want the cached template")
	}
	if got := e.cacheCount.Load(); got != 1 {
		t.Fatalf("cache count = %d, want 1", got)
	}
}

func TestParse_CachesParseErrors(t *testing.T) {
	e := New("")
	const format = "{{.Label cache-error"
	_, first := e.Parse(format)
	if first == nil {
		t.Fatal("Parse: want a parse error for an unterminated action")
	}
	_, second := e.Parse(format)
	if second != first {
		t.Fatalf("second Parse error = %v, want the cached error %v", second, first)
	}
	if _, err := e.Render(format, Data{}); err != first {
		t.Fatalf("Render error = %v, want the cached parse error", err)
	}
}

func TestParse_CacheIsBounded(t *testing.T) {
	e := New("")
	for i := range maxCachedTemplates + 10 {
		if _, err := e.Parse("{{.Label}}" + strings.Repeat("x", i)); err != nil {
			t.Fatalf("Parse: %v", err)
		}
	}
	if got := e.cacheCount.Load(); got != maxCachedTemplates {
		t.Fatalf("cache count = %d, want the cap %d", got, maxCachedTemplates)
	}
	// Formats past the cap still render; they just are not cached.
	got, err := e.Render("{{.Label}} uncached", Data{Label: "shep"})
	if err != nil || got != "shep uncached" {
		t.Fatalf("Render past the cap = %q, %v", got, err)
	}
}

func TestEngines_DoNotShareState(t *testing.T) {
	a, b := New("/home/a"), New("/home/b")
	const format = "{{ .Path | tilde }}"
	got, _ := a.Render(format, Data{Path: "/home/a/x"})
	if got != "~/x" {
		t.Fatalf("engine a = %q, want ~/x", got)
	}
	got, _ = b.Render(format, Data{Path: "/home/a/x"})
	if got != "/home/a/x" {
		t.Fatalf("engine b = %q, want the path unchanged (different home)", got)
	}
}

func TestRender_ConcurrentCallsShareOneTemplate(t *testing.T) {
	e := New("/home/me")
	const format = "{{.Label}} · {{.Path | tilde}} concurrent"
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				got, err := e.Render(format, Data{Label: "shep", Path: "/home/me/shep"})
				if err != nil || got != "shep · ~/shep concurrent" {
					t.Errorf("Render = %q, %v", got, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestValidate(t *testing.T) {
	e := New(SampleHome)
	cases := []struct {
		name    string
		format  string
		samples []Data
		wantErr string
	}{
		{name: "valid against every kind", format: "{{ .Label | trimIcon | name }}"},
		{name: "literal", format: "plain"},
		{name: "missing meta key is fine", format: "{{ .Meta.nope }}"},
		{name: "syntax error", format: "{{ if .Label }}", wantErr: "field.x: template: shep:1: unexpected EOF"},
		{name: "unknown field", format: "{{ .Nope }}", wantErr: "field.x: template: shep:1:3: executing \"shep\" at <.Nope>: can't evaluate field Nope"},
		{name: "unknown function", format: "{{ osBase .Path }}", wantErr: `field.x: template: shep:1: function "osBase" not defined`},
		{
			name:    "data-dependent failure is caught by a sample",
			format:  "{{ slice .Branch 0 4 }}",
			samples: Samples(KindWorktree, KindFolder),
			wantErr: "field.x: ",
		},
		{name: "data-dependent template valid for its kind", format: "{{ slice .Branch 0 4 }}", samples: Samples(KindWorktree)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := e.Validate("field.x", tc.format, tc.samples...)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(%q) = %v, want nil", tc.format, err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) {
				t.Fatalf("Validate(%q) = %v, want prefix %q", tc.format, err, tc.wantErr)
			}
		})
	}
}

func TestValidate_EmptyFieldLeavesErrorUnwrapped(t *testing.T) {
	err := New("").Validate("", "{{ .Nope }}", Data{})
	if err == nil || strings.HasPrefix(err.Error(), ": ") {
		t.Fatalf("Validate with no field = %v, want a bare template error", err)
	}
}

func TestSamples(t *testing.T) {
	all := Samples()
	if len(all) != len(Kinds()) {
		t.Fatalf("Samples() = %d values, want one per kind (%d)", len(all), len(Kinds()))
	}
	for i, kind := range Kinds() {
		d := all[i]
		if d.Kind != kind {
			t.Errorf("Samples()[%d].Kind = %q, want %q", i, d.Kind, kind)
		}
		if d.Path == "" || d.Label == "" {
			t.Errorf("%s sample has an empty path or label: %+v", kind, d)
		}
		if !strings.HasPrefix(d.Path, SampleHome) {
			t.Errorf("%s sample path %q is outside SampleHome", kind, d.Path)
		}
	}
	if got := Samples(KindWorktree, "nope", KindTab); len(got) != 2 || got[0].Kind != KindWorktree || got[1].Kind != KindTab {
		t.Fatalf("Samples(worktree, nope, tab) = %+v", got)
	}
	wt := Samples(KindWorktree)[0]
	if !wt.IsWorktree || wt.Branch == "" || wt.Head == "" || wt.RepoName == "" {
		t.Errorf("worktree sample lacks git data: %+v", wt)
	}
	agent := Samples(KindAgent)[0]
	if agent.Agent == "" || agent.AgentStatus == "" || agent.Workspace == "" {
		t.Errorf("agent sample lacks agent data: %+v", agent)
	}
	tab := Samples(KindTab)[0]
	if tab.TabNumber == "" || tab.TabLabel == "" {
		t.Errorf("tab sample lacks tab data: %+v", tab)
	}
	// Fresh values: mutating one call's Meta never leaks into the next.
	Samples(KindWorktree)[0].Meta["branch"] = "mutated"
	if Samples(KindWorktree)[0].Meta["branch"] == "mutated" {
		t.Fatal("Samples returned shared Meta maps")
	}
}

func TestKinds_IsFreshAndComplete(t *testing.T) {
	want := []string{"workspace", "configured", "group", "folder", "project", "worktree", "session", "agent", "tab", "pane", "custom"}
	got := Kinds()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	got[0] = "mutated"
	if Kinds()[0] != KindWorkspace {
		t.Fatal("Kinds returned a shared slice")
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []string
		wantErr string
	}{
		{name: "splits whitespace", input: "open\t/path\n--background", want: []string{"open", "/path", "--background"}},
		{name: "keeps double quoted run together", input: `open "/Users/me/My Project"`, want: []string{"open", "/Users/me/My Project"}},
		{name: "keeps single quoted run together", input: "printf 'hello world'", want: []string{"printf", "hello world"}},
		{name: "preserves empty quoted argument", input: `cmd "" ''`, want: []string{"cmd", "", ""}},
		{name: "empty input", input: "   ", want: nil},
		{name: "rejects unterminated quote", input: `open "/Users/me/My Project`, wantErr: "unterminated quote in preview command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Tokenize(tc.input)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("Tokenize(%q) error = %v, want %q", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Tokenize(%q): %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Tokenize(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

func BenchmarkRender_RowLabel(b *testing.B) {
	e := New("/home/user")
	const format = "{{if .Label}}{{.Label}}{{else}}{{.Path}}{{end}}"
	data := Data{Label: "~/Proyectos/shep", Path: "/home/user/Proyectos/shep", Source: "zoxide", Kind: KindFolder}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.Render(format, data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRender_NamePipeline(b *testing.B) {
	e := New("/home/user")
	const format = "{{ .Label | trimIcon | name }}"
	data := Data{Label: "\U000f0cc6 ~/Proyectos/shep", Path: "/home/user/Proyectos/shep", Source: "herdr", Kind: KindWorkspace}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := e.Render(format, data); err != nil {
			b.Fatal(err)
		}
	}
}
