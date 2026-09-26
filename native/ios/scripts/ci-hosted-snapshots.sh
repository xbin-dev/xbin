#!/usr/bin/env bash
# native/ios/scripts/ci-hosted-snapshots.sh — the renderer's snapshot tests
# once more, hosted by an app: project.yml's scheme XbinSnapshots
# (XbinSnapshotHost, an empty app, + XbinSnapshotTests, the same
# SnapshotTests.swift compiled with XBIN_SNAPSHOT_HOST). There each fixture
# is drawn on the app's window scene with drawHierarchy, so Liquid Glass bar
# items, tab bars, materials and vibrancy come out as the screen shows them;
# the package run (ci-snapshots.sh) has no scene and draws with
# layer.render, which skips all of those (bar items come out white). The
# ios.yml "snapshots" job runs this after ci-snapshots.sh.
#
#   ci-hosted-snapshots.sh "<destination>"     e.g. "$(pick-sim.sh)"
#
#   TEST_RUNNER_SNAPSHOT_DIR  where the PNGs go, named as ci-snapshots.sh's
#                             (default $XBIN_CI_OUT/snapshots/hosted)
#   TEST_RUNNER_FIXTURES_DIR  native/fixtures (the default)
#   XBIN_SNAPSHOT_SCHEME      the scheme (default XbinSnapshots)
#
# Results in $XBIN_CI_OUT: hosted-snapshots.xcresult and hosted-snapshots.log.
# No project.yml → a notice, success. Needs xcodegen on PATH (ci-xcodegen.sh).
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

dest=${1:?usage: ci-hosted-snapshots.sh "<xcodebuild destination>"}
scheme=${XBIN_SNAPSHOT_SCHEME:-XbinSnapshots}
ios="$XBIN_REPO/native/ios"

if [ ! -f "$ios/project.yml" ]; then
  ci_notice "no native/ios/project.yml yet — hosted snapshots skipped"
  exit 0
fi
TEST_RUNNER_SNAPSHOT_DIR=${TEST_RUNNER_SNAPSHOT_DIR:-$XBIN_CI_OUT/snapshots/hosted}
TEST_RUNNER_FIXTURES_DIR=${TEST_RUNNER_FIXTURES_DIR:-$XBIN_REPO/native/fixtures}
export TEST_RUNNER_SNAPSHOT_DIR TEST_RUNNER_FIXTURES_DIR
snap=$TEST_RUNNER_SNAPSHOT_DIR
mkdir -p "$snap" "$XBIN_CI_OUT"
echo "SNAPSHOT_DIR=$snap"

cd "$ios"
ci_project
ci_conditions

rm -rf "$XBIN_CI_OUT/hosted-snapshots.xcresult"
status=0
ci_xcodebuild "$XBIN_CI_OUT/hosted-snapshots.log" test \
  -project "$proj" \
  -scheme "$scheme" \
  -destination "$dest" \
  -derivedDataPath "$XBIN_CI_DERIVED/hosted" \
  -clonedSourcePackagesDirPath "$XBIN_CI_SPM" \
  -resultBundlePath "$XBIN_CI_OUT/hosted-snapshots.xcresult" \
  -skipMacroValidation \
  -skipPackagePluginValidation \
  COMPILER_INDEX_STORE_ENABLE=NO \
  CODE_SIGNING_ALLOWED=NO \
  ${CI_COND[@]+"${CI_COND[@]}"} || status=$?

pngs=$(find "$snap" -type f -name '*.png' | wc -l | tr -d ' ')
if [ "$status" -eq 0 ]; then
  ci_summary "**Hosted snapshots:** \`$scheme\` passed on \`$dest\` — $pngs PNG under hosted/ in the snapshots artifact."
else
  ci_error "xcodebuild test -scheme $scheme failed (exit $status); hosted-snapshots.log and .xcresult are in the xcresult-snapshots artifact"
  ci_summary "**Hosted snapshots:** \`$scheme\` **failed** (exit $status) — $pngs PNG under hosted/; see xcresult-snapshots."
fi
if [ "$pngs" -eq 0 ]; then
  ci_warn "the hosted snapshot tests wrote no PNG to $snap"
fi
exit "$status"
