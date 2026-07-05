package preview

import (
	"context"
	"errors"
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
	cache  *Cache
}

// NewRenderer wires the production renderer from a preview config, a binary
// probes snapshot, an optional GitProvider, and an optional CommandRunner. When
// preview.Command is set the runner path is used; otherwise the built-in or
// declarative-section layout is rendered. A TTL cache (preview.cache_ttl)
// keeps cursor revisits responsive.
func NewRenderer(cfg config.PreviewConfig, probes config.Probes, git GitProvider, runner CommandRunner) Renderer {
	return &defaultRenderer{
		cfg:    cfg,
		probes: probes,
		git:    git,
		runner: runner,
		cache:  NewCache(time.Duration(cfg.CacheTTL)),
	}
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

// renderSections (WP-2) renders [[preview.sections]] in declaration order using
// the declared field names (builtin) or a git summary (git).
func (r *defaultRenderer) renderSections(ctx context.Context, cand source.Candidate) string {
	blocks := make([]string, 0, len(r.cfg.Sections))
	for _, sec := range r.cfg.Sections {
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
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n")
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
