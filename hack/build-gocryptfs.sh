#!/bin/sh
# Builds a STATIC gocryptfs binary from source (pinned) and drops it where
# xbind looks (next to its own binary). xbind mounts each *encrypted* resource
# (filesystem/sqlite) with it: ciphertext lives under data/resources-enc/, the
# decrypted view is bind-mounted into the component sandbox, and the key is
# derived from the vault barrier — so a stolen workspace/disk yields only
# ciphertext (plans/vault-data.md).
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
DOCKER="${DOCKER:-docker}"

mkdir -p "$DEST"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

cat > "$ctx/Dockerfile" <<EOF
FROM golang:alpine AS build
RUN apk add --no-cache git
RUN git clone --depth 1 --branch ${VERSION} \\
    https://github.com/rfjakob/gocryptfs /src
WORKDIR /src
# without_openssl → Go stdlib crypto only (no CGO); fully static binary.
RUN CGO_ENABLED=0 go build -tags without_openssl \\
    -ldflags "-s -w -X main.GitVersion=${VERSION} -X main.BuildFlags=static" \\
    -o /gocryptfs .
FROM scratch AS out
COPY --from=build /gocryptfs /gocryptfs
EOF

echo ">> building static gocryptfs ${VERSION} (needs $DOCKER; cached after)"
DOCKER_BUILDKIT=1 "$DOCKER" build -f "$ctx/Dockerfile" --target out \
    -o "type=local,dest=$DEST" "$ctx"
chmod +x "$DEST/gocryptfs"
"$DEST/gocryptfs" --version | head -1
echo ">> built $DEST/gocryptfs (xbind next to it uses it automatically)"
