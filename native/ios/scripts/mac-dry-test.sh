#!/usr/bin/env bash
# native/ios/scripts/mac-dry-test.sh — mac-setup.sh, mac-cleanup.sh and
# mac-remote.sh on Linux against fake Mac tools (dry-lib.sh), the Mac itself
# played by a fake ssh that runs the remote command in a scratch "home"
# here. Real rsync and git do the mirroring, so what reaches the Mac — and
# what is deleted or kept there — is checked for real. What a real Mac,
# Xcode, Homebrew or GitHub's runner do with the commands is not: only the
# Mac mini can say. Part of ci-local-check.sh.
set -euo pipefail
# shellcheck source=SCRIPTDIR/dry-lib.sh
. "$(dirname "$0")/dry-lib.sh"

# A fake ssh: log the call, then run the remote command in $FAKE_REMOTE_HOME
# (sh -c, as sshd runs it through the login shell; /bin/bash → bash for
# boxes without one). -N (a tunnel only) runs nothing; -n closes stdin.
cat >"$bin/ssh" <<'SH'
#!/bin/sh
echo "ssh $*" >>"$FAKE_LOG"
nocmd=0 nostdin=0
while [ $# -gt 0 ]; do
  case $1 in
  -o | -R | -L | -p | -i | -F | -l | -J | -E) shift 2 ;;
  -N) nocmd=1; shift ;;
  -n) nostdin=1; shift ;;
  -*) shift ;;
  *) break ;;
  esac
done
shift # the host
[ "$nocmd" = 1 ] && exit 0
cd "$FAKE_REMOTE_HOME" || exit 1
cmd=$(printf '%s' "$*" | sed 's#/bin/bash #bash #g')
if [ "$nostdin" = 1 ]; then exec sh -c "$cmd" </dev/null; fi
exec sh -c "$cmd"
SH
cat >"$bin/pgrep" <<'SH'
#!/bin/sh
[ "${FAKE_PGREP:-1}" = 0 ]
SH
chmod +x "$bin/ssh" "$bin/pgrep"

have() { command -v "$1" >/dev/null 2>&1; }
local_env() { # a dev box, not Actions
  reset_env
  unset GITHUB_ACTIONS GITHUB_STEP_SUMMARY GITHUB_ENV GITHUB_OUTPUT GITHUB_PATH RUNNER_TEMP
}

# ---- mac-setup.sh --------------------------------------------------------
apps=$tmp/Applications
mkdir -p "$apps/Xcode_26.4.app/Contents/Developer" "$apps/Xcode_27.0.app/Contents/Developer"
xdev=$apps/Xcode_27.0.app/Contents/Developer
# The runner release, as a fake: config.sh leaves .runner, svc.sh install a
# LaunchAgent.
mkdir -p "$tmp/rt"
cat >"$tmp/rt/config.sh" <<'SH'
#!/bin/sh
echo "config.sh $*" >>"$FAKE_LOG"
echo '{"agentName":"fake"}' >.runner
SH
cat >"$tmp/rt/svc.sh" <<'SH'
#!/bin/sh
echo "svc.sh $*" >>"$FAKE_LOG"
if [ "$1" = install ]; then
  mkdir -p "$HOME/Library/LaunchAgents"
  touch "$HOME/Library/LaunchAgents/actions.runner.xbin-dev-xbin.xbin-mini.plist"
fi
SH
chmod +x "$tmp/rt/config.sh" "$tmp/rt/svc.sh"
tar -czf "$tmp/runner.tar.gz" -C "$tmp/rt" config.sh svc.sh
rsum=$(lib ci_sha256 <"$tmp/runner.tar.gz")

setup_env() { # a Mac with Xcode unpacked and nothing else done
  local_env
  export HOME=$tmp/mac1 FAKE_STATEFUL=1 XBIN_APPLICATIONS=$apps
  rm -rf "$HOME"
  mkdir -p "$HOME"
  cp "$td/simctl-typical.json" "$tmp/sim.json"
  export FAKE_SIMCTL_JSON=$tmp/sim.json FAKE_SDK=27.0 FAKE_XCODE_SELECT=/Library/Developer/CommandLineTools
  export FAKE_LICENSE_STATUS=69 FAKE_FIRSTLAUNCH_STATUS=69 FAKE_METAL_STATUS=1 FAKE_SLEEP=1 FAKE_AUTOLOGIN=""
  export FAKE_CURL_FILE=$tmp/runner.tar.gz XBIN_RUNNER_SHA256=$rsum
}
realhome=$HOME

setup_env
run "$S/mac-setup.sh" --check
eq "mac-setup --check: a fresh Mac is not ready" "$rc" 1
for want in "MISSING xcode-select → $xdev" "MISSING Xcode licence accepted" "MISSING Xcode first-launch packages" \
  "MISSING iOS 27.0 simulator runtime" "MISSING Metal toolchain" "MISSING brew: xcodegen" "MISSING brew: rsync" \
  "MISSING simulator xbin-e2e" "MISSING daily cleanup" "MISSING Actions runner xbin-mini"; do
  has "mac-setup --check: $want" "$out" "$want"
done
has "mac-setup --check: picks the newest Xcode*.app when xcode-select names none" "$out" "ok      Xcode ($apps/Xcode_27.0.app)"
has "mac-setup --check: warns about sleep" "$out" "warn    the Mac may sleep"
has "mac-setup --check: warns about automatic login" "$out" "warn    no automatic login"
log=$(cat "$FAKE_LOG")
hasnt "mac-setup --check: changes nothing (no sudo)" "$log" "sudo "
hasnt "mac-setup --check: …no brew install" "$log" "brew install"
hasnt "mac-setup --check: …no simulator created" "$log" "simctl create"
hasnt "mac-setup --check: …no download" "$log" "-download"

setup_env
run "$S/mac-setup.sh" --runner-token tok-registration
eq "mac-setup: sets a fresh Mac up" "$rc" 0
has "mac-setup: …ready" "$out" "mac-setup: ready."
log=$(cat "$FAKE_LOG")
has "mac-setup: selects the Xcode" "$log" "sudo xcode-select -s $xdev"
has "mac-setup: accepts the licence" "$log" "sudo xcodebuild -license accept"
has "mac-setup: first launch" "$log" "sudo xcodebuild -runFirstLaunch"
has "mac-setup: the iOS platform" "$log" "xcodebuild -downloadPlatform iOS"
has "mac-setup: the Metal toolchain" "$log" "xcodebuild -downloadComponent MetalToolchain"
for f in xcodegen xcbeautify rsync; do has "mac-setup: brew install $f" "$log" "brew install $f"; done
has "mac-setup: creates the xbin-e2e simulator" "$log" "xcrun simctl create xbin-e2e com.apple.CoreSimulator.SimDeviceType.iPhone-17"
has "mac-setup: loads the cleanup agent" "$log" "launchctl bootstrap gui/$(id -u) $HOME/Library/LaunchAgents/dev.xbin.ci-cleanup.plist"
has "mac-setup: downloads the pinned runner" "$log" "curl https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-osx-arm64-2.337.0.tar.gz"
has "mac-setup: registers it repository-scoped, labelled xbin-mini" "$log" \
  "config.sh --unattended --url https://github.com/xbin-dev/xbin --token tok-registration --name xbin-mini --labels xbin-mini --work _work --replace"
has "mac-setup: …as a launchd service" "$log" "svc.sh install"
has "mac-setup: …started" "$log" "svc.sh start"
hasnt "mac-setup: never prints the token" "$out" "tok-registration"
eq "mac-setup: the job hooks in the runner's .env" "$(grep '^ACTIONS_RUNNER_HOOK' "$HOME/actions-runner/.env")" \
  "ACTIONS_RUNNER_HOOK_JOB_STARTED=$HOME/xbin-ci/bin/job-hook.sh
ACTIONS_RUNNER_HOOK_JOB_COMPLETED=$HOME/xbin-ci/bin/job-hook.sh"
has "mac-setup: the hook runs mac-cleanup.sh --job-hook" "$(cat "$HOME/xbin-ci/bin/job-hook.sh")" "mac-cleanup.sh --job-hook"
if [ -x "$HOME/xbin-ci/bin/job-hook.sh" ]; then ok "mac-setup: the hook is executable"; else bad "mac-setup: hook not executable"; fi
if cmp -s "$S/mac-cleanup.sh" "$HOME/xbin-ci/bin/mac-cleanup.sh"; then ok "mac-setup: installs mac-cleanup.sh"; else bad "mac-setup: mac-cleanup.sh not installed"; fi
plist=$(python3 - "$HOME/Library/LaunchAgents/dev.xbin.ci-cleanup.plist" <<'PY'
import plistlib, sys
with open(sys.argv[1], "rb") as f:
    p = plistlib.load(f)
print(p["Label"], " ".join(p["ProgramArguments"]), p["StartCalendarInterval"]["Hour"], p["StartCalendarInterval"]["Minute"])
PY
)
eq "mac-setup: the LaunchAgent is a valid plist, daily at 04:30" "$plist" \
  "dev.xbin.ci-cleanup /bin/bash $HOME/xbin-ci/bin/mac-cleanup.sh 4 30"
has "mac-setup: next step named" "$out" "gh variable set XBIN_IOS_RUNNER --body '[\"self-hosted\",\"macOS\",\"xbin-mini\"]'"
: >"$FAKE_LOG"
run "$S/mac-setup.sh"
eq "mac-setup: a second run is a no-op" "$rc" 0
log=$(cat "$FAKE_LOG")
for w in "sudo " "brew install" "simctl create" "launchctl bootstrap" "config.sh" "svc.sh" "-download"; do
  hasnt "mac-setup: …again: no $w" "$log" "$w"
done
run "$S/mac-setup.sh" --check
eq "mac-setup --check: now ready" "$rc" 0
echo "# changed" >>"$HOME/xbin-ci/bin/mac-cleanup.sh"
: >"$FAKE_LOG"
run "$S/mac-setup.sh"
has "mac-setup: a changed cleanup script is reinstalled" "$(cat "$FAKE_LOG")" "launchctl bootstrap"
hasnt "mac-setup: …without touching the runner" "$(cat "$FAKE_LOG")" "svc.sh"

setup_env
run "$S/mac-setup.sh" --no-runner
eq "mac-setup --no-runner: ready without a runner" "$rc:$(grep -c 'config.sh' "$FAKE_LOG" || true)" "0:0"
has "mac-setup --no-runner: says so" "$out" "skip    the Actions runner (--no-runner)"
setup_env
run "$S/mac-setup.sh"
eq "mac-setup: no token, not a terminal → the runner stays missing" "$rc" 1
has "mac-setup: …and says how" "$out" "no --runner-token"
setup_env
XBIN_RUNNER_SHA256=0000 run "$S/mac-setup.sh" --runner-token tok
eq "mac-setup: a runner tarball with the wrong SHA-256 is not used" "$rc:$(grep -c 'config.sh' "$FAKE_LOG" || true)" "1:0"
setup_env
printf 'tok-from-stdin\n' >"$tmp/tokfile"
rc=0
out=$("$S/mac-setup.sh" --runner-token-stdin <"$tmp/tokfile" 2>&1) || rc=$?
eq "mac-setup --runner-token-stdin" "$rc" 0
has "mac-setup: …the token from stdin registers" "$(cat "$FAKE_LOG")" "--token tok-from-stdin"
setup_env
FAKE_UNAME=Linux run "$S/mac-setup.sh" --check
eq "mac-setup: not a Mac → refuses" "$rc" 1
has "mac-setup: …says so" "$out" "this runs on the Mac"
setup_env
XBIN_APPLICATIONS=$tmp/none run "$S/mac-setup.sh" --check
eq "mac-setup: no Xcode → stops there" "$rc" 1
has "mac-setup: …and says how to get one" "$out" "install Xcode 27"
setup_env
FAKE_SLEEP=0 FAKE_AUTOLOGIN=ci run "$S/mac-setup.sh" --check
has "mac-setup: sleep off is fine" "$out" "ok      system sleep off"
has "mac-setup: automatic login is fine" "$out" "ok      automatic login (ci)"
export HOME=$realhome
unset FAKE_STATEFUL XBIN_APPLICATIONS FAKE_SDK FAKE_XCODE_SELECT FAKE_LICENSE_STATUS FAKE_FIRSTLAUNCH_STATUS FAKE_SLEEP FAKE_AUTOLOGIN XBIN_RUNNER_SHA256

# ---- mac-cleanup.sh ------------------------------------------------------
local_env
export HOME=$tmp/mac2
old=202601010000
mk() { mkdir -p "$1"; touch -t "${2:-$old}" "$1"; }
mk "$HOME/Library/Developer/Xcode/DerivedData/Old-abc"
mk "$HOME/Library/Developer/Xcode/DerivedData/New-def" "$(date +%Y%m%d%H%M)"
mk "$HOME/Library/Caches/xbin-ci/xbin-mini/26.4-26E100"
mk "$HOME/Library/Caches/xbin-ci/xbin-mini/27.0-27A266a" "$(date +%Y%m%d%H%M)"
mk "$HOME/Library/Caches/xbin-ci/tools/xcodegen-2.46.0"
touch -t "$old" "$HOME/Library/Caches/xbin-ci/tools"
mk "$HOME/xbin-remote/out/build"
mk "$HOME/xbin-remote/derived"
run "$S/mac-cleanup.sh" --dry-run
eq "mac-cleanup --dry-run: removes nothing" "$rc:$(find "$HOME" -name Old-abc | wc -l | tr -d ' ')" "0:1"
has "mac-cleanup --dry-run: names what it would remove" "$out" "would remove $HOME/Library/Developer/Xcode/DerivedData/Old-abc"
: >"$FAKE_LOG"
run "$S/mac-cleanup.sh"
eq "mac-cleanup: runs" "$rc" 0
for gone in Library/Developer/Xcode/DerivedData/Old-abc Library/Caches/xbin-ci/xbin-mini/26.4-26E100 xbin-remote/out/build xbin-remote/derived; do
  if [ -e "$HOME/$gone" ]; then bad "mac-cleanup: $gone should be gone"; else ok "mac-cleanup: removes ~/$gone (a week unused)"; fi
done
for kept in Library/Developer/Xcode/DerivedData/New-def Library/Caches/xbin-ci/xbin-mini/27.0-27A266a Library/Caches/xbin-ci/tools/xcodegen-2.46.0; do
  isdir "mac-cleanup: keeps ~/$kept" "$HOME/$kept"
done
has "mac-cleanup: shuts the simulators down" "$(cat "$FAKE_LOG")" "xcrun simctl shutdown all"
has "mac-cleanup: deletes unavailable simulators" "$(cat "$FAKE_LOG")" "xcrun simctl delete unavailable"
mk "$HOME/Library/Developer/Xcode/DerivedData/Old-ghi"
FAKE_PGREP=0 run "$S/mac-cleanup.sh"
eq "mac-cleanup: skipped while a runner job runs" "$rc:$(find "$HOME" -name Old-ghi | wc -l | tr -d ' ')" "0:1"
has "mac-cleanup: …says so" "$out" "a runner job is running — skipped"
: >"$FAKE_LOG"
FAKE_SIMCTL_STATUS=1 run "$S/mac-cleanup.sh" --job-hook
eq "mac-cleanup --job-hook: never fails the job" "$rc" 0
has "mac-cleanup --job-hook: shuts the simulators down" "$(cat "$FAKE_LOG")" "xcrun simctl shutdown all"
run "$S/mac-cleanup.sh" --bogus
eq "mac-cleanup: an unknown argument fails" "$rc" 2
export HOME=$realhome

# ---- mac-remote.sh -------------------------------------------------------
if ! have git || ! have rsync; then
  echo "skip mac-remote.sh: needs git and rsync here"
  dry_done mac-dry-test
  exit $?
fi
local_env
export HOME=$tmp/mac3
mkdir -p "$HOME"
# The scratch tree as a git checkout: tracked, untracked and ignored files.
printf 'ignored.txt\nbuild/\n' >"$repo/.gitignore"
echo "name: Xbin" >"$repo/native/ios/project.yml"
echo "soon gone" >"$repo/tracked-gone.txt"
git -C "$repo" init -q
git -C "$repo" add -A
git -C "$repo" -c user.email=dry@test -c user.name=dry commit -qm tree
echo untracked >"$repo/untracked.txt"
echo ignored >"$repo/ignored.txt"
echo odd >"$repo/odd [1] *.txt"
mkdir -p "$repo/build" && echo x >"$repo/build/out.o"
export XBIN_MAC=me@mini XBIN_MAC_DIR=xbin-remote XBIN_MAC_PULL=$tmp/pull FAKE_REMOTE_HOME=$tmp/machome XBIN_MAC_PATH_PREFIX=
mkdir -p "$FAKE_REMOTE_HOME"
mirror=$FAKE_REMOTE_HOME/xbin-remote/tree

run "$S/mac-remote.sh"
eq "mac-remote: no command → usage" "$rc" 0
has "mac-remote: …lists the commands" "$out" "e2e [--port P] [--keep] [--only T]"
XBIN_MAC="" run "$S/mac-remote.sh" build
eq "mac-remote: no XBIN_MAC → refuses" "$rc" 2
run "$S/mac-remote.sh" frobnicate
eq "mac-remote: an unknown command fails" "$rc" 2

run "$S/mac-remote.sh" sync
eq "mac-remote sync: runs" "$rc" 0
isfile "mac-remote sync: tracked files" "$mirror/native/ios/scripts/mac-remote.sh"
isfile "mac-remote sync: untracked files" "$mirror/untracked.txt"
isfile "mac-remote sync: odd names (rsync wildcards escaped)" "$mirror/odd [1] *.txt"
if [ -e "$mirror/ignored.txt" ] || [ -e "$mirror/build" ]; then bad "mac-remote sync: ignored files reached the Mac"; else ok "mac-remote sync: .gitignore respected"; fi
if [ -e "$mirror/.git" ]; then bad "mac-remote sync: .git reached the Mac"; else ok "mac-remote sync: no .git"; fi
if [ -x "$mirror/native/ios/scripts/ci-build-app.sh" ]; then ok "mac-remote sync: modes kept"; else bad "mac-remote sync: modes lost"; fi
# What the Mac built in the mirror stays; what is gone here goes there.
mkdir -p "$mirror/native/ios/Packages/XbinCore/.build/debug" "$mirror/native/ios/Xbin.xcodeproj"
echo stale >"$mirror/stale.txt"
rm -f "$repo/untracked.txt" "$repo/tracked-gone.txt"
run "$S/mac-remote.sh" sync
if [ -e "$mirror/stale.txt" ] || [ -e "$mirror/untracked.txt" ] || [ -e "$mirror/tracked-gone.txt" ]; then bad "mac-remote sync: deletions did not propagate"; else ok "mac-remote sync: deletions propagate (the mirror is exact)"; fi
isdir "mac-remote sync: keeps the Mac's .build" "$mirror/native/ios/Packages/XbinCore/.build/debug"
isdir "mac-remote sync: keeps the Mac's generated .xcodeproj" "$mirror/native/ios/Xbin.xcodeproj"
has "mac-remote sync: Homebrew's rsync on the Mac" "$(cat "$FAKE_LOG")" "PATH=/opt/homebrew/bin:/usr/local/bin:\$PATH rsync"
rm -rf "$mirror/native/ios/Xbin.xcodeproj"

local_env
export FAKE_SIMCTL_JSON=$td/simctl-typical.json FAKE_SCHEMES="Xbin XbinUITests"
run "$S/mac-remote.sh" build
eq "mac-remote build: runs" "$rc" 0
log=$(cat "$FAKE_LOG")
has "mac-remote build: over ssh, stdin closed, the --on-mac half in the mirror" "$log" \
  "ssh -n me@mini cd xbin-remote/tree && env /bin/bash native/ios/scripts/mac-remote.sh --on-mac build"
has "mac-remote build: builds into the Mac's persistent DerivedData" "$log" \
  "-derivedDataPath $FAKE_REMOTE_HOME/xbin-remote/derived/app -clonedSourcePackagesDirPath $FAKE_REMOTE_HOME/xbin-remote/derived/SourcePackages"
has "mac-remote build: …unsigned by default" "$log" "CODE_SIGNING_ALLOWED=NO"
isfile "mac-remote build: pulls the log back" "$tmp/pull/build/app-build.log"
isdir "mac-remote build: …and the result bundle" "$tmp/pull/build/app-build.xcresult"
: >"$FAKE_LOG"
XBIN_SIGNING=adhoc XBIN_SIM="iPhone 17 Pro" run "$S/mac-remote.sh" build
has "mac-remote: passes XBIN_SIM / XBIN_SIGNING through" "$(cat "$FAKE_LOG")" "env XBIN_SIM=iPhone\\ 17\\ Pro XBIN_SIGNING=adhoc /bin/bash"
has "mac-remote: …and they apply there" "$(cat "$FAKE_LOG")" "-destination platform=iOS Simulator,id=BBBBBBBB-0000-4000-8000-000000002711"

: >"$FAKE_LOG"
FAKE_APP_PRODUCTS=$FAKE_REMOTE_HOME/xbin-remote/derived/app/Build/Products/Debug-iphonesimulator \
  run "$S/mac-remote.sh" run --url "xbin://127.0.0.1:9871/c/apps/counter" --wait 0
eq "mac-remote run: runs" "$rc" 0
log=$(cat "$FAKE_LOG")
u=BBBBBBBB-0000-4000-8000-000000002714
has "mac-remote run: builds signed to run locally" "$log" "CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual"
has "mac-remote run: boots" "$log" "xcrun simctl bootstatus $u -b"
has "mac-remote run: installs the app" "$log" "xcrun simctl install $u $FAKE_REMOTE_HOME/xbin-remote/derived/app/Build/Products/Debug-iphonesimulator/Xbin.app"
has "mac-remote run: launches it, output to files" "$log" \
  "xcrun simctl launch --terminate-running-process --stdout=$FAKE_REMOTE_HOME/xbin-remote/out/run/app.stdout.log --stderr=$FAKE_REMOTE_HOME/xbin-remote/out/run/app.stderr.log $u dev.xbin.app"
has "mac-remote run: opens the link" "$log" "xcrun simctl openurl $u xbin://127.0.0.1:9871/c/apps/counter"
has "mac-remote run: screenshots" "$log" "xcrun simctl io $u screenshot --type=png"
isfile "mac-remote run: pulls the screenshot" "$tmp/pull/run/screen.png"
isfile "mac-remote run: …and the app's log" "$tmp/pull/run/app.log"
rm -rf "$FAKE_REMOTE_HOME/xbin-remote/derived/app/Build"
run "$S/mac-remote.sh" run --wait 0
eq "mac-remote run: no app after the build fails" "$rc" 1

: >"$FAKE_LOG"
run "$S/mac-remote.sh" packages XbinCore
eq "mac-remote packages: runs ci-swift-test.sh there" "$rc" 0
has "mac-remote packages: …for the named packages" "$out" "XbinCore: not present"

# e2e against an xbind given by URL + token (the start/stop path below).
local_env
cp "$td/simctl-typical.json" "$tmp/sim-e2e.json"
export FAKE_SIMCTL_JSON=$tmp/sim-e2e.json FAKE_SCHEMES="Xbin XbinUITests" FAKE_E2E_PNGS=3
XBIN_E2E_URL=http://127.0.0.1:9871 XBIN_E2E_TOKEN=tok-owner-secret \
  run "$S/mac-remote.sh" e2e --only XbinUITests/XbinE2ETests/test03NativeCounter
eq "mac-remote e2e: runs" "$rc" 0
log=$(cat "$FAKE_LOG")
has "mac-remote e2e: a reverse tunnel on the same port, failing if it cannot bind" "$log" \
  "ssh -o ExitOnForwardFailure=yes -R 127.0.0.1:9871:127.0.0.1:9871 me@mini"
hasnt "mac-remote e2e: the token is on no command line" "$(grep '^ssh ' "$FAKE_LOG")" "tok-owner-secret"
has "mac-remote e2e: waits for the xbind through the tunnel" "$log" "curl http://127.0.0.1:9871/healthz"
has "mac-remote e2e: the simulator xbin-e2e (created when missing)" "$log" "xcrun simctl create xbin-e2e"
has "mac-remote e2e: …erased first" "$log" "xcrun simctl erase 99999999-0000-4000-8000-00000000C0DE"
has "mac-remote e2e: the tests get the URL and the token" "$log" "e2e-env URL=http://127.0.0.1:9871 TOKEN=tok-owner-secret"
has "mac-remote e2e: --only filters" "$log" "-only-testing:XbinUITests/XbinE2ETests/test03NativeCounter"
eq "mac-remote e2e: pulls the screenshots" "$(find "$tmp/pull/e2e" -name '*.png' | wc -l | tr -d ' ')" 3
XBIN_E2E_URL=http://127.0.0.1:9871 run "$S/mac-remote.sh" e2e
eq "mac-remote e2e: a URL without a token fails" "$rc" 2
: >"$FAKE_LOG"
XBIN_E2E_URL=http://10.0.0.5:8642 XBIN_E2E_TOKEN=t run "$S/mac-remote.sh" e2e
hasnt "mac-remote e2e: an xbind the Mac reaches itself → no tunnel" "$(cat "$FAKE_LOG")" " -R "

# e2e starting (and stopping) its own xbind: e2e-xbind.sh stood in for.
cat >"$S/e2e-xbind.sh" <<'SH'
#!/bin/sh
echo "e2e-xbind $*" >>"$FAKE_LOG"
if [ "$1" = start ]; then
  mkdir -p "$XBIN_E2E_DIR"
  printf 'XBIN_E2E_URL=http://127.0.0.1:%s\nXBIN_E2E_TOKEN=tok-started\n' "$3" >"$XBIN_E2E_DIR/env"
fi
SH
chmod +x "$S/e2e-xbind.sh"
: >"$FAKE_LOG"
XBIN_E2E_DIR=$tmp/e2edir run "$S/mac-remote.sh" e2e --port 9872
eq "mac-remote e2e: starts an xbind here" "$rc:$(grep '^e2e-xbind' "$FAKE_LOG" | tr '\n' '|')" "0:e2e-xbind start --port 9872|e2e-xbind stop|"
has "mac-remote e2e: …tunnels its port" "$(cat "$FAKE_LOG")" "-R 127.0.0.1:9872:127.0.0.1:9872"
has "mac-remote e2e: …hands its token over" "$(cat "$FAKE_LOG")" "TOKEN=tok-started"
: >"$FAKE_LOG"
XBIN_E2E_DIR=$tmp/e2edir run "$S/mac-remote.sh" e2e --port 9872 --keep
eq "mac-remote e2e --keep: leaves it running" "$(grep '^e2e-xbind' "$FAKE_LOG" | tr '\n' '|')" "e2e-xbind start --port 9872|"
git -C "$repo" checkout -q -- native/ios/scripts/e2e-xbind.sh

: >"$FAKE_LOG"
run "$S/mac-remote.sh" tunnel --port 9873
has "mac-remote tunnel: ssh -N with the reverse forward" "$(cat "$FAKE_LOG")" \
  "ssh -N -o ExitOnForwardFailure=yes -R 127.0.0.1:9873:127.0.0.1:9873 me@mini"
: >"$FAKE_LOG"
rm -rf "$FAKE_REMOTE_HOME/fresh"
XBIN_MAC_DIR=fresh XBIN_MAC_SSH_OPTS="-p 2222" run "$S/mac-remote.sh" setup --check
has "mac-remote setup: mac-setup.sh on a terminal there, with the ssh options" "$(cat "$FAKE_LOG")" \
  "ssh -t -p 2222 me@mini cd fresh/tree && /bin/bash native/ios/scripts/mac-setup.sh --check"
hasnt "mac-remote setup: no rsync before the Mac has Homebrew's" "$(cat "$FAKE_LOG")" "rsync"
isfile "mac-remote setup: the scripts reach the Mac by tar" "$FAKE_REMOTE_HOME/fresh/tree/native/ios/scripts/mac-setup.sh"
if [ -e "$FAKE_REMOTE_HOME/fresh/tree/untracked.txt" ] || [ -e "$FAKE_REMOTE_HOME/fresh/tree/native/ios/project.yml" ]; then
  bad "mac-remote setup: sent more than the scripts"
else
  ok "mac-remote setup: only the scripts"
fi
: >"$FAKE_LOG"
run "$S/mac-remote.sh" cleanup --dry-run
has "mac-remote cleanup: mac-cleanup.sh there" "$out" "mac-cleanup: sweep"
export HOME=$realhome

dry_done mac-dry-test
