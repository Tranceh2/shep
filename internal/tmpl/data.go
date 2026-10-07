// Package tmpl is shep's single template engine. Every user-authored template
// — row labels, workspace names and preview command arguments — is a Go
// text/template evaluated against the same Data model with the same function
// set (a hermetic, type-checked Sprig subset plus a few shep helpers). Row
// templates may also style their text and place live markers (see Segments);
// templates that render plain text reject those functions (see
// ValidatePlain).
//
// An Engine owns the home directory the tilde helper abbreviates, the
// function map and a bounded parse cache, so the package itself keeps no
// mutable state. tmpl imports no other shep package: callers convert their own
// values into Data (see source.TemplateData for candidates).
package tmpl

// Kind values classify what a template is rendering. The vocabulary is fixed:
// templates can branch on it (`{{ if eq .Kind "worktree" }}`) without knowing
// which provider produced the row.
const (
	// KindWorkspace is an open Herdr workspace (source herdr).
	KindWorkspace = "workspace"
	// KindConfigured is a [[workspaces]] entry that opens a directory,
	// command or template.
	KindConfigured = "configured"
	// KindGroup is a [[workspaces]] group entry that drills into a nested
	// picker.
	KindGroup = "group"
	// KindFolder is a plain directory: a zoxide entry or a direct --path.
	KindFolder = "folder"
	// KindProject is a discovered project root that is not a linked worktree.
	KindProject = "project"
	// KindWorktree is a git worktree discovered under a project.
	KindWorktree = "worktree"
	// KindSession is a Herdr session record.
	KindSession = "session"
	// KindAgent is an agent pane reported by Herdr.
	KindAgent = "agent"
	// KindTab is a Herdr tab row nested under an open workspace.
	KindTab = "tab"
	// KindPane is a Herdr pane row nested under a tab.
	KindPane = "pane"
	// KindCustom is a row produced by a [[sources.custom]] command.
	KindCustom = "custom"
)

// Kinds returns every Kind value in a stable order. The slice is fresh on
// every call so callers may modify it.
func Kinds() []string {
	return []string{
		KindWorkspace, KindConfigured, KindGroup, KindFolder, KindProject,
		KindWorktree, KindSession, KindAgent, KindTab, KindPane, KindCustom,
	}
}

// Data is the only value templates are executed against. Every field is
// empty (or false) when it does not apply to the row being rendered; a
// template never sees an error for reading an inapplicable field.
type Data struct {
	// Path is the path as the source reports it (absolute for every built-in
	// source).
	Path string
	// NormalizedPath is the absolute, symlink-resolved path when it is known,
	// empty otherwise.
	NormalizedPath string
	// Label is the source's own label for the row (a Herdr workspace name,
	// a "~/..." directory label, a session name, a custom row label...).
	Label string
	// Source names the provider: herdr, workspaces, zoxide, projects,
	// sessions, agents, path, or a custom source name. Rows shep synthesizes
	// itself (Herdr tabs and panes) have no source.
	Source string
	// Kind is one of the Kind* constants.
	Kind string
	// Icon is the icon the provider supplied for the row, if any.
	Icon string
	// Branch is the checked-out branch of a worktree. A detached worktree
	// reports its short head instead ("detached" when even that is unknown).
	Branch string
	// Head is the abbreviated (7 character) commit a worktree points at.
	Head string
	// RepoName is the repository name a worktree belongs to.
	RepoName string
	// IsWorktree reports a git worktree row, including the main checkout of a
	// repository that has linked worktrees.
	IsWorktree bool
	// IsMainWorktree reports the main checkout among a repository's worktrees.
	IsMainWorktree bool
	// Agent is the agent running in a pane (claude, codex, pi...).
	Agent string
	// AgentStatus is that agent's state: working, blocked, done, idle or
	// unknown.
	AgentStatus string
	// TabNumber is the Herdr tab number of a tab row.
	TabNumber string
	// TabLabel is the Herdr tab label of a tab, pane or agent row.
	TabLabel string
	// Workspace is the label of the Herdr workspace a tab, pane or agent row
	// belongs to.
	Workspace string
	// Meta carries every provider key verbatim, for custom data. A missing
	// key renders empty. Templates must treat it as read-only; no function in
	// the set can modify it.
	Meta map[string]string
}
