#!/bin/sh
# Builds a STATIC, minimal qemu-system-x86_64 for VM sandboxes on hosts
# without KVM (plans/vm-sandbox.md §Emulated VMs): software emulation (TCG)
# of the microvm machine with only the devices a VM sandbox uses — virtio-mmio
# block, net, balloon, vhost-user-vsock, the ISA serial console. No PCI
# devices, no display, no user-mode networking, no KVM. xbind runs it inside
# the namespace sandbox in place of Firecracker when /dev/kvm is unusable.
# The microvm board boots through two blobs from the same (sha256-pinned)
# tarball — qboot and the PVH option ROM — which travel next to the binary.
#
#   hack/build-qemu.sh [dest-dir]      # default: ./bin → bin/qemu-system-x86_64,
#                                      #   bin/qemu-bios-microvm.bin, bin/qemu-pvh.bin
set -eu

DEST="${1:-./bin}"
VERSION="${QEMU_VERSION:-11.1.1}"
SHA256="${QEMU_SHA256:-079ffbff8a7111bbc89022107cbabf3bbfd614d5fc9d7cc675991196aca12482}"
DOCKER="${DOCKER:-docker}"
ARCH="${ARCH:-$(uname -m)}"
case "$ARCH" in
  x86_64|amd64) ;;
  *) echo "build-qemu: $ARCH is not supported yet (x86_64 only)" >&2; exit 1 ;;
esac

mkdir -p "$DEST"
DEST="$(cd "$DEST" && pwd)"
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

# The device set: the microvm board selects its own buses, APIC, RTC and
# serial; the rest is exactly what the shim puts on the command line.
cat > "$ctx/xbin.mak" <<'MAK'
CONFIG_MICROVM=y
CONFIG_VIRTIO_BLK=y
CONFIG_VIRTIO_NET=y
CONFIG_VIRTIO_BALLOON=y
CONFIG_VHOST_USER_VSOCK=y
MAK

cat > "$ctx/Dockerfile" <<DOCKERFILE
FROM docker.io/library/alpine:3.22 AS build
RUN apk add --no-cache build-base python3 py3-setuptools ninja meson pkgconf bash perl flex bison \\
    linux-headers glib-dev glib-static pcre2-static libffi-dev zlib-dev zlib-static \\
    libseccomp-dev libseccomp-static curl xz git
RUN curl -fsSL -o /qemu.tar.xz https://download.qemu.org/qemu-${VERSION}.tar.xz \\
 && echo "${SHA256}  /qemu.tar.xz" | sha256sum -c - \\
 && tar -C / -xf /qemu.tar.xz && mv /qemu-${VERSION} /src
COPY xbin.mak /src/configs/devices/x86_64-softmmu/xbin.mak
RUN cd /src && ./configure --static --target-list=x86_64-softmmu \\
      --without-default-features --without-default-devices --with-devices-x86_64=xbin \\
      --enable-tcg --disable-kvm --enable-fdt=internal --enable-vhost-user --enable-seccomp \\
      --disable-docs --disable-tools --disable-install-blobs --disable-debug-info \\
 && make -j"\$(nproc)" qemu-system-x86_64 \\
 && strip build/qemu-system-x86_64
FROM scratch AS out
COPY --from=build /src/build/qemu-system-x86_64 /qemu-system-x86_64
COPY --from=build /src/pc-bios/bios-microvm.bin /qemu-bios-microvm.bin
COPY --from=build /src/pc-bios/pvh.bin /qemu-pvh.bin
DOCKERFILE
echo ">> building static qemu-system-x86_64 ${VERSION} (TCG, microvm only; needs $DOCKER; cached after)"
DOCKER_BUILDKIT=1 "$DOCKER" build ${PLATFORM:+--platform "$PLATFORM"} -f "$ctx/Dockerfile" --target out \
    -o "type=local,dest=$DEST" "$ctx"
chmod +x "$DEST/qemu-system-x86_64"
"$DEST/qemu-system-x86_64" --version 2>&1 | head -1
