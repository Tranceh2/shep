package source

import (
	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/tmpl"
)

// sourcePath is the Source of a candidate built from a direct --path (or
// "--path .") invocation rather than from a provider.
const sourcePath = "path"

// TemplateData is the single conversion from a candidate to the data every
// user template (row labels, workspace names, preview commands) is rendered
// against. Kind is derived from the source and its metadata; rows the TUI
// synthesizes itself (Herdr tabs and panes, Source "") get an empty Kind and
// the caller sets tmpl.KindTab or tmpl.KindPane. Meta is shared with the
// candidate, not copied: templates cannot modify it.
func TemplateData(c Candidate) tmpl.Data {
	isWorktree := c.Meta["is_worktree"] == "true"
	head := shortHead(c.Meta["head"])
	branch := c.Meta["branch"]
	if branch == "" && isWorktree {
		branch = head
		if branch == "" {
			branch = "detached"
		}
	}
	return tmpl.Data{
		Path:           c.Path,
		NormalizedPath: c.NormalizedPath,
		Label:          c.Label,
		Source:         c.Source,
		Kind:           candidateKind(c, isWorktree),
		Icon:           c.Icon,
		Branch:         branch,
		Head:           head,
		RepoName:       c.Meta["repo"],
		IsWorktree:     isWorktree,
		IsMainWorktree: c.Meta["main_worktree"] == "true",
		Agent:          c.Meta["agent"],
		AgentStatus:    c.Meta["agent_status"],
		TabNumber:      c.Meta["tab_number"],
		TabLabel:       c.Meta["tab_label"],
		Workspace:      c.Meta["workspace_label"],
		Meta:           c.Meta,
	}
}

// candidateKind maps a candidate's source (and, for configured workspaces and
// projects, its metadata) onto the fixed tmpl Kind vocabulary. Any source
// other than the built-in ones is a custom source.
func candidateKind(c Candidate, isWorktree bool) string {
	switch c.Source {
	case "":
		return ""
	case config.SourceHerdr:
		return tmpl.KindWorkspace
	case config.SourceWorkspaces:
		if c.Meta["group"] == "true" {
			return tmpl.KindGroup
		}
		return tmpl.KindConfigured
	case config.SourceZoxide, sourcePath:
		return tmpl.KindFolder
	case config.SourceProjects:
		if isWorktree {
			return tmpl.KindWorktree
		}
		return tmpl.KindProject
	case config.SourceSessions:
		return tmpl.KindSession
	case config.SourceAgents:
		return tmpl.KindAgent
	default:
		return tmpl.KindCustom
	}
}

// shortHead abbreviates a commit id to the 7 characters git shows by default.
func shortHead(head string) string {
	if len(head) > 7 {
		return head[:7]
	}
	return head
}
