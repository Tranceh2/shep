# shep

[![ci](https://github.com/tranceh2/shep/actions/workflows/ci.yml/badge.svg)](https://github.com/tranceh2/shep/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/tranceh2/shep)](https://goreportcard.com/report/github.com/tranceh2/shep)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`shep` is a fast, keyboard-driven project, workspace, and AI agent launcher built for [Herdr](https://herdr.dev).

It brings the instant session-hopping experience of tools like `tmux` + `sesh` into Herdr. Press a single global shortcut from anywhere to search across active workspaces, recent project directories, and background AI coding agents, and jump straight into context.

---

## Story & Honest Disclaimer

I used `tmux` for years and became completely addicted to the fast session-switching workflow powered by [sesh](https://github.com/joshmedeski/sesh) (by Josh Medeski). When I moved my primary terminal setup to [Herdr](https://herdr.dev), I missed that exact feeling: a single hotkey to search across active workspaces, zoxide history, and project directories without thinking about window IDs or paths.

**An honest note on the code:** I am not a professional Go developer (I probably know about 5% of Go syntax). This entire project was created, designed, and iterated using AI assistance to solve real friction in my day-to-day work. 

Because it grew organically for personal use, there are definitely areas that can be improved:
- Code structure and idioms could be cleaner and more idiomatic Go.
- Edge-case stability and performance under extreme workloads (e.g. scanning tens of thousands of nested repositories).
- Visual design and layout polish across different terminal sizes and fonts.
- Multi-platform testing (it is primarily built and tested on macOS/Linux).

I'm sharing it in case someone else in the Herdr or terminal community finds it useful. If you know Go, spot an architectural flaw, or want to make it faster or more robust, **pull requests and feedback are genuinely appreciated!**

---

## Features

- ⚡ **Instant Zero-Delay Picker:** First frame renders immediately. Slower providers (like scanning filesystem directories) stream in asynchronously in the background. Zero disk I/O while typing.
- 🤖 **AI Agents Attention Queue:** The `agents` tab (in the default `all` ↔ `agents` cycle) puts newly unacknowledged blocked (`◉`) or finished (`●`) agents first, then the previous agent even if idle. Other working and idle agents follow available history; the current pane is last unless it needs new attention. Herdr records prior workspace focus, not prior pane focus: if the immediately preceding workspace has no agents or multiple agents, Shep does not promote a guessed prior pane, even if an older Shep selection exists. Selecting an agent focuses its Herdr tab, not an individual pane.
- 🔍 **Extended Fuzzy Filtering (fzf + Snacks style):** Space-separated AND terms, pipe `|` OR matching, exact `'terms`, prefix `^`, suffix `$`, negation `!term`, and field filters (`status:blocked`, `agent:claude`, `source:herdr`, `path:api`).
- 🔁 **True MRU A↔B Jump-Back:** Ships with a native Herdr plugin action (`tranceh2.shep.jump-back`) to toggle back and forth between your two most recently visited workspaces like `prefix + L` in tmux.
- 📐 **Adaptive Ranking & Persistent Pins:** Learns from successful selections using local SQLite WAL storage (private `0600` permissions with auto-quarantine on corruption). Press `Ctrl+F` to pin high-priority entries to the top.
- 🧩 **Hierarchical Groups & Multi-Pane Templates:** Define nested pickers (`type = "group"`), multi-tab/multi-split layouts with custom focus nodes, and `close_on_exit` flags.
- 🎨 **Modular Terminal UI:** Built on Charm's Bubble Tea and Lip Gloss with Catppuccin themes (Mocha, Macchiato, Frappe, Latte), auto-color detection, and a pure structural ASCII fallback when `NO_COLOR` is set.

---

## Requirements

- **Go 1.26+** (built and tested with Go 1.26.4).
- **[Herdr](https://herdr.dev)** (`herdr` executable on `$PATH`) — optional but strongly recommended. When Herdr is absent or stopped, `shep` prints the resolved project path to stdout so terminal scripts still work.
- **[zoxide](https://github.com/ajeetdsouza/zoxide)** — optional; enabled by default to surface your most frequent directories.
- **[fzf](https://github.com/junegunn/fzf)** — optional external selector fallback.
- **Nerd Font** — required for the built-in source icons to render correctly; without it, those glyphs appear as replacement boxes. Herdr itself already assumes Nerd Fonts. On terminals that cannot render Unicode, `[tui].icons = "ascii"` remains available for the picker's own semantic markers (status, tree, and search prompt); it does not affect per-source icons, which are raw configured strings.
- **[lsd](https://github.com/lsd-rs/lsd)** or **[eza](https://github.com/eza-community/eza)** — optional; used for colored, compact directory previews (names only, directories first; falls back to `ls -1Ap`).

---

## Installation

### Via `go install`

```sh
go install github.com/tranceh2/shep/cmd/shep@latest
```

### Pre-compiled Binaries

Download ready-to-run binaries for Linux and macOS (ARM64 and AMD64) from the [GitHub Releases](https://github.com/tranceh2/shep/releases) page.

### From Source

```sh
git clone https://github.com/tranceh2/shep.git
cd shep
make install   # builds and installs shep to $(go env GOPATH)/bin
```

Or build locally:

```sh
make build     # produces ./shep
```

### Nix / Flake

Install to your profile:

```sh
nix profile install github:tranceh2/shep
```

Or run directly without installing:

```sh
nix run github:tranceh2/shep -- open
```

### Publishing to PATH (`shep link`)

`shep link` is **optional**. Installing the Herdr plugin already provides
every `type = "plugin_action"` keybinding (`shep open`, `jump-back`,
`start-history`, `doctor`) with no linking step. Run `shep link` only when
you also want a bare `shep` command available for: a direct shell command
(`shep list --format tsv`), a script, the Television cable
([`cables/shep.toml`](cables/shep.toml)), or a native `type = "popup"`
keybind (as opposed to `type = "plugin_action"`).

To make `shep` available globally on your `$PATH`:
- If installed via Herdr plugin: run `./bin/shep link` inside the plugin directory to create a symlink at `~/.local/bin/shep`.
- From source or local build: run `./shep link` from your build directory.

To remove the symlink:

```sh
shep unlink
```

---

## Quickstart

```sh
shep open                  # Launch the interactive picker
shep open my-project       # Open or jump directly to matching candidate
shep open .                 # Open current directory as a Herdr workspace
shep list                  # Output discovered candidates as a plain table
shep list --format json    # Output candidates as JSON (for scripts/tooling)
shep doctor                 # Validate your configuration and workspace paths
```

---

## Recommended First-Run Setup

1. **Write a config.** Copy [`examples/config.toml`](examples/config.toml) to
   `~/.config/shep/config.toml` and edit the example paths, or run
   `shep init` to generate the exhaustively-commented canonical default.
2. **Install the Herdr plugin.** See [Herdr Integration](#herdr-integration)
   below. This alone gives you every `plugin_action` keybinding — `shep link`
   is a separate, optional step (see the callout in that section).
3. **Add keybindings** to `~/.config/herdr/config.toml` and
   `herdr server reload-config`.
4. **Verify** with the plugin's doctor action, which works with the plugin
   alone:
   ```sh
   herdr plugin action invoke doctor --plugin tranceh2.shep
   ```
   If you separately installed or linked the `shep` CLI (see
   [Publishing to PATH](#publishing-to-path-shep-link)), `shep doctor` is an
   equivalent check. Either way, finish with `herdr plugin list` to confirm
   `tranceh2.shep` is enabled.

Checklist:

- [ ] `~/.config/shep/config.toml` exists and the plugin doctor action
      (`herdr plugin action invoke doctor --plugin tranceh2.shep`) reports no
      errors.
- [ ] `herdr plugin list` shows `tranceh2.shep` enabled.
- [ ] Your keybindings reload cleanly (`herdr server reload-config`).
- [ ] `prefix+ctrl+f` (or your chosen key) opens the picker popup.
- [ ] `prefix+tab` toggles between two focused workspaces (see
      [Troubleshooting](#troubleshooting-prefixtab-does-nothing) if it does
      not).

---

## Herdr Integration

Shep integrates with Herdr through the unified plugin `tranceh2.shep` (which provides the interactive picker popup, focus history collector, and jump-back navigation).

### 1. Install the Herdr Plugin

**Via Herdr plugin install (recommended):**

```sh
herdr plugin install Tranceh2/shep/contrib/herdr-plugin
```

To install a non-default branch or ref, pass it separately with `--ref`:

```sh
herdr plugin install --ref <branch> Tranceh2/shep/contrib/herdr-plugin
```

*Note: Requires Go 1.26+ installed. Herdr clones the repository and runs `bash scripts/build.sh`, which compiles the checkout into the plugin-local `bin/shep` with version and commit metadata.*

**Or from a local checkout:**

```sh
# From your shep repository clone
herdr plugin link "$PWD/contrib/herdr-plugin"
cd contrib/herdr-plugin
bash scripts/build.sh
```

Installing the plugin (step 1) is all `type = "plugin_action"` keybindings
need. **`shep link` is NOT required** for the keybindings below — it is a
separate, optional step described in
[Publishing to PATH](#publishing-to-path-shep-link), only needed for a bare
`shep` shell command, a Television cable, or a native `type = "popup"`
keybind.

### 2. Configure Keybindings

Add the keybindings to `~/.config/herdr/config.toml`:

```toml
# Open Shep picker popup
[[keys.command]]
key = "prefix+ctrl+f"  # Replace with your preferred shortcut
type = "plugin_action"
command = "tranceh2.shep.open"
description = "open Shep picker"

# Jump back to previous workspace (A<->B toggle)
[[keys.command]]
key = "prefix+tab"
type = "plugin_action"
command = "tranceh2.shep.jump-back"
description = "jump to previous workspace"
```

Reload Herdr's configuration:

```sh
herdr server reload-config
```

You can also invoke any action manually from the CLI for testing. The
command takes the bare action ID (not the fully-qualified
`tranceh2.shep.<id>` form used in `command =` above) plus
`--plugin tranceh2.shep`:

```sh
herdr plugin action invoke open --plugin tranceh2.shep
herdr plugin action invoke jump-back --plugin tranceh2.shep
```

When running inside a Herdr popup, `shep` detects the active pane and unlocks in-place actions:
- `Ctrl+T`: open the selected candidate as a new **tab** in the current workspace.
- `Ctrl+P`: open the selected candidate as a **split pane** beside your current pane.

See [`docs/herdr-plugin.md`](docs/herdr-plugin.md) for the full plugin reference and [`docs/jump-back.md`](docs/jump-back.md) for jump-back error codes and lifecycle rules.

### Troubleshooting: `prefix+tab` does nothing

If your `prefix+tab` keybinding is already correctly configured (`command =
"tranceh2.shep.jump-back"`) but pressing it does nothing or the picker
reports no previous workspace, the usual cause is that the background
history collector (`watch-history`) is not running — `jump-back` needs
focus events from two distinct workspaces before it has anything to toggle
between.

1. Confirm the keybinding uses the fully-qualified plugin action command:
   `command = "tranceh2.shep.jump-back"`.
2. Reload Herdr's configuration: `herdr server reload-config`.
3. Start (or recover) the collector for the current session:
   ```sh
   herdr plugin action invoke start-history --plugin tranceh2.shep
   ```
4. Focus two distinct Herdr workspaces (switch to workspace A, then
   workspace B) so the collector observes two focus events.
5. Retry `prefix+tab`, or invoke it directly to confirm:
   ```sh
   herdr plugin action invoke jump-back --plugin tranceh2.shep
   ```

From subsequent Herdr starts onward, the plugin's `[[startup]]` hook starts
the collector automatically once the session is restored and the API socket
is ready — step 3 is normally only needed after installing/upgrading the
plugin or recovering from a crashed collector. See
[`docs/jump-back.md`](docs/jump-back.md#recovery-refusal-and-shutdown) for
the full recovery and refusal taxonomy.

---

## Reading the Picker

The picker is one grid with a single frame (Herdr's popup border when it runs as a plugin): views on top, the search prompt and the list on the left, the preview on the right, and the shortcuts that apply to the selected row at the bottom.

```text
 all   agents   kube-contexts
 ❯ api█                               12/651 │ api-gateway                    project
 ────────────────────────────────────────────┼──────────────────────────────────────────
 ❯ 󰳆  payments                             ⠋ │ ~/work/api-gateway
     api-gateway  ~/work                     │ on main · 2 changes
     Kubernetes                            › │
                                             │ Files ───────────────────────────────────
 enter open · tab agents · ctrl+f pin · ? help · esc clear
```

- **Views** (`tab` / `shift+tab`) stay visible at every width; the active one is highlighted.
- **Prompt**: what you typed, the cursor, and `matches/total` (just the total when nothing is typed). `?` opens the shortcut and search-syntax cheat sheet.
- **Rows** show the name first and the parent folder dimmed, so the part you scan for survives narrow widths; the parent shrinks before the name does. Paths under your home directory are shown with `~`.
- **Icons are colored by source**: open Herdr workspaces green, configured workspaces lavender, zoxide blue, projects and worktrees peach, sessions teal, agents mauve, custom sources sky.
- **Right-hand markers**: the most urgent agent state of an open workspace (`⠋` working, `◉` blocked, `●` done, a dim `✓` when idle), `★` pinned, `›` a group that opens its own picker, `current` for where you are now, and the branch of a worktree.
- **Preview**: the title row names the selection and its kind; below come its `~` path, a one-line summary (agent state with tab and pane counts, or the git branch with its changes), then sections such as Tabs, Files, your custom commands and, last, the newest lines of the active pane.
- The divider between list and preview doubles as the list's scroll bar.

---

## Keybindings Reference

| Key | Context | Action |
|---|---|---|
| `Up` / `Down`, `Ctrl+K` / `Ctrl+J` | List | Move selection cursor up / down one row |
| `Ctrl+U` / `Ctrl+D` | List | Move selection cursor up / down half the visible list rows (at least one) |
| `PageUp` / `PageDown` | List | Scroll the preview viewport up / down one page without moving the list cursor |
| `Left` / `Right` | List | Collapse / expand a workspace's tabs and panes |
| `Tab` / `Shift+Tab` | Global (except help) | Cycle configured top tabs in order (defaults to `all` ↔ `agents`) |
| `Enter` | List | Open the highlighted row |
| `Ctrl+T` | Inside Herdr | Open selected entry as a new tab in current workspace |
| `Ctrl+P` | Inside Herdr | Open selected entry as a split pane in current workspace |
| `Ctrl+F` | List | Toggle persistent pin status on the selected top-level candidate, when pinning is configured |
| `Ctrl+X` | List | Close the highlighted open Herdr pane, tab, or workspace (with confirmation if configured) |
| `Ctrl+L` | List | Cycle layout override between auto and landscape |
| `Backspace` | List | Delete the last query character |
| `Ctrl+W` / `Alt+Backspace` | List | Delete the last query word |
| `?` | List | Open the in-app help overlay (`?` / `Esc` closes it) |
| `Esc` | List | Clear search query; quit if query is already empty |
| `Ctrl+C` / `Ctrl+G` | Global | Cancel and exit |

---

## Search Syntax & Filter Cheatsheet

Shep includes an extended fuzzy search engine inspired by `fzf` and modern editor pickers:

| Syntax | Example | Description |
|---|---|---|
| Space | `api auth` | **AND**: item must match both "api" and "auth" |
| Pipe `\|` | `frontend \| web` | **OR**: item matches either "frontend" or "web" |
| Single quote `'` | `'server` | **Exact substring**: matches literal "server" |
| Caret `^` | `^core` | **Prefix match**: item label or path starts with "core" |
| Dollar `$` | `service$` | **Suffix match**: item ends with "service" |
| Exclamation `!` | `!test` | **Negation**: excludes items matching "test" |
| `status:` | `status:blocked` | Filter agents by status: `blocked`, `working`, `done`, `idle` |
| `agent:` | `agent:claude` | Filter agents by name: `claude`, `opencode`, `hermes`, etc. |
| `source:` | `source:herdr` | Filter by source: `herdr`, `workspaces`, `zoxide`, `projects` |
| `path:` | `path:backend` | Filter candidates whose filesystem path contains "backend" |

---

## Configuration Guide (`config.toml`)

Shep searches for its configuration in:
1. `$XDG_CONFIG_HOME/shep/config.toml`
2. `~/.config/shep/config.toml`

Generate a starter configuration file with defaults:

```sh
shep init          # writes config.toml (safe, does not overwrite)
shep init --force  # overwrites existing config
```

Or copy a shorter, ready-to-edit working example from
[`examples/config.toml`](examples/config.toml) — it is not what `shep init`
writes (that is the exhaustively-commented canonical reference below), but a
practical starting point with real `[[workspaces]]`, `[templates.<name>]`,
and `[[wildcards]]` entries you can adapt directly.

Below is an exhaustive breakdown of every configuration section and parameter.

---

### `[general]` — Global Settings

```toml
[general]
# Order in which sources appear in the picker.
# Available built-ins: "herdr", "workspaces", "zoxide", "projects", "sessions"
source_order = ["herdr", "workspaces", "zoxide", "projects"]

# Interactive selector backend:
# - "builtin": Charm Bubble Tea TUI (recommended, default)
# - "fzf": Pipes candidates through external fzf CLI
# - "auto": Uses fzf if available on PATH, otherwise falls back to builtin
selector = "builtin"

# Template for dynamic workspace names created when opening a project from zoxide or projects.
# Uses the shared template data and functions (see "Templates" below), e.g. base, lower, tilde.
# Unset: worktrees are named "<repo>@<branch>", everything else by its full path.
workspace_name = '{{ .Path | base | lower }}'
```

---

### `[ranking]` — Adaptive Frecency Learning

```toml
[ranking]
# When true, shep learns from successful opens and elevates frequently/recently used
# candidates. State is stored in private SQLite WAL at ~/.local/state/shep/ranking.sqlite3.
enabled = true
```

*Note: You can clear ranking history at any time with `shep ranking clear`.*

---

### `[defaults]` — Fallback Workspace Metadata

```toml
[defaults]
# Default workspace type applied when creating a workspace without explicit type (usually "shell")
type = "shell"

# Default template name applied from [templates.<name>] when no workspace-specific or
# wildcard template matches.
template = "default"
```

---

### `[tui]` — Terminal User Interface Appearance

```toml
[tui]
# Ordered top tabs; omitted or [] defaults to ["all", "agents"].
# Built-in source tabs: herdr, workspaces, zoxide, projects, sessions.
# Custom source tabs use their declared name; group tabs use [[workspaces]].id.
tabs = ["all", "agents"]
# Layout orientation:
# - "landscape": Forces side-by-side split (list on left, preview on right).
# - omit or "": Responsive auto (side-by-side on wide terminals, list-only on narrow).
layout = "landscape"

# Width split ratios: either "auto" or percentage string like "60%"
list_width = "auto"
preview_width = "60%"

# Theme: "mocha", "macchiato", "frappe", "latte", "plain" (no colors), or "inherit" (from Herdr)
theme = "mocha"

# Icon glyph tier:
# - "unicode": Modern Unicode icons (recommended)
# - "ascii": 7-bit plain ASCII fallback (for basic terminals or remote SSH)
icons = "unicode"

# Optional per-kind confirmation for ctrl+x; absent or [] closes immediately.
# Allowed kinds: workspace, tab, pane (any subset, no duplicates).
confirm_close = ["workspace", "tab"]

```

---

In the picker, `ctrl+x` closes the selected **open Herdr** item: an agent or
pane closes its pane, a tab closes its tab, and a Herdr workspace closes its
workspace. Projects, sessions, custom sources, and other non-open entries are
not closed. With `confirm_close`, the listed kinds ask in the footer; `y`
confirms and any other key, including Esc, cancels. Workspace group-close
errors are shown rather than closing linked workspaces automatically.

Tabs can be reordered or reduced to one entry. `all` runs only providers in
`[general].source_order`; a source or custom source tab listed only in `[tui].tabs`
loads that provider without adding its rows to `all`. Group tabs evaluate their
root and `source_order` only when selected, sharing the Herdr snapshot where
available. Duplicate, unknown, ambiguous and non-group tab references fail
configuration validation. `shep open --view <id> [query]` always opens the built-in
picker, even for one candidate or when fzf is configured. Valid ids are `all`,
`agents`, a built-in source, a declared custom source, or a group workspace id.
The optional query filters inside that view. `--view` conflicts with `--path`
and the `.` query. If the view is absent from `[tui].tabs`, it is appended as a
temporary active tab so Tab can reach it again; it does not enable its source
inside `all`. For example, `shep open --view agents` works with hidden agents,
and `shep open --view team-projects rust` searches only that group.

For example, add the following entries to the same config to expose a
custom source and a group shortcut:

```toml
[tui]
tabs = ["all", "pull-requests", "team-projects", "agents"]

[[sources.custom]]
name = "pull-requests"
command = ["/path/to/list-prs-for-shep"] # emits Shep JSON rows

[[workspaces]]
id = "team-projects" # unique, stable tab reference (not name or path)
name = "Team projects"
type = "group"
path = "~/projects/team"
source_order = ["projects", "zoxide"]
```

### Templates — Shared Data and Functions

Every user-written template (`workspace_name`, every `label_format`, preview command arguments) is a [Go template](https://pkg.go.dev/text/template) over the same data and the same functions. Templates are checked when the config loads, against representative rows of every kind the field applies to, so a typo such as `{{ .Nope }}` fails with the field's path instead of at runtime.

Data: `.Path`, `.NormalizedPath`, `.Label`, `.Source` (herdr, workspaces, zoxide, projects, sessions, agents, path or a custom source name), `.Kind` (workspace, configured, group, folder, project, worktree, session, agent, tab, pane, custom), `.Icon`, `.Branch`, `.Head`, `.RepoName`, `.IsWorktree`, `.IsMainWorktree`, `.Agent`, `.AgentStatus`, `.TabNumber`, `.TabLabel`, `.Workspace` (the Herdr workspace label of a tab, pane or agent row) and `.Meta.<key>` for every provider key. Fields that do not apply to a row and missing `.Meta` keys render empty.

Template functions:

- Paths: `base`, `dir`, `clean`, `ext`, `isAbs` (slash-separated paths).
- Strings: `trim`, `trimPrefix`, `trimSuffix`, `trimAll`, `lower`, `upper`, `title`, `replace`, `contains`, `hasPrefix`, `hasSuffix`, `nospace`, `snakecase`, `camelcase`, `kebabcase`.
- Values and lists: `default`, `coalesce`, `ternary`, `splitList`, `join`, `mustSlice`, `compact`, `first`, `last`.
- Numbers: `add`, `sub`, `max`, `min`, `int`.
- Patterns and hashing: `mustRegexMatch`, `mustRegexReplaceAllLiteral`, `regexQuoteMeta`, `sha256sum`.
- Shep additions: `tilde` (your home directory becomes `~`), `name` (last element of a `~/…` or `/…` path, any other text unchanged), `parent` (the parent of a `~/…` or `/…` path, `""` otherwise; `~/a/b` → `~/a`), `trimIcon` (drops leading icons, emoji and spaces: `" ~/ops/x"` → `"~/ops/x"`).

These are Sprig's deterministic functions with one name each: nothing reads the environment, the clock or the network, and the `osBase`/`osDir`/`osClean`/`osExt`/`osIsAbs` aliases are not available (use `base`, `dir`, `clean`, `ext`, `isAbs`).

### `[sources.<name>]` — Source Provider Presentation

Customize icons and label formats per source with the shared template data above. Agents rows carry `.Agent`, `.AgentStatus`, `.TabLabel` and `.Workspace`, plus `workspace_id`, `tab_id`, `pane_id`, `terminal_title` and `kind` through `.Meta`. Herdr itself shortens `terminal_title`; Shep cannot display more than Herdr provides. The default agents label is title-only (`{{.Label}}`); the status marker remains separate from the configured source icon.

```toml
[sources.herdr]
icon = " "
label_format = "{{if .Label}}{{.Label}} · {{end}}{{.Path}}"
tab_label_format = "tab {{.TabNumber}} · {{.Label}}"
pane_label_format = "{{.Label}}"

[sources.sessions]
# Opt-in source for local Herdr sessions (must be added to [general].source_order to activate)
icon = " "
label_format = "{{.Label}}"

[sources.agents]
icon = " "
label_format = "{{.Label}}"
# preview = ["identity", "git"] # unset uses [preview].default; [] shows only identity

[sources.workspaces]
icon = " "
label_format = "{{.Label}}"

[sources.zoxide]
icon = " "
limit = 100
label_format = "{{.Path}}"

[sources.projects]
icon = " "
# Root directories to scan for project folders
roots = ["~/projects", "~/work"]
# Marker files or directories that identify a folder as a project root
markers = [".git", "Cargo.toml", "go.mod", "package.json", "flake.nix"]
# Maximum folder depth to traverse looking for markers
max_depth = 3
# Directory names to completely skip while scanning
ignore = [".cache", "node_modules", "vendor", "dist", "target"]
```

---

### `[preview]` — Preview Pane Configuration

```toml
[preview]
# Timeout for preview generation before showing a loading indicator
timeout = "150ms"
# TTL for caching preview contents in memory
cache_ttl = "5s"
# Maximum number of lines rendered in the preview box
max_lines = 50

# Default sections to render (in order).
# Built-in sections: "identity", "git", "workspace", "active_pane", "agent_status", "dir"
#
# Out of the box each source previews what describes its own rows, so you do not
# have to configure anything: herdr workspaces show tabs, the active pane and
# agent status; `workspaces` and zoxide directories show identity and a directory
# listing; projects adds git. Sessions always show session info.
#
# Setting `default` here replaces those built-in per-source lists for every
# source, so it stays a single obvious control rather than being silently
# outranked. To change just one source, set `[sources.<name>].preview`, which
# wins over both.
default = ["identity", "git"]

# Custom global preview commands (tokenized safely, no raw shell execution)
[preview.commands.recent_commits]
command = "git -C {{.Path}} log -n 3 --oneline"
```

---

### `[[workspaces]]` — Statically Configured Workspaces

Define individual projects or nested group pickers.

```toml
# 1. Single Project Workspace
[[workspaces]]
name = "api-service"
path = "~/projects/api"
template = "backend"
preview = ["identity", "git"]
aliases = ["backend", "go", "service"]

# 2. Command Workspace (runs a command in the root pane and closes on exit)
[[workspaces]]
name = "k9s"
path = "~/infrastructure"
command = "k9s"
close_on_exit = true

# 3. Group Workspace (Acts as a nested sub-picker when selected!)
[[workspaces]]
name = "infrastructure"
type = "group"
path = "~/infrastructure"
source_order = ["projects", "zoxide"]

  # Override project scan settings specifically for this group
  [workspaces.sources.projects]
  max_depth = 5
  markers = ["Terraform", "Terragrunt", "Pulumi.yaml"]
```

---

### `[templates.<name>]` — Workspace Layout Templates

Templates define how newly created workspaces are structured into tabs, panes, and commands.

```toml
[templates.backend]
description = "Development environment for backend services"

# Choose which tab and pane receives keyboard focus after creation
focus = { tab = "code", node = "editor" }

[[templates.backend.tabs]]
name = "code"
root = "main_split"

  [[templates.backend.tabs.nodes]]
  id = "main_split"
  split = "rows"              # "rows" stacks top/bottom, "cols" places side-by-side
  children = ["editor", "shell"]
  sizes = [75, 25]           # Percentages

  [[templates.backend.tabs.nodes]]
  id = "editor"
  command = "nvim ."

  [[templates.backend.tabs.nodes]]
  id = "shell"
  command = ""               # Empty string opens your default shell

[[templates.backend.tabs]]
name = "ai"
root = "agent_pane"

  [[templates.backend.tabs.nodes]]
  id = "agent_pane"
  command = "opencode"
  close_on_exit = true       # Closes the pane when the process exits
```

---

### `[[wildcards]]` — Dynamic Path Rules

Apply templates, custom naming rules, previews and row presentation keys (`icon`, `icon_color`, `label_format`, `detail_format`, `marker_format`) based on path patterns. Each setting comes from the first matching pattern that sets it; a pattern that does not set it never stops the scan. A `[[workspaces]]` entry's own keys outrank wildcards for its own row (its `preview` also for the other rows in its directory), and `[sources.<name>]` comes after both.

```toml
[[wildcards]]
pattern = "**/microservices/*"
template = "backend"
workspace_name = 'svc-{{ .Path | base }}'
preview = ["identity", "git"]
```

---

### `[[sources.custom]]` — Custom Command Providers

Declare a command-backed source that writes a JSON array of Shep rows (not a tool's native JSON). Use an argv-only helper to transform external output; Shep does not invoke a shell or interpolate the list command.

```toml
[general]
source_order = ["herdr", "workspaces", "pull-requests"]

[[sources.custom]]
name = "pull-requests"
command = ["/path/to/list-prs-for-shep"]
icon = " "
aliases = ["pr", "review"]
label_format = "PR {{.Label}}"
preview = ["identity"]
timeout = "3s"
```

The helper must print rows such as `[{"id":"42","label":"#42 fix bug","command":"gh pr checkout 42","aliases":["bug"]}]`. A row needs a label; it may also supply an ID, path, command, icon, aliases, template, close_on_exit, or inert string metadata. Reserved metadata keys cannot override identity or launch behavior. Use a group's `source_order` instead of `[general].source_order` to run the provider only when that group opens. Private `[sources.custom.preview_commands.<name>]` entries accept argv lists and inherit the global preview time and line limits unless overridden. The generated config example documents the full row and preview contract.

---

## Current Limitations & Roadmap

Because this tool was built to solve my personal workflow, there are known technical boundaries:

1. **Offline Status Cycles:** If an AI coding agent goes from `blocked` → `working` → `blocked` completely while `shep` is closed, the second blocked state is not detected as "new" because Herdr's current wire events do not expose a persistent transition generation. Real-time transitions observed while `shep` is open are cleared and re-prioritized immediately.
2. **Terminal Height Under Extreme Sizes:** The TUI requires at least 12 rows of terminal height to render side-by-side previews comfortably. On very small terminals (<80x12), it automatically switches to a compact list-only view.
3. **Large Monorepos:** Scanning project roots with `max_depth` greater than 5 across network mounts or huge monorepos can introduce noticeable delay on the first scan. We recommend setting tighter `markers` and `roots`.

---

## Contributing

Pull requests, discussions, and issue reports are very welcome!

- Please use [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`).
- Verify the test suite and linters before opening a PR:
  ```sh
  make test    # runs go test -race ./... across all packages
  make lint    # runs golangci-lint
  ./scripts/check-no-user-paths.sh  # verifies no hardcoded machine paths
  ```

---

## License

This project is licensed under the [MIT License](LICENSE).
