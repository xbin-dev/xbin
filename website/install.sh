#!/bin/sh
# xbin — bootstrap installer, served at https://xbin.dev/install.sh
#
# This file stays tiny on purpose so you can read what you pipe into sudo —
# and it is 100% STATIC across releases: it resolves the latest tagged
# release from GitHub at run time and delegates to that release's real
# installer (deploy/install.sh in the tagged tree), which preflight-checks
# the kernel, builds from source, and sets up the systemd service. Audit:
#   https://github.com/xbin-dev/xbin/tags → deploy/install.sh at the tag
#
#   curl -fsSL https://xbin.dev/install.sh | sudo bash              # system service
#   curl -fsSL https://xbin.dev/install.sh | bash -s -- --user      # your user, no root
#   curl -fsSL https://xbin.dev/install.sh | sudo bash -s -- --check-only
#   XBIN_VERSION=v0.2.0 curl -fsSL https://xbin.dev/install.sh | sudo bash   # pin one
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
URL="https://raw.githubusercontent.com/xbin-dev/xbin/${VERSION}/deploy/install.sh"

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

# Pin the source checkout the installer builds to the same release.
XBIN_REF="$VERSION"
export XBIN_REF

bash "$tmp" "$@"
