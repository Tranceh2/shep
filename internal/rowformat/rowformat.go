// Package rowformat renders configurable row labels and preview command tokens.
package rowformat

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
)

// Context contains the values available to row-label and preview-command
// templates. Every field is empty when its value is not applicable.
type Context struct {
	Path        string
	Label       string
	Icon        string
	TabNumber   string
	AgentStatus string
	Meta        map[string]string
}

// FuncMap is the extension point for future template filters. It intentionally
// contains no filters until a compatible filter contract is introduced.
var FuncMap = template.FuncMap{}

// Parse validates format using the shared template function map.
func Parse(format string) (*template.Template, error) {
	return template.New("rowformat").Funcs(FuncMap).Option("missingkey=zero").Parse(format)
}

// maxCachedTemplates bounds the parsed-template cache. Formats and command
// tokens come from configuration, so real sessions stay far below it; the cap
// only guarantees the cache can never grow without limit.
const maxCachedTemplates = 512

// parsedTemplate is one cached Parse outcome, including a parse error so an
// invalid format is not re-parsed on every call either.
type parsedTemplate struct {
	tmpl *template.Template
	err  error
}

var (
	// templateCache maps a format string to its parsedTemplate. The picker
	// renders every visible row on every frame, and text/template parsing
	// dominated that cost. A parsed template is never mutated afterwards and
	// text/template documents Execute as safe for concurrent use.
	templateCache      sync.Map
	templateCacheCount atomic.Int64
)

// cachedParse returns the parsed template for format, parsing it at most
// once while the cache has room.
func cachedParse(format string) (*template.Template, error) {
	if cached, ok := templateCache.Load(format); ok {
		entry := cached.(parsedTemplate)
		return entry.tmpl, entry.err
	}
	tmpl, err := Parse(format)
	if templateCacheCount.Load() < maxCachedTemplates {
		if _, loaded := templateCache.LoadOrStore(format, parsedTemplate{tmpl: tmpl, err: err}); !loaded {
			templateCacheCount.Add(1)
		}
	}
	return tmpl, err
}

// Render evaluates format with data using the shared template function map.
func Render(format string, data Context) (string, error) {
	tmpl, err := cachedParse(format)
	if err != nil {
		return "", err
	}

	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return "", err
	}
	return output.String(), nil
}

// Tokenize splits s on whitespace while honouring single and double quotes.
// Quotes are removed; a quoted run with spaces stays one token. An unmatched
// quote is an error so callers fail fast on a malformed command.
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
