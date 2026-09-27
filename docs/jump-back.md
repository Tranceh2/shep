# Jump back to the previous workspace

`shep jump-back` toggles between the two most recently focused workspaces (A↔B)
for the Herdr socket you are currently attached to. It is the navigation half of
the feature: it reads focus history that the `watch-history` collector maintains,
revalidates the target against live Herdr state, and focuses it — or refuses with
a specific reason. The same focus history also drives the open workspace MRU order
in the Shep picker when opening with an empty query (gracefully falling back to
launch ranking when the collector is not running).

This is intentionally different from Herdr's native `previous_workspace`: the
native action walks sidebar/order, while Shep `jump-back` uses the observed focus
MRU and toggles A↔B. The bundled plugin action is
`tranceh2.shep.jump-back`.

For a Herdr keybinding, use the plugin action rather than native
`previous_workspace`:

```toml
[[keys.command]]
key = "prefix+tab"
type = "plugin_action"
command = "tranceh2.shep.jump-back"
description = "jump to previous workspace"
```

The plugin-local action command is argv-only (`["./bin/shep", "jump-back"]`).
Herdr injects `HERDR_SOCKET_PATH` for the current session and
`HERDR_BIN_PATH` for the authoritative Herdr executable; Shep consumes both
without shell evaluation.

History is collected by `shep watch-history`, a hidden long-lived command that
the bundled Herdr plugin starts automatically on launch. Until that collector
runs, `jump-back` correctly reports `history not ready` (exit 3) and changes
nothing.

## Quick path

```sh
shep jump-back        # focus the previous distinct live workspace
echo $?               # 0 on success; see the taxonomy below otherwise
```

On success the focused workspace id is printed to stdout and the exit status
is `0`. Every refusal writes one sanitized line to stderr and exits non-zero.

## Error taxonomy

Each category is distinguishable by exit code and message. Internal errors are
classified, never echoed verbatim.

| Exit | Message | What it means |
|---|---|---|
| `0` | *(workspace id on stdout)* | The previous distinct workspace was focused. |
| `1` | `jump-back: focus failed for workspace <id>` | Validation passed but the Herdr focus operation itself failed. The failure is surfaced; the underlying cause stays in the error chain rather than being echoed, so an internal command line or payload never reaches your terminal. |
| `2` | `jump-back: no previous workspace` | History is ready but holds no distinct live workspace other than the current one. |
| `3` | `jump-back: history not ready` | No collector answered, the collector reports an unverified epoch, or the owner is hung. Nothing is focused. |
| `4` | `jump-back: target session no longer available` | The resolved target vanished before focus, or the fresh snapshot could not be taken. |
| `5` | `jump-back: current workspace changed during resolve` | Live state disagreed with the collector, or the current workspace changed between resolution and focus. |
| `6` | `jump-back: history store error` | The collector reported a storage failure. The internal cause is not leaked. |

## How a target is chosen

1. **Ask the owner.** `jump-back` dials the collector's per-socket control
   endpoint with a bounded timeout and reads readiness plus the ordered MRU. A
   dial or read failure is a not-ready signal, so an absent or hung collector
   fails closed. The CLI never reads the history database directly.
2. **Agree on the current workspace.** A fresh `herdr api snapshot` must report
   the same current workspace the collector reports. A disagreement exits `5`.
3. **Resolve the previous distinct live target.** The MRU is walked
   newest-first, skipping the current workspace (so consecutive duplicates
   collapse), skipping workspaces absent from the live snapshot, and skipping
   any id that is not a well-formed Herdr identifier.
4. **Revalidate, then focus.** A second fresh snapshot must still list the
   target *and* still show the unchanged current workspace. Only then is the
   existing focus operation invoked for that workspace id, so `jump-back` can
   never create a workspace.

## Limits you should know

These are real constraints of the public Herdr contract, not implementation
shortcuts.

| Limit | Consequence |
|---|---|
| **No sequence or snapshot-cut marker.** The public Herdr event API exposes no sequence number or cut marker between a snapshot and the event stream. | Snapshots validate **membership and current state, never chronology**. History reflects the focus order actually observed on a healthy stream. |
| **Offline history cannot be reconstructed.** Focus changes that happen while no collector is running are never recovered. | After a gap, history becomes ready again only from newly observed focus events. |
| **Undetectable delivery gaps are possible.** A silently dropped stream frame cannot be detected from the client side. | No losslessness is claimed. Detectable gaps — cold start, disconnect, decode failure, store failure — invalidate readiness and make `jump-back` refuse. |
| **A residual pre-focus race remains.** The public focus contract has no expected-current precondition and no compare-and-swap. | A window remains between the final validation and the focus call. `jump-back` narrows it with a second fresh snapshot; it does not eliminate it. |
| **Reconnect exhaustion is not host death.** Spending the bounded reconnect budget proves only that the stream was not re-established. | It is reported as a classified non-zero failure, never as a clean exit and never as proof the Herdr host died. |

## Per-socket isolation

History is keyed by the SHA-256 of the canonical Herdr socket path, so two
Herdr sessions never share or merge history. Only workspace ids are stored —
no pane content, no command output, no secrets. The state directory is created
with `0700` and the database with `0600`.

## Install the collector

The collector runs as part of the unified Herdr plugin `tranceh2.shep`.

### Option A: Install from GitHub (Remote Install)

When installing via Herdr, Herdr clones the repository and automatically runs
the declared `[[build]]` step (`go build -o bin/shep ./cmd/shep`), so no manual
binary copying is required:

```sh
herdr plugin install Tranceh2/shep/contrib/herdr-plugin
```

### Option B: Local Checkout (Development)

For local development or manual installs, link the plugin directory and build
the binary in place:

```sh
cd /path/to/shep
herdr plugin link "$PWD/contrib/herdr-plugin"
cd contrib/herdr-plugin
go build -o bin/shep ../../cmd/shep
./bin/shep --version   # verify the build
```

`bin/shep` is an installation artifact. It is intentionally not committed, and
the plugin's tests assert that it never is.

### Verify and Start

Confirm the plugin is recognized and enabled:

```sh
herdr plugin list                                  # confirm tranceh2.shep is listed and enabled
herdr plugin action list --plugin tranceh2.shep
```

For the current session, start or recover the collector immediately:

```sh
herdr plugin action invoke tranceh2.shep.start-history
herdr plugin action invoke tranceh2.shep.jump-back
herdr plugin log list --plugin tranceh2.shep       # inspect the run
```

From subsequent Herdr starts onward, the `[[startup]]` hook starts the history
collector automatically once the session is restored and the API socket is ready.

### Confirm it works

```sh
shep jump-back    # exit 3 until two distinct workspaces have been focused
# focus workspace A, then workspace B, then:
shep jump-back    # focuses A, exit 0
shep jump-back    # focuses B, exit 0: the next invocation toggles back
```

Readiness requires two trustworthy focus observations in the current epoch, so the
first `jump-back` after a fresh start legitimately reports `not ready`. Bootstrap
membership validates live ids but is not chronology; the focus event caused by a
successful jump-back is then observed and makes the next invocation toggle back.

## Recovery, refusal, and shutdown

| Situation | What happens |
|---|---|
| **Start against a live owner** | The duplicate refuses immediately and exits without disturbing the incumbent. Its control endpoint is left alone. Safe to invoke the start action any number of times. |
| **Recover after the owner exited** | Invoke the start action (`tranceh2.shep.start-history`) again. The new process acquires the per-socket lock the dead owner released, and history begins a fresh epoch. Nothing from before the gap is trusted. |
| **A live but hung owner** | Recovery is **refused**, not forced. This plugin never kills, signals, or guesses a process id, and offers no `--force`. `jump-back` meanwhile fails closed with `history not ready` because the control socket does not answer within its bounded timeout. Resolving a hung owner is a deliberate manual act outside this plugin. |
| **Ctrl-C / SIGTERM** | The collector cancels its run context, closes the I/O it owns, joins its workers, releases the lock, and removes only its own control endpoint. |
| **Reconnect budget exhausted** | The collector stops with a classified non-zero status. This means the stream was not re-established; it is **not** proof the Herdr host died and is never reported as a clean exit. |

## Disable, unlink, and uninstall

```sh
herdr plugin disable tranceh2.shep   # stop future autostart
herdr plugin unlink  tranceh2.shep   # unregister, leave files in place
```

Both are **future-only**. Neither terminates a collector that is already
running: an existing child continues until it exits naturally or you stop it
yourself. What they prevent is the startup hook running on subsequent Herdr
starts.

To remove the plugin entirely, unlink it and remove the build artifact
`contrib/herdr-plugin/bin/shep`. The history database is **preserved by default**
— uninstalling the plugin never deletes your data. Remove it deliberately if you
want it gone:

```sh
rm -f "${XDG_STATE_HOME:-$HOME/.local/state}/shep/jump_history.sqlite3"
```

The per-socket control endpoint and lock file live beside that database and are
recreated on the next start.

## Verified status

The jump-back command, collector, control protocol, private history store, and
bundled plugin are implemented in the current source. Focused tests cover
readiness refusal, bounded control I/O, per-socket history, duplicate-owner
handling, shutdown, and end-to-end focus behavior using test fixtures.

The current repository verification command is:

```sh
go test ./internal/command ./internal/history ./internal/herdrwatch
```

A live Herdr session is not required by the test suite and no manual production
session is claimed here. Before release, run the full repository race and
cross-build verification. The documented limitations above remain part of the
contract.
