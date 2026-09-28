#!/bin/sh
set -eu

script_path=$0
case $script_path in
  /*) ;;
  *) script_path=$(pwd)/$script_path ;;
esac
plugin_root=${script_path%/scripts/run-shep.sh}

path_entries=
append_unique() {
  candidate_entry=$1
  entries_to_scan=$path_entries
  while [ -n "$entries_to_scan" ]; do
    case $entries_to_scan in
      *:*) existing_entry=${entries_to_scan%%:*}; entries_to_scan=${entries_to_scan#*:} ;;
      *) existing_entry=$entries_to_scan; entries_to_scan= ;;
    esac
    [ "$existing_entry" = "$candidate_entry" ] && return 0
  done
  if [ -n "$path_entries" ]; then
    path_entries=$path_entries:$candidate_entry
  else
    path_entries=$candidate_entry
  fi
}

add_dir() {
  directory_entry=$1
  [ -n "$directory_entry" ] && [ "${directory_entry#/}" != "$directory_entry" ] && [ -d "$directory_entry" ] || return 0
  append_unique "$directory_entry"
}

add_dir "${XDG_BIN_HOME:-}"
if [ -n "${HOME:-}" ]; then
  add_dir "${HOME}/.local/bin"
  add_dir "${HOME}/go/bin"
fi
add_dir "${GOBIN:-}"
add_dir "${BUN_INSTALL:-${HOME:-}/.bun}/bin"
if [ -n "${HOME:-}" ]; then
  add_dir "${HOME}/.cargo/bin"
  add_dir "${HOME}/.local/share/mise/shims"
fi
if [ -n "${USER:-}" ]; then
  add_dir "/etc/profiles/per-user/${USER}/bin"
fi
add_dir "/run/current-system/sw/bin"
add_dir "/opt/homebrew/bin"
add_dir "/usr/local/bin"

existing_path=${PATH:-}
while [ -n "$existing_path" ]; do
  case $existing_path in
    *:*) existing_entry=${existing_path%%:*}; existing_path=${existing_path#*:} ;;
    *) existing_entry=$existing_path; existing_path= ;;
  esac
  [ -n "$existing_entry" ] && append_unique "$existing_entry"
done
export PATH="$path_entries"
exec "$plugin_root/bin/shep" "$@"
