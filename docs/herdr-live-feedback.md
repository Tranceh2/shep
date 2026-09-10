# Real-time agent status and Herdr theme inheritance

Shep connects to the local Herdr daemon socket to reflect pane agent status
changes in real time while the picker is open, and inherits your Herdr UI theme
when Shep's own theme is unset. Both capabilities degrade gracefully to standard
static behavior when Herdr is absent, older, or disconnected.

## Quick path

```sh
# 1. Real-time agent status (automatic when running under Herdr)
shep open

# 2. Inherit Herdr theme (default when [tui].theme is unset or "inherit")
# In ~/.config/shep/config.toml:
[tui]
theme = "inherit" # or omit theme entirely
```

## Behavior

### 1. Live agent status
While the Shep picker is open, pane rows update their status icons (`idle`, `working`, `blocked`, `done`, `unknown`) in place as events occur in Herdr:
- **In-place updates**: Row status updates immediately without rebuilding the workspace tree or refetching candidates.
- **Selection stability**: The selected cursor row identity is preserved.
- **Order stability**: Row order is preserved; status changes never trigger re-sorting, re-filtering, or re-ranking.
- **Collapsed workspaces**: Status updates for collapsed workspaces are updated in the underlying tree model and appear immediately when expanded.
- **Snapshot synchronization**: Background snapshot refreshes preserve live status observations that arrived after the snapshot was requested (monotonic sequence tracking).

### 2. Theme inheritance precedence
When resolving colors, Shep applies the first matching rule:

1. `NO_COLOR` set and non-empty -> `plain` (ANSI disabled)
2. `SHEP_THEME` environment variable -> recognized flavor (`mocha`, `macchiato`, `frappe`, `latte`, `plain`)
3. `[tui].theme` in Shep config -> explicit configured flavor, except `"inherit"`
4. Herdr `[theme].name` in Herdr config -> mapped recognized flavor
5. Default fallback -> `mocha`

The exact precedence is `NO_COLOR > SHEP_THEME > explicit config theme (except
inherit) > Herdr theme > mocha`. `theme = "inherit"` delegates explicitly to
Herdr.

Theme resolution is a safe, bounded file read (<= 1 MiB) of Herdr's `config.toml`. It never executes shell commands or interpolates environment variables.

### 3. Graceful degradation
If live feedback is unavailable, Shep falls back seamlessly to static snapshots and periodic ticks:
- **Socket absent**: If `HERDR_SOCKET_PATH` is unset or nonexistent, no connection is attempted and Shep operates statically.
- **Subscription rejected / older Herdr**: If the daemon rejects the subscription event type, the failure is absorbed without blocking or error modals.
- **Mid-session disconnect / EOF**: If the socket closes or disconnects, Shep keeps the last-known statuses and continues running without reconnect storms.
- **Malformed payloads**: Unparseable or malformed payloads are safely discarded without crashing.

## Limits you should know

These are real constraints of the public Herdr contract and Shep's design, stated honestly without fabrication.

| Limit | Consequence |
|---|---|
| **No sequence, cursor, or snapshot-cut marker.** Herdr's public event contract (Herdr 0.8.2, protocol 22) exposes no monotonic sequence number, stream cursor, or snapshot-cut marker. | Live agent status is **best-effort**. Undetectable event gaps remain possible. Shep never claims lossless or authoritative status delivery. |
| **No fabricated state.** Shep never infers, extrapolates, or synthesizes an unobserved state after a gap. | If events stop arriving, pane rows retain their last observed status or show `unknown`. |
| **Flavourless Herdr theme names are honest no-ops.** Herdr configuration often specifies a family name (e.g., `[theme].name = "catppuccin"`) without a specific flavor. | Flavourless names do not map to an arbitrary flavor; they fall through to Shep's default `mocha`. On such configurations, theme inheritance is visibly a no-op. |
| **Buffered event queue with drop-oldest policy.** The internal live event queue has a bounded buffer of 32 items. | Under high event pressure, older unconsumed events are dropped to guarantee that the UI never blocks or stalls during rendering. |
| **Single-dial lifecycle and no reconnect.** Live status opens exactly one connection bounded to the picker's lifetime and does not attempt reconnects if dropped. | Transient socket disconnects degrade cleanly to static snapshot refreshes for the remainder of the picker session. |
| **No runtime daemon or plugin dependency.** Live feedback requires no external background daemon or plugin process outliving Shep. | All socket interactions are strictly bounded to the active picker process. |

## Verified status

The live status and Herdr theme paths are implemented in the current source and
covered by focused unit tests. Live startup is bounded to a short dial plus
subscription timeout; failure closes the socket and falls back to the static
picker path. The stream remains best-effort, single-connection, drop-oldest,
and non-reconnecting as described above.

The current repository verification command is:

```sh
go test ./internal/tui
```

Run the full repository and race verification before release. This document does
not claim that release verification has been run.
