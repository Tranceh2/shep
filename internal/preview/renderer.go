package preview

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/rowformat"
	"github.com/tranceh2/shep/internal/source"
)

// Renderer turns one candidate into a preview Result. The same Renderer backs
// the Bubble Tea selector (async, cached) and `shep preview <path>` (sync,
// stdout), so the two views never diverge.
type Renderer interface {
	Render(ctx context.Context, cand source.Candidate) (Result, error)
}

// Result is the rendered preview. Text is the body — plain text for
// "identity"/"git"/"workspace", and possibly ANSI-colored for "dir" and
// "active_pane": some built-in sections carry real ANSI color codes from the
// tool they shell out to ("dir" runs lsd/eza's own colored listing;
// "active_pane" is the pane's real captured terminal appearance), so the user
// sees what they'd actually see. "identity", "git", and "workspace" stay
// plain text. A user-declared [preview.commands.<name>] may or may not emit
// color depending on the command itself. FromCache is true when Text was
// served from the in-memory cache. Any ANSI-carrying section is safe to
// truncate: see internal/tui/model.go's truncateToWidth, which is ANSI-aware
// (charmbracelet/x/ansi.Truncate) and never cuts mid-escape sequence.
//
// Sections carries the same content as Text, but split into structured,
// typed blocks so the TUI can consume each section by Kind without parsing
// the joined text. Sections is populated by defaultRenderer.Render in the
// same order as the blocks are joined into Text; a custom Renderer that
// returns only Text (no Sections) is safe — the TUI degrades to a compact
// identity display rather than parsing untrusted text. Each Section's Kind
// is the configured section name (config.PreviewIdentity, config.PreviewGit,
// etc.) or the custom command name — never inferred from the payload text.
// Section.Text is the complete section block and may contain arbitrary blank
// lines or heading-like content; it is preserved byte-for-byte.
type Result struct {
	Text      string
	Sections  []Section
	FromCache bool
}

// Section is one structured block of the preview renderer's output. Kind is
// the configured section name (config.PreviewIdentity, config.PreviewGit,
// config.PreviewWorkspace, config.PreviewActivePane, config.PreviewAgentStatus,
// config.PreviewDir, or a custom command name). Text is the complete section
// block — it may contain blank lines and heading-like content, and is
// preserved byte-for-byte by the TUI recomposition.
type Section struct {
	Kind string
	Text string
}

// defaultRenderer resolves the ordered section list per candidate (workspace
// > wildcard > source > global default > built-in fallback) and renders each
// named section — hardcoded built-ins (identity, git, workspace, active_pane,
// dir) or a declared [preview.commands.<name>] — with TTL caching.
type defaultRenderer struct {
	cfg    *config.Config
	probes config.Probes
	git    GitProvider
	runner CommandRunner
	driver source.HerdrDriver
	cache  *Cache
}

// RendererOption configures a Renderer at construction (e.g. to inject a
// HerdrDriver for the workspace/active_pane preview sections).
type RendererOption func(*defaultRenderer)

// WithHerdrDriver injects a HerdrDriver so the workspace and active_pane
// preview sections can enumerate tabs/panes and read the active pane buffer.
// Without a driver those sections degrade to a skip. Intended for production
// wiring; tests inject a fake.
func WithHerdrDriver(d source.HerdrDriver) RendererOption {
	return func(r *defaultRenderer) { r.driver = d }
}

// NewRenderer wires the production renderer from the full config (Workspaces
// and Wildcards feed the preview-name precedence chain; Preview carries the
// caps/commands/default), a binary probes snapshot, an optional GitProvider,
// and an optional CommandRunner (used for both the "dir" built-in and any
// declared preview.commands). A TTL cache (preview.cache_ttl) keeps cursor
// revisits responsive.
func NewRenderer(cfg *config.Config, probes config.Probes, git GitProvider, runner CommandRunner, opts ...RendererOption) Renderer {
	if cfg == nil {
		cfg = config.Defaults()
	}
	r := &defaultRenderer{
		cfg:    cfg,
		probes: probes,
		git:    git,
		runner: runner,
		cache:  NewCache(time.Duration(cfg.Preview.CacheTTL)),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Render resolves the ordered preview section list for cand and renders each
// in turn, joining non-empty blocks with a blank line, then caches the
// result by path and config.
func (r *defaultRenderer) Render(ctx context.Context, cand source.Candidate) (Result, error) {
	key := PreviewCacheKey(cand, r.cfg.Preview)
	if cached, ok := r.cache.Get(key); ok {
		return cached, nil
	}

	names := resolvePreviewNames(r.cfg, cand)
	var blocks []string
	var sections []Section
	for _, name := range names {
		if block, ok := r.renderSection(ctx, cand, name); ok {
			blocks = append(blocks, block)
			sections = append(sections, Section{Kind: name, Text: block})
		}
	}
	// Every configured section can legitimately contribute nothing (a
	// command failed, a herdr query timed out, a section is not applicable
	// to this candidate). A normal preview must never come back blank in
	// that case: fall back to the built-in identity section so there is
	// always clean, useful output instead of a silent empty success.
	if len(blocks) == 0 {
		identity := renderIdentity(cand)
		blocks = append(blocks, identity)
		sections = append(sections, Section{Kind: config.PreviewIdentity, Text: identity})
	}
	res := Result{Text: strings.Join(blocks, "\n\n"), Sections: sections}
	r.cache.Put(key, res)
	return res, nil
}

// renderSection dispatches a single named section to its renderer. ok=false
// means the section contributed nothing (unavailable dependency, unknown
// name, or empty output) and is omitted entirely from normal preview output.
func (r *defaultRenderer) renderSection(ctx context.Context, cand source.Candidate, name string) (string, bool) {
	switch name {
	case config.PreviewIdentity:
		return renderIdentity(cand), true
	case config.PreviewGit:
		if line, ok := r.gitLine(ctx, cand); ok {
			return "git: " + line, true
		}
		return "", false
	case config.PreviewWorkspace:
		return r.renderWorkspaceSection(ctx, cand)
	case config.PreviewActivePane:
		return r.renderActivePaneSection(ctx, cand)
	case config.PreviewAgentStatus:
		return r.renderAgentStatusSection(ctx, cand)
	case config.PreviewDir:
		return r.renderDirSection(ctx, cand)
	default:
		cmd, ok := r.cfg.Preview.Commands[name]
		if !ok {
			return "", false
		}
		return r.renderCustomCommand(ctx, cmd, cand)
	}
}

// resolvePreviewNames implements the documented precedence: an exact
// [[workspaces]] path match with its own preview list wins; else the first
// matching [[wildcards]] entry with a non-empty preview list; else the
// candidate's source-level preview list; else [preview].default; else a
// single built-in "identity" fallback so the preview is never blank.
func resolvePreviewNames(cfg *config.Config, cand source.Candidate) []string {
	path := renderPath(cand)
	base := filepath.Base(path)

	for _, ws := range cfg.Workspaces {
		wsPath := ws.Path
		if expanded, err := pathutil.ExpandTilde(ws.Path); err == nil {
			wsPath = expanded
		}
		if wsPath == "" || len(ws.Preview) == 0 {
			continue
		}
		// pathutil.SameDir already resolves symlinks and case-fold
		// equivalence via os.Stat + os.SameFile (device+inode identity), so
		// no separate Normalize pass is needed on either side here: a
		// wsPath that is itself a symlink, or that differs only in case
		// from path on a case-insensitive filesystem, still matches.
		if pathutil.SameDir(wsPath, path) {
			return ws.Preview
		}
	}
	for _, w := range cfg.Wildcards {
		if config.MatchWildcard(w.Pattern, path) || config.MatchWildcard(w.Pattern, base) {
			if len(w.Preview) > 0 {
				return w.Preview
			}
			break
		}
	}
	if names := sourcePreview(cfg, cand.Source); len(names) > 0 {
		return names
	}
	if len(cfg.Preview.Default) > 0 {
		return cfg.Preview.Default
	}
	return []string{config.PreviewIdentity}
}

// sourcePreview returns the configured [sources.<name>].preview list for the
// candidate's source, or nil when unset/unknown.
func sourcePreview(cfg *config.Config, sourceName string) []string {
	switch sourceName {
	case config.SourceHerdr:
		return cfg.Sources.Herdr.Preview
	case config.SourceWorkspaces:
		return cfg.Sources.Workspaces.Preview
	case config.SourceZoxide:
		return cfg.Sources.Zoxide.Preview
	case config.SourceProjects:
		return cfg.Sources.Projects.Preview
	}
	return nil
}

// renderIdentity shows label, path, source, and a matched template (when
// present in Meta) — the built-in "identity" section.
func renderIdentity(cand source.Candidate) string {
	lines := []string{
		cand.Label,
		"path: " + renderPath(cand),
		"source: " + cand.Source,
	}
	if t := cand.Meta["template"]; t != "" {
		lines = append(lines, "template: "+t)
	}
	return strings.Join(lines, "\n")
}

// herdrPreviewTimeout bounds every herdr query made while rendering a preview
// so a slow daemon never freezes the selector. Each herdr call gets its own
// deadline; a timeout degrades the section to an unavailable note.
const herdrPreviewTimeout = 100 * time.Millisecond

// renderWorkspaceSection renders an indented tree of the workspace's tabs and
// panes. ok=false means the section is skipped entirely: the candidate is
// not an active herdr workspace, or no driver is wired. A query failure or
// timeout degrades to an unavailable note under the heading.
func (r *defaultRenderer) renderWorkspaceSection(ctx context.Context, cand source.Candidate) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.driver == nil {
		return "", false
	}
	lines := []string{"workspace"}
	tabs, panes, err := r.loadWorkspacePreview(ctx, workspaceID)
	if err != nil {
		lines = append(lines, "(workspace unavailable)")
		return strings.Join(lines, "\n"), true
	}
	for _, t := range tabs {
		marker := " "
		if t.Focused {
			marker = "*"
		}
		lines = append(lines, fmt.Sprintf("  tab %d: %s %s (%d panes)", t.Number, t.Label, marker, t.PaneCount))
	}
	for _, p := range panes {
		marker := " "
		if p.Focused {
			marker = "*"
		}
		lines = append(lines, fmt.Sprintf("  pane %s %s %s", p.ID, marker, p.CWD))
	}
	return strings.Join(lines, "\n"), true
}

// renderAgentStatusSection renders a static, at-open-time snapshot of the
// focused Herdr pane's agent status (via Driver.CurrentPane). ok=false means
// the section is skipped entirely: the candidate is not an active herdr
// workspace, or no driver is wired. A query failure or timeout degrades to
// an unavailable note; no focused pane degrades to a "no active pane" note.
// This section normalizes both an empty AgentStatus and an explicit
// "unknown" to the same "unknown" display text — the preview always shows a
// definite line under the heading rather than distinguishing "not reported"
// from "reported as unknown".
func (r *defaultRenderer) renderAgentStatusSection(ctx context.Context, cand source.Candidate) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.driver == nil {
		return "", false
	}
	lines := []string{"agent status"}
	pane, err := r.currentPanePreview(ctx)
	if err != nil {
		if errors.Is(err, source.ErrNoFocusedPane) {
			lines = append(lines, "(no active pane)")
			return strings.Join(lines, "\n"), true
		}
		lines = append(lines, "(agent status unavailable)")
		return strings.Join(lines, "\n"), true
	}
	status := pane.AgentStatus
	if status == "" {
		status = "unknown"
	}
	lines = append(lines, "  status: "+status)
	return strings.Join(lines, "\n"), true
}

// renderActivePaneSection renders the active pane's captured terminal
// buffer, capped at cfg.Preview.MaxLines. ok=false means the section is
// skipped entirely (non-herdr candidate, no driver, no panes, read error,
// or empty/whitespace-only buffer). An unavailable or empty capture never
// produces a heading-only section — the TUI must not show an empty "Active
// pane" block (R3-001 fix). Non-TUI consumers (shep preview) also see the
// section omitted, since there is no useful content to display.
func (r *defaultRenderer) renderActivePaneSection(ctx context.Context, cand source.Candidate) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.driver == nil {
		return "", false
	}
	panes, err := r.listPanesPreview(ctx, workspaceID)
	if err != nil {
		return "", false
	}
	paneID := activePaneID(panes)
	if paneID == "" {
		return "", false
	}
	buf, err := r.readPanePreview(ctx, paneID, r.cfg.Preview.MaxLines)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(buf) == "" {
		return "", false
	}
	lines := []string{"active pane"}
	lines = append(lines, capLines(buf, r.cfg.Preview.MaxLines))
	return strings.Join(lines, "\n"), true
}

// renderDirSection runs the first available of lsd/eza/ls against the
// candidate's path. ok=false means no runner is wired or the command
// produced no output; command failures are hidden from normal preview
// output (they are simply omitted), never shown as an error to the user.
func (r *defaultRenderer) renderDirSection(ctx context.Context, cand source.Candidate) (string, bool) {
	if r.runner == nil {
		return "", false
	}
	argv := dirArgv(renderPath(cand))
	runCtx, cancel := boundedContext(ctx, r.cfg.Preview.Timeout)
	defer cancel()
	out, err := r.runner.Run(runCtx, argv, "", r.cfg.Preview.MaxLines)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// renderCustomCommand executes a declared [preview.commands.<name>] entry.
// ok=false (empty output or any failure) hides the section entirely per the
// "hide command errors from normal preview output" rule.
func (r *defaultRenderer) renderCustomCommand(ctx context.Context, cmd config.PreviewCommand, cand source.Candidate) (string, bool) {
	if r.runner == nil {
		return "", false
	}
	argv, err := ParseCommand(cmd.Command, rowformat.Context{
		Path:  renderPath(cand),
		Label: cand.Label,
	})
	if err != nil {
		return "", false
	}
	runCtx, cancel := boundedContext(ctx, r.cfg.Preview.Timeout)
	defer cancel()
	out, err := r.runner.Run(runCtx, argv, cand.Path, r.cfg.Preview.MaxLines)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// boundedContext applies timeout to ctx when positive, mirroring the escape
// hatch commands' safety bound.
func boundedContext(ctx context.Context, timeout config.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(timeout))
}

// dirLookPath is the exec.LookPath seam so tests can control which of
// lsd/eza is "available" without touching the real PATH.
var dirLookPath = exec.LookPath

// dirArgv picks the first available of lsd, eza, else falls back to ls, per
// the documented "dir" built-in. lsd and eza are forced to --color=always so
// the "dir" section shows their real colored listing — the reason to prefer
// them over plain ls in the first place. Forcing color also keeps the
// output deterministic regardless of the invoking process's $TERM/color-
// profile detection (which would otherwise vary whether lsd/eza auto-detect
// a color-capable terminal), so this built-in section's TTL-cached result
// stays stable for the same input. internal/tui/model.go's truncateToWidth
// is ANSI-aware (it delegates to charmbracelet/x/ansi.Truncate), which is
// what makes it safe to carry real color codes through this section without
// corrupting truncation at narrow widths. ls has no such flag and stays
// plain. Icons are kept — they're plain glyphs, not escape sequences.
func dirArgv(path string) []string {
	if _, err := dirLookPath("lsd"); err == nil {
		return []string{"lsd", "-la", "--icon=always", "--color=always", path}
	}
	if _, err := dirLookPath("eza"); err == nil {
		return []string{"eza", "--all", "--git", "--icons", "--color=always", path}
	}
	return []string{"ls", "-la", path}
}

// loadWorkspacePreview fetches tabs and panes for a workspace, each query
// bounded by herdrPreviewTimeout. Either query failing yields an error so the
// caller can degrade to an unavailable note.
func (r *defaultRenderer) loadWorkspacePreview(ctx context.Context, workspaceID string) ([]source.Tab, []source.Pane, error) {
	tabs, err := r.listTabsPreview(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	panes, err := r.listPanesPreview(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	return tabs, panes, nil
}

// listTabsPreview bounds a ListTabs call by herdrPreviewTimeout.
func (r *defaultRenderer) listTabsPreview(ctx context.Context, workspaceID string) ([]source.Tab, error) {
	qctx, cancel := context.WithTimeout(ctx, herdrPreviewTimeout)
	defer cancel()
	return r.driver.ListTabs(qctx, workspaceID)
}

// listPanesPreview bounds a ListPanes call by herdrPreviewTimeout.
func (r *defaultRenderer) listPanesPreview(ctx context.Context, workspaceID string) ([]source.Pane, error) {
	qctx, cancel := context.WithTimeout(ctx, herdrPreviewTimeout)
	defer cancel()
	return r.driver.ListPanes(qctx, workspaceID)
}

// readPanePreview bounds a ReadPane call by herdrPreviewTimeout.
func (r *defaultRenderer) readPanePreview(ctx context.Context, paneID string, lines int) (string, error) {
	qctx, cancel := context.WithTimeout(ctx, herdrPreviewTimeout)
	defer cancel()
	return r.driver.ReadPane(qctx, paneID, lines)
}

// currentPanePreview bounds a CurrentPane call by herdrPreviewTimeout, for
// the agent_status section's static snapshot.
func (r *defaultRenderer) currentPanePreview(ctx context.Context) (source.Pane, error) {
	qctx, cancel := context.WithTimeout(ctx, herdrPreviewTimeout)
	defer cancel()
	return r.driver.CurrentPane(qctx)
}

// activePaneID returns the focused pane's id, falling back to the first pane.
// Empty when there are no panes.
func activePaneID(panes []source.Pane) string {
	if len(panes) == 0 {
		return ""
	}
	for _, p := range panes {
		if p.Focused {
			return p.ID
		}
	}
	return panes[0].ID
}

// capLines truncates a buffer to at most max trailing lines. max <= 0 returns
// the buffer unchanged so a misconfigured cap never blanks the preview.
func capLines(buf string, max int) string {
	if max <= 0 {
		return buf
	}
	lines := strings.Split(buf, "\n")
	if len(lines) <= max {
		return buf
	}
	return strings.Join(lines[len(lines)-max:], "\n")
}

// gitLine returns the rendered git summary for the candidate path, or false when
// git is unavailable (probe off, provider nil), slow, or missing.
func (r *defaultRenderer) gitLine(ctx context.Context, cand source.Candidate) (string, bool) {
	if r.git == nil || !r.probes.Git {
		return "", false
	}
	sum, err := r.git.Summary(ctx, cand.Path)
	if err != nil {
		return "", false
	}
	return sum.String(), true
}

// renderPath prefers the post-dedup/post-symlink normalised path and falls
// back to the raw path so output is never empty.
func renderPath(cand source.Candidate) string {
	if cand.NormalizedPath != "" {
		return cand.NormalizedPath
	}
	return cand.Path
}
