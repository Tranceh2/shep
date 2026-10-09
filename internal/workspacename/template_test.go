package workspacename_test

import (
	"strings"
	"testing"

	"github.com/tranceh2/shep/internal/source"
	"github.com/tranceh2/shep/internal/tmpl"
	"github.com/tranceh2/shep/internal/workspacename"
)

var engine = tmpl.New("/home/me")

func candidateData(path, normalized, label, src string, meta map[string]string) tmpl.Data {
	return source.TemplateData(source.Candidate{Path: path, NormalizedPath: normalized, Label: label, Source: src, Meta: meta})
}

func TestRender_ForbiddenFunctionsAreUnusable(t *testing.T) {
	for _, format := range []string{`{{ env "HOME" }}`, `{{ now }}`, `{{ osBase .Path }}`} {
		if _, err := workspacename.Render(engine, "test", format, tmpl.Data{Path: "/x"}); err == nil {
			t.Errorf("Render(%q) succeeded, want the function to be undefined", format)
		}
	}
}

func TestRender_WorktreeTemplatesAndDefaults(t *testing.T) {
	t.Parallel()
	meta := map[string]string{"is_worktree": "true", "repo": "shep", "branch": "feature/x", "head": "abcdef0123456789"}
	data := candidateData("/trees/shep-feature", "/trees/shep-feature", "shep (feature/x)", "projects", meta)

	cases := []struct {
		name   string
		format string
		data   tmpl.Data
		want   workspacename.Name
	}{
		{name: "custom worktree fields", format: `{{.RepoName}}@{{.Branch}}`, data: data, want: "shep@feature/x"},
		{name: "default worktree", format: "", data: data, want: "shep@feature/x"},
		{name: "detached fallback", format: "", data: candidateData("/trees/detached", "/trees/detached", "detached", "projects", map[string]string{"is_worktree": "true", "repo": "repo", "head": "1234567890abcdef"}), want: "repo@1234567"},
		{name: "bare repo linked worktree", format: "", data: candidateData("/trees/bare-linked", "/trees/bare-linked", "bare-repo (bugfix)", "projects", map[string]string{"is_worktree": "true", "repo": "bare-repo", "branch": "bugfix"}), want: "bare-repo@bugfix"},
		{name: "path fields", format: `{{.Path}}|{{.NormalizedPath}}|{{.Label}}`, data: tmpl.Data{Path: "/raw", NormalizedPath: "/normalized", Label: "label"}, want: "/raw|/normalized|label"},
		{name: "default is the full normalized path", format: "", data: tmpl.Data{Path: "/raw/web-app", NormalizedPath: "/srv/projects/web-app"}, want: "/srv/projects/web-app"},
		{name: "default falls back to the raw path", format: "", data: tmpl.Data{Path: "/raw/web-app"}, want: "/raw/web-app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspacename.Render(engine, tc.name, tc.format, tc.data)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRender_IsMainWorktreeAndConditionalNaming(t *testing.T) {
	t.Parallel()
	primary := candidateData("/srv/shep", "/srv/shep", "shep", "projects", map[string]string{
		"is_worktree": "true", "main_worktree": "true", "repo": "shep", "branch": "main", "head": "1111222233334444",
	})
	linked := candidateData("/trees/shep-auth", "/trees/shep-auth", "shep (feature/auth)", "projects", map[string]string{
		"is_worktree": "true", "main_worktree": "false", "repo": "shep", "branch": "feature/auth", "head": "2222333344445555",
	})
	detached := candidateData("/trees/shep-detached", "/trees/shep-detached", "shep (abcdef0)", "projects", map[string]string{
		"is_worktree": "true", "main_worktree": "false", "repo": "shep", "head": "abcdef0123456789",
	})
	ordinary := candidateData("/srv/ordinary", "/srv/ordinary", "ordinary", "projects", nil)

	if !primary.IsWorktree || !primary.IsMainWorktree {
		t.Errorf("primary = %+v, want a main worktree", primary)
	}
	if !linked.IsWorktree || linked.IsMainWorktree {
		t.Errorf("linked = %+v, want a linked worktree", linked)
	}
	if detached.Branch != "abcdef0" {
		t.Errorf("detached.Branch = %q, want abcdef0", detached.Branch)
	}
	if ordinary.IsWorktree || ordinary.IsMainWorktree {
		t.Errorf("ordinary = %+v, want no worktree flags", ordinary)
	}

	// Path-based names for the main checkout and ordinary projects,
	// <repo>@<branch> (or <repo>@<short-sha>) for secondary linked worktrees.
	const format = `{{ if and .IsWorktree (not .IsMainWorktree) }}{{ .RepoName }}@{{ .Branch }}{{ else }}{{ .Path | base }}{{ end }}`
	for _, tc := range []struct {
		name string
		data tmpl.Data
		want workspacename.Name
	}{
		{"primary checkout uses path base", primary, "shep"},
		{"ordinary project uses path base", ordinary, "ordinary"},
		{"secondary linked worktree with branch", linked, "shep@feature/auth"},
		{"secondary linked worktree detached sha", detached, "shep@abcdef0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspacename.Render(engine, tc.name, format, tc.data)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Render = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	validation := tmpl.New(tmpl.SampleHome)
	cases := []struct {
		name    string
		format  string
		wantErr string
	}{
		{name: "base name", format: `{{ .Path | base | lower }}`},
		{name: "blank for some kinds only", format: `{{ .Branch }}`},
		{name: "every function name is canonical", format: `{{ .Path | dir | base }}-{{ .Path | clean | ext }}{{ isAbs .Path }}`},
		{name: "unknown field", format: `{{ .Unknown }}`, wantErr: "general.workspace_name: template: shep:1:3: executing"},
		{name: "excluded Sprig alias", format: `{{ .Path | osBase }}`, wantErr: `general.workspace_name: template: shep:1: function "osBase" not defined`},
		{name: "blank for every kind", format: `{{ "   " }}`, wantErr: "general.workspace_name: output is blank"},
		{name: "control character", format: "line\nname", wantErr: "general.workspace_name: output contains control character U+000A"},
		{name: "invalid regex", format: `{{ mustRegexMatch "[" .Path }}`, wantErr: "general.workspace_name: template: shep:1:3: executing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := workspacename.Validate(validation, "general.workspace_name", tc.format)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(%q) = %v", tc.format, err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantErr) {
				t.Fatalf("Validate(%q) = %v, want prefix %q", tc.format, err, tc.wantErr)
			}
		})
	}
}

// TestRowFunctionsAreRejected proves workspace names stay plain text: the
// style and live functions of row templates are rejected by name at
// validation and fail at render time instead of leaking their markup into a
// Herdr workspace label.
func TestRowFunctionsAreRejected(t *testing.T) {
	t.Parallel()
	for _, name := range tmpl.MarkupFunctions() {
		format := "{{ .Path | base }}{{ " + name + " }}"
		if name == "muted" || name == "accent" || name == "bold" {
			format = "{{ " + name + " (.Path | base) }}"
		}
		err := workspacename.Validate(engine, "general.workspace_name", format)
		if err == nil || !strings.Contains(err.Error(), "general.workspace_name: "+name+" is a row presentation function") {
			t.Errorf("Validate(%q) error = %v, want it to reject %s", format, err, name)
		}
		if got, err := workspacename.Render(engine, "workspace name", format, tmpl.Data{Path: "/srv/api"}); err == nil {
			t.Errorf("Render(%q) = %q, want an error", format, got)
		}
	}
}
