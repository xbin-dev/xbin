#!/bin/sh
# Builds a STATIC vhost-device-vsock (rust-vmm, pinned) for emulated VM
# sandboxes (plans/vm-sandbox.md §Emulated VMs). QEMU has no vsock device of
# its own outside the host kernel's vhost-vsock (global CIDs, root to set up);
# this vhost-user backend serves the guest's vsock over the same hybrid
# Unix-socket protocol as Firecracker's — CONNECT <port> in, <uds>_<port> out
# — so the shim and the guest agent don't know which VMM they run under.
#
#   hack/build-vhost-vsock.sh [dest-dir]   # default: ./bin → bin/vhost-device-vsock
set -eu

DEST="${1:-./bin}"
VERSION="${VHOST_VSOCK_VERSION:-0.3.0}"
DOCKER="${DOCKER:-docker}"

mkdir -p "$DEST"
DEST="$(cd "$DEST" && pwd)"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

# Rust's musl targets link statically by default; --locked builds the
# crate's published Cargo.lock (crates.io checksums every download).
cat > "$ctx/Dockerfile" <<DOCKERFILE
FROM docker.io/library/rust:1-alpine3.22 AS build
RUN apk add --no-cache musl-dev
RUN cargo install --locked --root /out vhost-device-vsock@${VERSION} \\
 && strip /out/bin/vhost-device-vsock
FROM scratch AS out
COPY --from=build /out/bin/vhost-device-vsock /vhost-device-vsock
DOCKERFILE
echo ">> building static vhost-device-vsock ${VERSION} (needs $DOCKER; cached after)"
DOCKER_BUILDKIT=1 "$DOCKER" build ${PLATFORM:+--platform "$PLATFORM"} -f "$ctx/Dockerfile" --target out \
    -o "type=local,dest=$DEST" "$ctx"
chmod +x "$DEST/vhost-device-vsock"
"$DEST/vhost-device-vsock" --version 2>&1 | head -1
