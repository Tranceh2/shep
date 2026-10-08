package command

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
	"github.com/tranceh2/shep/internal/queryhistory"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tui"
)

// worktreeDriver is an openDriver that also creates worktrees and renames
// workspaces, recording both.
type worktreeDriver struct {
	*openDriver
	created     herdr.CreatedWorktree
	createErr   error
	createdFrom string
	branch      string
	renamed     map[string]string
}

func (d *worktreeDriver) CreateWorktree(_ context.Context, repoPath, branch string) (herdr.CreatedWorktree, error) {
	d.createdFrom, d.branch = repoPath, branch
	return d.created, d.createErr
}

func (d *worktreeDriver) RenameWorkspace(_ context.Context, id, label string) error {
	if d.renamed == nil {
		d.renamed = map[string]string{}
	}
	d.renamed[id] = label
	return nil
}

// TestWorktreeCreator_NamesAndLaysOutTheNewWorkspace proves a new worktree's
// workspace is renamed to what workspace_name gives the worktree (the
// repo@branch form by default, as reopening it from the picker would name it)
// and laid out with the template the row it branched from resolves to.
func TestWorktreeCreator_NamesAndLaysOutTheNewWorkspace(t *testing.T) {
	t.Parallel()
	cfg := config.Defaults()
	cfg.Templates = map[string]config.TemplateConfig{"dev": {Command: "nvim ."}}
	cfg.Defaults.Template = "dev"
	repoPath := t.TempDir()
	driver := &worktreeDriver{
		openDriver: &openDriver{detect: true},
		created: herdr.CreatedWorktree{
			WorkspaceID: "w9", WorkspaceLabel: "login", RootTabID: "w9:t1", RootPaneID: "w9:p1",
			Path: repoPath + "-worktrees/login", Branch: "feat/login", RepoName: "api",
		},
	}
	app := New(WithHerdrDriver(driver), WithLayoutApplier(driver.openDriver))
	app.cfg = cfg
	app.probes = config.Probes{Herdr: true}

	repo := source.Candidate{Label: "api", Path: repoPath, NormalizedPath: repoPath, Source: config.SourceZoxide}
	msg := app.worktreeCreator()(context.Background(), repo, "feat/login")
	if msg.Err != nil {
		t.Fatalf("worktree: %v", msg.Err)
	}
	if driver.createdFrom != repoPath || driver.branch != "feat/login" {
		t.Errorf("CreateWorktree(%q, %q), want the row's path and the branch", driver.createdFrom, driver.branch)
	}
	if got := driver.renamed["w9"]; got != "api@feat/login" {
		t.Errorf("workspace renamed to %q, want api@feat/login", got)
	}
	if len(driver.ran) == 0 || !strings.Contains(strings.Join(driver.ran, " "), "nvim .") {
		t.Errorf("layout commands = %v, want the row's dev template", driver.ran)
	}
}

// TestWorktreeCreator_ReportsHerdrsRefusal proves a refused creation comes
// back as the message's error, with nothing renamed or laid out.
func TestWorktreeCreator_ReportsHerdrsRefusal(t *testing.T) {
	t.Parallel()
	driver := &worktreeDriver{openDriver: &openDriver{detect: true}, createErr: errors.New("branch already exists")}
	app := New(WithHerdrDriver(driver), WithLayoutApplier(driver.openDriver))
	app.cfg = config.Defaults()
	app.probes = config.Probes{Herdr: true}
	msg := app.worktreeCreator()(context.Background(), source.Candidate{Path: t.TempDir(), Source: config.SourceZoxide}, "main")
	if msg.Err == nil || len(driver.renamed) != 0 || len(driver.layouts) != 0 {
		t.Errorf("err %v, renamed %v, layouts %v; want only the refusal", msg.Err, driver.renamed, driver.layouts)
	}
}

// TestAttachPickerActions_SearchHistoryFollowsRanking proves the search
// history is on with ranking and off without it: disabling ranking keeps the
// picker from recording what the user searched.
func TestAttachPickerActions_SearchHistoryFollowsRanking(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		app := New()
		app.cfg = config.Defaults()
		app.cfg.Ranking.Enabled = enabled
		app.probes = config.Probes{}
		var layout tui.Layout
		app.attachPickerActions(&layout)
		if got := layout.RecordQuery != nil; got != enabled {
			t.Errorf("ranking enabled %v: search history recorded %v, want %v", enabled, got, enabled)
		}
	}
}

// TestRankingClear_ForgetsSavedSearches proves `shep ranking clear` also
// forgets the searches ctrl+y brings back, which learn from use like ranking.
func TestRankingClear_ForgetsSavedSearches(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(state, "shep", "queries")
	if err := queryhistory.Record(path, "allsafe"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := New(WithStreams(&out, &errOut)).executeArgs([]string{"ranking", "clear"}); err != nil {
		t.Fatalf("ranking clear: %v (%s)", err, errOut.String())
	}
	if got, _ := queryhistory.Load(path); got != nil {
		t.Errorf("searches after ranking clear = %q, want none", got)
	}
}
