# shellcheck shell=bash
# native/ios/scripts/ci-lib.sh — helpers the ci-*.sh scripts source. They
# work the same in GitHub Actions (log groups, annotations, the step
# summary) and in a plain shell (a Mac over ssh): outside Actions the
# workflow commands degrade to plain lines.
#
# Bash 3.2 (macOS's /bin/bash): no mapfile, no associative arrays, no
# ${var,,}, and "${arr[@]}" of an empty array under `set -u` is an error.

# The repository root, from this file's location.
XBIN_REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
export XBIN_REPO

# Where results go (uploaded as artifacts): $XBIN_CI_OUT, default
# $RUNNER_TEMP/xbin-ci. DerivedData lives beside it, never inside it.
XBIN_CI_OUT=${XBIN_CI_OUT:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/xbin-ci}
XBIN_CI_DERIVED=${XBIN_CI_DERIVED:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/xbin-derived}
export XBIN_CI_OUT XBIN_CI_DERIVED

in_actions() { [ "${GITHUB_ACTIONS:-}" = true ]; }

ci_group() { if in_actions; then echo "::group::$*"; else echo ">> $*"; fi; }
ci_endgroup() { if in_actions; then echo "::endgroup::"; fi; }
ci_notice() { if in_actions; then echo "::notice::$*"; else echo "notice: $*"; fi; }
ci_warn() { if in_actions; then echo "::warning::$*"; else echo "warning: $*" >&2; fi; }
ci_error() { if in_actions; then echo "::error::$*"; else echo "error: $*" >&2; fi; }

# ci_summary line… — append Markdown to the job's summary page (no-op
# outside Actions).
ci_summary() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf '%s\n' "$@" >>"$GITHUB_STEP_SUMMARY"
  fi
}

# ci_udid <destination> — the id= part of "platform=iOS Simulator,id=<UDID>".
ci_udid() {
  case $1 in
  *id=*) printf '%s\n' "${1##*id=}" | cut -d, -f1 ;;
  *) return 1 ;;
  esac
}

# ci_timeout <seconds> <command…> — run command; kill it after <seconds>
# (status 124). macOS ships no timeout(1).
ci_timeout() {
  local secs=$1 waited=0 pid
  shift
  "$@" &
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$secs" ]; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
}

# ci_xcodebuild <logfile> <xcodebuild args…> — run xcodebuild, keep the
# full log in <logfile> (uploaded next to the .xcresult), and show it
# through xcbeautify when the runner has it (XCBEAUTIFY=0 turns that off);
# in Actions its github-actions renderer turns errors into annotations.
# Returns xcodebuild's status.
ci_xcodebuild() {
  local log=$1
  shift
  mkdir -p "$(dirname "$log")"
  echo "+ xcodebuild $*"
  if [ "${XCBEAUTIFY:-1}" = 0 ] || ! command -v xcbeautify >/dev/null 2>&1; then
    NSUnbufferedIO=YES xcodebuild "$@" 2>&1 | tee "$log"
    return
  fi
  local help
  help=$(xcbeautify --help 2>&1 || true)
  case $help in
  *--renderer*)
    if in_actions; then
      NSUnbufferedIO=YES xcodebuild "$@" 2>&1 | tee "$log" | xcbeautify --renderer github-actions
      return
    fi
    ;;
  esac
  NSUnbufferedIO=YES xcodebuild "$@" 2>&1 | tee "$log" | xcbeautify
}

# ci_schemes [xcodebuild -list args…] — the scheme names `xcodebuild -list`
# prints (run in the current directory: a package, or pass -project), one
# per line; ci_schemes_in parses that output from stdin (tested on Linux).
ci_schemes() { xcodebuild -list "$@" 2>/dev/null | ci_schemes_in; }
ci_schemes_in() {
  awk 'f { sub(/^[ \t]+/, ""); if ($0 == "") exit; print } /^[ \t]*Schemes:[ \t]*$/ { f = 1 }'
}
