package tmpl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestREADME_ListsEveryTemplateFunction proves the README's Customization
// reference names every function templates can call (the Sprig subset and
// shep's own path, style and live functions), so a function added to the set
// cannot go undocumented.
func TestREADME_ListsEveryTemplateFunction(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	doc := string(data)
	start := strings.Index(doc, "\n## Customization\n")
	if start < 0 {
		t.Fatal(`README.md has no "## Customization" section`)
	}
	section := doc[start:]
	if end := strings.Index(section[1:], "\n## "); end >= 0 {
		section = section[:end+1]
	}
	funcs, err := buildFuncs("/home/user")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(funcs))
	for name := range funcs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !strings.Contains(section, "`"+name+"`") {
			t.Errorf("README Customization section does not list the template function %q", name)
		}
	}
}
