package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// fakeGit is a deterministic GitProvider for renderer tests; no real processes.
type fakeGit struct {
	summary GitSummary
	err     error
	calls   int
}

func (f *fakeGit) Summary(_ context.Context, _ string) (GitSummary, error) {
	f.calls++
	if f.err != nil {
		return GitSummary{}, f.err
	}
	return f.summary, nil
}

func candidate(label, path, src, template string) source.Candidate {
	c := source.Candidate{
		Path:   path,
		Label:  label,
		Source: src,
	}
	if template != "" {
		c.Meta = map[string]string{"template": template}
	}
	return c
}

func mustRender(t *testing.T, r Renderer, cand source.Candidate) string {
	t.Helper()
	res, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return res.Text
}

// TestRender_DefaultLayout (WP-1) confirms the zero-config built-in preview
// shows label, path, source, and omits template/git when absent.
func TestRender_DefaultLayout(t *testing.T) {
	t.Parallel()

	r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true}, &fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "foo\npath: /p/foo\nsource: roots\ngit: main (clean)"
	if got != want {
		t.Errorf("default layout:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DefaultWithTemplate (WP-1) includes a matched template line.
func TestRender_DefaultWithTemplate(t *testing.T) {
	t.Parallel()

	r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true}, &fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", "go"))
	want := "foo\npath: /p/foo\nsource: roots\ntemplate: go\ngit: main (clean)"
	if got != want {
		t.Errorf("default+template:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DefaultWithGit (WP-1) appends a git summary line when git is fast
// and available, and stays clean when the branch is clean.
func TestRender_DefaultWithGit(t *testing.T) {
	t.Parallel()

	r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "main", Dirty: 2}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "foo\npath: /p/foo\nsource: roots\ngit: main (2 changes)"
	if got != want {
		t.Errorf("default+git:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DefaultGitBypassed (WP-1) confirms the git line is omitted when
// probes report git missing, or when Summary returns an error (slow/missing).
func TestRender_DefaultGitBypassed(t *testing.T) {
	t.Parallel()

	t.Run("git probe off", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: false},
			&fakeGit{summary: GitSummary{Branch: "main"}}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
		if gitSummaryLinePresent(got) {
			t.Errorf("git line must be absent when probe off: %q", got)
		}
	})
	t.Run("git summary errors", func(t *testing.T) {
		t.Parallel()
		r := NewRenderer(config.PreviewConfig{}, config.Probes{Git: true},
			&fakeGit{err: errors.New("timeout")}, nil)
		got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
		if gitSummaryLinePresent(got) {
			t.Errorf("git line must be absent on summary error: %q", got)
		}
	})
}

// gitSummaryLinePresent reports whether any line starts with "git:".
func gitSummaryLinePresent(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if len(line) >= 4 && line[:4] == "git:" {
			return true
		}
	}
	return false
}

// TestRender_DeclarativeSections (WP-2) renders [[preview.sections]] in TOML
// declaration order using the declared field names and section types.
func TestRender_DeclarativeSections(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Sections: []config.PreviewSection{
			{Name: "Identity", Type: config.PreviewSectionBuiltin, Fields: []string{"label", "path", "source"}},
			{Name: "Git", Type: config.PreviewSectionGit},
		},
	}
	r := NewRenderer(cfg, config.Probes{Git: true},
		&fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "Identity\nlabel: foo\npath: /p/foo\nsource: roots\n\nGit\nmain (clean)"
	if got != want {
		t.Errorf("declarative sections:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DeclarativeBuiltinFieldOrder (WP-2) confirms the renderer honours
// the declared field order within a builtin section, not a fixed order.
func TestRender_DeclarativeBuiltinFieldOrder(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Sections: []config.PreviewSection{
			{Name: "X", Type: config.PreviewSectionBuiltin, Fields: []string{"source", "label"}},
		},
	}
	r := NewRenderer(cfg, config.Probes{Git: true}, &fakeGit{}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "X\nsource: roots\nlabel: foo"
	if got != want {
		t.Errorf("builtin field order:\n got %q\nwant %q", got, want)
	}
}

// TestRender_DeclarativeGitUnavailable (WP-2) renders a git section that cannot
// satisfy the summary as an explicit unavailable note under its heading.
func TestRender_DeclarativeGitUnavailable(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Sections: []config.PreviewSection{
			{Name: "Git", Type: config.PreviewSectionGit},
		},
	}
	r := NewRenderer(cfg, config.Probes{Git: false}, &fakeGit{summary: GitSummary{Branch: "main"}}, nil)
	got := mustRender(t, r, candidate("foo", "/p/foo", "roots", ""))
	want := "Git\n(git unavailable)"
	if got != want {
		t.Errorf("git unavailable section:\n got %q\nwant %q", got, want)
	}
}

// fakeRunner is a deterministic CommandRunner for renderer routing tests.
type fakeRunner struct {
	out      string
	err      error
	outs     []string
	errs     []error
	calls    int
	lastArgv []string
	lastDir  string
	lastMax  int
}

func (f *fakeRunner) Run(_ context.Context, argv []string, dir string, maxLines int) (string, error) {
	f.calls++
	f.lastArgv = argv
	f.lastDir = dir
	f.lastMax = maxLines
	idx := f.calls - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return "", f.errs[idx]
	}
	if f.err != nil {
		return "", f.err
	}
	if idx < len(f.outs) {
		return f.outs[idx], nil
	}
	return f.out, nil
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ []string, _ string, _ int) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestRender_CommandRouting (WP-3, 2.9) executes the configured command via the
// injected runner, passes the candidate path as a single substituted argument
// and the candidate directory as dir, and surfaces the runner's stdout.
func TestRender_CommandRouting(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{
		Command:  "git -C {path} log -n 5",
		MaxLines: 50,
	}
	runner := &fakeRunner{out: "commit-a\ncommit-b"}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "roots", ""), RenderOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if res.Text != "commit-a\ncommit-b" {
		t.Errorf("text: got %q want %q", res.Text, "commit-a\\ncommit-b")
	}
	if runner.calls != 1 {
		t.Errorf("runner calls: got %d want 1", runner.calls)
	}
	wantArgv := []string{"git", "-C", "/p/foo", "log", "-n", "5"}
	if !stringSliceEqual(runner.lastArgv, wantArgv) {
		t.Errorf("argv: got %v want %v", runner.lastArgv, wantArgv)
	}
	if runner.lastDir != "/p/foo" {
		t.Errorf("dir: got %q want %q", runner.lastDir, "/p/foo")
	}
	if runner.lastMax != 50 {
		t.Errorf("maxlines: got %d want 50", runner.lastMax)
	}
	if res.Warning != "" {
		t.Errorf("unexpected warning %q", res.Warning)
	}
}

// TestRender_CommandFallback (WP-3, 2.9) falls back to the built-in preview and
// records a transient warning when the command fails.
func TestRender_CommandFallback(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "broken-cmd"}
	runner := &fakeRunner{err: errors.New("exit 127")}
	r := NewRenderer(cfg, config.Probes{Git: true}, &fakeGit{summary: GitSummary{Branch: "main"}}, runner)
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "roots", ""), RenderOptions{})
	if err != nil {
		t.Fatalf("render error must be nil on fallback: %v", err)
	}
	// Fallback is the built-in default layout (label/path/source, then git).
	want := "foo\npath: /p/foo\nsource: roots\ngit: main (clean)"
	if res.Text != want {
		t.Errorf("fallback text:\n got %q\nwant %q", res.Text, want)
	}
	if res.Warning == "" {
		t.Error("expected a non-empty warning on command failure")
	}
	if res.Warning != "preview command failed" {
		t.Errorf("warning: got %q want generic command failure", res.Warning)
	}
}

// TestRender_CommandTimeoutBounded applies preview.timeout at renderer level so
// even a blocking runner falls back quickly with a safe warning.
func TestRender_CommandTimeoutBounded(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "slow {path}", Timeout: config.Duration(20 * time.Millisecond)}
	r := NewRenderer(cfg, config.Probes{}, nil, blockingRunner{})
	start := time.Now()
	res, err := r.Render(context.Background(), candidate("foo", "/p/foo", "roots", ""), RenderOptions{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("render error must be nil on timeout fallback: %v", err)
	}
	if res.Warning != "preview command timed out" {
		t.Errorf("warning: got %q want timeout warning", res.Warning)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("timeout fallback took %v, want bounded elapsed", elapsed)
	}
	if res.Text != "foo\npath: /p/foo\nsource: roots" {
		t.Errorf("fallback text: %q", res.Text)
	}
}

// TestRender_CommandFailureIsNotCached retries a failed command on the next
// render for the same path and clears the transient warning after success.
func TestRender_CommandFailureIsNotCached(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "preview {path}", CacheTTL: 0}
	runner := &fakeRunner{outs: []string{"", "fresh output"}, errs: []error{errors.New("boom"), nil}}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	cand := candidate("foo", "/p/foo", "roots", "")
	first, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if first.Warning != "preview command failed" {
		t.Fatalf("first warning: got %q", first.Warning)
	}
	second, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if second.Text != "fresh output" {
		t.Errorf("second text: got %q want fresh command output", second.Text)
	}
	if second.Warning != "" {
		t.Errorf("second warning must be cleared, got %q", second.Warning)
	}
	if second.FromCache {
		t.Error("second render after failure must retry, not come from cache")
	}
	if runner.calls != 2 {
		t.Errorf("runner calls: got %d want 2", runner.calls)
	}
}

// TestRender_CommandCache (WP-3, 2.9) serves the second render for the same
// candidate from cache without invoking the runner again.
func TestRender_CommandCache(t *testing.T) {
	t.Parallel()

	cfg := config.PreviewConfig{Command: "echo hi", CacheTTL: 0} // 0 -> no expiry by time
	runner := &fakeRunner{out: "hi"}
	r := NewRenderer(cfg, config.Probes{}, nil, runner)
	cand := candidate("foo", "/p/foo", "roots", "")
	if _, err := r.Render(context.Background(), cand, RenderOptions{}); err != nil {
		t.Fatalf("first render: %v", err)
	}
	res, err := r.Render(context.Background(), cand, RenderOptions{})
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !res.FromCache {
		t.Error("second render must come from cache (FromCache=true)")
	}
	if runner.calls != 1 {
		t.Errorf("runner calls after cache hit: got %d want 1", runner.calls)
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
