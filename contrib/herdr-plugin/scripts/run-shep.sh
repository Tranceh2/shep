#!/usr/bin/env bash
set -euo pipefail

script_path="${BASH_SOURCE[0]}"
if [[ "$script_path" != /* ]]; then
  script_path="${PWD}/${script_path}"
fi
plugin_root="${script_path%/scripts/run-shep.sh}"

path_entries=()
append_unique() {
  local entry="$1"
  local existing
  for existing in "${path_entries[@]-}"; do
    [[ "$existing" == "$entry" ]] && return 0
  done
  path_entries+=("$entry")
}

add_dir() {
  local dir="$1"
  [[ -n "$dir" && "$dir" = /* && -d "$dir" ]] || return 0
  append_unique "$dir"
}

add_dir "${XDG_BIN_HOME:-}"
if [[ -n "${HOME:-}" ]]; then
  add_dir "${HOME}/.local/bin"
  add_dir "${HOME}/go/bin"
fi
add_dir "${GOBIN:-}"
add_dir "${BUN_INSTALL:-${HOME:-}/.bun}/bin"
if [[ -n "${HOME:-}" ]]; then
  add_dir "${HOME}/.cargo/bin"
  add_dir "${HOME}/.local/share/mise/shims"
fi
if [[ -n "${USER:-}" ]]; then
  add_dir "/etc/profiles/per-user/${USER}/bin"
fi
add_dir "/run/current-system/sw/bin"
add_dir "/opt/homebrew/bin"
add_dir "/usr/local/bin"

IFS=: read -r -a existing_path <<< "${PATH:-}"
for entry in "${existing_path[@]-}"; do
  [[ -n "$entry" ]] && append_unique "$entry"
done
IFS=:
export PATH="${path_entries[*]}"
exec "${plugin_root}/bin/shep" "$@"
