# Changelog

All notable changes to this project are documented in this file.

## Unreleased

### Breaking

- `close_on_exit` is now validated at config `Load`. Previously-ignored
  combinations are now rejected with a hard error instead of being silently
  no-op:
  - `close_on_exit = true` on a `type = "group"` workspace.
  - `close_on_exit = true` on a workspace that also sets `template = "..."`.
  - `close_on_exit = true` on a top-level `[templates.<name>]` that also sets
    `tabs` (per-tab/per-pane close-on-exit is the node-level feature).
  - `close_on_exit = true` with no `command` set (it would never trigger).

  **Migration**: set `close_on_exit = false` (or remove the key) on any
  affected `[[workspaces]]` or `[templates.<name>]` entry.

### Added

- `shep open --target=workspace|tab|pane` selects where a Command-only entry
  opens: `workspace` (default, unchanged behavior), `tab` (new tab in the
  Herdr workspace shep is running inside), or `pane` (new pane split beside
  the current one). `tab`/`pane` require shep to be running inside a Herdr
  workspace pane and only support Command-only entries.
- TUI keybindings `Ctrl+T` (open as new tab) and `Ctrl+P` (open as new pane)
  mirror `--target=tab`/`--target=pane` from the interactive picker; `Enter`
  keeps the previous default behavior.
- `close_on_exit` now also works on a workspace top-level `command`
  (`[[workspaces]]` with `command = "..."`) and on a simple-command
  `[templates.<name>]` (no `tabs`), not just on template leaf nodes.

### Changed

- `FocusOrCreate` no longer falls back to a CWD-only match for
  `[[workspaces]]`-sourced candidates when the label doesn't match: a
  candidate's identity is now its label plus its cwd together, not the cwd
  alone. **Behavior**: renaming a `[[workspaces]]` entry's `name` no longer
  re-focuses the Herdr workspace created under the old name — `shep open`
  for that entry now creates a brand-new workspace, leaving the old one open
  and orphaned. **Migration**: to rename an entry without losing its
  existing workspace, `shep close` the old Herdr workspace first (or rename
  it directly in Herdr), then edit the `name` field and `shep open` the
  entry again to re-create it under the new name. **Why**: prevents
  cross-label collisions when two `[[workspaces]]` entries share the same
  `path` (a CWD-only match could previously focus the wrong entry's
  workspace).
</content>
