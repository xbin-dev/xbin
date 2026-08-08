#!/bin/sh
# xbin — bootstrap installer, served at https://xbin.dev/install.sh
#
# This file stays tiny on purpose so you can read what you pipe into sudo —
# and it is 100% STATIC across releases: it resolves the latest tagged
# release from GitHub at run time and delegates to that release's real
# installer in the tagged tree — deploy/install.sh on Linux (preflight-checks
# the kernel, builds from source, sets up the systemd service), or
# deploy/install-macos.sh on a Mac (sets up a Lima Linux VM you size, and
# installs inside it). Audit:
#   https://github.com/xbin-dev/xbin/tags → deploy/install*.sh at the tag
#
#   curl -fsSL https://xbin.dev/install.sh | sudo bash              # Linux: system service
#   curl -fsSL https://xbin.dev/install.sh | bash -s -- --user      # Linux: your user, no root
#   curl -fsSL https://xbin.dev/install.sh | sh                     # macOS: Lima VM (no sudo)
#   curl -fsSL https://xbin.dev/install.sh | sudo bash -s -- --check-only
#   XBIN_VERSION=v0.2.0 curl -fsSL https://xbin.dev/install.sh | sudo bash   # pin one
#   curl -fsSL https://xbin.dev/install.sh | sudo bash -s -- --system --prebuilt-rootfs
#                                          # skip the build: download a prebuilt bundle (no podman/Go)
set -eu

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$1"
  else
    echo "error: need curl or wget to fetch the installer" >&2
    exit 1
  fi
}

VERSION="${XBIN_VERSION:-}"
if [ -z "$VERSION" ]; then
  # Latest release = highest semver tag. The tags API needs no auth and no
  # jq: pull the vX.Y.Z names and version-sort. (GitHub Releases "latest"
  # would also work, but tags exist for every release by construction.)
  VERSION="$(fetch https://api.github.com/repos/xbin-dev/xbin/tags 2>/dev/null     | tr ',' '\n' | sed -n 's/.*"name": *"\(v[0-9][0-9.]*\)".*/\1/p'     | sort -V | tail -1)" || true
fi
if [ -z "$VERSION" ]; then
  echo "error: could not resolve the latest xbin release from GitHub" >&2
  echo "       (network/rate limit?) — pin one: XBIN_VERSION=vX.Y.Z $0" >&2
  echo "       releases: https://github.com/xbin-dev/xbin/tags" >&2
  exit 1
fi
# macOS gets the Lima-VM installer; everything else the Linux one (which
# explains itself on unsupported platforms).
SCRIPT=deploy/install.sh
RUNNER=bash
if [ "$(uname -s)" = Darwin ]; then
  SCRIPT=deploy/install-macos.sh
  RUNNER=sh
fi
URL="https://raw.githubusercontent.com/xbin-dev/xbin/${VERSION}/${SCRIPT}"

echo "xbin bootstrap"
echo "  release:   ${VERSION}"
echo "  installer: ${URL}"

command -v bash >/dev/null 2>&1 || {
  echo "error: the xbin installer needs bash" >&2
  exit 1
}

tmp="$(mktemp /tmp/xbin-install.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

if ! fetch "$URL" >"$tmp" || ! [ -s "$tmp" ]; then
  echo "error: could not fetch the ${VERSION} installer — is that a real release?" >&2
  echo "       releases: https://github.com/xbin-dev/xbin/releases" >&2
  exit 1
fi

# Pin the source checkout the installer builds to the same release; the mac
# installer also needs the release name itself (it pins the in-VM install).
XBIN_REF="$VERSION"
XBIN_VERSION="$VERSION"
export XBIN_REF XBIN_VERSION

"$RUNNER" "$tmp" "$@"
