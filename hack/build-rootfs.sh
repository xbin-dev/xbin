#!/bin/sh
# Builds the xbin component base rootfs (plans/runtime.md) and unpacks it into
# a directory for `xbind --isolate --rootfs <dir>`. Needs docker (or podman);
# in production this is a published OCI image the appliance ships unpacked.
#
#   hack/build-rootfs.sh [output-dir]        # default: ./.rootfs
set -eu

OUT="${1:-./.rootfs}"
TAG="${XBIN_ROOTFS_TAG:-xbin-rootfs:dev}"
DOCKER="${DOCKER:-docker}"

repo=$(cd "$(dirname "$0")/.." && pwd)
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

# The Dockerfile COPYs a prebuilt bx from the build context.
cp "$repo/docker/rootfs.Dockerfile" "$ctx/Dockerfile"
( cd "$repo" && CGO_ENABLED=0 go build -o "$ctx/bx" ./cmd/bx )

echo ">> building $TAG"
# PLATFORM (e.g. linux/arm64) cross-builds the base via the engine's qemu
# emulation — set by deploy/publish-release.sh for a foreign-arch bundle. The
# COPYed bx must match, so build it for the same arch (pure Go cross-compile:
# GOARCH is exported by the publish script; harmless when unset = host).
"$DOCKER" build ${PLATFORM:+--platform "$PLATFORM"} -t "$TAG" "$ctx"

echo ">> unpacking to $OUT"
mkdir -p "$OUT"
cid=$("$DOCKER" create ${PLATFORM:+--platform "$PLATFORM"} "$TAG")
"$DOCKER" export "$cid" | tar -C "$OUT" -xf -
"$DOCKER" rm "$cid" >/dev/null

# Stamp the base version — a content hash of the recipe (Dockerfile + this
# script). xbind uses it to pin each terminal's persistent overlay to the base
# it was built on and to offer an upgrade when the base changes; the install
# upgrade preserves old bases as <rootfs>-<version> (plans/component-env.md).
ver=$(cat "$repo/docker/rootfs.Dockerfile" "$0" | sha256sum | cut -c1-12)
printf '%s\n' "$ver" > "$OUT/etc/xbin-base-version"
echo ">> base version $ver"

echo ">> done. run:"
echo "   xbind --workspace <ws> --isolate --rootfs $(cd "$OUT" && pwd)"
