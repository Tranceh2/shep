#!/usr/bin/env bash
# scripts/check-no-user-paths.sh — CI guard against hardcoded user paths (CD-5).
#
# shep ships path-agnostic defaults so it is safe to clone onto any machine.
# This script fails the build if a real user-home path leaks into shipped
# source, configs, the Television cable, or the README. The intentional
# detector literals inside *_test.go (the slices that `shep init` checks
# against) are excluded, as are example placeholder strings.

set -euo pipefail

# Match a complete user directory component, not a prefix such as the words
# "home/end" in a keybinding comment. The leading boundary also prevents these
# expressions from matching a path fragment embedded in another token.
patterns=(
  '(^|[^[:alnum:]_])/(Users|home)/[A-Za-z0-9][A-Za-z0-9._-]*(/|$)'
  '(^|[^[:alnum:]_])~/Proyectos(/|$)'
)

# Scan every committed text artifact except dependency checksums, generated
# output, vendored code, binary files, and tests whose fixtures intentionally
# use neutral fake home paths. This includes shipped non-test Go, README/docs,
# internal/config/example.go, cables, and config/sample files.
scan_files() {
  git ls-files -- ':!go.sum' ':!vendor/**' ':!dist/**' ':!**/*_test.go' ':!internal/tui/testdata/**' ':!scripts/check-no-user-paths.sh'
}

check_paths() {
  local violations=0 pat hits
  for pat in "${patterns[@]}"; do
    hits=$(git grep -nI -E "$pat" -- $(scan_files) || true)
    if [[ -n "$hits" ]]; then
      echo "violation: pattern '$pat' found:" >&2
      echo "$hits" >&2
      violations=$((violations + 1))
    fi
  done
  return "$violations"
}

# A shell-level regression contract for the boundary-sensitive detector. It is
# runnable without a test framework: scripts/check-no-user-paths.sh --self-test.
self_test() {
  local fixture
  fixture=$(mktemp)
  trap 'rm -f "$fixture"' RETURN
  printf '%s\n' '/home/alice/project' >"$fixture"
  if ! grep -qE "${patterns[0]}" "$fixture"; then
    echo "self-test: /home/alice/project must be rejected" >&2
    return 1
  fi
  printf '%s\n' 'home/end' 'PATH_TO_PROJECT' >"$fixture"
  if grep -qE "${patterns[0]}" "$fixture"; then
    echo "self-test: neutral placeholders must pass" >&2
    return 1
  fi
  echo "ok: path detector boundary self-test"
}

if [[ "${1:-}" == "--self-test" ]]; then
  self_test
  exit $?
fi

if ! check_paths; then
  echo "FAIL: hardcoded user-path patterns detected. Remove real home paths;" >&2
  echo "example/placeholder strings in comments are fine, real paths are not." >&2
  exit 1
fi

echo "ok: no hardcoded user paths in committed text artifacts."
