# shep

[![ci](https://github.com/tranceh2/shep/actions/workflows/ci.yml/badge.svg)](https://github.com/tranceh2/shep/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/tranceh2/shep)](https://goreportcard.com/report/github.com/tranceh2/shep)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)

`shep` is a fast, keyboard-first project and session launcher designed for [Herdr](https://herdr.dev).

It discovers project candidates across your active Herdr workspaces, configured workspaces and groups, zoxide frecency history, and marker-based project directories. Selecting a candidate instantly focuses the workspace if it is already open, or creates a fresh one with your configured multi-pane template applied. When Herdr is absent or stopped, `shep` gracefully degrades to printing the resolved absolute path so you never get stranded.

---

## Inspiration & Story

I used `tmux` for years and grew completely reliant on the fast session-switching workflow popularized by tools like [sesh](https://github.com/joshmedeski/sesh) (by Josh Medeski).

When I moved my setup to [Herdr](https://herdr.dev), I missed that exact muscle memory: pressing a single global keybinding from anywhere to search across active workspaces, recent project directories, and background tasks, and jumping right into context without fiddling with tabs or paths.

`shep` started as a personal tool that I built and evolved over time with AI assistance for my own daily workflow. I am admittedly terrible at self-promotion and marketing, but I wanted to open-source it in case anyone else using Herdr or terminal multiplexers finds it useful. If you have ideas, spot bugs, or want to improve it, contributions are very welcome!

---

## Highlights

- ⚡ **Instant Startup & Pure In-Memory Filtering:** Immediate first-frame render. Slower providers load asynchronously in the background. Zero disk I/O while typing.
- 🤖 **Dedicated Agents Scope & Attention Queue:** Press `Tab` to switch between `all` workspaces and `agents`. Attention-first ordering puts newly blocked (`◉`) or completed (`●`) AI coding agents at the top. Once you jump in to review an agent, it automatically drops below actively working agents (`⠋`).
- 🔍 **Extended Fuzzy Search & Field Filters:** Inspired by `fzf` and `Snacks.nvim`. Supports multi-term space-AND, pipe `|` OR, exact `'term`, prefix `^`, suffix `$`, negation `!term`, and field filters like `status:blocked`, `status:working`, `agent:claude`, `source:herdr`, and `path:api`.
- 🔁 **True MRU A↔B Jump-Back:** Ships with a native Herdr plugin action (`tranceh2.shep-jump-back.jump-back`) to alternate back and forth between your two most recently visited workspaces like `prefix + L` in tmux.
- 📐 **Adaptive Learning & Pins:** Learns from successful selections using private SQLite WAL storage (with 0600 file permissions and automatic corruption quarantine). Press `Ctrl+F` to pin high-priority projects to the top.
- 🧩 **Hierarchical Groups & Templates:** Nested group pickers (`type = "group"`), multi-tab/multi-split layouts with custom focus nodes, and `close_on_exit` flags.
- 🎨 **Modular TUI Architecture:** Built on Charm's Bubble Tea and Lip Gloss with Catppuccin themes (Mocha, Macchiato, Frappe, Latte), auto-color detection, and a pure structural ASCII fallback when `NO_COLOR` is set.

---

## Requirements

- **Go 1.26+** (built and tested with Go 1.26.4).
- **[Herdr](https://herdr.dev)** (`herdr` on `$PATH`) — recommended. If Herdr is absent, `shep` simply outputs the selected path to stdout.
- **[zoxide](https://github.com/ajeetdsouza/zoxide)** — optional, enabled by default as a history source.
- **[fzf](https://github.com/junegunn/fzf)** — optional external selector fallback.
- **[lsd](https://github.com/lsd-rs/lsd)** or **[eza](https://github.com/eza-community/eza)** — optional, used by directory previews (falls back to `ls -la`).

---

## Installation

### Via `go install`

```sh
go install github.com/tranceh2/shep/cmd/shep@latest
```

### Pre-built Binaries

Download pre-compiled binaries for Linux and macOS (ARM64 and AMD64) from the [GitHub Releases](https://github.com/tranceh2/shep/releases) page.

### From Source

```sh
git clone https://github.com/tranceh2/shep.git
cd shep
make install   # builds and installs to $GOPATH/bin with ldflags versioning
```

Or build locally:

```sh
make build     # produces ./shep
```

### Nix / Flake

```sh
nix profile install github:tranceh2/shep
```

Or run ad-hoc:

```sh
nix run github:tranceh2/shep -- open
```

---

## Quickstart

Run the interactive picker:

```sh
shep open
```

Open a candidate directly by name:

```sh
shep open my-project
```

Open the current working directory:

```sh
shep open .
```

List discovered candidates as text or JSON:

```sh
shep list
shep list --format json
```

Verify your configuration and workspace paths:

```sh
shep doctor
```

---

## Herdr Integration

### 1. Popup Launcher

To launch `shep` in a floating modal inside Herdr, add this to `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+ctrl+f"  # Or your preferred shortcut
type = "popup"
command = "shep open"
```

Reload the config:

```sh
herdr server reload-config
```

When running inside a Herdr popup, `shep` detects the active pane, allowing you to press `Ctrl+T` to open candidates as tabs in the current workspace or `Ctrl+P` to split as panes.

### 2. Fast A↔B Jump-Back Toggle

`shep` includes a Herdr plugin that tracks your focus history across workspaces. Pressing the shortcut alternates between your current workspace and the previous one.

Register the plugin:

```sh
herdr plugin link ./contrib/jump-back-plugin
herdr plugin enable tranceh2.shep-jump-back
```

Bind the action in `~/.config/herdr/config.toml`:

```toml
[[keys.action]]
key = "prefix+tab"
action = "tranceh2.shep-jump-back.jump-back"
```

See [`docs/jump-back.md`](docs/jump-back.md) for architecture details.

---

## Keybindings

| Key | Context | Action |
|---|---|---|
| `Up` / `Down`, `j` / `k` | List | Move selection cursor |
| `Ctrl+N` / `Ctrl+P` | List | Alternative cursor navigation |
| `Home` / `End`, `g` / `G` | List | Jump to top / bottom |
| `PageUp` / `PageDown` | List | Scroll page up / down |
| `Left` / `Right` | List | Collapse / expand grouped workspaces |
| `Tab` / `Shift+Tab` | Global | Cycle filter scope: `all` ↔ `agents` |
| `Enter` | List | Open selected workspace (or focus agent's tab) |
| `Ctrl+T` | Inside Herdr | Open selected entry as a new tab in current workspace |
| `Ctrl+P` | Inside Herdr | Open selected entry as a split pane in current workspace |
| `Ctrl+F` | List | Toggle pin status for selected candidate |
| `Alt+Up` / `Alt+Down` | Global | Scroll preview pane up / down |
| `Alt+j` / `Alt+k` | Global | Alternative preview scrolling |
| `Ctrl+L` | Global | Toggle layout mode (side-by-side vs responsive auto) |
| `?` | Global | Show full contextual help overlay |
| `Esc` | Global | Clear active search query; quit if query is empty |
| `Ctrl+C` / `Ctrl+G` | Global | Cancel and exit |

---

## Search Syntax

Shep features an extended fuzzy search engine:

- **Multi-term AND:** `frontend auth` (matches items containing both terms)
- **OR Operator:** `api | backend` (matches items containing either term)
- **Exact matching:** `'billing` (matches literal substring "billing")
- **Prefix match:** `^web` (item must start with "web")
- **Suffix match:** `service$` (item must end with "service")
- **Negation:** `!test` (excludes items matching "test")
- **Field filters:**
  - `status:blocked` or `status:working` or `status:done`
  - `agent:claude` or `agent:opencode`
  - `source:herdr`, `source:zoxide`, `source:projects`
  - `path:services`

---

## Configuration Reference (`config.toml`)

Shep loads `$XDG_CONFIG_HOME/shep/config.toml` (or `~/.config/shep/config.toml`).

Generate a fully commented starter config:

```sh
shep init
```

### Full Configuration Example

```toml
[general]
# Order in which sources appear in the picker. Built-in: herdr, workspaces, zoxide, projects, sessions.
source_order = ["herdr", "workspaces", "zoxide", "projects"]

# Selector backend: "builtin" (TUI), "fzf", or "auto"
selector = "builtin"

# Template for dynamic workspace names created from zoxide/projects
workspace_name = '{{ .Path | osBase | lower }}'

[ranking]
# Enable adaptive learning from successful launches
enabled = true

[defaults]
type = "shell"
template = "default"

[tui]
# Layout options: "landscape" (side-by-side) or omit for responsive auto
layout = "landscape"
list_width = "auto"
preview_width = "60%"

# Theme: "mocha", "macchiato", "frappe", "latte", "plain", or "inherit" (from Herdr)
theme = "mocha"

# Icon tier: "unicode" (default) or "ascii"
icons = "unicode"

# Initial scope on startup: "all" or "agents"
initial_scope = "all"

[sources.herdr]
icon = " "
label_format = "{{if .Label}}{{.Label}} · {{end}}{{.Path}}"

[sources.workspaces]
icon = " "
label_format = "{{.Label}}"

[sources.zoxide]
icon = " "
limit = 100

[sources.projects]
icon = " "
roots = ["~/projects", "~/work"]
markers = [".git", "Cargo.toml", "go.mod", "package.json"]
max_depth = 3

[preview]
timeout = "150ms"
cache_ttl = "5s"
max_lines = 50
default = ["identity", "git"]

# Statically defined workspaces
[[workspaces]]
name = "api"
path = "~/projects/api"
template = "backend"

# Nested group picker
[[workspaces]]
name = "infrastructure"
type = "group"
path = "~/infrastructure"
source_order = ["projects"]

# Multi-pane templates
[templates.backend]
description = "Backend service environment"
focus = { tab = "code", node = "editor" }

[[templates.backend.tabs]]
name = "code"
root = "split"

  [[templates.backend.tabs.nodes]]
  id = "split"
  split = "rows"
  children = ["editor", "logs"]
  sizes = [75, 25]

  [[templates.backend.tabs.nodes]]
  id = "editor"
  command = "nvim ."

  [[templates.backend.tabs.nodes]]
  id = "logs"
  command = ""

# Wildcard naming and templates
[[wildcards]]
pattern = "**/microservices/*"
template = "backend"
workspace_name = 'svc-{{ .Path | osBase }}'

# Custom script integrations
[[integrations]]
name = "pull-requests"
command = ["gh", "pr", "list", "--json", "number,title,headRefName"]
icon = " "
aliases = ["pr", "review"]
timeout = "3s"
```

---

## Television Integration

If you use [Television](https://github.com/alexpasmantier/television), a ready-to-use cable is provided in [`cables/shep.toml`](cables/shep.toml):

```sh
mkdir -p ~/.config/television/cable
cp cables/shep.toml ~/.config/television/cable/shep.toml
tv shep
```

---

## Contributing

Contributions, feedback, and pull requests are warmly welcomed!

- Conventional Commit messages (`feat: ...`, `fix: ...`, `docs: ...`, `refactor: ...`).
- Keep defaults path-agnostic: run `./scripts/check-no-user-paths.sh` before submitting changes.
- Ensure the test suite passes with race detection:
  ```sh
  make test    # go test -race ./...
  make lint    # golangci-lint run
  ```

---

## License

This project is licensed under the [MIT License](LICENSE).
