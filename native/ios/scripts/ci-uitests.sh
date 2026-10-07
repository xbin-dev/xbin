#!/usr/bin/env bash
# native/ios/scripts/ci-uitests.sh — the XCUITest target XbinUITests
# (native/ios/UITests: the app end to end on a simulator against a real
# xbind). Always builds it for testing — the ios.yml app job stops there,
# since a hosted runner has no xbind to talk to; with XBIN_E2E_URL set it
# also runs it (mac-remote.sh e2e, over an ssh tunnel to a harness xbind).
#
#   ci-uitests.sh "<destination>"     e.g. "$(pick-sim.sh)"
#
#   XBIN_E2E_URL, XBIN_E2E_USER, XBIN_E2E_PASSWORD  the xbind, as the
#                         simulator reaches it, and an admin account on it
#                         (the tests sign in through the app's Log in, as a
#                         person does); handed to the tests as
#                         TEST_RUNNER_XBIN_E2E_* (unset → the run is
#                         skipped; the tests themselves skip without them)
#   TEST_RUNNER_E2E_DIR   where the tests write their screenshots (default
#                         $XBIN_CI_OUT/e2e); each is also an attachment in
#                         the result bundle
#   XBIN_E2E_ERASE=1      erase the simulator first: a clean app, no saved
#                         workspace (use a dedicated one: XBIN_SIM_ENSURE);
#                         then, after it boots, a warm-up: it settles for
#                         XBIN_E2E_WARMUP seconds (30; 0 skips the warm-up),
#                         Settings opens once — a fresh iPad's first tap hung
#                         without it
#   XBIN_E2E_ONLY         run only these tests (-only-testing: values, space
#                         separated, e.g. XbinUITests/XbinE2ETests/test03NativeCounter)
#   XBIN_SIGNING          none (default when only building) or adhoc
#                         (default when running: the app's Keychain needs
#                         its entitlements)
#   XBIN_UITEST_SCHEME    default XbinUITests
#
# Results in $XBIN_CI_OUT: uitests-build.log (+ .xcresult); when run,
# uitests.xcresult and uitests.log. Builds into $XBIN_CI_DERIVED/app — the
# app build's tree, so after ci-build-app.sh only the test bundle compiles.
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

dest=${1:?usage: ci-uitests.sh "<xcodebuild destination>"}
scheme=${XBIN_UITEST_SCHEME:-XbinUITests}
ios="$XBIN_REPO/native/ios"
run=0
[ -n "${XBIN_E2E_URL:-}" ] && run=1

if [ ! -f "$ios/project.yml" ]; then
  ci_notice "no native/ios/project.yml yet — UI tests skipped"
  exit 0
fi
if [ "$run" = 1 ]; then
  XBIN_SIGNING=${XBIN_SIGNING:-adhoc}
  [ -n "${XBIN_E2E_USER:-}" ] && [ -n "${XBIN_E2E_PASSWORD:-}" ] ||
    { ci_error "XBIN_E2E_URL is set but XBIN_E2E_USER or XBIN_E2E_PASSWORD is not"; exit 2; }
fi
ci_signing
ci_conditions

cd "$ios"
ci_project
schemes=$(ci_schemes -project "$proj") || true
if ! printf '%s\n' "$schemes" | grep -qxF "$scheme"; then
  ci_warn "$proj lists no scheme \"$scheme\" — project.yml should declare it (schemes.$scheme)"
fi

mkdir -p "$XBIN_CI_OUT"
common=(
  -project "$proj"
  -scheme "$scheme"
  -destination "$dest"
  -derivedDataPath "$XBIN_CI_DERIVED/app"
  -clonedSourcePackagesDirPath "$XBIN_CI_SPM"
  -skipMacroValidation
  -skipPackagePluginValidation
)

ci_metal
rm -rf "$XBIN_CI_OUT/uitests-build.xcresult"
status=0
ci_xcodebuild "$XBIN_CI_OUT/uitests-build.log" build-for-testing "${common[@]}" \
  -resultBundlePath "$XBIN_CI_OUT/uitests-build.xcresult" \
  -IDEBuildingContinueBuildingAfterErrors=YES \
  COMPILER_INDEX_STORE_ENABLE=NO \
  "${CI_SIGN[@]}" ${CI_COND[@]+"${CI_COND[@]}"} || status=$?
if [ "$status" -ne 0 ]; then
  ci_error "xcodebuild build-for-testing -scheme $scheme failed (exit $status); uitests-build.log is in the xcresult-app artifact"
  ci_summary "**UI tests:** \`$scheme\` build **failed** (exit $status)."
  exit "$status"
fi
if [ "$run" = 0 ]; then
  ci_summary "**UI tests:** \`$scheme\` built; not run (no XBIN_E2E_URL — they need an xbind, see native/AGENTS.md → Mac mini)."
  echo "built $scheme; not run: XBIN_E2E_URL is unset"
  exit 0
fi

TEST_RUNNER_E2E_DIR=${TEST_RUNNER_E2E_DIR:-$XBIN_CI_OUT/e2e}
TEST_RUNNER_XBIN_E2E_URL=$XBIN_E2E_URL
TEST_RUNNER_XBIN_E2E_USER=$XBIN_E2E_USER
TEST_RUNNER_XBIN_E2E_PASSWORD=$XBIN_E2E_PASSWORD
export TEST_RUNNER_E2E_DIR TEST_RUNNER_XBIN_E2E_URL TEST_RUNNER_XBIN_E2E_USER TEST_RUNNER_XBIN_E2E_PASSWORD
shots=$TEST_RUNNER_E2E_DIR
rm -rf "$shots"
mkdir -p "$shots"
echo "E2E_DIR=$shots"
echo "XBIN_E2E_URL=$XBIN_E2E_URL"

if udid=$(ci_udid "$dest"); then
  if [ "${XBIN_E2E_ERASE:-0}" = 1 ]; then
    ci_group "erase simulator $udid"
    xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
    xcrun simctl erase "$udid"
    ci_endgroup
  fi
  ci_group "boot simulator $udid"
  ci_timeout 300 xcrun simctl bootstatus "$udid" -b ||
    ci_warn "simctl bootstatus $udid failed or took over 5 min; leaving the boot to xcodebuild"
  ci_endgroup
  warm=${XBIN_E2E_WARMUP:-30}
  if [ "${XBIN_E2E_ERASE:-0}" = 1 ] && [ "$warm" -gt 0 ]; then
    # A freshly erased simulator is still setting itself up after
    # bootstatus: the first test's first tap then hung (an iPad, 2026-10-07).
    # Let it settle and open an app once (Settings) before the tests.
    ci_group "warm up simulator $udid (${warm}s)"
    sleep "$warm"
    xcrun simctl launch "$udid" com.apple.Preferences >/dev/null 2>&1 || ci_warn "could not open Settings on $udid"
    sleep $((warm / 3))
    xcrun simctl terminate "$udid" com.apple.Preferences >/dev/null 2>&1 || true
    sleep $((warm / 6))
    ci_endgroup
  fi
fi

only=()
for t in ${XBIN_E2E_ONLY:-}; do only+=("-only-testing:$t"); done
rm -rf "$XBIN_CI_OUT/uitests.xcresult"
status=0
# "${only[@]+…}": an empty array under set -u is an error in bash 3.2.
# No sysdiagnose on a failure (XBIN_E2E_DIAGNOSE=1 to get one): it takes
# the simulator up to 10 minutes and the log + screenshots say enough.
diag=never
[ "${XBIN_E2E_DIAGNOSE:-0}" = 1 ] && diag=on-failure
ci_xcodebuild "$XBIN_CI_OUT/uitests.log" test-without-building "${common[@]}" \
  -resultBundlePath "$XBIN_CI_OUT/uitests.xcresult" -collect-test-diagnostics "$diag" \
  ${only[@]+"${only[@]}"} || status=$?

pngs=$(find "$shots" -type f -name '*.png' | wc -l | tr -d ' ')
if [ "$pngs" -eq 0 ] && [ -d "$XBIN_CI_OUT/uitests.xcresult" ]; then
  ci_group "export result-bundle attachments"
  xcrun xcresulttool export attachments --path "$XBIN_CI_OUT/uitests.xcresult" --output-path "$shots/attachments" ||
    ci_warn "xcresulttool export attachments failed"
  ci_endgroup
  pngs=$(find "$shots" -type f -name '*.png' | wc -l | tr -d ' ')
fi
ci_group "screenshots ($pngs PNG)"
(cd "$shots" && find . -type f -name '*.png' | sed 's|^\./||' | sort)
ci_endgroup
if [ "$status" -eq 0 ]; then
  ci_summary "**UI tests:** \`$scheme\` passed against $XBIN_E2E_URL — $pngs screenshots."
else
  ci_error "xcodebuild test-without-building -scheme $scheme failed (exit $status); uitests.log and uitests.xcresult are in $XBIN_CI_OUT"
  ci_summary "**UI tests:** \`$scheme\` **failed** (exit $status) — $pngs screenshots."
fi
exit "$status"
