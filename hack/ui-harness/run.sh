#!/usr/bin/env bash
# UI harness for xbin: fresh workspace → seed orgs/net-sets/users/tiles via the
# API → Playwright screenshots of the org-networking surfaces (D54) into
# $HARNESS_DIR/out, so a change to the admin console, the organisations tile,
# the tile popover or the terminal scope menu can be eyeballed without a
# browser session. Not a test suite (AGENTS.md: the frontend has none) — a
# way to LOOK. Needs node + Playwright with its Chromium: `npm i -g playwright
# && npx playwright install chromium`, or point PLAYWRIGHT_DIR at a checkout
# that has it in node_modules.
#
#   ./run.sh            build, fresh workspace, seed, shoot, stop xbind
#   ./run.sh --keep     same, but leave xbind running on $PORT
#   ./run.sh --restart  rebuild xbind and restart it on the EXISTING workspace
#                       (no reseed), then shoot; xbind stays up
#   ./run.sh --shots [pass…]   only shoot against the running instance —
#                       every pass, or just the named ones (node shots.js --list)
#   ./run.sh --stop     stop xbind
set -euo pipefail
H="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$H/../.." && pwd)"
export PORT=${PORT:-8697}
# Workspace + screenshots live OUTSIDE the repo by default (set HARNESS_DIR
# to move them); both are throwaway.
export HARNESS_DIR=${HARNESS_DIR:-${TMPDIR:-/tmp}/xbin-ui-harness}
export WS="$HARNESS_DIR/ws"
export URL="http://127.0.0.1:$PORT"
export OUT="$HARNESS_DIR/out"
export REPO
mkdir -p "$OUT"
mode="${1:-}"
[[ $# -gt 0 ]] && shift
passes=("$@")   # --shots [pass…]

# The [x] keeps pkill from matching this script's own command line. Also
# stop a harness instance from another HARNESS_DIR still holding the port.
stop() {
  pkill -f "bin/[x]bind --dev --workspace $WS" 2>/dev/null || true
  pkill -f "bin/[x]bind --dev .*--listen 127.0.0.1:$PORT" 2>/dev/null || true
  sleep 0.5
}
# A workspace holds its own copies of the scaffold (xbind init); --dev-overlay
# serves the repo's workspace-template/ over them, so the shell and tiles
# under test are always the source tree (web/ is served from source by --dev).
start() {
  (cd "$REPO" && nohup bin/xbind --dev --dev-overlay "$REPO/workspace-template" --workspace "$WS" --listen "127.0.0.1:$PORT" \
      --external-url "$URL" > "$HARNESS_DIR/xbind.log" 2>&1 < /dev/null &)
  for _ in $(seq 1 60); do curl -sf -o /dev/null "$URL/login" && return 0; sleep 0.25; done
  echo "xbind did not come up; see $H/xbind.log" >&2; exit 1
}
build() { (cd "$REPO" && go build -o bin/xbind ./cmd/xbind); }

case "$mode" in
  --stop) stop; exit 0 ;;
  --shots) ;;
  --restart) stop; build; start ;;
  *)
    stop; build
    rm -rf "$WS"; "$REPO/bin/xbind" init "$WS" >/dev/null
    # auth ON (no --no-auth): --dev seeds admin/admin, so other users can log in.
    start
    export TOKEN; TOKEN=$(cat "$WS/.xbin/token")
    bash "$H/seed.sh" > "$OUT/seed.log" 2>&1 || { echo "seed failed:"; tail -20 "$OUT/seed.log"; exit 1; }
    echo "seeded (log: $OUT/seed.log)" ;;
esac

(cd "$H" && node shots.js "${passes[@]}") > "$OUT/shots.log" 2>&1 || { echo "shots failed:"; tail -30 "$OUT/shots.log"; exit 1; }
tail -40 "$OUT/shots.log"
[[ "$mode" == "" ]] && stop
echo "screenshots: $OUT"
ls "$OUT"
