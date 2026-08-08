#!/bin/sh
# Builds a STATIC fuse-overlayfs binary from source (pinned) and drops it where
# xbind looks (next to its own binary). xbind mounts each sandbox root with it
# so unprivileged directory renames work — i.e. so `apt install` etc. succeed in
# a terminal (plans/isolation.md §1). Without it xbind falls back to kernel
# overlayfs (which forbids redirect_dir unprivileged).
#
# From source + static so we vendor our own portable binary (works across host
# distros) rather than trusting a prebuilt blob. Needs docker/podman, like
# build-rootfs.sh; the appliance bakes the result in.
#
#   hack/build-fuse-overlayfs.sh [dest-dir]     # default: ./bin
set -eu

DEST="${1:-./bin}"
VERSION="${FUSE_OVERLAYFS_VERSION:-v1.14}"
DOCKER="${DOCKER:-docker}"

mkdir -p "$DEST"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

# Alpine/musl gives a genuinely static libfuse3 (fuse3-static), so the result is
# a single self-contained binary with no runtime library deps.
cat > "$ctx/Dockerfile" <<EOF
FROM docker.io/library/alpine:3.20 AS build
RUN apk add --no-cache git autoconf automake libtool gcc make \\
    musl-dev fuse3-dev fuse3-static pkgconf linux-headers
RUN git clone --depth 1 --branch ${VERSION} \\
    https://github.com/containers/fuse-overlayfs /src
RUN cd /src && ./autogen.sh \\
 && LIBS="-ldl" LDFLAGS="-static" ./configure \\
 && make
FROM scratch AS out
COPY --from=build /src/fuse-overlayfs /fuse-overlayfs
EOF

echo ">> building static fuse-overlayfs ${VERSION} (needs $DOCKER; cached after)"
# PLATFORM (e.g. linux/arm64) cross-builds via the engine's qemu emulation —
# set by deploy/publish-release.sh when producing a foreign-arch bundle.
DOCKER_BUILDKIT=1 "$DOCKER" build ${PLATFORM:+--platform "$PLATFORM"} -f "$ctx/Dockerfile" --target out \
    -o "type=local,dest=$DEST" "$ctx"
chmod +x "$DEST/fuse-overlayfs"
"$DEST/fuse-overlayfs" --version | head -1
echo ">> built $DEST/fuse-overlayfs (xbind next to it uses it automatically)"
