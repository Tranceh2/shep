// Package config defines the shep configuration model, discovery rules and
// the binary probe used to gate optional source providers.
//
// Configuration lives at $XDG_CONFIG_HOME/shep/config.toml (falling back to
// ~/.config/shep/config.toml when XDG_CONFIG_HOME is unset), so shep uses the
// same location across Linux and macOS. A missing config is not an error:
// callers fall back to Defaults(), which enables every built-in source
// (herdr, workspaces, zoxide, projects) without any hardcoded user-specific
// paths.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tranceh2/shep/internal/pathutil"
)

// Built-in source names. general.sources lists which of these are enabled
// and in what merge/display order; unknown names fail Load fast. shep does
// not support arbitrary user-defined source providers — only these four.
const (
	SourceHerdr      = "herdr"
	SourceWorkspaces = "workspaces"
	SourceZoxide     = "zoxide"
	SourceProjects   = "projects"
)

// defaultSourceOrder is used when general.sources is empty/absent.
var defaultSourceOrder = []string{SourceHerdr, SourceWorkspaces, SourceZoxide, SourceProjects}

var validSourceNames = map[string]bool{
	SourceHerdr: true, SourceWorkspaces: true, SourceZoxide: true, SourceProjects: true,
}

// Selector values for the [general].selector field. They pick the interactive
// candidate picker used by `shep open` after the direct (exact/single) match.
const (
	SelectorBuiltin = "builtin"
	SelectorFzf     = "fzf"
	SelectorAuto    = "auto"
)

// Workspace entry types. Empty (WorkspaceTypeShell) is a single project
// workspace; WorkspaceTypeGroup turns the entry into a picker source at its
// own path, drawing candidates from its own Sources list.
const (
	WorkspaceTypeShell = "shell"
	WorkspaceTypeGroup = "group"
)

// Split directions for template layout nodes. "rows" stacks children
// top/bottom; "cols" places them side by side.
const (
	SplitRows = "rows"
	SplitCols = "cols"
)

// Built-in preview section names. These render hardcoded logic in the
// preview renderer and require no declaration in config; they are simply
// valid names wherever a `preview = [...]` list is accepted.
const (
	PreviewIdentity    = "identity"
	PreviewGit         = "git"
	PreviewWorkspace   = "workspace"
	PreviewActivePane  = "active_pane"
	PreviewDir         = "dir"
	PreviewAgentStatus = "agent_status"
)

var builtinPreviewNames = map[string]bool{
	PreviewIdentity: true, PreviewGit: true, PreviewWorkspace: true,
	PreviewActivePane: true, PreviewDir: true, PreviewAgentStatus: true,
}

// Default preview durations and output cap. They apply when the user omits
// the field (zero-value) so a custom preview command still gets a safe
// timeout, cache, and line cap without explicit config.
const (
	defaultPreviewTimeout  = 150 * time.Millisecond
	defaultPreviewCacheTTL = 5 * time.Second
	defaultPreviewMaxLines = 50
)

// Config is the top-level shep configuration document.
type Config struct {
	Version  int            `toml:"version,omitempty"`
	General  General        `toml:"general,omitempty"`
	Herdr    Herdr          `toml:"herdr,omitempty"`
	Defaults DefaultsConfig `toml:"defaults,omitempty"`
	TUI      TUIConfig      `toml:"tui,omitempty"`
	Preview  PreviewConfig  `toml:"preview,omitempty"`
	Sources  SourcesConfig  `toml:"sources,omitempty"`
	// Workspaces lists predefined project (or group) entries the workspaces
	// source provider surfaces as candidates.
	Workspaces []WorkspaceConfig `toml:"workspaces,omitempty"`
	// Templates describes what opens after Enter for a freshly created
	// workspace: tabs, panes, splits, sizes and commands. Keyed by name and
	// referenced from [defaults], [[workspaces]] and [[wildcards]].
	Templates map[string]TemplateConfig `toml:"templates,omitempty"`
	// Wildcards is an ordered list of glob -> template/preview rules scanned
	// in declaration order; the first pattern matching the candidate's
	// normalised path or base name wins.
	Wildcards []WildcardConfig `toml:"wildcards,omitempty"`
}

// General holds global tweaks. Sources lists the enabled built-in source
// names and their merge/display order; Selector picks the interactive
// picker for `shep open` after the direct match.
type General struct {
	Sources  []string `toml:"sources,omitempty"`
	Selector string   `toml:"selector,omitempty"`
}

// Herdr configures how the shep<->Herdr bridge locates the binary.
type Herdr struct {
	// Binary overrides the executable name looked up via exec.LookPath. Empty
	// means "herdr".
	Binary string `toml:"binary,omitempty"`
}

// DefaultsConfig holds the small set of fallback values applied when a
// resolved candidate carries none of its own: Type is informational
// candidate metadata (e.g. "shell"); Template names the [templates.<name>]
// applied when no workspace/wildcard template matched.
type DefaultsConfig struct {
	Type     string `toml:"type,omitempty"`
	Template string `toml:"template,omitempty"`
}

// TUIConfig configures the Bubble Tea picker's pane sizing and orientation.
// ListWidth/PreviewWidth are either "auto" or a percentage string like
// "60%"; see ParsePercent. Layout is TUILayoutLandscape (default when empty)
// or TUILayoutPortrait — in both orientations ListWidth/PreviewWidth mean
// "share of the split axis" (width in landscape, height in portrait).
type TUIConfig struct {
	ListWidth    string `toml:"list_width,omitempty"`
	PreviewWidth string `toml:"preview_width,omitempty"`
	Layout       string `toml:"layout,omitempty"`
}

// TUI layout orientation values for [tui].layout. TUILayoutLandscape (empty/
// default) splits the list/preview panes side by side; TUILayoutPortrait
// stacks the list pane above the preview pane. Named after Television's own
// landscape/portrait convention for the same concept, since shep already
// integrates with Television.
const (
	TUILayoutLandscape = "landscape"
	TUILayoutPortrait  = "portrait"
)

// SourcesConfig configures the four built-in source providers. Only these
// four tables are recognised; there is no support for arbitrary
// user-defined provider kinds.
type SourcesConfig struct {
	Herdr      HerdrSourceConfig      `toml:"herdr,omitempty"`
	Workspaces WorkspacesSourceConfig `toml:"workspaces,omitempty"`
	Zoxide     ZoxideSourceConfig     `toml:"zoxide,omitempty"`
	Projects   ProjectsSourceConfig   `toml:"projects,omitempty"`
}

// HerdrSourceConfig configures the herdr workspaces source's presentation.
type HerdrSourceConfig struct {
	Icon    string   `toml:"icon,omitempty"`
	Preview []string `toml:"preview,omitempty"`
}

// WorkspacesSourceConfig configures the predefined-[[workspaces]] source's
// presentation.
type WorkspacesSourceConfig struct {
	Icon    string   `toml:"icon,omitempty"`
	Preview []string `toml:"preview,omitempty"`
}

// ZoxideSourceConfig configures the zoxide source's presentation.
type ZoxideSourceConfig struct {
	Icon    string   `toml:"icon,omitempty"`
	Preview []string `toml:"preview,omitempty"`
}

// ProjectsSourceConfig configures the projects source: directories detected
// because they contain any configured marker (a file OR a directory name),
// discovered recursively up to MaxDepth beneath a [[workspaces]] type="group"
// entry's own path, when that entry lists "projects" in its Sources. The
// projects source never runs at the top level: it only contributes
// candidates once scoped to a group workspace's nested picker.
type ProjectsSourceConfig struct {
	Icon      string   `toml:"icon,omitempty"`
	Recursive bool     `toml:"recursive,omitempty"`
	MaxDepth  int      `toml:"max_depth,omitempty"`
	Markers   []string `toml:"markers,omitempty"`
	Ignore    []string `toml:"ignore,omitempty"`
	Preview   []string `toml:"preview,omitempty"`
}

// WorkspaceConfig is one entry in the [[workspaces]] list. A plain entry
// (Type empty or "shell") is a single project candidate; Type "group" turns
// the entry into a nested picker source rooted at Path, drawing candidates
// from Sources.
type WorkspaceConfig struct {
	Name string `toml:"name"`
	// Path is the project (or group root) path. A leading "~/" is expanded
	// to the user's home directory by the source provider.
	Path string `toml:"path,omitempty"`
	// Type is "" / "shell" for a single workspace, or "group" for a nested
	// picker source.
	Type string `toml:"type,omitempty"`
	// Sources lists the built-in source names a group entry draws from.
	// Only meaningful when Type == "group".
	Sources []string `toml:"sources,omitempty"`
	// Template names a [templates.<name>] applied when this workspace is
	// freshly created. Takes precedence over wildcards and [defaults].
	Template string `toml:"template,omitempty"`
	// Command, when set (and Template is not), runs directly in the root
	// pane of a freshly created workspace for this entry.
	Command string `toml:"command,omitempty"`
	// CloseOnExit wraps Command (only when Command is set) so the workspace's
	// root pane closes itself after the command's shell returns control
	// (regardless of exit status), via the same shell-chaining
	// ("; <binary> pane close <pane_id>") used by leaf nodes. It is rejected
	// for type=group and template= entries (those own their own close-on-exit
	// per node).
	CloseOnExit bool     `toml:"close_on_exit,omitempty"`
	Preview     []string `toml:"preview,omitempty"`
}

// WildcardConfig is one entry in the [[wildcards]] list: a glob pattern with
// an optional template and preview override. The list is scanned in
// declaration order; the first pattern matching the candidate's normalised
// path or base name wins.
type WildcardConfig struct {
	Pattern  string   `toml:"pattern"`
	Template string   `toml:"template,omitempty"`
	Preview  []string `toml:"preview,omitempty"`
}

// Duration wraps time.Duration so TOML string values ("150ms", "5s") parse
// via time.ParseDuration. time.Duration itself has no UnmarshalText, so
// go-toml v2 cannot decode into it directly.
type Duration time.Duration

// UnmarshalText parses a TOML duration string into a Duration.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// PreviewConfig configures the workspace preview rendered in the Bubble Tea
// selector and `shep preview <path>`. Default lists the section names shown
// when nothing more specific (workspace > wildcard > source > default)
// applies; Commands declares custom preview commands referenced by name from
// any `preview = [...]` list alongside the hardcoded built-ins.
type PreviewConfig struct {
	Timeout  Duration                  `toml:"timeout,omitempty"`
	CacheTTL Duration                  `toml:"cache_ttl,omitempty"`
	MaxLines int                       `toml:"max_lines,omitempty"`
	Default  []string                  `toml:"default,omitempty"`
	Commands map[string]PreviewCommand `toml:"commands,omitempty"`
}

// PreviewCommand is one [preview.commands.<name>] entry: a shell-style
// command with a {path} placeholder, executed safely (argv-parsed, no
// `sh -c`, timeout + line cap from the surrounding PreviewConfig).
type PreviewCommand struct {
	Command string `toml:"command"`
}

// TemplateFocus names which tab (and optionally which pane within it) should
// end up with keyboard focus after the template is fully applied. Tab refers
// to a [[templates.<name>.tabs]].name; Node refers to a TemplateNode.ID
// scoped to that same tab. Node may be empty (focus just the tab's root
// pane). The whole struct may be nil (apply the default: the first tab stays
// focused). Focus is resolved entirely at tab/pane creation time via the
// --focus/--no-focus flags; there is no post-hoc focus command.
type TemplateFocus struct {
	Tab  string `toml:"tab,omitempty"`
	Node string `toml:"node,omitempty"`
}

// TemplateConfig describes what opens after Enter for a freshly created
// workspace. A template uses either Command (a single command run directly
// in the root pane, empty string means a plain shell) or Tabs (a structured
// multi-tab/pane layout) — never both. Focus, when non-nil, names the tab
// (and optionally a pane within it) that receives keyboard focus after the
// layout is applied.
type TemplateConfig struct {
	Command     string         `toml:"command,omitempty"`
	Description string         `toml:"description,omitempty"`
	Tabs        []TemplateTab  `toml:"tabs,omitempty"`
	Focus       *TemplateFocus `toml:"focus,omitempty"`
	// CloseOnExit wraps Command (only the simple-Command case: no Tabs) so the
	// template's root pane closes itself after the command's shell returns
	// control (regardless of exit status), via the same shell-chaining used
	// by leaf nodes. It is rejected when Tabs is set (per-tab/per-pane
	// close-on-exit is already the node-level feature).
	CloseOnExit bool `toml:"close_on_exit,omitempty"`
}

// TemplateTab is one tab in a template, in creation order. Name is the tab's
// label (not the root node's id). Root names the TemplateNode that anchors
// this tab's layout tree; Nodes are scoped to this tab only. A tab with no
// Nodes is a plain single empty-shell tab. Which tab receives focus is
// declared once at the template level via TemplateConfig.Focus, not here.
type TemplateTab struct {
	Name  string         `toml:"name"`
	Root  string         `toml:"root,omitempty"`
	Nodes []TemplateNode `toml:"nodes,omitempty"`
}

// TemplateNode is one node in a tab's layout tree, identified by ID (unique
// within its tab). A branch node sets Split + Children (and optionally
// Sizes, parallel to Children); a leaf node sets Command instead (empty
// string means a plain shell pane) and leaves Split/Children/Sizes empty.
// CloseOnExit, when true on a leaf with a non-empty Command, wraps the
// command so the pane closes itself once the command's shell returns control
// (achieved via shell chaining because Herdr's pane run types into an
// already-live shell rather than spawning the command as the pane process).
type TemplateNode struct {
	ID          string   `toml:"id"`
	Split       string   `toml:"split,omitempty"`
	Children    []string `toml:"children,omitempty"`
	Sizes       []int    `toml:"sizes,omitempty"`
	Command     string   `toml:"command,omitempty"`
	CloseOnExit bool     `toml:"close_on_exit,omitempty"`
}

// IsBranch reports whether the node is a layout-only branch (Split set).
func (n TemplateNode) IsBranch() bool { return n.Split != "" }

// Probes records which optional binaries are available at startup. The
// source registry consults this to skip providers whose binary is missing.
type Probes struct {
	Herdr  bool
	Zoxide bool
	Git    bool
}

// Probe reports whether name resolves on PATH. It never returns an error:
// a missing binary is a normal, non-fatal state for shep.
func Probe(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// ProbesFor returns a Probes snapshot for the binaries shep cares about,
// honouring an optional herdr binary override from cfg.
func ProbesFor(cfg *Config) Probes {
	herdrBin := "herdr"
	if cfg != nil && cfg.Herdr.Binary != "" {
		herdrBin = cfg.Herdr.Binary
	}
	return Probes{
		Herdr:  Probe(herdrBin),
		Zoxide: Probe("zoxide"),
		Git:    Probe("git"),
	}
}

// Defaults returns a path-agnostic Config with every built-in source enabled
// and no user-specific roots. Absent config maps to this so shep works on a
// pristine machine without leaking developer paths into the shipped defaults.
func Defaults() *Config {
	cfg := &Config{
		Version:    1,
		General:    General{Sources: append([]string(nil), defaultSourceOrder...), Selector: SelectorBuiltin},
		Herdr:      Herdr{},
		Defaults:   DefaultsConfig{Type: WorkspaceTypeShell, Template: "default"},
		Sources:    SourcesConfig{},
		Workspaces: []WorkspaceConfig{},
		Templates:  map[string]TemplateConfig{"default": {Command: ""}},
		Wildcards:  []WildcardConfig{},
	}
	normalizePreview(&cfg.Preview)
	return cfg
}

// DiscoverPath returns the config file path. It prefers the XDG base directory
// ($XDG_CONFIG_HOME, or ~/.config when unset) on every platform so shep lives
// alongside other XDG-style tools, including on macOS. It falls back to
// os.UserConfigDir only when no home directory can be resolved. It never
// creates files; callers (init) are responsible for that.
func DiscoverPath() (string, error) {
	if dir := configHome(); dir != "" {
		return filepath.Join(dir, "shep", "config.toml"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(dir, "shep", "config.toml"), nil
}

// configHome resolves the XDG config base directory: $XDG_CONFIG_HOME when set
// to an absolute path, otherwise ~/.config. Returns "" when neither is
// available so the caller can fall back to os.UserConfigDir.
func configHome() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return xdg
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config")
}

// Load reads and parses the config at path. When path is empty, DiscoverPath
// is used. A missing file is not an error and yields Defaults; all other
// read/parse/validation failures are wrapped with their originating step.
func Load(path string) (*Config, error) {
	resolved := path
	if resolved == "" {
		p, err := DiscoverPath()
		if err != nil {
			return nil, err
		}
		resolved = p
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), nil
		}
		return nil, fmt.Errorf("read config %q: %w", resolved, err)
	}

	cfg := Defaults()
	// DisallowUnknownFields makes an unrecognised or legacy/removed key (a
	// typo'd field, a stale top-level table, an arbitrary [sources.<name>])
	// fail Load fast instead of silently ignoring it.
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", resolved, err)
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = []WorkspaceConfig{}
	}
	if cfg.Templates == nil {
		cfg.Templates = map[string]TemplateConfig{}
	}
	if cfg.Wildcards == nil {
		cfg.Wildcards = []WildcardConfig{}
	}
	if len(cfg.General.Sources) == 0 {
		cfg.General.Sources = append([]string(nil), defaultSourceOrder...)
	}
	if cfg.General.Selector == "" {
		cfg.General.Selector = SelectorBuiltin
	}
	normalizePreview(&cfg.Preview)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", resolved, err)
	}
	return cfg, nil
}

// normalizePreview fills zero-value durations and max_lines with the
// documented defaults. It runs even when [preview] is absent because those
// defaults are only meaningful once a custom command is configured.
func normalizePreview(p *PreviewConfig) {
	if p.Timeout == 0 {
		p.Timeout = Duration(defaultPreviewTimeout)
	}
	if p.CacheTTL == 0 {
		p.CacheTTL = Duration(defaultPreviewCacheTTL)
	}
	if p.MaxLines == 0 {
		p.MaxLines = defaultPreviewMaxLines
	}
}

// validate enforces every schema invariant that must fail Load fast rather
// than surface as a confusing runtime error later.
func validate(cfg *Config) error {
	if err := validateSources(cfg.General.Sources); err != nil {
		return err
	}
	if !isValidSelector(cfg.General.Selector) {
		return fmt.Errorf("invalid general.selector %q (valid: builtin, fzf, auto)", cfg.General.Selector)
	}
	if err := validateTemplates(cfg.Templates); err != nil {
		return err
	}
	if err := validateWorkspaces(cfg.Workspaces, cfg.Templates); err != nil {
		return err
	}
	if err := validateWildcards(cfg.Wildcards, cfg.Templates); err != nil {
		return err
	}
	if cfg.Defaults.Template != "" {
		if _, ok := cfg.Templates[cfg.Defaults.Template]; !ok {
			return fmt.Errorf("defaults.template %q: no such [templates.%s]", cfg.Defaults.Template, cfg.Defaults.Template)
		}
	}
	if err := validatePreview(cfg.Preview); err != nil {
		return err
	}
	if err := validateAllPreviewLists(cfg); err != nil {
		return err
	}
	if err := validateTUI(cfg.TUI); err != nil {
		return err
	}
	return nil
}

// validateAllPreviewLists validates every `preview = [...]` list found
// outside [preview] itself (sources, workspaces, wildcards) against the
// built-ins plus cfg.Preview.Commands, so a typo'd preview name fails Load
// fast no matter where it is declared.
func validateAllPreviewLists(cfg *Config) error {
	checks := []struct {
		label string
		names []string
	}{
		{"sources.herdr.preview", cfg.Sources.Herdr.Preview},
		{"sources.workspaces.preview", cfg.Sources.Workspaces.Preview},
		{"sources.zoxide.preview", cfg.Sources.Zoxide.Preview},
		{"sources.projects.preview", cfg.Sources.Projects.Preview},
	}
	for _, c := range checks {
		if err := ValidatePreviewNames(c.names, cfg.Preview.Commands); err != nil {
			return fmt.Errorf("%s: %w", c.label, err)
		}
	}
	for i, ws := range cfg.Workspaces {
		if err := ValidatePreviewNames(ws.Preview, cfg.Preview.Commands); err != nil {
			return fmt.Errorf("workspaces[%d] (%q).preview: %w", i, ws.Name, err)
		}
	}
	for i, w := range cfg.Wildcards {
		if err := ValidatePreviewNames(w.Preview, cfg.Preview.Commands); err != nil {
			return fmt.Errorf("wildcards[%d] (%q).preview: %w", i, w.Pattern, err)
		}
	}
	return nil
}

// validateSources rejects any name not in validSourceNames so a typo fails
// fast instead of silently disabling a source.
func validateSources(names []string) error {
	for _, n := range names {
		if !validSourceNames[n] {
			return fmt.Errorf("invalid general.sources entry %q (valid: %s, %s, %s, %s)",
				n, SourceHerdr, SourceWorkspaces, SourceZoxide, SourceProjects)
		}
	}
	return nil
}

// validateTemplates enforces that a template uses either Command or Tabs
// (never both), that every tab/node is internally consistent, and that the
// top-level Focus (when set) refers to a real tab name and (when Node is
// set) a real node id within that specific tab. It also enforces that
// close_on_exit, when set, is consistent with the template shape: rejected
// on a TemplateConfig that sets Tabs, or on a TemplateConfig with an empty
// Command.
func validateTemplates(templates map[string]TemplateConfig) error {
	for name, tpl := range templates {
		if tpl.Command != "" && len(tpl.Tabs) > 0 {
			return fmt.Errorf("templates.%s: sets both command and tabs; use one or the other", name)
		}
		if tpl.CloseOnExit {
			if len(tpl.Tabs) > 0 {
				return fmt.Errorf("templates.%s: sets close_on_exit but also has tabs (per-tab close-on-exit is the node-level feature)", name)
			}
			if tpl.Command == "" {
				return fmt.Errorf("templates.%s: sets close_on_exit but has no command; it would never trigger", name)
			}
		}
		for i, tab := range tpl.Tabs {
			if err := validateTemplateTab(name, i, tab); err != nil {
				return err
			}
		}
		if tpl.Focus != nil {
			if err := validateTemplateFocus(name, tpl); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateTemplateFocus resolves the top-level Focus against the template's
// declared tabs/nodes: Focus.Tab must match a [[templates.<name>.tabs]].name,
// and when Focus.Node is set it must match a node id within that exact tab
// (node ids are scoped per-tab, so a node id from a different tab is not a
// match). Both mismatches fail Load fast with a clear error so a typo in the
// focus target surfaces immediately instead of silently focusing the wrong
// (or no) tab/pane at apply time.
func validateTemplateFocus(name string, tpl TemplateConfig) error {
	focusTab := tpl.Focus.Tab
	if focusTab == "" {
		// Tab omitted but Focus non-nil: treat as "focus the first tab". We
		// only validate Node references when Tab is set, because Node is
		// scoped to a tab. An empty Tab with a Node is ambiguous and treated
		// as a config error.
		if tpl.Focus.Node != "" {
			return fmt.Errorf("templates.%s: focus.node %q set but focus.tab is empty", name, tpl.Focus.Node)
		}
		return nil
	}
	tabIdx := -1
	for i, tab := range tpl.Tabs {
		if tab.Name == focusTab {
			tabIdx = i
			break
		}
	}
	if tabIdx == -1 {
		return fmt.Errorf("templates.%s: focus.tab %q does not match any tab name", name, focusTab)
	}
	if tpl.Focus.Node != "" {
		tab := tpl.Tabs[tabIdx]
		found := false
		for _, n := range tab.Nodes {
			if n.ID == tpl.Focus.Node {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("templates.%s: focus.node %q does not match any node id in tab %q",
				name, tpl.Focus.Node, focusTab)
		}
	}
	return nil
}

// validateTemplateTab checks one tab: a name is required; node ids are
// unique within the tab; root (if the tab has nodes) must reference a real
// node; every branch node's children must exist and match sizes length when
// given; every leaf node must not carry split/children/sizes.
func validateTemplateTab(templateName string, idx int, tab TemplateTab) error {
	prefix := fmt.Sprintf("templates.%s.tabs[%d]", templateName, idx)
	if strings.TrimSpace(tab.Name) == "" {
		return fmt.Errorf("%s: name is required", prefix)
	}
	if len(tab.Nodes) == 0 {
		return nil
	}
	byID := make(map[string]TemplateNode, len(tab.Nodes))
	for _, n := range tab.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return fmt.Errorf("%s: node with empty id", prefix)
		}
		if _, dup := byID[n.ID]; dup {
			return fmt.Errorf("%s: duplicate node id %q", prefix, n.ID)
		}
		byID[n.ID] = n
	}
	if tab.Root == "" {
		return fmt.Errorf("%s (%q): root is required when nodes are declared", prefix, tab.Name)
	}
	if _, ok := byID[tab.Root]; !ok {
		return fmt.Errorf("%s (%q): root %q does not match any node id", prefix, tab.Name, tab.Root)
	}
	for _, n := range tab.Nodes {
		if err := validateTemplateNode(prefix, tab.Name, n, byID); err != nil {
			return err
		}
	}
	return detectTemplateCycle(prefix, tab.Name, tab.Root, byID)
}

// detectTemplateCycle walks the branch/children graph reachable from root
// and fails if any node is reachable from itself. Without this check a
// cyclic template would recurse forever in templates.Apply, repeatedly
// splitting/creating Herdr panes until the daemon or process resources are
// exhausted.
func detectTemplateCycle(prefix, tabName, root string, byID map[string]TemplateNode) error {
	const (
		unvisited = iota
		inProgress
		done
	)
	state := make(map[string]int, len(byID))
	var visit func(id string) error
	visit = func(id string) error {
		switch state[id] {
		case inProgress:
			return fmt.Errorf("%s (%q): node %q is part of a cycle", prefix, tabName, id)
		case done:
			return nil
		}
		state[id] = inProgress
		for _, childID := range byID[id].Children {
			if _, ok := byID[childID]; !ok {
				continue // unknown child already reported by validateTemplateNode
			}
			if err := visit(childID); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	return visit(root)
}

func validateTemplateNode(prefix, tabName string, n TemplateNode, byID map[string]TemplateNode) error {
	if n.IsBranch() {
		if n.Split != SplitRows && n.Split != SplitCols {
			return fmt.Errorf("%s (%q): node %q split %q must be %q or %q", prefix, tabName, n.ID, n.Split, SplitRows, SplitCols)
		}
		if len(n.Children) < 2 {
			return fmt.Errorf("%s (%q): node %q needs at least 2 children", prefix, tabName, n.ID)
		}
		if len(n.Sizes) > 0 && len(n.Sizes) != len(n.Children) {
			return fmt.Errorf("%s (%q): node %q has %d sizes for %d children", prefix, tabName, n.ID, len(n.Sizes), len(n.Children))
		}
		for _, sz := range n.Sizes {
			if sz <= 0 {
				return fmt.Errorf("%s (%q): node %q sizes must be positive, got %d", prefix, tabName, n.ID, sz)
			}
		}
		for _, childID := range n.Children {
			if _, ok := byID[childID]; !ok {
				return fmt.Errorf("%s (%q): node %q references unknown child %q", prefix, tabName, n.ID, childID)
			}
		}
		if n.Command != "" {
			return fmt.Errorf("%s (%q): node %q is a branch (split set) and cannot also set command", prefix, tabName, n.ID)
		}
		if n.CloseOnExit {
			return fmt.Errorf("%s (%q): node %q is a branch (split set) and cannot also set close_on_exit", prefix, tabName, n.ID)
		}
		return nil
	}
	if len(n.Children) > 0 || len(n.Sizes) > 0 {
		return fmt.Errorf("%s (%q): node %q is a leaf (no split) and cannot set children/sizes", prefix, tabName, n.ID)
	}
	if n.CloseOnExit && n.Command == "" {
		return fmt.Errorf("%s (%q): node %q sets close_on_exit but has no command; it would never trigger", prefix, tabName, n.ID)
	}
	return nil
}

// validateWorkspaces enforces per-entry invariants: a name is required;
// group entries validate their Sources list; a template reference (if set)
// must exist. It also enforces that close_on_exit, when set, is consistent
// with the workspace shape: rejected on a Type="group" workspace, on a
// workspace that sets a Template, or on a workspace with an empty Command.
func validateWorkspaces(workspaces []WorkspaceConfig, templates map[string]TemplateConfig) error {
	for i, ws := range workspaces {
		if strings.TrimSpace(ws.Name) == "" {
			return fmt.Errorf("workspaces[%d]: name is required", i)
		}
		switch ws.Type {
		case "", WorkspaceTypeShell, WorkspaceTypeGroup:
		default:
			return fmt.Errorf("workspaces[%d] (%q): invalid type %q (valid: %s, %s)", i, ws.Name, ws.Type, WorkspaceTypeShell, WorkspaceTypeGroup)
		}
		if ws.Type == WorkspaceTypeGroup {
			if err := validateSources(ws.Sources); err != nil {
				return fmt.Errorf("workspaces[%d] (%q): %w", i, ws.Name, err)
			}
		}
		if ws.Template != "" {
			if _, ok := templates[ws.Template]; !ok {
				return fmt.Errorf("workspaces[%d] (%q): template %q: no such [templates.%s]", i, ws.Name, ws.Template, ws.Template)
			}
		}
		if ws.Template != "" && ws.Command != "" {
			return fmt.Errorf("workspaces[%d] (%q): sets both template and command; use one or the other", i, ws.Name)
		}
		if ws.CloseOnExit {
			if ws.Type == WorkspaceTypeGroup {
				return fmt.Errorf("workspaces[%d] (%q): sets close_on_exit but type=%q (a group has no pane/command of its own)", i, ws.Name, ws.Type)
			}
			if ws.Template != "" {
				return fmt.Errorf("workspaces[%d] (%q): sets close_on_exit but template=%q (the template owns close-on-exit per node)", i, ws.Name, ws.Template)
			}
			if ws.Command == "" {
				return fmt.Errorf("workspaces[%d] (%q): sets close_on_exit but has no command; it would never trigger", i, ws.Name)
			}
		}
	}
	return nil
}

// validateWildcards enforces that Pattern is set and any Template reference
// exists.
func validateWildcards(wildcards []WildcardConfig, templates map[string]TemplateConfig) error {
	for i, w := range wildcards {
		if strings.TrimSpace(w.Pattern) == "" {
			return fmt.Errorf("wildcards[%d]: pattern is required", i)
		}
		if w.Template != "" {
			if _, ok := templates[w.Template]; !ok {
				return fmt.Errorf("wildcards[%d] (%q): template %q: no such [templates.%s]", i, w.Pattern, w.Template, w.Template)
			}
		}
	}
	return nil
}

// validatePreview enforces the preview schema invariants the renderer relies
// on: max_lines/timeout/cache_ttl are non-negative, and every name in
// default (or in a workspace/source/wildcard preview list, validated by
// their own callers) is a built-in or a declared command.
func validatePreview(p PreviewConfig) error {
	if p.MaxLines < 0 {
		return fmt.Errorf("preview.max_lines must be >= 0, got %d", p.MaxLines)
	}
	if p.Timeout < 0 {
		return fmt.Errorf("preview.timeout must be >= 0, got %v", time.Duration(p.Timeout))
	}
	if p.CacheTTL < 0 {
		return fmt.Errorf("preview.cache_ttl must be >= 0, got %v", time.Duration(p.CacheTTL))
	}
	for _, name := range p.Default {
		if !isValidPreviewName(name, p.Commands) {
			return fmt.Errorf("preview.default: %q is not a built-in preview or a declared preview.commands entry", name)
		}
	}
	return nil
}

// isValidPreviewName reports whether name is a hardcoded built-in or a key
// in the commands map. Used to validate every `preview = [...]` list found
// throughout the config (global default, sources, workspaces, wildcards).
func isValidPreviewName(name string, commands map[string]PreviewCommand) bool {
	if builtinPreviewNames[name] {
		return true
	}
	_, ok := commands[name]
	return ok
}

// ValidatePreviewNames validates an arbitrary `preview = [...]` list (from a
// source, workspace or wildcard) against the built-ins plus cfg's declared
// commands. Exported so callers assembling ad-hoc PreviewConfig-less checks
// (e.g. tests) can reuse the same rule.
func ValidatePreviewNames(names []string, commands map[string]PreviewCommand) error {
	for _, n := range names {
		if !isValidPreviewName(n, commands) {
			return fmt.Errorf("%q is not a built-in preview or a declared preview.commands entry", n)
		}
	}
	return nil
}

// validateTUI enforces that list_width/preview_width are "auto" or a valid
// percentage string, and that configuring both as percentages never sums
// past 100% — a picker pane split wider than the terminal, which would
// overflow the rendered layout.
func validateTUI(t TUIConfig) error {
	if err := validateWidthField("tui.list_width", t.ListWidth); err != nil {
		return err
	}
	if err := validateWidthField("tui.preview_width", t.PreviewWidth); err != nil {
		return err
	}
	listFrac, listOK := PercentOrAuto(t.ListWidth)
	prevFrac, prevOK := PercentOrAuto(t.PreviewWidth)
	if listOK && prevOK && listFrac+prevFrac > 1 {
		return fmt.Errorf("tui.list_width (%s) + tui.preview_width (%s) must not exceed 100%%",
			t.ListWidth, t.PreviewWidth)
	}
	if err := validateTUILayout(t.Layout); err != nil {
		return err
	}
	return nil
}

// validateTUILayout enforces that [tui].layout is empty (defaults to
// landscape) or one of the documented orientation values, failing Load fast
// with the bad value named in the error — consistent with validateWidthField
// above.
func validateTUILayout(layout string) error {
	switch layout {
	case "", TUILayoutLandscape, TUILayoutPortrait:
		return nil
	}
	return fmt.Errorf("tui.layout: %q must be %q or %q", layout, TUILayoutLandscape, TUILayoutPortrait)
}

func validateWidthField(field, value string) error {
	if value == "" || value == "auto" {
		return nil
	}
	if _, ok := ParsePercent(value); !ok {
		return fmt.Errorf("%s: %q must be \"auto\" or a percentage like \"60%%\"", field, value)
	}
	return nil
}

// PercentOrAuto returns the fraction for a configured percentage width, or
// ok=false for "" / "auto" (not a percentage). Reuses ParsePercent so an
// already-invalid value (rejected by validateWidthField above) never reaches
// here as ok=true. Shared with internal/tui, which applies the same
// "" / "auto"-means-unconfigured rule when splitting pane widths.
func PercentOrAuto(s string) (float64, bool) {
	if s == "" || s == "auto" {
		return 0, false
	}
	return ParsePercent(s)
}

// ParsePercent parses a percentage string like "60%" into a 0..1 fraction.
// ok is false when s is not a valid percentage (missing "%" suffix,
// unparseable number, or out of the [0, 100] range).
func ParsePercent(s string) (float64, bool) {
	if !strings.HasSuffix(s, "%") {
		return 0, false
	}
	numStr := strings.TrimSuffix(s, "%")
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil || n < 0 || n > 100 {
		return 0, false
	}
	return n / 100, true
}

// MatchWildcard reports whether path matches a [[wildcards]] pattern. A
// leading "~" in pattern expands to the user's home directory so documented
// patterns like "~/projects/kubernetes/**" compare correctly against
// resolved candidate paths; "**" matches zero or more whole path segments
// (recursive descent) the way it is documented, while any other segment
// keeps filepath.Match semantics (single-segment globbing). A malformed
// pattern is treated as "no match" rather than an error, so a bad glob in
// config never crashes resolution.
func MatchWildcard(pattern, path string) bool {
	if pattern == "" || path == "" {
		return false
	}
	// Expand a leading "~" once, here, via the shared leaf helper. A malformed
	// pattern later degrades to "no match"; an unresolvable HOME is treated the
	// same way (pattern left untouched) so resolution never crashes.
	if expanded, err := pathutil.ExpandTilde(pattern); err == nil {
		pattern = expanded
	}
	patSegs := strings.Split(filepath.ToSlash(pattern), "/")
	pathSegs := strings.Split(filepath.ToSlash(path), "/")
	return matchWildcardSegments(patSegs, pathSegs)
}

// matchWildcardSegments recursively matches pattern segments against path
// segments. "**" may match zero or more segments; any other pattern segment
// matches exactly one path segment via filepath.Match.
func matchWildcardSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		if matchWildcardSegments(pat[1:], path) {
			return true
		}
		if len(path) == 0 {
			return false
		}
		return matchWildcardSegments(pat, path[1:])
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchWildcardSegments(pat[1:], path[1:])
}

// isValidSelector reports whether s is one of the supported selector values.
func isValidSelector(s string) bool {
	switch s {
	case SelectorBuiltin, SelectorFzf, SelectorAuto:
		return true
	}
	return false
}

// HerdrBinary returns the configured herdr binary name, defaulting to "herdr".
func (c *Config) HerdrBinary() string {
	if c.Herdr.Binary != "" {
		return c.Herdr.Binary
	}
	return "herdr"
}
