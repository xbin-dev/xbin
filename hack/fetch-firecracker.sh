#!/bin/sh
# Fetches the pinned upstream Firecracker release (static-pie, musl) and drops
# it where xbind looks (next to its own binary). xbind runs it inside each
# VM sandbox (plans/vm-sandbox.md) — never the jailer, which needs root; the
# namespace sandbox is the jail.
#
#   hack/fetch-firecracker.sh [dest-dir]     # default: ./bin
set -eu

DEST="${1:-./bin}"
VERSION="${FIRECRACKER_VERSION:-v1.17.0}"
ARCH="${ARCH:-$(uname -m)}"
case "$ARCH" in
  x86_64|amd64) ARCH=x86_64 SHA256="${FIRECRACKER_SHA256:-06094a1108ae9e82aa4c23a775aa92758f53f1175d422270d9d6162cb9ade558}" ;;
  aarch64|arm64) ARCH=aarch64 SHA256="${FIRECRACKER_SHA256:-e351ebe4f7a16b5873bbd51005d2e6767103cff4d5ebc829df2d3f95a93e2256}" ;;
  *) echo "fetch-firecracker: unsupported arch $ARCH" >&2; exit 1 ;;
esac

mkdir -p "$DEST"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
url="https://github.com/firecracker-microvm/firecracker/releases/download/${VERSION}/firecracker-${VERSION}-${ARCH}.tgz"
echo ">> fetching Firecracker ${VERSION} (${ARCH})"
curl -fsSL -o "$tmp/fc.tgz" "$url"
echo "${SHA256}  $tmp/fc.tgz" | sha256sum -c - >/dev/null
tar -xzf "$tmp/fc.tgz" -C "$tmp"
install -m 0755 "$tmp/release-${VERSION}-${ARCH}/firecracker-${VERSION}-${ARCH}" "$DEST/firecracker"
"$DEST/firecracker" --version | head -1
