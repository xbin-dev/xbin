#!/usr/bin/env bash
# native/ios/scripts/ci-toolchain.sh — select the Xcode and log the Apple
# toolchain at the start of every ios.yml job: before using an API, check
# the SDK the runner actually has (native/AGENTS.md → "Apple").
#
#   XBIN_XCODE=/Applications/Xcode_27.1.app   use this Xcode (sets
#       DEVELOPER_DIR here and, in Actions, for the job's later steps);
#       empty → the runner's default (xcode-select -p)
#
# Prints: xcodebuild -version, swift --version, the installed Xcodes,
# xcodebuild -showsdks, and `xcrun simctl list` of device types, runtimes
# and available devices; one line of it goes to the job summary, and the
# step output `xcode` is the Xcode's version-build ("27.0-27A266a").
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

if [ -n "${XBIN_XCODE:-}" ]; then
  dev="${XBIN_XCODE%/}/Contents/Developer"
  if [ ! -d "$dev" ]; then
    ci_error "XBIN_XCODE=$XBIN_XCODE is not an Xcode (no $dev); installed:"
    ls -d /Applications/Xcode*.app 2>/dev/null || true
    exit 1
  fi
  export DEVELOPER_DIR=$dev
  if [ -n "${GITHUB_ENV:-}" ]; then
    echo "DEVELOPER_DIR=$dev" >>"$GITHUB_ENV"
  fi
fi

ci_group "toolchain"
echo "developer dir: $(xcode-select -p 2>/dev/null || echo '?')${DEVELOPER_DIR:+ (DEVELOPER_DIR=$DEVELOPER_DIR)}"
xcodebuild -version
swift --version 2>&1 || true
sw_vers 2>/dev/null || true
echo "installed Xcodes:"
ls -d /Applications/Xcode*.app 2>/dev/null || echo "  (none under /Applications)"
command -v xcodegen >/dev/null 2>&1 && echo "xcodegen $(xcodegen --version 2>/dev/null </dev/null)"
command -v xcbeautify >/dev/null 2>&1 && echo "xcbeautify $(xcbeautify --version 2>/dev/null </dev/null)"
ci_endgroup

ci_group "SDKs (xcodebuild -showsdks)"
xcodebuild -showsdks || true
ci_endgroup

# `simctl list` takes one kind per call (a second word is a search term).
ci_group "simulator device types"
xcrun simctl list devicetypes || true
ci_endgroup
ci_group "simulator runtimes"
xcrun simctl list runtimes || true
ci_endgroup
ci_group "available simulators"
xcrun simctl list devices available || true
ci_endgroup

xcode=$(xcodebuild -version 2>/dev/null | tr '\n' ' ' | sed 's/ *$//')
sdks=$(xcodebuild -showsdks 2>/dev/null | sed -n 's/.*-sdk \(iphone[a-z]*[0-9.]*\).*/\1/p' | tr '\n' ' ' | sed 's/ *$//')
ci_summary "**Toolchain:** ${xcode:-?} · iOS SDKs: ${sdks:-none}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "xcode=$(ci_xcode_slug)" >>"$GITHUB_OUTPUT"
fi
