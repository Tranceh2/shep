#!/bin/sh
set -eu

# Opens the Shep picker popup. Inside Herdr (HERDR_SOCKET_PATH set) the
# plugin's own shep asks the server directly, which is much faster than
# starting the herdr CLI for it; otherwise the Herdr binary provided by the
# plugin runtime (HERDR_BIN_PATH) or discovered from PATH opens it.
plugin_id="tranceh2.shep"
pane_id="picker"

script_path=$0
case $script_path in
  /*) ;;
  *) script_path=$(pwd)/$script_path ;;
esac
shep_bin=${script_path%/scripts/open-picker.sh}/bin/shep

if [ -n "${HERDR_SOCKET_PATH:-}" ] && [ -x "$shep_bin" ]; then
  exec "$shep_bin" popup --plugin "$plugin_id" --entrypoint "$pane_id"
fi

herdr_bin=${HERDR_BIN_PATH:-}
if [ -z "$herdr_bin" ]; then
  if ! herdr_bin=$(command -v herdr); then
    echo "shep herdr plugin: herdr binary not found on PATH" >&2
    exit 127
  fi
elif [ ! -x "$herdr_bin" ]; then
  echo "shep herdr plugin: HERDR_BIN_PATH is not executable: $herdr_bin" >&2
  exit 127
fi

exec "$herdr_bin" plugin pane open \
  --plugin "$plugin_id" \
  --entrypoint "$pane_id" \
  --placement popup
