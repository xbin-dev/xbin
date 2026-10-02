#!/usr/bin/env bash
# hack/demo/cam/capture.sh — the 4K master path through a real display
# pipeline: a virtual X screen (Xvfb :99, 3840x2160x24), headful Chromium on
# it (Playwright's, from ~/.cache/ms-playwright) in kiosk mode at 1920×1080
# CSS px × device scale 2, filmed by ffmpeg x11grab at 60 fps into
# h264_nvenc (lossless 4:4:4 by default) — shot.js --capture x11 does the
# browser and ffmpeg; this script owns the display and cleans it up.
#
#   capture.sh <shot> --out DIR [shot.js options…]   film one shot
#   capture.sh --framecheck [DIR] [SECONDS]           film the frame counter, then
#                                                     check.js says what was dropped
#   capture.sh --check                                what this machine has / lacks
#
# Env: CAM_DISPLAY (:99), CAM_SIZE (1920x1080, CSS px), CAM_DPR (2), XVFB
# (an Xvfb binary; default: the one on PATH), PLAYWRIGHT_DIR (as for the UI
# harness), CAM_KEEP_X=1 (leave Xvfb running, e.g. to look at it with
# `DISPLAY=:99 xwd -root`).
#
# Without Xvfb (the package is xorg-server-xvfb on Arch, xvfb on Debian):
# `node shot.js <shot> --mode video --capture beginframe` films frame-exact
# 4K masters headless, no X at all (README: "Which capture").
set -euo pipefail
H="$(cd "$(dirname "$0")" && pwd)"
DISP=${CAM_DISPLAY:-:99}
SIZE=${CAM_SIZE:-1920x1080}
DPR=${CAM_DPR:-2}
W=${SIZE%x*}
HT=${SIZE#*x}
SW=$((W * DPR))
SH=$((HT * DPR))
N=${DISP#:}

say() { printf 'capture: %s\n' "$*" >&2; }
xvfb_bin() { if [[ -n "${XVFB:-}" ]]; then printf '%s\n' "$XVFB"; else command -v Xvfb || true; fi; }

check() {
  local ok=0 x
  x=$(xvfb_bin)
  if [[ -n "$x" && -x "$x" ]]; then say "Xvfb: $x"; else say "Xvfb: MISSING — sudo pacman -S xorg-server-xvfb (Debian/Ubuntu: apt install xvfb), or XVFB=/path/to/Xvfb"; ok=1; fi
  if ! command -v ffmpeg >/dev/null; then say "ffmpeg: MISSING"; ok=1
  else
    if ffmpeg -hide_banner -devices 2>/dev/null | grep -q x11grab; then say "ffmpeg: x11grab yes"; else say "ffmpeg: no x11grab device"; ok=1; fi
    if ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=black:s=256x144:d=0.1 -c:v h264_nvenc -f null - 2>/dev/null; then say "ffmpeg: h264_nvenc works"
    else say "ffmpeg: h264_nvenc unusable here — shot.js falls back to libx264 (slower; may not hold 60 fps at 4K)"; fi
  fi
  local chrome
  if chrome=$(cd "$H" && node -e 'const p=require(process.env.PLAYWRIGHT_DIR?require("path").join(process.env.PLAYWRIGHT_DIR,"node_modules","playwright"):"playwright");console.log(p.chromium.executablePath())' 2>/dev/null); then
    if [[ -x "$chrome" ]]; then say "chromium: $chrome"; else say "chromium: $chrome is not installed (npx playwright install chromium)"; ok=1; fi
  else say "playwright: not found (npm i -g playwright, or PLAYWRIGHT_DIR)"; ok=1; fi
  return $ok
}

XPID=""
cleanup() {
  if [[ -n "$XPID" && -z "${CAM_KEEP_X:-}" ]]; then
    kill "$XPID" 2>/dev/null || true
    for _ in $(seq 1 40); do kill -0 "$XPID" 2>/dev/null || break; sleep 0.05; done
    kill -9 "$XPID" 2>/dev/null || true
  fi
}

start_x() {
  local x
  x=$(xvfb_bin)
  if [[ -z "$x" || ! -x "$x" ]]; then
    say "Xvfb is not installed, so there is no X display to film."
    say "  install it:  sudo pacman -S xorg-server-xvfb   (Debian/Ubuntu: sudo apt install xvfb)"
    say "  or point XVFB at a binary.  Meanwhile, frame-exact 4K masters without X:"
    say "    node $H/shot.js <shot> --out DIR --mode video --capture beginframe"
    exit 3
  fi
  if [[ -e "/tmp/.X${N}-lock" || -S "/tmp/.X11-unix/X${N}" ]]; then
    local owner
    owner=$(tr -d ' ' < "/tmp/.X${N}-lock" 2>/dev/null || true)
    if [[ -n "$owner" ]] && kill -0 "$owner" 2>/dev/null; then
      say "display $DISP is taken (pid $owner) — CAM_DISPLAY=:N picks another"; exit 1
    fi
    say "display $DISP: removing a stale lock"
    rm -f "/tmp/.X${N}-lock" "/tmp/.X11-unix/X${N}" 2>/dev/null || true
  fi
  # -nocursor: no X pointer in the picture (the camera draws its own);
  # -noreset: the server survives the last client closing
  "$x" "$DISP" -screen 0 "${SW}x${SH}x24" -nolisten tcp -nocursor -noreset -dpi 96 >"${CAM_XLOG:-/dev/null}" 2>&1 &
  XPID=$!
  trap cleanup EXIT
  for _ in $(seq 1 100); do
    [[ -S "/tmp/.X11-unix/X${N}" ]] && break
    kill -0 "$XPID" 2>/dev/null || { say "Xvfb exited at start (CAM_XLOG=file to see why)"; exit 1; }
    sleep 0.05
  done
  [[ -S "/tmp/.X11-unix/X${N}" ]] || { say "Xvfb never opened $DISP"; exit 1; }
  say "Xvfb $DISP ${SW}x${SH}x24 (pid $XPID)"
}

case "${1:-}" in
  ""|-h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  --check) check; exit $? ;;
  --framecheck)
    OUT=${2:-.}
    SECS=${3:-10}
    start_x
    node "$H/shot.js" framecheck --out "$OUT" --take x11-framecheck --mode video --capture x11 --display "$DISP" \
      --size "$SIZE" --dpr "$DPR" --set "seconds=$SECS"
    node "$H/framecheck/check.js" "$OUT/x11-framecheck.mp4" --json "$OUT/x11-framecheck.check.json"
    ;;
  *)
    start_x
    node "$H/shot.js" "$@" --mode video --capture x11 --display "$DISP" --size "$SIZE" --dpr "$DPR"
    ;;
esac
