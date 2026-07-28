package workspacename

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRender_ExamplesAndDataContext(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		format string
		data   Context
		want   Name
	}{
		{name: "basename", format: `{{ .Path | osBase }}`, data: Context{Path: "/srv/services/platform-api"}, want: "platform-api"},
		{name: "prefix cleanup", format: `{{ .Path | osBase | trimPrefix "services-" }}`, data: Context{Path: "/srv/services/platform-api"}, want: "platform-api"},
		{name: "source condition", format: `{{ if eq .Source "zoxide" }}z-{{ end }}{{ .Path | osBase }}`, data: Context{Path: "/srv/services/platform-api", Source: "zoxide"}, want: "z-platform-api"},
		{name: "ECORP emoji", format: `✈️ {{ printf "%s/%s" (.Path | osDir | osBase) (.Path | osBase) }}`, data: Context{Path: "/srv/services/platform-api"}, want: "✈️ services/platform-api"},
		{name: "normalized hash", format: `{{ printf "%s-%s" (.Path | osBase) (slice (.NormalizedPath | sha256sum) 0 8) }}`, data: Context{Path: "/srv/services/platform-api", NormalizedPath: "/srv/services/platform-api"}, want: Name("platform-api-" + hashPrefix("/srv/services/platform-api"))},
		{name: "whitespace fallback", format: `{{ .Label | trim | default "workspace" }}`, data: Context{Label: "   "}, want: "workspace"},
		{name: "literal regex replacement", format: `{{ mustRegexReplaceAllLiteral "x" "$1" "x" }}`, data: Context{}, want: "$1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.name, tc.format, tc.data)
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
		{name: "blank", format: `{{ " \t" }}`, want: "output is blank"},
		{name: "newline", format: "line\nname", want: "U+000A"},
		{name: "nul", format: "line\x00name", want: "U+0000"},
		{name: "invalid regex", format: `{{ mustRegexMatch "[" .Path }}`, want: "execute"},
		{name: "invalid list bounds", format: `{{ mustSlice (splitList "," "a") 2 }}`, want: "execute"},
		{name: "unknown field", format: `{{ .Missing }}`, want: "execute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Render(tc.name, tc.format, Context{Path: "/tmp/project"}); err == nil || !strings.Contains(err.Error(), tc.want) {
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
		data   Context
		want   Name
	}{
		{name: "tilde is literal", format: `{{ .Path }}`, data: Context{Path: "~/services/api"}, want: "~/services/api"},
		{name: "root native", format: `{{ .Path | osClean }}`, data: Context{Path: string(filepath.Separator)}, want: Name(string(filepath.Separator))},
		{name: "symlink spelling is literal", format: `{{ .Path }}`, data: Context{Path: link}, want: Name(link)},
		{name: "dot repeated trailing native", format: `{{ .Path | osClean }}`, data: Context{Path: "./services//api/../api/"}, want: "services/api"},
		{name: "foreign backslash slash text", format: `{{ .Path | clean }}`, data: Context{Path: `C:\services\api`}, want: `C:\services\api`},
		{name: "foreign slash separator", format: `{{ .Path | clean | base }}`, data: Context{Path: `C:/services/api`}, want: "api"},
		{name: "unicode and emoji", format: `{{ .Label }}`, data: Context{Label: "✈️ ECORP api"}, want: "✈️ ECORP api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				got, err := Render(tc.name, tc.format, tc.data)
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
		data   Context
		want   Name
	}{
		{name: "native relative", format: `{{ .Path | osClean }}`, data: Context{Path: "./services/../api/"}, want: "api"},
		{name: "slash foreign windows", format: `{{ .Path | clean | base }}`, data: Context{Path: `C:\Users\dev\project`}, want: `C:\Users\dev\project`},
		{name: "unc slash", format: `{{ .Path | clean | base }}`, data: Context{Path: `//server/share/project`}, want: "project"},
		{name: "unicode spaces", format: `{{ .Label }}`, data: Context{Label: "  ✈️ platform api  "}, want: "  ✈️ platform api  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 3; i++ {
				got, err := Render(tc.name, tc.format, tc.data)
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
	name, err := Render("hash", `{{ slice (.NormalizedPath | sha256sum) 0 8 }}`, Context{NormalizedPath: path})
	if err != nil {
		panic(err)
	}
	return string(name)
}

var _ = filepath.Separator
