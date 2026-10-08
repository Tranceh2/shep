package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/resolver"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/templates"
	"github.com/tranceh2/shep/internal/tui"
)

const (
	// renameTimeout bounds one rename request.
	renameTimeout = 10 * time.Second
	// worktreeTimeout bounds creating a worktree and laying out its
	// workspace: Git checks the branch out, which takes a while in a large
	// repository.
	worktreeTimeout = time.Minute
)

// attachPickerActions gives the picker its command-layer actions: live agent
// status, pins, acknowledgement clearing, and the ones that change Herdr
// (close, rename, new worktree).
func (a *App) attachPickerActions(layout *tui.Layout) {
	layout.StatusDialer = a.resolveStatusDialer()
	layout.PinToggler = a.pinToggler()
	layout.Closer = a.herdrCloser()
	layout.AckClearer = a.ackClearer()
	layout.Renamer = a.herdrRenamer()
	layout.WorktreeCreator = a.worktreeCreator()
}

// herdrRenamer renames open Herdr items through the shared driver, as
// herdrCloser closes them. A missing Herdr leaves the action unavailable.
func (a *App) herdrRenamer() tui.Renamer {
	if !a.Probes().Herdr {
		return nil
	}
	driver, ok := a.Driver().(interface {
		RenameWorkspace(context.Context, string, string) error
		RenameTab(context.Context, string, string) error
		RenamePane(context.Context, string, *string) error
	})
	if !ok {
		return nil
	}
	return func(ctx context.Context, kind, id, label string) tui.RenameResultMsg {
		ctx, cancel := context.WithTimeout(ctx, renameTimeout)
		defer cancel()
		var err error
		switch kind {
		case "workspace":
			err = driver.RenameWorkspace(ctx, id, label)
		case "tab":
			err = driver.RenameTab(ctx, id, label)
		case "pane":
			err = driver.RenamePane(ctx, id, &label)
		default:
			err = fmt.Errorf("not an open Herdr item: %s", kind)
		}
		return tui.RenameResultMsg{Err: err}
	}
}

// worktreeCreator opens a new Git worktree of a row's repository. Herdr
// creates the worktree and a focused workspace on it; shep then names the
// workspace and lays it out the way it does any workspace it creates: the
// name from workspace_name for the worktree, the layout from the template of
// the row it branched from.
func (a *App) worktreeCreator() tui.WorktreeCreator {
	if !a.Probes().Herdr {
		return nil
	}
	driver, ok := a.Driver().(interface {
		CreateWorktree(context.Context, string, string) (herdr.CreatedWorktree, error)
		RenameWorkspace(context.Context, string, string) error
	})
	if !ok {
		return nil
	}
	return func(ctx context.Context, repo source.Candidate, branch string) tui.WorktreeResultMsg {
		ctx, cancel := context.WithTimeout(ctx, worktreeTimeout)
		defer cancel()
		created, err := driver.CreateWorktree(ctx, repo.Path, branch)
		if err != nil {
			return tui.WorktreeResultMsg{Err: err}
		}
		request, err := a.workspaceLaunchRequest(worktreeCandidate(repo, created))
		if err != nil {
			return tui.WorktreeResultMsg{Err: err}
		}
		if name := string(request.WorkspaceName); name != "" && name != created.WorkspaceLabel {
			if err := driver.RenameWorkspace(ctx, created.WorkspaceID, name); err != nil {
				return tui.WorktreeResultMsg{Err: err}
			}
		}
		target := templates.Target{
			WorkspaceID: created.WorkspaceID,
			RootTabID:   created.RootTabID,
			CWD:         created.Path,
			SocketPath:  currentHerdrSocketPath(),
			Shell:       os.Getenv("SHELL"),
			PathEnv:     os.Getenv("PATH"),
		}
		if err := templates.Apply(ctx, a.LayoutApplier(), target, resolveTemplate(a.settings().For(repo), a.Config())); err != nil {
			return tui.WorktreeResultMsg{Err: fmt.Errorf("template: %w", err)}
		}
		return tui.WorktreeResultMsg{}
	}
}

// worktreeCandidate is the projects row shep would list for a worktree
// created from repo (see the projects source's worktree rows), so the
// workspace is named exactly as reopening it from the picker would name it.
func worktreeCandidate(repo source.Candidate, wt herdr.CreatedWorktree) source.Candidate {
	repoName := wt.RepoName
	if repoName == "" {
		repoName = repo.Meta["repo"]
	}
	if repoName == "" {
		repoName = filepath.Base(repo.Path)
	}
	normalized, err := resolver.Normalize(wt.Path)
	if err != nil {
		normalized = filepath.Clean(wt.Path)
	}
	return source.Candidate{
		Path:           wt.Path,
		NormalizedPath: normalized,
		Label:          fmt.Sprintf("%s (%s)", repoName, wt.Branch),
		Source:         config.SourceProjects,
		Meta: map[string]string{
			"is_worktree": "true", "main_worktree": "false", "branch": wt.Branch,
			"repo": repoName, "worktree_path": wt.Path,
		},
	}
}
