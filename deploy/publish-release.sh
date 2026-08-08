#!/usr/bin/env bash
# Build and publish prebuilt xbin bundles for a release tag.
#
# A bundle is everything an install needs with NO build on the target: the
# four native binaries + the unpacked base rootfs + the SDK, one tarball per
# architecture. `install.sh --prebuilt-rootfs` downloads the manifest, picks
# the arch variant, verifies its sha256, and unpacks it — no podman, no Go, no
# docker, no multi-GB rootfs build on the box.
#
#   deploy/publish-release.sh [--tag vX.Y.Z] [--arch amd64,arm64]
#                             [--no-upload] [--out DIR]
#
# Produces, under --out (default ./dist):
#   xbin-<tag>-linux-<arch>.tar.zst        (bin/ + rootfs/ + sdk/)
#   release-manifest.json                  (version, baseVersion, variants[])
# and, unless --no-upload, uploads them as GitHub Release assets on <tag>.
#
# Native arch builds directly (fast). A foreign arch cross-builds: pure-Go
# binaries via GOARCH (no emulation), the rootfs + fuse-overlayfs via
# `--platform` and the engine's qemu (SLOW — register binfmt first, or run
# this on native hardware of that arch). Needs: podman|docker, git, zstd,
# sha256sum, and (to upload) an authenticated `gh`.
set -euo pipefail

TAG=""
ARCHES="amd64,arm64"
UPLOAD=1
OUTDIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) TAG="$2"; shift 2 ;;
    --tag=*) TAG="${1#*=}"; shift ;;
    --arch) ARCHES="$2"; shift 2 ;;
    --arch=*) ARCHES="${1#*=}"; shift ;;
    --no-upload) UPLOAD=0; shift ;;
    --out) OUTDIR="$2"; shift 2 ;;
    --out=*) OUTDIR="${1#*=}"; shift ;;
    -h|--help) sed -n '2,21p' "$0"; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; exit 1 ;;
  esac
done

die() { echo "error: $*" >&2; exit 1; }
info() { echo ">> $*" >&2; }

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
OUTDIR="${OUTDIR:-$repo/dist}"

# Tag: explicit, else the tag pointing at HEAD (publishing = tag then publish).
if [ -z "$TAG" ]; then
  TAG=$(git describe --tags --exact-match HEAD 2>/dev/null) \
    || die "HEAD is not tagged — pass --tag vX.Y.Z (tag the release first)"
fi
case "$TAG" in v[0-9]*) ;; *) die "tag must look like vX.Y.Z (got: $TAG)";; esac

ENGINE="${DOCKER:-}"
[ -n "$ENGINE" ] || { command -v podman >/dev/null && ENGINE=podman || ENGINE=docker; }
command -v "$ENGINE" >/dev/null || die "no container engine ($ENGINE) — install podman or docker"
command -v zstd >/dev/null || die "zstd not found (needed to compress bundles)"
command -v sha256sum >/dev/null || die "sha256sum not found"

host_arch=$(dpkg --print-architecture 2>/dev/null || case "$(uname -m)" in
  x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) echo "$(uname -m)" ;;
esac)

# Ensure the engine can run a foreign platform (qemu/binfmt). Best-effort
# auto-register; fatal for that arch if it still can't.
ensure_platform() {
  local arch="$1"
  "$ENGINE" run --rm --platform "linux/$arch" docker.io/library/alpine:3.20 true 2>/dev/null && return 0
  info "registering qemu/binfmt for linux/$arch (foreign-arch cross-build)"
  "$ENGINE" run --rm --privileged docker.io/tonistiigi/binfmt --install "$arch" >/dev/null 2>&1 || true
  "$ENGINE" run --rm --platform "linux/$arch" docker.io/library/alpine:3.20 true 2>/dev/null \
    || die "cannot run linux/$arch under $ENGINE — install qemu-user-static/binfmt, or run this on native $arch hardware"
}

mkdir -p "$OUTDIR"
VARIANTS_TSV=""   # arch<TAB>file<TAB>sha256<TAB>size, one per line
BASE_VERSION=""

build_arch() {
  local arch="$1"
  local bdir stage out sha size
  bdir=$(mktemp -d)
  stage="$bdir/stage"
  mkdir -p "$stage/bin"

  info "[$arch] building binaries + rootfs (${arch} == $host_arch native? $([ "$arch" = "$host_arch" ] && echo yes || echo NO — qemu cross))"
  # Force a clean per-arch build: make's file targets would otherwise reuse
  # the previous arch's bin/gocryptfs etc.
  rm -f "$repo/bin/xbind" "$repo/bin/bx" "$repo/bin/gocryptfs" "$repo/bin/fuse-overlayfs"

  local env_pre=(DOCKER="$ENGINE")
  if [ "$arch" != "$host_arch" ]; then
    ensure_platform "$arch"
    env_pre+=(PLATFORM="linux/$arch" GOOS=linux GOARCH="$arch")
  fi
  env "${env_pre[@]}" make -C "$repo" build
  env "${env_pre[@]}" make -C "$repo" rootfs ROOTFS="$bdir/rootfs"

  local b
  for b in xbind bx gocryptfs fuse-overlayfs; do
    [ -f "$repo/bin/$b" ] || die "[$arch] build produced no bin/$b"
    cp "$repo/bin/$b" "$stage/bin/$b"
  done
  mv "$bdir/rootfs" "$stage/rootfs"
  cp -a "$repo/sdk" "$stage/sdk"

  [ -n "$BASE_VERSION" ] || BASE_VERSION=$(cat "$stage/rootfs/etc/xbin-base-version" 2>/dev/null || echo unknown)

  out="$OUTDIR/xbin-$TAG-linux-$arch.tar.zst"
  info "[$arch] packing $out"
  tar -C "$stage" -cf - bin rootfs sdk | zstd -T0 -6 -q -f -o "$out"
  sha=$(sha256sum "$out" | cut -d' ' -f1)
  size=$(stat -c %s "$out" 2>/dev/null || wc -c <"$out")
  VARIANTS_TSV="${VARIANTS_TSV}${arch}	$(basename "$out")	${sha}	${size}
"
  # Best-effort cleanup: the staged rootfs carries a read-only Go module cache
  # (/root/go/pkg/mod, mode 0444) that plain rm refuses — chmod first, never fatal.
  chmod -R u+w "$bdir" 2>/dev/null || true
  rm -rf "$bdir" 2>/dev/null || true
  info "[$arch] done: $(basename "$out") ($((size/1024/1024)) MiB, sha256 ${sha:0:12}…)"
}

IFS=',' read -r -a arch_list <<<"$ARCHES"
for a in "${arch_list[@]}"; do build_arch "$a"; done

# --- manifest (no jq: emit via python3, which every publisher host has) ------
MANIFEST="$OUTDIR/release-manifest.json"
VARIANTS_TSV="$VARIANTS_TSV" TAG="$TAG" BASE_VERSION="$BASE_VERSION" python3 - "$MANIFEST" <<'PY'
import json, os, sys
rows = [l for l in os.environ["VARIANTS_TSV"].splitlines() if l.strip()]
variants = []
for l in rows:
    arch, file, sha, size = l.split("\t")
    variants.append({"arch": arch, "flavor": "full", "file": file,
                     "sha256": sha, "size": int(size)})
doc = {"version": os.environ["TAG"], "baseVersion": os.environ["BASE_VERSION"],
       "variants": variants}
open(sys.argv[1], "w").write(json.dumps(doc, indent=2) + "\n")
PY
info "wrote $MANIFEST"

if [ "$UPLOAD" = 1 ]; then
  command -v gh >/dev/null || die "gh not found — install it or pass --no-upload"
  gh auth status >/dev/null 2>&1 || die "gh is not authenticated (gh auth login) — or pass --no-upload"
  gh release view "$TAG" >/dev/null 2>&1 \
    || gh release create "$TAG" --title "$TAG" --notes "xbin $TAG — prebuilt install bundles (see release-manifest.json)."
  # shellcheck disable=SC2046
  gh release upload "$TAG" "$MANIFEST" $(ls "$OUTDIR"/xbin-"$TAG"-linux-*.tar.zst) --clobber
  info "uploaded manifest + $(echo "$ARCHES" | tr ',' ' ') bundle(s) to release $TAG"
else
  info "artifacts in $OUTDIR (not uploaded; --no-upload)"
fi
