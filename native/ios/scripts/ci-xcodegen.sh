#!/usr/bin/env bash
# native/ios/scripts/ci-xcodegen.sh — make sure xcodegen is on PATH (the
# ios.yml app and snapshots jobs, mac-setup.sh). Nothing to do when it
# already is; otherwise the pinned release zip, checked against its
# SHA-256, unpacked into a tools directory; if that fails, Homebrew.
#
#   XCODEGEN_VERSION, XCODEGEN_SHA256   the pin (below); change them together
#   XBIN_CI_TOOLS   where the zip is unpacked: default ~/Library/Caches/
#                   xbin-ci/tools on a self-hosted runner (kept between jobs),
#                   else $RUNNER_TEMP/xbin-tools
#
# In Actions the tool's bin/ goes onto GITHUB_PATH for the job's later steps;
# outside, the script prints the PATH line to use.
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

XCODEGEN_VERSION=${XCODEGEN_VERSION:-2.46.0}
XCODEGEN_SHA256=${XCODEGEN_SHA256:-4d9e34b62172d645eed6457cac13fc222569974098ef4ee9c3368bedf0196806}

if command -v xcodegen >/dev/null 2>&1; then
  echo "xcodegen $(xcodegen --version 2>/dev/null </dev/null | sed 's/^Version: //') at $(command -v xcodegen)"
  exit 0
fi

if [ -z "${XBIN_CI_TOOLS:-}" ]; then
  if [ "${RUNNER_ENVIRONMENT:-}" = self-hosted ]; then
    XBIN_CI_TOOLS=$HOME/Library/Caches/xbin-ci/tools
  else
    XBIN_CI_TOOLS=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/xbin-tools
  fi
fi
dir=$XBIN_CI_TOOLS/xcodegen-$XCODEGEN_VERSION

use() {
  if [ -n "${GITHUB_PATH:-}" ]; then
    echo "$1" >>"$GITHUB_PATH"
  else
    echo "export PATH=\"$1:\$PATH\""
  fi
  echo "xcodegen $("$1/xcodegen" --version 2>/dev/null </dev/null | sed 's/^Version: //') at $1/xcodegen"
}

# Unpacked by an earlier job on this machine.
if [ -x "$dir/xcodegen/bin/xcodegen" ]; then
  use "$dir/xcodegen/bin"
  exit 0
fi

zip_url="https://github.com/yonaskolb/XcodeGen/releases/download/$XCODEGEN_VERSION/xcodegen.zip"
fetch() {
  local tmp got
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/xcodegen.XXXXXX")
  if ! curl -fsSL --retry 3 -o "$tmp/xcodegen.zip" "$zip_url"; then
    ci_warn "could not download $zip_url"
    rm -rf "$tmp"
    return 1
  fi
  got=$(ci_sha256 <"$tmp/xcodegen.zip")
  if [ "$got" != "$XCODEGEN_SHA256" ]; then
    ci_warn "xcodegen.zip $XCODEGEN_VERSION: SHA-256 $got, expected $XCODEGEN_SHA256 — not used"
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$dir"
  mkdir -p "$dir"
  if ! unzip -q "$tmp/xcodegen.zip" -d "$dir" || [ ! -x "$dir/xcodegen/bin/xcodegen" ]; then
    ci_warn "xcodegen.zip $XCODEGEN_VERSION did not unpack to xcodegen/bin/xcodegen"
    rm -rf "$tmp" "$dir"
    return 1
  fi
  rm -rf "$tmp"
}

ci_group "xcodegen $XCODEGEN_VERSION (pinned release zip)"
if fetch; then
  ci_endgroup
  use "$dir/xcodegen/bin"
  exit 0
fi
ci_endgroup

if command -v brew >/dev/null 2>&1; then
  ci_group "brew install xcodegen"
  brew install xcodegen
  ci_endgroup
  echo "xcodegen $(xcodegen --version 2>/dev/null </dev/null | sed 's/^Version: //') (Homebrew)"
  exit 0
fi
ci_error "no xcodegen: the pinned zip failed and there is no Homebrew (native/ios/scripts/mac-setup.sh installs both)"
exit 1
