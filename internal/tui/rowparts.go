package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/fuzzy"
	"github.com/tranceh2/shep/internal/tmpl"
)

// rowparts.go turns a row presentation — the icon, label_format,
// detail_format and marker_format templates of config.RowPresentation —
// into the parts one list row draws:
//
//	[icon] label  detail                                   marker
//
// Each template is rendered with the shared engine and data model, its
// output decoded into styled text and live markers (tmpl.Segments), every
// text segment made plain (plainText) and every live marker resolved for the
// row: the status glyph, the pin, "current", the group chevron, "missing".
// Parts are built once per row view (see buildRowView); only the working
// status glyph's spinner frame is drawn per frame.

// The parts of a row presentation, in template order.
const (
	partIcon = iota
	partLabel
	partDetail
	partMarker
	numParts
)

// rowFormat is one row presentation prepared for rendering: its four
// templates, also joined by tmpl.PartSeparator so a row renders all of them
// with a single template execution, and its icon color.
type rowFormat struct {
	parts  [numParts]string
	joined string
	// icon indexes rowFormats.iconRefs (and so rowStyles.icons).
	icon int
}

// rowFormats is the prepared presentation of every kind of row a candidate's
// own resolved presentation does not cover — Herdr tab and pane rows nested
// under a workspace, and candidates that carry none (built directly rather
// than by the command layer's producers) — plus the index of every icon color
// a row can name. Built once per Model from Layout.Presentation and
// Layout.IconColors, it is immutable once built and shared.
type rowFormats struct {
	herdr, tab, pane, sessions, workspaces, zoxide, projects, agents, other rowFormat
	custom                                                                  map[string]*rowFormat
	// iconRefs are the distinct icon color references, in first-use order;
	// iconIndex maps each to its position.
	iconRefs  []string
	iconIndex map[string]int
}

func newRowFormats(p config.Presentations, iconColors []string) *rowFormats {
	f := &rowFormats{iconIndex: make(map[string]int)}
	prepare := func(rp config.RowPresentation) rowFormat {
		return f.prepare(rp.Icon, rp.Label, rp.Detail, rp.Marker, f.addIconRef(rp.IconColor))
	}
	f.herdr = prepare(p.Herdr)
	f.tab = prepare(p.HerdrTab)
	f.pane = prepare(p.HerdrPane)
	f.sessions = prepare(p.Sessions)
	f.workspaces = prepare(p.Workspaces)
	f.zoxide = prepare(p.Zoxide)
	f.projects = prepare(p.Projects)
	f.agents = prepare(p.Agents)
	f.other = prepare(p.Other)
	if len(p.Custom) > 0 {
		f.custom = make(map[string]*rowFormat, len(p.Custom))
		for name, rp := range p.Custom {
			prepared := prepare(rp)
			f.custom[name] = &prepared
		}
	}
	for _, ref := range iconColors {
		f.addIconRef(ref)
	}
	return f
}

// addIconRef registers an icon color reference and returns its index.
func (f *rowFormats) addIconRef(ref string) int {
	i, ok := f.iconIndex[ref]
	if !ok {
		i = len(f.iconRefs)
		f.iconIndex[ref] = i
		f.iconRefs = append(f.iconRefs, ref)
	}
	return i
}

func (f *rowFormats) prepare(icon, label, detail, marker string, iconStyle int) rowFormat {
	parts := [numParts]string{icon, label, detail, marker}
	return rowFormat{parts: parts, joined: strings.Join(parts[:], tmpl.PartSeparator), icon: iconStyle}
}

// rowFormat selects row's presentation: Herdr tab and pane rows nested under
// a workspace have their own; any other row draws with its candidate's
// resolved presentation. A candidate that carries none draws as its source —
// flat pane rows of the agents view (and pane rows tagged as agents) as
// agents, a [[sources.custom]] provider by name, anything else (a direct
// --path candidate) with the defaults for rows of no source.
func (m Model) rowFormat(row Row) rowFormat {
	f := m.formats
	c := row.Candidate
	agent := false
	switch row.Kind {
	case RowTab:
		return f.tab
	case RowPane:
		if row.Depth > 0 && c.Meta["kind"] != "agent" {
			return f.pane
		}
		agent = true
	}
	fallback := f.sourceFormat(c.Source, agent)
	p := c.Presentation
	if p == nil {
		return *fallback
	}
	// A color no presentation was prepared with (a candidate resolved
	// against another configuration) keeps its source's icon color.
	icon, ok := f.iconIndex[p.IconColor]
	if !ok {
		icon = fallback.icon
	}
	return f.prepare(p.Icon, p.Label, p.Detail, p.Marker, icon)
}

// sourceFormat is the prepared presentation of a source's rows (agent: a
// pane row drawn as an agent).
func (f *rowFormats) sourceFormat(name string, agent bool) *rowFormat {
	if agent {
		return &f.agents
	}
	switch name {
	case config.SourceHerdr:
		return &f.herdr
	case config.SourceSessions:
		return &f.sessions
	case config.SourceWorkspaces:
		return &f.workspaces
	case config.SourceZoxide:
		return &f.zoxide
	case config.SourceProjects:
		return &f.projects
	case config.SourceAgents:
		return &f.agents
	}
	if custom, ok := f.custom[name]; ok {
		return custom
	}
	return &f.other
}

// renderParts renders f's four templates against d: all at once through the
// joined template, or, when that fails (a template that errors for this row,
// or a separator rune a template wrote itself), one by one so a failure only
// costs its own part. ok reports the parts that rendered.
func renderParts(e *tmpl.Engine, f *rowFormat, d tmpl.Data) (out [numParts]string, ok [numParts]bool) {
	if rendered, err := e.Render(f.joined, d); err == nil {
		rest := rendered
		split := true
		for i := range numParts - 1 {
			before, after, found := strings.Cut(rest, tmpl.PartSeparator)
			if !found {
				split = false
				break
			}
			out[i], rest = before, after
		}
		if split && !strings.Contains(rest, tmpl.PartSeparator) {
			out[numParts-1] = rest
			return out, [numParts]bool{true, true, true, true}
		}
	}
	for i, format := range f.parts {
		text, err := e.Render(format, d)
		out[i], ok[i] = text, err == nil
	}
	return out, ok
}

// liveValues are what a row's live markers show, resolved for one row.
type liveValues struct {
	// status is the agent status word the status marker draws: an open
	// workspace's aggregate (aggregate true) or a pane or agent row's own.
	status    string
	aggregate bool
	pinned    bool
	// pinColumn keeps an unpinned row's pin cells as blanks, so the pins of
	// the view, and the markers before them, line up (see Model.pinColumn).
	pinColumn bool
	current   bool
	group     bool
	missing   bool
}

// liveValues resolves row's live markers from the model: the tree's agent
// statuses, the ranking snapshot's pins and the current pane.
func (m Model) liveValues(row Row) liveValues {
	c := row.Candidate
	v := liveValues{
		current: m.containsCurrentPane(row),
		group:   c.Meta["group"] == "true",
		missing: c.Missing,
	}
	switch {
	case row.Kind == RowPane || c.Source == config.SourceAgents:
		v.status = c.Meta["agent_status"]
	case row.Kind == RowCandidate && c.Source == config.SourceHerdr:
		v.status, v.aggregate = m.tree.WorkspaceAgentStatus(c.Meta["workspace_id"]), true
	}
	if row.Kind == RowCandidate {
		v.pinned = m.rankingSnapshot.IsPinned(c)
		v.pinColumn = m.pinColumn
	}
	return v
}

// runRole selects how one run of a part renders.
type runRole uint8

const (
	// rolePart is the part's own role: the icon color, row.label (or
	// row.descendant), row.detail or row.marker.
	rolePart runRole = iota
	roleMuted
	roleAccent
	rolePin
	roleMissing
	roleStatusIdle
	roleStatusWorking
	roleStatusBlocked
	roleStatusDone
	roleStatusUnknown
)

// run is a stretch of a part's text drawn in one style.
type run struct {
	end  int // byte offset in part.text where the run ends
	role runRole
	bold bool
	// live marks text a live marker produced: it is never scored for
	// highlights nor shown in the preview title.
	live bool
}

// part is one drawn part of a row: plain display text with its style runs
// and cell width. A nil runs means one run in the part's own role.
type part struct {
	text  string
	runs  []run
	width int
	// hl marks the runes of text the query matched; nil when none did.
	hl []bool
}

func plainPart(text string) part {
	return part{text: text, width: ansi.StringWidth(text)}
}

// piece is one decoded segment on its way into a part.
type piece struct {
	text   string
	role   runRole
	bold   bool
	live   bool
	absent bool // a live marker with nothing to show for this row
	// reserved is a blank standing in for a marker the row does not show,
	// kept by the blank trimming and collapsing so the column stays.
	reserved bool
}

// workingPlaceholder stands in for the spinner frame a working status glyph
// draws: every frame is one cell, like it.
const workingPlaceholder = "⠿"

// partShape selects how a part's blanks are normalized (see buildPart).
type partShape uint8

const (
	// shapeIcon keeps the icon's own blanks: a Nerd Font glyph is followed by
	// a space because it draws wider than the one cell it measures.
	shapeIcon partShape = iota
	// shapeText trims the label and detail.
	shapeText
	// shapeMarker trims the marker and collapses its inner blank runs.
	shapeMarker
)

// buildPart builds one part from a rendered template. An absent live value
// takes the blank run beside it (the one before it, else the one after), so
// "{{ current }} {{ status }} {{ pin }}" leaves no gap for the markers a row
// does not show; the label, detail and marker are trimmed, and the marker
// also collapses every inner blank run to one blank.
func buildPart(raw string, live *liveValues, set *IconSet, shape partShape) part {
	if !tmpl.HasMarkup(raw) {
		text := plainText(raw)
		if shape != shapeIcon {
			text = strings.Trim(text, " ")
		}
		if shape == shapeMarker {
			text = collapseBlanks(text)
		}
		return plainPart(text)
	}
	var segBuf [16]tmpl.Segment
	var pieceBuf [16]piece
	pieces := pieceBuf[:0]
	for _, seg := range tmpl.AppendSegments(segBuf[:0], raw) {
		if seg.Live == tmpl.LiveNone {
			if text := plainText(seg.Text); text != "" {
				pieces = append(pieces, piece{text: text, role: styleRole(seg.Style), bold: seg.Style&tmpl.StyleBold != 0})
			}
			continue
		}
		pieces = append(pieces, live.piece(seg, set))
	}
	for i := range pieces {
		if pieces[i].absent {
			takeBlankBeside(pieces, i)
		}
	}
	switch shape {
	case shapeMarker:
		collapsePieces(pieces)
		pieces = trimPieces(pieces)
	case shapeText:
		pieces = trimPieces(pieces)
	}
	return joinPieces(pieces)
}

// styleRole maps a template style to the role of its run.
func styleRole(s tmpl.Style) runRole {
	switch {
	case s&tmpl.StyleMuted != 0:
		return roleMuted
	case s&tmpl.StyleAccent != 0:
		return roleAccent
	default:
		return rolePart
	}
}

// piece resolves one live marker for the row. The status glyph, the pin and
// "missing" have roles of their own; "current" and the group chevron are
// text in the style they were written in.
func (v *liveValues) piece(seg tmpl.Segment, set *IconSet) piece {
	text := func(show bool, s string, role runRole, bold bool) piece {
		if !show || s == "" {
			return piece{live: true, absent: true}
		}
		return piece{text: s, role: role, bold: bold, live: true}
	}
	bold := seg.Style&tmpl.StyleBold != 0
	switch seg.Live {
	case tmpl.LiveStatus:
		glyph, role := statusPiece(v.status, set)
		if v.aggregate && v.status == "idle" {
			// In the list the aggregate answers "which workspace needs me",
			// so the resting state reads muted; pane and agent glyphs keep
			// Herdr's colors.
			role = roleMuted
		}
		return text(glyph != "", glyph, role, false)
	case tmpl.LivePin:
		if !v.pinned && v.pinColumn && set.Pinned != "" {
			return piece{text: strings.Repeat(" ", ansi.StringWidth(set.Pinned)), role: rolePin, live: true, reserved: true}
		}
		return text(v.pinned, set.Pinned, rolePin, false)
	case tmpl.LiveCurrent:
		return text(v.current, currentMarker, styleRole(seg.Style), bold)
	case tmpl.LiveGroup:
		return text(v.group, set.Group, styleRole(seg.Style), bold)
	case tmpl.LiveMissing:
		return text(v.missing, missingMarker, roleMissing, false)
	}
	return piece{live: true, absent: true}
}

// currentMarker marks the rows that hold the pane shep runs in (see
// containsCurrentPane); missingMarker a row whose path is gone.
const (
	currentMarker = "current"
	missingMarker = "missing"
)

// statusPiece returns the glyph an agent status draws (a placeholder for the
// animated working spinner, see workingPlaceholder) and its role. "" and
// unrecognized values draw nothing.
func statusPiece(status string, set *IconSet) (string, runRole) {
	switch status {
	case "idle":
		return set.StatusIdle, roleStatusIdle
	case "done":
		return set.StatusDone, roleStatusDone
	case "blocked":
		return set.StatusBlocked, roleStatusBlocked
	case "unknown":
		return set.StatusUnknown, roleStatusUnknown
	case "working":
		if set.StatusWorking != "" {
			return set.StatusWorking, roleStatusWorking
		}
		return workingPlaceholder, roleStatusWorking
	}
	return "", rolePart
}

// takeBlankBeside drops the blank run next to the absent live value at i:
// the trailing blanks of the nearest text before it, else the leading blanks
// of the nearest text after it.
func takeBlankBeside(pieces []piece, i int) {
	for j := i - 1; j >= 0; j-- {
		if pieces[j].text == "" {
			continue
		}
		if !pieces[j].live && strings.HasSuffix(pieces[j].text, " ") {
			pieces[j].text = strings.TrimRight(pieces[j].text, " ")
			return
		}
		break
	}
	for j := i + 1; j < len(pieces); j++ {
		if pieces[j].text == "" {
			continue
		}
		if !pieces[j].live {
			pieces[j].text = strings.TrimLeft(pieces[j].text, " ")
		}
		return
	}
}

// collapseBlanks collapses every run of blanks in s to one blank.
func collapseBlanks(s string) string {
	if !strings.Contains(s, "  ") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	blank := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// collapsePieces collapses every blank run across pieces to one blank,
// leaving reserved blanks as they are.
func collapsePieces(pieces []piece) {
	blank := false
	for i := range pieces {
		t := pieces[i].text
		if t == "" {
			continue
		}
		if pieces[i].reserved {
			blank = false
			continue
		}
		if blank && t[0] == ' ' {
			t = strings.TrimLeft(t, " ")
		}
		t = collapseBlanks(t)
		pieces[i].text = t
		if t != "" {
			blank = t[len(t)-1] == ' '
		}
	}
}

// trimPieces trims the blanks at either end of the pieces' joined text.
func trimPieces(pieces []piece) []piece {
	for i := range pieces {
		if pieces[i].text == "" {
			continue
		}
		if pieces[i].reserved {
			break
		}
		pieces[i].text = strings.TrimLeft(pieces[i].text, " ")
		if pieces[i].text != "" {
			break
		}
	}
	for i := len(pieces) - 1; i >= 0; i-- {
		if pieces[i].text == "" {
			continue
		}
		if pieces[i].reserved {
			break
		}
		pieces[i].text = strings.TrimRight(pieces[i].text, " ")
		if pieces[i].text != "" {
			break
		}
	}
	return pieces
}

// joinPieces joins pieces into a part, merging neighbours drawn alike into
// one run. A part that is one plain run carries no runs at all.
func joinPieces(pieces []piece) part {
	n, size, only := 0, 0, -1
	for i, p := range pieces {
		if p.text != "" {
			n++
			size += len(p.text)
			only = i
		}
	}
	switch {
	case n == 0:
		return part{}
	case n == 1 && pieces[only].role == rolePart && !pieces[only].bold && !pieces[only].live:
		return plainPart(pieces[only].text)
	}
	var b strings.Builder
	b.Grow(size)
	runs := make([]run, 0, n)
	for _, p := range pieces {
		if p.text == "" {
			continue
		}
		b.WriteString(p.text)
		if last := len(runs) - 1; last >= 0 && runs[last].role == p.role && runs[last].bold == p.bold && runs[last].live == p.live {
			runs[last].end = b.Len()
			continue
		}
		runs = append(runs, run{end: b.Len(), role: p.role, bold: p.bold, live: p.live})
	}
	return part{text: b.String(), runs: runs, width: ansi.StringWidth(b.String())}
}

// hasLive reports whether p holds text a live marker produced.
func (p *part) hasLive() bool {
	for _, r := range p.runs {
		if r.live {
			return true
		}
	}
	return false
}

// scored returns the text of p the search scores (every run but the live
// ones) and, when that is not p.text itself, the rune index in p.text of
// each of its runes.
func (p *part) scored() (string, []int) {
	if !p.hasLive() {
		return p.text, nil
	}
	var b strings.Builder
	var index []int
	start, at := 0, 0
	for _, r := range p.runs {
		text := p.text[start:r.end]
		n := utf8.RuneCountInString(text)
		if !r.live {
			b.WriteString(text)
			for k := range n {
				index = append(index, at+k)
			}
		}
		start, at = r.end, at+n
	}
	return b.String(), index
}

// mark sets the highlight of the scored rune i of p (see scored).
func (p *part) mark(i int, index []int) {
	if index != nil {
		if i >= len(index) {
			return
		}
		i = index[i]
	}
	if p.hl == nil {
		p.hl = make([]bool, utf8.RuneCountInString(p.text))
	}
	if i >= 0 && i < len(p.hl) {
		p.hl[i] = true
	}
}

// highlight marks the label and detail runes the query matched. Highlighting
// rescores the text the row shows — row.MatchedIndexes index the matching
// haystack (label + path), not the drawn parts — and only direct matches of
// top-level and agent rows are highlighted. The detail and the label are
// scored together as "<detail>/<label>", the order a path reads (the
// default name-first layout draws a path's parent as the detail), so a match
// spanning both is found and mapped back onto each.
func (m Model) highlight(row Row, label, detail *part) {
	if row.Match != MatchDirect || len(row.MatchedIndexes) == 0 ||
		(row.Kind != RowCandidate && row.Candidate.Source != config.SourceAgents) {
		return
	}
	ltext, lindex := label.scored()
	dtext, dindex := detail.scored()
	hay, offset := ltext, 0
	if dtext != "" {
		hay = dtext + "/" + ltext
		offset = utf8.RuneCountInString(dtext) + 1
	}
	_, indexes := fuzzy.Score(m.query, hay)
	for _, i := range indexes {
		switch {
		case i < offset-1:
			detail.mark(i, dindex)
		case i >= offset:
			label.mark(i-offset, lindex)
		}
	}
}

// pathLike reports whether p's scored text reads as a path ("/…" or "~…"):
// such a label keeps its end when truncated, like a path does.
func (p *part) pathLike() bool {
	if p.runs == nil {
		return isPathLike(strings.TrimLeft(p.text, " "))
	}
	start := 0
	for _, r := range p.runs {
		if !r.live {
			if text := strings.TrimLeft(p.text[start:r.end], " "); text != "" {
				return isPathLike(text)
			}
		}
		start = r.end
	}
	return false
}

// isPathLike reports a label that reads as a path, so truncation keeps its
// deepest element: absolute or home-relative once a leading icon is skipped
// (" ~/ops/x", the icon-prefixed names a workspace_name template gives Herdr
// workspaces), or a relative path without blanks ("Proyectos/shep"). Titles
// with blanks keep their start even when they contain a slash.
func isPathLike(label string) bool {
	t := tmpl.TrimIcon(label)
	if strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~") {
		return true
	}
	return strings.Contains(t, "/") && !strings.ContainsAny(t, " \t")
}

// withoutLive returns p without the text its live markers produced, trimmed:
// what the preview title shows of a label.
func (p *part) withoutLive() part {
	if !p.hasLive() {
		return *p
	}
	pieces := make([]piece, 0, len(p.runs))
	start := 0
	for _, r := range p.runs {
		if !r.live {
			pieces = append(pieces, piece{text: p.text[start:r.end], role: r.role, bold: r.bold})
		}
		start = r.end
	}
	return joinPieces(trimPieces(pieces))
}
