#!/usr/bin/env bash
# hack/helpers-static.sh — publish a staged set of prebuilt helpers as static
# files on the website (for now: https://xbin.dev/static/helpers, the default
# URL in hack/helpers-lib.sh; docs/maintenance.md → "Prebuilt helpers"):
#
#   hack/publish-helpers.sh --stage-only DIR     # build + pack (docker)
#   hack/helpers-static.sh DIR                   # this: check, copy, manifest
#   make website                                 # → website/dist/static/helpers/
#   (deploy website/dist by hand; then commit hack/helpers.sha256)
#
# It checks every staged tarball against DIR/helpers.sha256, copies the
# tarballs to website/static-helpers/<group>/<key>/<arch>.tar.zst (gitignored:
# binaries never go into git), and merges DIR/helpers.sha256 into
# hack/helpers.sha256 (a group+key+arch already there is replaced). Commit
# the manifest only once the deployed site serves the files — until then
# `make helpers` would fall back to building from source anyway.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
dir=${1:?usage: hack/helpers-static.sh STAGED-DIR (from publish-helpers.sh --stage-only)}
dir=$(cd "$dir" && pwd)
man=$repo/hack/helpers.sha256
out=$repo/website/static-helpers
[ -s "$dir/helpers.sha256" ] || { echo "helpers-static: no $dir/helpers.sha256 (stage with publish-helpers.sh --stage-only)" >&2; exit 1; }

# Every tarball the staged manifest names is there, with its hash.
n=0
while read -r group key arch file sum; do
  case $group in '' | '#'*) continue ;; esac
  [ "$file" = "$arch.tar.zst" ] || continue
  f="$dir/$group/$key/$file"
  [ -f "$f" ] || { echo "helpers-static: $group/$key/$file named in the staged manifest but missing" >&2; exit 1; }
  got=$(sha256sum "$f" | cut -d' ' -f1)
  [ "$got" = "$sum" ] || { echo "helpers-static: $group/$key/$file: sha256 $got, staged manifest says $sum" >&2; exit 1; }
  mkdir -p "$out/$group/$key"
  cp "$f" "$out/$group/$key/$file"
  echo ">> website/static-helpers/$group/$key/$file ($(du -h "$f" | cut -f1))"
  n=$((n + 1))
done <"$dir/helpers.sha256"
[ "$n" -gt 0 ] || { echo "helpers-static: the staged manifest names no tarball" >&2; exit 1; }

# The manifest: the header and every other entry kept, the staged ones in.
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
{
  grep '^#' "$man" || true
  awk 'NR==FNR { if ($0 !~ /^#/ && NF==5) staged[$1" "$2" "$3]=1; next }
       $0 !~ /^#/ && NF==5 && !(($1" "$2" "$3) in staged)' "$dir/helpers.sha256" "$man"
  grep -v '^#' "$dir/helpers.sha256" | awk 'NF==5'
} >"$tmp"
cp "$tmp" "$man"
echo ">> hack/helpers.sha256: $(grep -vc '^#' "$man") entries — commit it once the site serves the files"
