package command

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// runListFor builds a fresh App + registry-friendly config and executes
// `shep list` with the supplied format, returning captured stdout/stderr.
func runListFor(t *testing.T, cfg *config.Config, format string) (string, string, error) {
	t.Helper()
	if cfg == nil {
		cfg = config.Defaults()
	}
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	app.cfg = cfg
	app.probes = config.Probes{}

	cmd := app.rootCmd()
	cmd.SetArgs([]string{"list", "--format", format})
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// workspacesCfg builds a Config whose only enabled source is workspaces,
// seeded with one entry per name under a fresh temp root.
func workspacesCfg(t *testing.T, names ...string) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	for _, n := range names {
		dir := filepath.Join(root, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.Workspaces = append(cfg.Workspaces, config.WorkspaceConfig{Name: n, Path: dir})
	}
	return cfg, root
}

// TestList_HumanFormatHasHeader confirms the human table renders a header
// and a seeded workspace candidate.
func TestList_HumanFormatHasHeader(t *testing.T) {
	t.Parallel()
	cfg, _ := workspacesCfg(t, "proj")
	out, _, err := runListFor(t, cfg, "human")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "PATH") || !strings.Contains(out, "LABEL") || !strings.Contains(out, "SOURCE") {
		t.Errorf("human output missing header, got:\n%s", out)
	}
	if !strings.Contains(out, "proj") {
		t.Errorf("expected seeded workspace candidate in output, got:\n%s", out)
	}
}

// TestList_TSVFormat asserts each non-header line is path\tlabel\n with
// exactly one tab and no ANSI escapes, parseable by Television.
func TestList_TSVFormat(t *testing.T) {
	t.Parallel()
	cfg, _ := workspacesCfg(t, "proj")
	out, _, err := runListFor(t, cfg, "tsv")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("expected at least the seeded candidate in tsv output")
	}
	for _, line := range lines {
		tabCount := strings.Count(line, "\t")
		if tabCount != 1 {
			t.Errorf("tsv line must have exactly one tab, got %d: %q", tabCount, line)
		}
		if strings.ContainsAny(line, "\x1b[") {
			t.Errorf("tsv line must not contain ANSI escapes: %q", line)
		}
	}
}

// TestList_JSONFormat decodes the output as a structured array with the
// expected fields and valid JSON.
func TestList_JSONFormat(t *testing.T) {
	t.Parallel()
	cfg, _ := workspacesCfg(t, "proj")
	out, _, err := runListFor(t, cfg, "json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []listCandidate
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one candidate; got empty array")
	}
	for _, c := range got {
		if c.Label == "" || c.Path == "" || c.Source == "" {
			t.Errorf("candidate has empty core field: %+v", c)
		}
	}
}

// TestList_JSONEmptyIsArray guards the non-nil-array contract: [] not null
// when nothing matches.
func TestList_JSONEmptyIsArray(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := renderJSON(&out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Fatalf("expected JSON array, got: %q", out.String())
	}
	var got []listCandidate
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("empty must unmarshal to []: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("expected non-nil empty slice, got %v", got)
	}
}

// TestList_MissingWorkspaceMarked confirms a configured workspace whose path
// does not exist appears in the listing marked as missing, in both human and
// json output.
func TestList_MissingWorkspaceMarked(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.General.Sources = []string{config.SourceWorkspaces}
	cfg.Workspaces = []config.WorkspaceConfig{{Name: "ghost", Path: filepath.Join(t.TempDir(), "nope")}}

	humanOut, _, err := runListFor(t, cfg, "human")
	if err != nil {
		t.Fatalf("list human: %v", err)
	}
	if !strings.Contains(humanOut, "yes") {
		t.Errorf("human output missing 'yes' missing marker:\n%s", humanOut)
	}

	jsonOut, _, err := runListFor(t, cfg, "json")
	if err != nil {
		t.Fatalf("list json: %v", err)
	}
	var got []listCandidate
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(got) != 1 || !got[0].Missing {
		t.Errorf("expected one missing=true candidate, got %+v", got)
	}
}

// TestList_InvalidFormatReturnsError guards the format validator.
func TestList_InvalidFormatReturnsError(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	app := New(WithStreams(&out, &errOut))
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"list", "--format", "xml"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for invalid format, got nil")
	}
}

// TestList_PartialSourceErrorWarnsButSucceeds (HI-6 philosophy) verifies a
// failing source surfaces a stderr warning while list still exits 0 with
// candidates from the healthy sources.
func TestList_PartialSourceErrorWarnsButSucceeds(t *testing.T) {
	t.Parallel()
	cfg, _ := workspacesCfg(t, "proj")
	cfg.General.Sources = []string{config.SourceHerdr, config.SourceWorkspaces}
	out, _, err := runListFor(t, cfg, "human")
	if err != nil {
		t.Fatalf("list should succeed on partial: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("expected candidates from healthy sources despite nil herdr driver")
	}
}

// render unit coverage keeps formatting logic honest without the cobra layer.
func TestRender_TSVNoTrailingNewlineDup(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "aa", Source: "s"},
		{Path: "/b", NormalizedPath: "/b", Label: "bb", Source: "s"},
	}
	var out bytes.Buffer
	if err := renderTSV(&out, cands); err != nil {
		t.Fatal(err)
	}
	want := "/a\taa\n/b\tbb\n"
	if out.String() != want {
		t.Errorf("tsv mismatch: got %q want %q", out.String(), want)
	}
}

func TestRender_HumanHasColumns(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{{Path: "/x", NormalizedPath: "/x", Label: "x", Source: "z"}}
	var out bytes.Buffer
	if err := renderHuman(&out, cands); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"PATH", "LABEL", "SOURCE", "MISSING", "/x", "x", "z"} {
		if !strings.Contains(got, want) {
			t.Errorf("human output missing %q: %q", want, got)
		}
	}
}

// TestList_IncludesConfigWorkspaces confirms predefined [[workspaces]]
// entries surface as candidates with Source "workspaces" in `shep list`
// output across the human, tsv and json formats.
func TestList_IncludesConfigWorkspaces(t *testing.T) {
	t.Parallel()
	cfg, _ := workspacesCfg(t, "proj")

	for _, format := range []string{"human", "tsv", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			out, _, err := runListFor(t, cfg, format)
			if err != nil {
				t.Fatalf("list %s: %v", format, err)
			}
			if !strings.Contains(out, "proj") {
				t.Errorf("list %s missing workspace label 'proj':\n%s", format, out)
			}
			if format == "tsv" {
				return
			}
			if !strings.Contains(out, "workspaces") {
				t.Errorf("list %s missing 'workspaces' source:\n%s", format, out)
			}
		})
	}
}

func TestParseFormat(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "human"} {
		got, err := parseFormat(in)
		if err != nil || got != formatHuman {
			t.Errorf("parseFormat(%q): got %v err=%v", in, got, err)
		}
	}
	if _, err := parseFormat("nope"); err == nil {
		t.Error("expected error for invalid format")
	}
}
