// Package preview renders the workspace preview shown in the Bubble Tea
// selector and emitted by `shep preview <path>`. It is intentionally free of
// TUI dependencies: it turns a config.PreviewConfig plus a candidate into a
// plain-text Result (text, optional warning, from-cache flag). The TUI and CLI
// layer adapt this text to their styling needs.
package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// pathPlaceholder is the substitution token for the candidate path in a custom
// preview command. It is replaced as one argument value, never re-tokenised.
const pathPlaceholder = "{path}"

const maxCapturedOutputBytes = 64 * 1024

// CommandRunner executes a parsed argv under ctx and returns trimmed stdout.
// The default implementation uses exec.CommandContext (no sh -c); tests inject
// a fake to keep renderer tests process-free.
type CommandRunner interface {
	Run(ctx context.Context, argv []string, dir string, maxLines int) (string, error)
}

// commandRunner is the production CommandRunner backed by os/exec.
type commandRunner struct{}

// NewCommandRunner returns the production CommandRunner backed by os/exec.
func NewCommandRunner() CommandRunner { return commandRunner{} }

// Run executes argv[0] with argv[1:] in dir. Capture is stdout-only into the
// returned string; a non-zero exit OR any stderr output is a failure (WP-3),
// and the returned error includes stderr content capped during execution.
// stdout is capped during execution by maxLines and a byte ceiling; a non-positive
// maxLines means no line cap, but the byte ceiling still applies.
func (commandRunner) Run(ctx context.Context, argv []string, dir string, maxLines int) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty preview command")
	}
	stdout := newCappedLineWriter(maxLines, maxCapturedOutputBytes)
	stderr := newCappedLineWriter(0, maxCapturedOutputBytes)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", fmt.Errorf("preview command failed: %w: %s", err, strings.TrimRight(stderr.String(), "\n"))
	}
	if stderr.Len() > 0 {
		return "", fmt.Errorf("preview command wrote to stderr: %s", strings.TrimRight(stderr.String(), "\n"))
	}
	return stdout.String(), nil
}

type cappedLineWriter struct {
	b       strings.Builder
	maxLine int
	maxByte int
	lines   int
	bytes   int
	closed  bool
}

func newCappedLineWriter(maxLines, maxBytes int) *cappedLineWriter {
	return &cappedLineWriter{maxLine: maxLines, maxByte: maxBytes}
}

func (w *cappedLineWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		if w.closed {
			continue
		}
		if w.maxByte > 0 && w.bytes >= w.maxByte {
			w.closed = true
			continue
		}
		if c == '\n' {
			w.lines++
			if w.maxLine > 0 && w.lines >= w.maxLine {
				w.closed = true
				continue
			}
		}
		w.b.WriteByte(c)
		w.bytes++
	}
	return len(p), nil
}

func (w *cappedLineWriter) String() string { return strings.TrimRight(w.b.String(), "\n") }

func (w *cappedLineWriter) Len() int { return w.b.Len() }

var _ io.Writer = (*cappedLineWriter)(nil)

// ParseCommand splits a shell-style command string into argv and substitutes
// {path} with the candidate path as a single argument value. It supports
// single- and double-quoted arguments so users can pass descriptions with
// spaces; it deliberately performs no shell expansion and never invokes sh -c.
func ParseCommand(cmd, path string) ([]string, error) {
	tokens, err := tokenize(cmd)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("empty preview command")
	}
	out := make([]string, len(tokens))
	for i, tok := range tokens {
		out[i] = strings.ReplaceAll(tok, pathPlaceholder, path)
	}
	return out, nil
}

// tokenize splits on whitespace while honouring single and double quotes.
// Quotes are removed; a quoted run with spaces stays one token. An unmatched
// quote is an error so users fail fast on a malformed command.
func tokenize(s string) ([]string, error) {
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
