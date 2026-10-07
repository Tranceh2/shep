package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/tranceh2/shep/internal/config"
)

const maxCustomSourceOutputBytes = 64 * 1024

// reservedCustomSourceMetaKeys are keys owned by Shep's providers and launch
// pipeline. Custom-source rows may expose arbitrary inert metadata, but they
// must use typed row fields for the public command/template/close-on-exit
// contract and cannot forge internal identity, grouping, control, or display
// state through meta.
var reservedCustomSourceMetaKeys = map[string]struct{}{
	"active_tab_id":    {},
	"agent_status":     {},
	"branch":           {},
	"close_on_exit":    {},
	"command":          {},
	"default":          {},
	"entry_id":         {},
	"group":            {},
	"group_sources":    {},
	"group_template":   {},
	"head":             {},
	"custom_source":    {},
	"custom_source_id": {},
	"is_worktree":      {},
	"main_worktree":    {},
	"pane_id":          {},
	"parent_template":  {},
	"repo":             {},
	"running":          {},
	"session_dir":      {},
	"session_name":     {},
	"socket_path":      {},
	"tab_id":           {},
	"tab_label":        {},
	"tab_number":       {},
	"tab_panes":        {},
	"template":         {},
	"workspace_id":     {},
	"workspace_label":  {},
	"workspace_tabs":   {},
}

// customSourceRow is the deliberately small JSON contract accepted from a
// custom source command. The command must print one JSON array of rows. Meta is
// inert presentation/preview data; reserved internal keys are rejected rather
// than allowed to alter launch, identity, grouping, or control behavior.
type customSourceRow struct {
	ID          string            `json:"id,omitempty"`
	Label       string            `json:"label"`
	Path        string            `json:"path,omitempty"`
	Command     string            `json:"command,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	Template    string            `json:"template,omitempty"`
	CloseOnExit bool              `json:"close_on_exit,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

// ParseCustomSourceJSON parses the documented custom source row array into
// ordinary source candidates. The whole payload is rejected when it is not a
// JSON array, contains no rows, or contains a row without a non-empty label.
// Repeated explicit IDs are allowed when their effective commands agree; the
// resolver later keeps the first row for that stable identity. An explicit ID
// with conflicting commands is rejected because choosing one route would hide
// an actionable custom source definition.
func ParseCustomSourceJSON(name string, payload []byte) ([]Candidate, error) {
	return parseCustomSourceJSON(name, payload, nil)
}

// ParseCustomSourceJSONWithAliases parses rows and applies shared custom-source
// aliases before combining and normalizing each row's aliases.
func ParseCustomSourceJSONWithAliases(name string, payload []byte, sharedAliases []string) ([]Candidate, error) {
	return parseCustomSourceJSON(name, payload, sharedAliases)
}

func normalizeAliases(aliases []string) []string {
	seen := make(map[string]struct{}, len(aliases))
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if strings.IndexFunc(alias, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			continue
		}
		key := strings.ToLower(alias)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, alias)
	}
	return out
}

func parseCustomSourceJSON(name string, payload []byte, sharedAliases []string) ([]Candidate, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, errors.New("custom source output is empty")
	}
	var rows []customSourceRow
	if err := json.Unmarshal(payload, &rows); err != nil {
		return nil, fmt.Errorf("parse JSON rows: %w", err)
	}
	if rows == nil {
		return nil, errors.New("custom source output must be a JSON array")
	}
	candidates := make([]Candidate, 0, len(rows))
	explicitIDs := make(map[string]string, len(rows))
	for i, row := range rows {
		if strings.TrimSpace(row.Label) == "" {
			return nil, fmt.Errorf("row %d: label is required", i)
		}
		for key := range row.Meta {
			if _, reserved := reservedCustomSourceMetaKeys[key]; reserved {
				return nil, fmt.Errorf("row %d: meta key %q is reserved; use the row field for this value", i, key)
			}
		}
		meta := make(map[string]string, len(row.Meta)+4)
		for key, value := range row.Meta {
			meta[key] = value
		}
		if row.Command != "" {
			meta["command"] = row.Command
		}
		if row.Template != "" {
			meta["template"] = row.Template
		}
		if row.CloseOnExit {
			meta["close_on_exit"] = "true"
		}
		if id := strings.TrimSpace(row.ID); id != "" {
			command := meta["command"]
			if previousCommand, seen := explicitIDs[id]; seen && previousCommand != command {
				return nil, fmt.Errorf("row %d: custom source id %q has conflicting commands", i, id)
			}
			explicitIDs[id] = command
		}
		// This marker lets the shared target predicate distinguish an external
		// custom source from direct/path candidates without adding a parallel
		// launch path or passing config through the source package.
		meta["custom_source"] = "true"
		if strings.TrimSpace(row.ID) != "" {
			meta["custom_source_id"] = strings.TrimSpace(row.ID)
		}
		aliases := append([]string{}, sharedAliases...)
		aliases = append(aliases, row.Aliases...)
		candidates = append(candidates, Candidate{
			Path:    row.Path,
			Label:   row.Label,
			Icon:    row.Icon,
			Source:  name,
			Aliases: normalizeAliases(aliases),
			Meta:    meta,
		})
	}
	return candidates, nil
}

type cappedOutput struct {
	buf      bytes.Buffer
	exceeded bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	remaining := maxCustomSourceOutputBytes - w.buf.Len()
	if remaining <= 0 {
		w.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.buf.Write(p[:remaining])
		w.exceeded = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *cappedOutput) Bytes() []byte { return w.buf.Bytes() }
func (w *cappedOutput) Len() int      { return w.buf.Len() }

var _ io.Writer = (*cappedOutput)(nil)

type customSourceProvider struct {
	cfg config.CustomSourceConfig
}

func (p *customSourceProvider) Name() string { return p.cfg.Name }

func (p *customSourceProvider) List(ctx context.Context) ([]Candidate, error) {
	if len(p.cfg.Command) == 0 || p.cfg.Command[0] == "" {
		return nil, errors.New("custom source command is empty")
	}
	timeout := time.Duration(p.cfg.Timeout)
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, p.cfg.Command[0], p.cfg.Command[1:]...)
	var stdout, stderr cappedOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("custom source %q timed out after %s", p.cfg.Name, timeout)
		}
		return nil, fmt.Errorf("custom source %q failed: %w", p.cfg.Name, err)
	}
	if err := commandCtx.Err(); err != nil {
		return nil, fmt.Errorf("custom source %q: %w", p.cfg.Name, err)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("custom source %q output exceeds maximum size of %d bytes", p.cfg.Name, maxCustomSourceOutputBytes)
	}
	if stderr.Len() > 0 {
		return nil, fmt.Errorf("custom source %q wrote to stderr", p.cfg.Name)
	}
	out := stdout.Bytes()
	candidates, err := ParseCustomSourceJSONWithAliases(p.cfg.Name, out, p.cfg.Aliases)
	if err != nil {
		return nil, fmt.Errorf("custom source %q: %w", p.cfg.Name, err)
	}
	return candidates, nil
}
