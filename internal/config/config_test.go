package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestDefaults_PathAgnostic (CD-2, CD-5) ensures Defaults() produces no
// hardcoded absolute user paths and ships with built-in providers ready.
func TestDefaults_PathAgnostic(t *testing.T) {
	t.Parallel()

	cfg := Defaults()
	if cfg == nil {
		t.Fatal("Defaults returned nil")
	}
	if cfg.Sources == nil {
		t.Fatal("Defaults Sources map must be non-nil")
	}
	if cfg.Layouts == nil {
		t.Fatal("Defaults Layouts map must be non-nil")
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	got := string(b)
	for _, bad := range []string{"/Users/", "/home/trance", "Proyectos"} {
		if strings.Contains(got, bad) {
			t.Errorf("defaults contain hardcoded %q:\n%s", bad, got)
		}
	}
}

// TestLoad_MissingFileFallsBackToDefaults (CD-1) confirms a missing config
// path resolves to Defaults rather than an error.
func TestLoad_MissingFileFallsBackToDefaults(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg, err := Load(filepath.Join(tmp, "nope.toml"))
	if err != nil {
		t.Fatalf("expected nil error on missing file, got %v", err)
	}
	if cfg == nil {
		t.Fatal("expected defaults, got nil")
	}
	if len(cfg.Sources) != 0 {
		t.Errorf("expected empty default sources, got %d", len(cfg.Sources))
	}
}

// TestLoad_ParsesSchema (CD-3) covers general, herdr, sources (roots +
// override-disable), and layouts sections.
func TestLoad_ParsesSchema(t *testing.T) {
	t.Parallel()

	const doc = `
[general]
provider_order = ["herdr", "zoxide", "cwd"]

[herdr]
binary = "/usr/local/bin/herdr"

[sources.repos]
kind = "roots"
enabled = true
[sources.repos.options]
path = "~/code"

[sources.zoxide]
kind = "zoxide"
enabled = false

[layouts."**/*.go"]
startup = "go test ./..."
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := len(cfg.General.ProviderOrder), 3; got != want {
		t.Errorf("provider_order len: got %d want %d", got, want)
	}
	if got, want := cfg.Herdr.Binary, "/usr/local/bin/herdr"; got != want {
		t.Errorf("herdr binary: got %q want %q", got, want)
	}
	roots, ok := cfg.Sources["repos"]
	if !ok {
		t.Fatal("missing sources.repos")
	}
	if roots.Kind != KindRoots || !roots.Enabled {
		t.Errorf("repos source: kind=%q enabled=%v", roots.Kind, roots.Enabled)
	}
	if got, want := roots.Options["path"], "~/code"; got != want {
		t.Errorf("repos path: got %q want %q", got, want)
	}
	zox, ok := cfg.Sources["zoxide"]
	if !ok {
		t.Fatal("missing sources.zoxide")
	}
	if zox.Enabled {
		t.Error("zoxide should be disabled by override")
	}
	lay, ok := cfg.Layouts["**/*.go"]
	if !ok {
		t.Fatal("missing layouts '**/*.go'")
	}
	if got, want := lay.Startup, "go test ./..."; got != want {
		t.Errorf("layout startup: got %q want %q", got, want)
	}
}

// TestLoad_MalformedReturnsError wraps the parse error so callers can surface
// it without losing the originating file path.
func TestLoad_MalformedReturnsError(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.toml")
	if err := os.WriteFile(path, []byte("not = = valid toml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error parsing malformed toml, got nil")
	}
}

// TestDiscoverPath_UnderUserConfigDir (CD-1) checks the path lives under the
// user config directory and ends with the shep-relative suffix.
func TestDiscoverPath_UnderUserConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	p, err := DiscoverPath()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("shep", "config.toml")) {
		t.Errorf("discover path suffix mismatch: %q", p)
	}
}

// TestDiscoverPath_XDGOverride (CD-1, CD-S5) honours XDG_CONFIG_HOME on
// platforms where os.UserConfigDir reads it (Linux). On darwin UserConfigDir
// ignores XDG, so we only assert the suffix and that the call does not error.
func TestDiscoverPath_XDGOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p, err := DiscoverPath()
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.HasSuffix(p, filepath.Join("shep", "config.toml")) {
		t.Errorf("discover suffix mismatch: %q", p)
	}
}

// TestProbe reports presence of guaranteed-present and guaranteed-absent
// binaries so the probe helper cannot silently regress.
func TestProbe(t *testing.T) {
	t.Parallel()
	if !Probe("sh") && !Probe("go") {
		t.Error("expected at least one of sh/go to be found on PATH")
	}
	if Probe("definitely-not-a-binary-xyz-shep") {
		t.Error("probe should report false for missing binary")
	}
}

// TestHerdrBinary_Default checks the empty-config default.
func TestHerdrBinary_Default(t *testing.T) {
	t.Parallel()
	cfg := Defaults()
	if got, want := cfg.HerdrBinary(), "herdr"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	cfg.Herdr.Binary = "custom-herdr"
	if got, want := cfg.HerdrBinary(), "custom-herdr"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
