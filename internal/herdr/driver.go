// Package herdr implements the real shep <-> Herdr CLI bridge behind the
// source.HerdrDriver interface.
//
// Herdr ships a JSON envelope on every command:
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
// workspace_id to derive a representative cwd. All command execution goes
// through a small CommandRunner so tests inject a fake instead of shelling out.
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

// Driver is the real source.HerdrDriver backed by the herdr CLI. It is safe
// to construct one per command invocation; all state lives in the JSON
// envelopes returned by Herdr.
type Driver struct {
	binary   string
	run      CommandRunner
	lookPath lookPathFn
}

// Option configures a Driver at construction.
type Option func(*Driver)

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
	AgentStatus   string `json:"agent_status"`
}

type rawTab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
}

// snapshotEnvelope wraps `herdr api snapshot`. The command provides one
// coherent state generation containing the workspace, tab, and pane records
// that previously needed several independent calls.
type snapshotEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Snapshot rawSnapshot `json:"snapshot"`
	} `json:"result"`
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

// Snapshot obtains one full coherent state generation through the official
// Herdr CLI. Records without their primary identity are ignored so a partial
// daemon response cannot invalidate complete neighboring records.
func (d *Driver) Snapshot(ctx context.Context) (source.Snapshot, error) {
	out, err := d.run.Run(ctx, d.binary, "api", "snapshot")
	if err != nil {
		return source.Snapshot{}, fmt.Errorf("herdr api snapshot: %w", err)
	}
	var env snapshotEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return source.Snapshot{}, fmt.Errorf("herdr api snapshot: parse: %w", err)
	}
	return rawSnapshotToSnapshot(env.Result.Snapshot), nil
}

// ListSessions lists local Herdr sessions through the official CLI boundary.
// Only the verified top-level 0.7.4 envelope is accepted; unknown fields are
// ignored and incomplete records without a name are not actionable.
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

// workspaceCreatedEnvelope wraps `herdr workspace create`.
//
//	{"id":"cli:workspace:create","result":{"type":"workspace_created",
//	  "workspace":{...},"tab":{...},"root_pane":{...}}}
type workspaceCreatedEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Type      string       `json:"type"`
		Workspace rawWorkspace `json:"workspace"`
		Tab       rawTab       `json:"tab"`
		RootPane  rawPane      `json:"root_pane"`
	} `json:"result"`
}

// tabCreatedEnvelope wraps `herdr tab create`.
//
//	{"id":"cli:tab:create","result":{"type":"tab_created","tab":{...},
//	  "root_pane":{...}}}
type tabCreatedEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Type     string  `json:"type"`
		Tab      rawTab  `json:"tab"`
		RootPane rawPane `json:"root_pane"`
	} `json:"result"`
}

// paneInfoEnvelope wraps `herdr pane split` (and other pane_info results).
//
//	{"id":"cli:pane:split","result":{"type":"pane_info","pane":{...}}}
type paneInfoEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Type string  `json:"type"`
		Pane rawPane `json:"pane"`
	} `json:"result"`
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
// The candidate carries its own NormalizedPath (filled by the resolver); when
// empty we normalise on the fly against cand.Path so the create label fallback
// (filepath.Base of a clean path) is stable. A freshly created workspace's
// root tab id/pane id are returned so the caller can apply a template.
func (d *Driver) FocusOrCreate(ctx context.Context, cand source.Candidate) (source.FocusResult, error) {
	if cand.Source == config.SourceHerdr {
		id := cand.Meta["workspace_id"]
		if _, err := d.run.Run(ctx, d.binary, "workspace", "focus", id); err != nil {
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
	label := cand.Label
	if label == "" {
		label = filepath.Base(needle)
	}
	out, err := d.run.Run(ctx, d.binary, "workspace", "create",
		"--cwd", cand.Path, "--label", label, "--focus")
	if err != nil {
		return source.FocusResult{}, fmt.Errorf("herdr workspace create: %w", err)
	}
	var env workspaceCreatedEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return source.FocusResult{}, fmt.Errorf("herdr workspace create: parse: %w", err)
	}
	if env.Result.Workspace.WorkspaceID == "" || env.Result.Tab.TabID == "" || env.Result.RootPane.PaneID == "" {
		return source.FocusResult{}, errors.New("herdr workspace create: incomplete response")
	}
	return source.FocusResult{
		WorkspaceID: env.Result.Workspace.WorkspaceID,
		Action:      source.HerdrActionCreated,
		RootTabID:   env.Result.Tab.TabID,
		RootPaneID:  env.Result.RootPane.PaneID,
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
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	out, err := d.run.Run(ctx, d.binary, args...)
	if err != nil {
		return source.Tab{}, source.Pane{}, fmt.Errorf("herdr tab create: %w", err)
	}
	var env tabCreatedEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return source.Tab{}, source.Pane{}, fmt.Errorf("herdr tab create: parse: %w", err)
	}
	if env.Result.Tab.TabID == "" || env.Result.RootPane.PaneID == "" {
		return source.Tab{}, source.Pane{}, errors.New("herdr tab create: incomplete response")
	}
	if !validPaneID.MatchString(env.Result.RootPane.PaneID) {
		return source.Tab{}, source.Pane{}, fmt.Errorf("herdr tab create: invalid pane id %q", env.Result.RootPane.PaneID)
	}
	return source.Tab{
		ID:          env.Result.Tab.TabID,
		WorkspaceID: env.Result.Tab.WorkspaceID,
		Label:       env.Result.Tab.Label,
		Focused:     env.Result.Tab.Focused,
		Number:      env.Result.Tab.Number,
		PaneCount:   env.Result.Tab.PaneCount,
	}, rawPaneToPane(env.Result.RootPane), nil
}

// RenameTab renames tabID via `herdr tab rename <tab_id> <label>`.
func (d *Driver) RenameTab(ctx context.Context, tabID, label string) error {
	if tabID == "" {
		return errors.New("herdr tab rename: empty tab id")
	}
	if _, err := d.run.Run(ctx, d.binary, "tab", "rename", tabID, label); err != nil {
		return fmt.Errorf("herdr tab rename %s: %w", tabID, err)
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
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	out, err := d.run.Run(ctx, d.binary, args...)
	if err != nil {
		return source.Pane{}, fmt.Errorf("herdr pane split %s: %w", paneID, err)
	}
	var env paneInfoEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return source.Pane{}, fmt.Errorf("herdr pane split %s: parse: %w", paneID, err)
	}
	if env.Result.Pane.PaneID == "" {
		return source.Pane{}, errors.New("herdr pane split: incomplete response")
	}
	if !validPaneID.MatchString(env.Result.Pane.PaneID) {
		return source.Pane{}, fmt.Errorf("herdr pane split: invalid pane id %q", env.Result.Pane.PaneID)
	}
	return rawPaneToPane(env.Result.Pane), nil
}

// RunPane runs command in paneID via `herdr pane run <pane_id> <command>`.
// An empty command is a no-op so a plain-shell leaf never shells out.
func (d *Driver) RunPane(ctx context.Context, paneID, command string) error {
	if paneID == "" {
		return errors.New("herdr pane run: empty pane id")
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if _, err := d.run.Run(ctx, d.binary, "pane", "run", paneID, command); err != nil {
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
	if _, err := d.run.Run(ctx, d.binary, "tab", "focus", tabID); err != nil {
		return fmt.Errorf("herdr tab focus %s: %w", tabID, err)
	}
	return nil
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
		AgentStatus:   p.AgentStatus,
	}
}

// ReadPane returns the captured terminal buffer of a pane via
// `herdr pane read <pane_id> --lines <lines> --format ansi`. Unlike the list
// methods, ReadPane does not parse a JSON envelope: `--format ansi` returns
// the raw terminal buffer as stdout, preserving the pane's real ANSI color
// codes (unlike `--format text`, which strips them). Preserving color is the
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
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
	}
	args = append(args, "--format", "ansi")
	out, err := d.run.Run(ctx, d.binary, args...)
	if err != nil {
		return "", fmt.Errorf("herdr pane read %s: %w", paneID, err)
	}
	return string(out), nil
}
