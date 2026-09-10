// Package rowformat renders configurable row labels and preview command tokens.
package rowformat

import (
	"bytes"
	"errors"
	"strings"
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
	return template.New("rowformat").Funcs(FuncMap).Parse(format)
}

// Render evaluates format with data using the shared template function map.
func Render(format string, data Context) (string, error) {
	tmpl, err := Parse(format)
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
