#!/usr/bin/env bash
set -euo pipefail

# Opens the Shep picker popup using the Herdr binary provided by the plugin
# runtime (HERDR_BIN_PATH) or discovered from PATH.
plugin_id="tranceh2.shep"
pane_id="picker"

herdr_bin="${HERDR_BIN_PATH:-}"
if [[ -z "$herdr_bin" ]]; then
  if ! herdr_bin="$(command -v herdr)"; then
    echo "shep herdr plugin: herdr binary not found on PATH" >&2
    exit 127
  fi
elif [[ ! -x "$herdr_bin" ]]; then
  echo "shep herdr plugin: HERDR_BIN_PATH is not executable: $herdr_bin" >&2
  exit 127
fi

exec "$herdr_bin" plugin pane open \
  --plugin "$plugin_id" \
  --entrypoint "$pane_id" \
  --placement popup
