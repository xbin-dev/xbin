#!/usr/bin/env bash
# native/ios/scripts/footage.sh — footage of the app for the promo video and
# the website: a simulator's screen recorded by `simctl io recordVideo` while
# a footage UI test walks the app with its animations on (native/AGENTS.md
# → "Footage"). From this box, over ssh, like mac-remote.sh:
#
#   XBIN_MAC=dev@host footage.sh [record] [--port P] [--keep] [--out DIR]
#                                [--name N] [--appearance light|dark]
#                                [--no-dress]
#   XBIN_MAC=dev@host footage.sh clean     the Mac's footage tree and
#                                          simulator, gone
#
# record (the default):
#   1. the xbind the app shows: the demo film set (hack/demo, Larkspan) when
#      XBIN_E2E_SET names its workspace directory — `hack/demo/up.sh
#      --isolate` made it: its address (WS.snap.meta/listen), the phone
#      persona of hack/demo/company.json (Maya Okafor, maya) and the set's
#      random password (WS.password); it must run sandboxed (--isolate:
#      its terminals are no shells on this box), or footage.sh refuses to
#      tunnel it (FOOTAGE_ALLOW_UNSANDBOXED=1 overrides). Or the xbind
#      XBIN_E2E_URL, XBIN_E2E_USER and XBIN_E2E_PASSWORD name, taken as it is
#      (a loopback one through the tunnel, like mac-remote.sh e2e). Else
#      e2e-xbind.sh start on 127.0.0.1:P (9871) in XBIN_E2E_DIR (default
#      ${TMPDIR:-/tmp}/xbin-footage), its terminals bash, dressed as the
#      film set's company (below; --no-dress leaves it as the UI tests have
#      it)
#   2. mac-remote.sh sync, into XBIN_MAC_DIR (default xbin-footage: a mirror,
#      DerivedData and results of its own, apart from the dev loop's)
#   3. on the Mac, through an ssh reverse tunnel (the account's name and
#      password on ssh's stdin): build the UI tests (signed to run locally);
#      the simulator XBIN_SIM_ENSURE (default xbin-e2e-footage: made if
#      missing, as pick-sim.sh makes one — the newest iPhone on the newest
#      iOS — or of FOOTAGE_DEVICE's type), erased unless XBIN_E2E_ERASE=0,
#      in US English (FOOTAGE_LOCALE: the Mac's own region gave it a 24-hour
#      "09:41" and a second keyboard), its status bar 9:41, full bars,
#      battery charged, the appearance; then XbinUITests/XbinFootageTests
#      (UITests/XbinFootageTests.swift — not in git: copy it in first). The
#      test signs in off camera, relaunches the app without the UI tests'
#      flags (animations on) and writes footage-ready; recordVideo starts,
#      and once simctl says "Recording started" footage-rolling tells the
#      test to walk Home → a screen → the native tile → a terminal → an
#      agent, a beat on each; at footage-cut a SIGINT stops the recording
#      and simctl finalizes the file. The app quits and the simulator shuts
#      down after (FOOTAGE_KEEP_SIM=1: it stays up)
#   4. pulls the Mac's results into XBIN_MAC_PULL/footage/raw (default
#      ${TMPDIR:-/tmp}/xbin-mac/footage/raw: each run replaces it) — never
#      after a run that failed on the Mac — and puts the named take in --out
#      (default XBIN_MAC_PULL/footage/takes, beside raw/, so a take outlives
#      the next run): <name>.mp4 (simctl's own: the display's pixels, a
#      frame only when the screen changes), <name>-60fps.mp4 (an editing
#      copy at a constant rate, FOOTAGE_CFR; with ffmpeg here),
#      <name>.cues.tsv (each step's second in the clip) and <name>-stills/
#      (a full-resolution PNG at each beat)
#
# Dressing (the xbind footage.sh started, never one it was given): the
# workspace's branding (FOOTAGE_TITLE, default the film set's company,
# hack/demo/company.json, and a plain mark), the account's display name
# (FOOTAGE_USER_NAME, default its phone persona's), the
# UI tests' fixture tiles gone (apps/wide, apps/phone, apps/parted), the
# calendar example as apps/calendar with a day of meetings (FOOTAGE_EVENTS,
# "HH:MM|title" lines; FOOTAGE_CALENDAR=0: none), a bash prompt that names
# the person and the tile (FOOTAGE_PROMPT_USER, "maya"), the counter at
# FOOTAGE_COUNT (26); the test seeds the account's layout with one screen
# (FOOTAGE_SCREEN, "Operations").
#
#   XBIN_MAC, XBIN_MAC_SSH_OPTS, XBIN_MAC_DIR, XBIN_MAC_PULL  as mac-remote.sh
#   XBIN_SIM_ENSURE, XBIN_E2E_ERASE, XBIN_XCODE   passed through
#   XBIN_E2E_XBIND_ARGS  extra xbind flags (e2e-xbind.sh), e.g. "--isolate
#                      --rootfs …" where this box can sandbox
#   FOOTAGE_SCREEN, _TILE, _TAPS, _COMMAND, _AGENT, _PROMPT, _BEAT
#                      the walk (UITests/XbinFootageTests.swift says what)
#   FOOTAGE_DEVICE     the simulator's device type when it is made, e.g.
#                      "iPhone 18 Pro" (default: pick-sim.sh's rule)
#   FOOTAGE_CODEC      h264 (default) or hevc
#   FOOTAGE_MASK       recordVideo's --mask: ignored (default: the whole
#                      rectangular framebuffer, for a device frame in the
#                      edit), black
#
# The agent: FOOTAGE_AGENT names a real provider (claude, codex…) whose ACP
# adapter the xbind can run (claude-agent-acp…: on its PATH, or in the
# rootfs under --isolate) and whose key reaches it — an unsandboxed xbind
# passes its environment on (ANTHROPIC_API_KEY, OPENAI_API_KEY…, exported
# where footage.sh runs). Nothing here stores a key. The walk's default, the
# scripted test agent ("fake": its launcher box reads "Fake agent (tests)",
# its answers "fakeacp … todo done"), films as a test run: footage.sh
# refuses it unless FOOTAGE_ALLOW_FAKE=1 (a test take, never footage).
#
# The same file runs on the Mac as `footage.sh --on-mac record --url U`.
# Bash 3.2 there.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)

say() { echo "footage: $*" >&2; }
usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; }

# ======================================================================
# On the Mac
# ======================================================================
now() { python3 -c 'import time; print("%.3f" % time.time())'; }

on_mac_record() {
  local url="" user="" password="" i
  while [ $# -gt 0 ]; do
    case $1 in
    --url) url=$2; shift 2 ;;
    *) say "record: unknown argument $1"; return 2 ;;
    esac
  done
  [ -n "$url" ] || { say "record: --url is required"; return 2; }
  if [ "$(id -un)" = "${XBIN_CI_USER:-ci}" ] || [ -f "${XBIN_RUNNER_DIR:-$HOME/actions-runner}/.runner" ]; then
    say "not as $(id -un), the Actions runner's user (native/AGENTS.md → The users)"
    return 2
  fi
  IFS= read -r user || true
  IFS= read -r password || true
  [ -n "$user" ] && [ -n "$password" ] || { say "no account (user, password) on stdin"; return 2; }

  local prefix=${XBIN_MAC_PATH_PREFIX-/opt/homebrew/bin:/usr/local/bin}
  if [ -n "$prefix" ]; then PATH="$prefix:$PATH"; fi
  export PATH
  local base
  base=${XBIN_MAC_BASE:-$(dirname "$repo")}
  export XBIN_CI_OUT="$base/out/footage" XBIN_CI_DERIVED="$base/derived" XBIN_CI_TOOLS="$base/tools"
  rm -rf "$XBIN_CI_OUT"
  mkdir -p "$XBIN_CI_OUT" "$XBIN_CI_DERIVED"
  touch "$XBIN_CI_DERIVED"
  # shellcheck source=SCRIPTDIR/ci-lib.sh
  . "$here/ci-lib.sh"
  local S=$here out=$XBIN_CI_OUT

  [ -f "$repo/native/ios/UITests/XbinFootageTests.swift" ] ||
    { ci_error "native/ios/UITests/XbinFootageTests.swift is missing (it is not in git)"; return 2; }
  i=0
  until curl -fsS -o /dev/null --max-time 5 "$url/healthz"; do
    i=$((i + 1))
    [ "$i" -lt 20 ] || { ci_error "$url does not answer from the Mac (is the tunnel up?)"; return 1; }
    sleep 1
  done

  # xcodegen, as mac-remote.sh finds it.
  local p
  p=$(mktemp "${TMPDIR:-/tmp}/xcodegen-path.XXXXXX")
  GITHUB_PATH=$p "$S/ci-xcodegen.sh" >/dev/null
  if [ -s "$p" ]; then PATH="$(cat "$p"):$PATH"; fi
  rm -f "$p"

  # The simulator: our own, made of FOOTAGE_DEVICE's type when asked.
  local name=${XBIN_SIM_ENSURE:-xbin-e2e-footage} dest udid
  if [ -n "${FOOTAGE_DEVICE:-}" ] && ! xcrun simctl list devices available | grep -q "^ *$name ("; then
    local runtime
    runtime=$(xcrun simctl list runtimes available | sed -n 's/^iOS .* - \(com\.apple\.CoreSimulator\.SimRuntime\.iOS-[0-9-]*\)$/\1/p' | tail -n 1)
    say "making $name: $FOOTAGE_DEVICE on $runtime"
    xcrun simctl create "$name" "$FOOTAGE_DEVICE" "$runtime" >/dev/null
  fi
  dest=$(XBIN_SIM_ENSURE=$name "$S/pick-sim.sh")
  udid=$(ci_udid "$dest")

  # Build the UI tests, signed to run locally (the app's Keychain).
  XBIN_E2E_URL="" XBIN_SIGNING=adhoc "$S/ci-uitests.sh" "$dest"
  local proj
  proj=$(cd "$repo/native/ios" && ls -d ./*.xcodeproj | head -n 1)
  proj=${proj#./}

  if [ "${XBIN_E2E_ERASE:-1}" = 1 ]; then
    ci_group "erase simulator $udid"
    xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
    xcrun simctl erase "$udid"
    ci_endgroup
  fi
  ci_group "boot simulator $udid"
  xcrun simctl boot "$udid" >/dev/null 2>&1 || true
  ci_timeout 300 xcrun simctl bootstatus "$udid" -b || ci_warn "simctl bootstatus $udid failed or took over 5 min"
  # A simulator takes the Mac's region (here en_PL: a 24-hour 09:41, a
  # Polish keyboard): US English, one keyboard, then boot again for it.
  if [ "$(xcrun simctl spawn "$udid" defaults read -g AppleLocale 2>/dev/null)" != "${FOOTAGE_LOCALE:-en_US}" ]; then
    xcrun simctl spawn "$udid" defaults write -g AppleLocale -string "${FOOTAGE_LOCALE:-en_US}"
    xcrun simctl spawn "$udid" defaults write -g AppleLanguages -array "${FOOTAGE_LANGUAGE:-en-US}"
    xcrun simctl spawn "$udid" defaults write -g AppleKeyboards -array "${FOOTAGE_LOCALE:-en_US}@sw=QWERTY;hw=Automatic" 'emoji@sw=Emoji'
    xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
    xcrun simctl boot "$udid" >/dev/null 2>&1 || true
    ci_timeout 300 xcrun simctl bootstatus "$udid" -b >/dev/null || ci_warn "simctl bootstatus $udid (again) failed"
  fi
  ci_endgroup
  xcrun simctl status_bar "$udid" override --time 9:41 --batteryState charged --batteryLevel 100 \
    --cellularBars 4 --wifiBars 3 || ci_warn "simctl status_bar override failed"
  if [ -n "${FOOTAGE_APPEARANCE:-}" ]; then
    xcrun simctl ui "$udid" appearance "$FOOTAGE_APPEARANCE" || ci_warn "simctl ui appearance $FOOTAGE_APPEARANCE failed"
  fi
  xcrun simctl io "$udid" enumerate >"$out/display.txt" 2>&1 || true

  # The test, in the background: it signs in, then waits for the camera.
  local shots=$out/e2e
  mkdir -p "$shots"
  local v envs=()
  for v in FOOTAGE_SCREEN FOOTAGE_TILE FOOTAGE_TAPS FOOTAGE_COMMAND FOOTAGE_AGENT FOOTAGE_PROMPT FOOTAGE_BEAT FOOTAGE_SEED; do
    if [ -n "${!v:-}" ]; then envs+=("TEST_RUNNER_$v=${!v}"); fi
  done
  say "the walk: XbinUITests/XbinFootageTests on $dest"
  (
    cd "$repo/native/ios"
    exec env ${envs[@]+"${envs[@]}"} TEST_RUNNER_FOOTAGE=1 TEST_RUNNER_E2E_DIR="$shots" \
      TEST_RUNNER_XBIN_E2E_URL="$url" TEST_RUNNER_XBIN_E2E_USER="$user" TEST_RUNNER_XBIN_E2E_PASSWORD="$password" \
      NSUnbufferedIO=YES xcodebuild test-without-building -project "$proj" -scheme XbinUITests -destination "$dest" \
      -derivedDataPath "$XBIN_CI_DERIVED/app" -clonedSourcePackagesDirPath "$XBIN_CI_SPM" \
      -skipMacroValidation -skipPackagePluginValidation -resultBundlePath "$out/uitests.xcresult" \
      -collect-test-diagnostics never -only-testing:XbinUITests/XbinFootageTests
  ) >"$out/uitests.log" 2>&1 &
  # Not local: the EXIT trap reads them after this function returned.
  test_pid=$! rec_pid="" sim_udid=$udid
  local status=0 t0="" waited=0
  trap 'kill "$test_pid" 2>/dev/null; [ -z "$rec_pid" ] || kill -INT "$rec_pid" 2>/dev/null
        xcrun simctl terminate "$sim_udid" dev.xbin.app >/dev/null 2>&1
        [ "${FOOTAGE_KEEP_SIM:-0}" = 1 ] || xcrun simctl shutdown "$sim_udid" >/dev/null 2>&1; true' EXIT

  # Ready → roll: recordVideo, then footage-rolling once its first frame is in.
  while [ ! -f "$shots/footage-ready" ] && kill -0 "$test_pid" 2>/dev/null; do
    sleep 0.5
    waited=$((waited + 1))
    [ "$waited" -lt 1200 ] || { ci_error "no footage-ready in 10 min"; return 1; }
  done
  if [ ! -f "$shots/footage-ready" ]; then
    wait "$test_pid" || status=$?
    xcrun simctl terminate "$udid" dev.xbin.app >/dev/null 2>&1 || true
    ci_error "the test ended before it was ready (exit $status): $out/uitests.log"
    grep -E 'error|failed|Skipped|skipped' "$out/uitests.log" | tail -n 20 >&2 || true
    return 1
  fi
  local clip=$out/footage.mp4 simctl mask=()
  simctl=$(xcrun -f simctl)
  [ -n "${FOOTAGE_MASK:-}" ] && mask=(--mask="$FOOTAGE_MASK")
  say "rolling: recordVideo --codec=${FOOTAGE_CODEC:-h264} $udid"
  # A background job of a script ignores SIGINT; the recording must take it.
  python3 -c 'import os, signal, sys; signal.signal(signal.SIGINT, signal.SIG_DFL); os.execv(sys.argv[1], sys.argv[1:])' \
    "$simctl" io "$udid" recordVideo --codec="${FOOTAGE_CODEC:-h264}" ${mask[@]+"${mask[@]}"} --force "$clip" \
    >"$out/record.log" 2>&1 &
  rec_pid=$!
  waited=0
  until grep -q 'Recording started' "$out/record.log" 2>/dev/null; do
    kill -0 "$rec_pid" 2>/dev/null || { ci_error "recordVideo exited: $(cat "$out/record.log")"; return 1; }
    sleep 0.1
    waited=$((waited + 1))
    [ "$waited" -lt 300 ] || { ci_warn "no 'Recording started' in 30 s — rolling anyway"; break; }
  done
  t0=$(now)
  echo "$t0" >"$out/record.start"
  touch "$shots/footage-rolling"

  # Cut: the test says so (or ends); a moment's tail, then SIGINT.
  while [ ! -f "$shots/footage-cut" ] && kill -0 "$test_pid" 2>/dev/null; do sleep 0.2; done
  sleep 0.5
  kill -INT "$rec_pid" 2>/dev/null || true
  waited=0
  while kill -0 "$rec_pid" 2>/dev/null; do
    sleep 0.2
    waited=$((waited + 1))
    if [ "$waited" -ge 300 ]; then
      ci_warn "recordVideo still finalizing after 60 s — killing it"
      kill -9 "$rec_pid" 2>/dev/null || true
      break
    fi
  done
  wait "$rec_pid" 2>/dev/null || true
  rec_pid=""
  say "cut: $(tr '\n' ' ' <"$out/record.log")"
  wait "$test_pid" || status=$?
  trap - EXIT
  # The app's sockets ride the ssh tunnel (it stays open while they do), and
  # a booted simulator holds memory the Mac's other users want.
  xcrun simctl terminate "$udid" dev.xbin.app >/dev/null 2>&1 || true
  [ "${FOOTAGE_KEEP_SIM:-0}" = 1 ] || xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true

  # The cue sheet: each mark's second in the clip.
  if [ -f "$shots/footage-marks.tsv" ]; then
    python3 - "$t0" "$shots/footage-marks.tsv" >"$out/footage.cues.tsv" <<'PY'
import sys
t0 = float(sys.argv[1])
print("second\tstep")
for line in open(sys.argv[2]):
    t, _, label = line.rstrip("\n").partition("\t")
    print("%.2f\t%s" % (float(t) - t0, label))
PY
  fi
  [ -s "$clip" ] || { ci_error "no clip at $clip"; return 1; }
  ls -l "$clip" >&2
  [ "$status" -eq 0 ] || ci_warn "the walk's test failed (exit $status): $out/uitests.log, uitests.xcresult — the clip may still be good"
  return 0
}

if [ "${1:-}" = --on-mac ]; then
  shift
  cmd=${1:-}
  [ $# -gt 0 ] && shift
  case $cmd in
  record) on_mac_record "$@" ;;
  *) say "--on-mac: unknown command ${cmd:-(none)}"; exit 2 ;;
  esac
  exit $?
fi

# ======================================================================
# Here (the Linux box)
# ======================================================================
cmd=record
case ${1:-} in
-h | --help | help) usage; exit 0 ;;
record | clean) cmd=$1; shift ;;
esac
[ -n "${XBIN_MAC:-}" ] || { say "set XBIN_MAC=user@host (the Mac's ssh destination; the dev user, never the runner's)"; exit 2; }
case $XBIN_MAC in "${XBIN_CI_USER:-ci}"@*)
  say "not as the Actions runner's user: XBIN_MAC=${XBIN_DEV_USER:-dev}@… (native/AGENTS.md → The users)"
  exit 2
  ;;
esac
export XBIN_MAC_DIR=${XBIN_MAC_DIR:-xbin-footage}
rbase=$XBIN_MAC_DIR
rtree=$rbase/tree
# A footage tree is one directory in the Mac user's home, named xbin-footage…
# — never a path (xbin-footage/.. would make clean remove the home itself).
if ! printf '%s' "$rbase" | grep -Eq '^xbin-footage[A-Za-z0-9._-]*$' || [[ "$rbase" == *..* ]]; then
  say "XBIN_MAC_DIR=$rbase is not a footage tree (one directory: xbin-footage, letters, digits, . _ -; no ..)"
  exit 2
fi
# shellcheck disable=SC2206 # options, split on purpose
ssh_opts=(${XBIN_MAC_SSH_OPTS:-})
rsync_path='PATH=/opt/homebrew/bin:/usr/local/bin:$PATH rsync'
company=$repo/hack/demo/company.json
# cfield EXPR: a value of the film set's company.json (a python expression on c)
cfield() { python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$company" "$1"; }

if [ "$cmd" = clean ]; then
  # The simulator by its UDID (names needn't be unique), then the tree.
  ssh ${ssh_opts[@]+"${ssh_opts[@]}"} "$XBIN_MAC" /bin/bash -s -- "${XBIN_SIM_ENSURE:-xbin-e2e-footage}" "$rbase" <<'EOF'
set -u
name=$1 base=$HOME/$2
for udid in $(xcrun simctl list devices -j | python3 -c 'import json,sys; n=sys.argv[1]; print(" ".join(d["udid"] for ds in json.load(sys.stdin)["devices"].values() for d in ds if d["name"] == n))' "$name"); do
  xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true
  xcrun simctl delete "$udid" && echo "footage: deleted simulator $name ($udid)"
done
if [ -d "$base" ]; then
  du -sh "$base" 2>/dev/null | sed 's/^/footage: removing /'
  rm -rf "$base"
fi
df -h "$HOME" | tail -n 1
EOF
  exit $?
fi

port=${XBIN_E2E_PORT:-9871} keep=0 out="" name="" dress=1 appearance=""
while [ $# -gt 0 ]; do
  case $1 in
  --port) port=$2; shift 2 ;;
  --keep) keep=1; shift ;;
  --out) out=$2; shift 2 ;;
  --name) name=$2; shift 2 ;;
  --appearance) appearance=$2; shift 2 ;;
  --no-dress) dress=0; shift ;;
  *) say "unknown argument $1"; usage >&2; exit 2 ;;
  esac
done
case $appearance in "" | light | dark) ;; *) say "--appearance light or dark"; exit 2 ;; esac
[ -f "$repo/native/ios/UITests/XbinFootageTests.swift" ] ||
  { say "native/ios/UITests/XbinFootageTests.swift is missing — the walk lives there (not in git); copy it in first"; exit 2; }
# The walk's own default agent is the scripted test one: footage of it reads
# as a test run ("Fake agent (tests)", "fakeacp … todo done")
if [ "${FOOTAGE_AGENT:-fake}" = fake ] && [ "${FOOTAGE_ALLOW_FAKE:-0}" != 1 ]; then
  say "FOOTAGE_AGENT is the scripted test agent (fake): name a real provider (FOOTAGE_AGENT=claude, its key in the xbind's reach), or FOOTAGE_ALLOW_FAKE=1 for a test take — never footage"
  exit 2
fi
footage_dir=${XBIN_MAC_PULL:-${TMPDIR:-/tmp}/xbin-mac}/footage
# raw/: the Mac's out/ as the last run left it (each pull replaces it);
# the named takes go elsewhere (takes/, or --out), so one outlives the next
pull_dir=$footage_dir/raw
name=${name:-ios-walk${appearance:+-$appearance}}
out=${out:-$footage_dir/takes}
case $(cd "$out" 2>/dev/null && pwd || echo "$out") in
"$(cd "$pull_dir" 2>/dev/null && pwd || echo "$pull_dir")"*) say "--out $out is inside the pull directory $pull_dir, which each run replaces: pick another"; exit 2 ;;
esac

# dress <ws> <url> <user> — the workspace as a company's (see the header).
dress() {
  local ws=$1 url=$2 user=$3 token t
  token=$(tr -d '[:space:]' <"$ws/.xbin/token")
  api() { # api <method> <path> [json]
    if [ $# -gt 2 ]; then
      curl -fsS -o /dev/null -X "$1" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$3" "$url$2"
    else
      curl -fsS -o /dev/null -X "$1" -H "Authorization: Bearer $token" "$url$2"
    fi
  }
  # the film set's company and its phone persona (hack/demo/company.json)
  local title=${FOOTAGE_TITLE:-$(cfield 'c["company"]["name"]')}
  local person=${FOOTAGE_USER_NAME:-$(cfield 'next(p["name"] for p in c["people"] if p["id"] == c["personas"]["phone"])')}
  # The company's mark (hack/demo/data/brand-icon.svg). As a PNG when this
  # box can render one: the app draws an SVG through a web view, a moment
  # late (the clip would open on the monogram it shows meanwhile).
  local svg
  svg=$(cat "$repo/hack/demo/$(cfield 'c["company"]["icon"]')")
  local icon png
  icon="data:image/svg+xml;base64,$(printf '%s' "$svg" | base64 -w0)"
  if command -v rsvg-convert >/dev/null 2>&1 && png=$(printf '%s' "$svg" | rsvg-convert -w 256 -h 256 -f png | base64 -w0) && [ -n "$png" ]; then
    icon="data:image/png;base64,$png"
  fi
  api PUT /api/xbin/branding "$(python3 -c 'import json,sys; print(json.dumps({"title": sys.argv[1], "icon": sys.argv[2]}))' "$title" "$icon")" ||
    say "warning: the branding did not take"
  api PATCH "/api/xbin/users/$user" "$(python3 -c 'import json,sys; print(json.dumps({"name": sys.argv[1]}))' "$person")" ||
    say "warning: the account's name did not take"
  # The UI tests' fixtures are no company's tiles.
  for t in wide phone parted; do
    case $ws/apps/$t in "$ws"/apps/?*) rm -rf "${ws:?}/apps/$t" ;; esac
  done
  # The terminal: bash (footage.sh starts the xbind with SHELL=/bin/bash),
  # a prompt naming the person and the tile, not this box.
  local home=$ws/homes/$user who=${FOOTAGE_PROMPT_USER:-maya}
  mkdir -p "$home"
  # The system bashrc's PROMPT_COMMAND titles the terminal user@host, and
  # the app shows that title in its bar: the tile's path instead.
  cat >"$home/.bashrc" <<EOF
unset PROMPT_COMMAND
PS1='\[\e]0;\${XBIN_COMPONENT:-~}\a\]\[\e[1;32m\]$who\[\e[0m\]:\[\e[1;34m\]\${XBIN_COMPONENT:-~}\[\e[0m\]\\$ '
alias ls='ls --color=auto'
EOF
  # The counter as a team's counter, not a fresh test's 0.
  local n=${FOOTAGE_COUNT:-26} i=0 have
  have=$(curl -fsS -H "Authorization: Bearer $token" "$url/api/apps/counter/count" | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])' 2>/dev/null || echo "$n")
  while [ "$have" -lt "$n" ] && [ "$i" -lt 500 ]; do
    api POST /api/apps/counter/count || break
    have=$((have + 1))
    i=$((i + 1))
  done
  # A second app on the screen: the calendar example, today's meetings in.
  local events=0
  if [ "${FOOTAGE_CALENDAR:-1}" = 1 ] && [ ! -e "$ws/apps/calendar" ]; then
    cp -r "$repo/examples/calendar" "$ws/apps/calendar"
    local day ev t i=0
    day=$(date +%Y-%m-%d)
    # Its Go backend builds on the first call (a cold build: a minute or two).
    until curl -fs -o /dev/null -H "Authorization: Bearer $token" "$url/api/apps/calendar/events?day=$day"; do
      i=$((i + 1))
      [ "$i" -lt 240 ] || { say "warning: the calendar's backend did not come up"; break; }
      sleep 1
    done
    while IFS='|' read -r t ev; do
      [ -n "$t" ] || continue
      api POST /api/apps/calendar/events "$(python3 -c 'import json,sys; print(json.dumps({"day": sys.argv[1], "time": sys.argv[2], "title": sys.argv[3]}))' "$day" "$t" "$ev")" &&
        events=$((events + 1))
    done <<EOF
${FOOTAGE_EVENTS:-09:30|Standup
11:00|Design review: onboarding
13:30|Lunch with the Harbor team
16:00|Release 2.4 go/no-go}
EOF
  fi
  say "dressed: \"$title\", $person, the counter at $have, $events calendar events"
}

url=${XBIN_E2E_URL:-} user=${XBIN_E2E_USER:-} password=${XBIN_E2E_PASSWORD:-} started=0 seed=0
# The film set (hack/demo/up.sh --isolate): its address, its phone persona,
# its random password — and only if it runs sandboxed: a tunnel hands its
# people's terminals to the Mac
if [ -n "${XBIN_E2E_SET:-}" ]; then
  set_ws=$(cd "$XBIN_E2E_SET" && pwd)
  [ -n "$url" ] || url="http://$(cat "$set_ws.snap.meta/listen")"
  [ -n "$user" ] || user=$(cfield 'c["personas"]["phone"]')
  [ -n "$password" ] || password=$(head -1 "$set_ws.password")
  isolated=$(curl -fsS -H "Authorization: Bearer $(tr -d '[:space:]' <"$set_ws/.xbin/token")" "$url/api/xbin/runtime" |
    python3 -c 'import json,sys; print("yes" if json.load(sys.stdin).get("host", {}).get("isolate") else "no")' 2>/dev/null || echo unknown)
  if [ "$isolated" != yes ] && [ "${FOOTAGE_ALLOW_UNSANDBOXED:-0}" != 1 ]; then
    say "the film set at $url runs unsandboxed (isolate: $isolated): its terminals are shells on this box — footage.sh won't tunnel it (hack/demo/up.sh --isolate; FOOTAGE_ALLOW_UNSANDBOXED=1 overrides)"
    exit 2
  fi
  say "the film set at $url, as $user"
fi
if [ -z "$url" ]; then
  e2e_dir=${XBIN_E2E_DIR:-${TMPDIR:-/tmp}/xbin-footage}
  say "the xbind runs unsandboxed: its terminals are shells as you on this box, open to whoever signs in as its account — only this run's ssh session gets the password"
  SHELL=/bin/bash XBIN_E2E_DIR=$e2e_dir "$here/e2e-xbind.sh" start --port "$port"
  started=1 seed=1
  url=$(sed -n 's/^XBIN_E2E_URL=//p' "$e2e_dir/env")
  user=$(sed -n 's/^XBIN_E2E_USER=//p' "$e2e_dir/env")
  password=$(sed -n 's/^XBIN_E2E_PASSWORD=//p' "$e2e_dir/env")
  if [ "$keep" = 0 ]; then trap 'XBIN_E2E_DIR=$e2e_dir "$here/e2e-xbind.sh" stop' EXIT; fi
  [ "$dress" = 0 ] || dress "$e2e_dir/ws" "$url" "$user"
fi
[ -n "$user" ] && [ -n "$password" ] || { say "XBIN_E2E_URL without XBIN_E2E_USER and XBIN_E2E_PASSWORD"; exit 2; }
tunnel=()
case $url in
http://127.0.0.1:* | http://localhost:*)
  p=${url##*:}
  p=${p%%/*}
  tunnel=(-o ExitOnForwardFailure=yes -R "127.0.0.1:$p:127.0.0.1:$p")
  ;;
esac

"$here/mac-remote.sh" sync
remote_env=""
for v in XBIN_SIM_ENSURE XBIN_E2E_ERASE XBIN_XCODE FOOTAGE_SCREEN FOOTAGE_TILE FOOTAGE_TAPS FOOTAGE_COMMAND FOOTAGE_AGENT \
  FOOTAGE_PROMPT FOOTAGE_BEAT FOOTAGE_DEVICE FOOTAGE_CODEC FOOTAGE_MASK FOOTAGE_LOCALE FOOTAGE_LANGUAGE FOOTAGE_KEEP_SIM; do
  if [ -n "${!v:-}" ]; then remote_env="$remote_env $v=$(printf '%q' "${!v}")"; fi
done
[ -n "$appearance" ] && remote_env="$remote_env FOOTAGE_APPEARANCE=$appearance"
[ "$seed" = 1 ] && remote_env="$remote_env FOOTAGE_SEED=1"
status=0
printf '%s\n%s\n' "$user" "$password" |
  ssh ${ssh_opts[@]+"${ssh_opts[@]}"} ${tunnel[@]+"${tunnel[@]}"} "$XBIN_MAC" \
    "cd $(printf '%q' "$rtree") && env${remote_env} /bin/bash native/ios/scripts/footage.sh --on-mac record --url $(printf '%q' "$url")" ||
  status=$?

# A run that failed on the Mac (its out/ cleared, no clip) pulls nothing:
# raw/ keeps the last good run's results
if [ "$status" -ne 0 ]; then
  say "the run on the Mac failed (exit $status): nothing pulled; $pull_dir is the last good run's"
  exit "$status"
fi
mkdir -p "$pull_dir"
rsync -a --delete -e "ssh ${XBIN_MAC_SSH_OPTS:-}" --rsync-path="$rsync_path" \
  "$XBIN_MAC:$rbase/out/footage/" "$pull_dir/" || say "nothing to pull"
say "results: $pull_dir"
if [ -f "$pull_dir/footage.mp4" ]; then
  mkdir -p "$out"
  cp "$pull_dir/footage.mp4" "$out/$name.mp4"
  if [ -f "$pull_dir/footage.cues.tsv" ]; then cp "$pull_dir/footage.cues.tsv" "$out/$name.cues.tsv"; fi
  if ls "$pull_dir"/e2e/still-*.png >/dev/null 2>&1; then
    rm -rf "${out:?}/$name-stills"
    mkdir -p "$out/$name-stills"
    cp "$pull_dir"/e2e/still-*.png "$out/$name-stills/"
  fi
  say "clip: $out/$name.mp4"
  if command -v ffprobe >/dev/null 2>&1; then
    ffprobe -v error -select_streams v:0 -count_frames \
      -show_entries stream=codec_name,width,height,r_frame_rate,avg_frame_rate,nb_read_frames:format=duration \
      -of default=noprint_wrappers=1 "$out/$name.mp4" >&2 || true
  fi
  # recordVideo writes a frame only when the screen changes (variable frame
  # rate) and with composition offsets that make ffmpeg fall back to the
  # decode times (frames out of place) — -fflags +igndts keeps the real
  # ones. An editing copy at a constant rate (FOOTAGE_CFR=0: none).
  cfr=${FOOTAGE_CFR:-60}
  if [ "$cfr" != 0 ] && command -v ffmpeg >/dev/null 2>&1; then
    if ffmpeg -v error -y -fflags +igndts -i "$out/$name.mp4" -vf "fps=$cfr" -c:v libx264 -preset slow -crf 14 \
      -pix_fmt yuv420p -movflags +faststart "$out/$name-${cfr}fps.mp4"; then
      say "editing copy: $out/$name-${cfr}fps.mp4"
    else
      say "warning: no ${cfr} fps copy (ffmpeg failed)"
    fi
  fi
  if [ -f "$out/$name.cues.tsv" ]; then cat "$out/$name.cues.tsv" >&2; fi
fi
if [ "$started" = 1 ] && [ "$keep" = 1 ]; then say "the xbind stays up: $url (XBIN_E2E_DIR=$e2e_dir e2e-xbind.sh stop)"; fi
exit "$status"
