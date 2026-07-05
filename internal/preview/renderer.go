package preview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/source"
)

// Renderer turns one candidate into a preview Result. The same Renderer backs
// the Bubble Tea selector (async, cached) and `shep preview <path>` (sync,
// stdout), so the two views never diverge.
type Renderer interface {
	Render(ctx context.Context, cand source.Candidate, opts RenderOptions) (Result, error)
}

// RenderOptions carries presentation hints. The renderer always returns plain text
// and does not apply ANSI styling internally. Color is kept as a hint so that callers
// can decide how they wish to style the returned Result; styling is handled externally
// by the CLI and TUI layers. Width is the target pane width (0 = unknown); the TUI layer
// adapts further.
type RenderOptions struct {
	Color bool
	Width int
}

// Result is the rendered preview. Text is the plain-text body; Warning is a
// transient, non-fatal note (e.g. a custom command failed and the built-in
// preview was shown instead); FromCache is true when Text was served from the
// in-memory cache rather than freshly computed.
type Result struct {
	Text      string
	Warning   string
	FromCache bool
}

// defaultRenderer renders the built-in/default layout or declarative sections,
// or delegates to a custom command when configured, with TTL caching.
type defaultRenderer struct {
	cfg    config.PreviewConfig
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
// Without a driver those sections degrade to a muted skip. Intended for
// production wiring; tests inject a fake.
func WithHerdrDriver(d source.HerdrDriver) RendererOption {
	return func(r *defaultRenderer) { r.driver = d }
}

// NewRenderer wires the production renderer from a preview config, a binary
// probes snapshot, an optional GitProvider, and an optional CommandRunner. When
// preview.Command is set the runner path is used; otherwise the built-in or
// declarative-section layout is rendered. A TTL cache (preview.cache_ttl)
// keeps cursor revisits responsive. RendererOption values (e.g.
// WithHerdrDriver) extend the renderer for herdr-backed preview sections.
func NewRenderer(cfg config.PreviewConfig, probes config.Probes, git GitProvider, runner CommandRunner, opts ...RendererOption) Renderer {
	r := &defaultRenderer{
		cfg:    cfg,
		probes: probes,
		git:    git,
		runner: runner,
		cache:  NewCache(time.Duration(cfg.CacheTTL)),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Render resolves the preview for one candidate, honouring the command escape
// hatch (with fallback + warning), declarative sections, and the built-in
// default, then caches the result by path and config.
func (r *defaultRenderer) Render(ctx context.Context, cand source.Candidate, _ RenderOptions) (Result, error) {
	key := PreviewCacheKey(renderPath(cand), r.cfg)
	if cached, ok := r.cache.Get(key); ok {
		return cached, nil
	}

	if r.cfg.Command != "" {
		return r.renderCommand(ctx, cand, key)
	}

	text := r.renderLayout(ctx, cand)
	res := Result{Text: text}
	r.cache.Put(key, res)
	return res, nil
}

// renderCommand executes the configured command and falls back to the built-in
// preview with a transient warning on any failure (parse error, timeout,
// non-zero exit, stderr).
func (r *defaultRenderer) renderCommand(ctx context.Context, cand source.Candidate, key string) (Result, error) {
	argv, err := ParseCommand(r.cfg.Command, renderPath(cand))
	if err != nil {
		return r.commandFallback(ctx, cand, err)
	}
	if r.runner == nil {
		return r.commandFallback(ctx, cand, errNoRunner)
	}
	runCtx := ctx
	cancel := func() {}
	if r.cfg.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(r.cfg.Timeout))
	}
	defer cancel()
	out, runErr := r.runner.Run(runCtx, argv, cand.Path, r.cfg.MaxLines)
	if runErr != nil {
		return r.commandFallback(ctx, cand, runErr)
	}
	res := Result{Text: out}
	r.cache.Put(key, res)
	return res, nil
}

// commandFallback renders the built-in preview and records a warning describing
// why the custom command did not run.
func (r *defaultRenderer) commandFallback(ctx context.Context, cand source.Candidate, cause error) (Result, error) {
	res := Result{
		Text:    r.renderDefault(ctx, cand),
		Warning: safeCommandWarning(cause),
	}
	return res, nil
}

func safeCommandWarning(cause error) string {
	if errors.Is(cause, context.DeadlineExceeded) {
		return "preview command timed out"
	}
	if errors.Is(cause, errNoRunner) {
		return "preview command unavailable"
	}
	return "preview command failed"
}

// renderLayout picks declarative sections when configured, otherwise the
// built-in default layout.
func (r *defaultRenderer) renderLayout(ctx context.Context, cand source.Candidate) string {
	if len(r.cfg.Sections) == 0 {
		return r.renderDefault(ctx, cand)
	}
	return r.renderSections(ctx, cand)
}

// renderDefault (WP-1) shows label, path, source, a matched template when
// present, and a fast git summary when available.
func (r *defaultRenderer) renderDefault(ctx context.Context, cand source.Candidate) string {
	var lines []string
	lines = append(lines, cand.Label)
	lines = append(lines, "path: "+renderPath(cand))
	lines = append(lines, "source: "+cand.Source)
	if t := cand.Meta["template"]; t != "" {
		lines = append(lines, "template: "+t)
	}
	if gline, ok := r.gitLine(ctx, cand); ok {
		lines = append(lines, "git: "+gline)
	}
	return strings.Join(lines, "\n")
}

// herdrPreviewTimeout bounds every herdr query made while rendering a preview
// so a slow daemon never freezes the selector (PR4 goal 2). Each herdr call
// gets its own deadline; a timeout degrades the section to a muted note.
const herdrPreviewTimeout = 100 * time.Millisecond

// renderSections (WP-2) renders [[preview.sections]] in declaration order using
// the declared field names (builtin) or a git summary (git). The herdr-backed
// workspace and active_pane sections are skipped entirely when the candidate
// is not an active herdr workspace (no workspace_id meta key) or when no
// driver is wired; on query failure/timeout they degrade to a muted
// unavailable note under the section heading.
func (r *defaultRenderer) renderSections(ctx context.Context, cand source.Candidate) string {
	blocks := make([]string, 0, len(r.cfg.Sections))
	for _, sec := range r.cfg.Sections {
		switch sec.Type {
		case config.PreviewSectionWorkspace:
			if block, ok := r.renderWorkspaceSection(ctx, cand, sec); ok {
				blocks = append(blocks, block)
			}
		case config.PreviewSectionActivePane:
			if block, ok := r.renderActivePaneSection(ctx, cand, sec); ok {
				blocks = append(blocks, block)
			}
		default:
			blocks = append(blocks, r.renderStaticSection(ctx, cand, sec))
		}
	}
	return strings.Join(blocks, "\n\n")
}

// renderStaticSection renders builtin and git sections (the WP-2 layout that
// does not depend on a herdr driver). Heading is always included when set.
func (r *defaultRenderer) renderStaticSection(ctx context.Context, cand source.Candidate, sec config.PreviewSection) string {
	var lines []string
	if sec.Name != "" {
		lines = append(lines, sec.Name)
	}
	switch sec.Type {
	case config.PreviewSectionBuiltin:
		for _, f := range sec.Fields {
			lines = append(lines, f+": "+fieldValue(cand, f))
		}
	case config.PreviewSectionGit:
		if gline, ok := r.gitLine(ctx, cand); ok {
			lines = append(lines, gline)
		} else {
			lines = append(lines, "(git unavailable)")
		}
	}
	return strings.Join(lines, "\n")
}

// renderWorkspaceSection renders an indented tree of the workspace's tabs and
// panes. ok=false means the section is skipped entirely (no block): the
// candidate is not an active herdr workspace, or no driver is wired. A query
// failure or timeout degrades to a muted unavailable note under the heading.
func (r *defaultRenderer) renderWorkspaceSection(ctx context.Context, cand source.Candidate, sec config.PreviewSection) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.driver == nil {
		return "", false
	}
	var lines []string
	if sec.Name != "" {
		lines = append(lines, sec.Name)
	}
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

// renderActivePaneSection renders the active pane's captured terminal buffer,
// capped at cfg.MaxLines. The active pane is the focused one, falling back to
// the first pane when none is focused. ok=false means the section is skipped
// (non-herdr candidate or no driver). A query failure/timeout or a workspace
// with no panes degrades to a muted unavailable note under the heading.
func (r *defaultRenderer) renderActivePaneSection(ctx context.Context, cand source.Candidate, sec config.PreviewSection) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.driver == nil {
		return "", false
	}
	var lines []string
	if sec.Name != "" {
		lines = append(lines, sec.Name)
	}
	panes, err := r.listPanesPreview(ctx, workspaceID)
	if err != nil {
		lines = append(lines, "(active pane unavailable)")
		return strings.Join(lines, "\n"), true
	}
	paneID := activePaneID(panes)
	if paneID == "" {
		lines = append(lines, "(no active pane)")
		return strings.Join(lines, "\n"), true
	}
	buf, err := r.readPanePreview(ctx, paneID, r.cfg.MaxLines)
	if err != nil {
		lines = append(lines, "(active pane unavailable)")
		return strings.Join(lines, "\n"), true
	}
	if buf != "" {
		lines = append(lines, capLines(buf, r.cfg.MaxLines))
	}
	return strings.Join(lines, "\n"), true
}

// loadWorkspacePreview fetches tabs and panes for a workspace, each query
// bounded by herdrPreviewTimeout. Either query failing yields an error so the
// caller can degrade to a muted note.
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
// git is unavailable (probe off, provider nil), slow, or missing — WP-1's
// bypass contract.
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

// fieldValue maps a declared builtin field name to the candidate value.
func fieldValue(cand source.Candidate, field string) string {
	switch field {
	case config.PreviewFieldPath:
		return renderPath(cand)
	case config.PreviewFieldLabel:
		return cand.Label
	case config.PreviewFieldSource:
		return cand.Source
	case config.PreviewFieldTemplate:
		return cand.Meta["template"]
	}
	return ""
}

// renderPath prefers the post-dedup/post-symlink normalised path and falls back
// to the raw path so output is never empty.
func renderPath(cand source.Candidate) string {
	if cand.NormalizedPath != "" {
		return cand.NormalizedPath
	}
	return cand.Path
}

// errNoRunner is the warning cause when a command is configured but no runner
// was injected (e.g. wiring incomplete).
var errNoRunner = errCommand("no command runner wired")

type errCommand string

func (e errCommand) Error() string { return string(e) }
