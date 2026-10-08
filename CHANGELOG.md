# Changelog

All notable changes to this project are documented in this file.

## v1.0.1

### Changed

- Installing the Herdr plugin no longer needs Go: the build step downloads
  the release binary of the plugin's version for your platform and checks it
  against the release checksums, and builds the checkout only if that fails.
  `SHEP_PLUGIN_BUILD=source` builds a local development link from source.
- The README's installation starts with the plugin in three steps (install,
  add a shortcut, reload), followed by the optional steps, updating, and the
  command-line installs.

### Fixed

- `shep --version` names the release when shep was installed with
  `go install github.com/tranceh2/shep/cmd/shep@<version>` (it printed `dev`)
  or as a Herdr plugin (it printed the commit hash: Herdr's checkout has no
  tags, so the plugin build now takes the version from its manifest).

## v1.0.0

The first stable release: the picker is redesigned, every part of it is
configurable, it acts on Herdr without leaving it, and it opens fast even
after the system has evicted it from memory.

### Breaking

- Configuration files must declare `version = 3`. Any other version, or none,
  fails to load with one message pointing to the README's
  [Migrating from version 2](README.md#migrating-from-version-2). The main
  steps: replace `osBase`, `osDir`, `osClean`, `osExt` and `osIsAbs` with
  `base`, `dir`, `clean`, `ext` and `isAbs`; move
  `[sources.herdr].tab_label_format` and `pane_label_format` to
  `label_format` in `[sources.herdr.tab]` and `[sources.herdr.pane]`; a
  custom agents or pane `label_format` must include `{{ status }}` to keep
  the status glyph; delete `[defaults].type` and
  `[workspaces.sources.projects].preview` (neither had an effect).
- `[[integrations]]` is now `[[sources.custom]]`, with the same argv-only
  JSON-row contract, previews and aliases. **Migration**: rename the table.
- `shep open --agents` is removed. **Migration**: use `shep open --view agents`.
- `[[wildcards]]` are scanned per setting: each setting comes from the first
  matching rule that sets it, where it used to stop at the first matching
  rule. An explicit `preview = []` on a rule or entry now means "no
  sections", and a group's template beats wildcards for its children.
- `ctrl+u` moves the cursor half a page up instead of clearing the query;
  `esc` clears it, `backspace` deletes a character and `ctrl+w` /
  `alt+backspace` a word.
- The layout key moves from `ctrl+l` to `ctrl+r`, and `ctrl+l` / `ctrl+h`
  now expand and collapse like `right` / `left`.

### Added

- The picker shows configurable view tabs (`[tui].tabs`): `all`, `agents`, a
  built-in source, a `[[sources.custom]]` name or a group workspace's `id`
  (a new `[[workspaces]]` key), cycled with `tab` / `shift+tab`. A source listed only there loads without
  joining `all`. `shep open --view <id>` opens any of them directly.
- `agents` is a built-in source: newly blocked or finished agents first, then
  the previous agent, then the rest by history.
- `ctrl+x` closes the selected open Herdr pane, tab or workspace, with a y/n
  confirmation for the kinds listed in `[tui].confirm_close`. `ctrl+d` /
  `ctrl+u` move half a page; `ctrl+w` / `alt+backspace` delete a word.
- Every visible part of a row is a template: `icon`, `icon_color`,
  `label_format`, `detail_format` and `marker_format`, per source, per
  `[[sources.custom]]`, for Herdr tab and pane rows, in `[[wildcards]]` and
  in `[[workspaces]]` entries. Templates share one engine and data model
  with `workspace_name` and preview commands, and gain `tilde`, `name`,
  `parent`, `trimIcon`, the style functions `muted`, `accent` and `bold`,
  and the live values `status`, `pin`, `current`, `group` and `missing`.
- Themes follow Herdr: the default inherits Herdr's own theme (its 18
  palettes, `[theme.custom]`, `auto_switch` light/dark variants and the
  `[ui].accent` fallback). `[tui].theme` also takes any Herdr built-in,
  `plain`, or a `[themes.<name>]` with a base, token overrides and roles;
  `SHEP_THEME` and `NO_COLOR` override it. `shep doctor` reports the theme,
  where it came from and Herdr's theme diagnostics.
- Preview command sections take an optional `title` (`""` hides the
  heading); `shep list --format json` includes `meta`.
- `ctrl+e` renames the highlighted open workspace, tab or pane in place of
  the search prompt (`enter` applies, `esc` cancels).
- `ctrl+n` on a row inside a Git repository asks for a branch, creates the
  worktree and a focused workspace on it through Herdr, and names and lays
  it out like any workspace shep creates (`repo@branch` by default).
- `ctrl+b` jumps to the next blocked agent in the agents view; the footer
  offers it while one is blocked.
- `ctrl+y` brings back earlier searches, newest first: the last 50 queries
  that ended in a selection, kept in `$XDG_STATE_HOME/shep/queries` while
  `[ranking]` is enabled. `shep ranking clear` forgets them along with the
  learned order, pins and acknowledged agent states.
- `right` on an expanded workspace moves onto its first tab, and `left` on a
  tab or pane moves back to its workspace and collapses it.

### Changed

- The picker is redesigned for scanning: one frame (Herdr's popup border),
  tabs always visible, a prompt with a cursor and matches/total, name-first
  rows with the parent dimmed, right-hand markers for agent state, pins,
  groups, worktree branches and "current", workspaces that open only along
  a matching tab or pane, and a preview with a title row, a summary line, a
  Tabs table, Files, one heading per custom command and the newest lines of
  the pane capture. Help is a cheat sheet of the real keys and search syntax.
- Every per-candidate setting (presentation, preview sections, template,
  `workspace_name`) resolves with one rule, per field: the candidate's own
  data, then same-directory `[[workspaces]]` entries (preview sections
  only), then the first `[[wildcards]]` rule that sets it, then the source,
  then the defaults.
- The `agent_status` preview reports the candidate workspace's own most
  urgent agent state; the `dir` section lists names only.
- `shep init` writes the defaults shep actually uses, as commented examples.
- The TUI runs on Bubble Tea v2, Lip Gloss v2 and Bubbles v2. Keys typed
  right after opening the picker are no longer lost (v1 queried the terminal
  at startup and discarded them), a bracketed paste is always text and
  never a key binding, a theme following the terminal's appearance asks for
  the background without blocking startup, and the terminal gets its
  keyboard mode back when the picker exits.

### Fixed

- Fast typing and pastes keep every rune; backspace removes whole runes, so
  accented input stays valid UTF-8.
- Control characters in pane captures and external data (CRLF line endings,
  C0/C1 controls) no longer blank or corrupt rows of the frame.
- Two quick `esc` presses act as two presses; `ctrl+x` works on expanded tab
  and pane rows; an unnamed tab's number is shown once in the preview Tabs
  table.
- `nix build` works again: the flake's `vendorHash` follows `go.mod`.
- The layout key always changes what is visible: the list alone while the
  preview shows, both side by side otherwise. It used to switch between auto
  and landscape, which look the same from 80 columns, so in the Herdr popup
  it never changed anything.

### Performance

- The first frame no longer waits on any Herdr socket call or on the SQLite
  ranking store, and the plugin wrapper builds its `PATH` in linear time:
  inside Herdr the picker appears 70-90 ms after the shortcut instead of
  100-370 ms.
- A keystroke ranks only the rows a query can show, from ranking keys
  computed once per candidate set, scores in pooled memory and never touches
  the disk (paths are normalized in the producers): about 1.7x faster per
  key with a long ranking history, under a third of the memory, and a third of
  the terminal output. The renderer runs at 120 frames per second, so a
  key's echo waits at most 8 ms for its frame.
- Inside Herdr every request (the snapshot behind the workspace rows, the
  preview's pane capture, focusing or creating on `enter`, `jump-back`) goes
  straight to the Herdr socket instead of launching the `herdr` CLI, and the
  plugin's open action asks for the popup through shep: launching the CLI
  costs about 200 ms once the system has evicted it. After a while unused,
  Herdr's rows appear about 100 ms after the picker starts instead of
  260-360 ms, and the popup opens 125-300 ms after the shortcut instead of
  480-520 ms.
- The projects scan and custom source commands show their last result the
  moment the picker opens and refresh in the background
  (`$XDG_CACHE_HOME/shep/sources`).

## v0.1.1

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
  Herdr workspace shep is running inside), or `pane` (new pane split beside the
  current one). `tab`/`pane` require shep to be running inside a Herdr
  workspace pane and only support Command-only entries.
- TUI keybindings `Ctrl+T` (open as new tab) and `Ctrl+P` (open as new pane)
  mirror `--target=tab`/`--target=pane` from the interactive picker; `Enter`
  keeps the previous default behavior.
- `close_on_exit` now also works on a workspace top-level `command`
  (`[[workspaces]]` with `command = "..."`) and on a simple-command
  `[templates.<name>]` (no `tabs`), not just on template leaf nodes.
- Herdr plugin builds now honor `HERDR_BIN_PATH` and resolve the Herdr
  executable from the plugin runtime environment instead of silently dropping
  the Herdr source when the host starts plugins with a minimal `PATH`.
- First-run picker presentation now uses a 35/65 list/preview split, label-first
  rows, per-source Nerd Font icons, and source-specific preview sections:
  Herdr rows show workspace, active pane, and agent status; workspaces and
  zoxide show identity and directory; projects show identity, Git, and
  directory content.

### Changed

- `FocusOrCreate` no longer falls back to a CWD-only match for
  `[[workspaces]]`-sourced candidates when the label doesn't match: a
  candidate's identity is now its label plus its cwd together, not the cwd
  alone. **Behavior**: renaming a `[[workspaces]]` entry's `name` no longer
  re-focuses the Herdr workspace created under the old name — `shep open`
  for that entry now creates a brand-new workspace, leaving the old one open
  and orphaned. **Migration**: to rename an entry without losing its
  existing workspace, close the old Herdr workspace first (or rename it
  directly in Herdr), then edit the `name` field and `shep open` the
  entry again to re-create it under the new name. **Why**: prevents
  cross-label collisions when two `[[workspaces]]` entries share the same
  `path` (a CWD-only match could previously focus the wrong entry's
  workspace).
- Session previews now apply the session-specific fallback before the global
  default, while an explicit per-source session preview remains authoritative.
  Explicit empty preview lists are preserved as an intentional request for no
  sections instead of being replaced by defaults.
- Errors from Cobra command dispatch and raw `RunE` failures now reach stderr
  with an actionable message while preserving existing exit codes. Deliberate
  quiet paths, including cancelled picker runs and sanitized jump-back
  diagnostics, remain quiet.

</content>
