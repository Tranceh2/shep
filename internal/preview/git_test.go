package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedExec returns branch output for "rev-parse" args and status output for
// "status" args, so the default gitRunner can be driven without real git.
func scriptedExec(branchOut, statusOut string) execFunc {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "rev-parse"):
			return []byte(branchOut), nil
		case strings.Contains(joined, "status"):
			return []byte(statusOut), nil
		default:
			return nil, errors.New("unexpected git args")
		}
	}
}

// blockingExec simulates a slow git command that exceeds the configured timeout.
func blockingExec(sleep time.Duration) execFunc {
	return func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		select {
		case <-time.After(sleep):
			return []byte("late"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// errExec simulates a missing git binary (exec.LookPath failure).
func errExec(msg string) execFunc {
	return func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New(msg)
	}
}

func newTestGit(t *testing.T, run execFunc, timeout time.Duration) *gitRunner {
	t.Helper()
	return &gitRunner{run: run, gitBin: "git", timeout: timeout}
}

// TestGitSummary_Success parses branch and a clean porcelain status.
func TestGitSummary_Success(t *testing.T) {
	t.Parallel()

	g := newTestGit(t, scriptedExec("main\n", ""), 50*time.Millisecond)
	sum, err := g.Summary(context.Background(), "/p/x")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Branch != "main" {
		t.Errorf("branch: got %q want %q", sum.Branch, "main")
	}
	if sum.Dirty != 0 {
		t.Errorf("dirty: got %d want 0", sum.Dirty)
	}
	if got, want := sum.String(), "main (clean)"; got != want {
		t.Errorf("string: got %q want %q", got, want)
	}
}

// TestGitSummary_DirtyCount counts porcelain lines as changes.
func TestGitSummary_DirtyCount(t *testing.T) {
	t.Parallel()

	g := newTestGit(t, scriptedExec("dev\n", " M a.go\n?? b.go\n"), 50*time.Millisecond)
	sum, err := g.Summary(context.Background(), "/p/x")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Dirty != 2 {
		t.Errorf("dirty: got %d want 2", sum.Dirty)
	}
	if got, want := sum.String(), "dev (2 changes)"; got != want {
		t.Errorf("string: got %q want %q", got, want)
	}
}

func TestFormatGitSummary_Worktree(t *testing.T) {
	t.Parallel()
	summary := GitSummary{Branch: "feat/auth", Dirty: 2}
	meta := map[string]string{"is_worktree": "true", "branch": "feat/auth", "head": "9fce23abcdef"}
	if got, want := formatGitSummary(summary, meta), "[worktree: feat/auth] 9fce23a feat/auth (2 changes)"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got, want := formatGitSummary(GitSummary{Branch: "main"}, nil), "main (clean)"; got != want {
		t.Fatalf("standard summary = %q, want %q", got, want)
	}
}

// TestGitSummary_SlowMiss bypasses when execution exceeds the 50ms
// budget by returning an error (the renderer then skips the git line).
func TestGitSummary_SlowMiss(t *testing.T) {
	t.Parallel()

	g := newTestGit(t, blockingExec(200*time.Millisecond), 50*time.Millisecond)
	start := time.Now()
	_, err := g.Summary(context.Background(), "/p/x")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 150*time.Millisecond {
		t.Errorf("timeout not honoured: elapsed %v", elapsed)
	}
}

// TestGitSummary_BinaryMissing surfaces a missing git binary as an
// error so the renderer can bypass the git line cleanly.
func TestGitSummary_BinaryMissing(t *testing.T) {
	t.Parallel()

	g := newTestGit(t, errExec("executable file not found"), 50*time.Millisecond)
	_, err := g.Summary(context.Background(), "/p/x")
	if err == nil {
		t.Fatal("expected error for missing git binary, got nil")
	}
}
