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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/tmpl"
	"github.com/tranceh2/shep/internal/workspacename"
)

// Built-in source names. general.source_order lists which of these are enabled
// and in what merge/display order. Declared custom source names extend this set
// for the current config document; unknown names still fail Load fast.
const (
	SourceHerdr      = "herdr"
	SourceSessions   = "sessions"
	SourceWorkspaces = "workspaces"
	SourceZoxide     = "zoxide"
	SourceProjects   = "projects"
	SourceAgents     = "agents"
)

// defaultSourceOrder is used when general.source_order is empty/absent.
var defaultSourceOrder = []string{SourceHerdr, SourceWorkspaces, SourceZoxide, SourceProjects}

const CurrentSchemaVersion = 2

var validSourceNames = map[string]bool{
	SourceHerdr: true, SourceSessions: true, SourceWorkspaces: true, SourceZoxide: true, SourceProjects: true, SourceAgents: true,
}

const defaultCustomSourceTimeout = 3 * time.Second

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
	PreviewSessionInfo = "session_info"
	PreviewActivePane  = "active_pane"
	PreviewDir         = "dir"
	PreviewAgentStatus = "agent_status"
)

var builtinPreviewNames = map[string]bool{
	PreviewIdentity: true, PreviewGit: true, PreviewWorkspace: true, PreviewSessionInfo: true,
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

// defaultPreviewSections is the generic fallback for a candidate whose source
// declares no list of its own — a direct --path candidate or a custom source
// without preview sections.
var defaultPreviewSections = []string{PreviewAgentStatus, PreviewIdentity, PreviewGit}

// Per-source preview defaults. Each built-in source gets the sections that
// actually describe its candidates, because the generic list wastes the pane on
// sections that render nothing for that kind of row: a Herdr workspace has tabs,
// panes and an agent to show and no git status worth repeating, while a zoxide
// directory is usually not a repository at all but always has contents.
var (
	defaultHerdrPreview      = []string{PreviewWorkspace, PreviewActivePane, PreviewAgentStatus}
	defaultWorkspacesPreview = []string{PreviewIdentity, PreviewDir}
	defaultZoxidePreview     = []string{PreviewIdentity, PreviewDir}
	defaultProjectsPreview   = []string{PreviewIdentity, PreviewGit, PreviewDir}
)

const (
	defaultTUIListWidth    = "35%"
	defaultTUIPreviewWidth = "65%"
)

// Config is the top-level shep configuration document.
type Config struct {
	Version  int            `toml:"version,omitempty"`
	General  General        `toml:"general,omitempty"`
	Herdr    Herdr          `toml:"herdr,omitempty"`
	Defaults DefaultsConfig `toml:"defaults,omitempty"`
	TUI      TUIConfig      `toml:"tui,omitempty"`
	Preview  PreviewConfig  `toml:"preview,omitempty"`
	Ranking  RankingConfig  `toml:"ranking,omitempty"`
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
	// Themes declares custom color themes, keyed by name and selected with
	// [tui].theme (see CustomThemes for the table's shape).
	Themes map[string]ThemeTable `toml:"themes,omitempty"`
}

// RankingConfig controls local adaptive candidate ranking. Disabled mode must
// avoid opening or touching ranking state entirely.
type RankingConfig struct {
	Enabled bool `toml:"enabled,omitempty"`
}

// General holds global tweaks. SourceOrder lists enabled built-in and declared
// custom source names in merge/display order; Selector picks the interactive
// picker for `shep open` after the direct match. WorkspaceName applies only to
// newly created dynamic workspaces.
type General struct {
	SourceOrder   []string `toml:"source_order,omitempty"`
	Selector      string   `toml:"selector,omitempty"`
	WorkspaceName string   `toml:"workspace_name,omitempty"`
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
// "60%"; see ParsePercent. Layout is TUILayoutLandscape (default when empty
// means auto-responsive) — the only orientation the picker resolves to a
// distinct mode now (it forces side-by-side wide mode). The stacked "portrait"
// layout was removed and is rejected at validation.
type TUIConfig struct {
	Tabs []string `toml:"tabs,omitempty"`
	// ConfirmClose opts into a footer confirmation for selected open Herdr kinds.
	// Empty or absent closes immediately.
	ConfirmClose []string `toml:"confirm_close,omitempty"`
	ListWidth    string   `toml:"list_width,omitempty"`
	PreviewWidth string   `toml:"preview_width,omitempty"`
	Layout       string   `toml:"layout,omitempty"`
	// Theme names the picker's color theme: "inherit" (the default, also
	// when empty: Herdr's own theme, [theme.custom] included), a built-in
	// theme name or alias (Herdr's themes plus catppuccin-frappe and
	// catppuccin-macchiato), "plain" (no color) or a [themes.<name>] table.
	// $NO_COLOR and $SHEP_THEME, when set, win over it (see theme.Select).
	Theme string `toml:"theme,omitempty"`
	// Icons selects the glyph tier: one of "unicode" (plain Unicode symbols,
	// safe on any UTF-8 terminal, plus Nerd Font source icons) or "ascii"
	// (7-bit ASCII only, for terminals/locales that cannot render Unicode).
	// Empty defaults to "unicode". It selects the picker's own glyphs (status,
	// pin, group, tree, chrome — see internal/tui/icons.go) and the default
	// row icons (see presentationDefaults); an icon configured in
	// [sources.<name>] is the user's own template and is drawn as written.
	Icons string `toml:"icons,omitempty"`
}

// TUI icon fallback tier names for [tui].icons, mirrored in
// internal/tui/icons.go's IconsUnicode/IconsASCII constants so config
// validation and the TUI resolve the exact same set without an import cycle
// (config cannot import tui). The "nerd" tier was removed (only unicode and
// ascii remain); validateTUI rejects it explicitly rather than silently
// falling back.
const (
	TUIIconsUnicode = "unicode"
	TUIIconsASCII   = "ascii"
)

var validTUIIcons = map[string]bool{
	TUIIconsUnicode: true, TUIIconsASCII: true,
}

// TUI layout orientation values for [tui].layout. TUILayoutLandscape (empty/
// default means auto-responsive) is the only orientation the picker resolves
// to a distinct mode now: it forces side-by-side wide mode (still subject to
// the terminal-height floor). The stacked "portrait" layout was removed (only
// wide and list-only modes remain), so [tui].layout = "portrait" is rejected
// at validation.
const TUILayoutLandscape = "landscape"

// SourcesConfig configures the built-in providers and command-backed custom sources.
// Only the named built-in tables and [[sources.custom]] entries are recognised.
type SourcesConfig struct {
	Herdr      HerdrSourceConfig      `toml:"herdr,omitempty"`
	Sessions   SessionsSourceConfig   `toml:"sessions,omitempty"`
	Workspaces WorkspacesSourceConfig `toml:"workspaces,omitempty"`
	Zoxide     ZoxideSourceConfig     `toml:"zoxide,omitempty"`
	Projects   ProjectsSourceConfig   `toml:"projects,omitempty"`
	Agents     AgentsSourceConfig     `toml:"agents,omitempty"`
	Custom     []CustomSourceConfig   `toml:"custom,omitempty"`
}

// AgentsSourceConfig configures the agents source's presentation.
type AgentsSourceConfig struct {
	Presentation
	Preview []string `toml:"preview,omitempty"`
}

// HerdrSourceConfig configures the herdr workspaces source's presentation:
// the workspace rows themselves, and the tab and pane rows nested under an
// open workspace ([sources.herdr.tab] and [sources.herdr.pane]).
type HerdrSourceConfig struct {
	Presentation
	Tab     Presentation `toml:"tab,omitempty"`
	Pane    Presentation `toml:"pane,omitempty"`
	Preview []string     `toml:"preview,omitempty"`
}

// SessionsSourceConfig configures the opt-in Herdr sessions source's
// presentation. Session rows never imply a filesystem path.
type SessionsSourceConfig struct {
	Presentation
	Preview []string `toml:"preview,omitempty"`
}

// WorkspacesSourceConfig configures the predefined-[[workspaces]] source's
// presentation.
type WorkspacesSourceConfig struct {
	Presentation
	Preview []string `toml:"preview,omitempty"`
}

// ZoxideSourceConfig configures the zoxide source's presentation.
type ZoxideSourceConfig struct {
	Presentation
	Preview []string `toml:"preview,omitempty"`
}

// ProjectsSourceConfig configures the projects source: directories detected
// because they contain any configured marker (a file OR a directory name),
// discovered recursively up to MaxDepth beneath configured roots or a group
// entry's own path.
type ProjectsSourceConfig struct {
	Presentation
	Roots     []string `toml:"roots,omitempty"`
	Recursive bool     `toml:"recursive,omitempty"`
	MaxDepth  int      `toml:"max_depth,omitempty"`
	Markers   []string `toml:"markers,omitempty"`
	Ignore    []string `toml:"ignore,omitempty"`
	Preview   []string `toml:"preview,omitempty"`
}

// CustomSourceConfig declares one external argv-only JSON source. Each command
// must write a JSON array of row objects; see the user-facing config example
// for the accepted row fields.
type CustomSourceConfig struct {
	Name    string   `toml:"name"`
	Command []string `toml:"command"`
	Presentation
	Timeout         Duration                              `toml:"timeout,omitempty"`
	Aliases         []string                              `toml:"aliases,omitempty"`
	Preview         []string                              `toml:"preview,omitempty"`
	PreviewCommands map[string]CustomSourcePreviewCommand `toml:"preview_commands,omitempty"`
}

// CustomSourcePreviewCommand is a private preview command belonging to one
// custom source. Command is argv, not a shell string. Zero timeout/max_lines
// values inherit the normalized global [preview] defaults during Load.
type CustomSourcePreviewCommand struct {
	Command  []string `toml:"command"`
	Timeout  Duration `toml:"timeout,omitempty"`
	MaxLines int      `toml:"max_lines,omitempty"`
}

// ProjectsSourceOverride contains optional group-local project settings. A
// pointer distinguishes omission from an explicit false, zero, or empty list.
type ProjectsSourceOverride struct {
	Recursive *bool     `toml:"recursive,omitempty"`
	MaxDepth  *int      `toml:"max_depth,omitempty"`
	Markers   *[]string `toml:"markers,omitempty"`
	Ignore    *[]string `toml:"ignore,omitempty"`
	Preview   *[]string `toml:"preview,omitempty"`
}

// WorkspaceSourcesConfig reserves structured provider settings below a group
// workspace's sources table.
type WorkspaceSourcesConfig struct {
	Projects *ProjectsSourceOverride `toml:"projects,omitempty"`
}

// WorkspaceConfig is one entry in the [[workspaces]] list. A plain entry
// (Type empty or "shell") is a single project candidate; Type "group" turns
// the entry into a nested picker source rooted at Path, drawing candidates
// from its SourceOrder and typed Sources settings.
type WorkspaceConfig struct {
	Name string `toml:"name"`
	// ID is a stable, unique tab reference for a group workspace.
	ID string `toml:"id,omitempty"`
	// Path is the project (or group root) path. A leading "~/" is expanded
	// to the user's home directory by the source provider.
	Path string `toml:"path,omitempty"`
	// Type is "" / "shell" for a single workspace, or "group" for a nested
	// picker source.
	Type string `toml:"type,omitempty"`
	// SourceOrder lists the built-in source names a group entry draws from.
	// Only meaningful when Type == "group".
	SourceOrder []string `toml:"source_order,omitempty"`
	// Sources contains structured settings for group-local providers.
	Sources WorkspaceSourcesConfig `toml:"sources,omitempty"`
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
	Aliases     []string `toml:"aliases,omitempty"`
	Preview     []string `toml:"preview,omitempty"`
}

// WildcardConfig is one entry in the [[wildcards]] list: a glob pattern with
// optional workspace-name, template, and preview overrides. The list is scanned
// in declaration order; the first pattern matching the candidate's normalised
// path or base name wins.
type WildcardConfig struct {
	Pattern       string   `toml:"pattern"`
	Template      string   `toml:"template,omitempty"`
	WorkspaceName string   `toml:"workspace_name,omitempty"`
	Preview       []string `toml:"preview,omitempty"`
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
// when nothing more specific (workspace > wildcard > source) applies; a source
// may provide its own last-resort fallback after Default. Commands declares
// custom preview commands referenced by name from any `preview = [...]` list
// alongside the hardcoded built-ins.
type PreviewConfig struct {
	Timeout  Duration                  `toml:"timeout,omitempty"`
	CacheTTL Duration                  `toml:"cache_ttl,omitempty"`
	MaxLines int                       `toml:"max_lines,omitempty"`
	Default  []string                  `toml:"default,omitempty"`
	Commands map[string]PreviewCommand `toml:"commands,omitempty"`
}

// PreviewCommand is one [preview.commands.<name>] entry: a shell-style
// command with template actions such as {{.Path}}, executed safely
// (argv-parsed, no
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
// (achieved via shell chaining with herdr pane close).
type TemplateNode struct {
	ID          string   `toml:"id"`
	Split       string   `toml:"split,omitempty"`
	Children    []string `toml:"children,omitempty"`
	Sizes       []int    `toml:"sizes,omitempty"`
	Command     string   `toml:"command,omitempty"`
	CloseOnExit bool     `toml:"close_on_exit,omitempty"`
	Label       *string  `toml:"label,omitempty"`
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

// ValidBinaryPath reports whether path names an executable regular file.
// It intentionally requires an absolute path because absolute paths supplied by
// the Herdr plugin are authoritative, while relative values remain subject to
// the normal PATH/configuration rules.
func ValidBinaryPath(path string) bool {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// Probe reports whether name resolves on PATH. It never returns an error:
// a missing binary is a normal, non-fatal state for shep.
func Probe(name string) bool {
	return probeWith(name, exec.LookPath)
}

func probeWith(name string, lookPath func(string) (string, error)) bool {
	if name == "" || lookPath == nil {
		return false
	}
	_, err := lookPath(name)
	return err == nil
}

// HerdrBinaryWith resolves the effective Herdr executable. A configured binary
// wins when it is usable, either as a valid absolute executable or through the
// normal PATH lookup. Otherwise a valid absolute HERDR_BIN_PATH is preferred,
// followed by the configured value for a command-boundary error, and finally
// the default PATH name.
func HerdrBinaryWith(cfg *Config, lookupEnv func(string) (string, bool), lookPath func(string) (string, error)) string {
	configured := ""
	if cfg != nil {
		configured = cfg.Herdr.Binary
	}
	if configured != "" && (ValidBinaryPath(configured) || probeWith(configured, lookPath)) {
		return configured
	}
	if lookupEnv != nil {
		if candidate, ok := lookupEnv("HERDR_BIN_PATH"); ok && ValidBinaryPath(candidate) {
			return candidate
		}
	}
	if configured != "" {
		return configured
	}
	return "herdr"
}

// HerdrBinaryWithEnv resolves the effective Herdr executable using the
// production PATH lookup seam.
func HerdrBinaryWithEnv(cfg *Config, lookupEnv func(string) (string, bool)) string {
	return HerdrBinaryWith(cfg, lookupEnv, exec.LookPath)
}

// ProbesFor returns a Probes snapshot for the binaries shep cares about,
// using the same Herdr resolution as the driver.
func ProbesFor(cfg *Config) Probes {
	return ProbesForWith(cfg, os.LookupEnv, exec.LookPath)
}

// ProbesForWith is the hermetic form of ProbesFor. A valid absolute
// HERDR_BIN_PATH enables the Herdr provider even when that binary is absent
// from PATH, but only after an unusable configured binary has been checked.
func ProbesForWith(cfg *Config, lookupEnv func(string) (string, bool), lookPath func(string) (string, error)) Probes {
	herdrBin := HerdrBinaryWith(cfg, lookupEnv, lookPath)
	herdrAvailable := ValidBinaryPath(herdrBin) || probeWith(herdrBin, lookPath)

	return Probes{
		Herdr:  herdrAvailable,
		Zoxide: probeWith("zoxide", lookPath),
		Git:    probeWith("git", lookPath),
	}
}

// Defaults returns a path-agnostic Config with every built-in source enabled
// and no user-specific roots. Absent config maps to this so shep works on a
// pristine machine without leaking developer paths into the shipped defaults.
func Defaults() *Config {
	cfg := &Config{
		Version:    CurrentSchemaVersion,
		General:    General{SourceOrder: append([]string(nil), defaultSourceOrder...), Selector: SelectorBuiltin},
		Herdr:      Herdr{},
		Ranking:    RankingConfig{Enabled: true},
		Defaults:   DefaultsConfig{Type: WorkspaceTypeShell, Template: "default"},
		Sources:    SourcesConfig{Custom: []CustomSourceConfig{}},
		Workspaces: []WorkspaceConfig{},
		Templates:  map[string]TemplateConfig{"default": {Command: ""}},
		Wildcards:  []WildcardConfig{},
	}
	normalizeTUI(&cfg.TUI)
	normalizePreview(&cfg.Preview)
	normalizePresentations(&cfg.Sources, cfg.TUI.Icons)
	// No document exists here, so no preview.default was ever written.
	normalizeSourcePreviews(&cfg.Sources, false)
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
	// The pane split is decided by normalizeTUI below, after decoding, because
	// the two widths are validated as a pair. Clearing the pre-seeded defaults
	// first is what lets normalizeTUI see which sides the document actually
	// names; otherwise a config naming one side would be silently paired with a
	// default it never asked for and rejected for exceeding 100%.
	cfg.TUI.ListWidth = ""
	cfg.TUI.PreviewWidth = ""
	// Same reason: the normalizers below must see what the document itself says,
	// and Defaults() has already supplied these. Clearing them keeps nil meaning
	// "absent from the document" through decoding, which is what lets an explicit
	// preview.default suppress the per-source lists, an explicit `= []` survive,
	// and an explicit empty presentation part ("") stay distinct from an unset
	// one whose default depends on the document's [tui].icons.
	cfg.Preview.Default = nil
	cfg.Sources = SourcesConfig{}
	// DisallowUnknownFields makes an unrecognised or legacy/removed key (a
	// typo'd field, a stale top-level table, an arbitrary [sources.<name>])
	// fail Load fast instead of silently ignoring it.
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		if strings.Contains(err.Error(), "strict mode") && bytes.Contains(data, []byte("[[integrations]]")) {
			return nil, fmt.Errorf("parse config %q: [[integrations]] was removed; declare command-backed sources under [[sources.custom]]: %w", resolved, err)
		}
		return nil, fmt.Errorf("parse config %q: %w", resolved, err)
	}
	if cfg.Version != CurrentSchemaVersion {
		if cfg.Version < CurrentSchemaVersion {
			return nil, fmt.Errorf("config schema version %d is no longer supported; migrate to version = %d (rename [general].sources to source_order and use [workspaces.sources.<name>] for group overrides)", cfg.Version, CurrentSchemaVersion)
		}
		return nil, fmt.Errorf("config schema version %d is newer than supported version %d", cfg.Version, CurrentSchemaVersion)
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
	if cfg.Sources.Custom == nil {
		cfg.Sources.Custom = []CustomSourceConfig{}
	}
	if len(cfg.General.SourceOrder) == 0 {
		cfg.General.SourceOrder = append([]string(nil), defaultSourceOrder...)
	}
	if cfg.General.Selector == "" {
		cfg.General.Selector = SelectorBuiltin
	}
	// Captured before normalizePreview supplies the built-in list, because that
	// is the only moment the document's own intent is still visible.
	userSetPreviewDefault := cfg.Preview.Default != nil
	normalizeTUI(&cfg.TUI)
	normalizePreview(&cfg.Preview)
	normalizePresentations(&cfg.Sources, cfg.TUI.Icons)
	normalizeSourcePreviews(&cfg.Sources, userSetPreviewDefault)
	normalizeAliases(cfg)
	normalizeCustomSources(cfg.Sources.Custom, cfg.Preview)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", resolved, err)
	}
	return cfg, nil
}

// normalizeAliases trims aliases, drops empty values, and removes duplicate
// aliases case-insensitively while preserving first-declaration order. Aliases
// are discovery terms only; they do not participate in candidate identity.
func normalizeAliases(cfg *Config) {
	normalize := func(aliases []string) []string {
		seen := make(map[string]struct{}, len(aliases))
		out := make([]string, 0, len(aliases))
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			if strings.IndexFunc(alias, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
				continue
			}
			key := strings.ToLower(alias)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, alias)
		}
		return out
	}
	for i := range cfg.Workspaces {
		cfg.Workspaces[i].Aliases = normalize(cfg.Workspaces[i].Aliases)
	}
	for i := range cfg.Sources.Custom {
		cfg.Sources.Custom[i].Aliases = normalize(cfg.Sources.Custom[i].Aliases)
	}
}

// normalizeCustomSources fills each declared custom source's zero-value timeout
// and its preview commands' limits with the documented defaults, mirroring
// normalizePreview's fill-in-defaults contract for the sibling [preview]
// table. Presentations are filled by normalizePresentations.
func normalizeCustomSources(customSources []CustomSourceConfig, preview PreviewConfig) {
	for i := range customSources {
		if customSources[i].Timeout == 0 {
			customSources[i].Timeout = Duration(defaultCustomSourceTimeout)
		}
		for name, command := range customSources[i].PreviewCommands {
			if command.Timeout == 0 {
				command.Timeout = preview.Timeout
			}
			if command.MaxLines == 0 {
				command.MaxLines = preview.MaxLines
			}
			customSources[i].PreviewCommands[name] = command
		}
	}
}

// normalizeTUI fills the picker's pane split when the user left it unset.
//
// The two widths are one decision, not two independent fields, because
// validateTUI rejects a pair summing past 100%. Defaulting them separately
// would make a previously valid `list_width = "50%"` fail against a 65%
// default the user never asked for, so a config that names either side keeps
// that side and leaves the other to the renderer's own split. Only a config
// that names neither receives the built-in 35/65 pair.
func normalizeTUI(t *TUIConfig) {
	if len(t.Tabs) == 0 {
		t.Tabs = []string{"all", "agents"}
	}
	if t.ListWidth == "" && t.PreviewWidth == "" {
		t.ListWidth = defaultTUIListWidth
		t.PreviewWidth = defaultTUIPreviewWidth
	}
}

// normalizePreview fills zero-value preview fields with documented defaults.
// It runs even when [preview] is absent so a partial config receives the same
// first-run behavior as Defaults().
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
	// nil means the document never mentioned preview.default; an explicit
	// `default = []` decodes to an empty non-nil slice and is a deliberate
	// request for no default sections, so it must survive normalization. Testing
	// length instead of nil would silently overwrite that choice — the same
	// defect this package just fixed for sources.sessions.preview.
	if p.Default == nil {
		p.Default = append([]string(nil), defaultPreviewSections...)
	}
}

// normalizeSourcePreviews fills each built-in source's preview list when the
// document names none.
//
// nil means the key was absent; an explicit `preview = []` decodes to an empty
// non-nil slice and is a deliberate request for no sections, so it survives —
// the same distinction preview.default relies on. Sessions is deliberately
// absent here: its fallback lives in the renderer so that a user-set
// preview.default is not shadowed by a value normalization invented.
//
// userSetDefault suppresses these built-in lists entirely. Since a per-source
// list outranks preview.default in the renderer, filling them would make an
// explicitly written preview.default invisible for the four sources the picker
// actually shows — the same silent surprise this package removed for
// sources.sessions.preview. So a document that writes preview.default keeps one
// obvious control, while a document that writes neither gets per-source lists
// that suit each kind of row. An explicit [sources.<name>].preview still wins
// over both.
func normalizeSourcePreviews(s *SourcesConfig, userSetDefault bool) {
	if userSetDefault {
		return
	}
	if s.Herdr.Preview == nil {
		s.Herdr.Preview = append([]string(nil), defaultHerdrPreview...)
	}
	if s.Workspaces.Preview == nil {
		s.Workspaces.Preview = append([]string(nil), defaultWorkspacesPreview...)
	}
	if s.Zoxide.Preview == nil {
		s.Zoxide.Preview = append([]string(nil), defaultZoxidePreview...)
	}
	if s.Projects.Preview == nil {
		s.Projects.Preview = append([]string(nil), defaultProjectsPreview...)
	}
}

// validate enforces every schema invariant that must fail Load fast rather
// than surface as a confusing runtime error later.
func validate(cfg *Config) error {
	// Templates are validated with a representative home so the result never
	// depends on the real $HOME of whoever loads the file.
	engine := tmpl.New(tmpl.SampleHome)
	if err := validateWorkspaceNames(cfg, engine); err != nil {
		return err
	}
	if err := validateCustomSources(cfg.Sources.Custom, engine); err != nil {
		return err
	}
	if err := validateSources(cfg.General.SourceOrder, cfg.Sources.Custom); err != nil {
		return err
	}
	if err := validatePresentations(cfg.Sources, engine); err != nil {
		return err
	}
	if !isValidSelector(cfg.General.Selector) {
		return fmt.Errorf("invalid general.selector %q (valid: builtin, fzf, auto)", cfg.General.Selector)
	}
	if err := validateTemplates(cfg.Templates); err != nil {
		return err
	}
	if err := validateWorkspaces(cfg.Workspaces, cfg.Templates, cfg.Sources.Custom); err != nil {
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
	if err := validatePreviewCommandTemplates(cfg.Preview.Commands, engine); err != nil {
		return err
	}
	for i, customSource := range cfg.Sources.Custom {
		if err := validateCustomSourcePreviewCommands(customSource, i, cfg.Preview.Commands, engine); err != nil {
			return err
		}
	}
	if err := validateAllPreviewLists(cfg); err != nil {
		return err
	}
	if err := validateTUI(cfg.TUI); err != nil {
		return err
	}
	if err := validateThemes(cfg); err != nil {
		return err
	}
	if err := validateTabs(cfg); err != nil {
		return err
	}
	return nil
}

// directoryKinds are the template kinds of every candidate that can be
// previewed or launched as a workspace: everything except the Herdr tab and
// pane rows the picker nests under an open workspace.
var directoryKinds = []string{
	tmpl.KindWorkspace, tmpl.KindConfigured, tmpl.KindGroup, tmpl.KindFolder,
	tmpl.KindProject, tmpl.KindWorktree, tmpl.KindSession, tmpl.KindAgent, tmpl.KindCustom,
}

func validateWorkspaceNames(cfg *Config, engine *tmpl.Engine) error {
	samples := tmpl.Samples(directoryKinds...)
	if cfg.General.WorkspaceName != "" {
		if err := workspacename.Validate(engine, "general.workspace_name", cfg.General.WorkspaceName, samples...); err != nil {
			return err
		}
	}
	for i, wildcard := range cfg.Wildcards {
		if wildcard.WorkspaceName == "" {
			continue
		}
		field := fmt.Sprintf("wildcards[%d].workspace_name", i)
		if err := workspacename.Validate(engine, field, wildcard.WorkspaceName, samples...); err != nil {
			return err
		}
	}
	return nil
}

// validatePreviewCommandTemplates applies the same validation that runtime
// command rendering will use: tokenize first, then render each argv token
// for every kind of candidate a global preview command can run for.
func validatePreviewCommandTemplates(commands map[string]PreviewCommand, engine *tmpl.Engine) error {
	for name, command := range commands {
		field := fmt.Sprintf("preview.commands.%s.command", name)
		tokens, err := tmpl.Tokenize(command.Command)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		for _, token := range tokens {
			if err := engine.ValidatePlain(field, token, tmpl.Samples(directoryKinds...)...); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCustomSourcePreviewCommands(customSource CustomSourceConfig, index int, global map[string]PreviewCommand, engine *tmpl.Engine) error {
	for name, command := range customSource.PreviewCommands {
		field := fmt.Sprintf("sources.custom[%d] (%q).preview_commands.%s", index, customSource.Name, name)
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%s: name is required", field)
		}
		if builtinPreviewNames[name] {
			return fmt.Errorf("%s: collides with built-in preview section", field)
		}
		if _, ok := global[name]; ok {
			return fmt.Errorf("%s: collides with global preview command", field)
		}
		if len(command.Command) == 0 {
			return fmt.Errorf("%s: command is required", field)
		}
		for j, arg := range command.Command {
			if j == 0 && strings.TrimSpace(arg) == "" {
				return fmt.Errorf("%s: command[0] is required", field)
			}
			if strings.IndexByte(arg, 0) >= 0 {
				return fmt.Errorf("%s: command[%d] contains NUL", field, j)
			}
			if err := engine.ValidatePlain(field+fmt.Sprintf(".command[%d]", j), arg, tmpl.Samples(tmpl.KindCustom)...); err != nil {
				return err
			}
		}
		if command.Timeout <= 0 {
			return fmt.Errorf("%s: timeout must be > 0", field)
		}
		if command.MaxLines <= 0 {
			return fmt.Errorf("%s: max_lines must be > 0", field)
		}
	}
	return nil
}

// validateAllPreviewLists validates every `preview = [...]` list found
// outside [preview] itself. Built-in-source, workspace, and wildcard lists use
// built-ins plus global commands; custom source lists additionally use their own
// private commands.
func validateAllPreviewLists(cfg *Config) error {
	checks := []struct {
		label string
		names []string
	}{
		{"sources.herdr.preview", cfg.Sources.Herdr.Preview},
		{"sources.sessions.preview", cfg.Sources.Sessions.Preview},
		{"sources.workspaces.preview", cfg.Sources.Workspaces.Preview},
		{"sources.zoxide.preview", cfg.Sources.Zoxide.Preview},
		{"sources.projects.preview", cfg.Sources.Projects.Preview},
		{"sources.agents.preview", cfg.Sources.Agents.Preview},
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
	for i, customSource := range cfg.Sources.Custom {
		if err := validateCustomSourcePreviewNames(customSource, i, cfg.Preview.Commands); err != nil {
			return err
		}
	}
	return nil
}

func validateCustomSourcePreviewNames(customSource CustomSourceConfig, index int, global map[string]PreviewCommand) error {
	for _, name := range customSource.Preview {
		if builtinPreviewNames[name] {
			continue
		}
		if _, ok := global[name]; ok {
			continue
		}
		if _, ok := customSource.PreviewCommands[name]; ok {
			continue
		}
		return fmt.Errorf("sources.custom[%d] (%q).preview: %q is not a built-in preview, global preview.commands entry, or local preview_commands entry", index, customSource.Name, name)
	}
	return nil
}

func validateCustomSources(customSources []CustomSourceConfig, engine *tmpl.Engine) error {
	seen := make(map[string]struct{}, len(customSources))
	for i, customSource := range customSources {
		name := strings.TrimSpace(customSource.Name)
		if name == "" {
			return fmt.Errorf("sources.custom[%d]: name is required", i)
		}
		if validSourceNames[name] || name == "all" {
			return fmt.Errorf("sources.custom[%d] (%q): name collides with built-in source or tab", i, name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("sources.custom[%d] (%q): duplicate name", i, name)
		}
		seen[name] = struct{}{}
		if len(customSource.Command) == 0 || strings.TrimSpace(customSource.Command[0]) == "" {
			return fmt.Errorf("sources.custom[%d] (%q): command is required", i, name)
		}
		for j, arg := range customSource.Command {
			if strings.IndexByte(arg, 0) >= 0 {
				return fmt.Errorf("sources.custom[%d] (%q): command[%d] contains NUL", i, name, j)
			}
			if j == 0 && strings.ContainsAny(arg, "\r\n\t") {
				return fmt.Errorf("sources.custom[%d] (%q): command[0] contains control characters", i, name)
			}
		}

		if customSource.Timeout <= 0 {
			return fmt.Errorf("sources.custom[%d] (%q): timeout must be > 0", i, name)
		}
		if err := validatePresentation(engine, fmt.Sprintf("sources.custom[%d]", i), customSource.Presentation, tmpl.KindCustom); err != nil {
			return err
		}
	}
	return nil
}

// validateSources rejects names outside validSourceNames and the declared
// custom sources, so typos fail fast instead of silently disabling a source.
func validateSources(names []string, customSources ...[]CustomSourceConfig) error {
	allowed := make(map[string]bool, len(validSourceNames))
	for name := range validSourceNames {
		allowed[name] = true
	}
	if len(customSources) > 0 {
		for _, customSource := range customSources[0] {
			allowed[customSource.Name] = true
		}
	}
	for _, n := range names {
		if !allowed[n] {
			return fmt.Errorf("invalid source_order entry %q (valid: %s, %s, %s, %s, %s, %s, or a declared custom source)",
				n, SourceHerdr, SourceSessions, SourceWorkspaces, SourceZoxide, SourceProjects, SourceAgents)
		}
	}
	return nil
}

// MergeProjectsSourceConfig applies a group override field by field. Slices are
// copied so effective settings remain independent of the source configuration.
func MergeProjectsSourceConfig(global ProjectsSourceConfig, override *ProjectsSourceOverride) ProjectsSourceConfig {
	out := global
	out.Roots = append([]string(nil), global.Roots...)
	out.Markers = append([]string(nil), global.Markers...)
	out.Ignore = append([]string(nil), global.Ignore...)
	out.Preview = append([]string(nil), global.Preview...)
	if override == nil {
		return out
	}
	if override.Recursive != nil {
		out.Recursive = *override.Recursive
	}
	if override.MaxDepth != nil {
		out.MaxDepth = *override.MaxDepth
	}
	if override.Markers != nil {
		out.Markers = append([]string(nil), (*override.Markers)...)
	}
	if override.Ignore != nil {
		out.Ignore = append([]string(nil), (*override.Ignore)...)
	}
	if override.Preview != nil {
		out.Preview = append([]string(nil), (*override.Preview)...)
	}
	return out
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
		if n.Label != nil {
			return fmt.Errorf("%s (%q): branch node %q cannot set label", prefix, tabName, n.ID)
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
func validateWorkspaces(workspaces []WorkspaceConfig, templates map[string]TemplateConfig, customSources []CustomSourceConfig) error {
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
			if strings.TrimSpace(ws.Path) == "" {
				return fmt.Errorf("workspaces[%d] (%q): group path is required", i, ws.Name)
			}
			if err := validateSources(ws.SourceOrder, customSources); err != nil {
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
// their own callers) is a built-in or a declared global command.
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
// in the global commands map. Used for global preview lists; custom source lists
// additionally accept only their own local command map.
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
// overflow the rendered layout. Load normalizes omitted fields before this
// check, so the built-in 35/65 split is authoritative for partial configs.
func validateTUI(t TUIConfig) error {
	seenClose := make(map[string]bool, len(t.ConfirmClose))
	for _, kind := range t.ConfirmClose {
		if kind != "workspace" && kind != "tab" && kind != "pane" {
			return fmt.Errorf("tui.confirm_close: invalid kind %q (valid: workspace, tab, pane)", kind)
		}
		if seenClose[kind] {
			return fmt.Errorf("tui.confirm_close: duplicate kind %q", kind)
		}
		seenClose[kind] = true
	}
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
	if t.Icons != "" && !validTUIIcons[t.Icons] {
		switch t.Icons {
		case "nerd":
			return fmt.Errorf("tui.icons: %q was removed (only %s and %s are supported)", t.Icons, TUIIconsUnicode, TUIIconsASCII)
		default:
			return fmt.Errorf("tui.icons: %q must be one of %s, %s",
				t.Icons, TUIIconsUnicode, TUIIconsASCII)
		}
	}
	return nil
}

// ViewKinds builds the shared namespace for configured tabs and explicit views.
// Duplicate declarations remain visible so the resolver can reject ambiguity.
func ViewKinds(cfg *Config) map[string][]string {
	known := map[string][]string{"all": {"scope"}, "agents": {"scope"}}
	for _, name := range []string{SourceHerdr, SourceWorkspaces, SourceZoxide, SourceProjects, SourceSessions} {
		known[name] = append(known[name], "source")
	}
	for _, customSource := range cfg.Sources.Custom {
		known[customSource.Name] = append(known[customSource.Name], "custom source")
	}
	for _, ws := range cfg.Workspaces {
		if ws.ID == "" {
			continue
		}
		kind := "workspace"
		if ws.Type == WorkspaceTypeGroup {
			kind = "group"
		}
		known[ws.ID] = append(known[ws.ID], kind)
	}
	return known
}

// ResolveView checks an explicit view against the same namespace as tui.tabs.
func ResolveView(cfg *Config, id string) (string, error) {
	known := ViewKinds(cfg)
	ids := make([]string, 0, len(known))
	for name, kinds := range known {
		if len(kinds) == 1 && kinds[0] != "workspace" {
			ids = append(ids, name)
		}
	}
	slices.Sort(ids)
	kinds := known[id]
	if len(kinds) > 1 {
		return "", fmt.Errorf("ambiguous --view %q (%s)", id, strings.Join(kinds, ", "))
	}
	if len(kinds) == 1 && kinds[0] == "workspace" {
		return "", fmt.Errorf("invalid --view %q: non-group workspace (valid ids: %s)", id, strings.Join(ids, ", "))
	}
	if len(kinds) == 0 {
		return "", fmt.Errorf("unknown --view %q (valid ids: %s)", id, strings.Join(ids, ", "))
	}
	return kinds[0], nil
}

// validateTabs resolves only stable group IDs; names and paths can be shared
// across workspace entries and cannot unambiguously identify a tab.
func validateTabs(cfg *Config) error {
	known := ViewKinds(cfg)
	for _, customSource := range cfg.Sources.Custom {
		if customSource.Name == "all" || customSource.Name == "agents" {
			return fmt.Errorf("sources.custom name %q: collides with built-in tab scope", customSource.Name)
		}
	}
	seenIDs := map[string]string{"all": "scope", "agents": "scope"}
	for _, name := range []string{SourceHerdr, SourceWorkspaces, SourceZoxide, SourceProjects, SourceSessions} {
		seenIDs[name] = "source"
	}
	for _, customSource := range cfg.Sources.Custom {
		seenIDs[customSource.Name] = "custom source"
	}
	for i, ws := range cfg.Workspaces {
		if ws.ID == "" {
			continue
		}
		if strings.TrimSpace(ws.ID) != ws.ID {
			return fmt.Errorf("workspaces[%d].id %q: must not have surrounding whitespace", i, ws.ID)
		}
		if prev, exists := seenIDs[ws.ID]; exists {
			return fmt.Errorf("workspaces[%d].id %q: ambiguous reference (already declared as %s)", i, ws.ID, prev)
		}
		seenIDs[ws.ID] = known[ws.ID][0]
	}
	seen := make(map[string]bool, len(cfg.TUI.Tabs))
	for _, tab := range cfg.TUI.Tabs {
		if seen[tab] {
			return fmt.Errorf("tui.tabs: duplicate tab %q", tab)
		}
		seen[tab] = true
		_, err := ResolveView(cfg, tab)
		switch {
		case err == nil:
		case len(known[tab]) == 1 && known[tab][0] == "workspace":
			return fmt.Errorf("tui.tabs: %q references a non-group workspace; set type = %q", tab, WorkspaceTypeGroup)
		case len(known[tab]) > 1:
			return fmt.Errorf("tui.tabs: %w", err)
		default:
			return fmt.Errorf("tui.tabs: unknown tab %q (use all, agents, a built-in source, declared custom source or group workspace id)", tab)
		}
	}
	return nil
}

// validateTUILayout enforces that [tui].layout is empty (auto) or "landscape",
// failing Load fast with the bad value named in the error — consistent with
// validateWidthField above. The removed "portrait" (stacked) layout is rejected
// with its own clear message so a stale config fails fast instead of silently
// degrading.
func validateTUILayout(layout string) error {
	switch layout {
	case "", TUILayoutLandscape:
		return nil
	case "portrait":
		return fmt.Errorf("tui.layout: %q was removed (only %q or empty/auto is supported; the stacked layout was deleted)",
			layout, TUILayoutLandscape)
	}
	return fmt.Errorf("tui.layout: %q must be %q or empty (auto)", layout, TUILayoutLandscape)
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

// FirstMatchingWildcard returns the first ordered wildcard matching normalizedPath
// or its host-native base name. Declaration order is the precedence contract.
func FirstMatchingWildcard(wildcards []WildcardConfig, normalizedPath string) (WildcardConfig, bool) {
	if normalizedPath == "" {
		return WildcardConfig{}, false
	}
	base := filepath.Base(normalizedPath)
	for _, wildcard := range wildcards {
		if MatchWildcard(wildcard.Pattern, normalizedPath) || MatchWildcard(wildcard.Pattern, base) {
			return wildcard, true
		}
	}
	return WildcardConfig{}, false
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
