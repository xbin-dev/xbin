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
# SwiftPM's clones (SourcePackages) for every xcodebuild of a job: one
# directory, next to the DerivedData, so a cache (ci-cache.sh) holds both.
XBIN_CI_SPM=${XBIN_CI_SPM:-$XBIN_CI_DERIVED/SourcePackages}
export XBIN_CI_OUT XBIN_CI_DERIVED XBIN_CI_SPM

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

# ci_content_mtimes <path>… — give every file git tracks under <path>
# (relative to the repository) an mtime that is a function of its content
# (its blob id): the same bytes get the same mtime in every checkout,
# different bytes a different one (1 in ~4.6e8 collides). A fresh checkout
# stamps every file "now", so a DerivedData restored from a cache
# (ci-cache.sh) would recompile every source: Xcode's build system and the
# Swift driver decide by mtime (and size), not content. Outside a git
# checkout it does nothing. Prints how many files it stamped.
ci_content_mtimes() {
  local n=0 stamp path
  if ! git -C "$XBIN_REPO" rev-parse --git-dir >/dev/null 2>&1; then
    echo 0
    return 0
  fi
  while read -r stamp path; do
    [ -n "$path" ] && [ -f "$XBIN_REPO/$path" ] || continue
    TZ=UTC touch -t "$stamp" "$XBIN_REPO/$path"
    n=$((n + 1))
  done <<EOF
$(git -C "$XBIN_REPO" -c core.quotePath=false ls-files -s -- "$@" | ci_content_stamps)
EOF
  echo "$n"
}
# ci_content_stamps — `git ls-files -s` lines on stdin → "<touch -t stamp>
# <path>": the blob id's first 8 hex digits spread over 2001–2016 × month
# × day 1–28 × hour × minute × second (UTC) — past dates, so no tool sees
# a file from the future. Tested on Linux (ci-dry-test.sh).
ci_content_stamps() {
  awk -F'\t' '{
    split($1, m, " "); h = m[2]; v = 0
    for (i = 1; i <= 8; i++) v = v * 16 + index("0123456789abcdef", substr(h, i, 1)) - 1
    y = 2001 + v % 16; v = int(v / 16)
    mo = 1 + v % 12; v = int(v / 12)
    d = 1 + v % 28; v = int(v / 28)
    hh = v % 24; v = int(v / 24)
    mi = v % 60; v = int(v / 60)
    printf "%04d%02d%02d%02d%02d.%02d %s\n", y, mo, d, hh, mi, v % 60, $2
  }'
}

# ci_sha256 — the SHA-256 of stdin, hex (macOS: shasum; Linux: sha256sum).
ci_sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | cut -d' ' -f1
  else
    sha256sum | cut -d' ' -f1
  fi
}

# ci_xcode_slug — "27.0-27A266a" from `xcodebuild -version` (cache keys, the
# self-hosted cache directory); "unknown" when xcodebuild says nothing.
ci_xcode_slug() {
  local v
  v=$(xcodebuild -version 2>/dev/null | awk '/^Xcode /{x=$2} /^Build version /{b=$3} END{if (x != "") print x (b != "" ? "-" b : "")}')
  printf '%s\n' "${v:-unknown}"
}

# ci_signing — sets CI_SIGN, the code-signing settings an app build gets,
# from XBIN_SIGNING:
#   none   (default) CODE_SIGNING_ALLOWED=NO: nothing is signed (what CI
#          needs to compile; no team, no identity)
#   adhoc  "Sign to Run Locally" (identity -, no team, no profile): what a
#          simulator run needs so the app's entitlements (the Keychain)
#          apply — mac-remote.sh's run and e2e
# shellcheck disable=SC2034 # CI_SIGN is for the caller
ci_signing() {
  case ${XBIN_SIGNING:-none} in
  none) CI_SIGN=(CODE_SIGNING_ALLOWED=NO) ;;
  adhoc) CI_SIGN=(CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER=) ;;
  *)
    ci_error "XBIN_SIGNING=${XBIN_SIGNING}: expected none or adhoc"
    return 1
    ;;
  esac
}

# ci_conditions — sets CI_COND, the build settings that add Swift
# compilation conditions from XBIN_SWIFT_CONDITIONS (space-separated, e.g.
# XBIN_SDK_27_1 once XBIN_XCODE pins an Xcode with the iOS 27.1 SDK — the
# iPhone Duo APIs; ios.yml's env sets it, empty by default). Empty → none.
# shellcheck disable=SC2034 # CI_COND is for the caller
ci_conditions() {
  CI_COND=()
  local c=${XBIN_SWIFT_CONDITIONS:-}
  c=$(printf '%s' "$c" | tr -s ' ' | sed 's/^ //; s/ $//')
  [ -z "$c" ] || CI_COND=("SWIFT_ACTIVE_COMPILATION_CONDITIONS=\$(inherited) $c")
}

# ci_project — in native/ios: `xcodegen generate` (project.yml → the
# .xcodeproj, never committed), then set proj to the one .xcodeproj there.
# A second one (a committed or stale project) is an error.
ci_project() {
  command -v xcodegen >/dev/null 2>&1 || {
    ci_error "xcodegen not found (native/ios/scripts/ci-xcodegen.sh, or brew install xcodegen)"
    return 1
  }
  ci_group "xcodegen generate"
  xcodegen generate --spec project.yml || { ci_endgroup; return 1; }
  ci_endgroup
  # XcodeGen names the project after project.yml's `name:`; a fresh
  # checkout holds exactly the one it just generated.
  proj=""
  local p
  for p in *.xcodeproj; do
    if [ -d "$p" ]; then
      if [ -n "$proj" ]; then
        ci_error "several .xcodeproj in native/ios ($proj, $p) — is one committed? (native/AGENTS.md: never commit it)"
        return 1
      fi
      proj=$p
    fi
  done
  [ -n "$proj" ] || { ci_error "xcodegen generated no .xcodeproj in native/ios"; return 1; }
}

# ci_metal — SwiftTerm compiles a Metal shader; since Xcode 26 the Metal
# toolchain is a separate component that runner images may lack ("cannot
# execute tool 'metal' due to missing Metal Toolchain"). Nothing to do when
# `xcrun metal` works; else fetch it. A failed fetch is only a warning: the
# build that needs it says what broke.
ci_metal() {
  ci_group "Metal toolchain"
  if xcrun metal --version >/dev/null 2>&1; then
    xcrun metal --version 2>&1 | head -n 1
  else
    echo "missing — xcodebuild -downloadComponent MetalToolchain"
    ci_timeout 900 xcodebuild -downloadComponent MetalToolchain ||
      ci_warn "xcodebuild -downloadComponent MetalToolchain failed; SwiftTerm's shader will not compile"
  fi
  ci_endgroup
}
