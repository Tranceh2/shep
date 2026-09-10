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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
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

// TestList_TSVFormat asserts each non-header line is path\tlabel\ticon with
// exactly two tabs and no ANSI escapes, parseable by Television's
// {split:\t:N} templates. The icon is the 3rd column (index 2); a candidate
// whose source has no configured icon renders an empty 3rd column so the
// column position stays stable for parsers ( Television reads index 0/1/2).
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
		if tabCount != 2 {
			t.Errorf("tsv line must have exactly two tabs (path\tlabel\ticon), got %d: %q", tabCount, line)
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

func TestRender_WorktreeMetadata(t *testing.T) {
	t.Parallel()
	candidate := source.Candidate{
		Path: "/trees/api", NormalizedPath: "/trees/api", Label: "api",
		Source: config.SourceProjects, Icon: "repo",
		Meta: map[string]string{"is_worktree": "true", "branch": "feat/x\tline\nnext"},
	}

	t.Run("json projects typed metadata", func(t *testing.T) {
		var out bytes.Buffer
		if err := renderJSON(&out, []source.Candidate{candidate}); err != nil {
			t.Fatal(err)
		}
		var got []listCandidate
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if len(got) != 1 || !got[0].IsWorktree || got[0].Branch != "feat/x\tline\nnext" {
			t.Fatalf("worktree metadata = %+v, want typed flag and exact branch", got)
		}
	})

	t.Run("tsv appends sanitized branch badge without adding columns", func(t *testing.T) {
		var out bytes.Buffer
		if err := renderTSV(&out, []source.Candidate{candidate}); err != nil {
			t.Fatal(err)
		}
		if got, want := out.String(), "/trees/api\tapi [worktree: feat/x line next]\trepo\n"; got != want {
			t.Fatalf("tsv = %q, want %q", got, want)
		}
		if got := strings.Count(strings.TrimSuffix(out.String(), "\n"), "\t"); got != 2 {
			t.Fatalf("tsv tabs = %d, want 2", got)
		}
	})
}

// TestRender_JSONIncludesIconField proves the JSON projection carries the
// candidate's Icon as a top-level "icon" field, for API/tooling parity with
// the TSV icon column. The field is always present (empty string when the
// source has no configured icon), matching the TSV's stable-column contract.
func TestRender_JSONIncludesIconField(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "aa", Source: "herdr", Icon: "\uf07c"},
		{Path: "/b", NormalizedPath: "/b", Label: "bb", Source: "zoxide"}, // empty Icon
	}
	var out bytes.Buffer
	if err := renderJSON(&out, cands); err != nil {
		t.Fatal(err)
	}
	var got []listCandidate
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(got), got)
	}
	if got[0].Icon != "\uf07c" {
		t.Errorf("candidate 0 icon: got %q want %q", got[0].Icon, "\uf07c")
	}
	// Empty Icon must serialize as "" (present key), not be omitted.
	if got[1].Icon != "" {
		t.Errorf("candidate 1 icon: expected empty string, got %q", got[1].Icon)
	}
	// The raw JSON must contain the "icon" key for both entries so external
	// parsers can rely on the schema.
	raw := out.String()
	if strings.Count(raw, `"icon"`) != 2 {
		t.Errorf("expected 'icon' key for both candidates, raw json:\n%s", raw)
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
	cfg.General.SourceOrder = []string{config.SourceWorkspaces}
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
	cfg.General.SourceOrder = []string{config.SourceHerdr, config.SourceWorkspaces}
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
	// Empty Icon still occupies the 3rd column as an empty field so column
	// position stays stable for parsers like Television's {split:\t:N}.
	want := "/a\taa\t\n/b\tbb\t\n"
	if out.String() != want {
		t.Errorf("tsv mismatch: got %q want %q", out.String(), want)
	}
}

// TestRender_TSVIncludesIconColumn proves a candidate with a non-empty Icon
// renders it as the 3rd TSV column: path\tlabel\ticon\n. This is the field
// Television's {split:\t:2} reads for the [source].display template.
func TestRender_TSVIncludesIconColumn(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "aa", Source: "herdr", Icon: "\uf07c"},
		{Path: "/b", NormalizedPath: "/b", Label: "bb", Source: "zoxide", Icon: "\uf07b"},
	}
	var out bytes.Buffer
	if err := renderTSV(&out, cands); err != nil {
		t.Fatal(err)
	}
	want := "/a\taa\t\uf07c\n/b\tbb\t\uf07b\n"
	if out.String() != want {
		t.Errorf("tsv icon column mismatch: got %q want %q", out.String(), want)
	}
}

// TestRender_TSVIconColumnStableWhenEmpty guards the parser-stability
// contract: a candidate with an empty Icon must still emit the trailing
// tab+empty-field, never collapse to two columns. Television indexes columns
// by position ({split:\t:2}), so a missing column would shift the icon off
// the expected index for every following line.
func TestRender_TSVIconColumnStableWhenEmpty(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "aa", Source: "s", Icon: "X"},
		{Path: "/b", NormalizedPath: "/b", Label: "bb", Source: "s"}, // empty Icon
	}
	var out bytes.Buffer
	if err := renderTSV(&out, cands); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out.String())
	}
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Errorf("line %d: expected 3 fields (path,label,icon), got %d: %q", i, len(fields), line)
		}
	}
	// The empty-Icon line's icon field is exactly "".
	emptyFields := strings.Split(lines[1], "\t")
	if emptyFields[2] != "" {
		t.Errorf("expected empty 3rd column for empty Icon, got %q in line %q", emptyFields[2], lines[1])
	}
}

// TestRender_TSVSanitizesEmbeddedDelimiters guards the column-stability
// contract against delimiter injection: Label (workspace/pane/dir names) and
// Icon (raw user-configured TOML string, [sources.<name>].icon) are not
// guaranteed free of embedded tabs or newlines. If either leaked a literal
// \t or \n into renderTSV's output, it would silently shift or split every
// subsequent field for that line for any TSV consumer, including
// Television's {split:\t:N}. Both characters must be replaced with a single
// space before writing the line.
func TestRender_TSVSanitizesEmbeddedDelimiters(t *testing.T) {
	t.Parallel()
	cands := []source.Candidate{
		{Path: "/a", NormalizedPath: "/a", Label: "a\tb\nc", Source: "s", Icon: "ic\ton\n1"},
		{Path: "/b", NormalizedPath: "/b", Label: "plain", Source: "s", Icon: "plain-icon"},
	}
	var out bytes.Buffer
	if err := renderTSV(&out, cands); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != len(cands) {
		t.Fatalf("expected %d lines (one per candidate, no embedded newlines splitting a record), got %d: %q", len(cands), len(lines), out.String())
	}
	for i, line := range lines {
		if got := strings.Count(line, "\t"); got != 2 {
			t.Errorf("line %d: expected exactly 2 tabs (path\\tlabel\\ticon), got %d: %q", i, got, line)
		}
		if strings.ContainsAny(line, "\n") {
			t.Errorf("line %d: raw newline survived in output: %q", i, line)
		}
	}
	if strings.Contains(lines[0], "a\tb\nc") || strings.Contains(out.String(), "\tb\nc\t") {
		t.Errorf("expected embedded delimiters in Label to be sanitized, got: %q", lines[0])
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

// TestList_SuccessfulListWorks verifies that a successful runList
// prints the filtered candidates.
func TestList_SuccessfulListWorks(t *testing.T) {
	cfg, _ := workspacesCfg(t, "proj-a", "proj-b")
	out, errOut, err := runListFor(t, cfg, "human")
	if err != nil {
		t.Fatalf("runListFor failed: %v", err)
	}
	if errOut != "" {
		t.Errorf("expected empty stderr, got: %q", errOut)
	}
	if !strings.Contains(out, "proj-a") || !strings.Contains(out, "proj-b") {
		t.Fatalf("expected stdout to contain proj-a and proj-b, got: %q", out)
	}
}
