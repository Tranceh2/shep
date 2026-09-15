# shep

`shep` is a Herdr-first project launcher. It enumerates project candidates from
Herdr workspaces, predefined `[[workspaces]]` entries, zoxide, and marker-based
project discovery, then opens the selected one with Herdr — focusing an
existing workspace when one already matches the path, or creating a fresh
focused workspace (and applying its template) otherwise. When Herdr is not
installed or unreachable, `shep` prints the resolved absolute path and exits 0
so the caller still gets to the project.

shep ships **path-agnostic defaults**: no hardcoded `~/code`, `/Users/`, or
project roots. A pristine machine (Herdr installed, optional zoxide) works out
of the box.

## Requirements

- Go 1.26+ (built and tested on Go 1.26.4)
- [Herdr](https://herdr.dev) (`herdr` on PATH) — optional but recommended;
  `shep` degrades to printing paths when it is absent.
- [zoxide](https://github.com/ajeetdsouza/zoxide) — optional source.
- [fzf](https://github.com/junegunn/fzf) — optional accelerator for
  `shep open`; the embedded Bubble Tea TUI is the universal fallback.
- [lsd](https://github.com/lsd-rs/lsd) or [eza](https://github.com/eza-community/eza)
  — optional, used by the built-in `dir` preview (falls back to `ls -la`).

## Install

```sh
git clone https://github.com/tranceh2/shep.git
cd shep
make install   # go install the shep binary, pinned to a tagged version via -ldflags
```

Or build directly:

```sh
go install ./cmd/shep
```

`make build` produces a local `./shep` binary.

## Configure

shep reads `$XDG_CONFIG_HOME/shep/config.toml`, or `~/.config/shep/config.toml`
when `XDG_CONFIG_HOME` is unset (this XDG order is used on every platform,
including macOS). A missing config is fine: shep falls back to built-in defaults
(all default built-in sources enabled, with the opt-in `sessions` source
excluded unless explicitly added to `source_order`; no predefined workspaces or
templates).

Generate a commented example config:

```sh
shep init          # write config.toml (errors if it already exists)
shep init --force  # overwrite an existing config
```

### Sources

`[general].source_order` lists the enabled sources and their merge/display
order. Five built-in names are always available: `herdr`, `sessions`, `workspaces`,
`zoxide`, and `projects`. `sessions` is opt-in: add it to `source_order` when
Herdr session rows should be listed. A name declared in `[[integrations]]` (see
below) extends the set for that config document. Any
other name fails Load fast:

```toml
[general]
source_order = ["herdr", "workspaces", "zoxide", "projects"]
selector = "builtin"   # builtin | fzf | auto
# Applies only to newly created dynamic workspaces.
workspace_name = '{{ .Path | osBase | lower }}'
```

- **herdr** — active Herdr workspaces.
- **sessions** — opt-in local Herdr sessions; add `sessions` to
  `[general].source_order` to enable this source.
- **workspaces** — predefined `[[workspaces]]` entries (single projects or
  `type = "group"` nested pickers).
- **zoxide** — your zoxide history.
- **projects** — directories detected because they contain any configured
  marker (a file or directory name — not git-only). Top-level discovery scans
  the global roots in `[sources.projects].roots`; a `type = "group"` workspace
  scans beneath that group's own path instead.
- **a declared integration** — an external command's JSON rows (see
  "Command/JSON integrations" below).

For an empty query, source blocks remain contiguous in `source_order`. Each
source keeps its local policy: Herdr orders open workspaces by live focus MRU
(using the `watch-history` collector when active, gracefully falling back to
launch history), putting previously focused workspaces first and the currently
focused workspace last; `workspaces` preserves configured order;
zoxide preserves provider order; and projects apply adaptive ordering only
within the projects block. A group's explicit `source_order` replaces the global order for that nested
picker. When it is omitted, the nested registry uses the global order while
still honoring the group's scoped source membership. Declared integrations
listed only by a group remain lazy. Its `[workspaces.sources.projects]` table is merged
field by field with `[sources.projects]`: omitted fields inherit the global
value, while specified fields apply only to that group. Group-local projects
always use the group's path rather than global project roots. A declared
integration may be excluded from `[general].source_order` and included only in
a group's `source_order`; in that case its command is loaded lazily when that
group opens, not during the top-level picker.

### Command/JSON integrations

`[[integrations]]` declares a trusted local producer that emits picker rows as a
JSON array on stdout. `aliases` on the integration apply to every emitted row;
row-level `aliases` are then combined with them. Shep starts the configured
provider executable with safe argv execution via `exec.CommandContext` — never
`sh -c` and never shell interpolation. The provider's JSON is a separate
trusted action contract: its emitted `command` fields are trusted local code
configured by the operator and execute with the user's privileges through the
existing Herdr template shell path. Do not consume untrusted provider output as
an integration configuration source:



```toml
[[integrations]]
name    = "prs"
command = ["gh", "pr", "list", "--json", "number,title,headRefName"]
aliases = ["pull request", "review"]
icon    = " "
timeout = "3s"

[general]
source_order = ["herdr", "workspaces", "prs", "zoxide", "projects"]
```

`gh`'s own JSON columns do not match the row schema below, so a real `gh pr
list` integration is usually a small wrapper script that reshapes each PR into
one row — `command` can point at that script instead of the raw CLI.

Each row is a JSON object:

| field           | type                | required | meaning                                                                 |
| --------------- | ------------------- | -------- | ------------------------------------------------------------------------ |
| `id`            | string               | no       | stable row identity for pathless rows; prefer this over the deterministic fallback |
| `label`         | string               | yes      | display text                                                              |
| `path`          | string               | no       | required for `--target=workspace`; opens as an ordinary workspace through the normal `shep open` pipeline |
| `command`       | string               | no       | runs in the root pane of a freshly created workspace, or via `--target=tab`/`--target=pane` inside the current one |
| `icon`          | string               | no       | overrides `[[integrations]].icon` for this row                           |
| `template`      | string               | no       | names a `[templates.<name>]` applied instead of `command`                |
| `close_on_exit` | bool                 | no       | wraps `command` the same way `[[workspaces]]`'s own `close_on_exit` does |
| `aliases`       | array of strings     | no       | alternate search terms; shared integration aliases combine with row aliases             |
| `meta`          | map[string]string    | no       | inert string metadata for custom previews and non-reserved consumers; reserved internal keys are rejected |

Aliases affect search only. They are not rendered in the primary row, do not group or filter candidates, and are normalized by trimming whitespace and removing case-insensitive duplicates while preserving the first declaration. Empty aliases are ignored.

Integration row `meta` is inert and cannot change launch, grouping, identity,
or control behavior. The reserved keys `command`, `template`, `close_on_exit`,
`group`, `group_sources`, `group_template`, `parent_template`, `workspace_id`,
`tab_id`, `pane_id`, `integration`, `integration_id`, `active_tab_id`,
`agent_status`, `branch`, `default`, `entry_id`, `head`, `is_worktree`,
`main_worktree`, `repo`, `running`, `session_dir`, `session_name`, `socket_path`,
`tab_label`, `tab_number`, `tab_panes`, `workspace_label`, and `workspace_tabs`
are rejected with a row/key error. Use the typed row fields for `command`,
`template`, and `close_on_exit`; `integration` is set internally.

A row with `path` and no `command`/`template` behaves like a zoxide/projects
row. In the built-in picker, `Ctrl+F` toggles a top-level row's pin; pins are
stored in Shep's existing private SQLite ranking state, not in config.toml.
External selectors such as fzf receive pinned-first ordering but cannot toggle
pins. A pathless integration row uses `id` as its stable identity when present;
otherwise Shep hashes the integration name, label, and command deterministically.
A row with `command` (and no `path`) remains valid for `--target=tab`/`--target=pane`,
because those targets use the current pane's cwd. It cannot be opened as a
standalone `--target=workspace` row: Herdr workspace creation requires a cwd,
and shep fails fast rather than inventing one. A row may set both `path` and
`command`. There is no parallel launch code: an integration row is an ordinary
candidate that reuses the exact Meta-driven template/target resolution
`[[workspaces]]` candidates already go through.

A failing, timing-out (bounded by `timeout`, default `3s`), or malformed
(non-JSON-array output, a row without `label`) integration command never
blanks the picker or silently degrades to an empty list: its error surfaces
through the same partial-failure path a failing Herdr/zoxide/projects source
already uses, so the rest of the picker keeps working while the failure stays
visible.

`name` must be unique and must not collide with a built-in source name. To run
an integration globally, include it in `[general].source_order`. To keep it
lazy, exclude it there and include it only in a group's `source_order`; it then
runs only after entering that group.

### Adaptive ranking

Ranking is enabled by default and learns only from successful launches. It stores
opaque action/resource identities, counts, and timestamps under
`$XDG_STATE_HOME/shep/ranking.sqlite3` (or `~/.local/state/shep/ranking.sqlite3`)
with private permissions. Labels, queries, templates, environment data,
telemetry, and network activity are never stored or emitted. History is retained
for 180 days and bounded to 10,000 usage keys and 32 recent selections. For a
non-empty query, ranking is label-first: exact label, word/prefix label match,
fuzzy label, alias match, then path/allowed metadata. Alias matches rank exact over
word-prefix over fuzzy within the alias layer. Within the same or near-tie textual quality,
focusable Herdr workspace/tab/pane actions precede attach/open/create actions;
history and stable input order are applied afterward. The same precomputed order
feeds the TUI and fzf, with fzf using input order only as its tie-breaker.

Disable it without touching ranking state:

```toml
[ranking]
enabled = false
```

Clear history at any time, including while disabled:

```sh
shep ranking clear
```

`shep open .` (or `shep open --path .`) always opens the current directory
directly; cwd is never a picker source in `shep list`/`shep open`'s
candidate set.

### Workspaces, groups and templates

```toml
[[workspaces]]
name = "main-app"
path = "~/projects/main-app"
template = "dev"

[[workspaces]]
name = "projects"
type = "group"
path = "~/projects"
source_order = ["projects", "zoxide"]
template = "dev"

# A declared integration can be scoped to a group instead of the global picker.
# Keep "kube-contexts" out of [general].source_order to load it only after this
# group opens; its rows still use the normal fuzzy search, ranking, pins,
# previews, and workspace/tab/pane launch behavior.
[[integrations]]
name = "kube-contexts"
command = ["/path/to/shep/integrations/kube-contexts"]
icon = "K"
timeout = "2s"

[[workspaces]]
name = "Kubernetes"
type = "group"
path = "~/projects/kubernetes"
source_order = ["kube-contexts"]

# Optional group-local project settings merge field by field with the global
# [sources.projects] table.
[workspaces.sources.projects]
max_depth = 5

[[wildcards]]
pattern = "**/services/*"
workspace_name = '✈️ {{ printf "%s/%s" (.Path | osDir | osBase) (.Path | osBase) }}'
```

`workspace_name` is evaluated only when a new dynamic zoxide, project, or direct-path
workspace is created. Precedence is explicit `[[workspaces]].name` (which bypasses
templates), first matching wildcard, `[general].workspace_name`, then the full
normalized path. Existing Herdr workspaces, sessions, tabs, panes, and current-
workspace launches bypass it. Candidate labels, paths, normalized identity,
previews, list output, and TUI rows are unchanged.

The naming engine uses a deterministic allow-list of Sprig functions. Core helpers
include `osBase`, `osDir`, `osClean`, `trim`, `lower`, `upper`, `title`, `replace`,
`default`, and path predicates. Advanced helpers include list, numeric, regex, and
`sha256sum` functions. Environment, time, DNS, random, crypto/cert, mutation,
reflection, serialization, URL/semver, and Helm-only helpers are unavailable.
Names preserve Unicode, spaces, and slashes; blank/control-character results fail
before Herdr creation. Duplicate rendered labels are allowed and are never
suffix-adjusted. `os*` helpers use host-native paths; slash helpers are for
slash-normalized values. No filesystem, symlink, tilde, clock, environment, or
network state is read while rendering.

A `type = "group"` entry is a nested picker: selecting it re-scopes the
picker to its own `sources` list rooted at its own `path`, instead of opening
the group entry itself.

Renaming a `[[workspaces]]` entry's `name` does **not** re-focus the Herdr
workspace created under the old name — `shep open` treats it as a new
identity and creates a fresh workspace, leaving the old one open. To rename
without losing the existing workspace: `shep close` the old Herdr workspace
(or rename it directly in Herdr), then edit `name` and `shep open` the entry
again.

`[templates.<name>]` describes what opens after Enter for a **freshly
created** workspace only (focusing an existing one never re-applies a
template): a plain `command` in the root pane, or a structured `tabs`/`nodes`
layout (tabs, panes, split direction, sizes). The first declared tab always
reuses/renames the workspace's own root tab; later tabs are created fresh.

```toml
[templates.dev]
description = "development workspace"
focus = { tab = "AI", node = "opencode" }

[[templates.dev.tabs]]
name = "code"
root = "main"

  [[templates.dev.tabs.nodes]]
  id = "main"
  split = "rows"          # rows stacks top/bottom, cols places side by side
  children = ["editor", "terminal"]
  sizes = [80, 20]

  [[templates.dev.tabs.nodes]]
  id = "editor"
  command = "nvim ."

  [[templates.dev.tabs.nodes]]
  id = "terminal"
  command = ""

[[templates.dev.tabs]]
name = "AI"
root = "opencode"

  [[templates.dev.tabs.nodes]]
  id = "opencode"
  command = "opencode"
  close_on_exit = true
```

`focus` is declared once at the template level, never per tab/node. `focus.tab`
names a `[[templates.<name>.tabs]].name`; `focus.node` (optional) names a node
id scoped to that same tab and targets that node's pane specifically. Both are
validated at config load: an unknown `focus.tab`, or a `focus.node` that
doesn't exist within that tab, fails fast with a clear error. `focus` may be
omitted entirely — the default is the first tab (which reuses the workspace's
already-focused root tab), so a template with no `focus` keeps today's normal
first-tab behavior instead of the last-created tab/pane stealing focus. Focus
is applied entirely via Herdr's `--focus`/`--no-focus` flags at tab/pane
creation time; there is no post-hoc "focus by id" command for panes.

A leaf node's `close_on_exit = true` closes its pane after the node's
command's shell returns control (regardless of exit status — e.g. quitting
`nvim`, or `nvim` exiting non-zero). This is opt-in and implemented via shell
chaining (`<command>; herdr pane close <pane_id>`) because Herdr's `pane run`
types the command into the pane's already-running interactive shell rather
than spawning it as the pane's root process — Herdr has no native
close-on-exit primitive today.

The same flag also works on a workspace with a top-level `command`
(`[[workspaces]]` with `command = "..."`) and on a simple-command
`[templates.<name>]` (no `tabs`), closing the workspace's root pane after
that command's shell returns control (regardless of exit status). It is
rejected for `type = "group"` workspaces, for
workspaces with `template = "..."` set (the template owns close-on-exit per
node), and for top-level templates with `tabs` set (per-tab/per-pane
close-on-exit is the node-level feature):

```toml
# A workspace whose root pane closes itself once k9s quits.
[[workspaces]]
name = "k9s"
path = "~/projects/ops"
command = "k9s"
close_on_exit = true

# The same on a simple-command template.
[templates.k9s-close]
command = "k9s"
close_on_exit = true
```

Template resolution precedence for a freshly created workspace:

1. an exact `[[workspaces]]` entry's own `template`
2. an exact `[[workspaces]]` entry's own `command`
3. the first matching `[[wildcards]]` entry's `template`
4. the template inherited from the parent group picker (if drilled into one)
5. `[defaults].template`

### Missing workspace paths

A configured workspace whose path does not exist on disk is not an error at
load time. `shep list`/the picker still show it, clearly marked missing;
`shep doctor` reports it as a warning; selecting it fails cleanly for that
selection only. shep never falls back to `/`, `$HOME`, or cwd, and never
creates the directory automatically.

```sh
shep doctor   # report configured workspace paths that don't exist
```

## Usage

```sh
shep list                  # table of every discovered candidate (default)
shep list --format tsv     # path<TAB>label<TAB>icon lines, for Television / scripts
shep list --format json    # structured output (icon included as a field)
shep doctor                 # check configured workspace paths

shep open                  # pick interactively (exact -> fzf -> TUI)
shep open foo               # open the single candidate matching "foo"
shep open .                 # open the current directory directly
shep open --path /abs/path  # open the given absolute path directly
shep preview /abs/path      # render the workspace preview for a path, then exit
shep init                   # write a path-agnostic example config
shep jump-back              # focus the previous distinct workspace for this socket
```

`shep jump-back` navigates back to the previous distinct workspace and refuses
with a specific exit code rather than guessing. See
[`docs/jump-back.md`](docs/jump-back.md) for the error taxonomy and limits.

### `shep open` selection cascade

1. **Exact** — when the query narrows to exactly one candidate, it is used
   immediately (no prompt).
2. **fzf** — when fzf is on PATH, candidates are piped through it; the query
   is pre-seeded.
3. **TUI** — the embedded Bubble Tea picker is the universal fallback. It
   groups candidates by kind (active Herdr **Workspaces**, discovered
   **Projects**, recent **Directories** from zoxide, and statically
   **Configured** `[[workspaces]]` entries, opt-in **Sessions** from Herdr, and
any declared **integration** sources included in the effective source order),
each its own collapsible group;
   a Herdr workspace can expand into its open tabs and, per tab, its panes.
    Typing fuzzy-filters (subsequence match over label + path + aliases): a match
    on a tab or pane keeps its parent workspace visible and auto-expands only the
    matching branch — sibling tabs/panes that don't match stay hidden, and a
    descendant-only match is marked distinctly from a direct one. Keys:
   arrows or `ctrl+j`/`ctrl+k` to move, `left`/`right` to collapse/expand a
   group or workspace, `enter` to open a row (or toggle a group header),
    `tab`/`shift+tab` to switch focus between the list and the preview pane
    (arrows/page keys then scroll the preview instead of moving the cursor;
     typing a letter jumps back to the list and resumes the search). Printable
     characters, including `q`, are search input. `esc` clears a non-empty
     query and cancels when the query is empty; `ctrl+c`/`ctrl+g` cancel
     immediately. `ctrl+l` toggles session-only auto/landscape only, and `?`
     opens the full keybinding reference. The responsive layout uses
     side-by-side panes when wide and list-only behavior at narrow widths; there
      is no portrait/stacked mode. The color theme follows `$NO_COLOR` >
      `$SHEP_THEME` > explicit `[tui].theme` (except `inherit`) > Herdr theme
      > a Catppuccin Mocha default (see below).


After selecting, `shep` asks Herdr to focus an existing workspace whose pane
cwd normalises to the candidate path, or to create a new focused workspace
(`herdr workspace create --cwd --label --focus`). A freshly **created**
workspace also has its resolved template applied (focused workspaces skip
templates). Herdr absent or unavailable prints the resolved path and exits 0.

### Opening inside the current workspace

`--target` selects WHERE a Command-only entry (a bare `command`, no
`template`, not a group workspace) opens:

- `workspace` (default) — focus/create a standalone Herdr workspace, same as
  today.
- `tab` — open a new tab in the Herdr workspace shep is already running
  inside.
- `pane` — split a new pane beside the pane shep is already running inside.

```sh
shep open ops --target=tab    # open "ops" as a new tab in the current workspace
shep open ops --target=pane   # open "ops" as a new pane beside the current one
```

The same targets are available from the interactive TUI picker: `Ctrl+T`
opens the highlighted candidate as a new tab, `Ctrl+P` as a new pane, and
`Enter` keeps the default (`--target`'s value, `workspace` unless overridden).

`tab` and `pane` only work when **both** conditions hold:

1. shep is running inside a Herdr workspace pane (e.g. launched from a
   Television cable or a shell running inside Herdr).
2. The selected entry is Command-only — no `template`, not a `type = "group"`
   workspace.

When shep is not running inside a Herdr pane, the TUI's `Ctrl+T`/`Ctrl+P`
footer hints are omitted and the keys are no-ops; `shep open --target=tab|pane`
returns a clear error instead. Targeting a template or group entry with
`--target=tab|pane` also returns a clear, specific error naming the entry.
Note: a synthesized tab/pane row (an already-open Herdr tab, or a pane
inside one) is never a `Ctrl+T`/`Ctrl+P` target either — `Enter` is the only
supported action there, and it focuses the row's containing tab (Herdr has
no command to focus one exact pane).

### `[tui]` pane sizing, layout, and theme

```toml
[tui]
list_width = "auto"     # "auto" or a percentage like "60%"
preview_width = "60%"
layout = "landscape"     # "landscape" (side-by-side), or omit for responsive auto
theme = "mocha"           # "mocha", "macchiato", "frappe", "latte", "plain", or "inherit"
```

`list_width`/`preview_width` mean "share of the split axis" for the
side-by-side layout. Omitting `layout` lets the picker choose side-by-side or
list-only from the reported terminal width (with a small hysteresis margin so
resizes near the breakpoint do not flicker); there is no portrait/stacked mode.
Press `ctrl+l` while the picker is open to toggle auto ↔ landscape for the
current session only — it never writes back to `config.toml`. `theme =
"inherit"` delegates to the Herdr theme. Theme precedence is exactly
`NO_COLOR > SHEP_THEME > explicit config theme (except inherit) > Herdr theme
> mocha`; `plain` (or `$NO_COLOR`) drops every color escape

sequence and relies on textual/structural markers (bold, underline, a `>`/`~`
row marker) instead, so the picker stays fully usable over a plain terminal
or when Nerd Fonts/24-bit color aren't available.

## Workspace previews

The `shep open` picker's right-hand pane and `shep preview <path>` use the
same underlying preview renderer and configuration. Built-in sections are
hardcoded and always available by name — no declaration needed:

- `identity` — label, path, source, matched template
- `git` — a fast git summary (skipped when git is missing or slow)
- `workspace` — the active Herdr workspace's tabs/panes tree
- `active_pane` — the active pane's captured terminal buffer
- `agent_status` — the focused Herdr pane's agent status, initialized from
  the startup snapshot and updated from live Herdr events when available;
  disconnected or unavailable event streams fall back gracefully to the
  snapshot/static behavior
- `dir` — a directory listing, preferring `lsd`, then `eza`, then `ls -la`

```sh
shep preview /abs/path       # plain text (no ANSI) — safe for pipes/Television
shep preview --color /path   # force real renderer colors (lsd/eza/pane) through, even when piped
```

`[preview].default` picks which sections render when nothing more specific
applies; the full precedence is workspace `preview` > wildcard `preview` >
source `preview` > `[preview].default` > a single built-in `identity`
fallback:

```toml
[preview]
timeout = "150ms"
cache_ttl = "5s"
max_lines = 50
default = ["identity", "git"]

[preview.commands.recent_commits]
command = "git -C {{.Path}} log -n 3"
```

`[preview.commands.<name>]` declares a reusable global preview command referenced by
name alongside the built-ins above. Its existing shell-style string contract is
preserved; it is tokenized and executed argv-first (no `sh -c`), with timeout,
output caps, and caching. A failing command is silently omitted from normal
preview output and never breaks the picker or `shep preview`.

An integration can add private commands under its own namespace. These commands
use argv arrays and are executable only for candidates from that integration:

```toml
[[integrations]]
name = "kube-contexts"
command = ["/path/kube-contexts"]
preview = ["identity", "cluster", "health"]

[integrations.preview_commands.cluster]
command = ["/path/kube-preview", "cluster", "{{ index .Meta \"context\" }}"]
max_lines = 12

[integrations.preview_commands.health]
command = ["/path/kube-preview", "health", "{{ index .Meta \"context\" }}"]
timeout = "1s"
max_lines = 10
```

`integration.preview` controls order and may name built-ins, global commands, or
that integration's private commands. A local command from another integration
is invalid. Local `timeout` and `max_lines` inherit `[preview].timeout` and
`[preview].max_lines` when omitted. Local names must not collide with built-in
sections or global command names, but may repeat across integrations.

Commands use rowformat actions such as `{{.Path}}`, `{{.Label}}`, and
`{{ index .Meta "context" }}`. Each argv token is rendered independently, so a
metadata value containing spaces remains one argument. Integration JSON rows
provide inert string metadata only; they cannot declare or override executable
preview commands. No environment or secrets are exposed to templates.

## Television integration

A [Television](https://github.com/alexpasmantier/television) cable ships at
[`cables/shep.toml`](cables/shep.toml). Copy it into your Television
cable directory and launch with `tv shep`:

```sh
mkdir -p ~/.config/television/cable
cp cables/shep.toml ~/.config/television/cable/shep.toml
tv shep
```

The cable's source is `shep list --format tsv`, which prints one
`path<TAB>label<TAB>icon` line per candidate (the icon is the source's
configured Nerd Font glyph). Television's `{split:\t:N}` templates extract
those fields: `[source].display` renders each results-list entry as
`<icon> <label>` (instead of the raw TSV line) and `[source].output` forwards
just the path. The preview panel runs `shep preview --color '{split:\t:0}'`,
which forces the renderer's real ANSI color through even though Television
runs it as a subprocess — so the picker's preview pane shows actual color for
`dir` (lsd/eza) and `active_pane` (captured pane) sections, matching the
`shep open` picker. Selecting an entry runs `shep open --path '{split:\t:0}'`,
which flows through the same Herdr focus/create path as the CLI.

## Herdr popup

Use Herdr's native popup command to open `shep open` in a session-modal
terminal without changing the tab layout.

**Prerequisites**: Herdr installed and `shep` available on `$PATH` (`go
install ./cmd/shep`, or `make install` from a clone of this repository).

**Bind a key** in `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+ctrl+f" # Pick any key your config does not already bind.
type = "popup"
command = "shep open"
```

Run `herdr server reload-config` to load the binding. The same block is in
[`contrib/herdr-config.toml`](contrib/herdr-config.toml).

When shep runs in the popup, it detects the Herdr pane it is running in
(`Driver.CurrentPane`) and enables in-overlay features: `Ctrl+T`/`Ctrl+P` to
open the highlighted entry as a new tab or pane, the `agent_status` preview
section, and the footer's `focused: <status>` hint. Running `shep open` from
a terminal remains supported and shows the picker without those extras.

## Build, test, lint

```sh
make build        # ./shep
make test         # go test -race ./...
make vet          # go vet ./...
make lint         # golangci-lint run (if installed)
scripts/check-no-user-paths.sh   # CI guard against hardcoded user-home paths
```

## Hardcoded path guarantee

shep must never ship a developer-specific path. The CI guard
(`scripts/check-no-user-paths.sh`) scans committed text artifacts, including
shipped non-test Go, README.md, docs, internal/config/example.go, cables, and
sample/config files. It rejects absolute `/Users/<name>/...`,
`/home/<name>/...`, and developer-specific `~/Proyectos` paths while allowing
neutral placeholders. The guard is intentionally run in CI; run it locally
before pushing changes that touch defaults.

## Contributing

- Conventional Commit messages; no AI attribution.
- Keep defaults path-agnostic — extend `scripts/check-no-user-paths.sh` if you
  add a new shipped file.
- Tests must pass `go test -race ./...`; the TUI is covered by `teatest` key
  sequences, no real TTY required.

## License

MIT.
