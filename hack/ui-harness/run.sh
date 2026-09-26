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
#   TILE_ASSETS=tokens|origins ./run.sh …   the same under strict tile asset gating
set -euo pipefail
H="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$H/../.." && pwd)"
export PORT=${PORT:-8697}
# Workspace + screenshots live OUTSIDE the repo by default (set HARNESS_DIR
# to move them); both are throwaway.
export HARNESS_DIR=${HARNESS_DIR:-${TMPDIR:-/tmp}/xbin-ui-harness}
export WS="$HARNESS_DIR/ws"
# TILE_ASSETS selects strict tile asset gating (docs/auth.md §Tile asset
# gating): legacy (default), tokens, or origins — origins serves the shell at
# xbin.localhost and each tile at t-<id>.xbin.localhost (browsers resolve
# *.localhost to loopback; shots.js pins it for Chromium).
export TILE_ASSETS=${TILE_ASSETS:-legacy}
if [[ "$TILE_ASSETS" == origins ]]; then
  export URL="http://xbin.localhost:$PORT"
  asset_flags=(--tile-assets origins --tiles-domain xbin.localhost)
else
  export URL="http://127.0.0.1:$PORT"
  asset_flags=(--tile-assets "$TILE_ASSETS")
fi
export OUT="$HARNESS_DIR/out"
export REPO
# the scripted OpenAI-compatible upstream the agentTemplate pass talks to
# through llm-gw (hack/fakeopenai)
export FAKEOPENAI_ADDR=${FAKEOPENAI_ADDR:-127.0.0.1:$((PORT + 10280))}
# the ingress listener the channels pass's webhooks arrive on
export INGRESS_ADDR=${INGRESS_ADDR:-127.0.0.1:$((PORT + 1))}
mkdir -p "$OUT"
mode="${1:-}"
[[ $# -gt 0 ]] && shift
passes=("$@")   # --shots [pass…]

# The [x] keeps pkill from matching this script's own command line. Also
# stop a harness instance from another HARNESS_DIR still holding the port.
stop() {
  pkill -f "bin/[f]akeopenai -addr $FAKEOPENAI_ADDR" 2>/dev/null || true
  pkill -f "bin/[x]bind --dev --workspace $WS" 2>/dev/null || true
  pkill -f "bin/[x]bind --dev .*--listen 127.0.0.1:$PORT" 2>/dev/null || true
  # xbind unmounts encrypted resources (gocryptfs) on its way out; give it
  # the time, then lazily drop whatever a killed one left behind — a stale
  # mount makes the fresh mode's rm -rf fail.
  for _ in $(seq 1 40); do pgrep -f "bin/[x]bind --dev .*127.0.0.1:$PORT" >/dev/null || break; sleep 0.25; done
  { grep -F " $WS/" /proc/self/mounts 2>/dev/null || true; } | cut -d' ' -f2 | while read -r m; do
    fusermount3 -uz "$m" 2>/dev/null || fusermount -uz "$m" 2>/dev/null || true
  done
}
# A workspace holds its own copies of the scaffold (xbind init); --dev-overlay
# serves the repo's workspace-template/ over them, so the shell and tiles
# under test are always the source tree (web/ is served from source by --dev).
start() {
  # XBIN_AGENT_FAKE registers the scripted "fake" agent provider (D74),
  # XBIN_BIN points the daemon at the bx it binds in as the agent host, and
  # XBIN_SDK_PATH lets Go backends (llm-gw, the agent template) build
  # against this checkout's sdk/.
  (cd "$REPO" && nohup bin/fakeopenai -addr "$FAKEOPENAI_ADDR" > "$HARNESS_DIR/fakeopenai.log" 2>&1 < /dev/null &)
  (cd "$REPO" && XBIN_AGENT_FAKE="$REPO/bin/fakeacp" XBIN_BIN="$REPO/bin" XBIN_SDK_PATH="$REPO/sdk" nohup bin/xbind --dev --dev-overlay "$REPO/workspace-template" --workspace "$WS" --listen "127.0.0.1:$PORT" \
      --ingress-listen "$INGRESS_ADDR" --external-url "$URL" "${asset_flags[@]}" > "$HARNESS_DIR/xbind.log" 2>&1 < /dev/null &)
  for _ in $(seq 1 60); do curl -sf -o /dev/null "$URL/login" && return 0; sleep 0.25; done
  echo "xbind did not come up; see $H/xbind.log" >&2; exit 1
}
build() { (cd "$REPO" && go build -o bin/xbind ./cmd/xbind && CGO_ENABLED=0 go build -o bin/bx ./cmd/bx && go build -o bin/fakeacp ./hack/fakeacp && go build -o bin/fakeopenai ./hack/fakeopenai); }

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
