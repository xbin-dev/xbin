#!/bin/sh
# Builds a STATIC gocryptfs binary from source (pinned) and drops it where
# xbind looks (next to its own binary). xbind mounts each *encrypted* resource
# (filesystem/sqlite) with it: ciphertext lives under data/resources-enc/, the
# decrypted view is bind-mounted into the component sandbox, and the key is
# derived from the vault barrier — so a stolen workspace/disk yields only
# ciphertext (plans/vault-data.md).
#
# Two pinned sources are patched before building:
#   - gocryptfs + hack/gocryptfs-patches/: the xbin single-tenant mode
#     (-xbin-single-tenant), which container-store resources need
#     (docs/resources.md);
#   - go-fuse + hack/gofuse-patches/: the FUSE writeback-cache opt-in the
#     single-tenant mode uses to batch small writes (go-fuse never wired
#     the flag upstream). Wired in with a local `replace`.
# A patch that no longer applies fails the build loudly rather than shipping
# a binary that silently lacks the mode.
#
# Built with `-tags without_openssl` so it's pure-Go and fully static (no CGO,
# no libcrypto) — one portable binary across host distros, same spirit as
# build-fuse-overlayfs.sh. At runtime it needs fusermount3 (the fuse3 package,
# which the installer already pulls in) to mount rootlessly.
#
#   hack/build-gocryptfs.sh [dest-dir]     # default: ./bin
set -eu

DEST="${1:-./bin}"
VERSION="${GOCRYPTFS_VERSION:-v2.6.1}"
GOFUSE_VERSION="${GOFUSE_VERSION:-v2.8.0}"
DOCKER="${DOCKER:-docker}"
PATCHES="$(cd "$(dirname "$0")" && pwd)/gocryptfs-patches"
GOFUSE_PATCHES="$(cd "$(dirname "$0")" && pwd)/gofuse-patches"

mkdir -p "$DEST"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT
mkdir -p "$ctx/patches" "$ctx/gofuse-patches"
cp "$PATCHES"/*.patch "$ctx/patches/"
cp "$GOFUSE_PATCHES"/*.patch "$ctx/gofuse-patches/"

cat > "$ctx/Dockerfile" <<EOF
FROM docker.io/library/golang:alpine AS build
RUN apk add --no-cache git patch
RUN git clone --depth 1 --branch ${VERSION} \\
    https://github.com/rfjakob/gocryptfs /src
RUN git clone --depth 1 --branch ${GOFUSE_VERSION} \\
    https://github.com/hanwen/go-fuse /go-fuse
COPY patches/*.patch /patches/
COPY gofuse-patches/*.patch /gofuse-patches/
# Apply both patchsets; any hunk failure aborts the build.
RUN set -e; for p in /patches/*.patch; do \\
        echo ">> applying \$(basename "\$p")"; \\
        patch -p1 -d /src < "\$p"; \\
    done; \\
    for p in /gofuse-patches/*.patch; do \\
        echo ">> applying \$(basename "\$p") (go-fuse)"; \\
        patch -p1 -d /go-fuse < "\$p"; \\
    done
WORKDIR /src
RUN go mod edit -replace github.com/hanwen/go-fuse/v2=/go-fuse
# without_openssl → Go stdlib crypto only (no CGO); fully static binary.
RUN CGO_ENABLED=0 go build -tags without_openssl \\
    -ldflags "-s -w -X main.GitVersion=${VERSION}+xbin -X main.BuildFlags=static" \\
    -o /gocryptfs .
FROM scratch AS out
COPY --from=build /gocryptfs /gocryptfs
EOF

echo ">> building static gocryptfs ${VERSION}+xbin (needs $DOCKER; cached after)"
DOCKER_BUILDKIT=1 "$DOCKER" build -f "$ctx/Dockerfile" --target out \
    -o "type=local,dest=$DEST" "$ctx"
chmod +x "$DEST/gocryptfs"
"$DEST/gocryptfs" --version | head -1
# The single-tenant mode is not optional — container-store resources are held
# without it (internal/resenc probes for it at mount time).
"$DEST/gocryptfs" -hh 2>&1 | grep -q xbin-single-tenant || {
    echo "error: built gocryptfs lacks -xbin-single-tenant (patch not applied?)" >&2
    exit 1
}
echo ">> built $DEST/gocryptfs (xbind next to it uses it automatically)"
