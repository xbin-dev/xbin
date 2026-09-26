#!/usr/bin/env bash
# native/ios/scripts/ci-build-app.sh — generate the Xcode project from
# native/ios/project.yml (XcodeGen; the .xcodeproj is never committed) and
# build the app scheme for a simulator, unsigned (the ios.yml "app" job).
#
#   ci-build-app.sh "<destination>"     e.g. "$(pick-sim.sh)"
#
#   XBIN_APP_SCHEME   the scheme to build (default Xbin)
#
# Results in $XBIN_CI_OUT (default $RUNNER_TEMP/xbin-ci): app-build.xcresult
# and the full xcodebuild log app-build.log. No project.yml yet → a notice,
# success. Needs xcodegen on PATH (brew install xcodegen).
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
command -v xcodegen >/dev/null 2>&1 || { ci_error "xcodegen not found (brew install xcodegen)"; exit 1; }

cd "$ios"
ci_group "xcodegen generate"
xcodegen generate --spec project.yml
ci_endgroup

# XcodeGen names the project after project.yml's `name:`; a fresh checkout
# holds exactly the one it just generated.
proj=""
for p in *.xcodeproj; do
  if [ -d "$p" ]; then
    if [ -n "$proj" ]; then
      ci_error "several .xcodeproj in native/ios ($proj, $p) — is one committed? (native/AGENTS.md: never commit it)"
      exit 1
    fi
    proj=$p
  fi
done
[ -n "$proj" ] || { ci_error "xcodegen generated no .xcodeproj in native/ios"; exit 1; }

ci_group "schemes in $proj"
schemes=$(ci_schemes -project "$proj") || true
echo "$schemes"
ci_endgroup
if ! printf '%s\n' "$schemes" | grep -qxF "$scheme"; then
  # xcodebuild has the final word; this only names the likely fix.
  ci_warn "$proj lists no scheme \"$scheme\" — project.yml should declare it (targets.$scheme.scheme or schemes.$scheme)"
fi

mkdir -p "$XBIN_CI_OUT"
rm -rf "$XBIN_CI_OUT/app-build.xcresult"
status=0
ci_xcodebuild "$XBIN_CI_OUT/app-build.log" build \
  -project "$proj" \
  -scheme "$scheme" \
  -configuration Debug \
  -destination "$dest" \
  -derivedDataPath "$XBIN_CI_DERIVED/app" \
  -resultBundlePath "$XBIN_CI_OUT/app-build.xcresult" \
  -skipMacroValidation \
  -skipPackagePluginValidation \
  CODE_SIGNING_ALLOWED=NO || status=$?

if [ "$status" -eq 0 ]; then
  ci_summary "**App:** \`$scheme\` built for \`$dest\`."
else
  ci_error "xcodebuild build -scheme $scheme failed (exit $status); the full log and .xcresult are in the xcresult-app artifact"
  ci_summary "**App:** \`$scheme\` build **failed** (exit $status) — see the xcresult-app artifact."
fi
exit "$status"
