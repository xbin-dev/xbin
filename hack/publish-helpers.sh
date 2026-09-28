#!/usr/bin/env bash
# hack/publish-helpers.sh — maintainers only (make helpers-publish): build
# the helper groups from source (docker, the hack/build-*.sh scripts), pack
# each into <prefix><group>/<key>/<arch>.tar.zst, upload it to the helpers
# bucket, check the public URL serves it, and rewrite hack/helpers.sha256 —
# then commit the manifest. Groups and keys: hack/helpers-lib.sh; the
# scheme: docs/maintenance.md → "Prebuilt helpers".
#
#   hack/publish-helpers.sh [--env FILE] [group…]        # default: every group
#   hack/publish-helpers.sh --stage-only DIR [group…]    # build + pack into DIR
#       (<group>/<key>/<arch>.tar.zst, plus DIR/helpers.sha256 with the
#       lines the manifest would get); no upload, the manifest untouched
#   hack/publish-helpers.sh --staged DIR [group…]        # upload what
#       --stage-only left in DIR instead of building
#
# The bucket settings and the write credentials come from s3secret.env at
# the repo root (--env FILE for another; s3secret.env.example documents it,
# hack/check-s3.sh checks it), parsed, never sourced (hack/s3-lib.sh). The
# upload is curl --aws-sigv4 with the credentials on its stdin — never on a
# command line, never in the repo. An object already in the bucket is never
# overwritten (builds aren't byte-reproducible, and a committed manifest may
# pin it): its entries are taken from it instead.
#
# Refuses on a dirty tree (a key must describe committed inputs) and in CI.
set -euo pipefail
# shellcheck source=hack/helpers-lib.sh
. "$(dirname "$0")/helpers-lib.sh"
# shellcheck source=hack/s3-lib.sh
. "$(dirname "$0")/s3-lib.sh"

say() { echo ">> helpers-publish: $*"; }
die() { echo "helpers-publish: $*" >&2; exit 1; }

MODE=upload DIR="" envfile="$HELPERS_ROOT/s3secret.env"
groups=()
while [ $# -gt 0 ]; do
  case "$1" in
    --stage-only|--staged)
      [ $# -ge 2 ] || die "$1 needs a dir"
      MODE=${1#--}; DIR=$2; shift ;;
    --env) [ $# -ge 2 ] || die "--env needs a file"; envfile=$2; shift ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    -*) die "unknown flag $1" ;;
    *) helpers_files "$1" >/dev/null || die "unknown group '$1' (have: $HELPERS_GROUPS)"
       groups+=("$1") ;;
  esac
  shift
done
[ ${#groups[@]} -gt 0 ] || read -r -a groups <<< "$HELPERS_GROUPS"
case "$MODE" in
  stage-only) mkdir -p "$DIR"; DIR=$(cd "$DIR" && pwd); touch "$DIR/helpers.sha256" ;;
  staged) [ -d "$DIR" ] || die "no dir $DIR"; DIR=$(cd "$DIR" && pwd) ;;
esac
case "$envfile" in /*) ;; *) envfile="$PWD/$envfile" ;; esac

[ -z "${CI:-}${GITHUB_ACTIONS:-}" ] || die "publishing runs on a maintainer's machine, never in CI"
cd "$HELPERS_ROOT"
[ -z "$(git status --porcelain)" ] \
  || die "the tree is dirty — commit first: a key must describe committed build inputs (git status)"
helpers_manifest_check >&2 || die "hack/helpers.sha256 is malformed"
command -v zstd >/dev/null || die "needs zstd"
arch=$(helpers_arch)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

if [ "$MODE" != stage-only ]; then
  s3_load "$envfile"
  s3_require "$envfile"
  s3_err="$work/curl.err"
  # make helpers must look where this uploads.
  if [ -z "$HELPERS_URL_DEFAULT" ]; then
    say "note: HELPERS_URL_DEFAULT in hack/helpers-lib.sh is still a placeholder — set it to $s3_public (and HELPERS_PREFIX_DEFAULT to \"$s3_prefix\") and commit it with the manifest, or make helpers can't fetch these"
  elif [ "${HELPERS_URL_DEFAULT%/}" != "$s3_public" ] || [ "$HELPERS_PREFIX_DEFAULT" != "$s3_prefix" ]; then
    die "$envfile says $s3_public/$s3_prefix but hack/helpers-lib.sh has ${HELPERS_URL_DEFAULT%/}/$HELPERS_PREFIX_DEFAULT — make helpers would look elsewhere; make them agree"
  fi
fi

# upload <objkey> <tarball>: put it in the bucket — unless an object is
# there already, which then replaces <tarball> — and check the public URL
# serves those bytes.
upload() {
  local objkey=$1 tarball=$2 api code want got
  api=$(s3_api "$objkey")
  code=$(s3_signed HEAD "$work/head.out" "$api")
  if [ "$code" = 403 ]; then # no list right: a missing key reads as 403; ask the public side
    code=$(curl -sS -o /dev/null -I -w '%{http_code}' "$s3_public/$objkey" 2>/dev/null || true)
    [ "$code" = 200 ] || code=404
  fi
  case $code in
    200)
      say "$objkey is already in the bucket — not overwriting it; taking its entries" >&2
      code=$(s3_signed GET "$work/existing.tar.zst" "$api")
      [ "$code" = 200 ] || die "reading the existing $objkey: HTTP $code"
      cp -f "$work/existing.tar.zst" "$tarball" ;;
    404)
      say "uploading $objkey ($(du -h "$tarball" | cut -f1))" >&2
      code=$(s3_signed PUT "$work/put.out" "$api" -H 'Content-Type: application/zstd' --upload-file "$tarball")
      case $code in
        200|201|204) ;;
        000) die "upload: no answer from ${s3_endpoint:-AWS} — $(tr '\n' ' ' <"$s3_err")" ;;
        *) die "upload of $objkey: HTTP $code: $(head -c 300 "$work/put.out" | tr '\n' ' ') (hack/check-s3.sh diagnoses the settings)" ;;
      esac ;;
    000) die "no answer from ${s3_endpoint:-AWS} — $(tr '\n' ' ' <"$s3_err")" ;;
    *) die "checking for $objkey: HTTP $code (hack/check-s3.sh diagnoses the settings)" ;;
  esac
  # What make helpers will download: the public URL, no credentials.
  want=$(sha256sum < "$tarball" | cut -d' ' -f1)
  for _ in 1 2 3 4 5; do
    got=$( { curl -fsSL "$s3_public/$objkey" 2>/dev/null || true; } | sha256sum | cut -d' ' -f1)
    [ "$got" != "$want" ] || return 0
    sleep 2
  done
  die "$s3_public/$objkey doesn't serve what was uploaded — public read not set up, or XBIN_HELPERS_URL isn't the bucket's root (hack/check-s3.sh diagnoses); the manifest is unchanged"
}

done_any=0
for g in "${groups[@]}"; do
  if ! helpers_arch_ok "$g" "$arch"; then
    say "$g: not built for $arch (only $(helpers_arches "$g")) — skipped"
    continue
  fi
  key=$(helpers_key "$g")
  obj=$(helpers_object "$g" "$key" "$arch")
  if [ "$MODE" != stage-only ] && helpers_published "$g" "$key" "$arch"; then
    say "$g: $key ($arch) is already in hack/helpers.sha256"
    continue
  fi

  tarball="$work/$obj"
  mkdir -p "$(dirname "$tarball")"
  case "$MODE" in
    staged)
      [ -f "$DIR/$obj" ] || die "$DIR has no $obj (the current $g key is $key — staged from other inputs?)"
      cp -f "$DIR/$obj" "$tarball" ;;
    *)
      say "$g: building $key ($arch) from source"
      helpers_build "$g" "$work/build-$g"
      helpers_pack "$g" "$work/build-$g" "$tarball" ;;
  esac
  helpers_hash_tarball "$g" "$key" "$arch" "$tarball" >/dev/null || die "$g: $tarball is not a $g set"

  if [ "$MODE" = stage-only ]; then
    mkdir -p "$(dirname "$DIR/$obj")"
    cp -f "$tarball" "$DIR/$obj"
    { awk -v g="$g" -v a="$arch" '!($1 == g && $3 == a)' "$DIR/helpers.sha256"
      helpers_hash_tarball "$g" "$key" "$arch" "$tarball"; } > "$work/staged"
    mv -f "$work/staged" "$DIR/helpers.sha256"
    say "$g: staged $DIR/$obj"
    continue
  fi

  upload "$s3_prefix$obj" "$tarball"
  helpers_manifest_put "$g" "$arch" "$(helpers_hash_tarball "$g" "$key" "$arch" "$tarball")"
  say "$g: hack/helpers.sha256 now pins $key ($arch), served at $s3_public/$s3_prefix$obj"
  done_any=1
done

if [ "$done_any" = 1 ]; then
  say "done — commit hack/helpers.sha256"
fi
