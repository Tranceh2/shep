// Package effective resolves every per-candidate setting — the row
// presentation, the preview sections, the template a freshly created
// workspace gets and the workspace_name format — with one precedence rule.
// Per setting, the first tier that defines it wins:
//
//  0. the candidate's own data: a [[workspaces]] entry's own keys for the
//     row the workspaces source made from it; the template or command a
//     configured workspace or a custom source row carries (Meta template,
//     command, close_on_exit); the template of the group it was picked
//     through (Meta parent_template);
//  1. for the preview sections only: the [[workspaces]] entries in the
//     candidate's directory, in declaration order (a workspaces row only
//     takes its own entry);
//  2. the [[wildcards]] matching the candidate's path or its base name, in
//     declaration order;
//  3. the candidate source's own table ([sources.<name>],
//     [[sources.custom]]);
//  4. the built-in defaults (the presentation defaults table, a source's
//     preview fallbacks, [preview].default, [defaults].template,
//     [general].workspace_name).
//
// Each setting is resolved on its own: an entry or wildcard that does not
// define a setting never stops the scan for it, and an explicitly empty
// value (icon = "", preview = []) does define it. Sessions are Herdr daemon
// identities, not directories, so tiers 1 and 2 never apply to them.
//
// An entry's presentation, template, command and close_on_exit are its own
// row's alone: several entries often share one directory (a group, a
// command entry and a template entry over the same checkout), and opening
// that directory from another source must not run an entry's command or
// draw as the entry. Only an entry's preview sections describe the
// directory itself, so they also apply to the other rows in it.
//
// A tier names a template only when that template exists, so a custom row
// naming an unknown template falls through to the next tier. A group
// entry's template is for the rows picked through the group (tier 0).
// [[workspaces]] and the source tables set no workspace_name.
//
// Matching uses the candidate path normalized once (absolute, symlinks
// resolved) together with the path as reported, so a candidate resolves the
// same in a source's own tab as in the deduplicated all tab, and a pattern
// written against either spelling matches. Resolving reads the filesystem
// (symlinks, directory identity) and caches what it learns per path, so a
// Resolver must never run on the picker's Update or View path: producers
// resolve candidates before they reach the picker.
package effective

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/source"
)

// Settings is everything resolved for one candidate.
type Settings struct {
	// Presentation is how the picker draws the candidate's row.
	Presentation source.Presentation
	// Preview is the ordered list of preview sections. It is shared with the
	// configuration and must not be modified.
	Preview []string
	// Template names the [templates.<name>] a freshly created workspace
	// gets. It is empty when Command applies or when nothing does (a plain
	// shell).
	Template string
	// Command, when set, runs in the root pane of a freshly created
	// workspace instead of a template; CloseOnExit closes that pane once the
	// command returns.
	Command     string
	CloseOnExit bool
	// WorkspaceName is the workspace_name template that names a freshly
	// created workspace; empty leaves internal/workspacename's default.
	WorkspaceName string
	// NormalizedPath is the candidate's path made absolute with symlinks
	// resolved: the path every match above used. Empty when the candidate
	// has no path.
	NormalizedPath string
}

// maxCachedPaths bounds the per-path cache. Real candidate sets stay far
// below it; past it paths are still resolved, just not remembered.
const maxCachedPaths = 8192

// Resolver resolves candidates against one configuration. Build it once per
// configuration with New; it is safe for concurrent use.
type Resolver struct {
	cfg *config.Config
	// base is every source's presentation with its defaults (tiers 3 and 4).
	base       config.Presentations
	entries    []entry
	wildcards  []wildcard
	iconColors []string

	normalize func(string) (string, error)
	stat      func(string) (os.FileInfo, error)
	// paths caches a *pathMatch per candidate path.
	paths  sync.Map
	cached atomic.Int64
}

// entry is one [[workspaces]] entry prepared for matching.
type entry struct {
	ws *config.WorkspaceConfig
	// id is the entry's identity, as the workspaces source reports it in
	// Meta entry_id.
	id string
	// path is the entry's normalized path, "" when it has none; info is
	// that directory's identity, nil when it cannot be read.
	path string
	info os.FileInfo
}

// wildcard is one [[wildcards]] entry with its pattern split into segments.
type wildcard struct {
	w    *config.WildcardConfig
	segs []string
}

// pathMatch is what the resolver learned about one candidate path: its
// normalized form and the [[workspaces]] entries and [[wildcards]] that
// apply to it, as indexes in declaration order.
type pathMatch struct {
	normalized string
	entries    []int
	wildcards  []int
}

// New returns a Resolver for cfg (config.Defaults when nil). It reads the
// filesystem to resolve the [[workspaces]] paths.
func New(cfg *config.Config) *Resolver {
	return newResolver(cfg, pathutil.Normalize, os.Stat)
}

func newResolver(cfg *config.Config, normalize func(string) (string, error), stat func(string) (os.FileInfo, error)) *Resolver {
	if cfg == nil {
		cfg = config.Defaults()
	}
	r := &Resolver{cfg: cfg, base: cfg.Presentations(), normalize: normalize, stat: stat}
	for i := range cfg.Workspaces {
		ws := &cfg.Workspaces[i]
		path := ws.Path
		if expanded, err := pathutil.ExpandTilde(path); err == nil {
			path = expanded
		}
		e := entry{ws: ws, id: source.WorkspaceEntryIdentity(path, *ws)}
		if path != "" {
			e.path = r.normalizeOr(path)
			if info, err := stat(e.path); err == nil {
				e.info = info
			}
		}
		r.entries = append(r.entries, e)
	}
	for i := range cfg.Wildcards {
		w := &cfg.Wildcards[i]
		r.wildcards = append(r.wildcards, wildcard{w: w, segs: patternSegments(w.Pattern)})
	}
	r.iconColors = r.collectIconColors()
	return r
}

// Config returns the configuration r resolves against.
func (r *Resolver) Config() *config.Config { return r.cfg }

// For resolves every setting of c.
func (r *Resolver) For(c source.Candidate) Settings {
	pm := r.match(c)
	own := r.ownEntry(c, pm)
	s := Settings{
		Presentation:   r.presentation(c, pm, own),
		Preview:        r.preview(c, pm, own),
		WorkspaceName:  r.workspaceName(c, pm),
		NormalizedPath: pm.normalized,
	}
	s.Template, s.Command, s.CloseOnExit = r.template(c, pm)
	return s
}

// Presentation resolves c's row presentation alone. It reads the filesystem
// only when a [[wildcards]] pattern could apply, or to find the entry of a
// workspaces row that carries no entry identity.
func (r *Resolver) Presentation(c source.Candidate) source.Presentation {
	var pm *pathMatch
	if len(r.wildcards) > 0 {
		pm = r.match(c)
	}
	return r.presentation(c, pm, r.ownEntry(c, pm))
}

// Attach resolves the presentation of every candidate and attaches it
// (source.Candidate.Presentation). Candidates resolving alike share one
// value.
func (r *Resolver) Attach(candidates []source.Candidate) {
	shared := make(map[source.Presentation]*source.Presentation)
	for i := range candidates {
		p := r.Presentation(candidates[i])
		ptr, ok := shared[p]
		if !ok {
			ptr = new(source.Presentation)
			*ptr = p
			shared[p] = ptr
		}
		candidates[i].Presentation = ptr
	}
}

// Workspace returns the [[workspaces]] entry the workspaces source made c
// from, false when c is not such a row.
func (r *Resolver) Workspace(c source.Candidate) (config.WorkspaceConfig, bool) {
	if i := r.ownEntry(c, nil); i >= 0 {
		return *r.entries[i].ws, true
	}
	return config.WorkspaceConfig{}, false
}

// IconColors returns every icon color a resolved presentation can name, each
// once, in a stable order: the picker prepares one style per color.
func (r *Resolver) IconColors() []string { return r.iconColors }

func (r *Resolver) collectIconColors() []string {
	var out []string
	seen := make(map[string]bool)
	add := func(ref string) {
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	b := r.base
	for _, p := range []config.RowPresentation{b.Herdr, b.HerdrTab, b.HerdrPane, b.Sessions, b.Workspaces, b.Zoxide, b.Projects, b.Agents, b.Other} {
		add(p.IconColor)
	}
	for _, custom := range r.cfg.Sources.Custom {
		add(b.Custom[custom.Name].IconColor)
	}
	for _, e := range r.entries {
		if e.ws.IconColor != nil {
			add(*e.ws.IconColor)
		}
	}
	for _, w := range r.wildcards {
		if w.w.IconColor != nil {
			add(*w.w.IconColor)
		}
	}
	return out
}

// pathTiers reports whether the directory tiers (same-directory entries'
// previews and wildcards) apply to c.
func pathTiers(c source.Candidate) bool { return c.Source != config.SourceSessions }

// presentation layers c's own entry and the matching wildcards over its
// source's presentation, field by field. pm may be nil when no wildcard
// exists.
func (r *Resolver) presentation(c source.Candidate, pm *pathMatch, own int) source.Presentation {
	base := r.basePresentation(c.Source)
	out := [5]string{base.Icon, base.IconColor, base.Label, base.Detail, base.Marker}
	var set [5]bool
	apply := func(p *config.Presentation) {
		for k, v := range [5]*string{p.Icon, p.IconColor, p.LabelFormat, p.DetailFormat, p.MarkerFormat} {
			if v != nil && !set[k] {
				out[k], set[k] = *v, true
			}
		}
	}
	if own >= 0 {
		apply(&r.entries[own].ws.Presentation)
	}
	if pm != nil && pathTiers(c) {
		for _, i := range pm.wildcards {
			apply(&r.wildcards[i].w.Presentation)
		}
	}
	return source.Presentation{Icon: out[0], IconColor: out[1], Label: out[2], Detail: out[3], Marker: out[4]}
}

// basePresentation is a source's own presentation over the built-in
// defaults: a [[sources.custom]] provider by name, and anything else (a
// direct --path candidate) with the defaults for rows of no source.
func (r *Resolver) basePresentation(name string) config.RowPresentation {
	b := &r.base
	switch name {
	case config.SourceHerdr:
		return b.Herdr
	case config.SourceSessions:
		return b.Sessions
	case config.SourceWorkspaces:
		return b.Workspaces
	case config.SourceZoxide:
		return b.Zoxide
	case config.SourceProjects:
		return b.Projects
	case config.SourceAgents:
		return b.Agents
	}
	if p, ok := b.Custom[name]; ok {
		return p
	}
	return b.Other
}

// preview resolves the section list. Past the tiers, a source's own list
// (an explicit empty one included) wins; sessions then fall back to
// session_info, because a session has no path, repository or pane for the
// general sections to describe; then [preview].default, then identity.
func (r *Resolver) preview(c source.Candidate, pm *pathMatch, own int) []string {
	if own >= 0 && r.entries[own].ws.Preview != nil {
		return r.entries[own].ws.Preview
	}
	if pathTiers(c) {
		if c.Source != config.SourceWorkspaces {
			for _, i := range pm.entries {
				if names := r.entries[i].ws.Preview; names != nil {
					return names
				}
			}
		}
		for _, i := range pm.wildcards {
			if names := r.wildcards[i].w.Preview; names != nil {
				return names
			}
		}
	}
	if names := r.sourcePreview(c.Source); names != nil {
		return names
	}
	if c.Source == config.SourceSessions {
		return []string{config.PreviewSessionInfo}
	}
	if len(r.cfg.Preview.Default) > 0 {
		return r.cfg.Preview.Default
	}
	return []string{config.PreviewIdentity}
}

// sourcePreview returns a source's own preview list, nil when it has none.
func (r *Resolver) sourcePreview(name string) []string {
	s := &r.cfg.Sources
	switch name {
	case config.SourceHerdr:
		return s.Herdr.Preview
	case config.SourceSessions:
		return s.Sessions.Preview
	case config.SourceWorkspaces:
		return s.Workspaces.Preview
	case config.SourceZoxide:
		return s.Zoxide.Preview
	case config.SourceProjects:
		return s.Projects.Preview
	case config.SourceAgents:
		return s.Agents.Preview
	}
	for _, custom := range s.Custom {
		if custom.Name == name {
			return custom.Preview
		}
	}
	return nil
}

// template resolves what a freshly created workspace runs: a template name,
// or a command with its close_on_exit. A [[workspaces]] entry's own launch
// reaches only its own row (through Meta), and a group entry's template only
// the rows picked through the group (Meta parent_template).
func (r *Resolver) template(c source.Candidate, pm *pathMatch) (name, command string, closeOnExit bool) {
	if r.hasTemplate(c.Meta["template"]) {
		return c.Meta["template"], "", false
	}
	if cmd := c.Meta["command"]; cmd != "" {
		// The writers emit the literal "true"; any other value is false.
		return "", cmd, c.Meta["close_on_exit"] == "true"
	}
	if r.hasTemplate(c.Meta["parent_template"]) {
		return c.Meta["parent_template"], "", false
	}
	if pathTiers(c) {
		for _, i := range pm.wildcards {
			if w := r.wildcards[i].w; r.hasTemplate(w.Template) {
				return w.Template, "", false
			}
		}
	}
	if r.hasTemplate(r.cfg.Defaults.Template) {
		return r.cfg.Defaults.Template, "", false
	}
	return "", "", false
}

func (r *Resolver) hasTemplate(name string) bool {
	if name == "" {
		return false
	}
	_, ok := r.cfg.Templates[name]
	return ok
}

// workspaceName resolves the workspace_name format: the first matching
// wildcard that sets one, else [general].workspace_name.
func (r *Resolver) workspaceName(c source.Candidate, pm *pathMatch) string {
	if pathTiers(c) {
		for _, i := range pm.wildcards {
			if format := r.wildcards[i].w.WorkspaceName; format != "" {
				return format
			}
		}
	}
	return r.cfg.General.WorkspaceName
}

// ownEntry returns the index of the [[workspaces]] entry the workspaces
// source made c from, -1 when c is not such a row. Rows carry the entry's
// identity (Meta entry_id), shared by entries that differ only in name, so
// the name breaks the tie; a row without an identity is matched by name
// within its directory. pm may be nil.
func (r *Resolver) ownEntry(c source.Candidate, pm *pathMatch) int {
	if c.Source != config.SourceWorkspaces || len(r.entries) == 0 {
		return -1
	}
	name := c.Meta["workspace_name"]
	if name == "" {
		name = c.Label
	}
	if id := c.Meta["entry_id"]; id != "" {
		first := -1
		for i, e := range r.entries {
			if e.id != id {
				continue
			}
			if e.ws.Name == name {
				return i
			}
			if first < 0 {
				first = i
			}
		}
		return first
	}
	if pm == nil {
		pm = r.match(c)
	}
	for _, i := range pm.entries {
		if r.entries[i].ws.Name == name {
			return i
		}
	}
	return -1
}

// match returns what applies to c's path, from the cache when it holds it.
func (r *Resolver) match(c source.Candidate) *pathMatch {
	raw := c.Path
	if raw == "" {
		raw = c.NormalizedPath
	}
	if raw == "" {
		return &pathMatch{}
	}
	if cached, ok := r.paths.Load(raw); ok {
		return cached.(*pathMatch)
	}
	pm := r.compute(raw)
	if r.cached.Load() < maxCachedPaths {
		if _, loaded := r.paths.LoadOrStore(raw, pm); !loaded {
			r.cached.Add(1)
		}
	}
	return pm
}

// compute matches raw against every entry and wildcard.
func (r *Resolver) compute(raw string) *pathMatch {
	pm := &pathMatch{normalized: r.normalizeOr(raw)}
	var info os.FileInfo
	statted := false
	for i, e := range r.entries {
		if e.path == "" {
			continue
		}
		if e.path == pm.normalized {
			pm.entries = append(pm.entries, i)
			continue
		}
		// Different spellings of one directory (a case-insensitive
		// filesystem) are the same directory by identity.
		if e.info == nil {
			continue
		}
		if !statted {
			info, _ = r.stat(pm.normalized)
			statted = true
		}
		if info != nil && os.SameFile(info, e.info) {
			pm.entries = append(pm.entries, i)
		}
	}
	if len(r.wildcards) > 0 {
		paths := [][]string{splitPath(pm.normalized), {filepath.Base(pm.normalized)}}
		if cleaned := filepath.Clean(raw); cleaned != pm.normalized {
			paths = append(paths, splitPath(cleaned), []string{filepath.Base(cleaned)})
		}
		for i, w := range r.wildcards {
			for _, path := range paths {
				if matchSegments(w.segs, path) {
					pm.wildcards = append(pm.wildcards, i)
					break
				}
			}
		}
	}
	return pm
}

// normalizeOr normalizes path, keeping it as is when it cannot be.
func (r *Resolver) normalizeOr(path string) string {
	if normalized, err := r.normalize(path); err == nil && normalized != "" {
		return normalized
	}
	return path
}

// patternSegments splits a [[wildcards]] pattern into path segments, a
// leading "~" expanded to the home directory (left as is when the home
// directory is unknown). An empty pattern matches nothing.
func patternSegments(pattern string) []string {
	if pattern == "" {
		return nil
	}
	if expanded, err := pathutil.ExpandTilde(pattern); err == nil {
		pattern = expanded
	}
	return splitPath(pattern)
}

func splitPath(p string) []string { return strings.Split(filepath.ToSlash(p), "/") }

// matchSegments reports whether a pattern matches a path, segment by
// segment: "**" matches zero or more whole segments; any other segment
// matches exactly one with filepath.Match (a malformed segment matches
// nothing, so a bad glob never fails resolution).
func matchSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		if matchSegments(pat[1:], path) {
			return true
		}
		if len(path) == 0 {
			return false
		}
		return matchSegments(pat, path[1:])
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], path[1:])
}
