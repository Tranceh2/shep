// Package templates applies a config.TemplateConfig (tabs, panes, splits,
// sizes, commands) to a freshly created Herdr workspace.
//
// A template is a full recipe for a freshly created workspace only —
// existing (focused) workspaces never have a template applied.
//
// Layouts are compiled before any I/O: compile.go turns the whole template into
// one herdr.LayoutApplyParams per tab, and Apply hands each of those to the
// daemon in a single `layout.apply` socket round trip. Each tab application is
// atomic, but Herdr exposes no whole-template transaction: a transport/runtime
// failure after earlier tabs succeed can leave a partial layout. Apply stops at
// that point and reports the applied progress.
//
// Two live facts the pure compiler cannot know are filled in here, and only
// here: the workspace id the layout targets, and the id of the root tab that
// `herdr workspace create` already opened. The first declared tab reuses that
// root tab by id so it is renamed in place instead of being left behind as an
// unused default beside a freshly created one; every later tab leaves tab_id
// empty so the daemon creates it (D5).
//
// Focus is resolved by the compiler and travels as the `focus` flag on each
// tab's params — no post-hoc focus commands are issued, because Herdr's
// `pane focus` accepts only --direction, never a positional pane id.
package templates

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/herdr"
)

// validPaneID matches the shape Herdr's own pane ids use. wrapCloseOnExit
// rejects anything outside this set so a hostile/malformed pane id can never
// smuggle shell metacharacters into the command typed into a pane's
// interactive shell.
var validPaneID = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

const defaultHerdrBinary = "herdr"

// LayoutApplier is the seam Apply dispatches through. It mirrors
// herdr.LayoutApplier so the production client satisfies it directly, while
// tests capture the dispatched params with no socket and no daemon in the
// loop (D2, R9.1).
type LayoutApplier interface {
	ApplyLayout(ctx context.Context, socketPath string, params herdr.LayoutApplyParams) (*herdr.LayoutApplyResult, error)
}

// CommandRunner types a command into an already-running pane. It is the
// narrow remnant of the old driver dependency, kept for RunCommand.
type CommandRunner interface {
	RunPane(ctx context.Context, paneID, command string) error
}

// Target identifies the freshly created workspace a template applies to: the
// workspace id, the id of the root tab the first declared tab should reuse,
// the cwd every pane inherits, the Herdr socket the layout is dispatched
// over, the login shell used by command-backed layout panes, and the pathEnv
// propagated to those panes. An empty RootTabID means the caller has no tab to
// donate, so every tab is created fresh.
type Target struct {
	WorkspaceID string
	RootTabID   string
	CWD         string
	SocketPath  string
	Shell       string
	PathEnv     string
}

// Apply realises tpl against target by compiling it to one layout per tab and
// dispatching each tab atomically.
//
// Compilation happens in full before the first dispatch, so an invalid
// geometry — a bad split axis, a cycle, a missing node — fails without a
// single byte reaching the socket. Herdr has no whole-template transaction;
// after a successful tab, a later dispatch failure may leave the workspace
// partially applied. The loop stops immediately and names the applied progress.
func Apply(ctx context.Context, applier LayoutApplier, target Target, tpl config.TemplateConfig) error {
	params, err := CompileTemplate(tpl, target.CWD, target.Shell, target.PathEnv)
	if err != nil {
		return fmt.Errorf("compile template: %w", err)
	}

	applied := 0
	for i := range params {
		// Only the first tab may adopt the workspace's existing root tab;
		// pinning tab_id on the rest would make every later tab overwrite
		// that same tab instead of creating its own.
		// The Herdr daemon requires exactly one target identity: use either
		// tab_id or workspace_id, not both.
		if i == 0 && target.RootTabID != "" {
			params[i].TabID = target.RootTabID
			params[i].WorkspaceID = ""
		} else {
			params[i].WorkspaceID = target.WorkspaceID
			params[i].TabID = ""
		}
		if _, err := applier.ApplyLayout(ctx, target.SocketPath, params[i]); err != nil {
			if applied == 0 {
				return fmt.Errorf("apply layout for tab %q (no previous tabs applied): %w", params[i].TabLabel, err)
			}
			return fmt.Errorf("apply layout for tab %q after %d tab(s) applied; partial template remains: %w", params[i].TabLabel, applied, err)
		}
		applied++
	}
	return nil
}

// RunCommand types tpl's command into an existing pane.
//
// This is the one place a command still reaches a pane outside layout.apply,
// and it exists for the --target=tab/--target=pane launches: those open into
// a container the caller has already created inside a LIVE workspace, where
// applying a layout would replace the surrounding tab rather than fill the
// new pane. An empty command leaves the pane a plain shell.
func RunCommand(ctx context.Context, runner CommandRunner, paneID, binary string, tpl config.TemplateConfig) error {
	if tpl.Command == "" {
		return nil
	}
	if err := runner.RunPane(ctx, paneID, wrapCloseOnExit(tpl.Command, paneID, binary, tpl.CloseOnExit)); err != nil {
		return fmt.Errorf("run command in pane %s: %w", paneID, err)
	}
	return nil
}

// wrapCloseOnExit returns cmd unchanged when on is false, otherwise appends
// a shell-quoted close action so the pane closes itself once the command's
// shell returns control. When binary is empty it defaults to "herdr". The
// chaining exists because Herdr has no native close-on-exit primitive, so the
// only way to express it is inside the command itself.
//
// paneID is validated against validPaneID before being concatenated: a paneID
// carrying shell metacharacters would otherwise let arbitrary commands run in
// the pane's shell. A paneID that fails validation degrades to the same
// defensive no-wrap as on=false or an empty paneID.
func wrapCloseOnExit(cmd, paneID, binary string, on bool) string {
	if !on || paneID == "" || !validPaneID.MatchString(paneID) {
		return cmd
	}
	if binary == "" {
		binary = defaultHerdrBinary
	}
	return cmd + "; " + ShellQuote(binary) + " pane close " + ShellQuote(paneID)
}

// ShellCommand returns argv as POSIX shell words. RunPane types shell text into
// an existing pane, so every configured value must remain data rather than shell
// source. Single-quote escaping preserves spaces, metacharacters, and quotes.
func ShellCommand(argv ...string) string {
	quoted := make([]string, len(argv))
	for i, value := range argv {
		quoted[i] = ShellQuote(value)
	}
	return strings.Join(quoted, " ")
}

// ShellQuote returns one POSIX shell word for callers that must append a
// single externally supplied argument to shell text.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
