#!/usr/bin/env bash
# native/ios/scripts/mac-setup.sh — make a Mac (the Mac mini) ready to build
# and test the iOS client, and to be the repository's self-hosted Actions
# runner. Idempotent: each item is checked first and only fixed when
# missing; run it again after an Xcode update. native/AGENTS.md → "Mac mini".
#
#   mac-setup.sh --check                 report only; exit 1 when anything is missing
#   mac-setup.sh [--runner-token T]      fix what is missing (asks for sudo
#                                        where Apple's tools need it)
#
#   --runner-token T | --runner-token-stdin   a registration token for the
#       runner: GitHub → the repository → Settings → Actions → Runners → New
#       self-hosted runner (valid for an hour; repository-scoped — never an
#       organization's). Without one, and not registered yet, the runner is
#       skipped (asked for interactively on a terminal).
#   --repo URL    default https://github.com/xbin-dev/xbin (XBIN_RUNNER_REPO)
#   --name N      the runner's name, default xbin-mini (XBIN_RUNNER_NAME)
#   --no-runner   leave the runner alone
#
# Items, in order: macOS; Xcode (XBIN_XCODE, else the selected one, else the
# newest /Applications/Xcode*.app — installing it is yours: the App Store or
# `xcodes`); xcode-select; the licence; the first-launch packages; the iOS
# simulator platform matching the SDK (xcodebuild -downloadPlatform iOS);
# the Metal toolchain (SwiftTerm's shader); Homebrew with xcodegen,
# xcbeautify and rsync (mac-remote.sh syncs with it); the simulator
# `xbin-e2e` (the UI tests' own, erased before each run); the daily cleanup
# (a LaunchAgent running mac-cleanup.sh: DerivedData, caches and simulators
# nobody used for a week); the Actions runner (pinned release, SHA-256
# checked, labels self-hosted, macOS, xbin-mini, as a launchd service, with
# a job hook that shuts the simulators down). Checked but only warned
# about: sleep (a CI machine must not sleep) and automatic login (the
# runner's LaunchAgent starts with the login session).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$here/ci-lib.sh"

RUNNER_VERSION=${XBIN_RUNNER_VERSION:-2.337.0}
RUNNER_SHA256_arm64=5a2cd92908a93d7276a194e1de6008099f3e7946f3f8e14aa7a1a7b4a31fdec2
RUNNER_SHA256_x64=d383f505d7ed041b1873ab68c35dd766fc093f2252330f95bb427be8f2c6dcfc

check=0 no_runner=0
token=${XBIN_RUNNER_TOKEN:-}
repo_url=${XBIN_RUNNER_REPO:-https://github.com/xbin-dev/xbin}
name=${XBIN_RUNNER_NAME:-xbin-mini}
while [ $# -gt 0 ]; do
  case $1 in
  --check) check=1; shift ;;
  --runner-token) token=${2:?--runner-token needs a value}; shift 2 ;;
  --runner-token-stdin) IFS= read -r token; shift ;;
  --repo) repo_url=${2:?}; shift 2 ;;
  --name) name=${2:?}; shift 2 ;;
  --no-runner) no_runner=1; shift ;;
  -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
  *) echo "mac-setup: unknown argument $1 (--help)" >&2; exit 2 ;;
  esac
done

apps=${XBIN_APPLICATIONS:-/Applications}
home_ci=${XBIN_CI_HOME:-$HOME/xbin-ci}
runner_dir=${XBIN_RUNNER_DIR:-$HOME/actions-runner}
agent_label=dev.xbin.ci-cleanup
agent_plist=$HOME/Library/LaunchAgents/$agent_label.plist
uid=$(id -u)
missing="" warned=""

ok() { echo "ok      $*"; }
warn() { echo "warn    $*"; warned="$warned
  - $*"; }
miss() { echo "MISSING $*"; missing="$missing
  - $*"; }

# item <what> <check function> <fix function> — fix only when the check
# fails and this is not --check; check again after the fix.
item() {
  local what=$1 chk=$2 fix=$3
  if "$chk"; then ok "$what"; return 0; fi
  if [ "$check" = 1 ]; then miss "$what"; return 0; fi
  echo ">>      $what: setting up"
  if "$fix" && "$chk"; then ok "$what (done)"; else miss "$what"; fi
}

[ "$(uname -s)" = Darwin ] || { echo "mac-setup: this runs on the Mac (uname: $(uname -s))" >&2; exit 1; }
echo "macOS $(sw_vers -productVersion 2>/dev/null || echo '?') on $(uname -m)$([ "$check" = 1 ] && echo ' — check only')"

# ---- Xcode ---------------------------------------------------------------
xcode=""
if [ -n "${XBIN_XCODE:-}" ]; then
  xcode=${XBIN_XCODE%/}
else
  sel=$(xcode-select -p 2>/dev/null || true)
  case $sel in
  *.app/Contents/Developer) xcode=${sel%/Contents/Developer} ;;
  esac
  if [ -z "$xcode" ] || [ ! -d "$xcode/Contents/Developer" ]; then
    # The newest Xcode*.app by name (Xcode_27.1 > Xcode_27.0 > Xcode).
    xcode=$({ find "$apps" -maxdepth 1 -name 'Xcode*.app' -type d 2>/dev/null || true; } | LC_ALL=C sort | tail -n 1)
  fi
fi
has_xcode() { [ -n "$xcode" ] && [ -d "$xcode/Contents/Developer" ]; }
no_fix_xcode() { return 1; }
item "Xcode (${xcode:-none found in $apps})" has_xcode no_fix_xcode
if ! has_xcode; then
  printf '\nmac-setup: no Xcode, so nothing else can be checked: install Xcode 27 (the App Store, or\nbrew install xcodes && xcodes install 27.0), open it once, then run this again.\n' >&2
  exit 1
fi
dev="$xcode/Contents/Developer"
export DEVELOPER_DIR=$dev

selected() { [ "$(xcode-select -p 2>/dev/null || true)" = "$dev" ]; }
select_it() { sudo xcode-select -s "$dev"; }
item "xcode-select → $dev" selected select_it

licensed() { xcodebuild -license check >/dev/null 2>&1; }
accept() { sudo xcodebuild -license accept; }
item "Xcode licence accepted" licensed accept

launched() { xcodebuild -checkFirstLaunchStatus >/dev/null 2>&1; }
first_launch() { sudo xcodebuild -runFirstLaunch; }
item "Xcode first-launch packages" launched first_launch

# The simulator runtime of the SDK's own version (iphonesimulator27.0 →
# an available iOS 27.0 runtime).
sdk=$(xcodebuild -showsdks 2>/dev/null | sed -n 's/.*-sdk iphonesimulator\([0-9.]*\).*/\1/p' | tail -n 1)
platform() {
  [ -n "$sdk" ] || return 1
  xcrun simctl list runtimes -j 2>/dev/null | python3 -c '
import json, sys
want = sys.argv[1]
rts = json.load(sys.stdin).get("runtimes", [])
ok = any(r.get("isAvailable", True) and (r.get("platform") == "iOS" or ".iOS-" in r.get("identifier", ""))
         and (r.get("version", "") == want or r.get("version", "").startswith(want + ".")) for r in rts)
sys.exit(0 if ok else 1)' "$sdk"
}
download_platform() { ci_timeout 3600 xcodebuild -downloadPlatform iOS; }
item "iOS ${sdk:-?} simulator runtime" platform download_platform

metal() { xcrun metal --version >/dev/null 2>&1; }
download_metal() { ci_timeout 1800 xcodebuild -downloadComponent MetalToolchain; }
item "Metal toolchain (SwiftTerm's shader)" metal download_metal

# ---- Homebrew ------------------------------------------------------------
brew_bin=""
for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do [ -x "$b" ] && brew_bin=$b && break; done
[ -n "$brew_bin" ] || brew_bin=$(command -v brew 2>/dev/null || true)
has_brew() { [ -n "$brew_bin" ] && [ -x "$brew_bin" ]; }
install_brew() {
  echo "        the Homebrew installer (https://brew.sh) — it asks for your password"
  NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do [ -x "$b" ] && brew_bin=$b && break; done
}
item "Homebrew" has_brew install_brew
if has_brew; then
  PATH="$(dirname "$brew_bin"):$PATH"
  export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ENV_HINTS=1
  for formula in xcodegen xcbeautify rsync; do
    f=$formula
    brewed() { "$brew_bin" list --formula "$f" >/dev/null 2>&1; }
    brew_install() { "$brew_bin" install "$f"; }
    item "brew: $formula" brewed brew_install
  done
fi

# ---- simulators ----------------------------------------------------------
e2e_sim() {
  xcrun simctl list devices available -j 2>/dev/null | python3 -c '
import json, sys
devs = json.load(sys.stdin).get("devices", {})
sys.exit(0 if any(d.get("name") == "xbin-e2e" for ds in devs.values() for d in ds) else 1)'
}
make_e2e_sim() { XBIN_SIM_ENSURE=xbin-e2e "$here/pick-sim.sh" >/dev/null; }
item "simulator xbin-e2e (the UI tests', erased before each run)" e2e_sim make_e2e_sim

# ---- the daily cleanup ---------------------------------------------------
plist_body() {
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$agent_label</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/bash</string>
		<string>$home_ci/bin/mac-cleanup.sh</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>4</integer>
		<key>Minute</key>
		<integer>30</integer>
	</dict>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
		<key>DEVELOPER_DIR</key>
		<string>$dev</string>
	</dict>
	<key>StandardOutPath</key>
	<string>$HOME/Library/Logs/xbin-ci/cleanup.log</string>
	<key>StandardErrorPath</key>
	<string>$HOME/Library/Logs/xbin-ci/cleanup.log</string>
	<key>LowPriorityIO</key>
	<true/>
	<key>Nice</key>
	<integer>10</integer>
</dict>
</plist>
EOF
}
agent_loaded() { launchctl print "gui/$uid/$agent_label" >/dev/null 2>&1 || launchctl print "user/$uid/$agent_label" >/dev/null 2>&1; }
cleanup_installed() {
  cmp -s "$here/mac-cleanup.sh" "$home_ci/bin/mac-cleanup.sh" &&
    [ -f "$agent_plist" ] && [ "$(plist_body)" = "$(cat "$agent_plist")" ] && agent_loaded
}
install_cleanup() {
  mkdir -p "$home_ci/bin" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs/xbin-ci" || return 1
  cp "$here/mac-cleanup.sh" "$home_ci/bin/mac-cleanup.sh" && chmod 755 "$home_ci/bin/mac-cleanup.sh" || return 1
  plist_body >"$agent_plist" || return 1
  launchctl bootout "gui/$uid/$agent_label" >/dev/null 2>&1 || launchctl bootout "user/$uid/$agent_label" >/dev/null 2>&1 || true
  # gui/ exists while the user is logged in (automatic login); over plain
  # ssh without one, the user/ domain still runs it.
  launchctl bootstrap "gui/$uid" "$agent_plist" 2>/dev/null || launchctl bootstrap "user/$uid" "$agent_plist"
}
item "daily cleanup ($agent_label → $home_ci/bin/mac-cleanup.sh, 04:30)" cleanup_installed install_cleanup

# ---- the Actions runner --------------------------------------------------
hook_line() {
  printf 'ACTIONS_RUNNER_HOOK_JOB_STARTED=%s\nACTIONS_RUNNER_HOOK_JOB_COMPLETED=%s\n' "$home_ci/bin/job-hook.sh" "$home_ci/bin/job-hook.sh"
}
registered() { [ -f "$runner_dir/.runner" ]; }
service() {
  local p
  for p in "$HOME"/Library/LaunchAgents/actions.runner.*.plist; do [ -f "$p" ] && return 0; done
  return 1
}
hooked() {
  [ -x "$home_ci/bin/job-hook.sh" ] && [ -f "$runner_dir/.env" ] &&
    grep -qxF "ACTIONS_RUNNER_HOOK_JOB_COMPLETED=$home_ci/bin/job-hook.sh" "$runner_dir/.env" &&
    grep -qxF "ACTIONS_RUNNER_HOOK_JOB_STARTED=$home_ci/bin/job-hook.sh" "$runner_dir/.env"
}
runner_ready() { registered && service && hooked; }
setup_runner() {
  local arch sum tmp got
  if ! registered; then
    if [ -z "$token" ] && [ -t 0 ]; then
      printf 'A registration token for %s (Settings → Actions → Runners → New self-hosted runner), or empty to skip: ' "$repo_url"
      IFS= read -rs token
      echo
    fi
    if [ -z "$token" ]; then
      echo "        no --runner-token: not registering (run again with one)"
      return 1
    fi
    case $(uname -m) in
    arm64) arch=arm64 sum=$RUNNER_SHA256_arm64 ;;
    *) arch=x64 sum=$RUNNER_SHA256_x64 ;;
    esac
    sum=${XBIN_RUNNER_SHA256:-$sum}
    tmp=$(mktemp -d "${TMPDIR:-/tmp}/actions-runner.XXXXXX") || return 1
    if ! curl -fsSL --retry 3 -o "$tmp/runner.tar.gz" \
      "https://github.com/actions/runner/releases/download/v$RUNNER_VERSION/actions-runner-osx-$arch-$RUNNER_VERSION.tar.gz"; then
      rm -rf "$tmp"
      return 1
    fi
    got=$(ci_sha256 <"$tmp/runner.tar.gz")
    if [ "$got" != "$sum" ]; then
      echo "        actions-runner-osx-$arch-$RUNNER_VERSION.tar.gz: SHA-256 $got, expected $sum" >&2
      rm -rf "$tmp"
      return 1
    fi
    mkdir -p "$runner_dir" && tar -xzf "$tmp/runner.tar.gz" -C "$runner_dir" || { rm -rf "$tmp"; return 1; }
    rm -rf "$tmp"
    # Repository-scoped (the URL is the repository's); self-hosted, macOS and
    # the architecture are its default labels, xbin-mini the one ios.yml's
    # XBIN_IOS_RUNNER names. Brew's tools must be on PATH now: config.sh
    # records PATH for the service.
    (cd "$runner_dir" && ./config.sh --unattended --url "$repo_url" --token "$token" --name "$name" \
      --labels xbin-mini --work _work --replace) || return 1
  fi
  mkdir -p "$home_ci/bin" || return 1
  cp "$here/mac-cleanup.sh" "$home_ci/bin/mac-cleanup.sh" || return 1
  printf '#!/bin/bash\n# The runner'"'"'s job hook (mac-setup.sh): every job starts and ends with no simulator running.\nexec /bin/bash %s --job-hook\n' \
    "$home_ci/bin/mac-cleanup.sh" >"$home_ci/bin/job-hook.sh" || return 1
  chmod 755 "$home_ci/bin/mac-cleanup.sh" "$home_ci/bin/job-hook.sh"
  touch "$runner_dir/.env"
  { grep -Ev '^ACTIONS_RUNNER_HOOK_JOB_(STARTED|COMPLETED)=' "$runner_dir/.env" || true; hook_line; } >"$runner_dir/.env.new" &&
    mv "$runner_dir/.env.new" "$runner_dir/.env" || return 1
  if ! service; then
    (cd "$runner_dir" && ./svc.sh install) || return 1
  fi
  # (Re)start it so it reads .env; only reached when something was missing.
  (cd "$runner_dir" && { ./svc.sh stop >/dev/null 2>&1 || true; } && ./svc.sh start)
}
if [ "$no_runner" = 1 ]; then
  echo "skip    the Actions runner (--no-runner)"
else
  item "Actions runner $name for $repo_url ($runner_dir, labels self-hosted,macOS,xbin-mini, launchd, job hook)" runner_ready setup_runner
fi

# ---- warnings only -------------------------------------------------------
if [ "$(pmset -g 2>/dev/null | awk '$1 == "sleep" { print $2; exit }')" = 0 ]; then
  ok "system sleep off"
else
  warn "the Mac may sleep: sudo pmset -a sleep 0 (a sleeping runner takes no jobs)"
fi
if defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser >/dev/null 2>&1; then
  ok "automatic login ($(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null))"
else
  warn "no automatic login: after a reboot the runner (a LaunchAgent) waits for someone to log in (System Settings → Users & Groups)"
fi

echo
if [ -n "$warned" ]; then printf 'warnings:%s\n' "$warned"; fi
if [ -n "$missing" ]; then
  printf 'mac-setup: not ready:%s\n' "$missing" >&2
  exit 1
fi
echo "mac-setup: ready."
if [ "$no_runner" = 0 ] && registered; then
  echo "Next: gh variable set XBIN_IOS_RUNNER --body '[\"self-hosted\",\"macOS\",\"xbin-mini\"]' (then ios.yml runs here)."
fi
