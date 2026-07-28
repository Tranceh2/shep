package workspacename

import (
	"reflect"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
)

func TestFuncMap_ExactHermeticAllowListAndTypes(t *testing.T) {
	got, err := FuncMap()
	if err != nil {
		t.Fatalf("FuncMap: %v", err)
	}
	want := sprig.HermeticTxtFuncMap()
	if len(got) != len(namingFunctionNames) {
		t.Fatalf("function count = %d, want %d", len(got), len(namingFunctionNames))
	}
	for _, name := range namingFunctionNames {
		wantFn, ok := want[name]
		if !ok {
			t.Fatalf("test whitelist contains missing Sprig function %q", name)
		}
		gotFn, ok := got[name]
		if !ok {
			t.Errorf("missing allow-listed function %q", name)
			continue
		}
		if reflect.TypeOf(gotFn) != reflect.TypeOf(wantFn) {
			t.Errorf("%s type = %T, want %T", name, gotFn, wantFn)
		}
	}
	for name := range got {
		if !containsName(namingFunctionNames, name) {
			t.Errorf("unexpected function in naming map: %q", name)
		}
	}
}

func TestFuncMap_ForbiddenSurfaceIsAbsentAndUnusable(t *testing.T) {
	got, err := FuncMap()
	if err != nil {
		t.Fatalf("FuncMap: %v", err)
	}
	for _, name := range []string{
		"env", "expandenv", "now", "uuidv4", "getHostByName", "randAlpha", "randInt",
		"bcrypt", "genPrivateKey", "get", "set", "merge", "deepCopy", "typeOf",
		"toJson", "fromYaml", "urlParse", "semver", "include", "required", "tpl", "lookup",
	} {
		if _, ok := got[name]; ok {
			t.Errorf("forbidden function %q is present", name)
		}
	}
	if _, err := Render("test", "{{ env \"HOME\" }}", Context{}); err == nil {
		t.Fatal("forbidden env function rendered successfully")
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

var _ template.FuncMap
