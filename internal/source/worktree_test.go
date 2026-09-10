package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorktreePorcelainParsesRecords(t *testing.T) {
	input := strings.Join([]string{
		"worktree /repos/main/.",
		"HEAD abc123",
		"branch refs/heads/main",
		"",
		"worktree /trees/feature",
		"HEAD def456",
		"branch refs/heads/feature/xyz",
		"locked maintenance",
		"prunable stale metadata",
		"",
	}, "\n")

	got, err := ParseWorktreePorcelain(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWorktreePorcelain() error = %v", err)
	}
	want := []WorktreeInfo{
		{Path: "/repos/main", Head: "abc123", Branch: "main"},
		{Path: "/trees/feature", Head: "def456", Branch: "feature/xyz", IsLocked: true, IsPrunable: true, LockReason: "maintenance", PruneReason: "stale metadata"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWorktreePorcelain() = %#v, want %#v", got, want)
	}
}

func TestWorktreePorcelainParsesDetachedAndBareWithoutBlankSeparator(t *testing.T) {
	input := "worktree /trees/detached\nHEAD 1234567\ndetached\nworktree /srv/repo.git\nbare\n"
	got, err := ParseWorktreePorcelain(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseWorktreePorcelain() error = %v", err)
	}
	want := []WorktreeInfo{
		{Path: "/trees/detached", Head: "1234567", IsDetached: true},
		{Path: "/srv/repo.git", IsBare: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseWorktreePorcelain() = %#v, want %#v", got, want)
	}
}

func TestWorktreePorcelainReturnsReaderError(t *testing.T) {
	_, err := ParseWorktreePorcelain(errorReader{})
	if !errors.Is(err, errFixtureRead) {
		t.Fatalf("ParseWorktreePorcelain() error = %v, want %v", err, errFixtureRead)
	}
}

func TestWorktreePorcelainRejectsOversizedRecords(t *testing.T) {
	_, err := ParseWorktreePorcelain(strings.NewReader("worktree /" + strings.Repeat("x", 70*1024)))
	if err == nil {
		t.Fatal("ParseWorktreePorcelain() error = nil, want scanner error")
	}
}

func TestWorktreeDiscoverySkipsGitWithoutMetadata(t *testing.T) {
	repo := t.TempDir()
	called := false
	got, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err != nil || got != nil || called {
		t.Fatalf("DiscoverWorktreesWithRunner() = %#v, %v; runGit called = %v", got, err, called)
	}
}

func TestWorktreeDiscoveryUsesFixedCommandAndFiltersPaths(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(t.TempDir(), "linked")
	if err := os.Mkdir(valid, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")

	got, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if dir != repo || !reflect.DeepEqual(args, []string{"worktree", "list", "--porcelain"}) {
			t.Fatalf("runGit(_, %q, %q), want repo and fixed worktree argv", dir, args)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 50*time.Millisecond {
			t.Fatalf("runner context deadline = %v, %v", deadline, ok)
		}
		return []byte("worktree " + valid + "/.\nHEAD abc\nbranch refs/heads/feat/x\n\nworktree " + missing + "\nHEAD def\nprunable gone\n"), nil
	})
	if err != nil {
		t.Fatalf("DiscoverWorktreesWithRunner() error = %v", err)
	}
	want := []WorktreeInfo{{Path: valid, Head: "abc", Branch: "feat/x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverWorktreesWithRunner() = %#v, want %#v", got, want)
	}
}

func TestWorktreeDiscoveryFromLinkedWorktreeUsesSharedMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	linked := filepath.Join(root, "linked")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run(main, "init", "-q")
	run(main, "config", "user.email", "test@example.invalid")
	run(main, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(main, "README"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(main, "add", "README")
	run(main, "commit", "-qm", "initial")
	run(main, "worktree", "add", "-q", linked, "-b", "linked")
	got, err := DiscoverWorktrees(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("linked worktree discovery = %#v, want main and linked", got)
	}
	seen := map[string]bool{}
	for _, item := range got {
		seen[filepath.Clean(item.Path)] = true
	}
	mainResolved, _ := filepath.EvalSymlinks(main)
	linkedResolved, _ := filepath.EvalSymlinks(linked)
	if !seen[filepath.Clean(mainResolved)] || !seen[filepath.Clean(linkedResolved)] {
		t.Fatalf("discovered paths = %v, want %q and %q", seen, mainResolved, linkedResolved)
	}
}

func TestWorktreeDiscoverySupportsBareMetadata(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err != nil || !called {
		t.Fatalf("DiscoverWorktreesWithRunner() error = %v; runGit called = %v", err, called)
	}
}

func TestWorktreeDiscoveryRejectsRelativeAndTraversalPaths(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(repo, "..", filepath.Base(repo)+"-outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	got, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("worktree relative\nHEAD abc\n\nworktree " + outside + "/../" + filepath.Base(outside) + "\nHEAD def\n"), nil
	})
	if err != nil {
		t.Fatalf("DiscoverWorktreesWithRunner() error = %v", err)
	}
	if got != nil {
		t.Fatalf("DiscoverWorktreesWithRunner() = %#v, want nil", got)
	}
}

func TestWorktreeDiscoveryPropagatesRunnerError(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("git failed")
	_, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(context.Context, string, ...string) ([]byte, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("DiscoverWorktreesWithRunner() error = %v, want %v", err, wantErr)
	}
}

func TestWorktreeDiscoveryRejectsFileTargets(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(target, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("worktree " + target + "\nHEAD abc\n"), nil
	})
	if err != nil || got != nil {
		t.Fatalf("DiscoverWorktreesWithRunner() = %#v, %v; want nil, nil", got, err)
	}
}

func TestWorktreeDiscoveryTimesOutRunner(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("DiscoverWorktreesWithRunner() error = %v after %v", err, time.Since(started))
	}
}

func TestWorktreeDiscoveryDropsDanglingSymlink(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), link); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverWorktreesWithRunner(context.Background(), repo, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("worktree " + link + "\nHEAD abc\n"), nil
	})
	if err != nil || got != nil {
		t.Fatalf("DiscoverWorktreesWithRunner() = %#v, %v; want nil, nil", got, err)
	}
}

var errFixtureRead = errors.New("fixture read failure")

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errFixtureRead }
