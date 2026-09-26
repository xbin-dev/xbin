#!/usr/bin/env bash
# native/ios/scripts/ci-build-app.sh — generate the Xcode project from
# native/ios/project.yml (XcodeGen; the .xcodeproj is never committed) and
# build the app scheme for a simulator (the ios.yml "app" job; also
# mac-remote.sh's build and run).
#
#   ci-build-app.sh "<destination>"     e.g. "$(pick-sim.sh)"
#
#   XBIN_APP_SCHEME   the scheme to build (default Xbin)
#   XBIN_SIGNING      none (default: unsigned, CODE_SIGNING_ALLOWED=NO) or
#                     adhoc (signed to run locally — a simulator run that
#                     needs the app's Keychain entitlements)
#   XBIN_SWIFT_CONDITIONS  extra compilation conditions, e.g. XBIN_SDK_27_1
#                     (every build script takes it; ci-lib.sh ci_conditions)
#
# Results in $XBIN_CI_OUT (default $RUNNER_TEMP/xbin-ci): app-build.xcresult
# and the full xcodebuild log app-build.log; the build in
# $XBIN_CI_DERIVED/app, SwiftPM clones in $XBIN_CI_SPM (ci-cache.sh decides
# both). No project.yml yet → a notice, success. Needs xcodegen on PATH
# (ci-xcodegen.sh). The build keeps going after an error, so one run reports
# every target's compile errors.
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

dest=${1:?usage: ci-build-app.sh "<xcodebuild destination>"}
scheme=${XBIN_APP_SCHEME:-Xbin}
ios="$XBIN_REPO/native/ios"

if [ ! -f "$ios/project.yml" ]; then
  ci_notice "no native/ios/project.yml yet — app build skipped"
  exit 0
fi
ci_signing
ci_conditions

cd "$ios"
ci_project

ci_group "schemes in $proj"
schemes=$(ci_schemes -project "$proj") || true
echo "$schemes"
ci_endgroup
if ! printf '%s\n' "$schemes" | grep -qxF "$scheme"; then
  # xcodebuild has the final word; this only names the likely fix.
  ci_warn "$proj lists no scheme \"$scheme\" — project.yml should declare it (targets.$scheme.scheme or schemes.$scheme)"
fi

ci_metal

mkdir -p "$XBIN_CI_OUT"
rm -rf "$XBIN_CI_OUT/app-build.xcresult"
status=0
ci_xcodebuild "$XBIN_CI_OUT/app-build.log" build \
  -project "$proj" \
  -scheme "$scheme" \
  -configuration Debug \
  -destination "$dest" \
  -derivedDataPath "$XBIN_CI_DERIVED/app" \
  -clonedSourcePackagesDirPath "$XBIN_CI_SPM" \
  -resultBundlePath "$XBIN_CI_OUT/app-build.xcresult" \
  -skipMacroValidation \
  -skipPackagePluginValidation \
  -IDEBuildingContinueBuildingAfterErrors=YES \
  COMPILER_INDEX_STORE_ENABLE=NO \
  "${CI_SIGN[@]}" \
  ${CI_COND[@]+"${CI_COND[@]}"} || status=$?

if [ "$status" -eq 0 ]; then
  ci_summary "**App:** \`$scheme\` built for \`$dest\`."
else
  ci_error "xcodebuild build -scheme $scheme failed (exit $status); the full log and .xcresult are in the xcresult-app artifact"
  ci_summary "**App:** \`$scheme\` build **failed** (exit $status) — see the xcresult-app artifact."
fi
exit "$status"
