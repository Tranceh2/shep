package source

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type WorktreeInfo struct {
	Path        string
	Head        string
	Branch      string
	IsBare      bool
	IsDetached  bool
	IsLocked    bool
	IsPrunable  bool
	LockReason  string
	PruneReason string
}

func ParseWorktreePorcelain(r io.Reader) ([]WorktreeInfo, error) {
	var records []WorktreeInfo
	var current *WorktreeInfo
	currentPathValid := false
	scanner := bufio.NewScanner(r)
	flush := func() {
		if current != nil && currentPathValid {
			records = append(records, *current)
		}
		current = nil
		currentPathValid = false
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		if key == "worktree" {
			flush()
			currentPathValid = filepath.IsAbs(value) && !hasParentTraversal(value)
			current = &WorktreeInfo{Path: filepath.Clean(value)}
			continue
		}
		if current == nil {
			continue
		}
		switch key {
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "bare":
			current.IsBare = true
		case "detached":
			current.IsDetached = true
		case "locked":
			current.IsLocked, current.LockReason = true, value
		case "prunable":
			current.IsPrunable, current.PruneReason = true, value
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

const worktreeDiscoveryTimeout = 50 * time.Millisecond

type worktreeGitRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

func DiscoverWorktrees(ctx context.Context, repoRoot string) ([]WorktreeInfo, error) {
	return DiscoverWorktreesWithRunner(ctx, repoRoot, runWorktreeGit)
}

func DiscoverWorktreesWithRunner(ctx context.Context, repoRoot string, runGit worktreeGitRunner) ([]WorktreeInfo, error) {
	if !hasWorktreeMetadata(repoRoot) {
		return nil, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, worktreeDiscoveryTimeout)
	defer cancel()
	out, err := runGit(runCtx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	worktrees, err := ParseWorktreePorcelain(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("parse git worktree list: %w", err)
	}

	valid := worktrees[:0]
	for _, worktree := range worktrees {
		if worktree.IsPrunable || !validWorktreePath(worktree) {
			continue
		}
		valid = append(valid, worktree)
	}
	if len(valid) == 0 {
		return nil, nil
	}
	return valid, nil
}

func hasWorktreeMetadata(repoRoot string) bool {
	commonDir := gitCommonDir(repoRoot)
	for _, path := range []string{
		filepath.Join(commonDir, "worktrees"),
		filepath.Join(repoRoot, "worktrees"),
	} {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// gitCommonDir resolves the shared metadata directory without starting an
// unbounded subprocess. A linked worktree has a .git file whose gitdir points
// at <common>/.git/worktrees/<name>; the main checkout has a .git directory.
func gitCommonDir(repoRoot string) string {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitPath)
	if err == nil && info.IsDir() {
		return gitPath
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return gitPath
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(line), prefix) {
		return gitPath
	}
	gitDir := strings.TrimSpace(line[len(prefix):])
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoRoot, gitDir)
	}
	return filepath.Clean(filepath.Join(gitDir, "..", ".."))
}

func hasParentTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func validWorktreePath(worktree WorktreeInfo) bool {
	if !filepath.IsAbs(worktree.Path) || hasParentTraversal(worktree.Path) {
		return false
	}
	info, err := os.Stat(worktree.Path)
	return err == nil && info.IsDir()
}

func runWorktreeGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}
