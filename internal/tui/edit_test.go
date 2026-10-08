package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
)

// renameCall records one Renamer invocation.
type renameCall struct{ kind, id, label string }

// recordingRenamer returns a Renamer that records its calls and answers err.
func recordingRenamer(calls *[]renameCall, err error) Renamer {
	return func(_ context.Context, kind, id, label string) RenameResultMsg {
		*calls = append(*calls, renameCall{kind, id, label})
		return RenameResultMsg{Err: err}
	}
}

// renameModel is a sized picker over one open workspace, with renaming wired.
func renameModel(t *testing.T, renamer Renamer) Model {
	t.Helper()
	m := NewModelWithLayout([]source.Candidate{herdrCandidate("backend api", "/svc", "w1")}, nil, Layout{Renamer: renamer})
	m, _ = update(t, m, sizeMsg(120, 30))
	return m
}

// TestRename_EditsTheCurrentNameAndApplies proves ctrl+e opens the line edit
// on the highlighted workspace, filled with its name, in place of the search
// prompt; editing keys change only the edit; Enter renames through the
// Renamer and reports it in the footer.
func TestRename_EditsTheCurrentNameAndApplies(t *testing.T) {
	t.Parallel()
	var calls []renameCall
	m := renameModel(t, recordingRenamer(&calls, nil))

	m, _ = update(t, m, key("ctrl+e"))
	if !m.edit.open() || m.edit.text != "backend api" {
		t.Fatalf("after ctrl+e: edit %+v, want it open on the workspace's name", m.edit)
	}
	if got := promptText(m); !strings.HasPrefix(got, "rename workspace") || !strings.Contains(got, "backend api") {
		t.Errorf("prompt = %q, want the rename edit in place of the search", got)
	}
	if got := footerText(m); !strings.Contains(got, "enter rename") || !strings.Contains(got, "esc cancel") {
		t.Errorf("footer = %q, want enter rename and esc cancel", got)
	}

	m, _ = update(t, m, key("ctrl+w"))
	m = typeText(t, m, "gateway")
	if m.edit.text != "backend gateway" || m.query != "" {
		t.Fatalf("edit %q query %q, want the edit changed and the query untouched", m.edit.text, m.query)
	}
	m, cmd := update(t, m, key("enter"))
	if m.edit.open() || cmd == nil {
		t.Fatalf("after enter: edit open %v, cmd %v; want it closed and the rename started", m.edit.open(), cmd)
	}
	m, _ = update(t, m, cmd())
	if len(calls) != 1 || calls[0] != (renameCall{"workspace", "w1", "backend gateway"}) {
		t.Errorf("Renamer calls = %+v, want workspace w1 renamed to backend gateway", calls)
	}
	if !strings.Contains(footerText(m), "renamed workspace") {
		t.Errorf("footer = %q, want the rename reported", footerText(m))
	}
}

// TestRename_EscKeepsEverything proves Esc closes the edit without renaming,
// leaving the query as it was.
func TestRename_EscKeepsEverything(t *testing.T) {
	t.Parallel()
	var calls []renameCall
	m := renameModel(t, recordingRenamer(&calls, nil))
	m = typeText(t, m, "back")
	m, _ = update(t, m, key("ctrl+e"))
	m = typeText(t, m, "x")
	m, cmd := update(t, m, key("esc"))
	if m.edit.open() || cmd != nil || m.query != "back" || len(calls) != 0 {
		t.Errorf("after esc: edit open %v, cmd %v, query %q, calls %v; want it closed and nothing else changed", m.edit.open(), cmd, m.query, calls)
	}
}

// TestRename_RefusesWhatItCannotApply proves an empty workspace name and a
// row that is not an open Herdr item are refused in the footer, without a
// request, and a failed rename is reported.
func TestRename_RefusesWhatItCannotApply(t *testing.T) {
	t.Parallel()
	var calls []renameCall
	m := renameModel(t, recordingRenamer(&calls, nil))
	m, _ = update(t, m, key("ctrl+e"))
	m, _ = update(t, m, key("ctrl+w"))
	m, _ = update(t, m, key("ctrl+w"))
	if m, cmd := update(t, m, key("enter")); cmd != nil || !strings.Contains(footerText(m), "cannot be empty") {
		t.Errorf("empty name: cmd %v, footer %q; want it refused", cmd, footerText(m))
	}

	z := NewModelWithLayout([]source.Candidate{zoxideCandidate("notes", "/notes")}, nil, Layout{Renamer: recordingRenamer(&calls, nil)})
	z, _ = update(t, z, sizeMsg(120, 30))
	z, _ = update(t, z, key("ctrl+e"))
	if z.edit.open() || !strings.Contains(footerText(z), "not an open Herdr item") {
		t.Errorf("zoxide row: edit open %v, footer %q; want it refused", z.edit.open(), footerText(z))
	}

	failing := renameModel(t, recordingRenamer(&calls, errors.New("workspace_not_found")))
	failing, _ = update(t, failing, key("ctrl+e"))
	failing = typeText(t, failing, "2")
	failing, cmd := update(t, failing, key("enter"))
	failing, _ = update(t, failing, cmd())
	if !strings.Contains(footerText(failing), "rename failed: workspace_not_found") {
		t.Errorf("failed rename footer = %q, want the failure", footerText(failing))
	}
}

// gitRepo makes a directory holding a .git entry and a subdirectory in it.
func gitRepo(t *testing.T) (root, sub string) {
	t.Helper()
	root = t.TempDir()
	sub = filepath.Join(root, "cmd", "app")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

// TestWorktree_BranchesFromTheRowAndQuits proves ctrl+n on a row inside a Git
// repository asks for a branch, creates the worktree through the
// WorktreeCreator, and quits quietly once it exists (Herdr focused it).
func TestWorktree_BranchesFromTheRowAndQuits(t *testing.T) {
	t.Parallel()
	_, sub := gitRepo(t)
	var gotRepo source.Candidate
	var gotBranch string
	creator := func(_ context.Context, repo source.Candidate, branch string) WorktreeResultMsg {
		gotRepo, gotBranch = repo, branch
		return WorktreeResultMsg{}
	}
	m := NewModelWithLayout([]source.Candidate{zoxideCandidate("app", sub)}, nil, Layout{WorktreeCreator: creator})
	m, _ = update(t, m, sizeMsg(120, 30))

	m, _ = update(t, m, key("ctrl+n"))
	if !m.edit.open() || !strings.HasPrefix(promptText(m), "new worktree branch") {
		t.Fatalf("after ctrl+n: prompt %q, want the branch edit", promptText(m))
	}
	m = typeText(t, m, "feat/login")
	m, cmd := update(t, m, key("enter"))
	if cmd == nil || !strings.Contains(footerText(m), "creating worktree feat/login") {
		t.Fatalf("after enter: cmd %v footer %q, want the worktree in progress", cmd, footerText(m))
	}
	m, cmd = update(t, m, cmd())
	if gotRepo.Path != sub || gotBranch != "feat/login" {
		t.Errorf("creator got %q %q, want the row's path and the branch", gotRepo.Path, gotBranch)
	}
	if cmd == nil {
		t.Fatal("no quit after the worktree was created")
	}
	if _, _, _, ok, err := finalizeRun(m); ok || !errors.Is(err, ErrCancelled) {
		t.Errorf("finalizeRun = ok %v err %v, want a quiet exit without a selection", ok, err)
	}
}

// TestWorktree_RefusesOutsideARepositoryAndReportsFailures proves ctrl+n on a
// directory outside any Git repository is refused, and a failed creation
// stays in the picker with the reason.
func TestWorktree_RefusesOutsideARepositoryAndReportsFailures(t *testing.T) {
	t.Parallel()
	creator := func(context.Context, source.Candidate, string) WorktreeResultMsg {
		return WorktreeResultMsg{Err: errors.New("branch already exists")}
	}
	plain := NewModelWithLayout([]source.Candidate{zoxideCandidate("tmp", t.TempDir())}, nil, Layout{WorktreeCreator: creator})
	plain, _ = update(t, plain, sizeMsg(120, 30))
	plain, _ = update(t, plain, key("ctrl+n"))
	if plain.edit.open() || !strings.Contains(footerText(plain), "not a Git repository") {
		t.Errorf("outside a repository: edit open %v footer %q, want it refused", plain.edit.open(), footerText(plain))
	}

	root, _ := gitRepo(t)
	m := NewModelWithLayout([]source.Candidate{projectCandidate("app", root)}, nil, Layout{WorktreeCreator: creator})
	m, _ = update(t, m, sizeMsg(120, 30))
	m, _ = update(t, m, key("ctrl+n"))
	m = typeText(t, m, "main")
	m, cmd := update(t, m, key("enter"))
	m, quit := update(t, m, cmd())
	if quit != nil || m.finished || !strings.Contains(footerText(m), "worktree failed: branch already exists") {
		t.Errorf("failed worktree: quit %v finished %v footer %q, want it reported in the picker", quit, m.finished, footerText(m))
	}
}
