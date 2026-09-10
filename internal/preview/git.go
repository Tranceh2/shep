package preview

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitCheckTimeout is the maximum budget for a git lookup before the renderer
// bypasses the git summary. WP-1 requires a fast, safe check that never janks
// the TUI.
const gitCheckTimeout = 50 * time.Millisecond

// GitProvider renders a fast git summary for a workspace path. The default
// implementation (gitRunner) bounds every invocation by gitCheckTimeout and
// returns an error on missing binary or timeout so the renderer can bypass.
type GitProvider interface {
	Summary(ctx context.Context, path string) (GitSummary, error)
}

// GitSummary is the minimal git state the preview shows: the current branch and
// the number of uncommitted changes.
type GitSummary struct {
	Branch string
	Dirty  int
}

// String renders the summary for display. A clean tree shows "(clean)"; a dirty
// tree shows "(N changes)".
func (s GitSummary) String() string {
	if s.Dirty == 0 {
		return fmt.Sprintf("%s (clean)", s.Branch)
	}
	return fmt.Sprintf("%s (%d changes)", s.Branch, s.Dirty)
}

// execFunc is the seam for exec.CommandContext so the timeout and missing-binary
// paths stay deterministic in tests without touching a real git process.
type execFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultExec is the production exec backing gitRunner.
func defaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// gitRunner is the default GitProvider. lookup timeout defaults to
// gitCheckTimeout but is overridable (tests use a shorter one to stay fast).
type gitRunner struct {
	run     execFunc
	gitBin  string
	timeout time.Duration
}

// NewGitProvider builds the production GitProvider backed by the git binary on
// PATH. It never returns an error; callers discover a missing git via Summary.
func NewGitProvider() GitProvider {
	return &gitRunner{run: defaultExec, gitBin: "git", timeout: gitCheckTimeout}
}

// Summary fetches branch (rev-parse --abbrev-ref HEAD) and porcelain status
// under the configured timeout. Any failure (missing binary, non-zero exit,
// timeout, non-repo path) returns an error; the renderer then bypasses git.
func (g *gitRunner) Summary(ctx context.Context, path string) (GitSummary, error) {
	if g.timeout <= 0 {
		g.timeout = gitCheckTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	branchOut, err := g.run(runCtx, g.gitBin, "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return GitSummary{}, fmt.Errorf("git branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOut))

	statusOut, err := g.run(runCtx, g.gitBin, "-C", path, "status", "--porcelain")
	if err != nil {
		return GitSummary{}, fmt.Errorf("git status: %w", err)
	}
	return GitSummary{Branch: branch, Dirty: countNonEmpty(strings.TrimSpace(string(statusOut)))}, nil
}

func formatGitSummary(summary GitSummary, meta map[string]string) string {
	line := summary.String()
	if meta["is_worktree"] != "true" {
		return line
	}
	branch := meta["branch"]
	if branch == "" {
		branch = summary.Branch
	}
	head := meta["head"]
	if len(head) > 7 {
		head = head[:7]
	}
	return strings.TrimSpace("[worktree: " + branch + "] " + head + " " + line)
}

// countNonEmpty returns the number of non-empty lines in s.
func countNonEmpty(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
