#!/bin/sh
# Inode-reuse cache-poisoning regression (D44, fixed in v0.3.11). Runs INSIDE
# `unshare --user --map-root-user --mount`.
#
# Backing filesystems like ext4/xfs hand a freed inode number to the next
# created file. The single-tenant per-inode caches must not let the next
# occupant wear the dead file's identity:
#   A. a fresh symlink at a reused inode must NOT stat as the deleted regular
#      file (that broke update-alternatives links: `which: Permission denied`,
#      dpkg exit 126 mid-apt-get);
#   B. a fresh binary at a reused inode must NOT inherit the deleted file's
#      security.capability (phantom caps also fail execve in a userns).
#
# CI hosts (and dev btrfs boxes) never reuse inode numbers, so reuse is
# simulated deterministically: reusefs is a FUSE passthrough that recycles
# its inode numbers LIFO on unlink; the gocryptfs cipher dir lives on it.
#
# Usage: poison.sh WORKDIR GOCRYPTFS REUSEFS TOOL
set -eu
T=$1; G=$2; REUSEFS=$3; TOOL=$4
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$T/back" "$T/cview" "$T/mnt"
"$REUSEFS" "$T/back" "$T/cview" &
FSPID=$!
# No pid ns here — an orphaned reusefs would outlive the test on the host.
trap 'kill $FSPID 2>/dev/null' EXIT
i=0
until grep -q "$T/cview" /proc/mounts; do
	[ $i -gt 100 ] && fail "reusefs did not mount"
	sleep 0.1; i=$((i + 1))
done
echo pw | "$G" -init -q -passfile /dev/stdin "$T/cview" || fail init
echo pw | "$G" -q -passfile /dev/stdin -xbin-single-tenant "$T/cview" "$T/mnt" || fail mount
M=$T/mnt

echo "--- A: symlink over a reused inode"
cp "$TOOL" "$M/target-prog"
chmod 755 "$M/target-prog"
echo data > "$M/f1"
chmod 644 "$M/f1"
I1=$(stat -c %i "$M/f1") # stat populates the identity cache
rm "$M/f1"
ln -s target-prog "$M/lnk"
I2=$(stat -c %i "$M/lnk")
[ "$I1" = "$I2" ] || fail "inode not reused ($I1 vs $I2) — reusefs broken, test void"
sleep 1.5 # kernel attr/entry caches out — force a daemon GETATTR
[ "$(stat -c %F "$M/lnk")" = "symbolic link" ] || fail "symlink poisoned: stat says '$(stat -c '%F %a' "$M/lnk")'"
[ "$(readlink "$M/lnk")" = "target-prog" ] || fail "readlink broken on reused inode"
out=$("$M/lnk") || fail "exec through symlink at reused inode: $out"
[ "$out" = "EXEC-OK" ] || fail "exec output: $out"

echo "--- B: fresh binary over a cap-carrying reused inode"
echo capfile > "$M/c1"
# VFS_CAP_REVISION_2 blob (cap_net_raw permitted) — planted, then the file dies
setfattr -n security.capability -v 0x0100000200200000000000000000000000000000 "$M/c1" || fail setcap
getfattr -n security.capability "$M/c1" > /dev/null 2>&1 # populate the cap cache
I3=$(stat -c %i "$M/c1")
rm "$M/c1"
cp "$TOOL" "$M/prog" || fail "cp onto reused inode (stale cache broke the write path)"
chmod 755 "$M/prog"
I4=$(stat -c %i "$M/prog")
[ "$I3" = "$I4" ] || fail "inode not reused ($I3 vs $I4) — reusefs broken, test void"
sleep 1.5
cap=$(getfattr --only-values -n security.capability "$M/prog" 2>/dev/null | od -An -tx1 | tr -d ' \n')
[ -z "$cap" ] || fail "phantom security.capability on fresh binary: $cap"
out=$("$M/prog") || fail "exec of fresh binary at reused inode"
[ "$out" = "EXEC-OK" ] || fail "exec output: $out"

umount "$M" 2>/dev/null || fusermount3 -uz "$M" || fail umount
echo "POISON-TESTS-PASSED"
