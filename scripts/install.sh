#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
#
# suctl installer (Linux/macOS). Fetches the latest release for this OS/arch,
# verifies its checksum, and delegates to `suctl install` — which owns the
# on-disk layout (sdk/paths), so this script never hardcodes install paths.
#
#   curl -fsSL https://suctl.com/install | sh
#
# Pin a version:   SUCTL_VERSION=v0.5.0   (default: latest release)
# Use a fork:      SUCTL_REPO=owner/name  (default: solutionsunity/suctl)
set -eu

REPO="${SUCTL_REPO:-solutionsunity/suctl}"
GH="https://github.com/${REPO}"

err()  { printf '\n  error: %s\n\n' "$1" >&2; exit 1; }
info() { printf '  %s\n' "$1"; }

# --- required tools ---------------------------------------------------------
command -v curl >/dev/null 2>&1 || err "curl is required"
command -v tar  >/dev/null 2>&1 || err "tar is required"
if   command -v sha256sum >/dev/null 2>&1; then SHACHECK="sha256sum -c"
elif command -v shasum    >/dev/null 2>&1; then SHACHECK="shasum -a 256 -c"
else err "need sha256sum or shasum to verify the download"
fi

# --- platform detection -----------------------------------------------------
os="$(uname -s)"
case "$os" in
  Linux)  os="linux"  ;;
  Darwin) os="darwin" ;;
  *) err "unsupported OS '$os' (this installer covers Linux and macOS; on Windows use install.ps1)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) err "unsupported architecture '$arch'" ;;
esac

case "${os}-${arch}" in
  linux-amd64|linux-arm64|darwin-amd64|darwin-arm64) ;;
  *) err "no suctl release is published for ${os}/${arch}" ;;
esac

# --- resolve version --------------------------------------------------------
# Honour an explicit pin; otherwise follow the releases/latest 302 to its tag.
# No API call, no rate limit, no jq — just the redirect GitHub already serves.
if [ -n "${SUCTL_VERSION:-}" ]; then
  tag="$SUCTL_VERSION"
else
  final="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${GH}/releases/latest")" \
    || err "could not reach GitHub to resolve the latest version"
  tag="${final##*/}"
fi
case "$tag" in
  v*) ;;
  *)  err "could not determine a valid version (got '$tag')" ;;
esac

name="suctl-${tag}-${os}-${arch}.tar.gz"
url="${GH}/releases/download/${tag}/${name}"

info ""
info "==> Installing suctl ${tag} (${os}/${arch})"

# --- download + verify ------------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

curl -fsSL "$url"          -o "${tmp}/${name}"        || err "download failed: $url"
curl -fsSL "${url}.sha256" -o "${tmp}/${name}.sha256" || err "checksum download failed"

( cd "$tmp" && $SHACHECK "${name}.sha256" >/dev/null 2>&1 ) \
  || err "checksum verification failed for ${name}"
info "    checksum ok"

# --- extract ----------------------------------------------------------------
tar -C "$tmp" -xzf "${tmp}/${name}" || err "extraction failed"
dir="${tmp}/suctl-${tag}-${os}-${arch}"
[ -x "${dir}/suctl" ] || err "extracted archive is missing the suctl binary"

# --- delegate to `suctl install` (requires root; owns the FHS layout) -------
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
    info "    elevating with sudo to install system-wide"
  else
    err "must run as root — re-run via sudo or as root"
  fi
fi

$SUDO "${dir}/suctl" install
