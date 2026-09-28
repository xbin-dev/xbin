# shellcheck shell=bash
# hack/s3-lib.sh — the helpers bucket's settings file and signed requests,
# shared by hack/check-s3.sh and hack/publish-helpers.sh. The caller
# defines say and die. curl only (--aws-sigv4, curl ≥ 7.75); the secrets
# reach curl on its stdin (-K -), never a command line.

# s3_load <env-file>: refuse a tracked file, warn about a group/world-readable
# one, read its KEY=value lines (never sourced) into XBIN_HELPERS_S3_BUCKET
# XBIN_HELPERS_S3_ENDPOINT XBIN_HELPERS_S3_REGION XBIN_HELPERS_S3_PREFIX
# XBIN_HELPERS_URL AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY, then derive
# s3_bucket s3_endpoint s3_region s3_prefix (with its trailing /) s3_public.
s3_load() {
  local envfile=$1 repo mode line key val
  repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
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

  s3_bucket=$XBIN_HELPERS_S3_BUCKET
  s3_endpoint=${XBIN_HELPERS_S3_ENDPOINT%/}
  s3_region=${XBIN_HELPERS_S3_REGION:-us-east-1}
  s3_prefix=$XBIN_HELPERS_S3_PREFIX
  s3_public=${XBIN_HELPERS_URL%/}
  case $s3_prefix in '' | */) ;; *) s3_prefix="$s3_prefix/" ;; esac

  curl --help all 2>/dev/null | grep -q -- '--aws-sigv4' || die "curl lacks --aws-sigv4 (needs curl ≥ 7.75)"
}

# s3_require <env-file>: die unless the settings a write needs are there.
s3_require() {
  local missing=""
  [ -n "$s3_bucket" ] || missing="$missing XBIN_HELPERS_S3_BUCKET"
  [ -n "$s3_public" ] || missing="$missing XBIN_HELPERS_URL"
  [ -n "$AWS_ACCESS_KEY_ID" ] || missing="$missing AWS_ACCESS_KEY_ID"
  [ -n "$AWS_SECRET_ACCESS_KEY" ] || missing="$missing AWS_SECRET_ACCESS_KEY"
  [ -z "$missing" ] || die "not set in $1:$missing"
}

# s3_api <key>: the S3 API address of an object — path style on a given
# endpoint (R2, B2, MinIO …), virtual-hosted on AWS.
s3_api() {
  if [ -n "$s3_endpoint" ]; then
    echo "$s3_endpoint/$s3_bucket/$1"
  else
    echo "https://$s3_bucket.s3.$s3_region.amazonaws.com/$1"
  fi
}

# s3_signed <method> <out> <url> [curl args…] — prints the HTTP status;
# curl's own errors go to $s3_err (default: discarded). The credentials
# travel in a curl config on stdin.
s3_signed() {
  local verb=(-X "$1") out=$2 url=$3
  [ "$1" != HEAD ] || verb=(-I) # -X HEAD would wait for a body
  shift 3
  printf 'user = "%s:%s"\n' "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" |
    curl -sS -K - -o "$out" -w '%{http_code}' --aws-sigv4 "aws:amz:$s3_region:s3" "${verb[@]}" "$@" "$url" 2>"${s3_err:-/dev/null}" || true
}
