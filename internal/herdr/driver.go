// Package herdr implements the real shep <-> Herdr bridge behind the
// source.HerdrDriver interface.
//
// Inside Herdr (HERDR_SOCKET_PATH set) every server request goes straight to
// the server socket (socket.go); otherwise it goes through the herdr CLI,
// which sends the same request and prints the same JSON envelope:
//
//	{"id":"cli:<command>","result":{...}}
//
// The shapes captured against a live Herdr daemon are:
//
//	workspace list {"id":"cli:workspace:list","result":{"type":"workspace_list",
//	              "workspaces":[{workspace_id,label,active_tab_id,focused,number,...}]}}
//	pane list     {"id":"cli:pane:list","result":{"panes":[{pane_id,workspace_id,
//	              cwd,foreground_cwd,focused,...}]}}
//	pane current  {"id":"cli:pane:current","result":{"pane":{pane_id,workspace_id,
//	              tab_id,cwd,foreground_cwd,focused,...}}}
//
// Workspaces do NOT carry a cwd; the driver joins workspaces to panes by
// workspace_id to derive a representative cwd. CLI execution goes through a
// small CommandRunner so tests inject a fake instead of shelling out.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/source"
)

// validPaneID matches the shape Herdr's own pane ids use. CreateTab,
// SplitPane rejects any pane_id outside this set as
// defense-in-depth: those ids eventually reach
// internal/templates.wrapCloseOnExit's shell-chained command construction, so
// a hostile or malformed Herdr response must never carry shell
// metacharacters through this layer.
var validPaneID = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

// CommandRunner executes a named command and returns its stdout. The default
// implementation shells out via exec.CommandContext; tests inject a fake to
// drive the driver deterministically without touching a real Herdr daemon.
type CommandRunner interface {
	// Run executes name with args under ctx and returns the captured stdout.
	// A non-nil error means the command failed (non-zero exit or could not
	// start); callers decide how to degrade.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// execRunner is the production CommandRunner backed by os/exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// lookPathFn is the signature of exec.LookPath so tests can substitute a
// fake without polluting PATH.
type lookPathFn func(string) (string, error)

// WithBinaryEnv injects the plugin runtime environment lookup. The default
// reads HERDR_BIN_PATH only when the driver is constructed, keeping command
// resolution deterministic for long-lived drivers and tests.
func WithBinaryEnv(lookup func(string) (string, bool)) Option {
	return func(d *Driver) {
		if lookup == nil {
			return
		}
		if candidate, ok := lookup("HERDR_BIN_PATH"); ok && validBinaryPath(candidate) {
			d.binary = candidate
		}
	}
}

func validBinaryPath(path string) bool {
	return config.ValidBinaryPath(path)
}

// Driver is the real source.HerdrDriver, backed by the Herdr server socket
// when it has one and by the herdr CLI otherwise. It is safe to construct one
// per command invocation; all state lives in the JSON envelopes returned by
// Herdr.
type Driver struct {
	binary   string
	socket   string
	run      CommandRunner
	lookPath lookPathFn
}

// Option configures a Driver at construction.
type Option func(*Driver)

// WithSocketPath sends server requests to the Herdr socket at path instead of
// launching the CLI for each one. Pass HERDR_SOCKET_PATH, which Herdr sets in
// its panes and plugin commands; an empty path keeps every request on the CLI.
func WithSocketPath(path string) Option {
	return func(d *Driver) { d.socket = path }
}

// WithRunner injects a CommandRunner (intended for tests).
func WithRunner(r CommandRunner) Option {
	return func(d *Driver) { d.run = r }
}

// WithLookPath injects a lookup function (intended for tests); the default is
// exec.LookPath.
func WithLookPath(f lookPathFn) Option {
	return func(d *Driver) { d.lookPath = f }
}

// New builds a Driver for the supplied binary name (falling back to "herdr").
// A nil runner is replaced with the exec-backed default.
func New(binary string, opts ...Option) *Driver {
	if binary == "" {
		binary = "herdr"
	}
	d := &Driver{binary: binary, run: execRunner{}, lookPath: exec.LookPath}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Detect reports whether the Herdr binary is on PATH (HI-1). Daemon liveness
// is discovered lazily by the subsequent command calls; Detect stays cheap.
func (d *Driver) Detect(_ context.Context) bool {
	if d.lookPath == nil {
		_, err := exec.LookPath(d.binary)
		return err == nil
	}
	_, err := d.lookPath(d.binary)
	return err == nil
}

// viaSocket sends one request to the server socket, decoding the response's
// result into result unless it is nil. ok is false when the request did not
// go out: the driver has no socket, or the server rejected the method as
// unknown (an older Herdr), and the caller should use the CLI instead.
func (d *Driver) viaSocket(ctx context.Context, method string, params, result any) (ok bool, err error) {
	if d.socket == "" {
		return false, nil
	}
	raw, err := callSocket(ctx, d.socket, nextRequestID(method), method, params, maxResponseBytes)
	if unknownMethod(err) {
		return false, nil
	}
	if err != nil || result == nil {
		return true, err
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return true, fmt.Errorf("parse: %w", err)
	}
	return true, nil
}

// send performs one server request over the socket (see viaSocket), or by
// running the CLI with cliArgs, whose stdout is the same response envelope.
// The response's result is decoded into result unless it is nil.
func (d *Driver) send(ctx context.Context, result any, method string, params any, cliArgs ...string) error {
	if ok, err := d.viaSocket(ctx, method, params, result); ok {
		return err
	}
	out, err := d.run.Run(ctx, d.binary, cliArgs...)
	if err != nil || result == nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	return nil
}

// --- JSON envelopes (captured against a live Herdr daemon) ---

type rawWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	ActiveTabID string `json:"active_tab_id"`
	Focused     bool   `json:"focused"`
}

type rawPane struct {
	PaneID        string `json:"pane_id"`
	Label         string `json:"label"`
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Focused       bool   `json:"focused"`
	Agent         string `json:"agent"`
	AgentStatus   string `json:"agent_status"`
	TerminalTitle string `json:"terminal_title"`
}

type rawTab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
}

// snapshotResult is the result of `session.snapshot` (`herdr api
// snapshot`): one coherent state generation containing the workspace, tab,
// and pane records that previously needed several independent calls.
type snapshotResult struct {
	Snapshot rawSnapshot `json:"snapshot"`
}

// sessionsEnvelope is the verified `herdr session list --json` 0.7.4 shape.
// It intentionally has no result wrapper: accepting a legacy shape would hide
// incompatible output behind an apparently valid empty list.
type sessionsEnvelope struct {
	Sessions []rawSession `json:"sessions"`
}

type rawSession struct {
	Name       string `json:"name"`
	Running    bool   `json:"running"`
	Default    bool   `json:"default"`
	SessionDir string `json:"session_dir"`
	SocketPath string `json:"socket_path"`
}

type rawSnapshot struct {
	Workspaces         []rawWorkspace `json:"workspaces"`
	Tabs               []rawTab       `json:"tabs"`
	Panes              []rawPane      `json:"panes"`
	FocusedWorkspaceID string         `json:"focused_workspace_id"`
	FocusedTabID       string         `json:"focused_tab_id"`
	FocusedPaneID      string         `json:"focused_pane_id"`
}

// Snapshot obtains one full coherent state generation. Records without their
// primary identity are ignored so a partial daemon response cannot invalidate
// complete neighboring records.
func (d *Driver) Snapshot(ctx context.Context) (source.Snapshot, error) {
	var result snapshotResult
	if err := d.send(ctx, &result, "session.snapshot", struct{}{}, "api", "snapshot"); err != nil {
		return source.Snapshot{}, fmt.Errorf("herdr api snapshot: %w", err)
	}
	return rawSnapshotToSnapshot(result.Snapshot), nil
}

// ListSessions lists local Herdr sessions through the official CLI boundary:
// sessions span servers, so no server socket answers for them. Only the
// verified top-level 0.7.4 envelope is accepted; unknown fields are ignored
// and incomplete records without a name are not actionable.
func (d *Driver) ListSessions(ctx context.Context) ([]source.Session, error) {
	out, err := d.run.Run(ctx, d.binary, "session", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("herdr session list: %w", err)
	}
	var env sessionsEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("herdr session list: parse: %w", err)
	}
	if env.Sessions == nil {
		return nil, errors.New("herdr session list: missing sessions envelope")
	}
	sessions := make([]source.Session, 0, len(env.Sessions))
	for _, raw := range env.Sessions {
		if raw.Name == "" {
			continue
		}
		sessions = append(sessions, source.Session{
			Name:       raw.Name,
			Running:    raw.Running,
			Default:    raw.Default,
			SessionDir: raw.SessionDir,
			SocketPath: raw.SocketPath,
		})
	}
	return sessions, nil
}

func rawSnapshotToSnapshot(raw rawSnapshot) source.Snapshot {
	snapshot := source.Snapshot{
		Workspaces:         make([]source.Workspace, 0, len(raw.Workspaces)),
		Tabs:               make([]source.Tab, 0, len(raw.Tabs)),
		Panes:              make([]source.Pane, 0, len(raw.Panes)),
		FocusedWorkspaceID: raw.FocusedWorkspaceID,
		FocusedTabID:       raw.FocusedTabID,
		FocusedPaneID:      raw.FocusedPaneID,
	}
	for _, workspace := range raw.Workspaces {
		if workspace.WorkspaceID == "" {
			continue
		}
		snapshot.Workspaces = append(snapshot.Workspaces, source.Workspace{
			ID:          workspace.WorkspaceID,
			Label:       workspace.Label,
			ActiveTabID: workspace.ActiveTabID,
			Focused:     workspace.Focused,
		})
	}
	for _, tab := range raw.Tabs {
		if tab.TabID == "" {
			continue
		}
		snapshot.Tabs = append(snapshot.Tabs, source.Tab{
			ID:          tab.TabID,
			WorkspaceID: tab.WorkspaceID,
			Label:       tab.Label,
			Focused:     tab.Focused,
			Number:      tab.Number,
			PaneCount:   tab.PaneCount,
		})
	}
	for _, pane := range raw.Panes {
		if pane.PaneID == "" {
			continue
		}
		snapshot.Panes = append(snapshot.Panes, rawPaneToPane(pane))
	}
	return snapshot
}

// workspaceCreatedResult is the result of `workspace.create` (`herdr
// workspace create`):
//
//	{"type":"workspace_created","workspace":{...},"tab":{...},"root_pane":{...}}
type workspaceCreatedResult struct {
	Workspace rawWorkspace `json:"workspace"`
	Tab       rawTab       `json:"tab"`
	RootPane  rawPane      `json:"root_pane"`
}

// tabCreatedResult is the result of `tab.create` (`herdr tab create`):
//
//	{"type":"tab_created","tab":{...},"root_pane":{...}}
type tabCreatedResult struct {
	Tab      rawTab  `json:"tab"`
	RootPane rawPane `json:"root_pane"`
}

// paneInfoResult is the result of `pane.split` (`herdr pane split`) and
// other pane_info answers:
//
//	{"type":"pane_info","pane":{...}}
type paneInfoResult struct {
	Pane rawPane `json:"pane"`
}

// FocusOrCreate resumes or opens a workspace for cand, deciding solely from
// the candidate's source — Dedup is the single source of truth for "already
// open", so this method never scans open panes/workspaces to look for a
// CWD/label match:
//   - A herdr-sourced candidate (an already-open workspace surfaced by the
//     herdr provider) is resumed by focusing its Meta["workspace_id"]; no
//     create, and no workspace/pane list probe is issued.
//   - Any other candidate (zoxide, projects, a [[workspaces]] config entry,
//     or a direct --path) creates a new focused workspace.
//
// The request's candidate carries its own NormalizedPath (filled by the
// resolver); when empty we normalise on the fly against cand.Path so the
// fallback label (filepath.Base of a clean path) is stable. A freshly created
// workspace's root tab id/pane id are returned so the caller can apply a
// template.
func (d *Driver) FocusOrCreate(ctx context.Context, request source.WorkspaceLaunchRequest) (source.FocusResult, error) {
	cand := request.Candidate
	if cand.Source == config.SourceHerdr {
		id := cand.Meta["workspace_id"]
		if err := d.send(ctx, nil, "workspace.focus", map[string]any{"workspace_id": id}, "workspace", "focus", id); err != nil {
			return source.FocusResult{}, fmt.Errorf("herdr workspace focus %s: %w", id, err)
		}
		return source.FocusResult{WorkspaceID: id, Action: source.HerdrActionFocused}, nil
	}

	needle := cand.NormalizedPath
	if needle == "" {
		n, err := pathutil.Normalize(cand.Path)
		if err != nil {
			return source.FocusResult{}, fmt.Errorf("normalize candidate path: %w", err)
		}
		needle = n
	}
	label := string(request.WorkspaceName)
	if label == "" {
		label = cand.Label
	}
	if label == "" {
		label = filepath.Base(needle)
	}
	var created workspaceCreatedResult
	params := map[string]any{"cwd": cand.Path, "label": label, "focus": true}
	if err := d.send(ctx, &created, "workspace.create", params, "workspace", "create", "--cwd", cand.Path, "--label", label, "--focus"); err != nil {
		return source.FocusResult{}, fmt.Errorf("herdr workspace create: %w", err)
	}
	if created.Workspace.WorkspaceID == "" || created.Tab.TabID == "" || created.RootPane.PaneID == "" {
		return source.FocusResult{}, errors.New("herdr workspace create: incomplete response")
	}
	return source.FocusResult{
		WorkspaceID: created.Workspace.WorkspaceID,
		Action:      source.HerdrActionCreated,
		RootTabID:   created.Tab.TabID,
		RootPaneID:  created.RootPane.PaneID,
	}, nil
}

// CreateTab creates a new tab in workspaceID via
// `herdr tab create --workspace <id> --cwd <cwd> --label <label> [--focus|--no-focus]`,
// returning the new tab and its root pane. focus true passes --focus (the new
// tab steals keyboard focus); false passes --no-focus (the currently focused
// tab keeps focus). Focus is set at creation time rather than via a post-hoc
// `tab focus` call so the final focus state is deterministic regardless of
// later pane operations.
func (d *Driver) CreateTab(ctx context.Context, workspaceID, cwd, label string, focus bool) (source.Tab, source.Pane, error) {
	if workspaceID == "" {
		return source.Tab{}, source.Pane{}, errors.New("herdr tab create: empty workspace id")
	}
	args := []string{"tab", "create", "--workspace", workspaceID}
	params := map[string]any{"workspace_id": workspaceID, "focus": focus}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
		params["cwd"] = cwd
	}
	if label != "" {
		args = append(args, "--label", label)
		params["label"] = label
	}
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	var created tabCreatedResult
	if err := d.send(ctx, &created, "tab.create", params, args...); err != nil {
		return source.Tab{}, source.Pane{}, fmt.Errorf("herdr tab create: %w", err)
	}
	if created.Tab.TabID == "" || created.RootPane.PaneID == "" {
		return source.Tab{}, source.Pane{}, errors.New("herdr tab create: incomplete response")
	}
	if !validPaneID.MatchString(created.RootPane.PaneID) {
		return source.Tab{}, source.Pane{}, fmt.Errorf("herdr tab create: invalid pane id %q", created.RootPane.PaneID)
	}
	return source.Tab{
		ID:          created.Tab.TabID,
		WorkspaceID: created.Tab.WorkspaceID,
		Label:       created.Tab.Label,
		Focused:     created.Tab.Focused,
		Number:      created.Tab.Number,
		PaneCount:   created.Tab.PaneCount,
	}, rawPaneToPane(created.RootPane), nil
}

// RenameTab renames tabID via `herdr tab rename <tab_id> <label>`.
func (d *Driver) RenameTab(ctx context.Context, tabID, label string) error {
	if tabID == "" {
		return errors.New("herdr tab rename: empty tab id")
	}
	if err := d.send(ctx, nil, "tab.rename", map[string]any{"tab_id": tabID, "label": label}, "tab", "rename", tabID, label); err != nil {
		return fmt.Errorf("herdr tab rename %s: %w", tabID, err)
	}
	return nil
}

// RenamePane renames or clears paneID's persistent Herdr label. A nil label
// is a caller error; a non-nil empty label clears it (--clear on the CLI).
// The CLI takes a non-empty label as one argv element, preserving spaces and
// shell metacharacters, but cannot take one starting with '-'.
func (d *Driver) RenamePane(ctx context.Context, paneID string, label *string) error {
	if paneID == "" {
		return errors.New("herdr pane rename: empty pane id")
	}
	if label == nil {
		return errors.New("herdr pane rename: nil label")
	}
	requested := *label
	params := map[string]any{"pane_id": paneID}
	if requested != "" {
		params["label"] = requested
	}
	if ok, err := d.viaSocket(ctx, "pane.rename", params, nil); ok {
		if err != nil {
			return fmt.Errorf("herdr pane rename %s %q: %w", paneID, requested, err)
		}
		return nil
	}
	if requested == "" {
		requested = "--clear"
	} else if strings.HasPrefix(requested, "-") {
		return fmt.Errorf("herdr pane rename %s %q: cannot send a label starting with '-' as a positional label", paneID, requested)
	}
	if _, err := d.run.Run(ctx, d.binary, "pane", "rename", paneID, requested); err != nil {
		return fmt.Errorf("herdr pane rename %s %q: %w", paneID, requested, err)
	}
	return nil
}

// SplitPane splits paneID via
// `herdr pane split <pane_id> --direction <direction> --ratio <ratio> --cwd <cwd> [--focus|--no-focus]`,
// returning the newly created pane. ratio is the fraction of the ORIGINAL
// pane (paneID) retained after the split; the new pane gets 1-ratio. focus
// true passes --focus (the NEW pane steals keyboard focus); false passes
// --no-focus (the original pane keeps focus). Herdr's `pane focus` command
// only accepts --direction (left/right/up/down), NOT a positional pane id, so
// pane-level focus MUST be controlled here at split creation time — there is
// no valid post-hoc "focus pane by id" command.
func (d *Driver) SplitPane(ctx context.Context, paneID, direction string, ratio float64, cwd string, focus bool) (source.Pane, error) {
	if paneID == "" {
		return source.Pane{}, errors.New("herdr pane split: empty pane id")
	}
	args := []string{"pane", "split", paneID, "--direction", direction, "--ratio", strconv.FormatFloat(ratio, 'f', -1, 64)}
	// right_click is the CLI's own default; it is sent for byte parity.
	params := map[string]any{"target_pane_id": paneID, "direction": direction, "ratio": ratio, "focus": focus, "right_click": "herdr"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
		params["cwd"] = cwd
	}
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	var split paneInfoResult
	if err := d.send(ctx, &split, "pane.split", params, args...); err != nil {
		return source.Pane{}, fmt.Errorf("herdr pane split %s: %w", paneID, err)
	}
	if split.Pane.PaneID == "" {
		return source.Pane{}, errors.New("herdr pane split: incomplete response")
	}
	if !validPaneID.MatchString(split.Pane.PaneID) {
		return source.Pane{}, fmt.Errorf("herdr pane split: invalid pane id %q", split.Pane.PaneID)
	}
	return rawPaneToPane(split.Pane), nil
}

// RunPane types command into paneID and submits it with Enter
// (`pane.send_input`, which is what `herdr pane run` sends). An empty command
// is a no-op so a plain-shell leaf never shells out.
func (d *Driver) RunPane(ctx context.Context, paneID, command string) error {
	if paneID == "" {
		return errors.New("herdr pane run: empty pane id")
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	params := map[string]any{"pane_id": paneID, "text": command, "keys": []string{"Enter"}}
	if err := d.send(ctx, nil, "pane.send_input", params, "pane", "run", paneID, command); err != nil {
		return fmt.Errorf("herdr pane run %s %q: %w", paneID, command, err)
	}
	return nil
}

// FocusTab focuses tabID via `herdr tab focus <id>`. This is a valid Herdr
// command and is kept as a fallback; however, the primary focus mechanism is
// the creation-time --focus/--no-focus flag on CreateTab/SplitPane, so this
// is rarely needed in the template-apply path. (There is intentionally no
// FocusPane method here: `herdr pane focus` only accepts --direction, not a
// positional pane id, so focusing an arbitrary pane by id after creation is
// not possible — it must be set at split time via SplitPane's focus flag.)
func (d *Driver) FocusTab(ctx context.Context, tabID string) error {
	if tabID == "" {
		return errors.New("herdr tab focus: empty tab id")
	}
	if err := d.send(ctx, nil, "tab.focus", map[string]any{"tab_id": tabID}, "tab", "focus", tabID); err != nil {
		return fmt.Errorf("herdr tab focus %s: %w", tabID, err)
	}
	return nil
}

// ClosePane closes one open Herdr pane by id.
func (d *Driver) ClosePane(ctx context.Context, paneID string) error {
	if paneID == "" {
		return errors.New("herdr pane close: empty pane id")
	}
	if err := d.send(ctx, nil, "pane.close", map[string]any{"pane_id": paneID}, "pane", "close", paneID); err != nil {
		return fmt.Errorf("herdr pane close %s: %w", paneID, closeError(err))
	}
	return nil
}

// CloseTab closes one open Herdr tab by id.
func (d *Driver) CloseTab(ctx context.Context, tabID string) error {
	if tabID == "" {
		return errors.New("herdr tab close: empty tab id")
	}
	if err := d.send(ctx, nil, "tab.close", map[string]any{"tab_id": tabID}, "tab", "close", tabID); err != nil {
		return fmt.Errorf("herdr tab close %s: %w", tabID, closeError(err))
	}
	return nil
}

// CloseWorkspace closes only the selected workspace, never its linked group.
func (d *Driver) CloseWorkspace(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return errors.New("herdr workspace close: empty workspace id")
	}
	if err := d.send(ctx, nil, "workspace.close", map[string]any{"workspace_id": workspaceID}, "workspace", "close", workspaceID); err != nil {
		return fmt.Errorf("herdr workspace close %s: %w", workspaceID, closeError(err))
	}
	return nil
}

// RenameWorkspace relabels workspaceID (`workspace.rename`).
func (d *Driver) RenameWorkspace(ctx context.Context, workspaceID, label string) error {
	if workspaceID == "" {
		return errors.New("herdr workspace rename: empty workspace id")
	}
	params := map[string]any{"workspace_id": workspaceID, "label": label}
	if err := d.send(ctx, nil, "workspace.rename", params, "workspace", "rename", workspaceID, label); err != nil {
		return fmt.Errorf("herdr workspace rename %s: %w", workspaceID, err)
	}
	return nil
}

// worktreeCreatedResult is the result of `worktree.create` (`herdr worktree
// create`): the new worktree and the focused workspace Herdr opened on it.
//
//	{"type":"worktree_created","workspace":{...,"worktree":{"repo_name":...}},
//	  "tab":{...},"root_pane":{...},"worktree":{"path":...,"branch":...}}
type worktreeCreatedResult struct {
	Workspace struct {
		rawWorkspace
		Worktree *struct {
			RepoName string `json:"repo_name"`
		} `json:"worktree"`
	} `json:"workspace"`
	Tab      rawTab  `json:"tab"`
	RootPane rawPane `json:"root_pane"`
	Worktree struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	} `json:"worktree"`
}

// CreatedWorktree is a Git worktree Herdr created and the focused workspace
// it opened on it. RepoName is empty when Herdr did not report one.
type CreatedWorktree struct {
	WorkspaceID    string
	WorkspaceLabel string
	RootTabID      string
	RootPaneID     string
	Path           string
	Branch         string
	RepoName       string
}

// CreateWorktree creates a Git worktree of the repository holding repoPath on
// a new branch, and a focused workspace on it, in one `worktree.create`
// request: Herdr picks the worktree's location.
func (d *Driver) CreateWorktree(ctx context.Context, repoPath, branch string) (CreatedWorktree, error) {
	if repoPath == "" || branch == "" {
		return CreatedWorktree{}, errors.New("herdr worktree create: empty repository path or branch")
	}
	var created worktreeCreatedResult
	params := map[string]any{"cwd": repoPath, "branch": branch, "focus": true}
	if err := d.send(ctx, &created, "worktree.create", params, "worktree", "create", "--cwd", repoPath, "--branch", branch, "--focus"); err != nil {
		return CreatedWorktree{}, fmt.Errorf("herdr worktree create: %w", err)
	}
	if created.Workspace.WorkspaceID == "" || created.Tab.TabID == "" || created.RootPane.PaneID == "" || created.Worktree.Path == "" {
		return CreatedWorktree{}, errors.New("herdr worktree create: incomplete response")
	}
	wt := CreatedWorktree{
		WorkspaceID:    created.Workspace.WorkspaceID,
		WorkspaceLabel: created.Workspace.Label,
		RootTabID:      created.Tab.TabID,
		RootPaneID:     created.RootPane.PaneID,
		Path:           created.Worktree.Path,
		Branch:         created.Worktree.Branch,
	}
	if wt.Branch == "" {
		wt.Branch = branch
	}
	if created.Workspace.Worktree != nil {
		wt.RepoName = created.Workspace.Worktree.RepoName
	}
	return wt, nil
}

// OpenPluginPane opens a plugin pane entrypoint, focused, in placement
// (`plugin.pane.open`; "popup" for the picker).
func (d *Driver) OpenPluginPane(ctx context.Context, pluginID, entrypoint, placement string) error {
	params := map[string]any{"plugin_id": pluginID, "entrypoint": entrypoint, "placement": placement, "focus": true}
	if err := d.send(ctx, nil, "plugin.pane.open", params, "plugin", "pane", "open", "--plugin", pluginID, "--entrypoint", entrypoint, "--placement", placement); err != nil {
		return fmt.Errorf("herdr plugin pane open %s/%s: %w", pluginID, entrypoint, err)
	}
	return nil
}

// closeError includes Herdr's stderr from exec.ExitError, including the
// workspace_group_close_required code, while preserving the original error.
// A socket rejection already carries its code in its message.
func closeError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(exit.Stderr)), err)
	}
	return err
}

// rawPaneToPane converts the JSON envelope's rawPane into the exported
// source.Pane. Kept unexported and local because it is only needed by the
// pane-producing methods of this driver; the templates/command layers consume
// source.Pane directly.
func rawPaneToPane(p rawPane) source.Pane {
	return source.Pane{
		ID:            p.PaneID,
		Label:         p.Label,
		WorkspaceID:   p.WorkspaceID,
		TabID:         p.TabID,
		CWD:           p.CWD,
		ForegroundCWD: p.ForegroundCWD,
		Focused:       p.Focused,
		Agent:         p.Agent,
		AgentStatus:   p.AgentStatus,
		TerminalTitle: p.TerminalTitle,
	}
}

// ReadPane returns the captured terminal buffer of a pane: `pane.read` over
// the socket answers it as result.read.text, while the CLI (`herdr pane read
// <pane_id> --lines <lines> --format ansi`) prints the same text as stdout
// instead of an envelope. The ansi format preserves the pane's real ANSI
// color codes (unlike `--format text`, which strips them). Preserving color is the
// point: the "active_pane" preview section exists to show the user what the
// pane actually looks like right now, colors included. This is safe because
// internal/tui/model.go's truncateToWidth is ANSI-aware (it delegates to
// charmbracelet/x/ansi.Truncate) and never cuts mid-escape sequence, so the
// colored buffer truncates cleanly at narrow widths. lines <= 0 omits the
// flag so the daemon applies its own default cap.
func (d *Driver) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	if paneID == "" {
		return "", errors.New("herdr pane read: empty pane id")
	}
	args := []string{"pane", "read", paneID}
	params := map[string]any{"pane_id": paneID, "source": "recent", "format": "ansi", "strip_ansi": true}
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
		params["lines"] = lines
	}
	args = append(args, "--format", "ansi")
	var read struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	if ok, err := d.viaSocket(ctx, "pane.read", params, &read); ok {
		if err != nil {
			return "", fmt.Errorf("herdr pane read %s: %w", paneID, err)
		}
		return read.Read.Text, nil
	}
	out, err := d.run.Run(ctx, d.binary, args...)
	if err != nil {
		return "", fmt.Errorf("herdr pane read %s: %w", paneID, err)
	}
	return string(out), nil
}
