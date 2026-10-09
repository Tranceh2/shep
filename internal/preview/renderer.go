package preview

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/effective"
	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
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
// preserved byte-for-byte by the TUI recomposition. Title is the picker
// heading of a custom command's section (config.PreviewTitle; "" draws it
// without one); built-in sections leave it empty and keep their own fixed
// headings.
type Section struct {
	Kind  string
	Title string
	Text  string
}

// defaultRenderer takes the ordered section list of each candidate from the
// settings resolver (see internal/effective for the precedence) and renders
// each named section — hardcoded built-ins (identity, git, workspace,
// active_pane, dir) or a declared [preview.commands.<name>] — with TTL
// caching.
type defaultRenderer struct {
	cfg      *config.Config
	settings *effective.Resolver
	// templates renders [preview.commands] and custom-source preview argv.
	templates *tmpl.Engine
	probes    config.Probes
	git       GitProvider
	runner    CommandRunner
	reader    PaneReader
	// snapshot is copied at construction and never mutated. A fresh renderer is
	// created for every successful Herdr generation replacement.
	snapshot *source.Snapshot
	cache    *Cache
}

// PaneReader is the live `herdr pane read` boundary retained by previews. All
// state derivation comes from the immutable snapshot instead.
type PaneReader interface {
	ReadPane(context.Context, string, int) (string, error)
}

// RendererOption configures a Renderer at construction.
type RendererOption func(*defaultRenderer)

// WithPaneReader injects the only live Herdr preview operation: pane capture.
func WithPaneReader(reader PaneReader) RendererOption {
	return func(r *defaultRenderer) { r.reader = reader }
}

// WithSnapshot supplies the resolved full Herdr generation used by workspace
// and agent-status preview sections. It is construction-only: callers build a
// new renderer for a new generation instead of mutating an in-flight one.
func WithSnapshot(snapshot source.Snapshot) RendererOption {
	return func(r *defaultRenderer) {
		copy := cloneSnapshot(snapshot)
		r.snapshot = &copy
	}
}

func cloneSnapshot(snapshot source.Snapshot) source.Snapshot {
	copy := snapshot
	copy.Workspaces = append([]source.Workspace(nil), snapshot.Workspaces...)
	copy.Tabs = append([]source.Tab(nil), snapshot.Tabs...)
	copy.Panes = append([]source.Pane(nil), snapshot.Panes...)
	return copy
}

// NewRenderer wires the production renderer from the process's settings
// resolver (each candidate's preview sections; its configuration's [preview]
// carries the caps and commands), the process's template engine (which
// renders preview command arguments), a binary probes snapshot, an optional
// GitProvider, and an optional CommandRunner (used for both the "dir"
// built-in and any declared preview.commands). settings and templates are
// required. A TTL cache (preview.cache_ttl) keeps cursor revisits
// responsive.
func NewRenderer(settings *effective.Resolver, templates *tmpl.Engine, probes config.Probes, git GitProvider, runner CommandRunner, opts ...RendererOption) Renderer {
	cfg := settings.Config()
	r := &defaultRenderer{
		cfg:       cfg,
		settings:  settings,
		templates: templates,
		probes:    probes,
		git:       git,
		runner:    runner,
		cache:     NewCache(time.Duration(cfg.Preview.CacheTTL)),
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
	key := PreviewCacheKeyWithCustomSources(cand, r.cfg.Preview, r.cfg.Sources.Custom)
	if cached, ok := r.cache.Get(key); ok {
		return cached, nil
	}

	names := r.settings.For(cand).Preview
	var blocks []string
	var sections []Section
	for _, name := range names {
		if block, ok := r.renderSection(ctx, cand, name); ok {
			blocks = append(blocks, block)
			sections = append(sections, Section{Kind: name, Title: r.sectionTitle(cand.Source, name), Text: block})
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
	case config.PreviewSessionInfo:
		return renderSessionInfoSection(cand), true
	case config.PreviewActivePane:
		return r.renderActivePaneSection(ctx, cand)
	case config.PreviewAgentStatus:
		return r.renderAgentStatusSection(ctx, cand)
	case config.PreviewDir:
		return r.renderDirSection(ctx, cand)
	default:
		if cmd, ok := customSourcePreviewCommand(r.cfg, cand.Source, name); ok {
			return r.renderCustomSourceCommand(ctx, cmd, cand)
		}
		cmd, ok := r.cfg.Preview.Commands[name]
		if !ok {
			return "", false
		}
		return r.renderCustomCommand(ctx, cmd, cand)
	}
}

// sectionTitle is the picker heading of section name for a candidate of
// sourceName: a custom command's configured or humanized title, "" for a
// built-in section (the picker draws those with fixed headings).
func (r *defaultRenderer) sectionTitle(sourceName, name string) string {
	if cmd, ok := customSourcePreviewCommand(r.cfg, sourceName, name); ok {
		return config.PreviewTitle(cmd.Title, name)
	}
	if cmd, ok := r.cfg.Preview.Commands[name]; ok {
		return config.PreviewTitle(cmd.Title, name)
	}
	return ""
}

func customSourcePreviewCommand(cfg *config.Config, sourceName, name string) (config.CustomSourcePreviewCommand, bool) {
	for _, customSource := range cfg.Sources.Custom {
		if customSource.Name == sourceName {
			cmd, ok := customSource.PreviewCommands[name]
			return cmd, ok
		}
	}
	return config.CustomSourcePreviewCommand{}, false
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

// renderSessionInfoSection renders only values already carried by a sessions
// candidate. It deliberately performs no directory, socket, pane, or command
// lookup so selecting a session cannot trigger cross-session inspection.
func renderSessionInfoSection(cand source.Candidate) string {
	state := "unavailable"
	switch cand.Meta["running"] {
	case "true":
		state = "running"
	case "false":
		state = "stopped"
	}
	lines := []string{
		"session",
		"  name: " + sessionInfoValue(cand.Meta["session_name"]),
		"  state: " + state,
		"  default: " + sessionBooleanValue(cand.Meta["default"]),
		"  session dir: " + sessionInfoValue(cand.Meta["session_dir"]),
		"  socket path: " + sessionInfoValue(cand.Meta["socket_path"]),
	}
	return strings.Join(lines, "\n")
}

func sessionInfoValue(value string) string {
	if value == "" {
		return "unavailable"
	}
	return value
}

func sessionBooleanValue(value string) string {
	if value == "true" || value == "false" {
		return value
	}
	return "unavailable"
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
	if workspaceID == "" || r.snapshot == nil {
		return "", false
	}
	lines := []string{"workspace"}
	tabs, panes := snapshotWorkspace(*r.snapshot, workspaceID)
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

// renderAgentStatusSection renders a static, at-open-time view of the
// candidate's own agent state, read from the immutable snapshot. It never
// looks at the pane focused globally in Herdr (where shep was launched from):
// every workspace row reports its own panes.
//
//   - An agent candidate (Meta["pane_id"] naming a pane of its workspace)
//     reports that pane's status, so an agent row never borrows a sibling
//     agent's state.
//   - Any other candidate reports the workspace's most important agent state
//     across all of its panes, by attention precedence blocked > working >
//     done > idle > unknown (see agentStatusRank). Panes with an empty
//     AgentStatus carry no agent (a plain shell pane, or an older Herdr) and
//     are ignored.
//
// ok=false means the section is skipped entirely: the candidate is not an
// active Herdr workspace, or no snapshot is wired. A workspace with no panes
// degrades to a "no active pane" note; one whose panes report no agent status
// at all shows "unknown". The section therefore normalizes "not reported" and
// an explicit "unknown" to the same display text, so the heading is always
// followed by a definite line. The "  status: " prefix is parsed by the TUI
// (internal/tui/preview_body.go statusLinePrefix) and must stay stable.
func (r *defaultRenderer) renderAgentStatusSection(ctx context.Context, cand source.Candidate) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.snapshot == nil {
		return "", false
	}
	lines := []string{"agent status"}
	_, panes := snapshotWorkspace(*r.snapshot, workspaceID)
	if len(panes) == 0 {
		lines = append(lines, "(no active pane)")
		return strings.Join(lines, "\n"), true
	}
	status, ok := paneAgentStatus(panes, cand.Meta["pane_id"])
	if !ok {
		status = workspaceAgentStatus(panes)
	}
	if status == "" {
		status = "unknown"
	}
	lines = append(lines, "  status: "+status)
	return strings.Join(lines, "\n"), true
}

// paneAgentStatus returns the AgentStatus of the pane with id paneID among
// panes. ok=false when paneID is empty or names no pane of the workspace (a
// non-agent candidate, or a stale id), so the caller falls back to the
// workspace aggregate.
func paneAgentStatus(panes []source.Pane, paneID string) (string, bool) {
	if paneID == "" {
		return "", false
	}
	for _, p := range panes {
		if p.ID == paneID {
			return p.AgentStatus, true
		}
	}
	return "", false
}

// workspaceAgentStatus returns the most important agent state reported by
// panes, by agentStatusRank, or "" when no pane reports one. Ties keep the
// first pane in snapshot order, so the result is deterministic.
func workspaceAgentStatus(panes []source.Pane) string {
	best, bestRank := "", -1
	for _, p := range panes {
		if p.AgentStatus == "" {
			continue
		}
		if rank := agentStatusRank(p.AgentStatus); rank > bestRank {
			best, bestRank = p.AgentStatus, rank
		}
	}
	return best
}

// agentStatusRank orders Herdr agent states by how urgently they need the
// user: a blocked agent waits on input, a working one is busy, a done one has
// output to review, an idle one needs nothing. "unknown" and any value this
// build does not recognise rank lowest, so they surface only when nothing
// better is known.
func agentStatusRank(status string) int {
	switch status {
	case "blocked":
		return 4
	case "working":
		return 3
	case "done":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

// renderActivePaneSection renders the active pane's captured terminal
// buffer, capped at cfg.Preview.MaxLines. ok=false means the section is
// skipped entirely (non-herdr candidate, no driver, no panes, read error,
// or empty/whitespace-only buffer). An unavailable or empty capture never
// produces a heading-only section — the TUI must not show an empty "Active
// pane" block. Non-TUI consumers (shep preview) also see the
// section omitted, since there is no useful content to display.
func (r *defaultRenderer) renderActivePaneSection(ctx context.Context, cand source.Candidate) (string, bool) {
	workspaceID := cand.Meta["workspace_id"]
	if workspaceID == "" || r.snapshot == nil || r.reader == nil {
		return "", false
	}
	_, panes := snapshotWorkspace(*r.snapshot, workspaceID)
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

func snapshotWorkspace(snapshot source.Snapshot, workspaceID string) ([]source.Tab, []source.Pane) {
	tabs := make([]source.Tab, 0)
	panes := make([]source.Pane, 0)
	for _, tab := range snapshot.Tabs {
		if tab.WorkspaceID == workspaceID {
			tabs = append(tabs, tab)
		}
	}
	for _, pane := range snapshot.Panes {
		if pane.WorkspaceID == workspaceID {
			panes = append(panes, pane)
		}
	}
	return tabs, panes
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
	argv, err := ParseCommand(r.templates, cmd.Command, commandData(cand))
	if err != nil {
		return "", false
	}
	return r.runCommand(ctx, argv, r.cfg.Preview.Timeout, r.cfg.Preview.MaxLines, cand)
}

func (r *defaultRenderer) renderCustomSourceCommand(ctx context.Context, cmd config.CustomSourcePreviewCommand, cand source.Candidate) (string, bool) {
	if r.runner == nil || len(cmd.Command) == 0 || cmd.Command[0] == "" {
		return "", false
	}
	argv, err := renderArgv(r.templates, cmd.Command, commandData(cand))
	if err != nil {
		return "", false
	}
	timeout := cmd.Timeout
	maxLines := cmd.MaxLines
	if timeout <= 0 {
		timeout = r.cfg.Preview.Timeout
	}
	if maxLines <= 0 {
		maxLines = r.cfg.Preview.MaxLines
	}
	return r.runCommand(ctx, argv, timeout, maxLines, cand)
}

func (r *defaultRenderer) runCommand(ctx context.Context, argv []string, timeout config.Duration, maxLines int, cand source.Candidate) (string, bool) {
	if r.runner == nil || len(argv) == 0 || argv[0] == "" {
		return "", false
	}
	runCtx, cancel := boundedContext(ctx, timeout)
	defer cancel()
	out, err := r.runner.Run(runCtx, argv, cand.Path, maxLines)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// commandData is the template data preview command arguments render with:
// the candidate's shared data, except that .Path is the normalized path when
// one is known (the same path the preview sections inspect).
func commandData(cand source.Candidate) tmpl.Data {
	data := source.TemplateData(cand)
	data.Path = renderPath(cand)
	return data
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
// the documented "dir" built-in. Every variant prints a compact listing sized
// for a narrow preview pane: one entry name per line, directories first,
// hidden entries included but never the implied "." and "..". Long-format
// columns (permissions, owner, group, size, date) are deliberately omitted —
// they drown the names the user scans for.
//
//   - lsd: --almost-all --oneline --group-directories-first (lsd >= 1.0; the
//     last flag is lsd's alias of --group-dirs=first).
//   - eza: --all (eza needs -aa to add "." and "..") --oneline
//     --group-directories-first (eza >= 0.18).
//   - ls: -1Ap — one per line, almost-all, a trailing "/" marks directories
//     in place of grouping, which BSD and GNU ls do not share a flag for.
//
// lsd and eza are forced to --color=always so the "dir" section shows their
// real colored listing — the reason to prefer them over plain ls in the first
// place. Forcing color also keeps the output deterministic regardless of the
// invoking process's $TERM/color-profile detection (which would otherwise
// vary whether lsd/eza auto-detect a color-capable terminal), so this built-in
// section's TTL-cached result stays stable for the same input. Icons are
// forced the same way (--icon=always / --icons=always): the output is piped,
// so their "auto" default would print none. internal/tui/model.go's
// truncateToWidth is ANSI-aware (it delegates to charmbracelet/x/ansi.Truncate),
// which is what makes it safe to carry real color codes through this section
// without corrupting truncation at narrow widths. ls has no portable color
// flag (BSD -G and GNU --color differ) and stays plain.
func dirArgv(path string) []string {
	if _, err := dirLookPath("lsd"); err == nil {
		return []string{"lsd", "--almost-all", "--oneline", "--group-directories-first", "--icon=always", "--color=always", path}
	}
	if _, err := dirLookPath("eza"); err == nil {
		return []string{"eza", "--all", "--oneline", "--group-directories-first", "--icons=always", "--color=always", path}
	}
	return []string{"ls", "-1Ap", path}
}

// readPanePreview bounds a ReadPane call by herdrPreviewTimeout.
func (r *defaultRenderer) readPanePreview(ctx context.Context, paneID string, lines int) (string, error) {
	qctx, cancel := context.WithTimeout(ctx, herdrPreviewTimeout)
	defer cancel()
	return r.reader.ReadPane(qctx, paneID, lines)
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
	return formatGitSummary(sum, cand.Meta), true
}

// renderPath prefers the post-dedup/post-symlink normalised path and falls
// back to the raw path so output is never empty.
func renderPath(cand source.Candidate) string {
	if cand.NormalizedPath != "" {
		return cand.NormalizedPath
	}
	return cand.Path
}
