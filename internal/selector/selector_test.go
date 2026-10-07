package selector

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/ranking"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

// withLookPathAndRunner builds an Fzf with substituted PATH lookup and
// subprocess runner.
func withLookPathAndRunner(lp lookPathFunc, r fzfRunner) *Fzf {
	return &Fzf{lookPath: lp, run: r}
}

func mkCand(path, label string) source.Candidate {
	return source.Candidate{Path: path, NormalizedPath: path, Label: label, Source: "projects"}
}

// Direct auto-selects a single candidate.
func TestDirect_SingleCandidate(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo")}
	d := Direct{}
	pick, ok, err := d.Select(context.Background(), cands, "")
	if err != nil || !ok {
		t.Fatalf("expected auto-select, ok=%v err=%v", ok, err)
	}
	if pick.Label != "foo" {
		t.Errorf("pick = %q, want foo", pick.Label)
	}
}

// Direct declines when more than one candidate exists, deferring to the next
// selector (PL-8: no query + multiple candidates is not direct's job).
func TestDirect_MultipleCandidates(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo"), mkCand("/x/bar", "bar")}
	if _, ok, _ := (Direct{}).Select(context.Background(), cands, ""); ok {
		t.Fatal("Direct should not pick when multiple candidates exist")
	}
}

// TestCascade_Names exposes the cascade shape so callers can assert routing
// without invoking real binaries or a TUI. The returned slice is a defensive
// copy so mutating it cannot reorder a live cascade.
func TestCascade_Names(t *testing.T) {
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, stubRunner{})
	c := New(Direct{}, fzf)
	got := c.Names()
	want := []string{"direct", "fzf"}
	if len(got) != len(want) {
		t.Fatalf("names len: got %d want %d (%v)", len(got), len(want), got)
	}
	for i, n := range want {
		if got[i] != n {
			t.Errorf("names[%d]: got %q want %q", i, got[i], n)
		}
	}
	// Defensive copy: mutating the returned slice must not affect the cascade.
	got[0] = "tampered"
	again := c.Names()
	if again[0] != "direct" {
		t.Errorf("Names returned shared backing array; mutating it changed the cascade: got %q", again[0])
	}
}

// Cascade runs selectors in order and returns the first successful pick.
func TestCascade_FirstApplicableWins(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo"), mkCand("/x/bar", "bar")}
	// Direct declines (2 cands); fakeFzf picks the second entry.
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, stubRunner{line: "1\t/x/bar\tbar"})
	c := New(Direct{}, fzf)
	pick, ok, err := c.Select(context.Background(), cands, "bar")
	if err != nil || !ok {
		t.Fatalf("expected pick, ok=%v err=%v", ok, err)
	}
	if pick.NormalizedPath != "/x/bar" {
		t.Errorf("pick = %q, want /x/bar", pick.NormalizedPath)
	}
}

// Cascade returns ok=false when no selector applies (no fzf installed and
// Direct declines), letting the caller render a "no selection" fallback.
func TestCascade_NoApplicableSelector(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo"), mkCand("/x/bar", "bar")}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "", errors.New("no fzf") }, stubRunner{})
	c := New(Direct{}, fzf)
	if _, ok, err := c.Select(context.Background(), cands, ""); err != nil || ok {
		t.Fatalf("expected no pick, ok=%v err=%v", ok, err)
	}
}

// Fzf is inert when LookPath fails so the cascade falls through cleanly.
func TestFzf_AbsentInert(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo")}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "", errors.New("not found") }, stubRunner{})
	if _, ok, err := fzf.Select(context.Background(), cands, ""); err != nil || ok {
		t.Fatalf("absent fzf should be inert, ok=%v err=%v", ok, err)
	}
}

// Fzf treats Ctrl-C (exit 130) as cancellation, not an error.
func TestFzf_CancelIsNotError(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo")}
	runner := stubRunner{err: &exitError{code: 130}}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	if _, ok, err := fzf.Select(context.Background(), cands, ""); err != nil || ok {
		t.Fatalf("cancel should be ok=false nil err, got ok=%v err=%v", ok, err)
	}
}

// Fzf parses the selected opaque ordinal back into the right candidate by
// position, so duplicate paths with distinct actions remain selectable.
func TestFzf_ParsesSelectionOrdinal(t *testing.T) {
	cands := []source.Candidate{mkCand("/same", "first"), mkCand("/same", "second")}
	runner := stubRunner{line: "1\t/same\tsecond"}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	pick, ok, err := fzf.Select(context.Background(), cands, "second")
	if err != nil || !ok {
		t.Fatalf("expected pick, ok=%v err=%v", ok, err)
	}
	if pick.Label != "second" {
		t.Errorf("pick = %q, want second", pick.Label)
	}
}

// Fzf passes the query through so users get a head-start filter.
func TestFzf_UsesCanonicalInputOrderAndIndexTieBreak(t *testing.T) {
	cands := []source.Candidate{mkCand("/first", "first"), mkCand("/second", "second")}
	var args [][]string
	runner := stubRunner{line: "0\t/first\tfirst", captureArgs: &args}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	if _, ok, err := fzf.Select(context.Background(), cands, ""); err != nil || !ok {
		t.Fatalf("expected fzf selection, ok=%v err=%v", ok, err)
	}
	joined := " " + strings.Join(args[0], " ") + " "
	if !strings.Contains(joined, " --nth=2.. ") {
		t.Fatalf("fzf must search path, label, and alias fields: %v", args[0])
	}
	if !strings.Contains(joined, " --accept-nth=1,2,3 ") {
		t.Fatalf("fzf must return only the ordinal, path, and label fields: %v", args[0])
	}
	if !strings.Contains(joined, "--tiebreak=index") {
		t.Fatalf("fzf must use input order as tie-breaker: %v", args[0])
	}
}

func TestFzf_SearchesAliasesButHidesThem(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/kubernetes", "Kubernetes")}
	cands[0].Aliases = []string{"k8s"}
	var input string
	args := new([][]string)
	runner := stubRunner{line: "0\t/x/kubernetes\tKubernetes\tk8s", captureInput: &input, captureArgs: args}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	if _, ok, err := fzf.Select(context.Background(), cands, "k8s"); err != nil || !ok {
		t.Fatalf("alias fzf selection = ok:%v err:%v", ok, err)
	}
	if !strings.Contains(input, "\tKubernetes\t\x1b[8mk8s\x1b[0m\n") {
		t.Fatalf("fzf input omitted concealed alias search field: %q", input)
	}
	joined := strings.Join((*args)[0], " ")
	if !strings.Contains(joined, "--ansi") || !strings.Contains(joined, "--accept-nth=1,2,3") {
		t.Fatalf("fzf must parse concealed aliases and strip them on accept: %v", (*args)[0])
	}
}

func TestFzf_SanitizesControlCharactersBeforeSerialization(t *testing.T) {
	cands := []source.Candidate{{Path: "/x", Label: "label", Aliases: []string{"safe\talias", "line\nalias", "ordinary unicode"}}}
	var input string
	runner := stubRunner{line: "0\t/x\tlabel\tordinary unicode", captureInput: &input}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	if _, ok, err := fzf.Select(context.Background(), cands, ""); err != nil || !ok {
		t.Fatalf("sanitized fzf selection = ok:%v err:%v", ok, err)
	}
	if input != "0\t/x\tlabel\t\x1b[8mordinary unicode\x1b[0m\n" {
		t.Fatalf("fzf input = %q, want control-safe TSV with concealed alias field", input)
	}
}

func TestFzf_RealAliasSearchAndDisplayContract(t *testing.T) {
	if _, err := exec.LookPath("fzf"); err != nil {
		t.Skip("fzf is not installed")
	}
	cands := []source.Candidate{
		mkCand("/x/first", "First"),
		mkCand("/x/kubernetes", "Kubernetes"),
	}
	cands[1].Aliases = []string{"k8s"}
	input := buildFzfInput(cands)

	args := []string{"--ansi", "--delimiter", "\t", "--nth=2..", "--accept-nth=1,2,3", "--filter=k8s"}
	run := exec.Command("fzf", args...)
	run.Stdin = strings.NewReader(input)
	out, err := run.Output()
	if err != nil {
		t.Fatalf("real fzf display-contract filter failed: %v", err)
	}
	line := strings.TrimRight(string(out), "\n")
	if strings.Contains(line, "k8s") || strings.Contains(line, "\x1b[8m") {
		t.Fatalf("accepted real fzf line leaked alias or ANSI concealment: %q", line)
	}
	if _, ok := findByFzfLine(cands, line); !ok {
		t.Fatalf("real fzf line lost ordinal contract: %q", line)
	}
}

func TestFzf_PassesQueryFlag(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo")}
	runner := stubRunner{line: "0\t/x/foo\tfoo", captureArgs: new([][]string)}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	_, _, _ = fzf.Select(context.Background(), cands, "myquery")
	got := (*runner.captureArgs)[0]
	found := false
	for _, a := range got {
		if a == "myquery" {
			found = true
		}
	}
	if !found {
		t.Errorf("query not forwarded to fzf; args=%v", got)
	}
}

// --- test helpers ---

func TestSharedRankingSnapshotAcrossFzfAndTUI(t *testing.T) {
	path := t.TempDir() + "/ranking.sqlite3"
	store, err := ranking.OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	first := mkCand("/a", "A")
	second := mkCand("/b", "B")
	if err := store.Record(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot(context.Background(), "")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	later, err := ranking.OpenPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := later.Record(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	later.Close()

	ranked := ranking.Sort([]source.Candidate{first, second}, "", snapshot)
	if ranked[0].Label != "B" {
		t.Fatalf("canonical order = %q, want B", ranked[0].Label)
	}
	matches := resolver.Match(ranked, "")
	if matches[0].Label != "B" {
		t.Fatalf("resolver preserved order = %q, want B", matches[0].Label)
	}
	var input string
	runner := stubRunner{line: "0\t/b\tB", captureInput: &input}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	pick, ok, err := fzf.Select(context.Background(), ranked, "")
	if err != nil || !ok || pick.Label != "B" {
		t.Fatalf("fzf pick = %+v, ok=%v, err=%v; want B", pick, ok, err)
	}
	if input != "0\t/b\tB\n1\t/a\tA\n" {
		t.Fatalf("fzf input = %q, want ranked opaque ordinals", input)
	}
	model := tui.NewModelWithLayout([]source.Candidate{first, second}, nil, tui.Layout{RankingSnapshot: snapshot})
	updatedModel, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	next, _ := updatedModel.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	selected, ok := next.(tui.Model).Selected()
	if !ok || selected.Label != "B" {
		t.Fatalf("TUI selection = %+v, ok=%v; want B", selected, ok)
	}
}

type stubRunner struct {
	line         string
	err          error
	captureArgs  *[][]string
	captureInput *string
}

func (s stubRunner) Run(_ context.Context, _ string, args []string, input string) (string, error) {
	if s.captureArgs != nil {
		*s.captureArgs = append(*s.captureArgs, args)
	}
	if s.captureInput != nil {
		*s.captureInput = input
	}
	return s.line, s.err
}

// exitError fakes exec.ExitError for the cancel-on-130 test without a real
// subprocess. Satisfies exec.ExitError's ExitCode() method enough for the
// errors.As check in Fzf.Select.
type exitError struct{ code int }

func (e *exitError) Error() string { return "exit status " + strconv.Itoa(e.code) }
func (e *exitError) ExitCode() int { return e.code }
