#!/usr/bin/env bash
# scripts/check-no-user-paths.sh — CI guard against hardcoded user paths (CD-5).
#
# shep ships path-agnostic defaults so it is safe to clone onto any machine.
# This script fails the build if a real user-home path leaks into shipped
# source, configs, the Television cable, or the README. The intentional
# detector literals inside *_test.go (the slices that `shep init` checks
# against) are excluded, as are example placeholder strings.

set -euo pipefail

# Patterns that indicate a real hardcoded user path.
#  - /Users/...        : macOS absolute home prefix
#  - /home/<letter>... : Linux absolute home prefix
#  - ~/Proyectos       : developer-specific project root used during v1 dev
patterns=(
  "/Users/"
  "/home/[a-z]"
  "~/Proyectos"
)

# Files we scan. _test.go files are excluded because they intentionally hold
# the detector slices; the example config comment uses placeholders only. We
# scan only shipped RUNTIME files (Go sources + the Television cable), not docs
# or the guard script itself: the README and this script legitimately reference
# the patterns to document and enforce them, which would be a self-hit.
scan_files() {
  # active Go sources (non-test) and the shipped Television cable.
  git ls-files \
    '*.go' ':!*_test.go' \
    'cables/*.toml'
}

violations=0
for pat in "${patterns[@]}"; do
  hits=$(scan_files | xargs -r grep -InE "$pat" || true)
  if [[ -n "$hits" ]]; then
    echo "violation: pattern '$pat' found:" >&2
    echo "$hits" >&2
    violations=$((violations + 1))
  fi
done

if (( violations > 0 )); then
  echo "FAIL: hardcoded user-path patterns detected. Remove real home paths;" >&2
  echo "example/placeholder strings in comments are fine, real paths are not." >&2
  exit 1
fi

echo "ok: no hardcoded user paths in shipped Go source or the Television cable."