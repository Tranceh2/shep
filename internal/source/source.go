// Package source enumerates project candidates from a collection of
// providers and offers a small registry that gates them by config and binary
// availability.
//
// A Provider yields Candidates; a Registry decides which Providers are
// enabled for a given Config + Probes snapshot (via [config.General.SourceOrder])
// and runs them in that order. Normalisation and deduplication are the
// resolver's job (see internal/resolver); source providers only collect raw,
// labelled paths.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/workspacename"
)

// SnapshotTimeout bounds each complete Herdr state read so startup and
// activity refreshes cannot leave the selector waiting on a hung daemon.
const (
	SnapshotTimeout     = 2 * time.Second
	SessionsListTimeout = 2 * time.Second
)

// Candidate is one project discovered by a provider. Path is the raw path as
// observed; NormalizedPath is filled by the resolver (left empty here).
// Label is a short human-friendly name (usually the base directory). Source
// names the provider that produced the candidate (herdr, workspaces, zoxide,
// projects, or "path" for a direct --path/--path . invocation). Meta carries
// optional, provider-specific metadata (copied defensively on read by
// callers). Missing marks a configured workspace whose path does not exist
// on disk; the picker may still show it (clearly marked), but selecting it
// fails cleanly instead of falling back to "/", $HOME, or cwd.
type Candidate struct {
	Path           string
	NormalizedPath string
	Label          string
	Icon           string
	Source         string
	Missing        bool
	Aliases        []string
	Meta           map[string]string
}

// Clone returns a deep-enough copy of the candidate so mutating the returned
// value cannot bleed back into a provider's cached slice.
func (c Candidate) Clone() Candidate {
	out := c
	out.Aliases = append([]string(nil), c.Aliases...)
	if c.Meta != nil {
		out.Meta = make(map[string]string, len(c.Meta))
		for k, v := range c.Meta {
			out.Meta[k] = v
		}
	}
	return out
}

// SupportsCurrentWorkspaceTarget reports whether cand can be opened as a new
// tab or pane inside the Herdr workspace shep is currently running in (the
// ctrl+t / ctrl+p targets). zoxide and projects candidates always can (they
// are plain paths launched as a shell); a [[workspaces]] candidate can only
// when it carries a command and is neither a group nor a template — a
// multi-tab/multi-pane template has no business materialising inside someone
// else's workspace, and a group drills into a nested picker instead. herdr
// candidates are already-open workspaces (resume, not open-new), and any other
// source (e.g. a direct --path) lacks a command to run, so neither can target
// the current workspace. This is the single source of truth shared by the TUI
// footer hints/bindings and the command-layer disallowTarget gate.
func SupportsCurrentWorkspaceTarget(c Candidate) bool {
	switch c.Source {
	case config.SourceZoxide, config.SourceProjects:
		return true
	case config.SourceWorkspaces:
		return c.Meta["command"] != "" && c.Meta["group"] != "true" && c.Meta["template"] == ""
	default:
		return c.Meta["command"] != "" && c.Meta["group"] != "true" && c.Meta["template"] == ""
	}
}

// Provider enumerates candidates from one source family. Providers hold a
// config/probes snapshot captured at construction, so List takes only a
// context (matching the design contract).
type Provider interface {
	// Name returns the source's canonical name (see config.Source* consts).
	Name() string
	// List collects candidates. An empty result with nil error is normal.
	List(ctx context.Context) ([]Candidate, error)
}

// WorkspaceLaunchRequest is the typed creation contract. Candidate identity and
// presentation remain unchanged while WorkspaceName carries the launch label.
type WorkspaceLaunchRequest struct {
	Candidate     Candidate
	WorkspaceName workspacename.Name
}

// HerdrDriver is the contract shep keeps with the Herdr CLI. The real
// implementation lives in internal/herdr; tests inject a fake. A nil driver
// keeps the herdr provider inert so source enumeration degrades to the
// remaining providers without panicking.
type HerdrDriver interface {
	// Detect reports whether Herdr is usable (binary present). Daemon liveness
	// is discovered lazily by actual command calls; Detect is a cheap probe.
	Detect(ctx context.Context) bool
	// Snapshot returns one complete Herdr state generation through the official
	// `herdr api snapshot` command.
	Snapshot(ctx context.Context) (Snapshot, error)
	// ListSessions returns local session records through
	// `herdr session list --json`.
	ListSessions(ctx context.Context) ([]Session, error)
	// FocusOrCreate decides focus-or-create from the typed request: if
	// request.Candidate.Source == config.SourceHerdr it focuses the workspace
	// identified by request.Candidate.Meta["workspace_id"], otherwise it creates
	// a new focused workspace via `herdr workspace create --cwd --label --focus`.
	// No pane/workspace scan is performed to find a CWD or label match. The
	// returned result carries the workspace + root tab + root pane so callers can
	// apply a template against a freshly created workspace.
	FocusOrCreate(ctx context.Context, request WorkspaceLaunchRequest) (FocusResult, error)
	// ReadPane returns the captured terminal buffer of a pane, with its real
	// ANSI color codes preserved, via
	// `herdr pane read <pane_id> --lines <lines> --format ansi`. lines caps
	// the number of trailing lines returned; <= 0 means the daemon default.
	ReadPane(ctx context.Context, paneID string, lines int) (string, error)
	// CreateTab creates a new tab in workspaceID via
	// `herdr tab create --workspace <id> --cwd <cwd> --label <label> [--focus|--no-focus]`,
	// returning the new tab and its root pane. focus controls whether the new
	// tab steals keyboard focus (--focus) or leaves the current tab focused
	// (--no-focus). Focus is set at creation time for determinism.
	CreateTab(ctx context.Context, workspaceID, cwd, label string, focus bool) (Tab, Pane, error)
	// RenameTab renames tabID via `herdr tab rename <tab_id> <label>`.
	RenameTab(ctx context.Context, tabID, label string) error
	// RenamePane renames or clears paneID's persistent Herdr label. A nil label
	// is invalid; a non-nil empty label clears, and a non-empty label renames.
	// The label is passed to Herdr as one argv element.
	RenamePane(ctx context.Context, paneID string, label *string) error
	// SplitPane splits paneID via
	// `herdr pane split <pane_id> --direction <direction> --ratio <ratio> --cwd <cwd> [--focus|--no-focus]`,
	// returning the newly created pane. direction is "down" or "right";
	// ratio is the fraction of the ORIGINAL pane retained by paneID (the new
	// pane gets 1-ratio); focus true passes --focus (new pane steals focus),
	// false passes --no-focus (original pane keeps focus). Pane focus MUST be
	// controlled here: there is no valid post-hoc "focus pane by id" command
	// in Herdr (pane focus only accepts --direction).
	SplitPane(ctx context.Context, paneID, direction string, ratio float64, cwd string, focus bool) (Pane, error)
	// RunPane runs command in paneID via `herdr pane run <pane_id> <command>`.
	// An empty command is a no-op (the pane stays a plain shell). The command
	// is typed into the pane's already-running interactive shell and submitted
	// with Enter; it does NOT spawn the command as the pane's root process.
	RunPane(ctx context.Context, paneID, command string) error
	// FocusTab focuses tabID via `herdr tab focus <id>`. A valid fallback;
	// the primary focus mechanism is the creation-time flag on CreateTab.
	FocusTab(ctx context.Context, tabID string) error
}

// Workspace is a minimal, driver-supplied description of a Herdr workspace.
// CWD is derived by the driver from the workspace's panes; the Herdr JSON
// envelope does not attach a cwd directly to a workspace.
type Workspace struct {
	ID          string
	Label       string
	CWD         string
	ActiveTabID string
	Focused     bool
}

// Tab is one tab of a Herdr workspace. PaneCount is the number of panes the
// tab owns (the Herdr tab-list envelope carries it per tab).
type Tab struct {
	ID          string
	WorkspaceID string
	Label       string
	Focused     bool
	Number      int
	PaneCount   int
}

// Pane is one pane of a Herdr workspace. Label is the optional human-facing
// name supplied by Herdr; ID remains the stable machine identifier. CWD is the
// pane's working directory; ForegroundCWD is the cwd of the foreground process
// running in it (Herdr populates this only while a command is active).
// AgentStatus mirrors the pane envelope's `agent_status` field: one of "idle",
// "working", "blocked",
// "done", "unknown", or "" when the pane predates the field (older Herdr) or
// carries no agent (a plain shell pane). Empty and "unknown" are distinct:
// "" means the status is unavailable, "unknown" is Herdr explicitly
// reporting it cannot classify the pane's agent.
type Pane struct {
	ID            string
	Label         string
	WorkspaceID   string
	TabID         string
	CWD           string
	ForegroundCWD string
	Focused       bool
	Agent         string
	AgentStatus   string
	TerminalTitle string
}

// Session is the CLI-reported identity and optional metadata for one local
// Herdr session. A named session is attachable regardless of the optional
// directory or socket fields.
type Session struct {
	Name       string
	Running    bool
	Default    bool
	SessionDir string
	SocketPath string
}

// Snapshot is one coherent Herdr state generation. Its records intentionally
// retain only the fields Shep consumes; unknown Herdr fields are ignored by the
// CLI driver so newer daemon versions remain usable.
type Snapshot struct {
	Workspaces         []Workspace
	Tabs               []Tab
	Panes              []Pane
	FocusedWorkspaceID string
	FocusedTabID       string
	FocusedPaneID      string
}

var validPaneID = regexp.MustCompile(`^[A-Za-z0-9:._-]+$`)

// HerdrCandidates derives one visible workspace candidate per snapshot
// workspace. A workspace with no derivable CWD remains visible and marked
// missing instead of disappearing from the selector.
func HerdrCandidates(snapshot Snapshot) []Candidate {
	panesByWorkspace := make(map[string][]Pane, len(snapshot.Workspaces))
	for _, pane := range snapshot.Panes {
		if pane.ID == "" || pane.WorkspaceID == "" {
			continue
		}
		panesByWorkspace[pane.WorkspaceID] = append(panesByWorkspace[pane.WorkspaceID], pane)
	}

	candidates := make([]Candidate, 0, len(snapshot.Workspaces))
	seen := make(map[string]struct{}, len(snapshot.Workspaces))
	for _, workspace := range snapshot.Workspaces {
		if workspace.ID == "" {
			continue
		}
		if _, duplicate := seen[workspace.ID]; duplicate {
			continue
		}
		seen[workspace.ID] = struct{}{}
		cwd := representativeCWD(panesByWorkspace[workspace.ID])
		missing := cwd == ""
		if cwd != "" {
			if _, err := os.Stat(cwd); err != nil {
				missing = true
			}
		}
		meta := map[string]string{"workspace_id": workspace.ID}
		if workspace.ActiveTabID != "" {
			meta["active_tab_id"] = workspace.ActiveTabID
		}
		candidates = append(candidates, Candidate{
			Path:    cwd,
			Label:   workspace.Label,
			Source:  config.SourceHerdr,
			Missing: missing,
			Meta:    meta,
		})
	}
	return candidates
}

// SessionCandidates maps actionable named sessions to flat picker rows. The
// name is the attach identity; session_dir remains display metadata and is
// never inspected as a filesystem path.
func SessionCandidates(sessions []Session, getenv func(string) string) []Candidate {
	if getenv == nil {
		getenv = os.Getenv
	}
	currentSocket := getenv("HERDR_SOCKET_PATH")
	currentSession := getenv("HERDR_SESSION")
	candidates := make([]Candidate, 0, len(sessions))
	for _, session := range sessions {
		if session.Name == "" {
			continue
		}
		if (currentSocket != "" && session.SocketPath != "" && currentSocket == session.SocketPath) ||
			(currentSession != "" && currentSession == session.Name) {
			continue
		}
		candidates = append(candidates, Candidate{
			Path:   session.SessionDir,
			Label:  session.Name,
			Source: config.SourceSessions,
			Meta: map[string]string{
				"session_name": session.Name,
				"running":      strconv.FormatBool(session.Running),
				"default":      strconv.FormatBool(session.Default),
				"session_dir":  session.SessionDir,
				"socket_path":  session.SocketPath,
			},
		})
	}
	return candidates
}

// ResolveFocusedPane returns the complete focused Pane record needed by
// command-workspace targeting. Invalid or absent ids deliberately degrade to
// no focus before a pane id can reach close-on-exit shell composition.
func ResolveFocusedPane(snapshot Snapshot) (*Pane, bool) {
	if !validPaneID.MatchString(snapshot.FocusedPaneID) {
		return nil, false
	}
	for i := range snapshot.Panes {
		if snapshot.Panes[i].ID == snapshot.FocusedPaneID {
			return &snapshot.Panes[i], true
		}
	}
	return nil, false
}

func representativeCWD(panes []Pane) string {
	for _, pane := range panes {
		if pane.Focused {
			return firstNonEmptyString(pane.ForegroundCWD, pane.CWD)
		}
	}
	if len(panes) == 0 {
		return ""
	}
	return firstNonEmptyString(panes[0].ForegroundCWD, panes[0].CWD)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// HerdrAction records what FocusOrCreate did so callers can gate template
// application (only a freshly created workspace gets its template applied).
type HerdrAction int

const (
	// HerdrActionNone is the zero value and should never be returned by a
	// successful FocusOrCreate call; it exists so an unset result is obvious.
	HerdrActionNone HerdrAction = iota
	// HerdrActionFocused means an existing workspace was focused.
	HerdrActionFocused
	// HerdrActionCreated means a new workspace was created and focused.
	HerdrActionCreated
)

// FocusResult is the outcome of FocusOrCreate. RootTabID/RootPaneID are only
// populated when Action == HerdrActionCreated (a freshly created workspace
// has exactly one tab and one pane to seed a template from).
type FocusResult struct {
	WorkspaceID string
	Action      HerdrAction
	RootTabID   string
	RootPaneID  string
}

// Registry keeps the provider set for a given config/probes snapshot. It is
// safe to construct a Registry per command invocation; tests rebuild it to
// avoid cross-test state.
type Registry struct {
	providers map[string]Provider
	cfg       *config.Config
	probes    config.Probes
}

// WithHerdrSnapshot supplies the already-captured startup generation to this
// registry, preventing its herdr provider from issuing another state query.
func (r *Registry) WithHerdrSnapshot(snapshot Snapshot) *Registry {
	provider, ok := r.providers[config.SourceHerdr].(*herdrProvider)
	if !ok {
		return r
	}
	copy := cloneSnapshot(snapshot)
	provider.snapshot = &copy
	return r
}

// DisableHerdr leaves the registry's unrelated providers usable after an
// initial snapshot failure, without retrying fragmented state loading.
func (r *Registry) DisableHerdr() *Registry {
	if provider, ok := r.providers[config.SourceHerdr].(*herdrProvider); ok {
		provider.disabled = true
	}
	return r
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	copy := snapshot
	copy.Workspaces = append([]Workspace(nil), snapshot.Workspaces...)
	copy.Tabs = append([]Tab(nil), snapshot.Tabs...)
	copy.Panes = append([]Pane(nil), snapshot.Panes...)
	return copy
}

// NewRegistry returns a Registry populated with the built-in providers gated
// by the supplied config/probes. The herdr driver is optional and may be nil
// (the herdr provider then contributes no candidates); callers may inject a
// real driver once available. basePath/baseSources let a group workspace
// entry build a scoped nested Registry (see command/open.go); pass "" and
// nil for the top-level registry.
func NewRegistry(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver) *Registry {
	if cfg == nil {
		cfg = config.Defaults()
	}
	providers := map[string]Provider{
		config.SourceHerdr:      &herdrProvider{driver: herdrDriver, probes: probes, cfg: cfg},
		config.SourceSessions:   &sessionsProvider{driver: herdrDriver, probes: probes},
		config.SourceWorkspaces: &workspacesProvider{cfg: cfg},
		config.SourceZoxide:     &zoxideProvider{probes: probes, cfg: cfg},
		config.SourceProjects:   &projectsProvider{cfg: cfg, roots: append([]string(nil), cfg.Sources.Projects.Roots...)},
	}
	for _, integration := range cfg.Integrations {
		providers[integration.Name] = &integrationProvider{cfg: integration}
	}
	return &Registry{providers: providers, cfg: cfg, probes: probes}
}

// NewScopedRegistry builds a Registry for a group workspace's nested picker:
// only the sources named in the group entry run; the projects source scans
// beneath root instead of contributing nothing, and the zoxide source is
// scoped to root's descendants instead of surfacing the user's entire
// unscoped zoxide history.
func NewScopedRegistry(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver, sources []string, root string) *Registry {
	return NewScopedRegistryWithOrder(cfg, probes, herdrDriver, sources, sources, root)
}

// NewScopedRegistryWithOrder builds a scoped registry with an explicit effective
// order. The source list controls which providers are available; order controls
// which of those providers run and how they are ranked/displayed. Keeping these
// separate lets a group with omitted source_order inherit the global order while
// preserving its explicit source membership and lazy integrations.
func NewScopedRegistryWithOrder(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver, sources, order []string, root string) *Registry {
	if len(sources) == 0 {
		sources = order
	}
	return newScopedRegistry(cfg, probes, herdrDriver, sources, order, root, nil)
}

// NewScopedRegistryForWorkspace applies the group's source order and typed
// project override while keeping the group path as the sole project root.
func NewScopedRegistryForWorkspace(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver, workspace config.WorkspaceConfig, root string) *Registry {
	return NewScopedRegistryForWorkspaceWithOrder(cfg, probes, herdrDriver, workspace, workspace.SourceOrder, root)
}

// NewScopedRegistryForWorkspaceWithOrder applies an effective order separately
// from the workspace's explicit source membership.
func NewScopedRegistryForWorkspaceWithOrder(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver, workspace config.WorkspaceConfig, order []string, root string) *Registry {
	sources := workspace.SourceOrder
	if len(sources) == 0 {
		sources = order
	}
	return newScopedRegistry(cfg, probes, herdrDriver, sources, order, root, workspace.Sources.Projects)
}

func newScopedRegistry(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver, sources, order []string, root string, override *config.ProjectsSourceOverride) *Registry {
	if cfg == nil {
		cfg = config.Defaults()
	}
	projects := config.MergeProjectsSourceConfig(cfg.Sources.Projects, override)
	projects.Roots = nil
	scoped := &config.Config{
		General:      config.General{SourceOrder: append([]string(nil), order...), Selector: cfg.General.Selector},
		Sources:      cfg.Sources,
		Defaults:     cfg.Defaults,
		Integrations: append([]config.IntegrationConfig(nil), cfg.Integrations...),
	}
	scoped.Sources.Projects = projects
	providers := map[string]Provider{
		config.SourceHerdr:      &herdrProvider{driver: herdrDriver, probes: probes, cfg: scoped},
		config.SourceSessions:   &sessionsProvider{driver: herdrDriver, probes: probes},
		config.SourceWorkspaces: &workspacesProvider{cfg: scoped},
		config.SourceZoxide:     &zoxideProvider{probes: probes, cfg: scoped, root: root},
		// The project provider reads the scoped effective config so local
		// overrides apply only within this group; the root remains the sole
		// scan root and global roots are deliberately discarded.
		config.SourceProjects: &projectsProvider{cfg: scoped, root: root},
	}
	for _, integration := range scoped.Integrations {
		providers[integration.Name] = &integrationProvider{cfg: integration}
	}
	return &Registry{
		providers: providers,
		cfg:       scoped,
		probes:    probes,
	}
}

// Providers returns a defensive copy of the registered providers, in
// [config.General.SourceOrder] order (unlisted providers are appended after, for
// callers introspecting the full set).
func (r *Registry) Providers() []Provider {
	out := make([]Provider, 0, len(r.providers))
	seen := make(map[string]bool, len(r.providers))
	for _, name := range r.cfg.General.SourceOrder {
		if p, ok := r.providers[name]; ok && !seen[name] {
			out = append(out, p)
			seen[name] = true
		}
	}
	for name, p := range r.providers {
		if !seen[name] {
			out = append(out, p)
			seen[name] = true
		}
	}
	return out
}

// gatedProvider lets a concrete provider report whether it should run given
// the config/probes snapshot (binary probes, empty workspace list, etc.),
// beyond simply being named in general.sources.
type gatedProvider interface {
	Provider
	enabled(cfg *config.Config, probes config.Probes) bool
}

// Enabled returns the providers that should run, in general.sources order.
func (r *Registry) Enabled() []Provider {
	enabled := make([]Provider, 0, len(r.providers))
	for _, name := range r.cfg.General.SourceOrder {
		p, ok := r.providers[name]
		if !ok {
			continue
		}
		if g, ok := p.(gatedProvider); ok && !g.enabled(r.cfg, r.probes) {
			continue
		}
		enabled = append(enabled, p)
	}
	return enabled
}

// Collect runs all enabled providers and concatenates their candidates. A
// failing provider contributes no candidates and its error is returned
// alongside the other providers' results, so a single broken source never
// blanks the list. Each candidate receives its source's configured icon
// (from [sources.<name>].icon) so the picker can render it.
func (r *Registry) Collect(ctx context.Context) ([]Candidate, error) {
	var (
		out      []Candidate
		firstErr error
	)
	for _, p := range r.Enabled() {
		cands, err := p.List(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		icon := r.IconFor(p.Name())
		// Defensive copy: providers may reuse backing arrays across calls.
		for _, c := range cands {
			clone := c.Clone()
			if icon != "" {
				clone.Icon = icon
			}
			out = append(out, clone)
		}
	}
	return out, firstErr
}

// IconFor returns the configured icon for the named source, or "" when no
// icon is configured. Centralised so every provider's candidates receive the
// same icon from a single lookup site.
func (r *Registry) IconFor(name string) string {
	switch name {
	case config.SourceHerdr:
		return r.cfg.Sources.Herdr.Icon
	case config.SourceSessions:
		return r.cfg.Sources.Sessions.Icon
	case config.SourceWorkspaces:
		return r.cfg.Sources.Workspaces.Icon
	case config.SourceZoxide:
		return r.cfg.Sources.Zoxide.Icon
	case config.SourceProjects:
		return r.cfg.Sources.Projects.Icon
	}
	for _, integration := range r.cfg.Integrations {
		if integration.Name == name {
			return integration.Icon
		}
	}
	return ""
}

// --- workspaces provider ---

// workspacesProvider surfaces predefined [[workspaces]] entries as
// selectable candidates. A plain entry (Type "" or "shell") yields one
// candidate labelled by Name with a tilde-expanded Path, marked Missing when
// the path does not exist on disk. A group entry (Type "group") yields one
// candidate representing the group itself (Meta["group"]="true",
// Meta["group_sources"]=comma-joined Sources) so the command layer can drill
// into a nested, scoped picker on selection instead of launching Herdr
// directly.
type workspacesProvider struct {
	cfg *config.Config
}

func (workspacesProvider) Name() string { return config.SourceWorkspaces }

func (workspacesProvider) enabled(cfg *config.Config, _ config.Probes) bool {
	return cfg != nil && len(cfg.Workspaces) > 0
}

func (p *workspacesProvider) List(ctx context.Context) ([]Candidate, error) {
	out := make([]Candidate, 0, len(p.cfg.Workspaces))
	for _, ws := range p.cfg.Workspaces {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		// Fall back to the raw path on an unresolvable HOME so os.Stat below
		// reports the bad path instead of crashing on expansion.
		path := ws.Path
		if expanded, err := pathutil.ExpandTilde(ws.Path); err == nil {
			path = expanded
		}
		cand := Candidate{
			Path:    path,
			Label:   ws.Name,
			Source:  config.SourceWorkspaces,
			Aliases: append([]string(nil), ws.Aliases...),
			Meta: map[string]string{
				"entry_id":       WorkspaceEntryIdentity(path, ws),
				"workspace_name": ws.Name,
			},
		}
		if _, err := os.Stat(path); err != nil {
			cand.Missing = true
		}
		switch {
		case ws.Type == config.WorkspaceTypeGroup:
			cand.Meta = map[string]string{
				"group":          "true",
				"group_sources":  strings.Join(ws.SourceOrder, ","),
				"workspace_name": ws.Name,
			}
			if ws.Template != "" {
				cand.Meta["group_template"] = ws.Template
			}
		case ws.Template != "":
			cand.Meta = map[string]string{
				"template":       ws.Template,
				"workspace_name": ws.Name,
			}
		case ws.Command != "":
			cand.Meta = map[string]string{
				"command":        ws.Command,
				"workspace_name": ws.Name,
			}
			if ws.CloseOnExit {
				cand.Meta["close_on_exit"] = "true"
			}
		}
		cand.Meta["entry_id"] = WorkspaceEntryIdentity(path, ws)
		out = append(out, cand)
	}
	return out, nil
}

func WorkspaceEntryIdentity(path string, ws config.WorkspaceConfig) string {
	normalized := path
	if canonical, err := pathutil.Normalize(path); err == nil {
		normalized = canonical
	}
	sources := append([]string(nil), ws.SourceOrder...)
	sort.Strings(sources)
	payload := strings.Join([]string{normalized, ws.Type, ws.Template, ws.Command, strconv.FormatBool(ws.CloseOnExit), strings.Join(sources, ",")}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return "v1:" + hex.EncodeToString(digest[:])
}

// --- herdr provider ---

type herdrProvider struct {
	driver   HerdrDriver
	probes   config.Probes
	cfg      *config.Config
	snapshot *Snapshot
	disabled bool
}

func (h *herdrProvider) Name() string { return config.SourceHerdr }

// enabled when a driver is present and the binary is on PATH. Without a
// driver the provider is inert.
func (h *herdrProvider) enabled(_ *config.Config, probes config.Probes) bool {
	return probes.Herdr && !h.disabled
}

func (h *herdrProvider) List(ctx context.Context) ([]Candidate, error) {
	if h.driver == nil || h.disabled {
		return nil, nil
	}
	if h.snapshot != nil {
		return HerdrCandidates(*h.snapshot), nil
	}
	snapshot, err := h.driver.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return HerdrCandidates(snapshot), nil
}

// --- sessions provider ---

// sessionsProvider performs the one startup-only session-list command. It has
// no snapshot state, so it cannot participate in the active-daemon refresh.
type sessionsProvider struct {
	driver HerdrDriver
	probes config.Probes
	getenv func(string) string
}

func (*sessionsProvider) Name() string { return config.SourceSessions }

func (*sessionsProvider) enabled(_ *config.Config, probes config.Probes) bool {
	return probes.Herdr
}

func (p *sessionsProvider) List(ctx context.Context) ([]Candidate, error) {
	if p.driver == nil {
		return nil, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, SessionsListTimeout)
	defer cancel()
	sessions, err := p.driver.ListSessions(listCtx)
	if err != nil {
		return nil, err
	}
	return SessionCandidates(sessions, p.getenv), nil
}

// --- zoxide provider ---

// zoxideProvider surfaces zoxide's directory history as candidates. root, set
// by a group workspace's nested picker (NewScopedRegistry), restricts results
// to root's own descendants (including root itself); the top-level registry
// leaves root empty so the full zoxide history is available unscoped.
type zoxideProvider struct {
	probes config.Probes
	cfg    *config.Config
	root   string
}

func (zoxideProvider) Name() string { return config.SourceZoxide }

func (zoxideProvider) enabled(_ *config.Config, probes config.Probes) bool {
	return probes.Zoxide
}

func (p zoxideProvider) List(ctx context.Context) ([]Candidate, error) {
	// zoxide query --list prints "score\t/path/to/dir"; only the path column
	// is used by shep. Errors (zoxide not initialised, empty db) degrade to
	// an empty result rather than aborting enumeration.
	cmd := exec.CommandContext(ctx, "zoxide", "query", "--list")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	return parseZoxideOutput(string(out), p.root), nil
}

// parseZoxideOutput parses `zoxide query --list` output ("score\tpath" per
// line) into candidates. When root is non-empty, only paths at or beneath
// root are kept, so a group workspace's nested picker never leaks the user's
// entire unscoped zoxide history. Exported as a pure function (not a method)
// so the scoping rule is unit-testable without shelling out to zoxide.
func parseZoxideOutput(out, root string) []Candidate {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	cands := make([]Candidate, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		path := line
		if idx := strings.IndexByte(line, '\t'); idx >= 0 {
			path = strings.TrimSpace(line[idx+1:])
		}
		if path == "" {
			continue
		}
		if root != "" && !isWithinRoot(root, path) {
			continue
		}
		cand := Candidate{
			Path:   path,
			Label:  RelativeLabel(path),
			Source: config.SourceZoxide,
		}
		// zoxide's directory history can go stale once a visited directory is
		// deleted; stat it here so launch()'s existing Missing check rejects
		// it instead of a false success (printing the path) or falling back
		// to "/", $HOME, or cwd.
		if _, err := os.Stat(path); err != nil {
			cand.Missing = true
		}
		cands = append(cands, cand)
	}
	return cands
}

// isWithinRoot reports whether path is root itself or one of its
// descendants. root may use a leading "~/" (as group workspace paths do); it
// is tilde-expanded before comparison. An unresolvable relative path (e.g.
// different volumes on Windows) is treated as "not within root".
func isWithinRoot(root, path string) bool {
	if expanded, err := pathutil.ExpandTilde(root); err == nil {
		root = expanded
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// --- projects provider ---

// projectsProvider discovers directories that contain any configured marker
// (a file or directory name) beneath root, recursively up to MaxDepth when
// Recursive is set (depth 1 = root's immediate children). It never descends
// past a directory it has already flagged as a project, to avoid noisy
// nested matches (e.g. vendored dependencies).
type projectsProvider struct {
	cfg   *config.Config
	root  string
	roots []string
}

func (projectsProvider) Name() string { return config.SourceProjects }

func (p *projectsProvider) enabled(_ *config.Config, _ config.Probes) bool {
	return p.root != "" || len(p.roots) > 0
}

func (p *projectsProvider) List(ctx context.Context) ([]Candidate, error) {
	if p.root != "" {
		return ListProjects(ctx, p.cfg.Sources.Projects, p.root)
	}
	return ListProjectsRoots(ctx, p.cfg.Sources.Projects, p.roots)
}

// ListProjects scans root for project directories per cfg. Exported so tests
// and the command layer can exercise the scan directly. maxDepth <= 0 with
// Recursive true is treated as depth 1 (immediate children only).
func ListProjects(ctx context.Context, cfg config.ProjectsSourceConfig, root string) ([]Candidate, error) {
	return listProjectsWithDiscovery(ctx, cfg, root, DiscoverWorktrees)
}

func listProjectsWithDiscovery(ctx context.Context, cfg config.ProjectsSourceConfig, root string, discover func(context.Context, string) ([]WorktreeInfo, error)) ([]Candidate, error) {
	if root == "" {
		return nil, nil
	}
	if expanded, err := pathutil.ExpandTilde(root); err == nil {
		root = expanded
	}
	maxDepth := cfg.MaxDepth
	if !cfg.Recursive || maxDepth <= 0 {
		maxDepth = 1
	}
	ignore := make(map[string]bool, len(cfg.Ignore))
	for _, n := range cfg.Ignore {
		ignore[n] = true
	}
	var out []Candidate
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		names := make(map[string]bool, len(entries))
		for _, e := range entries {
			names[e.Name()] = true
		}
		isProject := false
		for _, m := range cfg.Markers {
			if names[m] {
				isProject = true
				break
			}
		}
		if isProject {
			primary := Candidate{Path: dir, Label: RelativeLabel(dir), Source: config.SourceProjects}
			worktrees, err := discover(ctx, dir)
			if err != nil {
				out = append(out, primary)
				return nil
			}
			repoName := filepath.Base(dir)
			for _, wt := range worktrees {
				meta := map[string]string{
					"is_worktree": "true", "branch": wt.Branch, "repo": repoName,
					"worktree_path": wt.Path, "head": wt.Head,
				}
				if pathutil.SameDir(dir, wt.Path) {
					meta["main_worktree"] = "true"
					primary.Meta = meta
					continue
				}
				meta["main_worktree"] = "false"
				out = append(out, Candidate{
					Path: wt.Path, Label: fmt.Sprintf("%s (%s)", repoName, wt.Branch),
					Source: config.SourceProjects, Meta: meta,
				})
			}
			out = append(out, primary)
			return nil // do not descend past a detected project
		}
		if depth >= maxDepth {
			return nil
		}
		for _, e := range entries {
			if !e.IsDir() || ignore[e.Name()] {
				continue
			}
			if err := walk(filepath.Join(dir, e.Name()), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	// root itself is never a candidate (it is already represented by the
	// group workspace entry that owns it); scanning starts at its children.
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || ignore[e.Name()] {
			continue
		}
		if err := walk(filepath.Join(root, e.Name()), 1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListProjectsRoots scans each configured global root in normalized deterministic
// order and deduplicates projects by normalized path. An empty roots list is
// intentionally silent.
func ListProjectsRoots(ctx context.Context, cfg config.ProjectsSourceConfig, roots []string) ([]Candidate, error) {
	normalizedRoots := make([]string, 0, len(roots))
	seenRoots := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		normalized, err := pathutil.Normalize(root)
		if err != nil {
			continue
		}
		if _, exists := seenRoots[normalized]; exists {
			continue
		}
		seenRoots[normalized] = struct{}{}
		normalizedRoots = append(normalizedRoots, normalized)
	}
	sort.Strings(normalizedRoots)
	var out []Candidate
	seenProjects := make(map[string]struct{})
	for _, root := range normalizedRoots {
		projects, err := ListProjects(ctx, cfg, root)
		if err != nil {
			return nil, err
		}
		for _, candidate := range projects {
			normalized, err := pathutil.Normalize(candidate.Path)
			if err != nil {
				continue
			}
			if _, exists := seenProjects[normalized]; exists {
				continue
			}
			seenProjects[normalized] = struct{}{}
			candidate.Path = normalized
			candidate.Label = RelativeLabel(normalized)
			out = append(out, candidate)
		}
	}
	return out, nil
}

// userHomeDir resolves the current user's home directory. It is a package
// variable (not called directly as os.UserHomeDir) so tests can simulate an
// unresolvable home directory for RelativeLabel's fallback path.
var userHomeDir = os.UserHomeDir

// RelativeLabel formats an absolute path for display. Paths under the
// current user's home directory render "~/..." style; paths outside home,
// or any path when the home directory cannot be resolved, fall back to the
// path unchanged (outside home) or the path's base name (home unresolvable).
func RelativeLabel(p string) string {
	home, err := userHomeDir()
	if err != nil || home == "" {
		return filepath.Base(p)
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.Join("~", rel)
}
