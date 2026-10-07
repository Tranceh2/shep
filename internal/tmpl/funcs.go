package tmpl

import (
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"unicode"

	"github.com/Masterminds/sprig/v3"
)

// sprigFunction is one allow-listed Sprig function together with the exact
// signature shep documents for it. Pinning the signature means a Sprig
// upgrade that changes a function's behaviour contract fails loudly instead
// of silently changing what user templates do.
type sprigFunction struct {
	name string
	typ  reflect.Type
}

// sprigFunctions is the hermetic, deterministic Sprig subset templates may
// call. Each function has exactly one name: Sprig's os* aliases (osBase,
// osDir...) are deliberately left out, so base/dir/clean/ext/isAbs are the
// only path helpers. Nothing here reads the environment, the clock, the
// network or randomness.
var sprigFunctions = []sprigFunction{
	{"base", reflect.TypeFor[func(string) string]()},
	{"dir", reflect.TypeFor[func(string) string]()},
	{"clean", reflect.TypeFor[func(string) string]()},
	{"ext", reflect.TypeFor[func(string) string]()},
	{"isAbs", reflect.TypeFor[func(string) bool]()},
	{"trim", reflect.TypeFor[func(string) string]()},
	{"trimPrefix", reflect.TypeFor[func(string, string) string]()},
	{"trimSuffix", reflect.TypeFor[func(string, string) string]()},
	{"trimAll", reflect.TypeFor[func(string, string) string]()},
	{"lower", reflect.TypeFor[func(string) string]()},
	{"upper", reflect.TypeFor[func(string) string]()},
	{"title", reflect.TypeFor[func(string) string]()},
	{"replace", reflect.TypeFor[func(string, string, string) string]()},
	{"contains", reflect.TypeFor[func(string, string) bool]()},
	{"hasPrefix", reflect.TypeFor[func(string, string) bool]()},
	{"hasSuffix", reflect.TypeFor[func(string, string) bool]()},
	{"nospace", reflect.TypeFor[func(string) string]()},
	{"snakecase", reflect.TypeFor[func(string) string]()},
	{"camelcase", reflect.TypeFor[func(string) string]()},
	{"kebabcase", reflect.TypeFor[func(string) string]()},
	{"default", reflect.TypeFor[func(any, ...any) any]()},
	{"coalesce", reflect.TypeFor[func(...any) any]()},
	{"ternary", reflect.TypeFor[func(any, any, bool) any]()},
	{"splitList", reflect.TypeFor[func(string, string) []string]()},
	{"join", reflect.TypeFor[func(string, any) string]()},
	{"mustSlice", reflect.TypeFor[func(any, ...any) (any, error)]()},
	{"compact", reflect.TypeFor[func(any) []any]()},
	{"first", reflect.TypeFor[func(any) any]()},
	{"last", reflect.TypeFor[func(any) any]()},
	{"add", reflect.TypeFor[func(...any) int64]()},
	{"sub", reflect.TypeFor[func(any, any) int64]()},
	{"max", reflect.TypeFor[func(any, ...any) int64]()},
	{"min", reflect.TypeFor[func(any, ...any) int64]()},
	{"int", reflect.TypeFor[func(any) int]()},
	{"mustRegexMatch", reflect.TypeFor[func(string, string) (bool, error)]()},
	{"mustRegexReplaceAllLiteral", reflect.TypeFor[func(string, string, string) (string, error)]()},
	{"regexQuoteMeta", reflect.TypeFor[func(string) string]()},
	{"sha256sum", reflect.TypeFor[func(string) string]()},
}

// reservedFunctionNames are kept free for the semantic and live-marker
// functions a later stage adds (styling roles and row markers). No function
// in the set may use them until then.
var reservedFunctionNames = []string{"muted", "accent", "bold", "status", "pin", "current", "group", "missing"}

// buildFuncs assembles the complete function map for an engine whose home
// directory is home. It fails closed: a missing Sprig function, an entry
// without a declared signature, a changed signature or a name collision is an
// error rather than a silently smaller or different function set.
func buildFuncs(home string) (template.FuncMap, error) {
	hermetic := sprig.HermeticTxtFuncMap()
	shep := shepFuncs(home)
	out := make(template.FuncMap, len(sprigFunctions)+len(shep))
	for _, fn := range sprigFunctions {
		if fn.typ == nil {
			return nil, fmt.Errorf("function %q has no declared signature", fn.name)
		}
		impl, ok := hermetic[fn.name]
		if !ok {
			return nil, fmt.Errorf("function %q is unavailable in the pinned Sprig", fn.name)
		}
		if got := reflect.TypeOf(impl); got != fn.typ {
			return nil, fmt.Errorf("function %q changed signature: %v, want %v", fn.name, got, fn.typ)
		}
		if _, dup := out[fn.name]; dup {
			return nil, fmt.Errorf("function %q is declared twice", fn.name)
		}
		out[fn.name] = impl
	}
	for name, impl := range shep {
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("shep function %q collides with a Sprig function", name)
		}
		out[name] = impl
	}
	for _, name := range reservedFunctionNames {
		if _, used := out[name]; used {
			return nil, fmt.Errorf("function name %q is reserved", name)
		}
	}
	return out, nil
}

// shepFuncs returns shep's own helpers. tilde closes over the engine's home
// directory; the others are pure.
func shepFuncs(home string) template.FuncMap {
	return template.FuncMap{
		"tilde":    tildeFunc(home),
		"name":     pathName,
		"parent":   pathParent,
		"trimIcon": trimIcon,
	}
}

// tildeFunc returns the tilde helper: s with a leading home directory
// replaced by "~" ("/home/me/x" → "~/x", "/home/me" → "~"). A path that only
// shares a textual prefix with home ("/home/me2") and every string when home
// is empty or "/" are returned unchanged.
func tildeFunc(home string) func(string) string {
	return func(s string) string {
		if home == "" || home == "/" || !strings.HasPrefix(s, home) {
			return s
		}
		if rest := s[len(home):]; rest == "" || rest[0] == '/' {
			return "~" + rest
		}
		return s
	}
}

// isPathLike reports whether s reads as a filesystem path for the name and
// parent helpers: absolute ("/...") or home-relative ("~/...").
func isPathLike(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/")
}

// trimOneSlash drops one trailing "/" from a path-like string, keeping a bare
// root "/" intact.
func trimOneSlash(s string) string {
	if len(s) > 1 {
		return strings.TrimSuffix(s, "/")
	}
	return s
}

// pathName is the name helper: the last element of a path-like string
// ("~/foo" → "foo", "/a/b/" → "b", "/" → "/"); any other string, including
// a relative path, is returned unchanged ("Proyectos/shep" stays as is).
func pathName(s string) string {
	if !isPathLike(s) {
		return s
	}
	t := trimOneSlash(s)
	if t == "/" {
		return t
	}
	i := strings.LastIndexByte(t, '/')
	if i < 0 {
		return t // "~/" trimmed to "~"
	}
	return t[i+1:]
}

// pathParent is the parent helper: the parent of a path-like string ("~/foo"
// → "~", "~/a/b" → "~/a", "/foo" → "/"), or "" when s is not path-like or
// has no parent ("~", "/").
func pathParent(s string) string {
	if !isPathLike(s) {
		return ""
	}
	t := trimOneSlash(s)
	if t == "/" || t == "~" {
		return ""
	}
	switch i := strings.LastIndexByte(t, '/'); i {
	case -1:
		return ""
	case 0:
		return "/"
	default:
		return t[:i]
	}
}

// trimIcon drops the leading run of runes that are neither letters, digits
// nor one of "~/._-@" — icons, emoji with their variation selectors, symbols
// and spaces — so " ~/ops/x" → "~/ops/x" and "✈️ x" → "x". A string made only
// of such runes trims to "".
func trimIcon(s string) string {
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("~/._-@", r) {
			return s[i:]
		}
	}
	return ""
}
