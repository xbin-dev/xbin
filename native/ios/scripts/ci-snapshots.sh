#!/usr/bin/env bash
# native/ios/scripts/ci-snapshots.sh — run the XbinRenderer package's tests
# on an iOS simulator with xcodebuild; they render every fixture (light and
# dark × the default, large and ax2 Dynamic Type sizes) and write PNGs named
# <fixture>-<light|dark>-<default|large|ax2>.png (the ios.yml "snapshots"
# job).
#
#   ci-snapshots.sh "<destination>"     e.g. "$(pick-sim.sh)"
#
# The contract with the tests (xcodebuild passes TEST_RUNNER_<NAME> to the
# test process as <NAME>):
#
#   SNAPSHOT_DIR   write PNGs here  (TEST_RUNNER_SNAPSHOT_DIR; default
#                  $XBIN_CI_OUT/snapshots; created before the run)
#   FIXTURES_DIR   native/fixtures, absolute (TEST_RUNNER_FIXTURES_DIR)
#
#   XBIN_RENDERER_SCHEME   the scheme (default XbinRenderer, falling back to
#                          XbinRenderer-Package when only that exists)
#
# Results in $XBIN_CI_OUT: snapshots-test.xcresult and snapshots-test.log.
# The PNG count goes to the job summary. When the tests wrote no PNG but
# attached images to the result bundle, those are exported into
# SNAPSHOT_DIR/attachments instead. No XbinRenderer package yet → a notice,
# success.
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

dest=${1:?usage: ci-snapshots.sh "<xcodebuild destination>"}
pkg="$XBIN_REPO/native/ios/Packages/XbinRenderer"

if [ ! -f "$pkg/Package.swift" ]; then
  ci_notice "no native/ios/Packages/XbinRenderer yet — snapshots skipped"
  exit 0
fi

TEST_RUNNER_SNAPSHOT_DIR=${TEST_RUNNER_SNAPSHOT_DIR:-$XBIN_CI_OUT/snapshots}
TEST_RUNNER_FIXTURES_DIR=${TEST_RUNNER_FIXTURES_DIR:-$XBIN_REPO/native/fixtures}
export TEST_RUNNER_SNAPSHOT_DIR TEST_RUNNER_FIXTURES_DIR
snap=$TEST_RUNNER_SNAPSHOT_DIR
mkdir -p "$snap" "$XBIN_CI_OUT"
echo "SNAPSHOT_DIR=$snap"
echo "FIXTURES_DIR=$TEST_RUNNER_FIXTURES_DIR"

cd "$pkg"
# Loading the package is the slow part of a package-mode xcodebuild on a
# fresh runner: the job's first one sat 3–5 min before printing anything
# on the hosted xcode-27 (runs 36256774609, 36259062265), whether it was
# `-list` or `test`; later ones start at once. So the scheme listing (which
# picks the scheme) starts now, in the background, while the simulator
# boots, and both are timed.
schemes_file=$XBIN_CI_OUT/renderer-schemes.txt
t0=$(date +%s)
(
  # shellcheck disable=SC2119 # no -project: the package in this directory
  ci_schemes >"$schemes_file" 2>/dev/null || true
  echo $(($(date +%s) - t0)) >"$schemes_file.secs"
) &
list_pid=$!

# Boot first and wait for it: a cold simulator is the usual cause of
# "test runner failed to launch" timeouts. xcodebuild boots it anyway, so
# a failure here is only a warning.
if udid=$(ci_udid "$dest"); then
  ci_group "boot simulator $udid"
  ci_timeout 300 xcrun simctl bootstatus "$udid" -b ||
    ci_warn "simctl bootstatus $udid failed or took over 5 min; leaving the boot to xcodebuild"
  echo "booted after $(($(date +%s) - t0)) s"
  ci_endgroup
fi

wait "$list_pid" || true
schemes=$(cat "$schemes_file" 2>/dev/null || true)
ci_group "schemes in XbinRenderer (xcodebuild -list: $(cat "$schemes_file.secs" 2>/dev/null || echo '?') s)"
echo "$schemes"
ci_endgroup
scheme=${XBIN_RENDERER_SCHEME:-XbinRenderer}
if [ -z "${XBIN_RENDERER_SCHEME:-}" ] && ! printf '%s\n' "$schemes" | grep -qxF XbinRenderer &&
  printf '%s\n' "$schemes" | grep -qxF XbinRenderer-Package; then
  scheme=XbinRenderer-Package
fi

ci_conditions
rm -rf "$XBIN_CI_OUT/snapshots-test.xcresult"
status=0
ci_xcodebuild "$XBIN_CI_OUT/snapshots-test.log" test \
  -scheme "$scheme" \
  -destination "$dest" \
  -derivedDataPath "$XBIN_CI_DERIVED/renderer" \
  -clonedSourcePackagesDirPath "$XBIN_CI_SPM" \
  -resultBundlePath "$XBIN_CI_OUT/snapshots-test.xcresult" \
  -skipMacroValidation \
  -skipPackagePluginValidation \
  COMPILER_INDEX_STORE_ENABLE=NO \
  CODE_SIGNING_ALLOWED=NO \
  ${CI_COND[@]+"${CI_COND[@]}"} || status=$?
echo "xcodebuild test done $(($(date +%s) - t0)) s after the listing started"

count_pngs() { find "$snap" -type f -name '*.png' | wc -l | tr -d ' '; }

pngs=$(count_pngs)
if [ "$pngs" -eq 0 ] && [ -d "$XBIN_CI_OUT/snapshots-test.xcresult" ]; then
  # Tests that attach images (XCTAttachment / Attachment) instead of
  # writing files: export them (xcresulttool, Xcode 16+).
  ci_group "export result-bundle attachments"
  mkdir -p "$snap/attachments"
  xcrun xcresulttool export attachments \
    --path "$XBIN_CI_OUT/snapshots-test.xcresult" \
    --output-path "$snap/attachments" ||
    ci_warn "xcresulttool export attachments failed"
  ci_endgroup
  pngs=$(count_pngs)
fi

ci_group "snapshots ($pngs PNG)"
(cd "$snap" && find . -type f -name '*.png' | sed 's|^\./||' | sort)
ci_endgroup

if [ "$status" -eq 0 ]; then
  ci_summary "**Snapshots:** tests passed on \`$dest\` — $pngs PNG in the snapshots artifact."
else
  ci_error "xcodebuild test -scheme $scheme failed (exit $status); log and .xcresult are in the xcresult-snapshots artifact"
  ci_summary "**Snapshots:** tests **failed** (exit $status) — $pngs PNG in the snapshots artifact; see xcresult-snapshots."
fi
if [ "$pngs" -eq 0 ]; then
  ci_warn "the XbinRenderer tests wrote no PNG to SNAPSHOT_DIR ($snap)"
fi
exit "$status"
