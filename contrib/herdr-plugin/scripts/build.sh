#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
plugin_root="$(cd -- "${script_dir}/.." && pwd)"
repo_root="$(cd -- "${plugin_root}/../.." && pwd)"

version="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || printf '%s' dev)"
commit="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || printf '%s' none)"

mkdir -p "${plugin_root}/bin"

go -C "$repo_root" build \
  -ldflags "-X main.version=${version} -X main.commit=${commit}" \
  -o "${plugin_root}/bin/shep" \
  ./cmd/shep
