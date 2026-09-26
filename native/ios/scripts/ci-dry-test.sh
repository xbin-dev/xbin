#!/usr/bin/env bash
# native/ios/scripts/ci-dry-test.sh — run pick-sim.sh and the ci-*.sh
# scripts on Linux against fake xcrun / xcodebuild / xcodegen / swift, in a
# scratch copy of the tree, the way .github/workflows/ios.yml calls them
# (GITHUB_ACTIONS, RUNNER_TEMP, GITHUB_OUTPUT/ENV/STEP_SUMMARY set). It
# checks the decisions the scripts make — which simulator, which scheme,
# the xcodebuild arguments, skip/fail/exit codes, and that results land
# where the workflow uploads them from. It cannot check Apple tools
# themselves; only a CI run does. Part of ci-local-check.sh; the fakes and
# helpers are dry-lib.sh's (mac-dry-test.sh covers the Mac scripts).
set -euo pipefail
# shellcheck source=SCRIPTDIR/dry-lib.sh
. "$(dirname "$0")/dry-lib.sh"


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

: >"$FAKE_LOG"
XBIN_SIM_ENSURE=xbin-e2e run "$S/pick-sim.sh"
eq "pick-sim: XBIN_SIM_ENSURE creates a missing device" "$rc:$(printf '%s\n' "$out" | tail -n 1)" \
  "0:platform=iOS Simulator,id=99999999-0000-4000-8000-00000000C0DE"
has "pick-sim: …with exactly that name, newest iPhone on the newest runtime" "$(cat "$FAKE_LOG")" \
  "xcrun simctl create xbin-e2e com.apple.CoreSimulator.SimDeviceType.iPhone-17 com.apple.CoreSimulator.SimRuntime.iOS-27-1"
XBIN_SIM_ENSURE="iPhone 16e" run "$S/pick-sim.sh"
eq "pick-sim: XBIN_SIM_ENSURE uses an existing device of that name" "$(printf '%s\n' "$out" | tail -n 1)" \
  "platform=iOS Simulator,id=BBBBBBBB-0000-4000-8000-000000002715"

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
eq "ci-lib: SwiftPM clones sit in the DerivedData dir" "$(XBIN_CI_DERIVED=/d lib printenv XBIN_CI_SPM)" "/d/SourcePackages"
eq "ci_xcode_slug" "$(lib ci_xcode_slug)" "27.0-27A5000a"
eq "ci_sha256" "$(printf abc | lib ci_sha256)" "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
eq "ci_content_stamps: blob 00000000… → 2001-01-01 00:00:00" \
  "$(printf '100644 0000000000000000000000000000000000000000 0\tnative/ios/a b.swift\n' | lib ci_content_stamps)" \
  "200101010000.00 native/ios/a b.swift"
eq "ci_content_stamps: blob ffffffff… → the far end, still in the past" \
  "$(printf '100644 ffffffff00000000000000000000000000000000 0\tx\n' | lib ci_content_stamps)" \
  "201604020348.14 x"
eq "ci_content_stamps: every digit counts" \
  "$(printf '100644 00000001aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0\tx\n' | lib ci_content_stamps | cut -d' ' -f1)" \
  "200201010000.00"
eq "ci_content_mtimes: outside git does nothing" "$(lib ci_content_mtimes native/ios)" 0
signing() { (. "$S/ci-lib.sh" && ci_signing && echo "${CI_SIGN[*]}"); }
eq "ci_signing: unsigned by default" "$(signing)" "CODE_SIGNING_ALLOWED=NO"
eq "ci_signing: adhoc signs to run locally, no team" "$(XBIN_SIGNING=adhoc signing)" \
  "CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER="
rc=0
XBIN_SIGNING=team signing >/dev/null 2>&1 || rc=$?
eq "ci_signing: anything else fails" "$rc" 1

# ---- ci-toolchain.sh -----------------------------------------------------
reset_env
run "$S/ci-toolchain.sh"
eq "ci-toolchain: runs" "$rc" 0
has "ci-toolchain: logs xcodebuild -version" "$out" "Xcode 27.0"
has "ci-toolchain: lists device types (own call)" "$(cat "$FAKE_LOG")" "xcrun simctl list devicetypes"
has "ci-toolchain: lists runtimes (own call)" "$(cat "$FAKE_LOG")" "xcrun simctl list runtimes"
has "ci-toolchain: summary line" "$(cat "$GITHUB_STEP_SUMMARY")" "iOS SDKs: iphoneos27.0 iphonesimulator27.0"
eq "ci-toolchain: step output xcode (cache keys)" "$(cat "$GITHUB_OUTPUT")" "xcode=27.0-27A5000a"
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
  "xcodebuild build -project Xbin.xcodeproj -scheme Xbin -configuration Debug -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/app -clonedSourcePackagesDirPath $RUNNER_TEMP/xbin-derived/SourcePackages -resultBundlePath $RUNNER_TEMP/xbin-ci/app-build.xcresult -skipMacroValidation -skipPackagePluginValidation -IDEBuildingContinueBuildingAfterErrors=YES COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGNING_ALLOWED=NO"
hasnt "ci-build-app: Metal toolchain present → no download" "$log" "-downloadComponent"
# The workflow uploads exactly these two paths (xcresult-app).
isdir "ci-build-app: xcresult where ios.yml uploads it" "$RUNNER_TEMP/xbin-ci/app-build.xcresult"
has "ci-build-app: full log where ios.yml uploads it" "$(cat "$RUNNER_TEMP/xbin-ci/app-build.log" 2>/dev/null)" "** fake build **"
has "ci-build-app: summary" "$(cat "$GITHUB_STEP_SUMMARY")" "**App:** \`Xbin\` built"
: >"$FAKE_LOG"
FAKE_METAL_STATUS=1 run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: Metal toolchain missing → still builds" "$rc" 0
has "ci-build-app: …after downloading it" "$(cat "$FAKE_LOG")" "xcodebuild -downloadComponent MetalToolchain"
FAKE_METAL_STATUS=1 FAKE_DOWNLOAD_STATUS=1 run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: a failed Metal download leaves the verdict to the build" "$rc" 0
has "ci-build-app: …and warns" "$out" "::warning::xcodebuild -downloadComponent MetalToolchain failed"
: >"$FAKE_LOG"
XBIN_SIGNING=adhoc XBIN_CI_DERIVED=$tmp/dd run "$S/ci-build-app.sh" "$dest"
has "ci-build-app: XBIN_SIGNING=adhoc signs to run locally; DerivedData and SwiftPM dirs follow XBIN_CI_DERIVED" "$(cat "$FAKE_LOG")" \
  "-derivedDataPath $tmp/dd/app -clonedSourcePackagesDirPath $tmp/dd/SourcePackages -resultBundlePath $RUNNER_TEMP/xbin-ci/app-build.xcresult -skipMacroValidation -skipPackagePluginValidation -IDEBuildingContinueBuildingAfterErrors=YES COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER="
: >"$FAKE_LOG"
XBIN_SWIFT_CONDITIONS=" XBIN_SDK_27_1  XBIN_OTHER " run "$S/ci-build-app.sh" "$dest"
has "ci-build-app: XBIN_SWIFT_CONDITIONS adds compilation conditions to the target's own" "$(cat "$FAKE_LOG")" \
  "COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGNING_ALLOWED=NO SWIFT_ACTIVE_COMPILATION_CONDITIONS=\$(inherited) XBIN_SDK_27_1 XBIN_OTHER"
: >"$FAKE_LOG"
run "$S/ci-build-app.sh" "$dest"
hasnt "ci-build-app: …and nothing without it" "$(cat "$FAKE_LOG")" "SWIFT_ACTIVE_COMPILATION_CONDITIONS"
: >"$FAKE_LOG"
XBIN_SIGNING=bogus run "$S/ci-build-app.sh" "$dest"
eq "ci-build-app: an unknown XBIN_SIGNING fails before building" "$rc:$(grep -c 'xcodebuild build' "$FAKE_LOG" || true)" "1:0"
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
noxg=$(path_without xcodegen)
PATH="$noxg" run "$S/ci-build-app.sh" "$dest"
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
has "ci-snapshots: …while xcodebuild loads the package (-list)" "$log" "xcodebuild -list"
has "ci-snapshots: the listing is timed" "$out" "schemes in XbinRenderer (xcodebuild -list: "
has "ci-snapshots: xcodebuild test -scheme XbinRenderer" "$log" \
  "xcodebuild test -scheme XbinRenderer -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/renderer -clonedSourcePackagesDirPath $RUNNER_TEMP/xbin-derived/SourcePackages -resultBundlePath $RUNNER_TEMP/xbin-ci/snapshots-test.xcresult -skipMacroValidation -skipPackagePluginValidation COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGNING_ALLOWED=NO"
has "ci-snapshots: the tests get SNAPSHOT_DIR and FIXTURES_DIR" "$log" \
  "env SNAPSHOT_DIR=$RUNNER_TEMP/snapshots FIXTURES_DIR=$repo/native/fixtures"
eq "ci-snapshots: PNGs where ios.yml uploads them" "$(find "$RUNNER_TEMP/snapshots" -name '*.png' | wc -l | tr -d ' ')" 3
isdir "ci-snapshots: xcresult where ios.yml uploads it" "$RUNNER_TEMP/xbin-ci/snapshots-test.xcresult"
has "ci-snapshots: summary counts PNGs" "$(cat "$GITHUB_STEP_SUMMARY")" "3 PNG in the snapshots artifact"
: >"$FAKE_LOG"
XBIN_SWIFT_CONDITIONS=XBIN_SDK_27_1 run "$S/ci-snapshots.sh" "$dest"
has "ci-snapshots: XBIN_SWIFT_CONDITIONS reaches the renderer's tests" "$(cat "$FAKE_LOG")" \
  "CODE_SIGNING_ALLOWED=NO SWIFT_ACTIVE_COMPILATION_CONDITIONS=\$(inherited) XBIN_SDK_27_1"
: >"$FAKE_LOG"
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
FAKE_SCHEMES="XbinRenderer-Package" FAKE_STRICT_SCHEMES=1 FAKE_PNGS=0 run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: a missing XbinRenderer scheme is not a failure…" "$rc" 0
has "ci-snapshots: …it falls back to the -Package scheme" "$(cat "$FAKE_LOG")" "xcodebuild test -scheme XbinRenderer-Package"
hasnt "ci-snapshots: …without trying the missing one" "$(cat "$FAKE_LOG")" "xcodebuild test -scheme XbinRenderer "
has "ci-snapshots: warns when no PNG at all" "$out" "::warning::the XbinRenderer tests wrote no PNG"
eq "ci-snapshots: default SNAPSHOT_DIR is under XBIN_CI_OUT" "$(grep '^env ' "$FAKE_LOG" | head -n 1)" \
  "env SNAPSHOT_DIR=$RUNNER_TEMP/xbin-ci/snapshots FIXTURES_DIR=$repo/native/fixtures"
FAKE_SCHEMES="XbinRenderer" FAKE_BOOT_STATUS=1 run "$S/ci-snapshots.sh" "$dest"
eq "ci-snapshots: a failed pre-boot is only a warning" "$rc" 0
has "ci-snapshots: …reported" "$out" "::warning::simctl bootstatus"

# ---- ci-hosted-snapshots.sh ----------------------------------------------
reset_env
run "$S/ci-hosted-snapshots.sh" "$dest"
eq "ci-hosted-snapshots: no project.yml → success" "$rc" 0
has "ci-hosted-snapshots: notice" "$out" "::notice::no native/ios/project.yml yet"
hasnt "ci-hosted-snapshots: no project.yml → no xcodebuild" "$(cat "$FAKE_LOG")" "xcodebuild test"
echo "name: Xbin" >"$repo/native/ios/project.yml"
export TEST_RUNNER_SNAPSHOT_DIR=$RUNNER_TEMP/snapshots/hosted # as ios.yml sets it
export FAKE_PNGS=2
run "$S/ci-hosted-snapshots.sh" "$dest"
eq "ci-hosted-snapshots: passes" "$rc" 0
log=$(cat "$FAKE_LOG")
has "ci-hosted-snapshots: xcodegen generate in native/ios" "$log" "xcodegen generate --spec project.yml (in ios)"
has "ci-hosted-snapshots: xcodebuild test -scheme XbinSnapshots, unsigned" "$log" \
  "xcodebuild test -project Xbin.xcodeproj -scheme XbinSnapshots -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/hosted -clonedSourcePackagesDirPath $RUNNER_TEMP/xbin-derived/SourcePackages -resultBundlePath $RUNNER_TEMP/xbin-ci/hosted-snapshots.xcresult -skipMacroValidation -skipPackagePluginValidation COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGNING_ALLOWED=NO"
has "ci-hosted-snapshots: the tests get SNAPSHOT_DIR and FIXTURES_DIR" "$log" \
  "env SNAPSHOT_DIR=$RUNNER_TEMP/snapshots/hosted FIXTURES_DIR=$repo/native/fixtures"
eq "ci-hosted-snapshots: PNGs under the uploaded snapshots dir" "$(find "$RUNNER_TEMP/snapshots/hosted" -name '*.png' | wc -l | tr -d ' ')" 2
isdir "ci-hosted-snapshots: xcresult where ios.yml uploads it" "$RUNNER_TEMP/xbin-ci/hosted-snapshots.xcresult"
has "ci-hosted-snapshots: full log where ios.yml uploads it" "$(cat "$RUNNER_TEMP/xbin-ci/hosted-snapshots.log" 2>/dev/null)" "** fake test **"
has "ci-hosted-snapshots: summary counts PNGs" "$(cat "$GITHUB_STEP_SUMMARY")" "2 PNG under hosted/"
FAKE_XCODEBUILD_STATUS=65 run "$S/ci-hosted-snapshots.sh" "$dest"
eq "ci-hosted-snapshots: a failing test fails the step" "$rc" 65
has "ci-hosted-snapshots: failure annotation" "$out" "::error::xcodebuild test -scheme XbinSnapshots failed (exit 65)"
reset_env
echo "name: Xbin" >"$repo/native/ios/project.yml"
FAKE_PNGS=0 run "$S/ci-hosted-snapshots.sh" "$dest"
eq "ci-hosted-snapshots: default SNAPSHOT_DIR is under XBIN_CI_OUT" "$(grep '^env ' "$FAKE_LOG" | head -n 1)" \
  "env SNAPSHOT_DIR=$RUNNER_TEMP/xbin-ci/snapshots/hosted FIXTURES_DIR=$repo/native/fixtures"
has "ci-hosted-snapshots: warns when no PNG" "$out" "::warning::the hosted snapshot tests wrote no PNG"
PATH="$noxg" run "$S/ci-hosted-snapshots.sh" "$dest"
eq "ci-hosted-snapshots: no xcodegen fails" "$rc" 1
has "ci-hosted-snapshots: …and says how to get it" "$out" "brew install xcodegen"
rm -f "$repo/native/ios/project.yml"
rm -rf "$repo/native/ios/Xbin.xcodeproj"

# ---- ci-cache.sh ---------------------------------------------------------
mkdir -p "$repo/native/ios/Packages/XbinCore/.build/checkouts/dep" "$repo/native/ios/Packages/XbinRenderer"
echo "name: Xbin" >"$repo/native/ios/project.yml"
echo "// core" >"$repo/native/ios/Packages/XbinCore/Package.swift"
echo "// renderer" >"$repo/native/ios/Packages/XbinRenderer/Package.swift"
echo "// a dependency's own manifest" >"$repo/native/ios/Packages/XbinCore/.build/checkouts/dep/Package.swift"
outputs() { sed -n "s/^$1=//p" "$GITHUB_OUTPUT"; }
reset_env
run "$S/ci-cache.sh" app app
eq "ci-cache: hosted runner → actions/cache" "$rc:$(outputs mode)" "0:actions"
has "ci-cache: tells Xcode to ignore a checkout's new inodes" "$(cat "$FAKE_LOG")" \
  "defaults write com.apple.dt.XCBuild IgnoreFileSystemDeviceInodeChanges -bool YES"
key1=$(outputs key)
case $key1 in
ios-app-27.0-27A5000a-????????????????) ok "ci-cache: key is ios-<job>-<xcode>-<16 hex> ($key1)" ;;
*) bad "ci-cache: key shape: $key1" ;;
esac
eq "ci-cache: restore prefix is the job and Xcode" "$(outputs restore)" "ios-app-27.0-27A5000a-"
eq "ci-cache: later steps build under RUNNER_TEMP" "$(cat "$GITHUB_ENV")" \
  "XBIN_CI_DERIVED=$RUNNER_TEMP/xbin-derived
XBIN_CI_SPM=$RUNNER_TEMP/xbin-derived/SourcePackages"
eq "ci-cache: paths = SwiftPM clones + the job's DerivedData, minus logs and index" \
  "$(sed -n '/^paths<<XBIN_CI_PATHS$/,/^XBIN_CI_PATHS$/p' "$GITHUB_OUTPUT" | sed '1d;$d')" \
  "$RUNNER_TEMP/xbin-derived/SourcePackages
$RUNNER_TEMP/xbin-derived/app
!$RUNNER_TEMP/xbin-derived/app/Logs
!$RUNNER_TEMP/xbin-derived/app/Index.noindex"
reset_env
run "$S/ci-cache.sh" app app
eq "ci-cache: the key is stable for the same inputs" "$(outputs key)" "$key1"
reset_env
echo "// a dependency's manifest changed" >"$repo/native/ios/Packages/XbinCore/.build/checkouts/dep/Package.swift"
run "$S/ci-cache.sh" app app
eq "ci-cache: …and ignores SwiftPM's own checkouts under .build" "$(outputs key)" "$key1"
reset_env
echo "// core, changed" >"$repo/native/ios/Packages/XbinCore/Package.swift"
run "$S/ci-cache.sh" app app
if [ "$(outputs key)" != "$key1" ]; then ok "ci-cache: a Package.swift change makes a new key"; else bad "ci-cache: Package.swift change kept the key"; fi
echo "// core" >"$repo/native/ios/Packages/XbinCore/Package.swift"
reset_env
echo '{"pins":[]}' >"$repo/native/ios/Packages/XbinRenderer/Package.resolved"
run "$S/ci-cache.sh" app app
if [ "$(outputs key)" != "$key1" ]; then ok "ci-cache: a Package.resolved makes a new key"; else bad "ci-cache: Package.resolved ignored"; fi
rm -f "$repo/native/ios/Packages/XbinRenderer/Package.resolved"
reset_env
echo "name: Xbin # changed" >"$repo/native/ios/project.yml"
run "$S/ci-cache.sh" app app
if [ "$(outputs key)" != "$key1" ]; then ok "ci-cache: a project.yml change makes a new key"; else bad "ci-cache: project.yml change kept the key"; fi
echo "name: Xbin" >"$repo/native/ios/project.yml"
reset_env
XBIN_CI_CACHE_VERSION=2 run "$S/ci-cache.sh" app app
if [ "$(outputs key)" != "$key1" ]; then ok "ci-cache: XBIN_CI_CACHE_VERSION drops every entry"; else bad "ci-cache: XBIN_CI_CACHE_VERSION ignored"; fi
reset_env
run "$S/ci-cache.sh" snapshots renderer hosted
eq "ci-cache: one entry per job" "$(outputs restore)" "ios-snapshots-27.0-27A5000a-"
has "ci-cache: every subdir the job builds into" "$(cat "$GITHUB_OUTPUT")" "$RUNNER_TEMP/xbin-derived/renderer
!$RUNNER_TEMP/xbin-derived/renderer/Logs
!$RUNNER_TEMP/xbin-derived/renderer/Index.noindex
$RUNNER_TEMP/xbin-derived/hosted"
reset_env
RUNNER_ENVIRONMENT=self-hosted RUNNER_NAME="xbin mini" XBIN_CI_CACHE_ROOT=$tmp/cacheroot run "$S/ci-cache.sh" app app
eq "ci-cache: self-hosted → the runner's disk" "$rc:$(outputs mode)" "0:disk"
eq "ci-cache: …per runner and Xcode, outside RUNNER_TEMP" "$(head -n 1 "$GITHUB_ENV")" \
  "XBIN_CI_DERIVED=$tmp/cacheroot/xbin_mini/27.0-27A5000a/derived"
isdir "ci-cache: …created" "$tmp/cacheroot/xbin_mini/27.0-27A5000a/derived/SourcePackages"
has "ci-cache: …said on the summary" "$(cat "$GITHUB_STEP_SUMMARY")" "on this runner's disk"
reset_env
RUNNER_ENVIRONMENT=self-hosted XBIN_CI_CACHE=actions run "$S/ci-cache.sh" app app
eq "ci-cache: XBIN_CI_CACHE=actions overrides a self-hosted runner" "$(outputs mode):$(head -n 1 "$GITHUB_ENV")" \
  "actions:XBIN_CI_DERIVED=$RUNNER_TEMP/xbin-derived"
reset_env
XBIN_CI_CACHE=off run "$S/ci-cache.sh" app app
eq "ci-cache: XBIN_CI_CACHE=off" "$(outputs mode)" "off"
reset_env
XBIN_CI_CACHE=sometimes run "$S/ci-cache.sh" app app
eq "ci-cache: an unknown mode fails" "$rc" 2
run "$S/ci-cache.sh" app
eq "ci-cache: no subdir fails" "$rc" 2
# In a git checkout: the committed tree is part of the key, and tracked
# files under native/ios get mtimes from their content.
mtime() { stat -c %Y "$1" 2>/dev/null || stat -f %m "$1"; }
gitq() { git -C "$repo" -c user.name=dry -c user.email=dry@example.invalid -c commit.gpgsign=false "$@" >/dev/null; }
src=$repo/native/ios/Packages/XbinCore/Sources/Core.swift
mkdir -p "$(dirname "$src")"
echo "let a = 1" >"$src"
echo "let b = 2" >"$repo/native/ios/Packages/XbinCore/Sources/Other.swift"
gitq init -q
gitq add native/ios/project.yml native/ios/Packages/XbinCore/Package.swift native/ios/Packages/XbinRenderer/Package.swift \
  native/ios/Packages/XbinCore/Sources
gitq commit -q -m one
reset_env
run "$S/ci-cache.sh" app app
keyg=$(outputs key)
if [ "$keyg" != "$key1" ]; then ok "ci-cache: in git, the committed native/ios is part of the key"; else bad "ci-cache: git tree ignored"; fi
has "ci-cache: stamps the tracked files" "$out" "5 files under native/ios stamped"
m1=$(mtime "$src")
if [ "$m1" -lt 1500000000 ]; then ok "ci-cache: a content mtime is in the past ($m1)"; else bad "ci-cache: mtime $m1 is not a content stamp"; fi
if [ "$m1" != "$(mtime "$repo/native/ios/Packages/XbinCore/Sources/Other.swift")" ]; then
  ok "ci-cache: different content, different mtime"
else bad "ci-cache: two files share an mtime"; fi
touch "$src"
reset_env
run "$S/ci-cache.sh" app app
eq "ci-cache: a fresh checkout's file gets the same mtime back" "$(mtime "$src")" "$m1"
eq "ci-cache: …and the same key" "$(outputs key)" "$keyg"
echo "let a = 2" >"$src"
gitq commit -q -am two
reset_env
run "$S/ci-cache.sh" app app
if [ "$(mtime "$src")" != "$m1" ]; then ok "ci-cache: changed content, new mtime"; else bad "ci-cache: changed file kept its mtime"; fi
if [ "$(outputs key)" != "$keyg" ]; then ok "ci-cache: a committed source change makes a new key (saved when green)"; else bad "ci-cache: source change kept the key"; fi
touch "$src"
m2=$(mtime "$src")
reset_env
XBIN_CI_CACHE=off run "$S/ci-cache.sh" app app
eq "ci-cache: XBIN_CI_CACHE=off leaves mtimes alone" "$(mtime "$src")" "$m2"
hasnt "ci-cache: …and the Xcode default" "$(cat "$FAKE_LOG")" "defaults write"
rm -rf "$repo/.git" "$repo/native/ios/Packages/XbinCore/Sources"
rm -rf "$repo/native/ios/Packages" "$repo/native/ios/project.yml"

# ---- ci-xcodegen.sh ------------------------------------------------------
reset_env
run "$S/ci-xcodegen.sh"
eq "ci-xcodegen: on PATH → nothing to do" "$rc:$(cat "$GITHUB_PATH")" "0:"
hasnt "ci-xcodegen: …no download" "$(cat "$FAKE_LOG")" "curl "
# The release zip's layout (xcodegen/bin/xcodegen + share/), made here.
mkdir -p "$tmp/xgz/xcodegen/bin" "$tmp/xgz/xcodegen/share/xcodegen/SettingPresets"
printf '#!/bin/sh\necho "Version: 2.46.0"\n' >"$tmp/xgz/xcodegen/bin/xcodegen"
echo "{}" >"$tmp/xgz/xcodegen/share/xcodegen/SettingPresets/base.yml"
chmod +x "$tmp/xgz/xcodegen/bin/xcodegen"
python3 - "$tmp/xgz" "$tmp/xcodegen.zip" <<'PY'
import os, sys, zipfile
root, dest = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(dest, "w") as z:
    for d, _, files in os.walk(root):
        for f in sorted(files):
            p = os.path.join(d, f)
            info = zipfile.ZipInfo(os.path.relpath(p, root))
            info.external_attr = (os.stat(p).st_mode & 0o777) << 16
            info.create_system = 3
            with open(p, "rb") as fh:
                z.writestr(info, fh.read())
PY
zsum=$(lib ci_sha256 <"$tmp/xcodegen.zip")
noxg=$(path_without xcodegen brew)
reset_env
FAKE_CURL_FILE=$tmp/xcodegen.zip XCODEGEN_SHA256=$zsum PATH="$noxg" run "$S/ci-xcodegen.sh"
eq "ci-xcodegen: missing → the pinned zip" "$rc" 0
has "ci-xcodegen: …from the release URL" "$(cat "$FAKE_LOG")" "curl https://github.com/yonaskolb/XcodeGen/releases/download/2.46.0/xcodegen.zip"
eq "ci-xcodegen: …on GITHUB_PATH for later steps" "$(cat "$GITHUB_PATH")" "$RUNNER_TEMP/xbin-tools/xcodegen-2.46.0/xcodegen/bin"
has "ci-xcodegen: …and runs" "$out" "xcodegen 2.46.0 at $RUNNER_TEMP/xbin-tools/xcodegen-2.46.0/xcodegen/bin/xcodegen"
: >"$FAKE_LOG"
: >"$GITHUB_PATH"
FAKE_CURL_FILE=$tmp/xcodegen.zip XCODEGEN_SHA256=$zsum PATH="$noxg" run "$S/ci-xcodegen.sh"
eq "ci-xcodegen: already unpacked (a self-hosted runner's tools dir) → no download" "$rc:$(grep -c '^curl' "$FAKE_LOG" || true)" "0:0"
reset_env
FAKE_CURL_FILE=$tmp/xcodegen.zip PATH="$noxg" run "$S/ci-xcodegen.sh"
eq "ci-xcodegen: a checksum mismatch and no Homebrew fails" "$rc" 1
has "ci-xcodegen: …says the checksum is wrong" "$out" "SHA-256 $zsum, expected 4d9e34b6"
eq "ci-xcodegen: …and leaves nothing behind" "$(find "$RUNNER_TEMP" -name xcodegen -type f | wc -l | tr -d ' ')" 0
nox=$(path_without xcodegen)
mkdir -p "$tmp/brewbin"
reset_env
FAKE_CURL_FAIL=1 FAKE_BREW_BIN=$tmp/brewbin PATH="$nox:$tmp/brewbin" run "$S/ci-xcodegen.sh"
eq "ci-xcodegen: download fails → Homebrew" "$rc" 0
has "ci-xcodegen: …brew install xcodegen" "$(cat "$FAKE_LOG")" "brew install xcodegen"
reset_env
RUNNER_ENVIRONMENT=self-hosted HOME=$tmp/home FAKE_CURL_FILE=$tmp/xcodegen.zip XCODEGEN_SHA256=$zsum PATH="$noxg" run "$S/ci-xcodegen.sh"
eq "ci-xcodegen: self-hosted keeps it on disk" "$(cat "$GITHUB_PATH")" "$tmp/home/Library/Caches/xbin-ci/tools/xcodegen-2.46.0/xcodegen/bin"

# ---- ci-uitests.sh -------------------------------------------------------
reset_env
run "$S/ci-uitests.sh" "$dest"
eq "ci-uitests: no project.yml → success" "$rc" 0
has "ci-uitests: notice" "$out" "::notice::no native/ios/project.yml yet"
echo "name: Xbin" >"$repo/native/ios/project.yml"
export FAKE_SCHEMES="Xbin XbinUITests"
run "$S/ci-uitests.sh" "$dest"
eq "ci-uitests: builds" "$rc" 0
log=$(cat "$FAKE_LOG")
has "ci-uitests: build-for-testing into the app's DerivedData, unsigned" "$log" \
  "xcodebuild build-for-testing -project Xbin.xcodeproj -scheme XbinUITests -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/app -clonedSourcePackagesDirPath $RUNNER_TEMP/xbin-derived/SourcePackages -skipMacroValidation -skipPackagePluginValidation -resultBundlePath $RUNNER_TEMP/xbin-ci/uitests-build.xcresult -IDEBuildingContinueBuildingAfterErrors=YES COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGNING_ALLOWED=NO"
hasnt "ci-uitests: no XBIN_E2E_URL → not run" "$log" "test-without-building"
has "ci-uitests: …and the summary says why" "$(cat "$GITHUB_STEP_SUMMARY")" "not run (no XBIN_E2E_URL"
has "ci-uitests: build log where ios.yml uploads it" "$(cat "$RUNNER_TEMP/xbin-ci/uitests-build.log" 2>/dev/null)" "** fake build-for-testing **"
FAKE_SCHEMES="Xbin" run "$S/ci-uitests.sh" "$dest"
has "ci-uitests: warns when project.yml declares no XbinUITests scheme" "$out" "::warning::Xbin.xcodeproj lists no scheme \"XbinUITests\""
FAKE_XCODEBUILD_STATUS=65 run "$S/ci-uitests.sh" "$dest"
eq "ci-uitests: a failed build fails the step" "$rc" 65
XBIN_E2E_URL=http://127.0.0.1:9871 run "$S/ci-uitests.sh" "$dest"
eq "ci-uitests: a URL without a token fails" "$rc" 2
reset_env
export FAKE_SCHEMES="Xbin XbinUITests"
XBIN_E2E_URL=http://127.0.0.1:9871 XBIN_E2E_TOKEN=tok123 XBIN_E2E_ERASE=1 FAKE_E2E_PNGS=4 \
  XBIN_E2E_ONLY="XbinUITests/XbinE2ETests/test01AddWorkspace XbinUITests/XbinE2ETests/test03NativeCounter" \
  run "$S/ci-uitests.sh" "$dest"
eq "ci-uitests: runs against an xbind" "$rc" 0
log=$(cat "$FAKE_LOG")
has "ci-uitests: running builds signed to run locally (the Keychain)" "$log" "COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual"
has "ci-uitests: erases the simulator when asked" "$log" "xcrun simctl erase BBBBBBBB-0000-4000-8000-000000002714"
has "ci-uitests: …then boots it" "$log" "xcrun simctl bootstatus BBBBBBBB-0000-4000-8000-000000002714 -b"
has "ci-uitests: test-without-building, only the named tests" "$log" \
  "xcodebuild test-without-building -project Xbin.xcodeproj -scheme XbinUITests -destination $dest -derivedDataPath $RUNNER_TEMP/xbin-derived/app -clonedSourcePackagesDirPath $RUNNER_TEMP/xbin-derived/SourcePackages -skipMacroValidation -skipPackagePluginValidation -resultBundlePath $RUNNER_TEMP/xbin-ci/uitests.xcresult -only-testing:XbinUITests/XbinE2ETests/test01AddWorkspace -only-testing:XbinUITests/XbinE2ETests/test03NativeCounter"
has "ci-uitests: the tests get the URL, the token and E2E_DIR" "$log" \
  "e2e-env URL=http://127.0.0.1:9871 TOKEN=tok123 DIR=$RUNNER_TEMP/xbin-ci/e2e"
eq "ci-uitests: screenshots in E2E_DIR" "$(find "$RUNNER_TEMP/xbin-ci/e2e" -name '*.png' | wc -l | tr -d ' ')" 4
isdir "ci-uitests: the result bundle" "$RUNNER_TEMP/xbin-ci/uitests.xcresult"
has "ci-uitests: summary counts screenshots" "$(cat "$GITHUB_STEP_SUMMARY")" "passed against http://127.0.0.1:9871 — 4 screenshots"
hasnt "ci-uitests: the token never reaches the log" "$out" "tok123"
: >"$FAKE_LOG"
XBIN_E2E_URL=http://127.0.0.1:9871 XBIN_E2E_TOKEN=tok123 FAKE_ATTACH=1 run "$S/ci-uitests.sh" "$dest"
hasnt "ci-uitests: no erase unless asked" "$(cat "$FAKE_LOG")" "simctl erase"
has "ci-uitests: no PNG written → exports the attachments" "$(cat "$FAKE_LOG")" \
  "xcrun xcresulttool export attachments --path $RUNNER_TEMP/xbin-ci/uitests.xcresult --output-path $RUNNER_TEMP/xbin-ci/e2e/attachments"
: >"$FAKE_LOG"
XBIN_E2E_URL=http://127.0.0.1:9871 XBIN_E2E_TOKEN=tok123 XBIN_SIGNING=none run "$S/ci-uitests.sh" "$dest"
has "ci-uitests: XBIN_SIGNING=none still wins when set" "$(cat "$FAKE_LOG")" "COMPILER_INDEX_STORE_ENABLE=NO CODE_SIGNING_ALLOWED=NO"
FAKE_XCODEBUILD_STATUS=0 FAKE_E2E_PNGS=1 XBIN_E2E_URL=http://127.0.0.1:9871 XBIN_E2E_TOKEN=tok123 run "$S/ci-uitests.sh" "$dest"
eq "ci-uitests: a rerun starts with an empty E2E_DIR" "$(find "$RUNNER_TEMP/xbin-ci/e2e" -name '*.png' | wc -l | tr -d ' ')" 1
rm -f "$repo/native/ios/project.yml"
rm -rf "$repo/native/ios/Xbin.xcodeproj"

dry_done ci-dry-test
