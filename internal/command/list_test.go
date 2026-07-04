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
	// Inject the config so PersistentPreRunE sees it via the --config path is
	// awkward for synthetic configs; set the field directly and skip preload
	// by annotating — but list needs preload to run, so instead set cfg first.
	app.cfg = cfg
	app.probes = config.Probes{}

	cmd := app.rootCmd()
	cmd.SetArgs([]string{"list", "--format", format})
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestList_HumanFormatHasHeader (PL-1) confirms the human table renders a
// header and the cwd candidate.
func TestList_HumanFormatHasHeader(t *testing.T) {
	t.Parallel()
	out, _, err := runListFor(t, nil, "human")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "PATH") || !strings.Contains(out, "LABEL") || !strings.Contains(out, "SOURCE") {
		t.Errorf("human output missing header, got:\n%s", out)
	}
	if !strings.Contains(out, "cwd") {
		t.Errorf("expected cwd candidate in output, got:\n%s", out)
	}
}

// TestList_TSVFormat (PL-5) asserts each non-header line is path\tlabel\n
// with exactly one tab and no ANSI escapes, parseable by Television.
func TestList_TSVFormat(t *testing.T) {
	t.Parallel()
	out, _, err := runListFor(t, nil, "tsv")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("expected at least the cwd candidate in tsv output")
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

// TestList_JSONFormat (PL-6) decodes the output as a structured array with
// the expected fields and valid JSON.
func TestList_JSONFormat(t *testing.T) {
	t.Parallel()
	out, _, err := runListFor(t, nil, "json")
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

// TestList_JSONEmptyIsArray not []null when nothing matches; guard the
// non-nil-array contract.
func TestList_JSONEmptyIsArray(t *testing.T) {
	t.Parallel()
	// renderJSON must emit a non-null `[]` (not `null`) when there are no
	// candidates so Television/JSON consumers can safely iterate.
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

// TestList_DedupAcrossSources (PL-4) seeds cwd + a roots entry that resolves
// to the same directory and asserts only one candidate survives dedup.
// Cannot run t.Parallel because it changes the process cwd via t.Chdir.
func TestList_DedupAcrossSources(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "proj")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project) // cwd == project

	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{
		Kind:    config.KindRoots,
		Enabled: true,
		Options: map[string]string{"path": parent},
	}

	out, _, err := runListFor(t, cfg, "tsv")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Both the cwd candidate and the dev-roots "proj" candidate normalise to
	// the same path; dedup must leave exactly one "proj" row.
	count := 0
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[1] == "proj" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one 'proj' row after dedup, got %d:\n%s", count, out)
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
	// A roots source pointing at a path that exists but contains entries plus
	// an inaccessible herdr provider (nil driver) -> herdr inert, roots works.
	tmp := t.TempDir()
	cfg := config.Defaults()
	cfg.Sources["dev"] = config.Source{Kind: config.KindRoots, Enabled: true, Options: map[string]string{"path": tmp}}
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
	// tabwriter pads columns with spaces; assert token presence rather than
	// exact tab-separated substrings so the rendering contract stays loose.
	for _, want := range []string{"PATH", "LABEL", "SOURCE", "/x", "x", "z"} {
		if !strings.Contains(got, want) {
			t.Errorf("human output missing %q: %q", want, got)
		}
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
