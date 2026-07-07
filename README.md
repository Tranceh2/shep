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
(every built-in source enabled, no predefined workspaces or templates).

Generate a commented example config:

```sh
shep init          # write config.toml (errors if it already exists)
shep init --force  # overwrite an existing config
```

### Sources

`[general].sources` lists the enabled built-in sources and their
merge/display order. Only four names are supported — there is no support for
arbitrary user-defined providers:

```toml
[general]
sources = ["herdr", "workspaces", "zoxide", "projects"]
selector = "builtin"   # builtin | fzf | auto
```

- **herdr** — active Herdr workspaces.
- **workspaces** — predefined `[[workspaces]]` entries (single projects or
  `type = "group"` nested pickers).
- **zoxide** — your zoxide history.
- **projects** — directories detected because they contain any configured
  marker (a file or directory name — not git-only), scanned beneath a
  `type = "group"` workspace's own path.

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
sources = ["projects", "zoxide"]
template = "dev"
```

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
```

### `shep open` selection cascade

1. **Exact** — when the query narrows to exactly one candidate, it is used
   immediately (no prompt).
2. **fzf** — when fzf is on PATH, candidates are piped through it; the query
   is pre-seeded.
3. **TUI** — the embedded Bubble Tea fuzzy picker (subsequence filter over
   label + path, preview pane, Catppuccin Mocha palette) is the universal
   fallback. Keys: arrows or `ctrl+j`/`ctrl+k` to move, `enter` to select,
   `esc`/`q`/`ctrl+c`/`ctrl+g` to cancel, `ctrl+l` to toggle landscape/
   portrait layout for the current session. Below both panes, a full-width
   footer line always shows the highlighted candidate's complete icon+label/
   path, even when the list column is too narrow to show it in full.

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
hints render dimmed and the keys are no-ops; `shep open --target=tab|pane`
returns a clear error instead. Targeting a template or group entry with
`--target=tab|pane` also returns a clear, specific error naming the entry.

### `[tui]` pane sizing and layout

```toml
[tui]
list_width = "auto"     # "auto" or a percentage like "60%"
preview_width = "60%"
layout = "landscape"     # "landscape" (side-by-side, default) or "portrait" (stacked)
```

`list_width`/`preview_width` mean "share of the split axis" in both
orientations: width in `landscape`, height in `portrait` (list on top,
preview below, both full terminal width). Press `ctrl+l` while the picker is
open to toggle between `landscape` and `portrait` for the current session
only — it never writes back to `config.toml`.

## Workspace previews

The `shep open` picker's right-hand pane and `shep preview <path>` use the
same underlying preview renderer and configuration. Built-in sections are
hardcoded and always available by name — no declaration needed:

- `identity` — label, path, source, matched template
- `git` — a fast git summary (skipped when git is missing or slow)
- `workspace` — the active Herdr workspace's tabs/panes tree
- `active_pane` — the active pane's captured terminal buffer
- `agent_status` — the focused Herdr pane's agent status, a static
  at-open-time snapshot (not live-updated)
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
command = "git -C {path} log -n 3"
```

`[preview.commands.<name>]` declares a custom preview command referenced by
name alongside the built-ins above: argv-parsed (no `sh -c`), run with a
timeout, output capped and cached. A failing custom command is silently
omitted from normal preview output (no error/warning shown); it never breaks
the picker or `shep preview`.

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

## Herdr keybind

Bind a key in your own `~/.config/herdr/config.toml` to launch `shep open`
in a temporary Herdr pane — a plain user keybind, not a Herdr plugin (no
manifest, no `HERDR_PLUGIN_CONTEXT_JSON` decoding).

**Prerequisites**: Herdr installed, and `shep` on `$PATH` (`go install
./cmd/shep`, or `make install` from a clone of this repo).

```toml
# Launch shep in a temporary Herdr pane. type = "pane" opens a scratch pane
# that closes automatically once shep exits (after focusing/creating the
# target workspace) — the same pattern Herdr uses for lazygit/$SHELL panes.
[[keys.command]]
key = "prefix+alt+p"   # pick any key your config does not already bind
type = "pane"
command = "shep open"
description = "shep: open project picker"
```

The block above is copy-pasteable from
[`contrib/herdr-config.toml`](contrib/herdr-config.toml). `command = "shep
open"` references the binary by name only, so it resolves through the
invoking shell's `$PATH` — do not rewrite it to a relative path, a
hardcoded absolute path under your home directory, or a plugin-manifest
entry; none of those are needed for this keybind.

Herdr's custom-command keybindings (`[[keys.command]]`, verified against
Herdr 0.7.1's `--default-config`) are global only: there is currently no
per-workspace or workspace-local override for a custom command binding, so
this single block is the whole picture — the same keybind applies from
every workspace.

When the bound key launches `shep open` from inside that Herdr pane, shep
detects it is running inside a Herdr pane (`Driver.CurrentPane`) and enables
the in-overlay features: `Ctrl+T`/`Ctrl+P` to open the highlighted entry as
a new tab/pane, the `agent_status` preview section, and the footer's
`focused: <status>` hint. Launching `shep open` from a raw terminal (outside
any Herdr pane) shows the picker without any of those — same as today.

## Build, test, lint

```sh
make build        # ./shep
make test         # go test -race ./...
make vet          # go vet ./...
make lint         # golangci-lint run (if installed)
scripts/check-no-user-paths.sh   # CI guard against hardcoded /Users/ paths
```

## Hardcoded path guarantee

shep must never ship a developer-specific path. The CI guard
(`scripts/check-no-user-paths.sh`) greps shipped Go sources (non-test), the
cable, the README and configs for `/Users/`, `/home/<name>`, and `~/Proyectos`
and fails the build on any hit. The guard is intentionally run in CI; run it
locally before pushing changes that touch defaults.

## Contributing

- Conventional Commit messages; no AI attribution.
- Keep defaults path-agnostic — extend `scripts/check-no-user-paths.sh` if you
  add a new shipped file.
- Tests must pass `go test -race ./...`; the TUI is covered by `teatest` key
  sequences, no real TTY required.

## License

MIT.
