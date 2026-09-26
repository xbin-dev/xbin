#!/usr/bin/env bash
# native/ios/scripts/ci-swift-test.sh — `swift test` each SwiftPM package
# under native/ios/Packages/ that exists (the ios.yml "packages" job; it
# runs the same on Linux for XbinCore). A package that isn't there yet is
# skipped; every present package runs even when an earlier one fails, and
# the script fails if any did.
#
#   ci-swift-test.sh [Package…]     default: XbinCore XbinTerm XbinAgent
#
# `swift test` builds for the host (macOS on CI): code that needs UIKit
# sits behind #if canImport(UIKit), and tests that need a simulator belong
# in a package tested by xcodebuild (ci-snapshots.sh).
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

if [ $# -eq 0 ]; then
  set -- XbinCore XbinTerm XbinAgent
fi

ran=0 failed=""
ci_summary "| package | swift test |" "|---|---|"
for pkg in "$@"; do
  dir="$XBIN_REPO/native/ios/Packages/$pkg"
  if [ ! -f "$dir/Package.swift" ]; then
    echo "$pkg: not present (no native/ios/Packages/$pkg/Package.swift) — skipped"
    ci_summary "| $pkg | skipped (not present) |"
    continue
  fi
  ran=$((ran + 1))
  ci_group "swift test — $pkg"
  status=0
  (cd "$dir" && swift test) || status=$?
  ci_endgroup
  if [ "$status" -eq 0 ]; then
    ci_summary "| $pkg | passed |"
  else
    ci_error "$pkg: swift test failed (exit $status)"
    ci_summary "| $pkg | **failed** (exit $status) |"
    failed="$failed $pkg"
  fi
done

if [ "$ran" -eq 0 ]; then
  ci_notice "no SwiftPM package present under native/ios/Packages ($*) — nothing to test"
  exit 0
fi
if [ -n "$failed" ]; then
  echo "swift test failed:$failed" >&2
  exit 1
fi
echo "swift test: $ran package(s) passed"
