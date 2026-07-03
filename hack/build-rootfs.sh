#!/bin/sh
# Builds the buxon component base rootfs (plans/runtime.md) and unpacks it into
# a directory for `buxond --isolate --rootfs <dir>`. Needs docker (or podman);
# in production this is a published OCI image the appliance ships unpacked.
#
#   hack/build-rootfs.sh [output-dir]        # default: ./.rootfs
set -eu

OUT="${1:-./.rootfs}"
TAG="${BUXON_ROOTFS_TAG:-buxon-rootfs:dev}"
DOCKER="${DOCKER:-docker}"

repo=$(cd "$(dirname "$0")/.." && pwd)
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

# The Dockerfile COPYs a prebuilt bx from the build context.
cp "$repo/docker/rootfs.Dockerfile" "$ctx/Dockerfile"
( cd "$repo" && CGO_ENABLED=0 go build -o "$ctx/bx" ./cmd/bx )

echo ">> building $TAG"
"$DOCKER" build -t "$TAG" "$ctx"

echo ">> unpacking to $OUT"
mkdir -p "$OUT"
cid=$("$DOCKER" create "$TAG")
"$DOCKER" export "$cid" | tar -C "$OUT" -xf -
"$DOCKER" rm "$cid" >/dev/null

echo ">> done. run:"
echo "   buxond --workspace <ws> --isolate --rootfs $(cd "$OUT" && pwd)"
