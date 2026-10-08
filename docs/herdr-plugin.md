# Herdr Plugin: tranceh2.shep

The unified Shep plugin for [Herdr](https://herdr.dev) (`tranceh2.shep`) packages
interactive workspace navigation, focus history tracking, and diagnostic tools
into a single managed bundle.

## What the plugin provides

The plugin runtime uses its checked-in wrapper to add existing conventional per-user bin directories without sourcing shell profiles. This keeps optional zoxide and Git tooling discoverable even when the Herdr server started with a minimal `PATH`; no manual `PATH` editing is required for plugin usage.

- **Interactive Picker Popup (`90%` x `80%`):** Launches `shep open` in a native
  modal popup over your active Herdr session.
- **True MRU A↔B Workspace Toggle (`jump-back`):** Alternates between your two
  most recently focused workspaces (like `prefix + L` in tmux).
- **Automatic History Collector (`watch-history`):** A lightweight background
  daemon started automatically by Herdr via `[[startup]]` that observes
  workspace focus events on the Herdr socket.
- **Diagnostics (`doctor`):** Inspects configured workspace paths, the
  picker's color theme and where it came from, and the published PATH link
  status.

The plugin needs Herdr 0.8.2 or newer (`min_herdr_version` in the manifest).

---

## Installation

### Option 1: Install from GitHub (Herdr Remote Install)

If you have Go installed on your system (Go 1.26.4+), Herdr can clone the
repository and compile the binary automatically using the declared `[[build]]`
hook:

```sh
herdr plugin install Tranceh2/shep/contrib/herdr-plugin
```

To install a non-default branch or ref, pass it separately with `--ref`:

```sh
herdr plugin install --ref <branch> Tranceh2/shep/contrib/herdr-plugin
```

Herdr will:
1. Clone the repository into its managed plugin cache.
2. Run `bash scripts/build.sh` from the plugin directory; the script compiles from the checkout into plugin-local `bin/shep` with version and commit metadata.
3. Register and enable `tranceh2.shep`.

### Option 2: Link from a Local Checkout (Development)

For local development or when building from a cloned repository:

```sh
# From the root of your shep repository clone
herdr plugin link "$PWD/contrib/herdr-plugin"
cd contrib/herdr-plugin
bash scripts/build.sh
```

Verify that Herdr sees the plugin:

```sh
herdr plugin list
herdr plugin action list --plugin tranceh2.shep
```

---

## Declared Actions

The plugin registers four actions under the `tranceh2.shep` namespace:

| Action ID | Title | Contexts | Description |
|---|---|---|---|
| `tranceh2.shep.open` | Open Shep picker | global, workspace, tab, pane | Opens the picker popup overlay (see below) |
| `tranceh2.shep.jump-back` | Jump to previous workspace | workspace | Toggles between the two most recently focused workspaces |
| `tranceh2.shep.start-history` | Start Shep history collector | workspace | Operator recovery to restart the background focus watcher |
| `tranceh2.shep.doctor` | Shep doctor | global | Runs configuration and environment diagnostics |

The `open` action runs `scripts/open-picker.sh`. Inside Herdr it hands the
popup to the plugin's own `shep` (`shep popup --plugin tranceh2.shep
--entrypoint picker`, an internal command), which sends one
`plugin.pane.open` request to `HERDR_SOCKET_PATH`: the shortcut does not wait
for the `herdr` CLI to start, which takes about 200 ms once the system has
evicted it from memory. Without a socket the script runs
`herdr plugin pane open` as before. The picker itself then talks to the same
socket for its rows, previews and actions.

You can invoke any action manually from the CLI. The command takes the bare
action ID (not the `tranceh2.shep.<id>` form used in keybindings below) plus
`--plugin tranceh2.shep`:

```sh
herdr plugin action invoke open --plugin tranceh2.shep
herdr plugin action invoke jump-back --plugin tranceh2.shep
herdr plugin action invoke start-history --plugin tranceh2.shep
herdr plugin action invoke doctor --plugin tranceh2.shep
```

---

## Keybindings Configuration

Add the desired keybindings to your Herdr configuration file at
`~/.config/herdr/config.toml`:

```toml
# Open Shep picker popup
[[keys.command]]
key = "prefix+ctrl+f"
type = "plugin_action"
command = "tranceh2.shep.open"
description = "open Shep picker"

# Jump back to previous workspace
[[keys.command]]
key = "prefix+tab"
type = "plugin_action"
command = "tranceh2.shep.jump-back"
description = "jump to previous workspace"
```

Reload the Herdr configuration:

```sh
herdr server reload-config
```

### Alternative: Native Popup Keybind

If you have published `shep` to your `PATH` (see below), you can also bind the
native popup directly without going through the plugin action dispatcher:

```toml
[[keys.command]]
key = "prefix+ctrl+f"
type = "popup"
command = "shep open"
```

---

## Publishing `shep` to PATH (`shep link`)

Herdr plugin actions only execute fixed declared commands and accept no
arbitrary arguments. To run shep from a shell with your own arguments, such
as `shep open <query>`, `shep list --format tsv` or the Television cable,
publish `shep` to your PATH using `shep link` (the doctor check does not need
it: the plugin's `doctor` action runs it):

```sh
# Inside the plugin directory or local build directory
./bin/shep link
```

### How `shep link` works

- Creates a symlink (default: `~/.local/bin/shep`) pointing directly to the
  calling binary.
- Because it is a symlink rather than a copy, rebuilding the binary or updating
  the plugin immediately updates the command on your PATH.
- **Directory Precedence:**
  1. `SHEP_LINK_DIR` (explicit override)
  2. `XDG_BIN_HOME` (if set)
  3. `~/.local/bin` (standard fallback)
- **Safety:**
  - Refuses to overwrite non-Shep binaries or arbitrary files.
  - Warns if the target directory is not present in `$PATH`.
  - **Never edits shell configuration or startup profiles** (e.g. `.bashrc`,
    `.zshrc`). Adding `~/.local/bin` to PATH remains an operator choice.

To remove the symlink:

```sh
./bin/shep unlink   # or `shep unlink` if already on PATH
```

`shep unlink` only removes the link if it points back to this specific Shep
installation.

---

## Disable, Unlink, and Uninstall

To temporarily disable the plugin (stops future startup hooks):

```sh
herdr plugin disable tranceh2.shep
```

To unlink the plugin from Herdr:

```sh
herdr plugin unlink tranceh2.shep
```

If you published the binary to PATH via `shep link`, unlink it before removing
the plugin:

```sh
shep unlink
```

Focus history data is stored in
`${XDG_STATE_HOME:-$HOME/.local/state}/shep/jump_history.sqlite3`. Uninstalling
the plugin preserves your history data by default. To remove it manually:

```sh
rm -f "${XDG_STATE_HOME:-$HOME/.local/state}/shep/jump_history.sqlite3"
```
