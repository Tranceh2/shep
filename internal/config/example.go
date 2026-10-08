package config

import "strings"

// ExampleTOML returns the commented, path-agnostic configuration `shep init`
// writes. It contains no absolute user-home paths; example roots are generic
// placeholders (~/...) the user replaces. Every source's row presentation is
// listed as commented keys holding the built-in defaults (presentationDefaults,
// [tui].icons = "unicode"), so the document never drifts from them.
func ExampleTOML() string {
	return strings.NewReplacer(
		"%%herdr%%", commentedPresentation(rowHerdr),
		"%%herdr.tab%%", commentedPresentation(rowHerdrTab),
		"%%herdr.pane%%", commentedPresentation(rowHerdrPane),
		"%%sessions%%", commentedPresentation(rowSessions),
		"%%agents%%", commentedPresentation(rowAgents),
		"%%workspaces%%", commentedPresentation(rowWorkspaces),
		"%%zoxide%%", commentedPresentation(rowZoxide),
		"%%projects%%", commentedPresentation(rowProjects),
	).Replace(exampleTOML)
}

// commentedPresentation renders kind's built-in presentation as commented
// TOML keys, one per part, each value a literal string.
func commentedPresentation(kind rowKind) string {
	p := presentationDefaults(kind, TUIIconsUnicode)
	var b strings.Builder
	for _, kv := range [][2]string{
		{"icon", p.Icon},
		{"icon_color", p.IconColor},
		{"label_format", p.Label},
		{"detail_format", p.Detail},
		{"marker_format", p.Marker},
	} {
		b.WriteString("# " + kv[0] + " = " + tomlLiteral(kv[1]) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// tomlLiteral writes s as a TOML literal string. The built-in defaults never
// hold a single quote or a newline, which a literal string cannot.
func tomlLiteral(s string) string {
	if strings.ContainsAny(s, "'\n") {
		panic("config: default template " + s + " cannot be a TOML literal string")
	}
	return "'" + s + "'"
}

const exampleTOML = `# shep configuration — see https://github.com/tranceh2/shep
#
# Place this file at $XDG_CONFIG_HOME/shep/config.toml, or at
# ~/.config/shep/config.toml when XDG_CONFIG_HOME is unset (this XDG order is
# used on every platform, including macOS). Run "shep init --force" to
# regenerate it. The README's "Customization" section documents every
# template field, function, row part, theme token and role used below.

# version is required: shep loads only schema version 3 (see "Migrating from
# version 2" in the README for older files).
version = 3

[general]
# source_order lists the enabled sources and their merge/display order.
# Valid names: herdr, sessions (opt-in), agents (opt-in), workspaces, zoxide,
# projects, plus any name declared in [[sources.custom]] below. Unknown names
# fail fast.
source_order = ["herdr", "workspaces", "zoxide", "projects"]
# selector picks the interactive picker for "shep open" after the direct
# (exact / single-match) short-circuit. Valid values: builtin, fzf, auto.
selector = "builtin"
# workspace_name names the Herdr workspaces shep creates. It is a template over
# the shared data (.Path, .Label, .Kind, .Branch, .RepoName, .IsWorktree,
# .Meta...) and functions (base, dir, lower, tilde, name, parent, trimIcon...).
# Unset: worktrees are named "<repo>@<branch>", everything else by its full
# path. [[wildcards]] can set their own; existing workspaces keep their names.
# workspace_name = '{{ .Path | base | lower }}'

# [ranking] controls private local adaptive ordering. Its store keeps only
# opaque action/resource identities, bounded counts, and timestamps, never
# labels, queries, templates, environment data, telemetry, or network state.
# Separately, while it is enabled, the picker keeps the last 50 searches that
# ended in a selection ($XDG_STATE_HOME/shep/queries) for ctrl+y. enabled =
# false turns both off; "shep ranking clear" forgets both.
[ranking]
enabled = true

# [herdr] locates the Herdr CLI binary. Leave binary empty to use "herdr" from
# PATH. Set it to an absolute path only if Herdr is not on PATH.
[herdr]
# binary = "herdr"

# [defaults] names the [templates.<name>] applied to a freshly created
# workspace when no workspace or wildcard template applies.
[defaults]
template = "default"

# [tui] configures the picker's tabs, pane sizing, orientation, theme and
# glyphs. list_width/preview_width are "auto" or a percentage like "60%"
# (share of the split axis). layout is "landscape" (forces side-by-side) or
# omitted for the responsive default (the picker chooses wide or list-only
# from terminal width); ctrl+r shows or hides the preview for the session
# while the picker is open.
[tui]
# tabs lists the views cycled with tab / shift+tab, in order: all, agents, a
# built-in source name, a [[sources.custom]] name, or a group workspace id. A
# source tab does not add that source to "all" (see general.source_order).
# tabs = ["all", "agents"]
# The defaults below are what shep uses with nothing set: a 35% list and a
# 65% preview, side by side on wide terminals and list-only on narrow ones.
# list_width = "35%"
# preview_width = "65%"
# layout = "landscape" # force side-by-side at every width
# theme is "inherit" (the default: Herdr's own theme, [theme.custom]
# included), a built-in theme or alias (catppuccin, catppuccin-latte,
# catppuccin-frappe, catppuccin-macchiato, tokyo-night, dracula, nord,
# gruvbox, one-dark, solarized, kanagawa, rose-pine, vesper, their light
# variants, terminal), "plain" (no color) or the name of a [themes.<name>]
# table. NO_COLOR and SHEP_THEME win over it.
theme = "inherit"
# icons selects the glyph tier: "unicode" (the default; Nerd Font source icons
# plus plain Unicode symbols for the picker's own status, pin, group, tree and
# chrome glyphs) or "ascii" (7-bit ASCII only: no default source icons, ASCII
# markers). Icons you configure below are your own templates, drawn as written.
icons = "unicode"
# ctrl+x closes the selected open Herdr pane, tab, or workspace immediately.
# Optionally require y in the footer for any subset of these kinds; every
# other key cancels. Omitted or [] means no confirmation.
# confirm_close = ["workspace", "tab"]

# [themes.<name>] declares a custom theme selected with [tui].theme: a base
# (a built-in theme, "inherit" or another custom theme; default catppuccin),
# any of Herdr's 19 palette tokens, and role overrides. A role takes a token,
# another role or a color (#rrggbb, #rgb, rgb(r,g,b), a terminal color name,
# reset).
# [themes.example]
# base = "inherit"
# accent = "#f5c2e7"
# selection_bg = "#45475a"
# [themes.example.roles]
# "row.detail" = "subtext0"
# "source.zoxide" = "teal"
# "pin" = "warning"

# [preview] configures the workspace preview shown in the "shep open" selector
# and the "shep preview" command. Built-in sections (identity, git, workspace,
# session_info, active_pane, dir, agent_status) are always available by name;
# a [[workspaces]] entry's or a wildcard's own preview = [...] list wins over
# the source lists below.
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

# [preview.commands.<name>] declares a reusable preview command referenced by
# name from any preview = [...] list, alongside the built-ins above. command
# is a shell-style string whose tokens are templates, but execution is argv
# based with no shell expansion or sh -c. title is the section's heading in
# the picker: unset uses the humanized name ("Recent commits"), "" draws the
# output without a heading.
[preview.commands.recent_commits]
command = "git -C {{.Path}} log -n 3"
# title = "Recent commits"

# [sources.<name>] configures a built-in source: herdr, sessions, agents,
# workspaces, zoxide and projects. Each one draws its rows from five parts,
#
#   [icon] label  detail                                   marker
#
# set by icon, icon_color, label_format, detail_format and marker_format. Each
# is a template; icon_color is a palette token, a role or a color. The
# commented values are the built-in defaults: uncomment a key to change it,
# or set it to "" to draw nothing in that part. The preview lists are the
# exact shipped defaults for each source.
[sources.herdr]
%%herdr%%
preview = ["workspace", "active_pane", "agent_status"]

# The tab and pane rows nested under an open Herdr workspace.
[sources.herdr.tab]
%%herdr.tab%%

[sources.herdr.pane]
%%herdr.pane%%

# Herdr sessions (opt-in: add "sessions" to source_order). Without a preview
# list of their own, sessions show session_info.
[sources.sessions]
%%sessions%%

# [sources.agents] applies to the agents tab and agents in source/group tabs.
# Templates also see .Agent, .AgentStatus, .TabLabel, .Workspace (the Herdr
# workspace label) and every metadata key: .Meta.workspace_id, .Meta.tab_id,
# .Meta.pane_id, .Meta.terminal_title, .Meta.kind... Missing keys render
# empty. Herdr itself shortens terminal_title; Shep cannot recover any text
# Herdr omits. Keep {{ status }} in label_format to keep the status glyph.
[sources.agents]
%%agents%%
# preview = ["identity", "git"] # unset uses [preview].default; [] shows only identity

[sources.workspaces]
%%workspaces%%
preview = ["identity", "dir"]

[sources.zoxide]
%%zoxide%%
preview = ["identity", "dir"]

[sources.projects]
%%projects%%
# roots are the directories the projects source scans; there is no default,
# so without roots only group workspaces scan, beneath their own path.
# recursive descends into subdirectories (without it only the direct
# children are checked) and max_depth bounds how deep. markers can be a file
# or a directory name; a directory containing any of them is a project.
# ignore skips noisy directories during the scan.
# roots = ["~/code", "~/work"]
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
# command = ["/path/to/list-prs-for-shep"]
# aliases = ["pull request", "review"]
# timeout = "3s"
# Custom rows take the same five presentation keys. The default icon is
# '{{ .Icon }}', each row's own JSON icon; this keeps it and falls back to a
# glyph for rows without one:
# icon = '{{ .Icon | default " " }}'
# label_format = 'PR {{ .Label }}'
# marker_format = '{{ .Meta.context }} {{ pin }}'
# preview selects built-ins, global commands, and only this custom source's
# private preview_commands entries, in the listed order. Local names cannot
# collide with built-in or global preview names; the same local name may be used
# by different custom sources. Local timeout/max_lines inherit [preview] values
# when omitted, and title works as in [preview.commands]. Custom source JSON
# rows provide metadata only and cannot declare executable preview commands.
# preview = ["identity", "cluster", "health"]
# [sources.custom.preview_commands.cluster]
# command = ["kubectl", "config", "view", "--minify", "-o", "jsonpath={..context}"]
# title = "Context"
# max_lines = 12
# [sources.custom.preview_commands.health]
# command = ["kubectl", "get", "--context", "{{ index .Meta \"context\" }}", "--raw", "/healthz"]
# timeout = "1s"
# max_lines = 10

# [[workspaces]] lists predefined projects (or nested picker groups) shep
# surfaces as selectable candidates. name is the candidate label; aliases are
# alternate search terms only and are not displayed. path may use "~/..." which
# shep expands to your home directory. An entry's presentation keys (icon,
# icon_color, label_format, detail_format, marker_format), template, command
# and close_on_exit apply to its own row only; its preview list also applies
# to every other row for the same directory.
# [[workspaces]]
# name = "dotfiles"
# path = "~/dotfiles"
# aliases = ["config", "dot files"]
# icon = " "
# marker_format = '{{ pin }}'
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
# general.source_order; they run lazily only after this group opens. A group's
# template applies to the rows picked through it. id is a stable name for the
# group in [tui].tabs and "shep open --view".
# [[workspaces]]
# id = "projects"
# name = "projects"
# type = "group"
# path = "~/projects"
# source_order = ["projects", "zoxide"]
# template = "dev"
# [workspaces.sources.projects]
# max_depth = 5

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

# [[wildcards]] applies settings to every row whose path (or base name)
# matches a glob: workspace_name, template, preview and the presentation keys.
# "~" is your home directory and "**" any number of directories. Rules are
# scanned in declaration order separately for every setting: each setting
# comes from the first matching rule that sets it. Session rows never match.
# [[wildcards]]
# pattern = "~/projects/kubernetes/**"
# workspace_name = '󱃾 {{ printf "%s/%s" (.Path | dir | base) (.Path | base) }}'
# template = "k8s"
# preview = ["identity", "git", "recent_commits"]
# icon = "󱃾 "
# icon_color = "blue"
`
