#!/usr/bin/env bash
# hack/fetch-helpers.sh — put the native helper binaries xbind runs into
# bin/ (make helpers): the containerfs group (gocryptfs, fuse-overlayfs) and
# the vm group (vmlinux, mkfs.erofs, qemu-system-x86_64 + its two boot
# blobs, vhost-device-vsock). Groups and keys: hack/helpers-lib.sh; the
# whole scheme: docs/maintenance.md → "Prebuilt helpers".
#
# Per group, for this arch (PLATFORM's, else the host's):
#   - bin/.helpers-<group> says the files there were made from the current
#     key → nothing to do;
#   - hack/helpers.sha256 pins the current key → download
#     <url>/<prefix><group>/<key>/<arch>.tar.zst, check it and every file in it
#     against the manifest (any mismatch is fatal, nothing is installed),
#     install;
#   - otherwise (build inputs changed and nobody published yet, a download
#     failed, or a version override like QEMU_VERSION is set) → build from
#     source with the group's hack/build-*.sh scripts (docker), as the
#     Makefile's own targets do.
#
#   hack/fetch-helpers.sh [--dest DIR] [--build] [group…]   # default: every group, bin/
#     --build    always build from source (make helpers-build)
#     --status   print "<group> <key> <arch> published|unpublished" and exit
#
# Env: XBIN_HELPERS_URL, XBIN_HELPERS_S3_PREFIX (the bucket's public read
# URL and key prefix; defaults in hack/helpers-lib.sh — no credentials,
# s3secret.env is the publisher's), DOCKER and the build scripts' knobs.
# Under GitHub Actions an unpublished key is also a warning annotation.
set -euo pipefail
# shellcheck source=hack/helpers-lib.sh
. "$(dirname "$0")/helpers-lib.sh"

say()  { echo ">> helpers: $*"; }
die()  { echo "helpers: $*" >&2; exit 1; }
annotate() { [ "${GITHUB_ACTIONS:-}" != true ] || echo "::warning title=$1::$2"; }

DEST="$HELPERS_ROOT/bin" MODE=auto
groups=()
while [ $# -gt 0 ]; do
  case "$1" in
    --dest) [ $# -ge 2 ] || die "--dest needs a dir"; DEST=$2; shift ;;
    --build) MODE=build ;;
    --status) MODE=status ;;
    -h|--help) sed -n '2,27p' "$0"; exit 0 ;;
    -*) die "unknown flag $1" ;;
    *) helpers_files "$1" >/dev/null || die "unknown group '$1' (have: $HELPERS_GROUPS)"
       groups+=("$1") ;;
  esac
  shift
done
[ ${#groups[@]} -gt 0 ] || read -r -a groups <<< "$HELPERS_GROUPS"
helpers_manifest_check >&2 || die "hack/helpers.sha256 is malformed"
arch=$(helpers_arch)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

build() { # group key
  say "$1: building from source into $DEST (docker; the vm group takes a while)"
  rm -f "$DEST/.helpers-$1"
  helpers_build "$1" "$DEST"
  echo "$2 $arch built" > "$DEST/.helpers-$1"
  say "$1: built $2 ($arch)"
}

# Download, verify, install; 1 when the download failed (the caller builds).
fetch() { # group key
  local g="$1" key="$2" lines base url want got f t="$work/$1"
  lines=$(helpers_manifest_get "$g" "$key" "$arch")
  base=$(helpers_base_url)
  [ -n "$base" ] || die "$g: hack/helpers.sha256 pins a prebuilt $key ($arch), but no helpers URL is configured — set XBIN_HELPERS_URL (or HELPERS_URL_DEFAULT in hack/helpers-lib.sh), or build from source: make helpers-build"
  command -v zstd >/dev/null || die "$g: unpacking the prebuilt helpers needs zstd (apt install zstd) — or build from source: make helpers-build"
  url="$base/$(helpers_prefix)$(helpers_object "$g" "$key" "$arch")"
  mkdir -p "$t"
  say "$g: fetching prebuilt $key ($arch) from $url"
  if ! curl -fsSL --retry 3 -o "$t/$arch.tar.zst" "$url"; then
    return 1
  fi
  want=$(helpers_sha_of "$lines" "$arch.tar.zst")
  got=$(sha256sum < "$t/$arch.tar.zst" | cut -d' ' -f1)
  [ "$got" = "$want" ] || die "$g: sha256 mismatch for $url: got $got, hack/helpers.sha256 says $want — refusing it (nothing installed)"
  helpers_unpack "$g" "$t/$arch.tar.zst" "$t/x" || die "$g: $url is not a $g set — refusing it (nothing installed)"
  for f in $(helpers_files "$g"); do
    want=$(helpers_sha_of "$lines" "$f")
    got=$(sha256sum < "$t/x/$f" | cut -d' ' -f1)
    [ "$got" = "$want" ] || die "$g: sha256 mismatch for $f in $url: got $got, hack/helpers.sha256 says $want — refusing it (nothing installed)"
  done
  rm -f "$DEST/.helpers-$g"
  for f in $(helpers_files "$g"); do
    install -m "$(helpers_mode "$f")" "$t/x/$f" "$DEST/.$f.new"
    mv -f "$DEST/.$f.new" "$DEST/$f"
  done
  echo "$key $arch fetched" > "$DEST/.helpers-$g"
  say "$g: installed prebuilt $key ($arch), every file verified"
}

current() { # group key: bin/ holds that group's files, made from key
  local f
  [ "$(cut -d' ' -f1-2 "$DEST/.helpers-$1" 2>/dev/null)" = "$2 $arch" ] || return 1
  for f in $(helpers_files "$1"); do [ -f "$DEST/$f" ] || return 1; done
}

[ "$MODE" = status ] || { mkdir -p "$DEST"; DEST=$(cd "$DEST" && pwd); }

for g in "${groups[@]}"; do
  if ! helpers_arch_ok "$g" "$arch"; then
    [ "$MODE" = status ] || say "$g: not built for $arch (only $(helpers_arches "$g")) — skipped"
    continue
  fi
  key=$(helpers_key "$g")
  published=0
  if helpers_published "$g" "$key" "$arch"; then published=1; fi
  if [ "$MODE" = status ]; then
    echo "$g $key $arch $([ "$published" = 1 ] && echo published || echo unpublished)"
    continue
  fi
  if [ "$published" = 0 ]; then
    msg="$g helpers for build-input key $key ($arch) are not in hack/helpers.sha256 — building from source; a maintainer publishes them with: make helpers-publish"
    annotate "unpublished helpers" "$msg"
  fi
  if [ "$MODE" = auto ] && current "$g" "$key"; then
    say "$g: up to date ($key, $(cut -d' ' -f3 "$DEST/.helpers-$g"))"
    continue
  fi
  if [ "$MODE" = build ]; then build "$g" "$key"; continue; fi
  set_vars=""
  for v in $(helpers_overrides "$g"); do
    if [ -n "${!v:-}" ]; then set_vars="$set_vars $v"; fi
  done
  if [ -n "$set_vars" ]; then
    say "$g:${set_vars# } set — the prebuilt set doesn't apply"
    build "$g" "$key"
  elif [ "$published" = 0 ]; then
    say "$msg"
    build "$g" "$key"
  elif ! fetch "$g" "$key"; then
    say "$g: downloading the prebuilt $key failed — building from source instead"
    annotate "helpers download failed" "$g: fetching the prebuilt $key ($arch) failed — built from source instead"
    build "$g" "$key"
  fi
done
