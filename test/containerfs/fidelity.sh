#!/bin/sh
# Single-tenant fidelity suite, driven by the containerfs integration test.
# Usage: fidelity.sh WORKDIR GOCRYPTFS
#
# Runs INSIDE `unshare --user --map-root-user --mount` (ns-root, CAP_SYS_ADMIN
# over the nested mount ns → gocryptfs direct-mounts, allow_other permitted,
# mknod/setfcap allowed on the fuse sb).
#
# Verifies the -xbin-single-tenant semantics that made container stores die
# on stock gocryptfs:
#   1. 0555 dir, then create inside        (podman ApplyLayer .pivot_root)
#   2. chmod/chown round-trip via xattr    (layer extraction metadata)
#   3. mknod chr/fifo + whiteout           (device nodes, overlay whiteouts)
#   4. security.capability round-trip      (file caps in layers)
#   5. hardlink shares identity
#   6. rename keeps identity; RENAME_WHITEOUT decomposition (via tar-style flow)
#   7. tar layer round-trip (create + extract + diff)
set -x
T=$1; BIN=$2
fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$T/cipher" "$T/mnt"
echo pw123 | "$BIN" -init -q -passfile /dev/stdin "$T/cipher" || fail init
echo pw123 | "$BIN" -q -passfile /dev/stdin -xbin-single-tenant "$T/cipher" "$T/mnt" || fail mount
mount | grep -q "$T/mnt" || fail "not mounted"
M="$T/mnt"

# ---- 1. the .pivot_root repro: 0555 dir then mkdir/create inside ----
mkdir -m 0555 "$M/layer" || fail "mkdir 0555"
[ "$(stat -c %a "$M/layer")" = "555" ] || fail "0555 not reported: $(stat -c %a "$M/layer")"
mkdir "$M/layer/.pivot_root123" || fail "mkdir inside 0555 dir (THE bug)"
echo hi > "$M/layer/file" || fail "create inside 0555 dir"
rmdir "$M/layer/.pivot_root123" || fail "rmdir inside 0555"

# ---- 2. chmod/chown round-trip ----
chmod 4755 "$M/layer/file" || fail chmod
[ "$(stat -c %a "$M/layer/file")" = "4755" ] || fail "setuid mode lost: $(stat -c %a "$M/layer/file")"
chown 0:0 "$M/layer/file" || fail "chown root:root"
[ "$(stat -c %u:%g "$M/layer/file")" = "0:0" ] || fail "chown lost: $(stat -c %u:%g "$M/layer/file")"
# content still readable after chown/chmod (cipher file untouched)
[ "$(cat "$M/layer/file")" = "hi" ] || fail "content after chown"
# chmod must NOT leak to the cipher tree: cipher files stay 0600/0700
find "$T/cipher" -type f ! -perm 600 ! -name 'gocryptfs.*' | grep . && fail "cipher file not 0600"
find "$T/cipher" -mindepth 1 -type d ! -perm 700 | grep . && fail "cipher dir not 0700"

# ---- 3. special files ----
# Real device nodes (c 1:3): the KERNEL denies mknod in a userns regardless
# of filesystem (init-ns CAP_MKNOD) — parity with plain dirs; rootless
# extraction skips them. Whiteouts (c 0:0) pass the kernel check and must
# round-trip through the FUSE virtualization.
mknod "$M/dev-null" c 1 3 2>/dev/null && fail "kernel unexpectedly allowed real mknod (env changed?)"
mkfifo "$M/fifo1" || fail mkfifo
[ "$(stat -c %F "$M/fifo1")" = "fifo" ] || fail "fifo lost"
mknod "$M/wh" c 0 0 || fail "mknod whiteout (char 0:0)"
[ "$(stat -c '%F %t:%T' "$M/wh")" = "character special file 0:0" ] || fail "whiteout lost"
# fifo I/O is served by the kernel, not FUSE
( echo ping > "$M/fifo1" & ) ; [ "$(head -c4 "$M/fifo1")" = "ping" ] || fail "fifo I/O"

# ---- 4. security.capability ----
setcap cap_net_raw+ep "$M/layer/file" || fail "setcap"
getcap "$M/layer/file" | grep -q cap_net_raw || fail "getcap: $(getcap "$M/layer/file")"
# and survives re-stat / re-open
cp "$M/layer/file" "$T/copy-out" || fail "read file with caps"

# ---- 5. hardlinks share identity ----
# (chown to unmapped uids can't cross this host's single-uid userns — the
# kernel rejects pre-FUSE; real installs map subuid ranges. chmod exercises
# the same shared-identity xattr without needing a mapping.)
ln "$M/layer/file" "$M/hardlink" || fail ln
chmod 0640 "$M/hardlink" || fail "chmod via link"
[ "$(stat -c %a "$M/layer/file")" = "640" ] || fail "hardlink identity not shared"
chmod 4755 "$M/layer/file"
[ "$(stat -c %a "$M/hardlink")" = "4755" ] || fail "hardlink identity not shared (reverse)"

# ---- 6. rename keeps identity ----
mv "$M/layer/file" "$M/layer/file2" || fail mv
[ "$(stat -c %u:%g:%a "$M/layer/file2")" = "0:0:4755" ] || fail "identity lost on rename"
getcap "$M/layer/file2" | grep -q cap_net_raw || fail "caps lost on rename"

# ---- 6b. RENAME_WHITEOUT decomposition (fuse-overlayfs-style) ----
# python os.rename has no flags; use a tiny renameat2 shim
python3 - "$M" <<'PYEOF' || fail "renameat2 RENAME_WHITEOUT"
import ctypes, os, sys
libc = ctypes.CDLL(None, use_errno=True)
m = sys.argv[1]
with open(m + "/wo-src", "w") as f: f.write("data")
RENAME_WHITEOUT = 4
r = libc.renameat2(-100, (m + "/wo-src").encode(), -100, (m + "/wo-dst").encode(), RENAME_WHITEOUT)
if r != 0:
    raise OSError(ctypes.get_errno(), "renameat2")
st = os.lstat(m + "/wo-src")
import stat as stm
assert stm.S_ISCHR(st.st_mode), f"whiteout not chr: {oct(st.st_mode)}"
assert st.st_rdev == 0, f"whiteout rdev: {st.st_rdev}"
assert open(m + "/wo-dst").read() == "data"
print("RENAME_WHITEOUT ok")
PYEOF

# ---- 7. tar layer round-trip (numeric owners, devices, perms) ----
mkdir -p "$T/mklayer/etc" "$T/mklayer/opt"
echo conf > "$T/mklayer/etc/app.conf"
chmod 0400 "$T/mklayer/etc/app.conf"
mkdir -m 0555 "$T/mklayer/opt/ro"
ln -s ../etc/app.conf "$T/mklayer/link"
tar -C "$T/mklayer" -cf "$T/layer.tar" .
mkdir "$M/extract"
tar -C "$M/extract" --same-owner --same-permissions -xf "$T/layer.tar" || fail "tar extract"
[ "$(stat -c %a "$M/extract/etc/app.conf")" = "400" ] || fail "tar perms"
[ "$(stat -c %a "$M/extract/opt/ro")" = "555" ] || fail "tar 0555 dir"
# create inside the extracted read-only dir (ApplyLayer-style)
touch "$M/extract/opt/ro/marker" || fail "create inside extracted 0555"
rm "$M/extract/opt/ro/marker"
# re-tar and compare listings (types+modes survive the store)
( cd "$M/extract" && tar -cf "$T/relayer.tar" . ) || fail "re-tar"
tar -tvf "$T/layer.tar"   | awk '{print $1, $NF}' | sort > "$T/a.lst"
tar -tvf "$T/relayer.tar" | awk '{print $1, $NF}' | sort > "$T/b.lst"
diff "$T/a.lst" "$T/b.lst" || fail "layer round-trip diff"

# ---- 8. deep tree + many files (dircache / prepareAtSyscall paths) ----
mkdir -p "$M/deep/a/b/c/d/e"
for i in $(seq 1 50); do echo x$i > "$M/deep/a/b/c/d/e/f$i"; done
chmod -R 0555 "$M/deep" || fail "chmod -R 0555"
find "$M/deep" -type f | wc -l | grep -q 50 || fail "listing 0555 tree"
chmod -R 0755 "$M/deep" || fail "chmod -R back"
rm -rf "$M/deep" || fail "rm -rf"

# ---- 9. listxattr must not show the identity xattr ----
touch "$M/xtest"; chown 0:0 "$M/xtest"
getfattr -d -m - "$M/xtest" 2>/dev/null | grep -q gocryptfs && fail "identity xattr leaked to listing"
# user xattrs still work
setfattr -n user.hello -v world "$M/xtest" || fail setfattr
[ "$(getfattr --only-values -n user.hello "$M/xtest")" = "world" ] || fail getfattr

umount "$M" || fail umount
# ---- 10. at-rest check: nothing about the identity is plaintext on disk ----
grep -rl '"u":' "$T/cipher" && fail "PLAINTEXT identity metadata on disk!"
# xattrs on cipher files are present but encrypted
echo "ALL-SINGLE-TENANT-TESTS-PASSED"
