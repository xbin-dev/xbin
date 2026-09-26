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
# a job hook that shuts the simulators down).
#
# Then the box — reported, never changed, as it sits headless in a
# datacenter (native/AGENTS.md → "Mac mini"): pmset (sleep 0, autorestart
# 1, womp 1), restart after a freeze, FileVault, automatic login, the users
# (an admin; XBIN_CI_USER, default ci, and XBIN_RELEASE_USER, default
# release, both standard), Remote Login and sshd's key-only hardening
# (AllowUsers), the firewall and stealth mode, Screen Sharing restricted, no
# Apple ID, a display (the HDMI dummy plug), free disk (XBIN_MIN_FREE_GB,
# default 50), the Xcodes, SDKs and simulator runtimes, and whether the
# runner runs. Warnings don't fail --check; each says how to fix it.
#
# Users: the admin runs this first (`--no-runner`: sudo for Xcode and
# Homebrew), then the standard user ci runs it with the token (its own
# simulators, cleanup agent and runner). It refuses to put the runner under
# the release user, and warns when an admin would hold it.
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
ci_user=${XBIN_CI_USER:-ci}
release_user=${XBIN_RELEASE_USER:-release}
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
elif [ "$(id -un)" = "$release_user" ]; then
  warn "the Actions runner: not as $release_user — the user holding the release key never runs CI (run this as $ci_user)"
else
  if [ "$(id -un)" != "$ci_user" ] && dsmemberutil checkmembership -U "$(id -un)" -G admin 2>/dev/null | grep -q 'is a member'; then
    warn "the runner runs as $(id -un), an admin: register it as the standard user $ci_user (native/AGENTS.md → Mac mini)"
  fi
  item "Actions runner $name for $repo_url ($runner_dir, labels self-hosted,macOS,xbin-mini, launchd, job hook)" runner_ready setup_runner
fi

# ---- the box: headless in a datacenter (reported, never changed) ---------
# native/AGENTS.md → "Mac mini": a Mac on an isolated VLAN, ssh in over a
# VPN, a KVM-over-IP on its HDMI/USB, a switchable PDU for its power. These
# are the owner's settings: this only says what is set and how to set it.
echo "--      the box"
info() { echo "info    $*"; }
me=$(id -un)
is_admin() { dsmemberutil checkmembership -U "$1" -G admin 2>/dev/null | grep -q 'is a member'; }
user_exists() { dscl . -read "/Users/$1" UniqueID >/dev/null 2>&1; }

pm=$(pmset -g 2>/dev/null || true)
pmval() { printf '%s\n' "$pm" | awk -v k="$1" '$1 == k { print $2; exit }'; }
if [ "$(pmval sleep)" = 0 ]; then ok "system sleep off"; else warn "the Mac may sleep: sudo pmset -a sleep 0 (a sleeping runner takes no jobs)"; fi
if [ "$(pmval autorestart)" = 1 ]; then ok "restarts after a power loss (pmset autorestart 1)"
else warn "stays off after a power loss: sudo pmset -a autorestart 1 (the PDU's power cycle then brings it back)"; fi
if [ "$(pmval womp)" = 1 ]; then ok "wakes for network access (pmset womp 1)"; else warn "no wake for network access: sudo pmset -a womp 1"; fi
rf=$(systemsetup -getrestartfreeze 2>/dev/null || true)
case $rf in
*": On"*) ok "restarts after a system freeze (systemsetup restartfreeze on)" ;;
*": Off"*) warn "no restart after a system freeze: sudo systemsetup -setrestartfreeze on" ;;
*) info "restart after a freeze: unknown (systemsetup needs an admin — run --check as one)" ;;
esac

fv=$(fdesetup status 2>/dev/null || true)
fv_on=""
case $fv in
*"FileVault is On"*)
  fv_on=1
  ok "FileVault on (after a power loss the disk waits for a password at the KVM-over-IP; planned reboots: sudo fdesetup authrestart)" ;;
*"FileVault is Off"*)
  fv_on=0
  warn "FileVault off: this Mac holds the release user's App Store Connect key — sudo fdesetup enable (then unlock through the KVM-over-IP after a power loss; native/AGENTS.md → Mac mini)" ;;
*) info "FileVault: ${fv:-unknown}" ;;
esac
autologin=$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null || true)
if [ -n "$autologin" ]; then
  ok "automatic login ($autologin)"
  [ "$autologin" = "$ci_user" ] || warn "automatic login is $autologin's, but the runner is $ci_user's: its LaunchAgent starts with $ci_user's session"
elif [ "$fv_on" = 1 ]; then
  ok "no automatic login (FileVault on: unlocking the disk as $ci_user — KVM-over-IP, or authrestart with $ci_user's password — logs $ci_user in, and the runner starts)"
else
  warn "no automatic login: after a reboot the runner (a LaunchAgent) waits for someone to log in (System Settings → Users & Groups; with FileVault on, unlocking as $ci_user does it)"
fi

admins="" others=""
for u in $(dscl . -list /Users UniqueID 2>/dev/null | awk '$2 >= 501 && $1 !~ /^_/ { print $1 }'); do
  if is_admin "$u"; then admins="$admins $u"; else others="$others $u"; fi
done
info "users: admin:${admins:- none}; standard:${others:- none}; this is $me"
[ -n "$admins" ] || warn "no admin user found (dscl/dsmemberutil): the owner's account does setup and maintenance"
for pair in "$ci_user:the Actions runner, the simulators, the ssh dev loop" "$release_user:the App Store Connect key and release-build.sh"; do
  u=${pair%%:*}
  role=${pair#*:}
  if ! user_exists "$u"; then
    warn "no user $u ($role): sudo sysadminctl -addUser $u -fullName '$u' -password - (a standard user, not an admin)"
  elif is_admin "$u"; then
    warn "$u is an admin ($role): make it a standard user (System Settings → Users & Groups)"
  else
    ok "user $u, standard ($role)"
  fi
done

rl=$(systemsetup -getremotelogin 2>/dev/null || true)
case $rl in
*": On"*) ok "Remote Login on" ;;
*": Off"*) warn "Remote Login off: sudo systemsetup -setremotelogin on (key-only, below)" ;;
*)
  if nc -z -G 2 127.0.0.1 22 >/dev/null 2>&1; then ok "Remote Login on (sshd answers on port 22)"
  else warn "Remote Login seems off (nothing answers on port 22): System Settings → General → Sharing → Remote Login"; fi ;;
esac
sshd_conf=${XBIN_SSHD_CONFIG:-/etc/ssh/sshd_config}
# sshd_opt <keyword>: its value as sshd reads it — the first one wins, and
# macOS's sshd_config includes sshd_config.d/*.conf at its top; Match
# blocks are not followed.
sshd_opt() {
  local k f v
  k=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  for f in "$sshd_conf.d"/*.conf "$sshd_conf"; do
    [ -r "$f" ] || continue
    v=$(awk -v k="$k" 'tolower($1) == "match" { exit } tolower($1) == k { $1 = ""; sub(/^[ \t]+/, ""); print; exit }' "$f")
    if [ -n "$v" ]; then printf '%s\n' "$v"; return 0; fi
  done
  return 1
}
dropin="$sshd_conf.d/100-xbin.conf"
pa=$(sshd_opt PasswordAuthentication || echo yes)
kbd=$(sshd_opt KbdInteractiveAuthentication || sshd_opt ChallengeResponseAuthentication || echo yes)
if [ "$pa" = no ] && [ "$kbd" = no ]; then ok "sshd: keys only (PasswordAuthentication no, KbdInteractiveAuthentication no)"
else warn "sshd accepts passwords (PasswordAuthentication $pa, KbdInteractiveAuthentication $kbd — PAM asks for one through the latter): set both to no in $dropin"; fi
prl=$(sshd_opt PermitRootLogin || echo prohibit-password)
if [ "$prl" = no ]; then ok "sshd: no root login"; else warn "sshd: PermitRootLogin $prl — set it to no in $dropin"; fi
if [ "$(sshd_opt PubkeyAuthentication || echo yes)" = no ]; then warn "sshd: PubkeyAuthentication no — keys are the only way in: yes"; fi
allow=$(sshd_opt AllowUsers || true)
if [ -z "$allow" ]; then
  warn "sshd: no AllowUsers — every account may log in: AllowUsers <admin> $ci_user $release_user in $dropin"
else
  ok "sshd: AllowUsers $allow"
fi
la=$(sshd_opt ListenAddress || true)
[ -z "$la" ] || info "sshd listens on $la"

fw=${XBIN_SOCKETFILTERFW:-/usr/libexec/ApplicationFirewall/socketfilterfw}
case $("$fw" --getglobalstate 2>/dev/null || true) in
*"is enabled"*) ok "firewall on" ;;
*) warn "firewall off: sudo $fw --setglobalstate on" ;;
esac
case $("$fw" --getstealthmode 2>/dev/null || true) in
*"stealth mode is on"* | *"Stealth mode enabled"* | *"stealth mode enabled"*) ok "firewall stealth mode on" ;;
*) warn "stealth mode off (the Mac answers probes): sudo $fw --setstealthmode on" ;;
esac
if launchctl print system/com.apple.screensharing >/dev/null 2>&1; then
  ss=$(dscl . -read /Groups/com.apple.access_screensharing GroupMembership 2>/dev/null | sed 's/^GroupMembership:[ ]*//')
  case " $ss " in
  "  ") warn "Screen Sharing on for every user: System Settings → General → Sharing → Screen Sharing → Allow access for: Only these users (the admin)" ;;
  *" $ci_user "* | *" $release_user "*) warn "Screen Sharing lets $ss in: only the admin (not $ci_user or $release_user)" ;;
  *) ok "Screen Sharing on, only for:$([ -n "$ss" ] && echo " $ss")" ;;
  esac
else
  ok "Screen Sharing off (the KVM-over-IP is the console)"
fi
if defaults read MobileMeAccounts Accounts 2>/dev/null | grep -q AccountID; then
  warn "an Apple ID is signed in for $me: sign it out (System Settings → the account) — nothing on this box syncs or buys anything"
else
  ok "no Apple ID signed in ($me)"
fi
if system_profiler SPDisplaysDataType 2>/dev/null | grep -q 'Resolution:'; then
  ok "a display is attached (the HDMI dummy plug)"
else
  warn "no display attached: an HDMI dummy plug gives the login session a real screen (the KVM-over-IP, Screen Sharing, the simulators)"
fi
free_gb=$(df -Pk / 2>/dev/null | awk 'NR == 2 { print int($4 / 1048576) }')
min_gb=${XBIN_MIN_FREE_GB:-50}
if [ -n "$free_gb" ] && [ "$free_gb" -ge "$min_gb" ]; then ok "disk: $free_gb GB free"
else warn "disk: ${free_gb:-?} GB free (under $min_gb GB — old Xcodes and simulator runtimes go first: xcrun simctl runtime delete, mac-cleanup.sh)"; fi

for a in "$apps"/Xcode*.app; do
  [ -d "$a" ] || continue
  v=$(defaults read "$a/Contents/Info" CFBundleShortVersionString 2>/dev/null || echo '?')
  bv=$(defaults read "$a/Contents/version" ProductBuildVersion 2>/dev/null || echo '?')
  info "Xcode $v ($bv) $a$([ "$a" = "$xcode" ] && echo ' — selected')"
done
info "SDKs: $(xcodebuild -showsdks 2>/dev/null | sed -n 's/.*-sdk \([a-z]*[0-9.]*\).*/\1/p' | paste -sd ' ' -)"
info "simulator runtimes: $(xcrun simctl list runtimes -j 2>/dev/null | python3 -c '
import json, sys
try:
    rts = json.load(sys.stdin).get("runtimes", [])
except ValueError:
    rts = []
print(", ".join(r.get("name", "?") + ("" if r.get("isAvailable", True) else " (unavailable)") for r in rts) or "none")' 2>/dev/null)"
if registered; then
  for p in "$HOME"/Library/LaunchAgents/actions.runner.*.plist; do
    [ -f "$p" ] || continue
    l=$(basename "$p" .plist)
    if launchctl print "gui/$uid/$l" 2>/dev/null | grep -q 'state = running'; then ok "runner $l running"
    else warn "runner $l not running (cd $runner_dir && ./svc.sh start — it runs in $me's login session)"; fi
  done
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
