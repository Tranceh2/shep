package workspacename

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"unicode"

	"github.com/Masterminds/sprig/v3"
)

// Context is the data-only input exposed to workspace-name templates.
type Context struct {
	Path           string
	NormalizedPath string
	Label          string
	Source         string
}

// Name is a validated launch-only Herdr workspace label.
type Name string

var namingFunctionNames = []string{
	"osBase", "osDir", "osClean", "osExt", "osIsAbs",
	"base", "dir", "clean", "isAbs",
	"trim", "trimPrefix", "trimSuffix", "trimAll", "lower", "upper", "title",
	"replace", "contains", "hasPrefix", "hasSuffix", "nospace", "snakecase",
	"camelcase", "kebabcase", "default", "coalesce", "ternary", "splitList",
	"join", "mustSlice", "compact", "first", "last", "add", "sub", "max",
	"min", "int", "mustRegexMatch", "mustRegexReplaceAllLiteral", "regexQuoteMeta",
	"sha256sum",
}

var namingFunctionTypes = map[string]reflect.Type{
	"osBase": reflect.TypeOf(func(string) string { return "" }), "osDir": reflect.TypeOf(func(string) string { return "" }), "osClean": reflect.TypeOf(func(string) string { return "" }), "osExt": reflect.TypeOf(func(string) string { return "" }), "osIsAbs": reflect.TypeOf(func(string) bool { return false }),
	"base": reflect.TypeOf(func(string) string { return "" }), "dir": reflect.TypeOf(func(string) string { return "" }), "clean": reflect.TypeOf(func(string) string { return "" }), "isAbs": reflect.TypeOf(func(string) bool { return false }),
	"trim": reflect.TypeOf(func(string) string { return "" }), "trimPrefix": reflect.TypeOf(func(string, string) string { return "" }), "trimSuffix": reflect.TypeOf(func(string, string) string { return "" }), "trimAll": reflect.TypeOf(func(string, string) string { return "" }), "lower": reflect.TypeOf(func(string) string { return "" }), "upper": reflect.TypeOf(func(string) string { return "" }), "title": reflect.TypeOf(func(string) string { return "" }),
	"replace": reflect.TypeOf(func(string, string, string) string { return "" }), "contains": reflect.TypeOf(func(string, string) bool { return false }), "hasPrefix": reflect.TypeOf(func(string, string) bool { return false }), "hasSuffix": reflect.TypeOf(func(string, string) bool { return false }), "nospace": reflect.TypeOf(func(string) string { return "" }), "snakecase": reflect.TypeOf(func(string) string { return "" }), "camelcase": reflect.TypeOf(func(string) string { return "" }), "kebabcase": reflect.TypeOf(func(string) string { return "" }),
	"default": reflect.TypeOf(func(interface{}, ...interface{}) interface{} { return nil }), "coalesce": reflect.TypeOf(func(...interface{}) interface{} { return nil }), "ternary": reflect.TypeOf(func(interface{}, interface{}, bool) interface{} { return nil }), "splitList": reflect.TypeOf(func(string, string) []string { return nil }), "join": reflect.TypeOf(func(string, interface{}) string { return "" }), "mustSlice": reflect.TypeOf(func(interface{}, ...interface{}) (interface{}, error) { return nil, nil }), "compact": reflect.TypeOf(func(interface{}) []interface{} { return nil }), "first": reflect.TypeOf(func(interface{}) interface{} { return nil }), "last": reflect.TypeOf(func(interface{}) interface{} { return nil }),
	"add": reflect.TypeOf(func(...interface{}) int64 { return 0 }), "sub": reflect.TypeOf(func(interface{}, interface{}) int64 { return 0 }), "max": reflect.TypeOf(func(interface{}, ...interface{}) int64 { return 0 }), "min": reflect.TypeOf(func(interface{}, ...interface{}) int64 { return 0 }), "int": reflect.TypeOf(func(interface{}) int { return 0 }),
	"mustRegexMatch": reflect.TypeOf(func(string, string) (bool, error) { return false, nil }), "mustRegexReplaceAllLiteral": reflect.TypeOf(func(string, string, string) (string, error) { return "", nil }), "regexQuoteMeta": reflect.TypeOf(func(string) string { return "" }), "sha256sum": reflect.TypeOf(func(string) string { return "" }),
}

// FuncMap returns a fresh, fail-closed map containing exactly the supported
// deterministic Sprig functions.
func FuncMap() (template.FuncMap, error) {
	hermetic := sprig.HermeticTxtFuncMap()
	out := make(template.FuncMap, len(namingFunctionNames))
	for _, name := range namingFunctionNames {
		fn, ok := hermetic[name]
		if !ok {
			return nil, fmt.Errorf("workspace name function %q is unavailable in pinned Sprig", name)
		}
		if wantType, ok := namingFunctionTypes[name]; ok && reflect.TypeOf(fn) != wantType {
			return nil, fmt.Errorf("workspace name function %q changed signature", name)
		}
		out[name] = fn
	}
	return out, nil
}

// Validate parses and executes format with a representative context.
func Validate(field, format string) error {
	_, err := Render(field, format, Context{Path: "/srv/services/platform-api", NormalizedPath: "/srv/services/platform-api", Label: "platform-api", Source: "projects"})
	return err
}

// Render parses and executes a naming template, then validates the resulting
// label before returning it as the typed launch-only Name value.
func Render(field, format string, data Context) (Name, error) {
	funcs, err := FuncMap()
	if err != nil {
		return "", scopedError(field, "function map", err)
	}
	tmpl, err := template.New("workspace-name").Funcs(funcs).Parse(format)
	if err != nil {
		return "", scopedError(field, "parse", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return "", scopedError(field, "execute", err)
	}
	value := output.String()
	if strings.TrimSpace(value) == "" {
		return "", scopedError(field, "output", fmt.Errorf("output is blank"))
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", scopedError(field, "output", fmt.Errorf("output contains control character U+%04X", r))
		}
	}
	return Name(value), nil
}

func scopedError(field, phase string, err error) error {
	if field == "" {
		return fmt.Errorf("%s: %w", phase, err)
	}
	return fmt.Errorf("%s: %s: %w", field, phase, err)
}
