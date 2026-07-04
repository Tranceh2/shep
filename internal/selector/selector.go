// Package selector implements the candidate-picking cascade used by
// `shep open`: an exact (direct) match short-circuits, fzf accelerates when
// installed, and the Bubble Tea TUI is the universal interactive fallback
// (wired in a later commit). Each selector implements the same small
// interface so the open command can chain them and tests can stub any link.
package selector

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/tranceh2/shep/internal/source"
)

// Selector picks one candidate out of a list, optionally guided by a query.
// A successful pick returns the candidate and ok=true; a cancellation or
// "no choice possible" state returns ok=false with a nil error. Errors are
// reserved for genuine failures (e.g. fzf crashed mid-pick).
type Selector interface {
	// Name is a short, stable identifier for logging and tests.
	Name() string
	// Select picks one candidate. candidates is never mutated.
	Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error)
}

// Cascade runs selectors in order and returns the first successful pick.
// A selector that yields ok=false (not applicable / cancelled) is skipped so
// the next one can try. Cascade is a pointer so callers can compare against
// nil to mean "no cascade".
type Cascade struct {
	selectors []Selector
}

// New builds a Cascade from the supplied selectors (tried in order). The
// default cascade shep uses is DefaultCascade().
func New(selectors ...Selector) *Cascade {
	return &Cascade{selectors: selectors}
}

// Names returns the selector names in cascade order as a defensive copy. It
// lets tests assert the cascade shape (direct, fzf, tui...) without invoking
// real binaries or a TUI, and without reading the private selectors slice.
func (c Cascade) Names() []string {
	out := make([]string, len(c.selectors))
	for i, s := range c.selectors {
		out[i] = s.Name()
	}
	return out
}

// Select runs the cascade. ok=false means no selector produced a pick.
func (c Cascade) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	for _, s := range c.selectors {
		pick, ok, err := s.Select(ctx, candidates, query)
		if err != nil {
			return source.Candidate{}, false, fmt.Errorf("selector %s: %w", s.Name(), err)
		}
		if ok {
			return pick, true, nil
		}
	}
	return source.Candidate{}, false, nil
}

// --- exact / direct selector ---

// Direct auto-selects when the query narrows the candidate list to exactly
// one, or when there is a single candidate and no query. It never prompts.
type Direct struct{}

func (Direct) Name() string { return "direct" }

// Select picks the lone candidate. It uses the pre-filtered matches slice
// the caller already computed from the query; an empty query with len==1
// auto-selects, an empty query with len>1 declines (so the interactive
// selectors can take over).
func (Direct) Select(_ context.Context, candidates []source.Candidate, _ string) (source.Candidate, bool, error) {
	if len(candidates) == 1 {
		return candidates[0].Clone(), true, nil
	}
	return source.Candidate{}, false, nil
}

// --- fzf selector ---

// fzfRunner isolates the exec(Call) so tests can stub the fzf interaction.
// The production default shells out to the fzf binary found on PATH.
type fzfRunner interface {
	// Run pipes candidates to fzf and returns the chosen line. A non-nil
	// error means fzf failed; an empty line with nil error means cancelled.
	Run(ctx context.Context, name string, args []string, input string) (string, error)
}

type execFzf struct{}

// exitCoder is satisfied by *exec.ExitError and any fake that reports an exit
// code, so the cancel handling is testable without a real subprocess.
type exitCoder interface {
	ExitCode() int
}

func (execFzf) Run(ctx context.Context, name string, args []string, input string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// Fzf pipes candidates to fzf and reads the selection back. It is inert when
// fzf is not on PATH (LookPath fails), letting the cascade fall through to
// the next selector.
type Fzf struct {
	lookPath lookPathFunc
	run      fzfRunner
}

// lookPathFunc matches exec.LookPath for substitution in tests.
type lookPathFunc func(string) (string, error)

// NewFzf builds an fzf selector using exec.LookPath and a real subprocess.
func NewFzf() *Fzf {
	return &Fzf{lookPath: exec.LookPath, run: execFzf{}}
}

// withLookPathAndRunner is the test constructor.
func withLookPathAndRunner(lp lookPathFunc, r fzfRunner) *Fzf {
	return &Fzf{lookPath: lp, run: r}
}

func (*Fzf) Name() string { return "fzf" }

// Select formats candidates as "path\tlabel", pipes to fzf, and parses the
// selected line back into a candidate by matching the returned path. ok=false
// is returned when fzf is absent or the user cancelled.
func (f *Fzf) Select(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	if len(candidates) == 0 {
		return source.Candidate{}, false, nil
	}
	if f.lookPath == nil {
		return source.Candidate{}, false, nil
	}
	if _, err := f.lookPath("fzf"); err != nil {
		return source.Candidate{}, false, nil
	}
	input := buildFzfInput(candidates)
	args := []string{"--ansi", "--delimiter", "\t", "--with-nth", "2"}
	if query != "" {
		args = append(args, "--query", query)
	}
	line, err := f.run.Run(ctx, "fzf", args, input)
	if err != nil {
		// fzf returns exit 130 on Esc/Ctrl-C; treat that as cancellation, not
		// a failure, so the cascade can fall through to the next selector.
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 130 {
			return source.Candidate{}, false, nil
		}
		return source.Candidate{}, false, err
	}
	if line == "" {
		return source.Candidate{}, false, nil
	}
	pick, ok := findByFzfLine(candidates, line)
	if !ok {
		return source.Candidate{}, false, nil
	}
	return pick.Clone(), true, nil
}

// buildFzfInput writes "path\tlabel\n" per candidate. The path column is the
// stable key for parsing the selection back; the label is what the user sees.
func buildFzfInput(cands []source.Candidate) string {
	var b strings.Builder
	for _, c := range cands {
		path := c.NormalizedPath
		if path == "" {
			path = c.Path
		}
		fmt.Fprintf(&b, "%s\t%s\n", path, c.Label)
	}
	return b.String()
}

// findByFzfLine maps a returned fzf line back to a candidate by path prefix.
func findByFzfLine(cands []source.Candidate, line string) (source.Candidate, bool) {
	path := line
	if idx := strings.IndexByte(line, '\t'); idx >= 0 {
		path = line[:idx]
	}
	for _, c := range cands {
		np := c.NormalizedPath
		if np == "" {
			np = c.Path
		}
		if np == path {
			return c, true
		}
	}
	return source.Candidate{}, false
}
