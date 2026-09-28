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

[ -f "$envfile" ] || die "no $envfile — copy s3secret.env.example to s3secret.env and fill it in"
if git -C "$repo" ls-files --error-unmatch "$envfile" >/dev/null 2>&1; then
  die "$envfile is tracked by git — it holds secrets: git rm --cached it (s3secret.env is gitignored)"
fi
mode=$(stat -c %a "$envfile" 2>/dev/null || stat -f %Lp "$envfile")
case $mode in
*[0-7][0-7][1-7] | *[0-7][1-7][0-7]) say "  warn  $envfile is mode $mode: chmod 600 it (it holds write credentials)" ;;
esac

# KEY=value lines only (comments, blanks skipped); read, never sourced.
XBIN_HELPERS_S3_BUCKET="" XBIN_HELPERS_S3_ENDPOINT="" XBIN_HELPERS_S3_REGION="" XBIN_HELPERS_S3_PREFIX=""
XBIN_HELPERS_URL="" AWS_ACCESS_KEY_ID="" AWS_SECRET_ACCESS_KEY=""
while IFS= read -r line || [ -n "$line" ]; do
  line=${line%%$'\r'}
  case $line in '' | '#'*) continue ;; esac
  key=${line%%=*}
  val=${line#*=}
  val=${val%%[[:space:]]#*} # a trailing " # comment"
  val=${val%"${val##*[![:space:]]}"}
  case $key in
  XBIN_HELPERS_S3_BUCKET | XBIN_HELPERS_S3_ENDPOINT | XBIN_HELPERS_S3_REGION | XBIN_HELPERS_S3_PREFIX | \
    XBIN_HELPERS_URL | AWS_ACCESS_KEY_ID | AWS_SECRET_ACCESS_KEY)
    printf -v "$key" '%s' "$val" ;;
  *) say "  warn  unknown setting $key (ignored)" ;;
  esac
done <"$envfile"

bucket=$XBIN_HELPERS_S3_BUCKET
endpoint=${XBIN_HELPERS_S3_ENDPOINT%/}
region=${XBIN_HELPERS_S3_REGION:-us-east-1}
prefix=$XBIN_HELPERS_S3_PREFIX
public=${XBIN_HELPERS_URL%/}
missing=""
[ -n "$bucket" ] || missing="$missing XBIN_HELPERS_S3_BUCKET"
[ -n "$public" ] || missing="$missing XBIN_HELPERS_URL"
[ -n "$AWS_ACCESS_KEY_ID" ] || missing="$missing AWS_ACCESS_KEY_ID"
[ -n "$AWS_SECRET_ACCESS_KEY" ] || missing="$missing AWS_SECRET_ACCESS_KEY"
[ -z "$missing" ] || die "not set in $envfile:$missing"
case $prefix in '' | */) ;; *) prefix="$prefix/" ;; esac

curl --help all 2>/dev/null | grep -q -- '--aws-sigv4' || die "curl lacks --aws-sigv4 (needs curl ≥ 7.75)"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-s3.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
stamp=$(date -u +%Y%m%dT%H%M%SZ)
rand=$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')
key="${prefix}xbin-s3-check/$stamp-$rand.txt"
printf 'xbin S3 check %s %s — a throwaway object; safe to delete\n' "$stamp" "$rand" >"$tmp/obj"

# The S3 API address of the object: path style on a given endpoint (R2, B2,
# MinIO …), virtual-hosted on AWS.
if [ -n "$endpoint" ]; then
  api="$endpoint/$bucket/$key"
else
  api="https://$bucket.s3.$region.amazonaws.com/$key"
fi
pub="$public/$key"

# signed <method> <out> [curl args…] — prints the HTTP status. The
# credentials travel in a curl config on stdin.
signed() {
  local method=$1 out=$2
  shift 2
  printf 'user = "%s:%s"\n' "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" |
    curl -sS -K - -o "$out" -w '%{http_code}' --aws-sigv4 "aws:amz:$region:s3" -X "$method" "$@" "$api" 2>"$tmp/curl.err" || true
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
