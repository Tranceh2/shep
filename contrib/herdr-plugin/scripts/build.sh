#!/usr/bin/env bash
set -euo pipefail

# Puts the shep binary in the plugin's bin/. By default it downloads the
# release binary of the manifest's version for this platform and checks it
# against the release's checksums.txt, so installing the plugin needs no Go
# toolchain; if that is not possible it builds the checkout instead, which
# needs Go 1.26.4+. SHEP_PLUGIN_BUILD=source always builds the checkout (a
# local development link) and SHEP_PLUGIN_BUILD=release never does.

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
plugin_root="$(cd -- "${script_dir}/.." && pwd)"
repo_root="$(cd -- "${plugin_root}/../.." && pwd)"

mode="${SHEP_PLUGIN_BUILD:-auto}"
release_url="${SHEP_RELEASE_URL:-https://github.com/Tranceh2/shep/releases/download}"
manifest_version="$(sed -n 's/^version = "\(.*\)"$/\1/p' "${plugin_root}/herdr-plugin.toml")"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "${plugin_root}/bin"

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl --fail --silent --show-error --location --retry 2 --max-time 120 --output "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget --quiet --output-document="$2" "$1"
  else
    echo "shep plugin: neither curl nor wget is available to download the release" >&2
    return 1
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

release_binary() {
  local os arch archive want got
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "shep plugin: no release binary for $(uname -s)" >&2; return 1 ;;
  esac
  case "$(uname -m)" in
    arm64 | aarch64) arch=arm64 ;;
    x86_64 | amd64) arch=amd64 ;;
    *) echo "shep plugin: no release binary for $(uname -m)" >&2; return 1 ;;
  esac
  archive="shep_${manifest_version}_${os}_${arch}.tar.gz"
  fetch "${release_url}/v${manifest_version}/${archive}" "${work}/${archive}" || return 1
  fetch "${release_url}/v${manifest_version}/checksums.txt" "${work}/checksums.txt" || return 1
  want="$(awk -v name="$archive" '$2 == name {print $1}' "${work}/checksums.txt")"
  got="$(sha256 "${work}/${archive}")"
  if [[ -z "$want" || "$want" != "$got" ]]; then
    echo "shep plugin: ${archive} does not match the release checksum" >&2
    return 1
  fi
  tar -xzf "${work}/${archive}" -C "$work" shep
  chmod 0755 "${work}/shep"
  mv -f "${work}/shep" "${plugin_root}/bin/shep"
  echo "shep plugin: installed the v${manifest_version} release binary (${os}/${arch})"
}

source_binary() {
  if ! command -v go >/dev/null 2>&1; then
    echo "shep plugin: building from source needs Go 1.26.4+, which is not on PATH" >&2
    return 1
  fi
  local version commit
  version="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || true)"
  # Herdr installs the plugin from a checkout without tags, where describe
  # only finds the commit: the manifest's version names the release instead.
  case $version in
    v[0-9]*) ;;
    *) version="$manifest_version" ;;
  esac
  commit="$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || printf '%s' none)"
  go -C "$repo_root" build \
    -ldflags "-X main.version=${version} -X main.commit=${commit}" \
    -o "${plugin_root}/bin/shep" \
    ./cmd/shep
}

case $mode in
  release) release_binary ;;
  source) source_binary ;;
  auto)
    if ! release_binary; then
      echo "shep plugin: building v${manifest_version} from source instead" >&2
      source_binary
    fi
    ;;
  *)
    echo "shep plugin: SHEP_PLUGIN_BUILD must be auto, release or source, not ${mode}" >&2
    exit 2
    ;;
esac
