package effective

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// TestREADME_PrecedenceExample proves the README's precedence example
// resolves as the README says: under ~/work/legacy/app a zoxide row takes
// its icon from the first wildcard, its color from the second, its label,
// detail and marker from the zoxide defaults and its preview from the
// [[workspaces]] entry in that directory, and the entry's own row also takes
// the entry's marker.
func TestREADME_PrecedenceExample(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to expand ~ against")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "<!-- example:precedence -->\n```toml\n"
	i := strings.Index(string(data), marker)
	if i < 0 {
		t.Fatal("README.md has no precedence example")
	}
	block := string(data)[i+len(marker):]
	block = block[:strings.Index(block, "\n```\n")+1]
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 3\n"+block), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("README precedence example does not load: %v", err)
	}
	r := New(cfg)
	dir := filepath.Join(home, "work", "legacy", "app")
	zoxide := r.For(source.Candidate{Source: config.SourceZoxide, Path: dir, Label: "~/work/legacy/app"})
	def := cfg.Presentations().Zoxide
	want := source.Presentation{Icon: *cfg.Wildcards[0].Icon, IconColor: "red", Label: def.Label, Detail: def.Detail, Marker: def.Marker}
	if zoxide.Presentation != want {
		t.Errorf("zoxide row = %+v, want %+v", zoxide.Presentation, want)
	}
	if want := []string{"identity", "git"}; !reflect.DeepEqual(zoxide.Preview, want) {
		t.Errorf("zoxide row preview = %v, want the entry's %v", zoxide.Preview, want)
	}
	entry := r.For(source.Candidate{Source: config.SourceWorkspaces, Path: dir, Label: "legacy-app"})
	if entry.Presentation.Marker != "{{ pin }} legacy" || entry.Presentation.Icon != want.Icon {
		t.Errorf("entry row = %+v, want its own marker and the first wildcard's icon", entry.Presentation)
	}
}
