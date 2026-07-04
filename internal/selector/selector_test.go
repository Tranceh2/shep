package selector

import (
	"context"
	"errors"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

func mkCand(path, label string) source.Candidate {
	return source.Candidate{Path: path, NormalizedPath: path, Label: label}
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

// Cascade runs selectors in order and returns the first successful pick.
func TestCascade_FirstApplicableWins(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo"), mkCand("/x/bar", "bar")}
	// Direct declines (2 cands); fakeFzf picks the second entry.
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, stubRunner{line: "/x/bar"})
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

// Fzf parses the selected line back into the right candidate by path.
func TestFzf_ParsesSelectionLine(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo"), mkCand("/x/bar", "bar")}
	// fzf echoes back the first line: "/x/bar\tbar"
	runner := stubRunner{line: "/x/bar\tbar"}
	fzf := withLookPathAndRunner(func(string) (string, error) { return "/usr/bin/fzf", nil }, runner)
	pick, ok, err := fzf.Select(context.Background(), cands, "bar")
	if err != nil || !ok {
		t.Fatalf("expected pick, ok=%v err=%v", ok, err)
	}
	if pick.NormalizedPath != "/x/bar" {
		t.Errorf("pick = %q, want /x/bar", pick.NormalizedPath)
	}
}

// Fzf passes the query through so users get a head-start filter.
func TestFzf_PassesQueryFlag(t *testing.T) {
	cands := []source.Candidate{mkCand("/x/foo", "foo")}
	runner := stubRunner{line: "/x/foo\tfoo", captureArgs: new([][]string)}
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

type stubRunner struct {
	line        string
	err        error
	captureArgs *[][]string
}

func (s stubRunner) Run(_ context.Context, _ string, args []string, _ string) (string, error) {
	if s.captureArgs != nil {
		*s.captureArgs = append(*s.captureArgs, args)
	}
	return s.line, s.err
}

// exitError fakes exec.ExitError for the cancel-on-130 test without a real
// subprocess. Satisfies exec.ExitError's ExitCode() method enough for the
// errors.As check in Fzf.Select.
type exitError struct{ code int }

func (e *exitError) Error() string { return "exit status " + itoa(e.code) }
func (e *exitError) ExitCode() int { return e.code }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}