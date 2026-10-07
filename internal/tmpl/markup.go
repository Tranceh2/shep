package tmpl

import (
	"strings"
	"text/template"
	"unicode/utf8"
)

// Row templates (icon, label_format, detail_format, marker_format) style
// their text by meaning and leave placeholders for values only the renderer
// knows, through two families of functions:
//
//   - style: muted, accent and bold wrap their argument;
//   - live: status, pin, current, group and missing mark where the renderer
//     draws the agent status glyph, the pin star, the "current" marker, the
//     group chevron and the "missing" marker (each empty when it does not
//     apply to the row).
//
// They never emit terminal escapes. Each is encoded as Unicode noncharacters
// from U+FDD0–U+FDEF, which are never assigned and never appear in real text;
// Segments decodes a rendered row template into styled text and live markers.
// Every noncharacter of that range is removed from Data before a template
// runs (see sanitize), so text from a provider can never style a row or fake
// a marker. Templates whose output is plain text (workspace names, preview
// commands) must not call these functions: see ValidatePlain and RenderPlain.

// The sentinel runes, all inside the reserved range U+FDD0–U+FDEF.
const (
	runeEnd     = '\uFDD0' // closes the innermost style
	runeMuted   = '\uFDD1'
	runeAccent  = '\uFDD2'
	runeBold    = '\uFDD3'
	runeStatus  = '\uFDD8'
	runePin     = '\uFDD9'
	runeCurrent = '\uFDDA'
	runeGroup   = '\uFDDB'
	runeMissing = '\uFDDC'
)

// PartSeparator joins several formats into one template so a renderer can
// render them with a single execution and split the output again. No
// template function produces it and it is removed from Data like every other
// reserved rune, so it only appears where the joined format put it.
const PartSeparator = "\uFDEF"

// Style is the semantic styling of a text segment. The zero Style is plain
// text in the role of the part it belongs to.
type Style uint8

// Style bits. Styles nest: bold adds to the enclosing color, and an inner
// color replaces an outer one, so `{{ bold (muted "x") }}` is muted and bold
// while `{{ muted (accent "x") }}` is accent.
const (
	// StyleMuted is the text.muted role (the muted function).
	StyleMuted Style = 1 << iota
	// StyleAccent is the accent role (the accent function).
	StyleAccent
	// StyleBold is bold text (the bold function).
	StyleBold
)

// colorStyles are the Style bits that pick a color; they exclude each other.
const colorStyles = StyleMuted | StyleAccent

// Live names a value the renderer fills in at draw time.
type Live uint8

// Live markers. LiveNone marks a text segment.
const (
	LiveNone Live = iota
	// LiveStatus is the agent status glyph: an open workspace's aggregate,
	// or a pane or agent row's own state.
	LiveStatus
	// LivePin is the pin marker of a pinned row.
	LivePin
	// LiveCurrent is the "current" marker of the rows holding shep's pane.
	LiveCurrent
	// LiveGroup is the chevron of a group workspace.
	LiveGroup
	// LiveMissing is the "missing" marker of a row whose path is gone.
	LiveMissing
)

// String returns the template function that produces l.
func (l Live) String() string {
	switch l {
	case LiveStatus:
		return "status"
	case LivePin:
		return "pin"
	case LiveCurrent:
		return "current"
	case LiveGroup:
		return "group"
	case LiveMissing:
		return "missing"
	}
	return ""
}

// Segment is one piece of a decoded row template: styled text (Live is
// LiveNone) or a live marker, which carries the style it was written in.
type Segment struct {
	Text  string
	Style Style
	Live  Live
}

// markupFunctionNames are the style and live functions, in documentation
// order.
var markupFunctionNames = []string{"muted", "accent", "bold", "status", "pin", "current", "group", "missing"}

// MarkupFunctions returns the names of the style and live functions. The
// slice is fresh on every call.
func MarkupFunctions() []string { return append([]string(nil), markupFunctionNames...) }

// markupFuncs returns the style and live functions.
func markupFuncs() template.FuncMap {
	wrap := func(open rune) func(string) string {
		return func(s string) string { return string(open) + s + string(runeEnd) }
	}
	mark := func(r rune) func() string {
		s := string(r)
		return func() string { return s }
	}
	return template.FuncMap{
		"muted":   wrap(runeMuted),
		"accent":  wrap(runeAccent),
		"bold":    wrap(runeBold),
		"status":  mark(runeStatus),
		"pin":     mark(runePin),
		"current": mark(runeCurrent),
		"group":   mark(runeGroup),
		"missing": mark(runeMissing),
	}
}

// markupAt reports whether s[i:] starts with a reserved rune (U+FDD0–U+FDEF,
// encoded EF B7 90 – EF B7 AF).
func markupAt(s string, i int) bool {
	return i+2 < len(s) && s[i] == 0xEF && s[i+1] == 0xB7 && s[i+2] >= 0x90 && s[i+2] <= 0xAF
}

// HasMarkup reports whether s contains any reserved rune: style or live
// markup, or the part separator.
func HasMarkup(s string) bool {
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], 0xEF)
		if j < 0 {
			return false
		}
		i += j
		if markupAt(s, i) {
			return true
		}
		i++
	}
	return false
}

// StripMarkup removes every reserved rune from s. It returns s itself, without
// allocating, when there is none.
func StripMarkup(s string) string {
	if !HasMarkup(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	start := 0
	for i := 0; i < len(s); {
		if markupAt(s, i) {
			b.WriteString(s[start:i])
			i += 3
			start = i
			continue
		}
		i++
	}
	b.WriteString(s[start:])
	return b.String()
}

// Segments decodes a rendered row template into its styled text and live
// markers, in order. Text without markup is one plain segment (none for "").
// Malformed markup decodes leniently: a stray end is ignored and an unclosed
// style runs to the end of the text. Reserved runes that are not markup
// (the part separator) are dropped.
func Segments(rendered string) []Segment {
	return AppendSegments(nil, rendered)
}

// maxStyleDepth bounds style nesting; deeper styles keep the style in force at
// that depth (no template nests anywhere near it).
const maxStyleDepth = 16

// AppendSegments is Segments appending to dst, so a caller can decode into a
// reused buffer.
func AppendSegments(dst []Segment, rendered string) []Segment {
	var stack [maxStyleDepth]Style
	depth := 0
	cur := Style(0)
	start := 0
	flush := func(end int) {
		if end > start {
			dst = append(dst, Segment{Text: rendered[start:end], Style: cur})
		}
	}
	for i := 0; i < len(rendered); {
		if !markupAt(rendered, i) {
			i++
			continue
		}
		flush(i)
		r, _ := utf8.DecodeRuneInString(rendered[i:])
		i += 3
		start = i
		switch r {
		case runeMuted, runeAccent, runeBold:
			if depth < maxStyleDepth {
				stack[depth] = cur
			}
			depth++
			switch r {
			case runeMuted:
				cur = cur&^colorStyles | StyleMuted
			case runeAccent:
				cur = cur&^colorStyles | StyleAccent
			default:
				cur |= StyleBold
			}
		case runeEnd:
			if depth > 0 {
				depth--
				if depth < maxStyleDepth {
					cur = stack[depth]
				}
			}
		case runeStatus:
			dst = append(dst, Segment{Style: cur, Live: LiveStatus})
		case runePin:
			dst = append(dst, Segment{Style: cur, Live: LivePin})
		case runeCurrent:
			dst = append(dst, Segment{Style: cur, Live: LiveCurrent})
		case runeGroup:
			dst = append(dst, Segment{Style: cur, Live: LiveGroup})
		case runeMissing:
			dst = append(dst, Segment{Style: cur, Live: LiveMissing})
		}
	}
	flush(len(rendered))
	return dst
}

// sanitize removes every reserved rune from d's text (Meta values included),
// so data can never carry styling or live markers into a template's output.
// Data without reserved runes is left untouched, without allocating.
func sanitize(d *Data) {
	for _, field := range []*string{
		&d.Path, &d.NormalizedPath, &d.Label, &d.Source, &d.Kind, &d.Icon,
		&d.Branch, &d.Head, &d.RepoName, &d.Agent, &d.AgentStatus,
		&d.TabNumber, &d.TabLabel, &d.Workspace,
	} {
		*field = StripMarkup(*field)
	}
	for k, v := range d.Meta {
		if HasMarkup(k) || HasMarkup(v) {
			d.Meta = stripMeta(d.Meta)
			return
		}
	}
}

// stripMeta returns a copy of meta with every reserved rune removed from its
// keys and values; the caller's map is never modified.
func stripMeta(meta map[string]string) map[string]string {
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		out[StripMarkup(k)] = StripMarkup(v)
	}
	return out
}
