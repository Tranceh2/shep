package tmpl

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
)

// templateName names every parsed template, so errors read
// "template: shep:1:3: ...".
const templateName = "shep"

// maxCachedTemplates bounds the parse cache. Formats and command tokens come
// from configuration, so real sessions stay far below it; the cap only
// guarantees the cache can never grow without limit.
const maxCachedTemplates = 512

// Engine parses and renders templates with shep's function set. Construct it
// with New; the zero value is not usable. An Engine is safe for concurrent
// use: parsed templates are never modified after parsing and text/template
// documents Execute as safe for concurrent calls.
type Engine struct {
	home  string
	funcs template.FuncMap
	// cache maps a format string to its parsed outcome. The picker renders
	// every visible row on every frame and text/template parsing dominated
	// that cost, so each format is parsed at most once while the cache has
	// room. Parse errors are cached too.
	cache      sync.Map
	cacheCount atomic.Int64
}

// parsed is one cached Parse outcome, including a parse error so an invalid
// format is not re-parsed on every call either.
type parsed struct {
	tmpl *template.Template
	err  error
}

// New returns an Engine whose tilde helper abbreviates home. An empty home
// disables the abbreviation. New panics only when the pinned Sprig no longer
// provides the documented function set, which is a build defect the tests
// catch, not a runtime condition.
func New(home string) *Engine {
	if home != "" {
		home = filepath.Clean(home)
	}
	funcs, err := buildFuncs(home)
	if err != nil {
		panic("tmpl: " + err.Error())
	}
	return &Engine{home: home, funcs: funcs}
}

// Home returns the home directory the engine's tilde helper abbreviates.
func (e *Engine) Home() string { return e.home }

// Parse returns the parsed template for format, parsing it at most once. The
// returned template is shared with every other caller and must not be
// modified. Missing map keys (such as an absent .Meta entry) render empty;
// an unknown field or function is an error.
func (e *Engine) Parse(format string) (*template.Template, error) {
	if cached, ok := e.cache.Load(format); ok {
		entry := cached.(parsed)
		return entry.tmpl, entry.err
	}
	t, err := template.New(templateName).Funcs(e.funcs).Option("missingkey=zero").Parse(format)
	if e.cacheCount.Load() < maxCachedTemplates {
		if _, loaded := e.cache.LoadOrStore(format, parsed{tmpl: t, err: err}); !loaded {
			e.cacheCount.Add(1)
		}
	}
	return t, err
}

// Render evaluates format against d. A format without template actions is
// returned as is.
func (e *Engine) Render(format string, d Data) (string, error) {
	if !strings.Contains(format, "{{") {
		return format, nil
	}
	t, err := e.Parse(format)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.Grow(len(format) + 32)
	if err := t.Execute(&out, d); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Validate parses format and executes it against every sample (all kinds'
// Samples when none are given), so syntax errors, unknown fields and
// functions, and data-dependent execution failures are all reported before
// the template is ever used. Errors read "field: error".
func (e *Engine) Validate(field, format string, samples ...Data) error {
	if len(samples) == 0 {
		samples = Samples()
	}
	t, err := e.Parse(format)
	if err != nil {
		return scoped(field, err)
	}
	for _, sample := range samples {
		if err := t.Execute(io.Discard, sample); err != nil {
			return scoped(field, err)
		}
	}
	return nil
}

// scoped prefixes err with the configuration field it belongs to.
func scoped(field string, err error) error {
	if field == "" {
		return err
	}
	return fmt.Errorf("%s: %w", field, err)
}

// Tokenize splits s on whitespace while honouring single and double quotes.
// Quotes are removed; a quoted run with spaces stays one token. An unmatched
// quote is an error so callers fail fast on a malformed command. Preview
// commands are tokenized first and each token is rendered on its own, so a
// rendered value always stays inside one argv element.
func Tokenize(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuotes := byte(0)
	flushing := false

	flush := func() {
		if flushing {
			tokens = append(tokens, cur.String())
			cur.Reset()
			flushing = false
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuotes != 0:
			if c == inQuotes {
				inQuotes = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			inQuotes = c
			flushing = true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			cur.WriteByte(c)
			flushing = true
		}
	}
	if inQuotes != 0 {
		return nil, errors.New("unterminated quote in preview command")
	}
	flush()
	return tokens, nil
}
