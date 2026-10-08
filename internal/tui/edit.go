package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// RenameResultMsg is the typed completion of a rename (ctrl+e).
type RenameResultMsg struct {
	Kind  string
	ID    string
	Label string
	Err   error
}

// Renamer relabels an open Herdr item ("workspace", "tab" or "pane", by id)
// outside the TUI update loop. An empty pane label clears it.
type Renamer func(ctx context.Context, kind, id, label string) RenameResultMsg

// WorktreeResultMsg is the typed completion of a new worktree (ctrl+n).
type WorktreeResultMsg struct {
	Branch string
	Err    error
}

// WorktreeCreator creates a Git worktree on a new branch of the repository
// holding repo's path and opens it as a workspace, named and laid out like
// any workspace shep creates, outside the TUI update loop.
type WorktreeCreator func(ctx context.Context, repo source.Candidate, branch string) WorktreeResultMsg

// editKind is what a lineEdit is for.
type editKind int

const (
	editNone editKind = iota
	editRename
	editWorktree
)

// lineEdit is the one-line input that replaces the search prompt while it is
// open: the new name of the highlighted Herdr item, or the branch of a new
// worktree. Enter submits it and Esc closes it; the query and the list stay
// as they were. The zero value is closed.
type lineEdit struct {
	kind   editKind
	prompt string
	text   string
	// item is the Herdr item a rename applies to.
	item herdrItem
	// repo is the row a new worktree branches from.
	repo source.Candidate
}

func (e lineEdit) open() bool { return e.kind != editNone }

// submitLabel is the footer's word for what Enter does in the edit.
func (e lineEdit) submitLabel() string {
	if e.kind == editWorktree {
		return "create"
	}
	return "rename"
}

// handleEditKey applies one input while the line edit is open: Enter
// submits, Esc closes it, ctrl+c/ctrl+g still quit the picker, backspace and
// ctrl+w/alt+backspace delete, and any text is appended.
func (m Model) handleEditKey(chord, text string) (Model, tea.Cmd) {
	switch chord {
	case keyChordEnter:
		return m.submitEdit()
	case keyChordEsc:
		m.edit = lineEdit{}
		return m, nil
	case keyChordCtrlC, keyChordCtrlG:
		m.cancelled = true
		return m, tea.Quit
	case keyChordBackspace:
		m.edit.text = deleteLastRune(m.edit.text)
	case keyChordCtrlW, keyChordAltBksp:
		m.edit.text = deleteLastWord(m.edit.text)
	default:
		if t, ok := appendQueryText(m.edit.text, text); ok {
			m.edit.text = t
		}
	}
	return m, nil
}

// startRename opens the line edit on the highlighted open Herdr item (a
// workspace, tab, pane or agent row), filled with its current name.
func (m Model) startRename() (Model, tea.Cmd) {
	if m.actionPending {
		return m, nil
	}
	row, ok := m.currentRow()
	item, isItem := herdrItemFor(row)
	switch {
	case !ok || !isItem:
		m.actionStatus = errorStatus("not an open Herdr item")
	case m.layout.Renamer == nil:
		m.actionStatus = errorStatus("Herdr rename unavailable")
	default:
		m.edit = lineEdit{kind: editRename, prompt: "rename " + item.kind, text: item.label, item: item}
	}
	return m, nil
}

// startWorktree opens the line edit for the branch of a new worktree of the
// Git repository the highlighted row's path is in.
func (m Model) startWorktree() (Model, tea.Cmd) {
	if m.actionPending {
		return m, nil
	}
	row, ok := m.currentRow()
	switch {
	case !ok || row.Kind != RowCandidate || !worktreeSource(row.Candidate) || gitRepoRoot(row.Candidate.Path) == "":
		m.actionStatus = errorStatus("not a Git repository")
	case m.layout.WorktreeCreator == nil:
		m.actionStatus = errorStatus("worktrees unavailable")
	default:
		m.edit = lineEdit{kind: editWorktree, prompt: "new worktree branch", repo: row.Candidate}
	}
	return m, nil
}

// worktreeSource reports whether c is a directory row a worktree can branch
// from: an open workspace, a project, a zoxide entry or a configured
// workspace. Sessions, agents and custom rows are not directories of their
// own.
func worktreeSource(c source.Candidate) bool {
	switch c.Source {
	case config.SourceHerdr, config.SourceProjects, config.SourceZoxide, config.SourceWorkspaces:
		return c.Path != ""
	}
	return false
}

// gitRepoRoot returns the nearest directory at or above path holding a .git
// entry (a directory, or the file of a linked worktree), or "" when none
// does. It runs on a key press only, never per frame.
func gitRepoRoot(path string) string {
	if path == "" {
		return ""
	}
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent == dir {
			return ""
		}
	}
}

// submitEdit closes the line edit and starts what it was open for.
func (m Model) submitEdit() (Model, tea.Cmd) {
	e := m.edit
	m.edit = lineEdit{}
	ctx := m.renderCtx
	switch e.kind {
	case editRename:
		if e.text == e.item.label {
			return m, nil
		}
		if strings.TrimSpace(e.text) == "" && e.item.kind != "pane" {
			m.actionStatus = errorStatus("a " + e.item.kind + " name cannot be empty")
			return m, nil
		}
		m.actionPending = true
		m.actionStatus = infoStatus("renaming " + e.item.kind + "...")
		renamer, item, label := m.layout.Renamer, e.item, e.text
		return m, func() tea.Msg {
			msg := renamer(ctx, item.kind, item.id, label)
			msg.Kind, msg.ID, msg.Label = item.kind, item.id, label
			return msg
		}
	case editWorktree:
		branch := strings.TrimSpace(e.text)
		if branch == "" {
			m.actionStatus = errorStatus("a branch name cannot be empty")
			return m, nil
		}
		m.actionPending = true
		m.actionStatus = infoStatus("creating worktree " + branch + "...")
		creator, repo := m.layout.WorktreeCreator, e.repo
		return m, func() tea.Msg {
			msg := creator(ctx, repo, branch)
			msg.Branch = branch
			return msg
		}
	}
	return m, nil
}

// handleRenameResult reports a finished rename and, after a successful one,
// refreshes the rows so they show the new name.
func (m Model) handleRenameResult(msg RenameResultMsg) (Model, tea.Cmd) {
	if !m.actionPending {
		return m, nil
	}
	m.actionPending = false
	if msg.Err != nil {
		m.actionStatus = errorStatus("rename failed: " + msg.Err.Error())
		return m, nil
	}
	m.actionStatus = successStatus("renamed " + msg.Kind)
	return m, m.refreshAfterHerdrChange()
}

// handleWorktreeResult reports a failed worktree in the picker, or quits
// after a created one: Herdr already focused its workspace, so the picker's
// work is done and it closes quietly.
func (m Model) handleWorktreeResult(msg WorktreeResultMsg) (Model, tea.Cmd) {
	if !m.actionPending {
		return m, nil
	}
	m.actionPending = false
	if msg.Err != nil {
		m.actionStatus = errorStatus("worktree failed: " + msg.Err.Error())
		return m, nil
	}
	m.finished = true
	return m, tea.Quit
}

// refreshAfterHerdrChange asks for a fresh snapshot once the picker changed
// Herdr itself (closed or renamed an item), so the rows show the result. A
// refresh already in flight may predate the change: the pending flag makes
// its response request another generation.
func (m *Model) refreshAfterHerdrChange() tea.Cmd {
	if m.snapshotDriver == nil {
		return nil
	}
	m.herdrRefreshPending = true
	if m.snapshotRefreshing {
		return nil
	}
	m.lastSnapshotAt = m.now().Add(-snapshotTTL)
	cmd := m.maybeRefreshSnapshot()
	if cmd != nil {
		m.herdrRefreshPending = false
	}
	return cmd
}
