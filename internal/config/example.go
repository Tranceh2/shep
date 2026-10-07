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

version = 2

[general]
# source_order lists the enabled built-in sources and their merge/display order.
# Built-ins are herdr, sessions (opt-in), agents (opt-in), workspaces, zoxide, and projects.
# Valid names: herdr, sessions, agents, workspaces, zoxide, projects, plus any name
# declared in [[sources.custom]] below. Unknown names fail fast.
source_order = ["herdr", "workspaces", "zoxide", "projects"]
# selector picks the interactive picker for "shep open" after the direct
# (exact / single-match) short-circuit. Valid values: builtin, fzf, auto.
selector = "builtin"
# workspace_name controls only newly created dynamic workspaces. Like every
# template it receives the shared data (.Path, .NormalizedPath, .Label,
# .Source, .Kind, .Branch, .RepoName, .IsWorktree, .IsMainWorktree, .Meta...)
# and functions (base, dir, lower, tilde, name, parent...). Explicit
# workspace names and existing Herdr workspaces bypass this policy.
# workspace_name = '{{ .Path | base | lower }}'

# [ranking] controls private local adaptive ordering. It stores only opaque
# action/resource identities, bounded counts, and timestamps. It never stores
# labels, queries, templates, environment data, telemetry, or network state.
[ranking]
enabled = true

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
# the responsive default (the picker chooses wide or list-only from terminal
# width); ctrl+l toggles session-only auto/landscape while the picker is open.
# There is no portrait/stacked mode. theme is "inherit" (the default: Herdr's
# own theme, [theme.custom] included), a built-in theme or alias (catppuccin
# alias mocha, catppuccin-latte, catppuccin-frappe, catppuccin-macchiato,
# tokyo-night, dracula, nord, gruvbox, one-dark, solarized, kanagawa,
# rose-pine, vesper, their light variants, terminal), "plain" (no color) or
# the name of a [themes.<name>] table. NO_COLOR and SHEP_THEME win over it.
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
# ctrl+x closes the selected open Herdr pane, tab, or workspace immediately.
# Optionally require y in the footer for any subset of these kinds; every
# other key cancels. Omitted or [] means no confirmation.
# confirm_close = ["workspace", "tab"]

# [preview] configures the workspace preview shown in the "shep open" selector
# and the "shep preview" command. Built-in sections (identity, path/label/
# source, git summary, workspace tabs/panes, active pane buffer, directory
# listing, agent status) are hardcoded and always available by name; a
# workspace or wildcard's own preview = [...] list wins over anything below.
#
# This example intentionally does NOT set [preview].default. With no global
# default, each source falls back to its own [sources.<name>].preview list
# below — the shipped defaults, spelled out here so they stay visible and
# editable instead of being an invisible fallback: herdr shows
# workspace/active_pane/agent_status, workspaces and zoxide show
# identity/dir, projects adds git. Set [preview]'s own "default" list only
# if you want ONE list to replace every source's list at once; it is a
# single global override, not a per-source tweak, so most configs leave it
# unset, as this example does.
[preview]
timeout = "150ms"
cache_ttl = "5s"
max_lines = 50

# [preview.commands.<name>] declares a reusable global preview command referenced
# by name from any preview = [...] list, alongside the built-ins above. It keeps
# its backwards-compatible shell-style command string, but execution is still
# argv-based with no shell expansion or sh -c.
[preview.commands.recent_commits]
command = "git -C {{.Path}} log -n 3"

# [sources.<name>] configures the presentation of a built-in source. Only
# herdr, sessions, agents, workspaces, zoxide and projects are recognised. The preview lists
# below are the exact shipped defaults for each source.
[sources.herdr]
icon = "󰳆 "
preview = ["workspace", "active_pane", "agent_status"]

# [sources.agents] applies to the agents tab and agents in source/group tabs.
# The default label_format is "{{ status }} {{ or .Label .Path | tilde }}":
# the agent's status glyph, then its title. Templates also see .Agent,
# .AgentStatus, .TabLabel, .Workspace (the Herdr workspace label) and every
# metadata key: .Meta.workspace_id, .Meta.tab_id, .Meta.pane_id,
# .Meta.terminal_title, .Meta.kind... Missing keys render empty. Herdr itself
# shortens terminal_title; Shep cannot recover any text Herdr omits.
[sources.agents]
icon = " "
# preview = ["identity", "git"] # unset uses [preview].default; [] shows only identity

[sources.workspaces]
icon = " "
preview = ["identity", "dir"]

[sources.zoxide]
icon = " "
preview = ["identity", "dir"]

[sources.projects]
icon = " "
# recursive/max_depth bound how deep the projects source scans beneath a
# group workspace's path. markers can be a file or a directory name; a
# directory containing any of them is a project. ignore skips noisy
# directories during the scan.
recursive = true
max_depth = 3
markers = [".git", ".project", "package.json", "go.mod", "Cargo.toml", "pyproject.toml", "flake.nix"]
ignore = ["node_modules", "vendor", ".direnv", ".devenv", "target", "dist", ".cache"]
preview = ["identity", "git", "dir"]

# [[sources.custom]] declares an external command that emits picker rows as a
# JSON array on stdout. command is argv only — no shell, no "sh -c", no
# interpolation — so it never needs quoting or escaping; write a small script
# (a jq filter, a Python/Go helper, etc.) if you need to reshape a tool's
# native output into the row schema below, and point command at that script.
# Each row is a JSON object: label (required), plus optional path, command,
# aliases, icon, template, close_on_exit, and inert string metadata. Metadata
# cannot override launch, identity, grouping, control, or TUI fields. Reserved
# keys are rejected with a row/key error; use typed row fields for command,
# template, and close_on_exit. The custom-source marker is set internally.
# A row with path opens as an ordinary workspace through the open pipeline; a row
# with command runs that command instead (in the root pane of a freshly
# created workspace, or via --target=tab/pane inside the current one); a row
# may set both. name must be unique and must not collide with a built-in
# source name. Add it to general.source_order to run it globally, or exclude it
# there and include it only in a group's source_order to load it lazily when
# that group opens. Example: a script wrapping "gh pr list --json number,title,headRefName"
# and reshaping each PR into {"label": "#42 fix bug", "command": "gh pr checkout 42",
# "aliases": ["review", "bug"], "meta": {"context": "review"}}:
# [[sources.custom]]
# name = "prs"
# command = ["/path/to/shep/scripts/list-prs.sh"]
# aliases = ["pull request", "review"]
# icon = " "
# timeout = "3s"
# preview selects built-ins, global commands, and only this custom source's
# private preview_commands entries, in the listed order. Local names cannot
# collide with built-in or global preview names; the same local name may be used
# by different custom sources. Local timeout/max_lines inherit [preview] values
# when omitted. Custom source JSON rows provide metadata only and cannot declare
# executable preview commands.
# preview = ["identity", "cluster", "health"]
# [sources.custom.preview_commands.cluster]
# command = ["kubectl", "config", "view", "--minify", "-o", "jsonpath={..context}"]
# max_lines = 12
# [sources.custom.preview_commands.health]
# command = ["kubectl", "get", "--context", "{{ index .Meta \"context\" }}", "--raw", "/healthz"]
# timeout = "1s"
# max_lines = 10

# [[workspaces]] lists predefined projects (or nested picker groups) shep
# surfaces as selectable candidates. name is the candidate label; aliases are
# alternate search terms only and are not displayed. path may use "~/..." which
# shep expands to your home directory.
# [[workspaces]]
# name = "dotfiles"
# path = "~/dotfiles"
# aliases = ["config", "dot files"]
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
# drawing candidates from its own source_order list. If source_order is omitted,
# the global source order is used while the group's source membership remains
# scoped. Declared custom sources can be listed here without being added to
# general.source_order; they run lazily
# only after this group opens.
# [[workspaces]]
# name = "projects"
# type = "group"
# path = "~/projects"
# source_order = ["projects", "zoxide"]
# [workspaces.sources.projects]
# max_depth = 5
# template = "dev"
#
# A group may list a declared custom source in source_order without adding it to
# general.source_order. That custom source command runs lazily only after the
# group opens, and its rows use the normal picker and launch behavior.

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
# means a plain shell). Node ids are internal references scoped to their own
# tab; they are not persistent Herdr labels. A leaf may set label to choose its
# persistent pane label: omit label to preserve the current label, set label =
# "" to clear it, or set a non-empty value to rename it. Labels cannot begin
# with '-' due to a Herdr CLI limitation; use label = "" to clear. Branch nodes
# cannot set label.
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

# [[wildcards]] binds a glob pattern to a workspace name, template, preview
# and/or row presentation (icon, label_format, ...) override. Rules are
# scanned in declaration order: each setting comes from the first matching
# rule that sets it.
# [[wildcards]]
# pattern = "~/projects/kubernetes/**"
# workspace_name = '✈️ {{ printf "%s/%s" (.Path | dir | base) (.Path | base) }}'
# template = "k8s"
# preview = ["identity", "git", "recent_commits"]
`
}
