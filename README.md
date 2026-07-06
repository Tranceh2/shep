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

A leaf node's `close_on_exit = true` closes its pane once the node's command
finishes (e.g. quitting `nvim`). This is opt-in and implemented via shell
chaining (`<command>; herdr pane close <pane_id>`) because Herdr's `pane run`
types the command into the pane's already-running interactive shell rather
than spawning it as the pane's root process — Herdr has no native
close-on-exit primitive today.

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
shep list --format tsv     # path<TAB>label lines, for Television / scripts
shep list --format json    # structured output
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
   `esc`/`q`/`ctrl+c`/`ctrl+g` to cancel.

After selecting, `shep` asks Herdr to focus an existing workspace whose pane
cwd normalises to the candidate path, or to create a new focused workspace
(`herdr workspace create --cwd --label --focus`). A freshly **created**
workspace also has its resolved template applied (focused workspaces skip
templates). Herdr absent or unavailable prints the resolved path and exits 0.

### `[tui]` pane sizing

```toml
[tui]
list_width = "auto"     # "auto" or a percentage like "60%"
preview_width = "60%"
```

## Workspace previews

The `shep open` picker's right-hand pane and `shep preview <path>` use the
same underlying preview renderer and configuration. Built-in sections are
hardcoded and always available by name — no declaration needed:

- `identity` — label, path, source, matched template
- `git` — a fast git summary (skipped when git is missing or slow)
- `workspace` — the active Herdr workspace's tabs/panes tree
- `active_pane` — the active pane's captured terminal buffer
- `dir` — a directory listing, preferring `lsd`, then `eza`, then `ls -la`

```sh
shep preview /abs/path       # plain text (no ANSI) — safe for pipes/Television
shep preview --color /path   # passes through real renderer colors (lsd/eza/pane), only when stdout is a terminal
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

The cable's source is `shep list --format tsv`; its preview panel runs
`shep preview '{split:\t:0}'` (using the same preview configuration as the `shep open` picker); selecting an
entry runs `shep open --path '{split:\t:0}'`, which flows through the same Herdr
focus/create path as the CLI.

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
