# shellcheck shell=bash
# hack/helpers-lib.sh — the native helper groups, their keys and the
# manifest (docs/maintenance.md → "Prebuilt helpers"). Sourced by
# hack/fetch-helpers.sh, hack/publish-helpers.sh and hack/check-pins.sh.
#
# A GROUP is a set of helper binaries built by the same hack/build-*.sh
# scripts. Its KEY is a short sha256 of everything that decides what those
# scripts produce: the scripts themselves (their pinned versions and
# checksums live inside them), the patches and config they read, and the
# group's file list. Change any input and the key changes, so a prebuilt
# set is never used for inputs it wasn't built from.
#
# A published set is one object in the helpers bucket,
# <url>/<prefix><group>/<key>/<arch>.tar.zst, and hack/helpers.sha256 (committed)
# pins its sha256 and every file's inside it:
#   <group> <key> <arch> <file> <sha256>
# where <file> is <arch>.tar.zst (the object) or one of the group's files.

HELPERS_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HELPERS_MANIFEST="$HELPERS_ROOT/hack/helpers.sha256"
HELPERS_GROUPS="containerfs vm"
# Part of every key: bump when the tarball layout or the key scheme changes.
HELPERS_FORMAT=1
# Where `make helpers` downloads from: the helpers bucket's public read URL
# (its ROOT — object <k> is served at <url>/<k>, plain HTTPS GET, no
# credentials) and the key prefix inside it (empty, or ending in /): the
# XBIN_HELPERS_URL and XBIN_HELPERS_S3_PREFIX of s3secret.env.example.
# Those env vars override them.
# OWNER: fill both in once the bucket exists. While the URL is empty a
# manifest entry cannot be fetched, and `make helpers` fails saying so.
HELPERS_URL_DEFAULT=""
HELPERS_PREFIX_DEFAULT=""

helpers_files() {
  case "$1" in
    containerfs) echo "gocryptfs fuse-overlayfs" ;;
    vm) echo "vmlinux mkfs.erofs qemu-system-x86_64 qemu-bios-microvm.bin qemu-pvh.bin vhost-device-vsock" ;;
    *) return 1 ;;
  esac
}

# The build scripts, in order; each takes the destination dir.
helpers_scripts() {
  case "$1" in
    containerfs) echo "build-gocryptfs.sh build-fuse-overlayfs.sh" ;;
    vm) echo "build-vmkernel.sh build-mkfs-erofs.sh build-qemu.sh build-vhost-vsock.sh" ;;
  esac
}

# The arches a group builds for (vm: the guest kernel config and QEMU's
# target are x86_64's).
helpers_arches() {
  case "$1" in
    containerfs) echo "amd64 arm64" ;;
    vm) echo "amd64" ;;
  esac
}

# Env vars the scripts read that change what they build: when one is set,
# the prebuilt set (built without it) doesn't apply.
helpers_overrides() {
  case "$1" in
    containerfs) echo "GOCRYPTFS_VERSION GOFUSE_VERSION FUSE_OVERLAYFS_VERSION" ;;
    vm) echo "KERNEL_VERSION KERNEL_SHA256 FC_TAG FC_CONFIG_SHA256 EROFS_UTILS_VERSION QEMU_VERSION QEMU_SHA256 VHOST_VSOCK_VERSION NATIVE" ;;
  esac
}

# The build inputs, repo-relative.
helpers_inputs() {
  local s f
  for s in $(helpers_scripts "$1"); do echo "hack/$s"; done
  case "$1" in
    containerfs)
      for f in "$HELPERS_ROOT"/hack/gocryptfs-patches/*.patch "$HELPERS_ROOT"/hack/gofuse-patches/*.patch; do
        [ -e "$f" ] && echo "${f#"$HELPERS_ROOT"/}"
      done ;;
    vm) echo hack/vmkernel/xbin.config ;;
  esac
}

helpers_key() {
  local g="$1" list
  list=$(cd "$HELPERS_ROOT" && helpers_inputs "$g" | LC_ALL=C sort | xargs -d '\n' sha256sum --) \
    || { echo "helpers: cannot hash the $g build inputs" >&2; return 1; }
  printf 'xbin-helpers/%s %s: %s\n%s\n' "$HELPERS_FORMAT" "$g" "$(helpers_files "$g")" "$list" \
    | sha256sum | cut -c1-12
}

# amd64 | arm64: PLATFORM's (the build scripts' cross-build knob), else the host's.
helpers_arch() {
  local a="${PLATFORM:-}"
  if [ -n "$a" ]; then a=${a#*/}; a=${a%%/*}; else a=$(uname -m); fi
  case "$a" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) echo "$a" ;;
  esac
}

helpers_arch_ok() { [[ " $(helpers_arches "$1") " == *" $2 "* ]]; }

# <group> <key> <arch> → the object's key in the bucket, minus the prefix.
helpers_object() { echo "$1/$2/$3.tar.zst"; }

helpers_base_url() { local u="${XBIN_HELPERS_URL:-$HELPERS_URL_DEFAULT}"; echo "${u%/}"; }
helpers_prefix() {
  local p="${XBIN_HELPERS_S3_PREFIX-$HELPERS_PREFIX_DEFAULT}"
  case $p in '' | */) ;; *) p="$p/" ;; esac
  echo "$p"
}

helpers_mode() { case "$1" in *.bin) echo 0644 ;; *) echo 0755 ;; esac; }

# "<file> <sha256>" lines the manifest has for <group> <key> <arch>.
helpers_manifest_get() {
  [ -f "$HELPERS_MANIFEST" ] || return 0
  awk -v g="$1" -v k="$2" -v a="$3" '$1 !~ /^#/ && NF && $1 == g && $2 == k && $3 == a { print $4, $5 }' "$HELPERS_MANIFEST"
}

# sha256 of <file> in helpers_manifest_get output.
helpers_sha_of() { printf '%s\n' "$1" | awk -v f="$2" '$1 == f { print $2; exit }'; }

# The manifest pins <group> <key> <arch>: the object and every file.
helpers_published() {
  local lines f
  lines=$(helpers_manifest_get "$1" "$2" "$3")
  for f in "$3.tar.zst" $(helpers_files "$1"); do
    [ -n "$(helpers_sha_of "$lines" "$f")" ] || return 1
  done
}

# Syntax of the manifest; prints each problem, returns 1 on any.
helpers_manifest_check() {
  local n=0 bad=0 g k a f s extra why seen=" "
  [ -f "$HELPERS_MANIFEST" ] || { echo "hack/helpers.sha256 missing"; return 1; }
  while read -r g k a f s extra || [ -n "$g" ]; do
    n=$((n + 1))
    case "$g" in ''|'#'*) continue ;; esac
    why=""
    if [ -z "$s" ] || [ -n "$extra" ]; then why="want 5 fields: <group> <key> <arch> <file> <sha256>"
    elif ! helpers_files "$g" >/dev/null; then why="unknown group '$g' (have: $HELPERS_GROUPS)"
    elif ! [[ $k =~ ^[0-9a-f]{12}$ ]]; then why="key '$k' is not 12 hex digits"
    elif ! helpers_arch_ok "$g" "$a"; then why="$g has no arch '$a' (have: $(helpers_arches "$g"))"
    elif ! [[ $s =~ ^[0-9a-f]{64}$ ]]; then why="'$s' is not a sha256"
    elif [[ " $(helpers_files "$g") $a.tar.zst " != *" $f "* ]]; then why="'$f' is not a $g file"
    elif [[ $seen == *" $g/$k/$a/$f "* ]]; then why="duplicate entry for $g $k $a $f"
    fi
    seen="$seen$g/$k/$a/$f "
    [ -z "$why" ] || { echo "hack/helpers.sha256:$n: $why"; bad=1; }
  done < "$HELPERS_MANIFEST"
  return "$bad"
}

# Replace the manifest's <group> <arch> entries (whatever their key) with
# <lines> (full manifest lines); comments stay on top, entries sorted.
helpers_manifest_put() {
  local g="$1" a="$2" lines="$3" tmp
  tmp=$(mktemp "$HELPERS_MANIFEST.XXXXXX")
  {
    grep -E '^[[:space:]]*(#|$)' "$HELPERS_MANIFEST" || true
    { awk -v g="$g" -v a="$a" '$1 !~ /^#/ && NF && !($1 == g && $3 == a)' "$HELPERS_MANIFEST"
      printf '%s\n' "$lines"; } | LC_ALL=C sort
  } > "$tmp"
  mv -f "$tmp" "$HELPERS_MANIFEST"
}

# Build <group> from source into <dir> with its hack/build-*.sh scripts.
helpers_build() {
  local g="$1" dir="$2" s f
  mkdir -p "$dir"
  for s in $(helpers_scripts "$g"); do
    "$HELPERS_ROOT/hack/$s" "$dir"
  done
  for f in $(helpers_files "$g"); do
    [ -f "$dir/$f" ] || { echo "helpers: building $g produced no $f" >&2; return 1; }
  done
}

# Unpack <group>'s files from <tarball> into <dir> (only those names; each
# must be a regular file).
helpers_unpack() {
  local g="$1" tarball="$2" dir="$3" f
  mkdir -p "$dir"
  # shellcheck disable=SC2046 # the file names are fixed words
  zstd -dcq "$tarball" | tar -x -C "$dir" --no-same-owner -f - -- $(helpers_files "$g") \
    || { echo "helpers: $tarball does not hold every $g file ($(helpers_files "$g"))" >&2; return 1; }
  for f in $(helpers_files "$g"); do
    if [ -L "$dir/$f" ] || [ ! -f "$dir/$f" ]; then
      echo "helpers: $f in $tarball is not a regular file" >&2; return 1
    fi
  done
}

# Manifest lines for <group> <key> <arch> from a packed <tarball>.
helpers_hash_tarball() {
  local g="$1" k="$2" a="$3" tarball="$4" tmp f
  tmp=$(mktemp -d)
  helpers_unpack "$g" "$tarball" "$tmp" || { rm -rf "$tmp"; return 1; }
  echo "$g $k $a $a.tar.zst $(sha256sum < "$tarball" | cut -d' ' -f1)"
  for f in $(helpers_files "$g"); do
    echo "$g $k $a $f $(sha256sum < "$tmp/$f" | cut -d' ' -f1)"
  done
  rm -rf "$tmp"
}

# Pack <group>'s files in <dir> into <tarball> (fixed order/owner/mtime).
helpers_pack() {
  local g="$1" dir="$2" tarball="$3"
  mkdir -p "$(dirname "$tarball")"
  # shellcheck disable=SC2046 # the file names are fixed words
  tar -C "$dir" --sort=name --owner=0 --group=0 --numeric-owner --mtime=@0 -cf - -- $(helpers_files "$g") \
    | zstd -q -19 -T0 -f -o "$tarball"
}
