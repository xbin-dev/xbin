#!/usr/bin/env bash
# hack/check-s3.sh — check the S3 settings for the prebuilt helper binaries
# (s3secret.env, see s3secret.env.example) work end to end:
#
#   1. upload a small throwaway object with the write credentials
#   2. download it WITHOUT credentials from the public read URL, and compare
#      (on failure, read it again with credentials to tell "public read not
#      set up" from "the upload didn't land")
#   3. delete it
#
#   hack/check-s3.sh [env-file]        # default: s3secret.env at the repo root
#
# Needs only curl (≥ 7.75, for --aws-sigv4). The secrets go to curl on its
# stdin (-K -), never on a command line; nothing here prints them. Exit
# status 0 only when all three steps pass.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
envfile=${1:-$repo/s3secret.env}

say() { printf '%s\n' "$*"; }
pass() { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*" >&2; }
die() { fail "$*"; exit 1; }

# The settings: the file is parsed, never sourced (hack/s3-lib.sh).
# shellcheck source=hack/s3-lib.sh
. "$repo/hack/s3-lib.sh"
s3_load "$envfile"
s3_require "$envfile"
bucket=$s3_bucket endpoint=$s3_endpoint region=$s3_region prefix=$s3_prefix public=$s3_public

tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-s3.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
stamp=$(date -u +%Y%m%dT%H%M%SZ)
rand=$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')
key="${prefix}xbin-s3-check/$stamp-$rand.txt"
printf 'xbin S3 check %s %s — a throwaway object; safe to delete\n' "$stamp" "$rand" >"$tmp/obj"

api=$(s3_api "$key")
pub="$public/$key"
s3_err="$tmp/curl.err"

# signed <method> <out> [curl args…] — prints the HTTP status.
signed() {
  local method=$1 out=$2
  shift 2
  s3_signed "$method" "$out" "$api" "$@"
}

say "S3 check: bucket $bucket, region $region, ${endpoint:-AWS}, key $key"

# 1. upload
code=$(signed PUT "$tmp/put.out" -H 'Content-Type: text/plain; charset=utf-8' --upload-file "$tmp/obj")
case $code in
200 | 201 | 204) pass "upload ($code)" ;;
000) die "upload: no answer from ${endpoint:-AWS} — $(tr '\n' ' ' <"$tmp/curl.err")(endpoint URL, DNS, network?)" ;;
*)
  hint=""
  grep -q 'SignatureDoesNotMatch' "$tmp/put.out" 2>/dev/null && hint=" — wrong secret key or region"
  grep -q 'InvalidAccessKeyId' "$tmp/put.out" 2>/dev/null && hint=" — unknown access key id"
  grep -q 'NoSuchBucket' "$tmp/put.out" 2>/dev/null && hint=" — no such bucket"
  grep -q 'AccessDenied' "$tmp/put.out" 2>/dev/null && hint=" — the key may not write here (bucket/prefix policy)"
  die "upload: HTTP $code$hint: $(head -c 300 "$tmp/put.out" | tr '\n' ' ')" ;;
esac

# 2. public download, no credentials (a CDN or custom domain may lag a
# moment: a few tries)
ok=0
for _ in 1 2 3 4 5; do
  pcode=$(curl -sS -o "$tmp/pub.out" -w '%{http_code}' "$pub" 2>"$tmp/pub.err" || true)
  if [ "$pcode" = 200 ] && cmp -s "$tmp/obj" "$tmp/pub.out"; then ok=1; break; fi
  sleep 2
done
if [ "$ok" = 1 ]; then
  pass "public download without credentials, same bytes ($pub)"
else
  gcode=$(signed GET "$tmp/get.out")
  if [ "$gcode" = 200 ] && cmp -s "$tmp/obj" "$tmp/get.out"; then
    fail "public download: HTTP ${pcode:-000} from $pub — the object IS in the bucket (a signed read works), so public read isn't set up for it, or XBIN_HELPERS_URL doesn't map to the bucket's root"
  else
    fail "public download: HTTP ${pcode:-000} from $pub, and a signed read got HTTP $gcode — the upload didn't land where expected"
  fi
  signed DELETE "$tmp/del.out" >/dev/null
  exit 1
fi

# 3. delete
code=$(signed DELETE "$tmp/del.out")
case $code in
200 | 202 | 204) pass "delete ($code)" ;;
*) fail "delete: HTTP $code — the throwaway object $key stays (harmless; the key may lack delete rights)"; exit 1 ;;
esac

# Informational: is the bucket's listing public too?
lcode=$(curl -sS -o "$tmp/list.out" -w '%{http_code}' "$public/" 2>/dev/null || true)
if [ "$lcode" = 200 ] && grep -q '<ListBucketResult' "$tmp/list.out" 2>/dev/null; then
  say "  note  $public/ lists the bucket's objects publicly — fine for helper binaries, but know it"
fi
say "S3 check: all good"
