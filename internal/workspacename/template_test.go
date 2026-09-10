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

func TestWorkspaceNameContext_WorktreeTemplatesAndDefaults(t *testing.T) {
	t.Parallel()
	meta := map[string]string{"is_worktree": "true", "repo": "shep", "branch": "feature/x", "head": "abcdef0123456789"}
	ctx := NewContext("/trees/shep-feature", "/trees/shep-feature", "shep (feature/x)", "projects", meta)

	cases := []struct {
		name   string
		format string
		ctx    Context
		want   Name
	}{
		{name: "custom worktree fields", format: `{{.RepoName}}@{{.Branch}}`, ctx: ctx, want: "shep@feature/x"},
		{name: "default worktree", format: "", ctx: ctx, want: "shep@feature/x"},
		{name: "detached fallback", format: "", ctx: NewContext("/trees/detached", "/trees/detached", "detached", "projects", map[string]string{"is_worktree": "true", "repo": "repo", "head": "1234567890abcdef"}), want: "repo@1234567"},
		{name: "bare repo linked worktree", format: "", ctx: NewContext("/trees/bare-linked", "/trees/bare-linked", "bare-repo (bugfix)", "projects", map[string]string{"is_worktree": "true", "repo": "bare-repo", "branch": "bugfix"}), want: "bare-repo@bugfix"},
		{name: "legacy fields", format: `{{.Path}}|{{.NormalizedPath}}|{{.Label}}`, ctx: Context{Path: "/raw", NormalizedPath: "/normalized", Label: "label"}, want: "/raw|/normalized|label"},
		{name: "standard default", format: "", ctx: Context{NormalizedPath: "/srv/projects/web-app"}, want: "web-app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.name, tc.format, tc.ctx)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkspaceNameContext_IsMainWorktreeAndConditionalNaming(t *testing.T) {
	t.Parallel()

	// 1. Primary checkout
	primaryMeta := map[string]string{
		"is_worktree":   "true",
		"main_worktree": "true",
		"repo":          "shep",
		"branch":        "main",
		"head":          "1111222233334444",
	}
	primaryCtx := NewContext("/srv/shep", "/srv/shep", "shep", "projects", primaryMeta)
	if !primaryCtx.IsWorktree {
		t.Errorf("primaryCtx.IsWorktree = false, want true")
	}
	if !primaryCtx.IsMainWorktree {
		t.Errorf("primaryCtx.IsMainWorktree = false, want true")
	}

	// 2. Secondary linked worktree (with branch)
	linkedMeta := map[string]string{
		"is_worktree":   "true",
		"main_worktree": "false",
		"repo":          "shep",
		"branch":        "feature/auth",
		"head":          "2222333344445555",
	}
	linkedCtx := NewContext("/trees/shep-auth", "/trees/shep-auth", "shep (feature/auth)", "projects", linkedMeta)
	if !linkedCtx.IsWorktree {
		t.Errorf("linkedCtx.IsWorktree = false, want true")
	}
	if linkedCtx.IsMainWorktree {
		t.Errorf("linkedCtx.IsMainWorktree = true, want false")
	}

	// 3. Secondary linked worktree (detached)
	detachedMeta := map[string]string{
		"is_worktree":   "true",
		"main_worktree": "false",
		"repo":          "shep",
		"head":          "abcdef0123456789",
	}
	detachedCtx := NewContext("/trees/shep-detached", "/trees/shep-detached", "shep (abcdef0)", "projects", detachedMeta)
	if !detachedCtx.IsWorktree {
		t.Errorf("detachedCtx.IsWorktree = false, want true")
	}
	if detachedCtx.IsMainWorktree {
		t.Errorf("detachedCtx.IsMainWorktree = true, want false")
	}
	if detachedCtx.Branch != "abcdef0" {
		t.Errorf("detachedCtx.Branch = %q, want %q", detachedCtx.Branch, "abcdef0")
	}

	// 4. Ordinary project (no worktree metadata)
	ordinaryCtx := NewContext("/srv/ordinary", "/srv/ordinary", "ordinary", "projects", nil)
	if ordinaryCtx.IsWorktree {
		t.Errorf("ordinaryCtx.IsWorktree = true, want false")
	}
	if ordinaryCtx.IsMainWorktree {
		t.Errorf("ordinaryCtx.IsMainWorktree = true, want false")
	}

	// 5. Conditional naming template:
	// Preserves path-based name for main checkout and ordinary project,
	// but returns <repo>@<branch> or <repo>@<short-sha> for secondary linked worktrees.
	conditionalFormat := `{{ if and .IsWorktree (not .IsMainWorktree) }}{{ .RepoName }}@{{ .Branch }}{{ else }}{{ .Path | osBase }}{{ end }}`

	templateCases := []struct {
		name string
		ctx  Context
		want Name
	}{
		{name: "primary checkout uses path base", ctx: primaryCtx, want: "shep"},
		{name: "ordinary project uses path base", ctx: ordinaryCtx, want: "ordinary"},
		{name: "secondary linked worktree with branch", ctx: linkedCtx, want: "shep@feature/auth"},
		{name: "secondary linked worktree detached sha", ctx: detachedCtx, want: "shep@abcdef0"},
	}

	for _, tc := range templateCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.name, conditionalFormat, tc.ctx)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
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
