// Package source enumerates project candidates from a collection of
// providers and offers a small registry that gates them by config and binary
// availability.
//
// A Provider yields Candidates; a Registry decides which Providers are enabled
// for a given Config + Probes snapshot and runs them. Normalisation and
// deduplication are the resolver's job (see internal/resolver); source
// providers only collect raw, labelled paths.
package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tranceh2/shep/internal/config"
)

// Candidate is one project discovered by a provider. Path is the raw path as
// observed; NormalizedPath is filled by the resolver (left empty here).
// Label is a short human-friendly name (usually the base directory). Source
// names the provider that produced the candidate. Meta carries optional,
// provider-specific metadata (copied defensively on read by callers).
type Candidate struct {
	Path           string
	NormalizedPath string
	Label          string
	Source         string
	Meta           map[string]string
}

// Clone returns a deep-enough copy of the candidate so mutating the returned
// value cannot bleed back into a provider's cached slice.
func (c Candidate) Clone() Candidate {
	out := c
	if c.Meta != nil {
		out.Meta = make(map[string]string, len(c.Meta))
		for k, v := range c.Meta {
			out.Meta[k] = v
		}
	}
	return out
}

// Provider enumerates candidates from one source family. Providers hold a
// config/probes snapshot captured at construction, so List takes only a
// context (matching the design contract).
type Provider interface {
	// Name returns the short provider kind (cwd, herdr, zoxide, roots).
	Name() string
	// List collects candidates. An empty result with nil error is normal.
	List(ctx context.Context) ([]Candidate, error)
}

// HerdrDriver is the contract shep keeps with the Herdr CLI. The real
// implementation lives in internal/herdr; tests inject a fake. A nil driver
// keeps the herdr provider inert so source enumeration degrades to the
// remaining providers without panicking.
type HerdrDriver interface {
	// Detect reports whether Herdr is usable (binary present). Daemon liveness
	// is discovered lazily by actual command calls; Detect is a cheap probe.
	Detect(ctx context.Context) bool
	// ListWorkspaces returns the current Herdr workspaces with the CWD
	// resolved from the workspace's panes (workspaces do not carry a cwd in
	// the Herdr JSON envelope).
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	// FocusOrCreate focuses an existing workspace whose pane cwd /
	// foreground_cwd normalises to the candidate's path, or creates a new
	// focused workspace via `herdr workspace create --cwd --label --focus`. The
	// returned action lets callers decide whether to run a startup command.
	FocusOrCreate(ctx context.Context, cand Candidate) (FocusResult, error)
	// RunStartup runs a command in the first pane of the named workspace via
	// `herdr pane run`. It is intended to fire only on a freshly created
	// workspace (HI-4).
	RunStartup(ctx context.Context, workspaceID, command string) error
	// ListTabs returns the tabs of the named workspace via
	// `herdr tab list --workspace <id>`. Used by the workspace preview section.
	ListTabs(ctx context.Context, workspaceID string) ([]Tab, error)
	// ListPanes returns the panes of the named workspace via
	// `herdr pane list --workspace <id>`. Used by the workspace preview section.
	ListPanes(ctx context.Context, workspaceID string) ([]Pane, error)
	// ListAgents returns the agents known to Herdr via `herdr agent list`.
	ListAgents(ctx context.Context) ([]Agent, error)
	// ReadPane returns the captured terminal buffer of a pane via
	// `herdr pane read <pane_id> --lines <lines> --format ansi`. lines caps the
	// number of trailing lines returned; <= 0 means the daemon default.
	ReadPane(ctx context.Context, paneID string, lines int) (string, error)
}

// Workspace is a minimal, driver-supplied description of a Herdr workspace.
// CWD is derived by the driver from the workspace's panes; the Herdr JSON
// envelope does not attach a cwd directly to a workspace.
type Workspace struct {
	ID    string
	Label string
	CWD   string
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

// Pane is one pane of a Herdr workspace. CWD is the pane's working directory;
// ForegroundCWD is the cwd of the foreground process running in it (Herdr
// populates this only while a command is active).
type Pane struct {
	ID            string
	WorkspaceID   string
	CWD           string
	ForegroundCWD string
	Focused       bool
}

// Agent is one Herdr agent. Status mirrors the `agent_status` field of the
// Herdr agent-list envelope (e.g. "running", "idle", "").
type Agent struct {
	ID     string
	Label  string
	Status string
}

// HerdrAction records what FocusOrCreate did so callers can gate startup.
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

// FocusResult is the outcome of FocusOrCreate. WorkspaceID is the id of the
// focused or freshly created workspace, ready to feed RunStartup.
type FocusResult struct {
	WorkspaceID string
	Action      HerdrAction
}

// Registry keeps the provider set for a given config/probes snapshot. It is
// safe to construct a Registry per command invocation; tests rebuild it to
// avoid cross-test state.
type Registry struct {
	providers []Provider
	cfg       *config.Config
	probes    config.Probes
}

// NewRegistry returns a Registry populated with the built-in providers gated
// by the supplied config/probes. The herdr driver is optional and may be nil
// (the herdr provider then contributes no candidates); callers may inject a
// real driver once available.
func NewRegistry(cfg *config.Config, probes config.Probes, herdrDriver HerdrDriver) *Registry {
	if cfg == nil {
		cfg = config.Defaults()
	}
	return &Registry{
		providers: []Provider{
			&cwdProvider{},
			&rootsProvider{cfg: cfg},
			&configProvider{cfg: cfg},
			&herdrProvider{driver: herdrDriver, probes: probes, cfg: cfg},
			&zoxideProvider{probes: probes, cfg: cfg},
		},
		cfg:    cfg,
		probes: probes,
	}
}

// Providers returns a defensive copy of the registered providers.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, len(r.providers))
	copy(out, r.providers)
	return out
}

// Enabled returns the providers that should run, honouring General.Provider
// Order when set so callers iterate in a stable, user-controlled order.
func (r *Registry) Enabled() []Provider {
	enabled := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		if isEnabled(p, r.cfg, r.probes) {
			enabled = append(enabled, p)
		}
	}
	if order := r.cfg.General.ProviderOrder; len(order) > 0 {
		enabled = applyOrder(enabled, order)
	}
	return enabled
}

// isEnabled centralises gating so adding a provider only needs one knob. Each
// concrete provider implements an enabled() method instead of relying on the
// registry to know every kind.
type gatedProvider interface {
	Provider
	enabled(cfg *config.Config, probes config.Probes) bool
}

func isEnabled(p Provider, cfg *config.Config, probes config.Probes) bool {
	if g, ok := p.(gatedProvider); ok {
		return g.enabled(cfg, probes)
	}
	return true
}

// applyOrder reorders providers to match the requested kind sequence,
// appending unlisted enabled providers afterwards in registration order.
func applyOrder(providers []Provider, order []string) []Provider {
	byName := make(map[string]Provider, len(providers))
	for _, p := range providers {
		byName[p.Name()] = p
	}
	out := make([]Provider, 0, len(providers))
	seen := make(map[string]bool, len(providers))
	for _, name := range order {
		if p, ok := byName[name]; ok {
			out = append(out, p)
			seen[name] = true
		}
	}
	for _, p := range providers {
		if !seen[p.Name()] {
			out = append(out, p)
		}
	}
	return out
}

// Collect runs all enabled providers and concatenates their candidates. A
// failing provider contributes no candidates and its error is returned
// alongside the other providers' results, so a single broken source never
// blanks the list.
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
		// Defensive copy: providers may reuse backing arrays across calls.
		for _, c := range cands {
			out = append(out, c.Clone())
		}
	}
	return out, firstErr
}

// --- cwd provider ---

type cwdProvider struct{}

func (cwdProvider) Name() string { return "cwd" }

func (c cwdProvider) enabled(_ *config.Config, _ config.Probes) bool { return true }

func (cwdProvider) List(ctx context.Context) ([]Candidate, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return []Candidate{{Path: dir, Label: RelativeLabel(dir), Source: "cwd"}}, nil
}

// --- roots provider ---

type rootsProvider struct {
	cfg *config.Config
}

func (rootsProvider) Name() string { return "roots" }

// enabled when at least one configured roots source defines a non-empty path.
func (rootsProvider) enabled(cfg *config.Config, _ config.Probes) bool {
	if cfg == nil {
		return false
	}
	for _, s := range cfg.Sources {
		if s.Kind == config.KindRoots && s.Enabled {
			if p := s.Options["path"]; p != "" {
				return true
			}
		}
	}
	return false
}

func (p *rootsProvider) List(ctx context.Context) ([]Candidate, error) {
	return ListRoots(ctx, p.cfg)
}

// ListRoots expands every configured roots source into one candidate per
// immediate subdirectory. Exported so tests can exercise the scan directly
// without a full Collect round-trip.
func ListRoots(ctx context.Context, cfg *config.Config) ([]Candidate, error) {
	var out []Candidate
	for name, s := range cfg.Sources {
		if s.Kind != config.KindRoots || !s.Enabled {
			continue
		}
		root := s.Options["path"]
		if root == "" {
			continue
		}
		root = expandTilde(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			if !e.IsDir() {
				continue
			}
			full := filepath.Join(root, e.Name())
			out = append(out, Candidate{
				Path:   full,
				Label:  RelativeLabel(full),
				Source: name,
			})
		}
	}
	return out, nil
}

// --- config provider ---

// configProvider surfaces predefined [[workspaces]] entries from the config as
// selectable candidates. It is the user-curated counterpart to the dynamic
// providers (cwd, roots, herdr, zoxide): each workspace yields one candidate
// with Label = workspace Name, Path = tilde-expanded workspace Path, and
// Source = "config". It activates when cfg.Workspaces is non-empty and the
// config does not disable it via a sources override (KindConfig + enabled=false).
type configProvider struct {
	cfg *config.Config
}

func (configProvider) Name() string { return "config" }

// enabled: at least one predefined workspace AND not disabled by override.
// Gating on non-empty Workspaces keeps the default (empty) config from
// registering an inert provider and keeps `shep list` output minimal on a
// pristine machine.
func (configProvider) enabled(cfg *config.Config, _ config.Probes) bool {
	if cfg == nil {
		return false
	}
	if s, ok := cfg.Sources["config"]; ok && s.Kind == config.KindConfig && !s.Enabled {
		return false
	}
	return len(cfg.Workspaces) > 0
}

func (p *configProvider) List(ctx context.Context) ([]Candidate, error) {
	out := make([]Candidate, 0, len(p.cfg.Workspaces))
	for _, ws := range p.cfg.Workspaces {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		out = append(out, Candidate{
			Path:   expandTilde(ws.Path),
			Label:  ws.Name,
			Source: "config",
		})
	}
	return out, nil
}

// --- herdr provider ---

type herdrProvider struct {
	driver HerdrDriver
	probes config.Probes
	cfg    *config.Config
}

func (h *herdrProvider) Name() string { return "herdr" }

// enabled when a driver is present, the binary is on PATH, and the config
// does not disable herdr by override. Without a driver the provider is inert
// (PL-3 gating).
func (h *herdrProvider) enabled(cfg *config.Config, probes config.Probes) bool {
	if cfg != nil {
		if s, ok := cfg.Sources["herdr"]; ok && s.Kind == config.KindHerdr && !s.Enabled {
			return false
		}
	}
	if !probes.Herdr {
		return false
	}
	return true
}

func (h *herdrProvider) List(ctx context.Context) ([]Candidate, error) {
	if h.driver == nil {
		return nil, nil
	}
	workspaces, err := h.driver.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(workspaces))
	for _, w := range workspaces {
		if w.CWD == "" {
			continue
		}
		label := w.Label
		if label == "" {
			label = baseLabel(w.CWD)
		}
		out = append(out, Candidate{
			Path:   w.CWD,
			Label:  label,
			Source: "herdr",
			Meta:   map[string]string{"workspace_id": w.ID},
		})
	}
	return out, nil
}

// --- zoxide provider ---

type zoxideProvider struct {
	probes config.Probes
	cfg    *config.Config
}

func (zoxideProvider) Name() string { return "zoxide" }

func (zoxideProvider) enabled(cfg *config.Config, probes config.Probes) bool {
	if !probes.Zoxide {
		return false
	}
	if cfg != nil {
		if s, ok := cfg.Sources["zoxide"]; ok && s.Kind == config.KindZoxide && !s.Enabled {
			return false
		}
	}
	return true
}

func (zoxideProvider) List(ctx context.Context) ([]Candidate, error) {
	// zoxide query --list prints "score\t/path/to/dir"; only the path column
	// is used by shep. Errors (zoxide not initialised, empty db) degrade to
	// an empty result rather than aborting enumeration.
	cmd := exec.CommandContext(ctx, "zoxide", "query", "--list")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
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
		cands = append(cands, Candidate{
			Path:   path,
			Label:  RelativeLabel(path),
			Source: "zoxide",
		})
	}
	return cands, nil
}

// expandTilde replaces a leading ~ with the user's home dir. Unresolvable
// home dirs return the input untouched so the scan fails later at ReadDir
// rather than crashing here.
func expandTilde(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
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

// baseLabel derives a human label from a path's base; empty paths yield "?".
func baseLabel(p string) string {
	if p == "" {
		return "?"
	}
	base := filepath.Base(p)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return p
	}
	return base
}
