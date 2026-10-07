package workspacename_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/tmpl"
	"github.com/tranceh2/shep/internal/workspacename"
)

func TestRender_ExamplesAndData(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		format string
		data   tmpl.Data
		want   workspacename.Name
	}{
		{name: "basename", format: `{{ .Path | base }}`, data: tmpl.Data{Path: "/srv/services/platform-api"}, want: "platform-api"},
		{name: "prefix cleanup", format: `{{ .Path | base | trimPrefix "services-" }}`, data: tmpl.Data{Path: "/srv/services/platform-api"}, want: "platform-api"},
		{name: "source condition", format: `{{ if eq .Source "zoxide" }}z-{{ end }}{{ .Path | base }}`, data: tmpl.Data{Path: "/srv/services/platform-api", Source: "zoxide"}, want: "z-platform-api"},
		{name: "airplane emoji", format: `✈️ {{ printf "%s/%s" (.Path | dir | base) (.Path | base) }}`, data: tmpl.Data{Path: "/srv/services/platform-api"}, want: "✈️ services/platform-api"},
		{name: "normalized hash", format: `{{ printf "%s-%s" (.Path | base) (slice (.NormalizedPath | sha256sum) 0 8) }}`, data: tmpl.Data{Path: "/srv/services/platform-api", NormalizedPath: "/srv/services/platform-api"}, want: workspacename.Name("platform-api-" + hashPrefix("/srv/services/platform-api"))},
		{name: "whitespace fallback", format: `{{ .Label | trim | default "workspace" }}`, data: tmpl.Data{Label: "   "}, want: "workspace"},
		{name: "literal regex replacement", format: `{{ mustRegexReplaceAllLiteral "x" "$1" "x" }}`, data: tmpl.Data{}, want: "$1"},
		{name: "tilde and parent", format: `{{ .Path | tilde | parent }}`, data: tmpl.Data{Path: "/home/me/services/api"}, want: "~/services"},
		{name: "kind and missing meta", format: `{{ .Kind }}{{ .Meta.nope }}`, data: tmpl.Data{Kind: tmpl.KindFolder}, want: "folder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspacename.Render(engine, tc.name, tc.format, tc.data)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRender_RejectsBlankControlsAndInvalidTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		format string
		want   string
	}{
		{name: "blank", format: `{{ " \t" }}`, want: "blank: output is blank"},
		{name: "blank for this row", format: `{{ .Branch }}`, want: "blank for this row: output is blank"},
		{name: "newline", format: "line\nname", want: "U+000A"},
		{name: "nul", format: "line\x00name", want: "U+0000"},
		{name: "invalid regex", format: `{{ mustRegexMatch "[" .Path }}`, want: "invalid regex: template: shep"},
		{name: "invalid list bounds", format: `{{ mustSlice (splitList "," "a") 2 }}`, want: "invalid list bounds: template: shep"},
		{name: "unknown field", format: `{{ .Missing }}`, want: "can't evaluate field Missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := workspacename.Render(engine, tc.name, tc.format, tmpl.Data{Path: "/tmp/project"}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Render error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRender_PortablePathMatrix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "target"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cases := []struct {
		name   string
		format string
		data   tmpl.Data
		want   workspacename.Name
	}{
		{name: "tilde is literal", format: `{{ .Path }}`, data: tmpl.Data{Path: "~/services/api"}, want: "~/services/api"},
		{name: "root", format: `{{ .Path | clean }}`, data: tmpl.Data{Path: "/"}, want: "/"},
		{name: "symlink spelling is literal", format: `{{ .Path }}`, data: tmpl.Data{Path: link}, want: workspacename.Name(link)},
		{name: "dot repeated trailing", format: `{{ .Path | clean }}`, data: tmpl.Data{Path: "./services//api/../api/"}, want: "services/api"},
		{name: "foreign backslash slash text", format: `{{ .Path | clean }}`, data: tmpl.Data{Path: `C:\services\api`}, want: `C:\services\api`},
		{name: "foreign slash separator", format: `{{ .Path | clean | base }}`, data: tmpl.Data{Path: `C:/services/api`}, want: "api"},
		{name: "unicode and emoji", format: `{{ .Label }}`, data: tmpl.Data{Label: "✈️ fsociety api"}, want: "✈️ fsociety api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 2 {
				got, err := workspacename.Render(engine, tc.name, tc.format, tc.data)
				if err != nil {
					t.Fatalf("Render: %v", err)
				}
				if got != tc.want {
					t.Fatalf("Render = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestRender_IsDeterministicAndPreservesPathSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		format string
		data   tmpl.Data
		want   workspacename.Name
	}{
		{name: "relative", format: `{{ .Path | clean }}`, data: tmpl.Data{Path: "./services/../api/"}, want: "api"},
		{name: "slash foreign windows", format: `{{ .Path | clean | base }}`, data: tmpl.Data{Path: `C:\Users\dev\project`}, want: `C:\Users\dev\project`},
		{name: "unc slash", format: `{{ .Path | clean | base }}`, data: tmpl.Data{Path: `//server/share/project`}, want: "project"},
		{name: "unicode spaces", format: `{{ .Label }}`, data: tmpl.Data{Label: "  ✈️ platform api  "}, want: "  ✈️ platform api  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 3 {
				got, err := workspacename.Render(engine, tc.name, tc.format, tc.data)
				if err != nil {
					t.Fatalf("Render: %v", err)
				}
				if got != tc.want {
					t.Fatalf("Render = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func hashPrefix(path string) string {
	name, err := workspacename.Render(engine, "hash", `{{ slice (.NormalizedPath | sha256sum) 0 8 }}`, tmpl.Data{NormalizedPath: path})
	if err != nil {
		panic(err)
	}
	return string(name)
}
