#!/bin/sh
# Builds the guest kernel for VM sandboxes (plans/vm-sandbox.md): an
# uncompressed vmlinux that Firecracker boots directly (PVH), with no modules
# and no initrd of its own — xbind hands it the xbin-vmagent initrd.
#
# Inputs are pinned: the kernel tarball (sha256 from kernel.org's signed
# sha256sums), Firecracker's CI guest config for that kernel series (from the
# Firecracker tag xbind ships, sha256-pinned), and our fragment
# hack/vmkernel/xbin.config merged on top. Every fragment line must survive
# olddefconfig, so a dependency the base config lacks fails loudly.
#
#   hack/build-vmkernel.sh [dest-dir]      # default: ./bin → bin/vmlinux
#   NATIVE=1 hack/build-vmkernel.sh        # build on the host (gcc, make,
#                                          # flex, bison, bc, libelf, openssl)
set -eu

DEST="${1:-./bin}"
KERNEL_VERSION="${KERNEL_VERSION:-6.18.54}"
KERNEL_SHA256="${KERNEL_SHA256:-9df30b02dd8102bbd0be52556288ef6889ddbe7f1ddb96fbf847d0becf3eacac}"
FC_TAG="${FC_TAG:-v1.17.0}"
FC_CONFIG_SHA256="${FC_CONFIG_SHA256:-ba22401a0c7292a4c024ebcd10a562d4a1f1bfd2faed671406d3b159c0cf5215}"
DOCKER="${DOCKER:-docker}"
ARCH="${ARCH:-$(uname -m)}"
case "$ARCH" in
  x86_64|amd64) ARCH=x86_64 ;;
  *) echo "build-vmkernel: $ARCH is not supported yet (x86_64 only)" >&2; exit 1 ;;
esac
series=$(echo "$KERNEL_VERSION" | cut -d. -f1-2)
here="$(cd "$(dirname "$0")" && pwd)"

mkdir -p "$DEST"
DEST="$(cd "$DEST" && pwd)"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT
cp "$here/vmkernel/xbin.config" "$ctx/xbin.config"

# The build proper, run inside the container or on the host. Arguments:
# <workdir> <out-dir>.
cat > "$ctx/build.sh" <<SCRIPT
set -eu
cd "\$1"
curl -fsSL -o linux.tar.xz "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz"
echo "${KERNEL_SHA256}  linux.tar.xz" | sha256sum -c -
curl -fsSL -o base.config "https://raw.githubusercontent.com/firecracker-microvm/firecracker/${FC_TAG}/resources/guest_configs/microvm-kernel-ci-${ARCH}-${series}.config"
echo "${FC_CONFIG_SHA256}  base.config" | sha256sum -c -
tar xf linux.tar.xz
cd "linux-${KERNEL_VERSION}"
cp ../base.config .config
./scripts/kconfig/merge_config.sh -m .config ../xbin.config >/dev/null
make olddefconfig >/dev/null
grep '^CONFIG_' ../xbin.config | while IFS= read -r want; do
  grep -qx "\$want" .config || { echo "fragment option dropped: \$want" >&2; exit 1; }
done
make -j"\$(nproc)" vmlinux >/dev/null
cp vmlinux "\$2/vmlinux"
SCRIPT

if [ "${NATIVE:-0}" = 1 ]; then
  echo ">> building guest kernel ${KERNEL_VERSION} natively"
  sh "$ctx/build.sh" "$ctx" "$DEST"
else
  cat > "$ctx/Dockerfile" <<DOCKERFILE
FROM docker.io/library/debian:trixie-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends \\
    build-essential flex bison bc libelf-dev libssl-dev curl ca-certificates xz-utils python3 cpio \\
 && rm -rf /var/lib/apt/lists/*
COPY build.sh xbin.config /work/
RUN mkdir /out && sh /work/build.sh /work /out
FROM scratch AS out
COPY --from=build /out/vmlinux /vmlinux
DOCKERFILE
  echo ">> building guest kernel ${KERNEL_VERSION} in $DOCKER (~5-15 min, cached after)"
  DOCKER_BUILDKIT=1 "$DOCKER" build ${PLATFORM:+--platform "$PLATFORM"} -f "$ctx/Dockerfile" --target out \
      -o "type=local,dest=$DEST" "$ctx"
fi
echo ">> built $DEST/vmlinux ($(du -h "$DEST/vmlinux" | cut -f1))"
