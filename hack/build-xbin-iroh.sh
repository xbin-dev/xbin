#!/bin/sh
# Builds xbin-iroh, xbind's iroh sidecar (D204, sidecar/xbin-iroh), as one
# static, stripped musl binary: `make build` gzips it into xbind
# (internal/apptunnel/sidecar/embedded/, gitignored) and the tunnel runs it
# from a memfd. The builder is a digest-pinned cargo-zigbuild image (Rust
# plus zig as the C cross compiler ring needs); `--locked`, so the committed
# Cargo.lock is what ships. It cross-compiles natively for either arch —
# Rust under qemu emulation would take hours.
#
#   hack/build-xbin-iroh.sh [dest-dir]   # default ./bin → <dest>/xbin-iroh
#   hack/build-xbin-iroh.sh --test       # cargo fmt --check, clippy, test (make sidecar-check)
#   hack/build-xbin-iroh.sh --deny       # cargo deny: advisories + licences (make release)
#
# The arch: PLATFORM's (linux/arm64, as deploy/publish-release.sh and the
# helpers set it), else the host's. Env: DOCKER, XBIN_IROH_BUILDER (another
# image), XBIN_IROH_LOCAL=1 (--test/--deny with this machine's cargo instead
# of docker).
set -eu

BUILDER="${XBIN_IROH_BUILDER:-ghcr.io/rust-cross/cargo-zigbuild:0.24.0@sha256:eb5578cfc06e070a09deb7b5852474bd128a4e561f9f123261b5ed2405e478b1}"
CARGO_DENY_VERSION=0.20.2
DOCKER="${DOCKER:-docker}"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CRATE="$ROOT/sidecar/xbin-iroh"

MODE=build
case "${1:-}" in
  --test) MODE="test"; shift ;;
  --deny) MODE="deny"; shift ;;
  -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
esac
DEST="${1:-./bin}"

if [ "${XBIN_IROH_LOCAL:-}" = 1 ] && [ "$MODE" != build ]; then
  cd "$CRATE"
  if [ "$MODE" = test ]; then
    cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings && cargo test --locked
  else
    cargo deny --locked check advisories licenses
  fi
  exit
fi

a="${PLATFORM:-}"
if [ -n "$a" ]; then a=${a#*/}; a=${a%%/*}; else a=$(uname -m); fi
case "$a" in
  x86_64|amd64) TRIPLE=x86_64-unknown-linux-musl ;;
  aarch64|arm64) TRIPLE=aarch64-unknown-linux-musl ;;
  *) echo "build-xbin-iroh: no build for arch $a (amd64, arm64)" >&2; exit 1 ;;
esac

ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT
mkdir -p "$ctx/src"
# the crate, minus a local target/
tar -C "$CRATE" --exclude=./target -cf - . | tar -C "$ctx/src" -xf -

cat > "$ctx/Dockerfile" <<EOF
FROM ${BUILDER} AS base
WORKDIR /src
COPY src/ /src/

FROM base AS build
RUN --mount=type=cache,id=xbin-iroh-cargo,target=/usr/local/cargo/registry \\
    --mount=type=cache,id=xbin-iroh-target,target=/src/target \\
    cargo zigbuild --locked --release --target ${TRIPLE} \\
 && cp target/${TRIPLE}/release/xbin-iroh /xbin-iroh

FROM base AS test
RUN --mount=type=cache,id=xbin-iroh-cargo,target=/usr/local/cargo/registry \\
    --mount=type=cache,id=xbin-iroh-target-test,target=/src/target \\
    rustup component add rustfmt clippy >/dev/null 2>&1; \\
    cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings \\
 && cargo test --locked > /test.log 2>&1; rc=\$?; cat /test.log; exit \$rc

FROM base AS deny
RUN --mount=type=cache,id=xbin-iroh-cargo,target=/usr/local/cargo/registry \\
    cargo install --locked cargo-deny --version ${CARGO_DENY_VERSION} --root /opt/deny
RUN --mount=type=cache,id=xbin-iroh-cargo,target=/usr/local/cargo/registry \\
    /opt/deny/bin/cargo-deny --locked check advisories licenses > /deny.log 2>&1; rc=\$?; cat /deny.log; exit \$rc

FROM scratch AS out
COPY --from=build /xbin-iroh /xbin-iroh
FROM scratch AS test-out
COPY --from=test /test.log /test.log
FROM scratch AS deny-out
COPY --from=deny /deny.log /deny.log
EOF

case "$MODE" in
  test)
    echo ">> sidecar-check: cargo fmt, clippy, test in $BUILDER"
    DOCKER_BUILDKIT=1 "$DOCKER" build --progress=plain --no-cache-filter test -f "$ctx/Dockerfile" --target test-out -o "type=local,dest=$ctx/out" "$ctx" 2>&1 \
      | sed -n 's/^#[0-9]* [0-9.]* //p' | grep -E '^(test |error|warning: |Diff in|failures|---- )|FAILED|panicked' || true
    [ -f "$ctx/out/test.log" ] || { echo "sidecar-check: FAILED" >&2; exit 1; }
    grep -E '^test result' "$ctx/out/test.log"
    ;;
  deny)
    echo ">> cargo deny $CARGO_DENY_VERSION: advisories + licences (sidecar/xbin-iroh/deny.toml)"
    DOCKER_BUILDKIT=1 "$DOCKER" build --progress=plain --no-cache-filter deny -f "$ctx/Dockerfile" --target deny-out -o "type=local,dest=$ctx/out" "$ctx" 2>&1 \
      | sed -n 's/^#[0-9]* [0-9.]* //p' | grep -vE '^(sha256|extracting|resolve|transferring|naming)' | tail -40
    [ -f "$ctx/out/deny.log" ] || { echo "cargo deny: FAILED" >&2; exit 1; }
    ;;
  build)
    mkdir -p "$DEST"
    echo ">> building xbin-iroh for $TRIPLE in $BUILDER (needs $DOCKER; cached after)"
    DOCKER_BUILDKIT=1 "$DOCKER" build -f "$ctx/Dockerfile" --target out -o "type=local,dest=$ctx/out" "$ctx"
    install -m 0755 "$ctx/out/xbin-iroh" "$DEST/xbin-iroh"
    echo ">> built $DEST/xbin-iroh ($TRIPLE, $(wc -c < "$DEST/xbin-iroh") bytes)"
    ;;
esac
