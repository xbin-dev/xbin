#!/usr/bin/env bash
# native/ios/scripts/ci-dry-test.sh — run pick-sim.sh and the ci-*.sh
# scripts on Linux against fake xcrun / xcodebuild / xcodegen / swift, in a
# scratch copy of the tree, the way .github/workflows/ios.yml calls them
# (GITHUB_ACTIONS, RUNNER_TEMP, GITHUB_OUTPUT/ENV/STEP_SUMMARY set). It
# checks the decisions the scripts make — which simulator, which scheme,
# the xcodebuild arguments, skip/fail/exit codes, and that results land
# where the workflow uploads them from. It cannot check Apple tools
# themselves; only a CI run does. Part of ci-local-check.sh.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ci-dry-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

fails=0 passes=0
ok() { passes=$((passes + 1)); echo "ok   $*"; }
bad() { fails=$((fails + 1)); echo "FAIL $*"; }
eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: got [$2], want [$3]"; fi; }
has() { if printf '%s\n' "$2" | grep -qF -- "$3"; then ok "$1"; else bad "$1: [$3] not in: $2"; fi; }
isdir() { if [ -d "$2" ]; then ok "$1"; else bad "$1: no directory $2"; fi; }
hasnt() { if printf '%s\n' "$2" | grep -qF -- "$3"; then bad "$1: [$3] unexpectedly in: $2"; else ok "$1"; fi; }

# ---- fakes ---------------------------------------------------------------
bin=$tmp/bin
mkdir -p "$bin"
export FAKE_LOG=$tmp/fake.log

cat >"$bin/xcrun" <<'SH'
#!/bin/sh
# fake xcrun: simctl list -j | create | bootstatus | list <kind>; xcresulttool export attachments
echo "xcrun $*" >>"$FAKE_LOG"
case "$1" in
simctl)
  case "$2" in
  list) if [ "${3:-}" = -j ]; then cat "$FAKE_SIMCTL_JSON"; else echo "== fake simctl list $3 =="; fi ;;
  create) echo "${FAKE_CREATED_UDID:-99999999-0000-4000-8000-00000000C0DE}" ;;
  bootstatus) exit "${FAKE_BOOT_STATUS:-0}" ;;
  *) exit 64 ;;
  esac
  ;;
xcresulttool)
  out=""
  while [ $# -gt 0 ]; do
    [ "$1" = --output-path ] && out=$2
    shift
  done
  [ -n "$out" ] || exit 64
  mkdir -p "$out"
  if [ "${FAKE_ATTACH:-0}" = 1 ]; then printf 'PNG' >"$out/0A1B-counter-light.png"; fi
  echo '[]' >"$out/manifest.json"
  ;;
*) exit 64 ;;
esac
SH

cat >"$bin/xcodebuild" <<'SH'
#!/bin/sh
# fake xcodebuild: -version, -showsdks, -list, build/test (-resultBundlePath)
echo "xcodebuild $*" >>"$FAKE_LOG"
case "$1" in
-version) printf 'Xcode 27.0\nBuild version 27A5000a\n'; exit 0 ;;
-showsdks)
  printf 'iOS SDKs:\n\tiOS 27.0                      \t-sdk iphoneos27.0\n\niOS Simulator SDKs:\n\tSimulator - iOS 27.0          \t-sdk iphonesimulator27.0\n'
  exit 0 ;;
-list)
  printf 'Information about project "Fake":\n    Targets:\n        Xbin\n        XbinRenderer\n\n    Schemes:\n'
  for s in ${FAKE_SCHEMES:-}; do printf '        %s\n' "$s"; done
  printf '\n'
  exit 0 ;;
esac
bundle=""
action=""
prev=""
for a in "$@"; do
  [ "$prev" = -resultBundlePath ] && bundle=$a
  case "$a" in build | test) action=$a ;; esac
  prev=$a
done
echo "env SNAPSHOT_DIR=${TEST_RUNNER_SNAPSHOT_DIR:-} FIXTURES_DIR=${TEST_RUNNER_FIXTURES_DIR:-}" >>"$FAKE_LOG"
[ -n "$bundle" ] && mkdir -p "$bundle"
echo "** fake $action **"
if [ "$action" = test ] && [ "${FAKE_PNGS:-0}" -gt 0 ] && [ -n "${TEST_RUNNER_SNAPSHOT_DIR:-}" ]; then
  i=0
  while [ "$i" -lt "$FAKE_PNGS" ]; do
    printf 'PNG' >"$TEST_RUNNER_SNAPSHOT_DIR/fixture$i-light.png"
    i=$((i + 1))
  done
fi
exit "${FAKE_XCODEBUILD_STATUS:-0}"
SH

cat >"$bin/xcodegen" <<'SH'
#!/bin/sh
echo "xcodegen $* (in $(basename "$PWD"))" >>"$FAKE_LOG"
case "$1" in
--version) echo "Version: 2.44.1" ;;
generate) mkdir -p "${FAKE_PROJECT:-Xbin}.xcodeproj" ;;
*) exit 64 ;;
esac
SH

cat >"$bin/swift" <<'SH'
#!/bin/sh
case "$1" in
--version) echo "Swift version 6.4 (fake)"; exit 0 ;;
test)
  echo "swift test in $(basename "$PWD")" >>"$FAKE_LOG"
  case "$(basename "$PWD")" in Bad*) exit 1 ;; esac
  exit 0 ;;
esac
exit 64
SH

cat >"$bin/xcbeautify" <<'SH'
#!/bin/sh
case "${1:-}" in
--version) echo "2.30.1"; exit 0 ;;
--help)
  if [ "${FAKE_XCB_OLD:-0}" = 1 ]; then echo "USAGE: xcbeautify [--quiet]"; else echo "  --renderer <renderer>"; fi
  exit 0 ;;
esac
echo "xcbeautify $*" >>"$FAKE_LOG"
cat
SH

cat >"$bin/xcode-select" <<'SH'
#!/bin/sh
echo /Applications/Xcode_27.0.app/Contents/Developer
SH

chmod +x "$bin"/*
export PATH="$bin:$PATH"

# A scratch tree: the scripts at their real path, so XBIN_REPO resolves here.
repo=$tmp/repo
mkdir -p "$repo/native/ios/scripts" "$repo/native/fixtures"
cp "$here"/*.sh "$repo/native/ios/scripts/"
cp -R "$here/testdata" "$repo/native/ios/scripts/"
S=$repo/native/ios/scripts
td=$here/testdata

# Actions environment, as a runner sets it.
reset_env() {
  rm -rf "$tmp/runner"
  mkdir -p "$tmp/runner/temp"
  export GITHUB_ACTIONS=true
  export RUNNER_TEMP=$tmp/runner/temp
  export GITHUB_STEP_SUMMARY=$tmp/runner/summary.md GITHUB_ENV=$tmp/runner/env GITHUB_OUTPUT=$tmp/runner/output
  : >"$GITHUB_STEP_SUMMARY"
  : >"$GITHUB_ENV"
  : >"$GITHUB_OUTPUT"
  : >"$FAKE_LOG"
  unset XBIN_SIM XBIN_XCODE DEVELOPER_DIR TEST_RUNNER_SNAPSHOT_DIR TEST_RUNNER_FIXTURES_DIR XBIN_CI_OUT XBIN_CI_DERIVED \
    FAKE_SCHEMES FAKE_PNGS FAKE_ATTACH FAKE_XCODEBUILD_STATUS FAKE_BOOT_STATUS FAKE_PROJECT FAKE_XCB_OLD XCBEAUTIFY
  export XCBEAUTIFY=0 # off unless a case turns it on
}

# run <cmd…> — sets $out (stdout+stderr) and $rc.
run() {
  rc=0
  out=$("$@" 2>&1 </dev/null) || rc=$?
}

# ---- pick-sim.sh ---------------------------------------------------------
reset_env
export FAKE_SIMCTL_JSON=$td/simctl-typical.json
run "$S/pick-sim.sh"
eq "pick-sim: newest runtime, newest iPhone generation, base model" "$rc:$(printf '%s\n' "$out" | tail -n 1)" \
  "0:platform=iOS Simulator,id=BBBBBBBB-0000-4000-8000-000000002714"
has "pick-sim: names its choice on stderr" "$out" "iPhone 17 (iOS 27.1)"
dest=$("$S/pick-sim.sh" 2>/dev/null)
eq "pick-sim: stdout is only the destination" "$dest" "platform=iOS Simulator,id=BBBBBBBB-0000-4000-8000-000000002714"

XBIN_SIM="iPhone 17 Pro" run "$S/pick-sim.sh"
eq "pick-sim: XBIN_SIM picks that name on the newest runtime" "$(printf '%s\n' "$out" | tail -n 1)" \
  "platform=iOS Simulator,id=BBBBBBBB-0000-4000-8000-000000002711"
XBIN_SIM="iPhone 99" run "$S/pick-sim.sh"
eq "pick-sim: unknown XBIN_SIM falls back to the rule" "$rc:$(printf '%s\n' "$out" | tail -n 1)" \
  "0:platform=iOS Simulator,id=BBBBBBBB-0000-4000-8000-000000002714"
has "pick-sim: unknown XBIN_SIM is reported" "$out" "is not an available iOS simulator"

export FAKE_SIMCTL_JSON=$td/simctl-ipad-only.json
run "$S/pick-sim.sh"
eq "pick-sim: no iPhone → an iPad" "$rc:$(printf '%s\n' "$out" | tail -n 1)" \
  "0:platform=iOS Simulator,id=EEEEEEEE-0000-4000-8000-000000002702"

export FAKE_SIMCTL_JSON=$td/simctl-no-devices.json
: >"$FAKE_LOG"
run "$S/pick-sim.sh"
eq "pick-sim: no simulator → creates one" "$rc:$(printf '%s\n' "$out" | tail -n 1)" \
  "0:platform=iOS Simulator,id=99999999-0000-4000-8000-00000000C0DE"
has "pick-sim: creates the newest iPhone on the newest runtime" "$(cat "$FAKE_LOG")" \
  "xcrun simctl create xbin-ci iPhone 17 com.apple.CoreSimulator.SimDeviceType.iPhone-17 com.apple.CoreSimulator.SimRuntime.iOS-27-1"

export FAKE_SIMCTL_JSON=$td/simctl-no-ios-runtime.json
run "$S/pick-sim.sh"
if [ "$rc" -ne 0 ]; then ok "pick-sim: no usable iOS runtime fails (exit $rc)"; else bad "pick-sim: no iOS runtime exited 0: $out"; fi
has "pick-sim: says why" "$out" "no available iOS simulator runtime"
hasnt "pick-sim: prints no destination on failure" "$out" "platform=iOS Simulator"
export FAKE_SIMCTL_JSON=$td/simctl-typical.json

# ---- ci-lib.sh -----------------------------------------------------------
# lib <function> <args…> — call a ci-lib.sh function (the scratch copy's) in
# a subshell.
lib() {
  # shellcheck source=SCRIPTDIR/ci-lib.sh
  (. "$S/ci-lib.sh" && "$@")
}
parsed=$(printf 'Information about project "Xbin":\n    Targets:\n        Xbin\n        XbinTests\n\n    Build Configurations:\n        Debug\n        Release\n\n    If no build configuration is specified and -scheme is not passed then "Debug" is used.\n\n    Schemes:\n        Xbin\n        XbinUITests\n\n' | lib ci_schemes_in | tr '\n' ' ')
eq "ci_schemes_in: only the Schemes section" "$parsed" "Xbin XbinUITests "
parsed=$(printf 'Information about workspace "XbinRenderer":\n    Schemes:\n        XbinRenderer\n' | lib ci_schemes_in)
eq "ci_schemes_in: a package listing" "$parsed" "XbinRenderer"
eq "ci_udid" "$(lib ci_udid "platform=iOS Simulator,id=ABC-123")" "ABC-123"
eq "ci_udid: stops at the next key" "$(lib ci_udid "platform=iOS Simulator,id=ABC-123,arch=arm64")" "ABC-123"
rc=0
lib ci_timeout 1 sleep 5 || rc=$?
eq "ci_timeout kills a slow command (124)" "$rc" 124
rc=0
lib ci_timeout 5 sh -c 'exit 3' || rc=$?
eq "ci_timeout passes the command's status" "$rc" 3
eq "ci-lib: XBIN_REPO is the tree root" "$(lib printenv XBIN_REPO)" "$repo"

# ---- ci-toolchain.sh -----------------------------------------------------
reset_env
run "$S/ci-toolchain.sh"
eq "ci-toolchain: runs" "$rc" 0
has "ci-toolchain: logs xcodebuild -version" "$out" "Xcode 27.0"
has "ci-toolchain: lists device types (own call)" "$(cat "$FAKE_LOG")" "xcrun simctl list devicetypes"
has "ci-toolchain: lists runtimes (own call)" "$(cat "$FAKE_LOG")" "xcrun simctl list runtimes"
has "ci-toolchain: summary line" "$(cat "$GITHUB_STEP_SUMMARY")" "iOS SDKs: iphoneos27.0 iphonesimulator27.0"
mkdir -p "$tmp/Xcode_27.1.app/Contents/Developer"
XBIN_XCODE=$tmp/Xcode_27.1.app run "$S/ci-toolchain.sh"
eq "ci-toolchain: XBIN_XCODE runs" "$rc" 0
eq "ci-toolchain: XBIN_XCODE → DEVELOPER_DIR for later steps" "$(cat "$GITHUB_ENV")" \
  "DEVELOPER_DIR=$tmp/Xcode_27.1.app/Contents/Developer"
XBIN_XCODE=$tmp/nope.app run "$S/ci-toolchain.sh"
eq "ci-toolchain: a missing XBIN_XCODE fails" "$rc" 1

# ---- ci-swift-test.sh ----------------------------------------------------
reset_env
run "$S/ci-swift-test.sh"
eq "ci-swift-test: no packages → success" "$rc" 0
has "ci-swift-test: says so" "$out" "no SwiftPM package present"
mkdir -p "$repo/native/ios/Packages/Good" "$repo/native/ios/Packages/Bad"
touch "$repo/native/ios/Packages/Good/Package.swift" "$repo/native/ios/Packages/Bad/Package.swift"
run "$S/ci-swift-test.sh" Bad Missing Good
eq "ci-swift-test: a failing package fails the step" "$rc" 1
has "ci-swift-test: later packages still run" "$(cat "$FAKE_LOG")" "swift test in Good"
has "ci-swift-test: summary marks the failure" "$(cat "$GITHUB_STEP_SUMMARY")" "| Bad | **failed** (exit 1) |"
has "ci-swift-test: summary marks the skip" "$(cat "$GITHUB_STEP_SUMMARY")" "| Missing | skipped (not present) |"
has "ci-swift-test: error annotation" "$out" "::error::Bad: swift test failed"
run "$S/ci-swift-test.sh" Good
eq "ci-swift-test: passing package" "$rc" 0
rm -rf "$repo/native/ios/Packages/Good" "$repo/native/ios/Packages/Bad"

# ---- ci-build-app.sh -----------------------------------------------------
reset_env
run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: no project.yml → success" "$rc" 0
has "ci-build-app: notice" "$out" "::notice::no native/ios/project.yml yet"
hasnt "ci-build-app: no project.yml → no xcodebuild" "$(cat "$FAKE_LOG")" "xcodebuild build"
echo "name: Xbin" >"$repo/native/ios/project.yml"
export FAKE_SCHEMES="Xbin"
run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: builds" "$rc" 0
log=$(cat "$FAKE_LOG")
has "ci-build-app: xcodegen generate in native/ios" "$log" "xcodegen generate --spec project.yml (in ios)"
has "ci-build-app: build -scheme Xbin on the picked simulator, unsigned" "$log" \
  "xcodebuild build -project Xbin.xcodeproj -scheme Xbin -configuration Debug -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/app -resultBundlePath $RUNNER_TEMP/xbin-ci/app-build.xcresult -skipMacroValidation -skipPackagePluginValidation CODE_SIGNING_ALLOWED=NO"
# The workflow uploads exactly these two paths (xcresult-app).
isdir "ci-build-app: xcresult where ios.yml uploads it" "$RUNNER_TEMP/xbin-ci/app-build.xcresult"
has "ci-build-app: full log where ios.yml uploads it" "$(cat "$RUNNER_TEMP/xbin-ci/app-build.log" 2>/dev/null)" "** fake build **"
has "ci-build-app: summary" "$(cat "$GITHUB_STEP_SUMMARY")" "**App:** \`Xbin\` built"
FAKE_XCODEBUILD_STATUS=65 run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: xcodebuild's failure is the step's" "$rc" 65
has "ci-build-app: failure annotation" "$out" "::error::xcodebuild build -scheme Xbin failed (exit 65)"
FAKE_SCHEMES="Other" run "$S/ci-build-app.sh" "$dest"
has "ci-build-app: warns when project.yml declares no Xbin scheme" "$out" "::warning::Xbin.xcodeproj lists no scheme \"Xbin\""
mkdir -p "$repo/native/ios/Stale.xcodeproj"
run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: a second (committed) .xcodeproj fails" "$rc" 1
rm -rf "$repo/native/ios/Stale.xcodeproj" "$repo/native/ios/Xbin.xcodeproj"
# Without xcodegen: the fakes minus it, and only system dirs after them.
bin2=$tmp/bin-no-xcodegen
mkdir -p "$bin2"
for f in "$bin"/*; do [ "$(basename "$f")" = xcodegen ] || ln -s "$f" "$bin2/"; done
PATH="$bin2:/usr/local/bin:/usr/bin:/bin" run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: no xcodegen fails" "$rc" 1
has "ci-build-app: …and says how to get it" "$out" "brew install xcodegen"
# xcbeautify, when the runner has it: the github-actions renderer in
# Actions, xcodebuild's status and the raw log kept.
reset_env
unset XCBEAUTIFY
export FAKE_SCHEMES="Xbin"
FAKE_XCODEBUILD_STATUS=65 run "$S/ci-build-app.sh" "$dest"
eq "xcbeautify: xcodebuild's status survives the pipe" "$rc" 65
eq "xcbeautify: github-actions renderer in Actions" "$(grep '^xcbeautify' "$FAKE_LOG")" "xcbeautify --renderer github-actions"
has "xcbeautify: output still shown" "$out" "** fake build **"
has "xcbeautify: raw log kept" "$(cat "$RUNNER_TEMP/xbin-ci/app-build.log")" "** fake build **"
: >"$FAKE_LOG"
FAKE_XCB_OLD=1 run "$S/ci-build-app.sh" "$dest"
eq "xcbeautify: one without --renderer runs plain" "$(grep '^xcbeautify' "$FAKE_LOG")" "xcbeautify "
: >"$FAKE_LOG"
GITHUB_ACTIONS="" run "$S/ci-build-app.sh" "$dest"
eq "xcbeautify: outside Actions runs plain" "$rc:$(grep '^xcbeautify' "$FAKE_LOG")" "0:xcbeautify "
rm -f "$repo/native/ios/project.yml"
rm -rf "$repo/native/ios/Xbin.xcodeproj"

# ---- ci-snapshots.sh -----------------------------------------------------
reset_env
run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: no XbinRenderer → success" "$rc" 0
has "ci-snapshots: notice" "$out" "::notice::no native/ios/Packages/XbinRenderer yet"
mkdir -p "$repo/native/ios/Packages/XbinRenderer"
touch "$repo/native/ios/Packages/XbinRenderer/Package.swift"
export TEST_RUNNER_SNAPSHOT_DIR=$RUNNER_TEMP/snapshots # as ios.yml sets it
export FAKE_SCHEMES="XbinRenderer" FAKE_PNGS=3
run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: passes" "$rc" 0
log=$(cat "$FAKE_LOG")
has "ci-snapshots: boots the picked simulator first" "$log" "xcrun simctl bootstatus BBBBBBBB-0000-4000-8000-000000002714 -b"
has "ci-snapshots: xcodebuild test -scheme XbinRenderer" "$log" \
  "xcodebuild test -scheme XbinRenderer -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/renderer -resultBundlePath $RUNNER_TEMP/xbin-ci/snapshots-test.xcresult -skipMacroValidation -skipPackagePluginValidation CODE_SIGNING_ALLOWED=NO"
has "ci-snapshots: the tests get SNAPSHOT_DIR and FIXTURES_DIR" "$log" \
  "env SNAPSHOT_DIR=$RUNNER_TEMP/snapshots FIXTURES_DIR=$repo/native/fixtures"
eq "ci-snapshots: PNGs where ios.yml uploads them" "$(find "$RUNNER_TEMP/snapshots" -name '*.png' | wc -l | tr -d ' ')" 3
isdir "ci-snapshots: xcresult where ios.yml uploads it" "$RUNNER_TEMP/xbin-ci/snapshots-test.xcresult"
has "ci-snapshots: summary counts PNGs" "$(cat "$GITHUB_STEP_SUMMARY")" "3 PNG in the snapshots artifact"
FAKE_XCODEBUILD_STATUS=65 run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: a failing test fails the step" "$rc" 65
eq "ci-snapshots: …and its PNGs are still there" "$(find "$RUNNER_TEMP/snapshots" -name '*.png' | wc -l | tr -d ' ')" 3
reset_env
export TEST_RUNNER_SNAPSHOT_DIR=$RUNNER_TEMP/snapshots
FAKE_SCHEMES="XbinRenderer" FAKE_PNGS=0 FAKE_ATTACH=1 run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: attachments-only run passes" "$rc" 0
has "ci-snapshots: exports result-bundle attachments when no PNG was written" "$(cat "$FAKE_LOG")" \
  "xcrun xcresulttool export attachments --path $RUNNER_TEMP/xbin-ci/snapshots-test.xcresult --output-path $RUNNER_TEMP/snapshots/attachments"
eq "ci-snapshots: exported PNGs are uploaded" "$(find "$RUNNER_TEMP/snapshots" -name '*.png' | wc -l | tr -d ' ')" 1
reset_env
FAKE_SCHEMES="XbinRenderer-Package" FAKE_PNGS=0 run "$S/ci-snapshots.sh" "$dest"
has "ci-snapshots: falls back to the -Package scheme" "$(cat "$FAKE_LOG")" "xcodebuild test -scheme XbinRenderer-Package"
has "ci-snapshots: warns when no PNG at all" "$out" "::warning::the XbinRenderer tests wrote no PNG"
eq "ci-snapshots: default SNAPSHOT_DIR is under XBIN_CI_OUT" "$(grep '^env ' "$FAKE_LOG" | head -n 1)" \
  "env SNAPSHOT_DIR=$RUNNER_TEMP/xbin-ci/snapshots FIXTURES_DIR=$repo/native/fixtures"
FAKE_SCHEMES="XbinRenderer" FAKE_BOOT_STATUS=1 run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: a failed pre-boot is only a warning" "$rc" 0
has "ci-snapshots: …reported" "$out" "::warning::simctl bootstatus"

echo
echo "ci-dry-test: $passes passed, $fails failed (bash $BASH_VERSION)"
[ "$fails" -eq 0 ]
