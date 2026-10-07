// Package workspacename holds the naming policy for the Herdr workspaces shep
// creates: which template names a candidate, the default when none is
// configured, and which rendered names are acceptable. Rendering itself is
// internal/tmpl's job, with the same data and functions as every other
// template.
package workspacename

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/tranceh2/shep/internal/tmpl"
)

// Name is a validated launch-only Herdr workspace label.
type Name string

// worktreeFormat names a worktree when no workspace_name applies.
const worktreeFormat = `{{.RepoName}}@{{.Branch}}`

// Render resolves the workspace name for data. A configured format is
// rendered with engine; an empty format names a worktree "<repo>@<branch>"
// and anything else by its full normalized path (the raw path when it is not
// normalized). A workspace name is plain text: it must not be blank, contain
// control characters or carry row styling. Errors read "field: error".
func Render(engine *tmpl.Engine, field, format string, data tmpl.Data) (Name, error) {
	if format == "" {
		if !data.IsWorktree {
			if data.NormalizedPath != "" {
				return Name(data.NormalizedPath), nil
			}
			return Name(data.Path), nil
		}
		format = worktreeFormat
	}
	value, err := engine.RenderPlain(format, data)
	if err != nil {
		return "", scoped(field, err)
	}
	if strings.TrimSpace(value) == "" {
		return "", scoped(field, errors.New("output is blank"))
	}
	if err := rejectControls(value); err != nil {
		return "", scoped(field, err)
	}
	return Name(value), nil
}

// Validate checks a workspace_name template before it is ever used: it must
// parse and execute against every sample (all kinds when none are given),
// call no style or live function, never produce control characters, and
// produce a non-blank name for at least one sample. A template that is blank only for some kinds (such as
// "{{ .Branch }}" for plain folders) is accepted here and rejected when it is
// actually rendered blank.
func Validate(engine *tmpl.Engine, field, format string, samples ...tmpl.Data) error {
	if len(samples) == 0 {
		samples = tmpl.Samples()
	}
	if err := engine.ValidatePlain(field, format, samples...); err != nil {
		return err
	}
	blank := 0
	for _, sample := range samples {
		value, err := engine.Render(format, sample)
		if err != nil {
			return scoped(field, err)
		}
		if strings.TrimSpace(value) == "" {
			blank++
			continue
		}
		if err := rejectControls(value); err != nil {
			return scoped(field, err)
		}
	}
	if blank == len(samples) {
		return scoped(field, errors.New("output is blank"))
	}
	return nil
}

func rejectControls(value string) error {
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("output contains control character U+%04X", r)
		}
	}
	return nil
}

func scoped(field string, err error) error {
	if field == "" {
		return err
	}
	return fmt.Errorf("%s: %w", field, err)
}
