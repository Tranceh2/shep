# shep

`shep` is a Herdr-first project launcher. It enumerates project candidates from
Herdr workspaces, zoxide, the current directory, and any configured roots,
then opens the selected one with Herdr — focusing an existing workspace when
one already matches the path, or creating a fresh focused workspace otherwise.
When Herdr is not installed or unreachable, `shep` prints the resolved
absolute path and exits 0 so the caller still gets to the project.

shep ships **path-agnostic defaults**: no hardcoded `~/code`, `/Users/`, or
project roots. A pristine machine (Herdr installed, optional zoxide) works out
of the box.

## Requirements

- Go 1.26+ (built and tested on Go 1.26.4)
- [Herdr](https://github.com/...) (`herdr` on PATH) — optional but recommended;
  `shep` degrades to printing paths when it is absent.
- [zoxide](https://github.com/ajeetdsouza/zoxide) — optional source.
- [fzf](https://github.com/junegunn/fzf) — optional accelerator for
  `shep open`; the embedded Bubble Tea TUI is the universal fallback.

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

shep reads `$XDG_CONFIG_HOME/shep/config.toml` (resolved through
`os.UserConfigDir`, so `~/Library/Application Support/shep/config.toml` on
macOS). A missing config is fine: shep falls back to built-in defaults
(Herdr + zoxide when installed + the current directory).

Generate a commented example config:

```sh
shep init          # write config.toml (errors if it already exists)
shep init --force  # overwrite an existing config
```

The generated file contains no user-specific paths; uncomment and edit the
`[sources.repos]` block to add a roots directory if you want one.

```toml
# ~/.config/shep/config.toml
[sources.repos]
kind = "roots"
enabled = true

[sources.repos.options]
path = "~/code"   # set this to your projects directory

[layouts."**/*.go"]
startup = "go test ./..."
```

## Usage

```sh
shep list                  # table of every discovered candidate (default)
shep list --format tsv     # path<TAB>label lines, for Television / scripts
shep list --format json    # structured output

shep open                  # pick interactively (exact -> fzf -> TUI)
shep open foo              # open the single candidate matching "foo"
shep open --path /abs/path # open the given absolute path directly
shep preview /abs/path     # render the workspace preview for a path, then exit
shep init                  # write a path-agnostic example config
```

### `shep open` selection cascade

1. **Exact** — when the query narrows to exactly one candidate, it is used
   immediately (no prompt).
2. **fzf** — when fzf is on PATH, candidates are piped through it; the query
   is pre-seeded.
3. **TUI** — the embedded Bubble Tea fuzzy picker (subsequence filter over
   label + path, preview pane, Catppuccin Mocha palette) is the universal
   fallback. Keys: `j`/`k` or arrows to move, `enter` to select, `esc`/`q`/
   `ctrl+c` to cancel.

After selecting, `shep` asks Herdr to focus an existing workspace whose pane
cwd normalises to the candidate path, or to create a new focused workspace
(`herdr workspace create --cwd --label --focus`). When the candidate matches a
`[layouts.<glob>]`, a freshly **created** workspace also runs that startup via
`herdr pane run` (focused workspaces skip startup). Herdr absent or unavailable
prints the resolved path and exits 0.

## Workspace previews

The `shep open` picker's right-hand pane and `shep preview <path>` use the
same underlying preview renderer and configuration. However, because path-only
candidates shown via `shep preview` are loaded without the full picker
candidate context, their source field will show as `path`, so some metadata
may differ from the picker candidates. With no `[preview]` config at all, shep shows a
built-in default: `label`, `path`, `source`, a matched `[layouts.<glob>]`
template (when present), and a fast git summary (skipped when git is missing
or the check takes longer than 50ms).

```sh
shep preview /abs/path       # plain text (no ANSI) — safe for pipes/Television
shep preview --color /path   # Lip Gloss styling, only applied when stdout is a terminal
```

`[[preview.sections]]` replace the default layout with a declarative one,
rendered in declaration order:

```toml
[[preview.sections]]
name = "Identity"
type = "builtin"
fields = ["label", "path", "source", "template"]

[[preview.sections]]
name = "Git"
type = "git"
```

`preview.command` is an escape hatch for a custom preview, executed safely:
argv-parsed (no `sh -c`), run with a timeout (`preview.timeout`, default
`100ms`), stdout capped to `preview.max_lines` (default `50`) and cached for
`preview.cache_ttl` (default `5s`). A timeout, non-zero exit, or stderr output
falls back to the built-in preview plus a transient warning line — a broken
custom command never breaks the picker or `shep preview`.

```toml
[preview]
command = "git -C {path} log -n 5"
```

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