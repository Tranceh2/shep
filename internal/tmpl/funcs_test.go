package tmpl

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Masterminds/sprig/v3"
)

// shepFunctionNames lists shep's own helpers; together with sprigFunctions it
// is the complete, exact function set.
var shepFunctionNames = []string{"tilde", "name", "parent", "trimIcon"}

func TestBuildFuncs_ExactAllowListAndTypes(t *testing.T) {
	got, err := buildFuncs("/home/user")
	if err != nil {
		t.Fatalf("buildFuncs: %v", err)
	}
	if want := len(sprigFunctions) + len(shepFunctionNames); len(got) != want {
		t.Fatalf("function count = %d, want %d", len(got), want)
	}
	hermetic := sprig.HermeticTxtFuncMap()
	for _, fn := range sprigFunctions {
		impl, ok := got[fn.name]
		if !ok {
			t.Errorf("missing allow-listed function %q", fn.name)
			continue
		}
		if reflect.TypeOf(impl) != reflect.TypeOf(hermetic[fn.name]) {
			t.Errorf("%s type = %T, want %T", fn.name, impl, hermetic[fn.name])
		}
		if reflect.TypeOf(impl) != fn.typ {
			t.Errorf("%s type = %T, declared %v", fn.name, impl, fn.typ)
		}
	}
	for _, name := range shepFunctionNames {
		if _, ok := got[name]; !ok {
			t.Errorf("missing shep function %q", name)
		}
	}
	for name := range got {
		declared := slices.ContainsFunc(sprigFunctions, func(fn sprigFunction) bool { return fn.name == name })
		if !declared && !slices.Contains(shepFunctionNames, name) {
			t.Errorf("unexpected function %q in the set", name)
		}
	}
}

// TestSprigFunctions_EveryEntryDeclaresAType keeps the signature check
// strict: an allow-listed name without a declared type fails here (and makes
// New panic) instead of being silently accepted.
func TestSprigFunctions_EveryEntryDeclaresAType(t *testing.T) {
	seen := map[string]bool{}
	for _, fn := range sprigFunctions {
		if fn.typ == nil {
			t.Errorf("function %q has no declared type", fn.name)
		}
		if seen[fn.name] {
			t.Errorf("function %q is declared twice", fn.name)
		}
		seen[fn.name] = true
	}
}

func TestBuildFuncs_CanonicalPathNamesOnly(t *testing.T) {
	got, err := buildFuncs("")
	if err != nil {
		t.Fatalf("buildFuncs: %v", err)
	}
	for _, name := range []string{"base", "dir", "clean", "ext", "isAbs"} {
		if _, ok := got[name]; !ok {
			t.Errorf("canonical path function %q is missing", name)
		}
	}
	for _, name := range []string{"osBase", "osDir", "osClean", "osExt", "osIsAbs"} {
		if _, ok := got[name]; ok {
			t.Errorf("duplicate os* alias %q is present", name)
		}
	}
}

func TestBuildFuncs_ForbiddenSurfaceIsAbsentAndUnusable(t *testing.T) {
	got, err := buildFuncs("")
	if err != nil {
		t.Fatalf("buildFuncs: %v", err)
	}
	for _, name := range []string{
		"env", "expandenv", "now", "date", "uuidv4", "getHostByName", "randAlpha", "randInt",
		"bcrypt", "genPrivateKey", "get", "set", "unset", "merge", "deepCopy", "typeOf",
		"toJson", "fromYaml", "urlParse", "semver", "include", "required", "tpl", "lookup",
	} {
		if _, ok := got[name]; ok {
			t.Errorf("forbidden function %q is present", name)
		}
	}
	e := New("")
	if _, err := e.Render(`{{ env "HOME" }}`, Data{}); err == nil {
		t.Fatal("forbidden env function rendered successfully")
	}
}

func TestBuildFuncs_ReservedNamesStayFree(t *testing.T) {
	got, err := buildFuncs("")
	if err != nil {
		t.Fatalf("buildFuncs: %v", err)
	}
	for _, name := range reservedFunctionNames {
		if _, ok := got[name]; ok {
			t.Errorf("reserved function name %q is in use", name)
		}
	}
}

func TestTilde(t *testing.T) {
	cases := []struct {
		home, in, want string
	}{
		{"/home/me", "/home/me/Proyectos/shep", "~/Proyectos/shep"},
		{"/home/me", "/home/me", "~"},
		{"/home/me", "/home/me/", "~/"},
		{"/home/me", "/home/me2/x", "/home/me2/x"},
		{"/home/me", "/srv/x", "/srv/x"},
		{"/home/me", "", ""},
		{"/home/me", "~/already", "~/already"},
		{"/home/me", "relative/home/me", "relative/home/me"},
		{"", "/home/me/x", "/home/me/x"},
		{"/", "/x", "/x"},
	}
	for _, tc := range cases {
		if got := tildeFunc(tc.home)(tc.in); got != tc.want {
			t.Errorf("tilde(home=%q)(%q) = %q, want %q", tc.home, tc.in, got, tc.want)
		}
	}
}

func TestTilde_EngineHomeIsCleaned(t *testing.T) {
	e := New("/home/me/")
	got, err := e.Render("{{ .Path | tilde }}", Data{Path: "/home/me/x"})
	if err != nil || got != "~/x" {
		t.Fatalf("Render = %q, %v; want ~/x", got, err)
	}
	if e.Home() != "/home/me" {
		t.Fatalf("Home() = %q, want /home/me", e.Home())
	}
}

func TestName(t *testing.T) {
	cases := map[string]string{
		"~/foo":          "foo",
		"~/a/b":          "b",
		"/foo":           "foo",
		"/a/b/":          "b",
		"~/foo/":         "foo",
		"/":              "/",
		"~/":             "~",
		"~":              "~",
		"":               "",
		"Proyectos/shep": "Proyectos/shep",
		" ~/ops/x":       " ~/ops/x",
		"✈️ api":         "✈️ api",
		"~/✈️ trips/api": "api",
		"plain":          "plain",
	}
	for in, want := range cases {
		if got := pathName(in); got != want {
			t.Errorf("name(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParent(t *testing.T) {
	cases := map[string]string{
		"~/foo":          "~",
		"~/a/b":          "~/a",
		"~/a/b/":         "~/a",
		"/foo":           "/",
		"/a/b":           "/a",
		"/a/b/":          "/a",
		"~":              "",
		"~/":             "",
		"/":              "",
		"":               "",
		"Proyectos/shep": "",
		" ~/ops/x":       "",
		"✈️ api":         "",
	}
	for in, want := range cases {
		if got := pathParent(in); got != want {
			t.Errorf("parent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimIcon(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		" ~/ops/x":           "~/ops/x",
		"  ECORP":            "ECORP",
		"✈️ x":               "x",    // emoji + VS16 + space
		"✈️":                 "",     // only an icon
		"\U000f0cc6 shep":    "shep", // nerd-font private-use glyph
		"  api":             "api",
		"   ":                "",
		"shep":               "shep",
		"~/Proyectos":        "~/Proyectos",
		"/srv/api":           "/srv/api",
		".dotfiles":          ".dotfiles",
		"_private":           "_private",
		"-flag":              "-flag",
		"@scope/pkg":         "@scope/pkg",
		"42 answers":         "42 answers",
		"★ ñandú":            "ñandú",
		"👩‍💻 dev":            "dev", // ZWJ sequence
		"x ✈️ keeps the end": "x ✈️ keeps the end",
	}
	for in, want := range cases {
		if got := trimIcon(in); got != want {
			t.Errorf("trimIcon(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShepFunctions_ComposeInTemplates(t *testing.T) {
	e := New("/home/me")
	d := Data{Label: "\U000f0cc6 ~/Proyectos/shep", Path: "/home/me/Proyectos/shep"}
	cases := map[string]string{
		"{{ .Label | trimIcon | name }}":   "shep",
		"{{ .Label | trimIcon | parent }}": "~/Proyectos",
		"{{ .Path | tilde | parent }}":     "~/Proyectos",
		"{{ .Path | tilde | name }}":       "shep",
		"{{ .Path | base | upper }}":       "SHEP",
		"{{ .Path | dir | base }}":         "Proyectos",
		"{{ .Path | ext }}":                "",
	}
	for format, want := range cases {
		got, err := e.Render(format, d)
		if err != nil {
			t.Fatalf("Render(%q): %v", format, err)
		}
		if got != want {
			t.Errorf("Render(%q) = %q, want %q", format, got, want)
		}
	}
}
