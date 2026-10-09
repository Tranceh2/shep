# Changelog

All notable changes to this project are documented in this file.

## Unreleased

### Changed

- The configuration schema is version 1, shep's first stable schema:
  configuration files set `version = 1`, and any other version fails to load
  with a message naming the one shep needs.
- Theme names are Herdr's own names and aliases, plus `catppuccin-frappe`,
  `catppuccin-macchiato` and `plain`; the shorter `mocha`, `frappe` and
  `macchiato` are not theme names.

### Fixed

- Pinning a row pins that row only. It used to pin every row at the same
  directory: the open workspaces there, the group entries and the commands
  rooted at it.
- The pin star lines up across the list: it is the last marker of every row,
  a group's `›` included, and while the view holds a pinned row the other
  rows keep its cells blank.

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
  tab or pane moves back to its workspace and collapses it; `ctrl+l` and
  `ctrl+h` act like `right` and `left`, and `ctrl+r` switches between the list
  alone and the list with the preview.

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
  right after opening the picker are no longer lost (Bubble Tea v1 queried the
  terminal at startup and discarded them), a bracketed paste is always text and
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

## Earlier versions

The 0.x releases were previews; v1.0.0 is the first stable release.
