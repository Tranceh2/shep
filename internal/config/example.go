package config

// ExampleTOML returns a commented, path-agnostic example configuration suitable
// for `shep init` to write to disk. It contains no absolute user-home paths;
// example roots live only as generic placeholders (~/...) the user replaces.
func ExampleTOML() string {
	return `# shep configuration — see https://github.com/tranceh2/shep
#
# Place this file at $XDG_CONFIG_HOME/shep/config.toml, or at
# ~/.config/shep/config.toml when XDG_CONFIG_HOME is unset (this XDG order is
# used on every platform, including macOS). Run "shep init --force" to
# regenerate it.

version = 1

[general]
# sources lists the enabled built-in sources and their merge/display order.
# Valid names: herdr, workspaces, zoxide, projects. Unknown names fail fast.
sources = ["herdr", "workspaces", "zoxide", "projects"]
# selector picks the interactive picker for "shep open" after the direct
# (exact / single-match) short-circuit. Valid values: builtin, fzf, auto.
selector = "builtin"

# [herdr] locates the Herdr CLI binary. Leave binary empty to use "herdr" from
# PATH. Set it to an absolute path only if Herdr is not on PATH.
[herdr]
# binary = "herdr"

# [defaults] supplies the small set of fallback values used when a resolved
# candidate carries none of its own. type is informational metadata; template
# names the [templates.<name>] applied to a freshly created workspace when no
# workspace/wildcard template matched.
[defaults]
type = "shell"
template = "default"

# [tui] configures the picker's pane sizing, orientation, and theme.
# list_width/preview_width are "auto" or a percentage like "60%" (share of
# the split axis). layout is "landscape" (forces side-by-side) or omitted for
# the responsive default (the picker picks wide/list-only from the terminal
# width); toggle it live for the current session with ctrl+l while the picker
# is open (does not persist to this file). theme is one of "mocha",
# "macchiato", "frappe", "latte", or "plain" (no color, textual markers only);
# omitted defers to the $SHEP_THEME environment variable, then "mocha".
# $NO_COLOR (any non-empty value), when set, always forces "plain" regardless
# of both.
[tui]
list_width = "auto"
preview_width = "60%"
layout = "landscape"
theme = "mocha"
# icons selects the fallback tier for the picker's OWN semantic icons (pane
# agent-status markers, row expand/tab/pane markers): "unicode" (plain
# Unicode symbols, safe on any UTF-8 terminal), or "ascii" (7-bit ASCII only,
# for terminals/locales that cannot render Unicode); omitted defaults to
# "unicode". Does not affect [sources.<name>].icon below, which is your own
# configured string rendered verbatim.
icons = "unicode"

# [preview] configures the workspace preview shown in the "shep open" selector
# and the "shep preview" command. Built-in sections (identity, path/label/
# source, git summary, workspace tabs/panes, active pane buffer, directory
# listing) are hardcoded and always available by name; default picks which
# ones render when nothing more specific (workspace > wildcard > source >
# this default) applies.
[preview]
timeout = "150ms"
cache_ttl = "5s"
max_lines = 50
default = ["identity", "git"]

# [preview.commands.<name>] declares a custom preview command referenced by
# name from any preview = [...] list, alongside the built-ins above. {{.Path}}
# is substituted as one argument value; no shell expansion, no sh -c. Quote an
# action with internal whitespace (for example, "{{ .Path }}"); {{.Path}} is
# safe unquoted because raw command tokenization happens before rendering.
[preview.commands.recent_commits]
command = "git -C {{.Path}} log -n 3"

# [sources.<name>] configures the presentation of a built-in source. Only
# herdr, workspaces, zoxide and projects are recognised.
[sources.herdr]
icon = "󰳆 "
preview = ["workspace", "active_pane"]

[sources.workspaces]
icon = " "
preview = ["identity", "dir"]

[sources.zoxide]
icon = " "
preview = ["identity", "dir"]

[sources.projects]
icon = " "
# recursive/max_depth bound how deep the projects source scans beneath a
# group workspace's path. markers can be a file or a directory name; a
# directory containing any of them is a project. ignore skips noisy
# directories during the scan.
recursive = true
max_depth = 3
markers = [".git", ".project", "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "flake.nix"]
ignore = ["node_modules", "vendor", ".direnv", ".devenv", "target", "dist", ".cache"]
preview = ["identity", "git", "dir"]

# [[workspaces]] lists predefined projects (or nested picker groups) shep
# surfaces as selectable candidates. name is the candidate label; path may
# use "~/..." which shep expands to your home directory.
# [[workspaces]]
# name = "dotfiles"
# path = "~/dotfiles"
#
# [[workspaces]]
# name = "main-app"
# path = "~/projects/main-app"
# template = "dev"
#
# [[workspaces]]
# name = "downloads"
# path = "~/Downloads"
# command = "yazi"
#
# close_on_exit = true on a workspace (with a command) closes the workspace's
# root pane after that command's shell returns control (regardless of exit
# status), via the same shell-chaining used by leaf nodes. It is rejected for
# type=group and template= entries.
# [[workspaces]]
# name = "k9s"
# path = "~/projects/ops"
# command = "k9s"
# close_on_exit = true
#
# type = "group" turns an entry into a nested picker source rooted at path,
# drawing candidates from its own sources list.
# [[workspaces]]
# name = "projects"
# type = "group"
# path = "~/projects"
# sources = ["projects", "zoxide"]
# template = "dev"

# [templates.<name>] describes what opens after Enter for a freshly created
# workspace: a plain command in the root pane, or a structured multi-tab
# layout via tabs/nodes.
[templates.default]
command = ""

[templates.k8s]
command = "k9s"

# close_on_exit = true on a simple-command [templates.<name>] (no tabs) closes
# the workspace's root pane after that command's shell returns control
# (regardless of exit status), via the same shell-chaining as a leaf node. It
# is rejected when tabs is set (per-tab close-on-exit is the node-level
# feature).
# [templates.k9s-close]
# command = "k9s"
# close_on_exit = true

# A tabs-based template lists one or more [[templates.<name>.tabs]] entries.
# Each tab has a name (its label) and, when it needs more than one empty
# shell, a root node id plus [[templates.<name>.tabs.nodes]]. A node with
# split + children is layout-only (rows stacks top/bottom, cols places
# side by side); a node without split is a real pane running command (empty
# means a plain shell). Node ids are scoped to their own tab.
#
# focus = { tab = "...", node = "..." } is declared once at the template
# level (never per tab/node): focus.tab names a declared tab by its name;
# focus.node (optional) names a node id scoped to that same tab. Both are
# validated at load — an unknown tab/node name fails fast. Omitting focus
# entirely keeps the default: the first tab stays focused (it reuses the
# workspace's already-focused root tab).
#
# close_on_exit = true on a leaf node closes its pane after the node's
# command's shell returns control (regardless of exit status — e.g. quitting
# nvim), via shell-chaining a "herdr pane close <pane_id>" after the command.
# [templates.dev]
# description = "development workspace"
# focus = { tab = "AI", node = "opencode" }
#
# [[templates.dev.tabs]]
# name = "code"
# root = "main"
#
#   [[templates.dev.tabs.nodes]]
#   id = "main"
#   split = "rows"
#   children = ["editor", "terminal"]
#   sizes = [80, 20]
#
#   [[templates.dev.tabs.nodes]]
#   id = "editor"
#   command = "nvim ."
#
#   [[templates.dev.tabs.nodes]]
#   id = "terminal"
#   command = ""
#
# [[templates.dev.tabs]]
# name = "AI"
# root = "opencode"
#
#   [[templates.dev.tabs.nodes]]
#   id = "opencode"
#   command = "opencode"
#   close_on_exit = true

# [[wildcards]] binds a glob pattern to a template and/or preview override,
# scanned in declaration order on the resolved candidate's normalised path or
# base name. First match wins.
# [[wildcards]]
# pattern = "~/projects/kubernetes/**"
# template = "k8s"
# preview = ["identity", "git", "recent_commits"]
`
}
