# shellcheck shell=bash
# shellcheck disable=SC2034 # td, out, rc, … are for the tests that source this
# native/ios/scripts/dry-lib.sh — what the dry tests (ci-dry-test.sh,
# mac-dry-test.sh) share: a scratch directory, assertions, fake Apple and
# Mac tools on PATH, a scratch copy of the scripts at their real path (so
# XBIN_REPO resolves there), and an Actions-like environment. Sourced; the
# fakes log every call to $FAKE_LOG. Bash 3.2.
#
# The fakes stand in for xcrun (simctl, metal, xcresulttool), xcodebuild,
# xcodegen, swift, xcbeautify, xcode-select, curl, brew, sudo, launchctl,
# sw_vers, uname (Darwin), pmset, defaults, fdesetup, systemsetup, dscl,
# dsmemberutil, socketfilterfw (XBIN_SOCKETFILTERFW), nc, system_profiler,
# df and security. They record decisions; they
# cannot tell whether Apple's tools accept the flags — only a Mac can. With
# FAKE_STATEFUL=1 a fix sticks (xcode-select -s, -license accept,
# -runFirstLaunch, -downloadPlatform, the Metal download, brew install,
# simctl create, launchctl bootstrap), so a check → fix → check runs as on
# a Mac; the state lives in $FAKE_STATE (JSON listings are edited in place:
# give such a run a copy).

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ci-dry-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

fails=0 passes=0
ok() { passes=$((passes + 1)); echo "ok   $*"; }
bad() { fails=$((fails + 1)); echo "FAIL $*"; }
eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: got [$2], want [$3]"; fi; }
has() { if printf '%s\n' "$2" | grep -qF -- "$3"; then ok "$1"; else bad "$1: [$3] not in: $2"; fi; }
isdir() { if [ -d "$2" ]; then ok "$1"; else bad "$1: no directory $2"; fi; }
isfile() { if [ -f "$2" ]; then ok "$1"; else bad "$1: no file $2"; fi; }
hasnt() { if printf '%s\n' "$2" | grep -qF -- "$3"; then bad "$1: [$3] unexpectedly in: $2"; else ok "$1"; fi; }
dry_done() {
  echo
  echo "$1: $passes passed, $fails failed (bash $BASH_VERSION)"
  [ "$fails" -eq 0 ]
}

# ---- fakes ---------------------------------------------------------------
bin=$tmp/bin
mkdir -p "$bin"
export FAKE_LOG=$tmp/fake.log FAKE_STATE=$tmp/state
: >"$FAKE_LOG"
mkdir -p "$FAKE_STATE"

# The JSON edits the stateful fakes make (python3): add-device <json>
# <name> <type> <runtime> <udid>; add-runtime <json> <version>.
cat >"$bin/fake-simjson" <<'PY'
#!/usr/bin/env python3
import json, sys
op, path = sys.argv[1], sys.argv[2]
with open(path) as f:
    d = json.load(f)
if op == "add-device":
    name, dtype, rt, udid = sys.argv[3:7]
    d.setdefault("devices", {}).setdefault(rt, []).append(
        {"name": name, "udid": udid, "isAvailable": True, "deviceTypeIdentifier": dtype, "state": "Shutdown"})
elif op == "add-runtime":
    v = sys.argv[3]
    d.setdefault("runtimes", []).append({"identifier": "com.apple.CoreSimulator.SimRuntime.iOS-" + v.replace(".", "-"),
        "version": v, "platform": "iOS", "isAvailable": True, "name": "iOS " + v, "supportedDeviceTypes": []})
with open(path, "w") as f:
    json.dump(d, f)
PY

cat >"$bin/xcrun" <<'SH'
#!/bin/sh
# fake xcrun: simctl, metal, xcresulttool export attachments
echo "xcrun $*" >>"$FAKE_LOG"
case "$1" in
simctl)
  case "$2" in
  list)
    case " $* " in
    *" -j "*) cat "$FAKE_SIMCTL_JSON" ;;
    *) echo "== fake simctl list $3 ==" ;;
    esac ;;
  create)
    udid=${FAKE_CREATED_UDID:-99999999-0000-4000-8000-00000000C0DE}
    [ "${FAKE_STATEFUL:-0}" = 1 ] && fake-simjson add-device "$FAKE_SIMCTL_JSON" "$3" "$4" "$5" "$udid"
    echo "$udid" ;;
  bootstatus) exit "${FAKE_BOOT_STATUS:-0}" ;;
  io)
    # simctl io <udid> screenshot [--type=png] <file>
    for last in "$@"; do :; done
    printf 'PNG' >"$last" ;;
  launch)
    for a in "$@"; do
      case $a in --stdout=* | --stderr=*) : >"${a#*=}" ;; esac
    done
    echo "dev.xbin.app: 4242" ;;
  spawn) echo "fake log line" ;;
  boot | shutdown | erase | install | openurl | delete | terminate | uninstall) exit "${FAKE_SIMCTL_STATUS:-0}" ;;
  runtime) echo "== fake runtimes ==" ;;
  *) exit 64 ;;
  esac
  ;;
metal)
  [ "${FAKE_STATEFUL:-0}" = 1 ] && [ -f "$FAKE_STATE/metal" ] && exit 0
  exit "${FAKE_METAL_STATUS:-0}" ;;
xcresulttool)
  out=""
  while [ $# -gt 0 ]; do
    [ "$1" = --output-path ] && out=$2
    shift
  done
  [ -n "$out" ] || exit 64
  mkdir -p "$out"
  if [ "${FAKE_ATTACH:-0}" = 1 ]; then printf 'PNG' >"$out/0A1B-counter-go-light-default.png"; fi
  echo '[]' >"$out/manifest.json"
  ;;
*) exit 64 ;;
esac
SH

cat >"$bin/xcodebuild" <<'SH'
#!/bin/sh
# fake xcodebuild: -version, -showsdks, -downloadComponent, -downloadPlatform,
# -license, -checkFirstLaunchStatus, -runFirstLaunch, -list, and the build
# actions (build, test, build-for-testing, test-without-building)
echo "xcodebuild $*" >>"$FAKE_LOG"
case "$1" in
-version) printf 'Xcode 27.0\nBuild version 27A5000a\n'; exit 0 ;;
-downloadComponent)
  [ "${FAKE_DOWNLOAD_STATUS:-0}" = 0 ] && [ "${FAKE_STATEFUL:-0}" = 1 ] && touch "$FAKE_STATE/metal"
  exit "${FAKE_DOWNLOAD_STATUS:-0}" ;;
-downloadPlatform)
  [ "${FAKE_STATEFUL:-0}" = 1 ] && fake-simjson add-runtime "$FAKE_SIMCTL_JSON" "${FAKE_SDK:-27.0}"
  exit "${FAKE_PLATFORM_STATUS:-0}" ;;
-license)
  if [ "${2:-}" = accept ]; then touch "$FAKE_STATE/license"; exit 0; fi
  [ -f "$FAKE_STATE/license" ] && exit 0
  exit "${FAKE_LICENSE_STATUS:-0}" ;;
-checkFirstLaunchStatus)
  [ -f "$FAKE_STATE/firstlaunch" ] && exit 0
  exit "${FAKE_FIRSTLAUNCH_STATUS:-0}" ;;
-runFirstLaunch) touch "$FAKE_STATE/firstlaunch"; exit 0 ;;
-showsdks)
  v=${FAKE_SDK:-27.0}
  printf 'iOS SDKs:\n\tiOS %s                      \t-sdk iphoneos%s\n\niOS Simulator SDKs:\n\tSimulator - iOS %s          \t-sdk iphonesimulator%s\n' "$v" "$v" "$v" "$v"
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
archive="" export="" scheme=""
for a in "$@"; do
  [ "$prev" = -resultBundlePath ] && bundle=$a
  [ "$prev" = -scheme ] && scheme=$a
  [ "$prev" = -archivePath ] && archive=$a
  [ "$prev" = -exportPath ] && export=$a
  case "$a" in build | test | build-for-testing | test-without-building | archive | -exportArchive) action=$a ;; esac
  prev=$a
done
# FAKE_STRICT_SCHEMES=1: a -scheme not in FAKE_SCHEMES fails as xcodebuild does
if [ "${FAKE_STRICT_SCHEMES:-0}" = 1 ] && [ -n "$scheme" ]; then
  case " ${FAKE_SCHEMES:-} " in
  *" $scheme "*) ;;
  *)
    echo "xcodebuild: error: The workspace named \"Fake\" does not contain a scheme named \"$scheme\". The \"-list\" option can be used to find the names of the schemes in the workspace."
    exit 65 ;;
  esac
fi
# archive leaves the .xcarchive, -exportArchive an .ipa (unless FAKE_NO_IPA=1)
if [ "${FAKE_XCODEBUILD_STATUS:-0}" = 0 ]; then
  [ "$action" = archive ] && [ -n "$archive" ] && mkdir -p "$archive/Products/Applications/Xbin.app"
  [ "$action" = -exportArchive ] && [ -n "$export" ] && [ "${FAKE_NO_IPA:-0}" = 0 ] && mkdir -p "$export" && printf 'IPA' >"$export/Xbin.ipa"
fi
echo "env SNAPSHOT_DIR=${TEST_RUNNER_SNAPSHOT_DIR:-} FIXTURES_DIR=${TEST_RUNNER_FIXTURES_DIR:-}" >>"$FAKE_LOG"
echo "e2e-env URL=${TEST_RUNNER_XBIN_E2E_URL:-} TOKEN=${TEST_RUNNER_XBIN_E2E_TOKEN:-} DIR=${TEST_RUNNER_E2E_DIR:-}" >>"$FAKE_LOG"
[ -n "$bundle" ] && mkdir -p "$bundle"
echo "** fake $action **"
if [ "$action" = test ] && [ "${FAKE_PNGS:-0}" -gt 0 ] && [ -n "${TEST_RUNNER_SNAPSHOT_DIR:-}" ]; then
  i=0
  while [ "$i" -lt "$FAKE_PNGS" ]; do
    printf 'PNG' >"$TEST_RUNNER_SNAPSHOT_DIR/fixture$i-light-default.png"
    i=$((i + 1))
  done
fi
if [ "$action" = test-without-building ] && [ "${FAKE_E2E_PNGS:-0}" -gt 0 ] && [ -n "${TEST_RUNNER_E2E_DIR:-}" ]; then
  i=0
  while [ "$i" -lt "$FAKE_E2E_PNGS" ]; do
    printf 'PNG' >"$TEST_RUNNER_E2E_DIR/0$i-step.png"
    i=$((i + 1))
  done
fi
if [ "$action" = build ] && [ -n "${FAKE_APP_PRODUCTS:-}" ]; then
  mkdir -p "$FAKE_APP_PRODUCTS/Xbin.app"
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
echo "xcode-select $*" >>"$FAKE_LOG"
case "${1:-}" in
-p)
  if [ -f "$FAKE_STATE/xcode-select" ]; then cat "$FAKE_STATE/xcode-select"
  else echo "${FAKE_XCODE_SELECT:-/Applications/Xcode_27.0.app/Contents/Developer}"; fi ;;
-s) echo "$2" >"$FAKE_STATE/xcode-select" ;;
esac
SH

# curl -fsSL [--retry n] -o <file> <url>: copies $FAKE_CURL_FILE (or fails
# with FAKE_CURL_FAIL=1); without -o prints it.
cat >"$bin/curl" <<'SH'
#!/bin/sh
out="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
  -o) out=$2; shift ;;
  --retry | --max-time | -w | -H | -X | -d) shift ;;
  -*) ;;
  *) url=$1 ;;
  esac
  shift
done
echo "curl $url" >>"$FAKE_LOG"
[ "${FAKE_CURL_FAIL:-0}" = 1 ] && exit 22
if [ -z "$out" ]; then cat "${FAKE_CURL_FILE:-/dev/null}"
elif [ -n "${FAKE_CURL_FILE:-}" ]; then cp "$FAKE_CURL_FILE" "$out"
else : >"$out"; fi
SH

cat >"$bin/brew" <<'SH'
#!/bin/sh
echo "brew $*" >>"$FAKE_LOG"
case "$1" in
list)
  n=${3:-$2}
  [ -f "$FAKE_STATE/brew-$n" ] && exit 0
  for f in ${FAKE_BREW_HAS:-}; do [ "$f" = "$n" ] && exit 0; done
  exit 1 ;;
install)
  shift
  for f in "$@"; do
    case $f in -*) continue ;; esac
    touch "$FAKE_STATE/brew-$f"
    if [ -n "${FAKE_BREW_BIN:-}" ]; then
      printf '#!/bin/sh\necho "Version: 2.46.0"\n' >"$FAKE_BREW_BIN/$f"
      chmod +x "$FAKE_BREW_BIN/$f"
    fi
  done ;;
shellenv) echo "export HOMEBREW_PREFIX=/opt/homebrew" ;;
--prefix) echo /opt/homebrew ;;
esac
exit 0
SH

# sudo: log, then run the command (no password in a dry run).
cat >"$bin/sudo" <<'SH'
#!/bin/sh
echo "sudo $*" >>"$FAKE_LOG"
[ "${1:-}" = -n ] && shift
[ "${1:-}" = -v ] && exit 0
exec "$@"
SH

cat >"$bin/launchctl" <<'SH'
#!/bin/sh
echo "launchctl $*" >>"$FAKE_LOG"
case "$1" in
print) [ -f "$FAKE_STATE/launchd-${2##*/}" ] && { echo "state = running"; exit 0; }; exit "${FAKE_LAUNCHCTL_PRINT:-1}" ;;
bootstrap) n=${3##*/}; touch "$FAKE_STATE/launchd-${n%.plist}" ;;
bootout) rm -f "$FAKE_STATE/launchd-${2##*/}" ;;
esac
exit 0
SH

cat >"$bin/defaults" <<'SH'
#!/bin/sh
echo "defaults $*" >>"$FAKE_LOG"
case "$*" in
*autoLoginUser*) [ -n "${FAKE_AUTOLOGIN:-}" ] || exit 1; echo "$FAKE_AUTOLOGIN" ;;
*CFBundleShortVersionString*) echo "${FAKE_XCODE_VERSION:-27.0}" ;;
*ProductBuildVersion*) echo "${FAKE_XCODE_BUILD:-27A5000a}" ;;
write\ *) exit 0 ;;
*MobileMeAccounts*)
  [ -n "${FAKE_APPLE_ID:-}" ] || exit 1
  printf '(\n    {\n        AccountID = "%s";\n    }\n)\n' "$FAKE_APPLE_ID" ;;
*) exit 1 ;;
esac
SH

# The box's state (mac-setup.sh's report): FileVault, systemsetup, users and
# groups, the firewall, sshd's port, the display, free disk. Defaults: a Mac
# as mac-setup.sh wants it but FileVault off (FAKE_FILEVAULT=On to turn it on).
cat >"$bin/fdesetup" <<'SH'
#!/bin/sh
echo "fdesetup $*" >>"$FAKE_LOG"
[ "${1:-}" = status ] && echo "FileVault is ${FAKE_FILEVAULT:-Off}."
SH
cat >"$bin/systemsetup" <<'SH'
#!/bin/sh
echo "systemsetup $*" >>"$FAKE_LOG"
if [ "${FAKE_SYSTEMSETUP_DENIED:-0}" = 1 ]; then echo "You need administrator access to run this tool... exiting!"; exit 1; fi
case "${1:-}" in
-getrestartfreeze) echo "Restart After Freeze: ${FAKE_RESTARTFREEZE:-On}" ;;
-getremotelogin) echo "Remote Login: ${FAKE_REMOTELOGIN:-On}" ;;
*) exit 1 ;;
esac
SH
# dscl . -list /Users UniqueID | -read /Users/<u> UniqueID | -read /Groups/<g> GroupMembership
cat >"$bin/dscl" <<'SH'
#!/bin/sh
echo "dscl $*" >>"$FAKE_LOG"
users=${FAKE_USERS-owner:501 ci:502 release:503}
case "$2 $3" in
"-list /Users")
  echo "_www 70"
  for u in $users; do echo "${u%%:*} ${u#*:}"; done ;;
"-read /Groups/com.apple.access_screensharing")
  [ -n "${FAKE_SCREENSHARING_USERS:-}" ] || exit 56
  echo "GroupMembership: $FAKE_SCREENSHARING_USERS" ;;
"-read /Users/"*)
  n=${3#/Users/}
  for u in $users; do [ "${u%%:*}" = "$n" ] && { echo "UniqueID: ${u#*:}"; exit 0; }; done
  exit 56 ;;
*) exit 1 ;;
esac
SH
cat >"$bin/dsmemberutil" <<'SH'
#!/bin/sh
# dsmemberutil checkmembership -U <user> -G <group>
for a in ${FAKE_ADMINS-owner}; do [ "$a" = "$3" ] && { echo "user is a member of the group"; exit 0; }; done
echo "user is not a member of the group"
SH
cat >"$bin/socketfilterfw" <<'SH'
#!/bin/sh
case "${1:-}" in
--getglobalstate) echo "Firewall is ${FAKE_FW:-enabled}. (State = 1)" ;;
--getstealthmode) echo "Firewall stealth mode is ${FAKE_STEALTH:-on}" ;;
esac
SH
# security: the login keychain (FAKE_KEYCHAIN_LOCKED=1) and its signing
# identities (FAKE_DIST_IDENTITY=1: an Apple Distribution one).
cat >"$bin/security" <<'SH'
#!/bin/sh
echo "security $*" >>"$FAKE_LOG"
case "${1:-}" in
show-keychain-info) [ "${FAKE_KEYCHAIN_LOCKED:-0}" = 0 ] ;;
unlock-keychain) exit 0 ;;
find-identity)
  echo '  1) 0123456789ABCDEF0123456789ABCDEF01234567 "Apple Development: Release (ABCDE12345)"'
  [ "${FAKE_DIST_IDENTITY:-0}" = 1 ] && echo '  2) 89ABCDEF0123456789ABCDEF0123456789ABCDEF "Apple Distribution: Team (ABCDE12345)"'
  exit 0 ;;
*) exit 1 ;;
esac
SH
cat >"$bin/nc" <<'SH'
#!/bin/sh
exit "${FAKE_NC:-0}"
SH
cat >"$bin/system_profiler" <<'SH'
#!/bin/sh
[ "${FAKE_DISPLAY:-1}" = 1 ] && printf 'Graphics/Displays:\n    Displays:\n        Dummy:\n          Resolution: 1920 x 1080\n'
exit 0
SH
cat >"$bin/df" <<'SH'
#!/bin/sh
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s1s1 971350180 400000000 %s 40%% /\n' "${FAKE_DF_AVAIL_KB:-209715200}"
SH

cat >"$bin/sw_vers" <<'SH'
#!/bin/sh
case "${1:-}" in
-productVersion) echo 26.1 ;;
*) printf 'ProductName:\tmacOS\nProductVersion:\t26.1\n' ;;
esac
SH

cat >"$bin/uname" <<'SH'
#!/bin/sh
case "${1:-}" in
-m) echo arm64 ;;
*) echo "${FAKE_UNAME:-Darwin}" ;;
esac
SH

cat >"$bin/pmset" <<'SH'
#!/bin/sh
echo "pmset $*" >>"$FAKE_LOG"
printf 'System-wide power settings:\nCurrently in use:\n Sleep On Power Button 1\n'
printf ' womp                 %s\n autorestart          %s\n sleep                %s\n' "${FAKE_WOMP:-1}" "${FAKE_AUTORESTART:-1}" "${FAKE_SLEEP:-0}"
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
  export GITHUB_PATH=$tmp/runner/path
  : >"$GITHUB_STEP_SUMMARY"
  : >"$GITHUB_ENV"
  : >"$GITHUB_OUTPUT"
  : >"$GITHUB_PATH"
  : >"$FAKE_LOG"
  rm -rf "$FAKE_STATE"
  mkdir -p "$FAKE_STATE"
  unset XBIN_SIM XBIN_SIM_ENSURE XBIN_XCODE DEVELOPER_DIR TEST_RUNNER_SNAPSHOT_DIR TEST_RUNNER_FIXTURES_DIR \
    XBIN_CI_OUT XBIN_CI_DERIVED XBIN_CI_SPM XBIN_CI_CACHE XBIN_CI_CACHE_ROOT XBIN_CI_CACHE_VERSION XBIN_CI_TOOLS \
    RUNNER_ENVIRONMENT RUNNER_NAME XBIN_SIGNING XBIN_SWIFT_CONDITIONS XBIN_E2E_URL XBIN_E2E_TOKEN XBIN_E2E_ERASE XBIN_E2E_ONLY \
    TEST_RUNNER_E2E_DIR FAKE_SCHEMES FAKE_STRICT_SCHEMES FAKE_PNGS FAKE_E2E_PNGS FAKE_ATTACH FAKE_XCODEBUILD_STATUS FAKE_BOOT_STATUS \
    FAKE_PROJECT FAKE_XCB_OLD XCBEAUTIFY FAKE_METAL_STATUS FAKE_DOWNLOAD_STATUS FAKE_CURL_FAIL FAKE_CURL_FILE \
    FAKE_BREW_BIN XCODEGEN_SHA256 XCODEGEN_VERSION FAKE_APP_PRODUCTS \
    FAKE_FILEVAULT FAKE_SYSTEMSETUP_DENIED FAKE_RESTARTFREEZE FAKE_REMOTELOGIN FAKE_USERS FAKE_ADMINS \
    FAKE_SCREENSHARING_USERS FAKE_FW FAKE_STEALTH FAKE_NC FAKE_DISPLAY FAKE_DF_AVAIL_KB FAKE_WOMP FAKE_AUTORESTART \
    FAKE_APPLE_ID FAKE_XCODE_VERSION FAKE_XCODE_BUILD FAKE_KEYCHAIN_LOCKED FAKE_DIST_IDENTITY FAKE_NO_IPA \
    XBIN_TEAM_ID XBIN_ASC_KEY_ID XBIN_ASC_ISSUER_ID XBIN_ASC_KEY XBIN_RELEASE_FROM_ACTIONS GITHUB_EVENT_NAME GITHUB_HEAD_REF \
    GITHUB_EVENT_PATH RUNNER_NAME XBIN_CI_USER XBIN_RELEASE_USER
  export XCBEAUTIFY=0 # off unless a case turns it on
  export XBIN_SOCKETFILTERFW=$bin/socketfilterfw XBIN_SSHD_CONFIG=$tmp/sshd/sshd_config
  mkdir -p "$tmp/sshd/sshd_config.d"
  printf 'Include /etc/ssh/sshd_config.d/*\nUsePAM yes\n' >"$tmp/sshd/sshd_config"
  printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\nAllowUsers owner ci release\n' \
    >"$tmp/sshd/sshd_config.d/100-xbin.conf"
}

# run <cmd…> — sets $out (stdout+stderr) and $rc.
run() {
  rc=0
  out=$("$@" 2>&1 </dev/null) || rc=$?
}

# lib <function> <args…> — call a ci-lib.sh function (the scratch copy's) in
# a subshell.
lib() {
  # shellcheck source=SCRIPTDIR/ci-lib.sh
  (. "$S/ci-lib.sh" && "$@")
}

# A PATH without the fake xcodegen (and optionally more names): the fakes
# minus those, then only the system directories.
path_without() {
  local d f skip n
  d=$tmp/bin-without-$(printf '%s' "$*" | tr ' ' '-')
  if [ ! -d "$d" ]; then
    mkdir -p "$d"
    for f in "$bin"/*; do
      n=$(basename "$f")
      skip=0
      for x in "$@"; do [ "$n" = "$x" ] && skip=1; done
      [ "$skip" = 1 ] || ln -s "$f" "$d/"
    done
  fi
  printf '%s\n' "$d:/usr/local/bin:/usr/bin:/bin"
}
