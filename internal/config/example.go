package config

// ExampleTOML returns a commented, path-agnostic example configuration suitable
// for `shep init` to write to disk. It contains no absolute user-home paths;
// example roots live only in comments as generic placeholders the user
// replaces.
func ExampleTOML() string {
	return `# shep configuration — see https://github.com/tranceh2/shep
#
# Place this file at $XDG_CONFIG_HOME/shep/config.toml (or the equivalent
# os.UserConfigDir location on your platform). Run "shep init --force" to
# regenerate it.

# [general] overrides global behaviour. provider_order is optional; leave it
# unset to use the default order: herdr -> roots -> zoxide -> cwd.
# selector picks the interactive picker for "shep open" after the direct
# (exact / single-match) short-circuit. Valid values: builtin, fzf, auto.
#   builtin -> always use the Bubble Tea TUI (skip fzf even if installed)
#   fzf     -> prefer fzf, fall back to the Bubble Tea TUI when fzf is absent
#   auto    -> v1 behaviour: fzf if installed, else the Bubble Tea TUI
# Absent or empty defaults to "builtin".
# [general]
# provider_order = ["herdr", "zoxide", "cwd"]
# selector = "builtin"

# [herdr] locates the Herdr CLI binary. Leave binary empty to use "herdr" from
# PATH. Set it to an absolute path only if Herdr is not on PATH.
# [herdr]
# binary = "herdr"

# [defaults] supplies the fallback startup/preview commands applied when no
# predefined workspace (see [[workspaces]]) and no wildcard (see [[wildcards]])
# matched the resolved candidate. Leave unset to skip a default startup.
# [defaults]
# startup = "make"
# preview = "echo hi"

# [[workspaces]] lists predefined projects shep surfaces as selectable
# candidates (under the "config" source). name is the candidate label; path may
# use "~/..." which shep expands to your home directory; an optional startup
# overrides [[wildcards]] and [defaults] for this workspace.
# [[workspaces]]
# name = "docs"
# path = "~/docs"
# startup = "just serve"
#
# [[workspaces]]
# name = "shep"
# path = "~/code/shep"

# [[wildcards]] binds a glob pattern to a startup command, scanned in
# declaration order on the resolved candidate's normalised path or base name.
# First match wins. [[wildcards]] replaces the legacy per-glob startup table.
# [[wildcards]]
# pattern = "**/*.go"
# startup = "go test ./..."
#
# [[wildcards]]
# pattern = "Cargo.toml"
# startup = "cargo build"

# [sources.<name>] adds extra project roots to discover beyond the built-in
# providers (herdr workspaces, zoxide, cwd). kind selects the provider family.
#
# A "roots" source scans a directory you choose. Replace the example path
# below with your own; shep ships with NO default roots so this file is safe
# to check into dotfiles across machines.
# [sources.repos]
# kind = "roots"
# enabled = true
#
# [sources.repos.options]
# path = "~/code"  # <- set this to your projects directory

# Built-in providers can be disabled by declaring a source with the matching
# kind and enabled = false. For example, to stop shep from listing zoxide
# entries:
# [sources.zoxide]
# kind = "zoxide"
# enabled = false

# [preview] configures the workspace preview shown in the "shep open" selector
# and the "shep preview" command. With no [preview] table shep shows a calm built-in
# layout: label, path, source, matched template (when present), and a fast git
# summary. timeout/cache_ttl/max_lines default to 100ms / 5s / 50 lines.
# [preview]
# command = "git -C {path} log -n 5"   # escape hatch: {path} is one arg, no sh -c
# timeout = "100ms"
# cache_ttl = "5s"
# max_lines = 50

# [[preview.sections]] override the built-in layout IN DECLARATION ORDER. type
# is "builtin" (render named candidate fields) or "git" (render a git summary).
# builtin fields: path, label, source, template. Unknown types/fields fail
# fast at load so typos surface immediately.
# [[preview.sections]]
# name = "Identity"
# type = "builtin"
# fields = ["label", "path", "source", "template"]
#
# [[preview.sections]]
# name = "Git"
# type = "git"
`
}
