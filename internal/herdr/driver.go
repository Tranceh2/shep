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
// SplitPane and CurrentPane reject any pane_id outside this set as
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

type workspaceListEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Type       string         `json:"type"`
		Workspaces []rawWorkspace `json:"workspaces"`
	} `json:"result"`
}

type rawWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	ActiveTabID string `json:"active_tab_id"`
	Focused     bool   `json:"focused"`
}

type paneListEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Panes []rawPane `json:"panes"`
	} `json:"result"`
}

type rawPane struct {
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Focused       bool   `json:"focused"`
}

// tabListEnvelope wraps `herdr tab list --workspace <id>`.
//
//	{"id":"cli:tab:list","result":{"type":"tab_list","tabs":[
//	  {tab_id,workspace_id,label,focused,number,pane_count}]}}
type tabListEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Type string   `json:"type"`
		Tabs []rawTab `json:"tabs"`
	} `json:"result"`
}

type rawTab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
}

// agentListEnvelope wraps `herdr agent list`.
//
//	{"id":"cli:agent:list","result":{"agents":[
//	  {agent_id,label,agent_status}]}}
type agentListEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Agents []rawAgent `json:"agents"`
	} `json:"result"`
}

type rawAgent struct {
	AgentID     string `json:"agent_id"`
	Label       string `json:"label"`
	AgentStatus string `json:"agent_status"`
}

// ListWorkspaces enumerates Herdr workspaces and derives each workspace's CWD
// from its panes (preferring the focused pane's cwd, falling back to the first
// pane's foreground_cwd). A workspace with no panes keeps an empty CWD and is
// dropped by the herdr source provider.
func (d *Driver) ListWorkspaces(ctx context.Context) ([]source.Workspace, error) {
	workspaces, panes, err := d.loadState(ctx)
	if err != nil {
		return nil, err
	}
	return joinWorkspaces(workspaces, panes), nil
}

// loadState fetches workspaces and panes in two round trips. Either envelope
// failing to parse is an error so callers can fall back to a path-print
// (HI-6); we never silently return partial state.
func (d *Driver) loadState(ctx context.Context) ([]rawWorkspace, []rawPane, error) {
	wsOut, err := d.run.Run(ctx, d.binary, "workspace", "list")
	if err != nil {
		return nil, nil, fmt.Errorf("herdr workspace list: %w", err)
	}
	var wsEnv workspaceListEnvelope
	if err := json.Unmarshal(wsOut, &wsEnv); err != nil {
		return nil, nil, fmt.Errorf("herdr workspace list: parse: %w", err)
	}

	paneOut, err := d.run.Run(ctx, d.binary, "pane", "list")
	if err != nil {
		return nil, nil, fmt.Errorf("herdr pane list: %w", err)
	}
	var paneEnv paneListEnvelope
	if err := json.Unmarshal(paneOut, &paneEnv); err != nil {
		return nil, nil, fmt.Errorf("herdr pane list: parse: %w", err)
	}
	return wsEnv.Result.Workspaces, paneEnv.Result.Panes, nil
}

// joinWorkspaces attaches a representative CWD to each workspace by walking
// the pane list. Focused pane wins; otherwise the first pane's foreground_cwd
// (then cwd) is used.
func joinWorkspaces(workspaces []rawWorkspace, panes []rawPane) []source.Workspace {
	byWorkspace := make(map[string][]rawPane, len(workspaces))
	for _, p := range panes {
		if p.WorkspaceID == "" {
			continue
		}
		byWorkspace[p.WorkspaceID] = append(byWorkspace[p.WorkspaceID], p)
	}
	out := make([]source.Workspace, 0, len(workspaces))
	for _, w := range workspaces {
		if w.WorkspaceID == "" {
			continue
		}
		cwd := representativeCWD(byWorkspace[w.WorkspaceID])
		out = append(out, source.Workspace{
			ID:    w.WorkspaceID,
			Label: w.Label,
			CWD:   cwd,
		})
	}
	return out
}

// representativeCWD picks the cwd to represent a workspace from its panes.
// Focused pane > first pane's foreground_cwd > first pane's cwd > "".
func representativeCWD(panes []rawPane) string {
	for _, p := range panes {
		if p.Focused {
			return firstNonEmpty(p.ForegroundCWD, p.CWD)
		}
	}
	if len(panes) > 0 {
		return firstNonEmpty(panes[0].ForegroundCWD, panes[0].CWD)
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

// paneCurrentEnvelope wraps `herdr pane current`, documented in the package
// doc comment:
//
//	{"id":"cli:pane:current","result":{"pane":{pane_id,workspace_id,tab_id,
//	  cwd,foreground_cwd,focused,...}}}
type paneCurrentEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Pane rawPane `json:"pane"`
	} `json:"result"`
}

// FocusOrCreate implements HI-3: focus an existing workspace whose pane cwd /
// foreground_cwd normalises to the candidate's path, else create a new focused
// workspace. The candidate carries its own NormalizedPath (filled by the
// resolver); when empty we normalise on the fly against cand.Path. A freshly
// created workspace's root tab id/pane id are returned so the caller can
// apply a template.
func (d *Driver) FocusOrCreate(ctx context.Context, cand source.Candidate) (source.FocusResult, error) {
	needle := cand.NormalizedPath
	if needle == "" {
		n, err := pathutil.Normalize(cand.Path)
		if err != nil {
			return source.FocusResult{}, fmt.Errorf("normalize candidate path: %w", err)
		}
		needle = n
	}

	workspaces, panes, err := d.loadState(ctx)
	if err != nil {
		return source.FocusResult{}, err
	}

	// A [[workspaces]]-config candidate's identity is its label, not merely
	// "any workspace at this directory": sibling config entries may share a
	// path (e.g. a allsafe-launcher entry alongside a plain-shell entry at
	// the same repo root). Matching by label+cwd together prevents focusing
	// an unrelated same-path workspace; if no such match exists we go
	// straight to create instead of falling back to a CWD-only match, which
	// would reintroduce that exact mismatch. Any other source (zoxide,
	// projects, herdr, or a workspaces candidate with an empty label) keeps
	// the original CWD-only match unchanged.
	if cand.Source == config.SourceWorkspaces && cand.Label != "" {
		if id := matchWorkspaceByLabelAndCWD(workspaces, panes, cand.Label, needle); id != "" {
			if _, err := d.run.Run(ctx, d.binary, "workspace", "focus", id); err != nil {
				return source.FocusResult{}, fmt.Errorf("herdr workspace focus %s: %w", id, err)
			}
			return source.FocusResult{WorkspaceID: id, Action: source.HerdrActionFocused}, nil
		}
	} else if id := matchWorkspaceByCWD(panes, needle); id != "" {
		if _, err := d.run.Run(ctx, d.binary, "workspace", "focus", id); err != nil {
			return source.FocusResult{}, fmt.Errorf("herdr workspace focus %s: %w", id, err)
		}
		return source.FocusResult{WorkspaceID: id, Action: source.HerdrActionFocused}, nil
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
		}, source.Pane{
			ID:            env.Result.RootPane.PaneID,
			WorkspaceID:   env.Result.RootPane.WorkspaceID,
			TabID:         env.Result.RootPane.TabID,
			CWD:           env.Result.RootPane.CWD,
			ForegroundCWD: env.Result.RootPane.ForegroundCWD,
			Focused:       env.Result.RootPane.Focused,
		}, nil
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
	p := env.Result.Pane
	return source.Pane{
		ID:            p.PaneID,
		WorkspaceID:   p.WorkspaceID,
		TabID:         p.TabID,
		CWD:           p.CWD,
		ForegroundCWD: p.ForegroundCWD,
		Focused:       p.Focused,
	}, nil
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

// CurrentPane returns the pane that currently has keyboard focus inside Herdr,
// via `herdr pane current`. Newer Herdr builds answer with a single-pane
// envelope (pane_id/workspace_id/tab_id/cwd/focused). Older builds that do not
// recognise the `pane current` subcommand exit non-zero; CurrentPane then
// falls back to the loadState pair (`workspace list` + `pane list`) and returns
// the first pane whose Focused flag is set — the same filter pattern
// internal/preview/renderer.go's activePaneID uses. When no pane is focused
// (shep is not running inside a Herdr pane), CurrentPane returns
// source.ErrNoFocusedPane so callers can disable the tab/pane launch targets.
//
// `pane current` deliberately carries no --format flag, matching every other
// JSON-emitting herdr command in this driver (`workspace list`, `pane list`,
// `tab list`, `agent list`): JSON is the default envelope, and adding a
// format flag here would diverge from the established convention without
// changing the response shape.
func (d *Driver) CurrentPane(ctx context.Context) (source.Pane, error) {
	out, err := d.run.Run(ctx, d.binary, "pane", "current")
	if err == nil {
		var env paneCurrentEnvelope
		if jErr := json.Unmarshal(out, &env); jErr != nil {
			return source.Pane{}, fmt.Errorf("herdr pane current: parse: %w", jErr)
		}
		if env.Result.Pane.PaneID == "" {
			return source.Pane{}, fmt.Errorf("herdr pane current: incomplete response")
		}
		if !validPaneID.MatchString(env.Result.Pane.PaneID) {
			return source.Pane{}, fmt.Errorf("herdr pane current: invalid pane id %q", env.Result.Pane.PaneID)
		}
		return rawPaneToPane(env.Result.Pane), nil
	}

	// Fallback: older Herdr builds reject `pane current` as an unknown
	// subcommand (non-zero exit). Re-derive the focused pane from the full
	// `pane list`, mirroring activePaneID in internal/preview/renderer.go.
	_, panes, lerr := d.loadState(ctx)
	if lerr != nil {
		// Surface the original `pane current` failure so callers see why the
		// fallback was attempted, not just the secondary list error.
		return source.Pane{}, fmt.Errorf("herdr pane current: %w", err)
	}
	for _, p := range panes {
		if p.Focused {
			return rawPaneToPane(p), nil
		}
	}
	return source.Pane{}, source.ErrNoFocusedPane
}

// rawPaneToPane converts the JSON envelope's rawPane into the exported
// source.Pane. Kept unexported and local because it is only needed by the
// pane-producing methods of this driver; the templates/command layers consume
// source.Pane directly.
func rawPaneToPane(p rawPane) source.Pane {
	return source.Pane{
		ID:            p.PaneID,
		WorkspaceID:   p.WorkspaceID,
		TabID:         p.TabID,
		CWD:           p.CWD,
		ForegroundCWD: p.ForegroundCWD,
		Focused:       p.Focused,
	}
}

// paneMatchesCWD reports whether pane p's CWD or ForegroundCWD normalizes
// to the same filesystem entry as needle (the candidate's normalized path).
// Returns false on any normalize error or stat mismatch — duplicate of the
// inner loop previously inlined in matchWorkspaceByCWD and
// matchWorkspaceByLabelAndCWD, extracted to keep case-fold matching in one
// place.
func paneMatchesCWD(p rawPane, needle string) bool {
	for _, candidate := range []string{p.CWD, p.ForegroundCWD} {
		if candidate == "" {
			continue
		}
		n, err := pathutil.Normalize(candidate)
		if err != nil {
			continue
		}
		if pathutil.SameDir(n, needle) {
			return true
		}
	}
	return false
}

// matchWorkspaceByCWD returns the workspace_id of the first pane whose cwd or
// foreground_cwd refers to the same directory as needle — using
// pathutil.SameDir (filesystem identity via device+inode), not a string
// comparison, so a differently-cased duplicate on a case-insensitive
// filesystem (macOS APFS default, Windows) still matches. Returns "" when no
// pane matches.
func matchWorkspaceByCWD(panes []rawPane, needle string) string {
	for _, p := range panes {
		if paneMatchesCWD(p, needle) {
			return p.WorkspaceID
		}
	}
	return ""
}

// matchWorkspaceByLabelAndCWD returns the workspace_id of a workspace whose
// Label equals label (exact string match — label identifies which
// [[workspaces]] config entry a candidate belongs to, so two entries at the
// SAME directory but with different explicit names must stay distinct
// identities) AND at least one of its panes' cwd/foreground_cwd refers to
// the same directory as needle (pathutil.SameDir — filesystem identity via
// device+inode, so a differently-cased duplicate on a case-insensitive
// filesystem still matches). Both conditions must hold on the SAME
// workspace; the cwd check alone also guards against Herdr ever returning
// two workspaces with the same label at genuinely different paths. Returns
// "" when no workspace satisfies both conditions.
func matchWorkspaceByLabelAndCWD(workspaces []rawWorkspace, panes []rawPane, label, needle string) string {
	if label == "" {
		return ""
	}
	labeled := make(map[string]bool, len(workspaces))
	for _, w := range workspaces {
		if w.Label == label {
			labeled[w.WorkspaceID] = true
		}
	}
	if len(labeled) == 0 {
		return ""
	}
	for _, p := range panes {
		if !labeled[p.WorkspaceID] {
			continue
		}
		if paneMatchesCWD(p, needle) {
			return p.WorkspaceID
		}
	}
	return ""
}

// ListTabs enumerates the tabs of the named workspace via
// `herdr tab list --workspace <id>`. An empty tabs array is a normal
// nil-slice result, not an error.
func (d *Driver) ListTabs(ctx context.Context, workspaceID string) ([]source.Tab, error) {
	if workspaceID == "" {
		return nil, errors.New("herdr tab list: empty workspace id")
	}
	out, err := d.run.Run(ctx, d.binary, "tab", "list", "--workspace", workspaceID)
	if err != nil {
		return nil, fmt.Errorf("herdr tab list --workspace %s: %w", workspaceID, err)
	}
	var env tabListEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("herdr tab list --workspace %s: parse: %w", workspaceID, err)
	}
	tabs := make([]source.Tab, 0, len(env.Result.Tabs))
	for _, t := range env.Result.Tabs {
		if t.TabID == "" {
			continue
		}
		tabs = append(tabs, source.Tab{
			ID:          t.TabID,
			WorkspaceID: t.WorkspaceID,
			Label:       t.Label,
			Focused:     t.Focused,
			Number:      t.Number,
			PaneCount:   t.PaneCount,
		})
	}
	return tabs, nil
}

// ListPanes enumerates the panes of the named workspace via
// `herdr pane list --workspace <id>`.
func (d *Driver) ListPanes(ctx context.Context, workspaceID string) ([]source.Pane, error) {
	if workspaceID == "" {
		return nil, errors.New("herdr pane list: empty workspace id")
	}
	out, err := d.run.Run(ctx, d.binary, "pane", "list", "--workspace", workspaceID)
	if err != nil {
		return nil, fmt.Errorf("herdr pane list --workspace %s: %w", workspaceID, err)
	}
	var env paneListEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("herdr pane list --workspace %s: parse: %w", workspaceID, err)
	}
	panes := make([]source.Pane, 0, len(env.Result.Panes))
	for _, p := range env.Result.Panes {
		if p.PaneID == "" {
			continue
		}
		panes = append(panes, source.Pane{
			ID:            p.PaneID,
			WorkspaceID:   p.WorkspaceID,
			TabID:         p.TabID,
			CWD:           p.CWD,
			ForegroundCWD: p.ForegroundCWD,
			Focused:       p.Focused,
		})
	}
	return panes, nil
}

// ListAgents enumerates Herdr agents via `herdr agent list`. An empty agents
// array is a normal nil-slice result, not an error.
func (d *Driver) ListAgents(ctx context.Context) ([]source.Agent, error) {
	out, err := d.run.Run(ctx, d.binary, "agent", "list")
	if err != nil {
		return nil, fmt.Errorf("herdr agent list: %w", err)
	}
	var env agentListEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("herdr agent list: parse: %w", err)
	}
	agents := make([]source.Agent, 0, len(env.Result.Agents))
	for _, a := range env.Result.Agents {
		if a.AgentID == "" {
			continue
		}
		agents = append(agents, source.Agent{
			ID:     a.AgentID,
			Label:  a.Label,
			Status: a.AgentStatus,
		})
	}
	return agents, nil
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
