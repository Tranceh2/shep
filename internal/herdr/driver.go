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
//	              cwd,foreground_cwd,focused,...}}}
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
	"strconv"
	"strings"

	"github.com/tranceh2/shep/internal/source"
)

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
	// Number, TabCount, PaneCount, and AgentStatus are returned by
	// `herdr workspace list` but were previously discarded. They are now
	// deserialised so the preview layer can render richer workspace summaries
	// without extra round trips.
	Number      int    `json:"number"`
	TabCount    int    `json:"tab_count"`
	PaneCount   int    `json:"pane_count"`
	AgentStatus string `json:"agent_status"`
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
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Focused       bool   `json:"focused"`
}

type paneCurrentEnvelope struct {
	ID     string `json:"id"`
	Result struct {
		Pane rawPane `json:"pane"`
	} `json:"result"`
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

// FocusOrCreate implements HI-3: focus an existing workspace whose pane cwd /
// foreground_cwd normalises to the candidate's path, else create a new focused
// workspace. The candidate carries its own NormalizedPath (filled by the
// resolver); when empty we normalise on the fly against cand.Path.
func (d *Driver) FocusOrCreate(ctx context.Context, cand source.Candidate) (source.FocusResult, error) {
	needle := cand.NormalizedPath
	if needle == "" {
		n, err := normalizePath(cand.Path)
		if err != nil {
			return source.FocusResult{}, fmt.Errorf("normalize candidate path: %w", err)
		}
		needle = n
	}

	_, panes, err := d.loadState(ctx)
	if err != nil {
		return source.FocusResult{}, err
	}

	if id := matchWorkspaceByCWD(panes, needle); id != "" {
		if _, err := d.run.Run(ctx, d.binary, "workspace", "focus", id); err != nil {
			return source.FocusResult{}, fmt.Errorf("herdr workspace focus %s: %w", id, err)
		}
		return source.FocusResult{WorkspaceID: id, Action: source.HerdrActionFocused}, nil
	}

	label := cand.Label
	if label == "" {
		label = filepath.Base(needle)
	}
	if _, err := d.run.Run(ctx, d.binary, "workspace", "create",
		"--cwd", cand.Path, "--label", label, "--focus"); err != nil {
		return source.FocusResult{}, fmt.Errorf("herdr workspace create: %w", err)
	}

	// The create command's stdout shape is not part of Herdr's documented
	// contract; discover the freshly focused workspace via `herdr pane
	// current`, which returns the now-active pane with its workspace_id.
	id, err := d.currentWorkspaceID(ctx)
	if err != nil {
		return source.FocusResult{}, fmt.Errorf("herdr discover new workspace: %w", err)
	}
	return source.FocusResult{WorkspaceID: id, Action: source.HerdrActionCreated}, nil
}

// matchWorkspaceByCWD returns the workspace_id of the first pane whose cwd or
// foreground_cwd normalises to needle. Returns "" when no pane matches.
func matchWorkspaceByCWD(panes []rawPane, needle string) string {
	for _, p := range panes {
		for _, candidate := range []string{p.CWD, p.ForegroundCWD} {
			if candidate == "" {
				continue
			}
			n, err := normalizePath(candidate)
			if err != nil {
				continue
			}
			if n == needle {
				return p.WorkspaceID
			}
		}
	}
	return ""
}

// currentWorkspaceID returns the workspace_id of the currently focused pane.
func (d *Driver) currentWorkspaceID(ctx context.Context) (string, error) {
	out, err := d.run.Run(ctx, d.binary, "pane", "current")
	if err != nil {
		return "", fmt.Errorf("herdr pane current: %w", err)
	}
	var env paneCurrentEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return "", fmt.Errorf("herdr pane current: parse: %w", err)
	}
	if env.Result.Pane.WorkspaceID == "" {
		return "", errors.New("herdr pane current: empty workspace_id")
	}
	return env.Result.Pane.WorkspaceID, nil
}

// RunStartup runs a command in the first pane of the named workspace via
// `herdr pane run` (HI-4). It is meant to fire only on freshly created
// workspaces; the pane is located through `herdr pane list --workspace`.
func (d *Driver) RunStartup(ctx context.Context, workspaceID, command string) error {
	if workspaceID == "" {
		return errors.New("herdr run startup: empty workspace id")
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	out, err := d.run.Run(ctx, d.binary, "pane", "list", "--workspace", workspaceID)
	if err != nil {
		return fmt.Errorf("herdr pane list --workspace %s: %w", workspaceID, err)
	}
	var env paneListEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("herdr pane list --workspace %s: parse: %w", workspaceID, err)
	}
	if len(env.Result.Panes) == 0 {
		return fmt.Errorf("herdr workspace %s has no panes", workspaceID)
	}
	paneID := env.Result.Panes[0].PaneID
	if _, err := d.run.Run(ctx, d.binary, "pane", "run", paneID, command); err != nil {
		return fmt.Errorf("herdr pane run %s %q: %w", paneID, command, err)
	}
	return nil
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
// the raw terminal buffer as stdout. lines <= 0 omits the flag so the daemon
// applies its own default cap.
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

// normalizePath mirrors the resolver's normalisation (expand ~, absolute,
// trim trailing separator, EvalSymlinks) without importing the resolver to
// avoid a herdr -> resolver -> source cycle. Unresolved symlinks keep the
// cleaned absolute path.
func normalizePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cleaned := strings.TrimRight(abs, string(filepath.Separator))
	if cleaned == "" {
		cleaned = string(filepath.Separator)
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved, nil
	}
	return cleaned, nil
}
