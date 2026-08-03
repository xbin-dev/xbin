#!/bin/sh
# The container-store stack as podman runs it: fuse-overlayfs (lower/upper/
# work all on a single-tenant gocryptfs mount) with a base layer extracted
# podman-style, then dpkg's unpack syscall sequence into the merged dir and
# exec of everything after the kernel attr caches expire. Runs INSIDE
# `unshare --user --map-root-user --mount`.
#
# Usage: overlay.sh WORKDIR GOCRYPTFS FUSE_OVERLAYFS TOOL DPKGSIM
set -eu
T=$1; G=$2; FO=$3; TOOL=$4; DPKGSIM=$5
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$T/cipher" "$T/mnt" "$T/merged"
echo pw | "$G" -init -q -passfile /dev/stdin "$T/cipher" || fail init
echo pw | "$G" -q -passfile /dev/stdin -xbin-single-tenant "$T/cipher" "$T/mnt" || fail mount

mkdir -p "$T/mnt/lower/bin" "$T/mnt/upper" "$T/mnt/work"
# base image layer: podman-style extract (write content, then path-chmod)
cp "$TOOL" "$T/mnt/lower/bin/tool-base"
chmod 0755 "$T/mnt/lower/bin/tool-base"

"$FO" -o lowerdir="$T/mnt/lower",upperdir="$T/mnt/upper",workdir="$T/mnt/work" "$T/merged" || fail "overlay mount"

"$DPKGSIM" "$T/merged" "$TOOL" 40 || fail "dpkg-sequence exec through overlay"

# base-layer binary still execs through the merged view after the churn
out=$("$T/merged/bin/tool-base") || fail "base-layer exec after churn"
[ "$out" = "EXEC-OK" ] || fail "base-layer exec output: $out"

# Teardown: the mount ns dies with this unshare, so a busy unmount (the
# fuse-overlayfs daemon winding down) is not a failure — detach lazily.
umount "$T/merged" 2>/dev/null || fusermount3 -uz "$T/merged" || fail "umount merged"
umount "$T/mnt" 2>/dev/null || fusermount3 -uz "$T/mnt" || fail "umount store"
echo "OVERLAY-TESTS-PASSED"
