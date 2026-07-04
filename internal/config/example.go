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

# [layouts.<glob>] runs a startup command in the focused workspace via
# "herdr pane run" after shep creates/focuses a workspace whose path matches
# the glob. Leave empty in v1 unless you need a startup hook.
# [layouts."**/*.go"]
# startup = "go test ./..."
`
}
