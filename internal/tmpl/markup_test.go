package tmpl

import (
	"reflect"
	"strings"
	"testing"
)

// mark is the encoding of one sentinel rune, for building expected output.
func mark(r rune) string { return string(r) }

func TestMarkupFunctions_Encode(t *testing.T) {
	e := New("")
	cases := []struct {
		format string
		want   string
	}{
		{`{{ muted "x" }}`, mark(runeMuted) + "x" + mark(runeEnd)},
		{`{{ accent "x" }}`, mark(runeAccent) + "x" + mark(runeEnd)},
		{`{{ bold "x" }}`, mark(runeBold) + "x" + mark(runeEnd)},
		{`{{ .Label | muted }}`, mark(runeMuted) + "shep" + mark(runeEnd)},
		{`{{ status }}{{ pin }}{{ current }}{{ group }}{{ missing }}`, mark(runeStatus) + mark(runePin) + mark(runeCurrent) + mark(runeGroup) + mark(runeMissing)},
	}
	for _, tc := range cases {
		got, err := e.Render(tc.format, Data{Label: "shep"})
		if err != nil {
			t.Fatalf("Render(%q): %v", tc.format, err)
		}
		if got != tc.want {
			t.Errorf("Render(%q) = %q, want %q", tc.format, got, tc.want)
		}
	}
}

func TestSegments_RoundTrip(t *testing.T) {
	e := New("")
	cases := []struct {
		name   string
		format string
		data   Data
		want   []Segment
	}{
		{name: "plain text is one segment", format: "{{ .Label }}", data: Data{Label: "shep"}, want: []Segment{{Text: "shep"}}},
		{name: "empty output has no segment", format: "{{ .Label }}", data: Data{}, want: nil},
		{
			name: "styled runs keep their order", format: `{{ muted .TabNumber }} {{ .TabLabel }}`, data: Data{TabNumber: "2", TabLabel: "editor"},
			want: []Segment{{Text: "2", Style: StyleMuted}, {Text: " editor"}},
		},
		{
			name: "accent and bold", format: `a{{ accent "b" }}c{{ bold "d" }}`,
			want: []Segment{{Text: "a"}, {Text: "b", Style: StyleAccent}, {Text: "c"}, {Text: "d", Style: StyleBold}},
		},
		{
			name: "live markers between text", format: `{{ current }} {{ status }} {{ pin }}`,
			want: []Segment{{Live: LiveCurrent}, {Text: " "}, {Live: LiveStatus}, {Text: " "}, {Live: LivePin}},
		},
		{
			name: "every live kind", format: `{{ group }}{{ missing }}`,
			want: []Segment{{Live: LiveGroup}, {Live: LiveMissing}},
		},
		{
			name: "bold adds to an inner color", format: `{{ bold (muted "x") }}`,
			want: []Segment{{Text: "x", Style: StyleMuted | StyleBold}},
		},
		{
			name: "an inner color replaces the outer one", format: `{{ muted (printf "%s%s" (accent "a") "b") }}`,
			want: []Segment{{Text: "a", Style: StyleAccent}, {Text: "b", Style: StyleMuted}},
		},
		{
			name: "outer style resumes after an inner one", format: `{{ bold (printf "a%sc" (muted "b")) }}`,
			want: []Segment{{Text: "a", Style: StyleBold}, {Text: "b", Style: StyleBold | StyleMuted}, {Text: "c", Style: StyleBold}},
		},
		{
			name: "a live marker carries its enclosing style", format: `{{ muted (printf "%s" current) }}`,
			want: []Segment{{Style: StyleMuted, Live: LiveCurrent}},
		},
		{name: "empty styled text has no segment", format: `{{ muted "" }}x`, want: []Segment{{Text: "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := e.Render(tc.format, tc.data)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got := Segments(out); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Segments(%q) = %+v, want %+v", out, got, tc.want)
			}
		})
	}
}

func TestSegments_MalformedMarkupIsLenient(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Segment
	}{
		{"stray end is ignored", "a" + mark(runeEnd) + "b", []Segment{{Text: "a"}, {Text: "b"}}},
		{"unclosed style runs to the end", mark(runeMuted) + "ab", []Segment{{Text: "ab", Style: StyleMuted}}},
		{"part separator is dropped", "a" + PartSeparator + "b", []Segment{{Text: "a"}, {Text: "b"}}},
		{"other reserved runes are dropped", "a\ufde0b", []Segment{{Text: "a"}, {Text: "b"}}},
		{"neighbouring Arabic ligature is text", "\ufdfa", []Segment{{Text: "\ufdfa"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Segments(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Segments(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSegments_DeepNestingStaysBounded(t *testing.T) {
	inner := `(muted "x")`
	for range 20 {
		inner = "(bold " + inner + ")"
	}
	out, err := New("").Render("{{ "+inner+" }}", Data{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := Segments(out); len(got) != 1 || got[0].Text != "x" || got[0].Style != StyleMuted|StyleBold {
		t.Fatalf("Segments = %+v, want one muted bold x", got)
	}
}

// TestRender_DataCannotInjectMarkup proves provider text never styles a row
// or fakes a live marker: every reserved rune is removed from every Data
// field and Meta key and value before the template runs, and the caller's
// Meta map is left untouched.
func TestRender_DataCannotInjectMarkup(t *testing.T) {
	e := New("")
	inject := mark(runeMuted) + "x" + mark(runeEnd) + mark(runePin) + PartSeparator
	meta := map[string]string{"k": inject, inject: "v"}
	d := Data{
		Path: inject, NormalizedPath: inject, Label: inject, Source: inject, Kind: inject, Icon: inject,
		Branch: inject, Head: inject, RepoName: inject, Agent: inject, AgentStatus: inject,
		TabNumber: inject, TabLabel: inject, Workspace: inject, Meta: meta,
	}
	format := "{{ .Path }}{{ .NormalizedPath }}{{ .Label }}{{ .Source }}{{ .Kind }}{{ .Icon }}{{ .Branch }}{{ .Head }}" +
		"{{ .RepoName }}{{ .Agent }}{{ .AgentStatus }}{{ .TabNumber }}{{ .TabLabel }}{{ .Workspace }}" +
		"{{ .Meta.k }}{{ range $k, $v := .Meta }}{{ $k }}{{ end }}"
	out, err := e.Render(format, d)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if HasMarkup(out) {
		t.Fatalf("output %q carries markup injected through data", out)
	}
	if want := strings.Repeat("x", 15) + "kx"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if meta["k"] != inject {
		t.Error("Render modified the caller's Meta map")
	}
	if got := Segments(out); len(got) != 1 || got[0].Style != 0 || got[0].Live != LiveNone {
		t.Errorf("Segments = %+v, want one plain segment", got)
	}
}

func TestStripMarkup(t *testing.T) {
	plain := "~/Proyectos/shep ✈️ \ufdcf\ufdf0"
	if got := StripMarkup(plain); got != plain {
		t.Errorf("StripMarkup(%q) = %q, want it unchanged (no reserved rune)", plain, got)
	}
	if got := StripMarkup("a" + mark(runeBold) + "b" + PartSeparator); got != "ab" {
		t.Errorf("StripMarkup = %q, want ab", got)
	}
	if HasMarkup(plain) || !HasMarkup(mark(runeEnd)) || !HasMarkup(PartSeparator) {
		t.Error("HasMarkup misclassifies the reserved range")
	}
}

func TestRenderPlain_RejectsMarkup(t *testing.T) {
	e := New("")
	if got, err := e.RenderPlain("{{ .Label | upper }}", Data{Label: "api"}); err != nil || got != "API" {
		t.Fatalf("RenderPlain = %q, %v; want API", got, err)
	}
	for _, format := range []string{`{{ muted .Label }}`, `{{ .Label }} {{ pin }}`, "x" + mark(runeBold)} {
		if _, err := e.RenderPlain(format, Data{Label: "api"}); err == nil || !strings.Contains(err.Error(), "only apply to row templates") {
			t.Errorf("RenderPlain(%q) error = %v, want the row-template error", format, err)
		}
	}
}

func TestValidatePlain_NamesTheMarkupFunctionInAnyBranch(t *testing.T) {
	e := New(SampleHome)
	if err := e.ValidatePlain("general.workspace_name", "{{ .Path | base }}"); err != nil {
		t.Fatalf("ValidatePlain(plain): %v", err)
	}
	cases := []struct {
		format, name string
	}{
		{`{{ muted .Label }}`, "muted"},
		{`{{ .Label | accent }}`, "accent"},
		{`{{ if eq .Kind "nope" }}{{ bold .Label }}{{ end }}`, "bold"},
		{`{{ with .Meta.none }}x{{ else }}{{ status }}{{ end }}`, "status"},
		{`{{ range $i, $c := splitList "," "a" }}{{ pin }}{{ end }}`, "pin"},
		{`{{ define "x" }}{{ current }}{{ end }}{{ .Label }}`, "current"},
		{`{{ printf "%s" group }}`, "group"},
		{`{{ $m := missing }}{{ .Label }}`, "missing"},
	}
	for _, tc := range cases {
		err := e.ValidatePlain("general.workspace_name", tc.format)
		if err == nil || !strings.HasPrefix(err.Error(), "general.workspace_name: "+tc.name+" is a row presentation function") {
			t.Errorf("ValidatePlain(%q) error = %v, want it to name %s", tc.format, err, tc.name)
		}
	}
	if err := e.ValidatePlain("f", "x"+mark(runeBold)+"y"); err == nil || !strings.Contains(err.Error(), "only apply to row templates") {
		t.Errorf("ValidatePlain(literal markup) error = %v, want the row-template error", err)
	}
}

func TestValidate_AcceptsMarkupFunctions(t *testing.T) {
	e := New(SampleHome)
	format := `{{ muted .TabNumber }} {{ accent (bold .Label) }} {{ status }} {{ pin }} {{ current }} {{ group }} {{ missing }}`
	if err := e.Validate("sources.herdr.tab.label_format", format, Samples(KindTab)...); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := e.Validate("f", `{{ muted 3 }}`); err == nil {
		t.Fatal("Validate accepted a style function applied to a number")
	}
}

func TestEngine_Tilde(t *testing.T) {
	e := New("/home/me")
	if got := e.Tilde("/home/me/x"); got != "~/x" {
		t.Errorf("Tilde = %q, want ~/x", got)
	}
	if got := e.Tilde("/home/me2"); got != "/home/me2" {
		t.Errorf("Tilde = %q, want it unchanged", got)
	}
}

func TestLive_StringNamesTheFunction(t *testing.T) {
	for live, want := range map[Live]string{LiveNone: "", LiveStatus: "status", LivePin: "pin", LiveCurrent: "current", LiveGroup: "group", LiveMissing: "missing"} {
		if got := live.String(); got != want {
			t.Errorf("Live(%d).String() = %q, want %q", live, got, want)
		}
	}
}
