#!/usr/bin/env bash
# native/ios/scripts/mac-remote.sh — the Mac over ssh: the Apple toolchain
# in minutes, not a CI round trip (native/AGENTS.md → "Mac mini"). Mirrors
# this working tree to the Mac (tracked and untracked files, .gitignore
# respected, deletions included), runs the same scripts CI runs there, and
# pulls the results back.
#
#   XBIN_MAC=user@host mac-remote.sh <command> [args]
#
#   sync                       mirror the tree to <XBIN_MAC_DIR>/tree
#   setup [--check] [args…]    sync, then mac-setup.sh there (interactive:
#                              sudo, the runner token)
#   toolchain                  ci-toolchain.sh (Xcode, SDKs, simulators)
#   packages [Pkg…]            swift test (ci-swift-test.sh)
#   build                      xcodegen + build the app for a simulator
#   snapshots                  the renderer snapshots, package + hosted
#   uitests                    build the UI tests (compile only)
#   run [--url L] [--wait S]   build, boot a simulator, install, launch (open
#                              the xbin:// link L), screenshot after S s (8),
#                              the app's log
#   e2e [--port P] [--keep] [--only T]…
#                              the UI tests against an xbind on THIS box:
#                              e2e-xbind.sh starts one on 127.0.0.1:P (9871;
#                              or give XBIN_E2E_URL + XBIN_E2E_TOKEN), an ssh
#                              reverse tunnel makes it 127.0.0.1:P on the Mac
#                              too, and XbinUITests runs on the simulator
#                              xbin-e2e, erased first; the token travels on
#                              ssh's stdin, never on a command line. --keep
#                              leaves the xbind running.
#   tunnel [--port P]          only the reverse tunnel, in the foreground
#   shell                      a login shell in the tree on the Mac
#   pull [command]             fetch the results again
#   cleanup [--dry-run]        mac-cleanup.sh there
#
#   XBIN_MAC           ssh destination (required), e.g. me@mini.local
#   XBIN_MAC_DIR       the base on the Mac, relative to its home unless
#                      absolute (default xbin-remote): tree/ (the mirror),
#                      derived/ (DerivedData, kept between runs), out/<command>/
#   XBIN_MAC_PULL      where results land here (default ${TMPDIR:-/tmp}/xbin-mac):
#                      <command>/ — logs, .xcresult bundles, PNGs
#   XBIN_MAC_SSH_OPTS  extra ssh options, e.g. "-p 2222 -i ~/.ssh/mini"
#   XBIN_SIM, XBIN_SIGNING, XBIN_XCODE, XBIN_SWIFT_CONDITIONS, XBIN_SIM_GUI=1
#                      passed through (pick-sim.sh, ci-*.sh; GUI: show the
#                      Simulator window on the Mac's screen)
#
# The same file runs on the Mac as `mac-remote.sh --on-mac <command>` (what
# the commands above invoke over ssh; usable at the Mac too). Bash 3.2.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)

say() { echo "mac-remote: $*" >&2; }
usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; }

# ======================================================================
# On the Mac
# ======================================================================
on_mac() {
  local cmd=${1:-}
  [ $# -gt 0 ] && shift
  # Non-interactive ssh sessions skip the login profile: Homebrew's PATH.
  local prefix=${XBIN_MAC_PATH_PREFIX-/opt/homebrew/bin:/usr/local/bin}
  if [ -n "$prefix" ]; then PATH="$prefix:$PATH"; fi
  export PATH
  local base
  base=${XBIN_MAC_BASE:-$(dirname "$repo")}
  export XBIN_CI_OUT="$base/out/$cmd" XBIN_CI_DERIVED="$base/derived"
  export XBIN_CI_TOOLS="$base/tools"
  rm -rf "$XBIN_CI_OUT"
  mkdir -p "$XBIN_CI_OUT" "$XBIN_CI_DERIVED"
  touch "$XBIN_CI_DERIVED" # mac-cleanup.sh drops it after a week unused
  # shellcheck source=SCRIPTDIR/ci-lib.sh
  . "$here/ci-lib.sh"
  local S=$here dest udid status=0
  xcodegen_on_path() {
    local p
    p=$(mktemp "${TMPDIR:-/tmp}/xcodegen-path.XXXXXX")
    GITHUB_PATH=$p "$S/ci-xcodegen.sh" >/dev/null
    if [ -s "$p" ]; then PATH="$(cat "$p"):$PATH"; fi
    rm -f "$p"
  }
  case $cmd in
  toolchain) "$S/ci-toolchain.sh" ;;
  packages) "$S/ci-swift-test.sh" "$@" ;;
  build)
    xcodegen_on_path
    dest=$("$S/pick-sim.sh")
    "$S/ci-build-app.sh" "$dest"
    ;;
  uitests)
    xcodegen_on_path
    dest=$("$S/pick-sim.sh")
    XBIN_E2E_URL="" "$S/ci-uitests.sh" "$dest"
    ;;
  snapshots)
    xcodegen_on_path
    dest=$("$S/pick-sim.sh")
    TEST_RUNNER_SNAPSHOT_DIR="$XBIN_CI_OUT/snapshots" "$S/ci-snapshots.sh" "$dest" || status=$?
    TEST_RUNNER_SNAPSHOT_DIR="$XBIN_CI_OUT/snapshots/hosted" "$S/ci-hosted-snapshots.sh" "$dest" || status=$?
    return "$status"
    ;;
  run)
    local url="" wait=8 app
    while [ $# -gt 0 ]; do
      case $1 in
      --url) url=$2; shift 2 ;;
      --wait) wait=$2; shift 2 ;;
      *) say "run: unknown argument $1"; return 2 ;;
      esac
    done
    xcodegen_on_path
    dest=$("$S/pick-sim.sh")
    udid=$(ci_udid "$dest")
    # Signed to run locally: the app's Keychain needs its entitlements.
    XBIN_SIGNING=${XBIN_SIGNING:-adhoc} "$S/ci-build-app.sh" "$dest"
    app="$XBIN_CI_DERIVED/app/Build/Products/Debug-iphonesimulator/Xbin.app"
    [ -d "$app" ] || { ci_error "no $app after the build"; return 1; }
    xcrun simctl boot "$udid" >/dev/null 2>&1 || true # already booted is fine
    ci_timeout 300 xcrun simctl bootstatus "$udid" -b || ci_warn "simulator $udid did not finish booting"
    if [ "${XBIN_SIM_GUI:-0}" = 1 ]; then open -a Simulator --args -CurrentDeviceUDID "$udid" || true; fi
    xcrun simctl install "$udid" "$app"
    xcrun simctl launch --terminate-running-process --stdout="$XBIN_CI_OUT/app.stdout.log" \
      --stderr="$XBIN_CI_OUT/app.stderr.log" "$udid" dev.xbin.app
    if [ -n "$url" ]; then
      sleep 2
      xcrun simctl openurl "$udid" "$url"
    fi
    sleep "$wait"
    xcrun simctl io "$udid" screenshot --type=png "$XBIN_CI_OUT/screen.png"
    xcrun simctl spawn "$udid" log show --style compact --last 5m --predicate 'process == "Xbin"' \
      >"$XBIN_CI_OUT/app.log" 2>&1 || true
    echo "screenshot: $XBIN_CI_OUT/screen.png"
    ;;
  e2e)
    local url="" token="" only="" i
    while [ $# -gt 0 ]; do
      case $1 in
      --url) url=$2; shift 2 ;;
      --only) only="$only $2"; shift 2 ;;
      *) say "e2e: unknown argument $1"; return 2 ;;
      esac
    done
    [ -n "$url" ] || { say "e2e: --url is required"; return 2; }
    # The token arrives on stdin (never on a command line).
    IFS= read -r token || true
    [ -n "$token" ] || { say "e2e: no token on stdin"; return 2; }
    # The tunnel is up once the xbind answers through it.
    i=0
    until curl -fsS -o /dev/null --max-time 5 "$url/healthz"; do
      i=$((i + 1))
      [ "$i" -lt 20 ] || { ci_error "$url does not answer from the Mac (is the tunnel up?)"; return 1; }
      sleep 1
    done
    xcodegen_on_path
    dest=$(XBIN_SIM_ENSURE=${XBIN_SIM_ENSURE:-xbin-e2e} "$S/pick-sim.sh")
    XBIN_E2E_URL=$url XBIN_E2E_TOKEN=$token XBIN_E2E_ERASE=${XBIN_E2E_ERASE:-1} XBIN_E2E_ONLY=${only# } \
      "$S/ci-uitests.sh" "$dest"
    ;;
  cleanup) "$S/mac-cleanup.sh" "$@" ;;
  *) say "--on-mac: unknown command ${cmd:-(none)}"; return 2 ;;
  esac
}

if [ "${1:-}" = --on-mac ]; then
  shift
  on_mac "$@"
  exit $?
fi

# ======================================================================
# Here (the Linux box)
# ======================================================================
cmd=${1:-}
[ $# -gt 0 ] && shift
case $cmd in
"" | -h | --help | help) usage; exit 0 ;;
esac
[ -n "${XBIN_MAC:-}" ] || { say "set XBIN_MAC=user@host (the Mac's ssh destination)"; exit 2; }
rbase=${XBIN_MAC_DIR:-xbin-remote}
rtree=$rbase/tree
pull_dir=${XBIN_MAC_PULL:-${TMPDIR:-/tmp}/xbin-mac}
# shellcheck disable=SC2206 # options, split on purpose
ssh_opts=(${XBIN_MAC_SSH_OPTS:-})
# The Mac's own rsync is openrsync; mac-setup.sh installs Homebrew's.
rsync_path='PATH=/opt/homebrew/bin:/usr/local/bin:$PATH rsync'

q() { # the arguments, quoted for the remote shell
  local out="" a
  for a in "$@"; do out="$out $(printf '%q' "$a")"; done
  printf '%s' "${out# }"
}

# remote [ssh options…] -- <on-mac args…> — run this script's --on-mac half
# in the mirror, passing XBIN_SIM/XBIN_SIGNING/XBIN_XCODE through.
remote() {
  local opts=() envs=""
  while [ $# -gt 0 ] && [ "$1" != -- ]; do opts+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  local v
  for v in XBIN_SIM XBIN_SIGNING XBIN_XCODE XBIN_SWIFT_CONDITIONS XBIN_SIM_GUI XBIN_E2E_ERASE; do
    if [ -n "${!v:-}" ]; then envs="$envs $v=$(printf '%q' "${!v}")"; fi
  done
  ssh ${ssh_opts[@]+"${ssh_opts[@]}"} ${opts[@]+"${opts[@]}"} "$XBIN_MAC" \
    "cd $(printf '%q' "$rtree") && env${envs} /bin/bash native/ios/scripts/mac-remote.sh --on-mac $(q "$@")"
}

sync_tree() {
  local list
  list=$(mktemp "${TMPDIR:-/tmp}/mac-remote-files.XXXXXX")
  # Every tracked and untracked-but-not-ignored path, as anchored rsync
  # include rules, each parent directory included first; then exclude the
  # rest. --delete-excluded makes the mirror exact, except what the Mac
  # builds in it (P: protected).
  (cd "$repo" && git ls-files -co --exclude-standard) | awk '
    { gsub(/[][*?\\]/, "\\\\&") }
    {
      n = split($0, p, "/"); d = ""
      for (i = 1; i < n; i++) { d = d p[i] "/"; if (!(d in seen)) { seen[d] = 1; print "+ /" d } }
      print "+ /" $0
    }' >"$list"
  echo "- *" >>"$list"
  ssh -n ${ssh_opts[@]+"${ssh_opts[@]}"} "$XBIN_MAC" "mkdir -p $(printf '%q' "$rtree")"
  rsync -a --delete --delete-excluded -e "ssh ${XBIN_MAC_SSH_OPTS:-}" --rsync-path="$rsync_path" \
    --filter='P .build/' --filter='P .swiftpm/' --filter='P *.xcodeproj/' \
    --filter=". $list" ${XBIN_MAC_RSYNC_OPTS:-} "$repo/" "$XBIN_MAC:$rtree/"
  rm -f "$list"
  say "synced $(cd "$repo" && git ls-files -co --exclude-standard | wc -l | tr -d ' ') files to $XBIN_MAC:$rtree"
}

pull() { # pull <command>
  mkdir -p "$pull_dir/$1"
  rsync -a --delete -e "ssh ${XBIN_MAC_SSH_OPTS:-}" --rsync-path="$rsync_path" \
    "$XBIN_MAC:$rbase/out/$1/" "$pull_dir/$1/" || { say "nothing to pull for $1"; return 0; }
  say "results: $pull_dir/$1"
  (cd "$pull_dir/$1" && find . -maxdepth 3 \( -name '*.png' -o -name '*.log' -o -name '*.xcresult' \) | sed 's|^\./|  |' | sort | head -n 40) >&2
}

status=0
case $cmd in
sync) sync_tree ;;
setup)
  sync_tree
  ssh -t ${ssh_opts[@]+"${ssh_opts[@]}"} "$XBIN_MAC" \
    "cd $(printf '%q' "$rtree") && /bin/bash native/ios/scripts/mac-setup.sh $(q "$@")"
  ;;
toolchain | packages | build | snapshots | uitests | run | cleanup)
  sync_tree
  remote -n -- "$cmd" "$@" || status=$?
  pull "$cmd"
  exit "$status"
  ;;
e2e)
  port=${XBIN_E2E_PORT:-9871} keep=0 only=()
  while [ $# -gt 0 ]; do
    case $1 in
    --port) port=$2; shift 2 ;;
    --keep) keep=1; shift ;;
    --only) only+=(--only "$2"); shift 2 ;;
    *) say "e2e: unknown argument $1"; exit 2 ;;
    esac
  done
  url=${XBIN_E2E_URL:-} token=${XBIN_E2E_TOKEN:-} started=0
  if [ -z "$url" ]; then
    e2e_dir=${XBIN_E2E_DIR:-${TMPDIR:-/tmp}/xbin-e2e}
    XBIN_E2E_DIR=$e2e_dir "$here/e2e-xbind.sh" start --port "$port"
    started=1
    url=$(sed -n 's/^XBIN_E2E_URL=//p' "$e2e_dir/env")
    token=$(sed -n 's/^XBIN_E2E_TOKEN=//p' "$e2e_dir/env")
    if [ "$keep" = 0 ]; then trap 'XBIN_E2E_DIR=$e2e_dir "$here/e2e-xbind.sh" stop' EXIT; fi
  fi
  [ -n "$token" ] || { say "e2e: XBIN_E2E_URL without XBIN_E2E_TOKEN"; exit 2; }
  # A loopback xbind reaches the Mac through a reverse tunnel on the same
  # port (the workspace's origin stays http://127.0.0.1:P on both sides).
  tunnel=()
  case $url in
  http://127.0.0.1:* | http://localhost:*)
    p=${url##*:}
    p=${p%%/*}
    tunnel=(-o ExitOnForwardFailure=yes -R "127.0.0.1:$p:127.0.0.1:$p")
    ;;
  esac
  sync_tree
  printf '%s\n' "$token" | remote ${tunnel[@]+"${tunnel[@]}"} -- e2e --url "$url" ${only[@]+"${only[@]}"} || status=$?
  pull e2e
  if [ "$started" = 1 ] && [ "$keep" = 1 ]; then say "the xbind stays up: $url (e2e-xbind.sh stop)"; fi
  exit "$status"
  ;;
tunnel)
  port=${XBIN_E2E_PORT:-9871}
  [ "${1:-}" = --port ] && port=$2
  say "tunnel: the Mac's 127.0.0.1:$port → this box's 127.0.0.1:$port (Ctrl-C ends it)"
  exec ssh -N -o ExitOnForwardFailure=yes -R "127.0.0.1:$port:127.0.0.1:$port" ${ssh_opts[@]+"${ssh_opts[@]}"} "$XBIN_MAC"
  ;;
shell)
  exec ssh -t ${ssh_opts[@]+"${ssh_opts[@]}"} "$XBIN_MAC" "cd $(printf '%q' "$rtree") && exec \$SHELL -l"
  ;;
pull) pull "${1:-e2e}" ;;
*) say "unknown command $cmd"; usage >&2; exit 2 ;;
esac
