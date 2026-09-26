#!/bin/sh
# Builds a STATIC mkfs.erofs (erofs-utils, pinned) and drops it where xbind
# looks (next to its own binary). xbind turns the unpacked rootfs into the
# read-only erofs image VM sandboxes boot from (plans/vm-sandbox.md), in a
# confined run. Shipped rather than added to the rootfs image, so the base
# version — and every terminal's "base update" prompt — is untouched.
#
#   hack/build-mkfs-erofs.sh [dest-dir]     # default: ./bin
#   NATIVE=1 hack/build-mkfs-erofs.sh       # build on the host (autotools,
#                                           # liblz4, libuuid) — dynamic
set -eu

DEST="${1:-./bin}"
VERSION="${EROFS_UTILS_VERSION:-v1.9.4}"
DOCKER="${DOCKER:-docker}"
REPO=https://git.kernel.org/pub/scm/linux/kernel/git/xiang/erofs-utils.git

mkdir -p "$DEST"
DEST="$(cd "$DEST" && pwd)"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

if [ "${NATIVE:-0}" = 1 ]; then
  echo ">> building mkfs.erofs ${VERSION} natively"
  git clone -q --depth 1 --branch "$VERSION" "$REPO" "$ctx/src"
  (cd "$ctx/src" && ./autogen.sh >/dev/null && ./configure --enable-lz4 --disable-fuse >/dev/null && make -j"$(nproc)" >/dev/null)
  install -m 0755 "$ctx/src/mkfs/mkfs.erofs" "$DEST/mkfs.erofs"
else
  cat > "$ctx/Dockerfile" <<DOCKERFILE
FROM docker.io/library/alpine:3.22 AS build
RUN apk add --no-cache git autoconf automake libtool gcc make musl-dev pkgconf \\
    lz4-dev lz4-static util-linux-dev util-linux-static linux-headers
RUN git clone --depth 1 --branch ${VERSION} ${REPO} /src
RUN cd /src && ./autogen.sh \\
 && ./configure --enable-lz4 --disable-fuse --disable-shared --enable-static \\
 && make LDFLAGS=-all-static
FROM scratch AS out
COPY --from=build /src/mkfs/mkfs.erofs /mkfs.erofs
DOCKERFILE
  echo ">> building static mkfs.erofs ${VERSION} (needs $DOCKER; cached after)"
  DOCKER_BUILDKIT=1 "$DOCKER" build ${PLATFORM:+--platform "$PLATFORM"} -f "$ctx/Dockerfile" --target out \
      -o "type=local,dest=$DEST" "$ctx"
  chmod +x "$DEST/mkfs.erofs"
fi
"$DEST/mkfs.erofs" --version 2>&1 | head -1
