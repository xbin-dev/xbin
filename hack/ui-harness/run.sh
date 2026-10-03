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
#   HARNESS_THEME=light ./run.sh …   every browser context reports a light
#                       system (default dark, lib.js): documents that follow
#                       the person's theme draw Concrete Day (D184)
#   HARNESS_ISOLATE=1 (or ISOLATE=1) [ROOTFS=dir] ./run.sh …   xbind --isolate
#                       over ROOTFS (else $XBIN_TEST_ROOTFS, else the repo's
#                       .rootfs): backends and tile sandboxes run live, and
#                       the livereload, deployments and sandboxes passes drive
#                       them; the other passes are written for the default,
#                       unisolated harness
#   HARNESS_NO_OVERLAY=1 ./run.sh --keep oldScaffold   serve the workspace's
#                       own scaffold copies (no --dev-overlay): the oldScaffold
#                       pass swaps shell/ and tiles/admin/ for the last
#                       release's (HARNESS_OLD_SCAFFOLD, a git tag) and puts
#                       them back; under the overlay it SKIPs, said so
#   HARNESS_ISOLATE=1 HARNESS_AGENT_PARTITION=1 ./run.sh …   seed apps/agent
#                       partitioned (the template's default for new
#                       instances; otherwise it is seeded unpartitioned, as
#                       every other agent pass and the isolated sandbox
#                       passes expect): the admin's page is their own
#                       partition — the agentTemplate pass runs against it,
#                       and agentHomes (shared chats, two homes) needs it,
#                       as does agentHosted (non-secure, hosted chats)
#   HARNESS_SEED=demo ./run.sh --keep [pass…]   seed the demo film set
#                       (hack/demo/seed.sh: Larkspan, a fictional company's
#                       workspace — hack/demo/README.md) instead of the test
#                       seed, with fakeopenai playing hack/demo's script; the
#                       passes default to demoStills (the test passes need
#                       the test seed)
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
# HARNESS_ISOLATE=1 (ISOLATE=1 alike): xbind runs backends in per-component
# sandboxes (--isolate) on a rootfs: ROOTFS, else XBIN_TEST_ROOTFS, else the
# repo's .rootfs (make rootfs; a worktree borrows the main checkout's). Passes
# read HARNESS_ISOLATE: without it a backend half asserts the isolation
# refusal and prints SKIP; under it tile sandboxes run live and the sandboxes
# pass drives one. Needs user namespaces; XBIN_FUSE_OVERLAYFS reaches xbind
# from the environment. Run it for the passes that ask for it: the scripted
# fake agent (XBIN_AGENT_FAKE, a host path) can't start inside a tile
# sandbox, so the agent passes (agentTab…) fail under it.
HARNESS_ISOLATE=${HARNESS_ISOLATE:-${ISOLATE:-}}
if [[ "$HARNESS_ISOLATE" == 0 ]]; then HARNESS_ISOLATE=""; fi
export HARNESS_ISOLATE
iso_flags=()
if [[ -n "$HARNESS_ISOLATE" ]]; then
  ROOTFS=${ROOTFS:-${XBIN_TEST_ROOTFS:-$REPO/.rootfs}}
  iso_flags=(--isolate --rootfs "$ROOTFS")
fi
export OUT="$HARNESS_DIR/out"
export REPO
# the scripted OpenAI-compatible upstream the agentTemplate pass talks to
# through llm-gw (hack/fakeopenai)
export FAKEOPENAI_ADDR=${FAKEOPENAI_ADDR:-127.0.0.1:$((PORT + 10280))}
# the ingress listener the channels pass's webhooks arrive on
export INGRESS_ADDR=${INGRESS_ADDR:-127.0.0.1:$((PORT + 1))}
# the host port the sandbox-terminal tile's SSH is published on (the
# sandboxTerminal pass logs in there with OpenSSH)
export SBXTERM_SSH_ADDR=${SBXTERM_SSH_ADDR:-127.0.0.1:$((PORT + 2))}
mkdir -p "$OUT"
mode="${1:-}"
[[ $# -gt 0 ]] && shift
passes=("$@")   # --shots [pass…]
# HARNESS_SEED=demo: the demo film set (hack/demo) — its seed, its model
# script, and its passes unless some are named
export HARNESS_SEED=${HARNESS_SEED:-}
seed_sh="$H/seed.sh" fake_args=()
# the test seed's fakes: the scripted ACP agent registered as the "fake"
# provider (D74), fakesbx's scripted coding agent (D147 §7.3), and fakebin's
# scripted `claude` leading xbind's PATH (D178) — never on the demo set,
# where a terminal's "+" launcher or a pick of Claude Code is filmed
fake_env=(PATH="$H/fakebin:$PATH" XBIN_AGENT_FAKE="$REPO/bin/fakeacp"
  FSB_HARNESS_FAKE="$REPO/bin/fakeacp --steer --auto-mode --require-login --persist")
if [[ "$HARNESS_SEED" == demo ]]; then
  seed_sh="$REPO/hack/demo/seed.sh" fake_env=()
  # the set's clock (hack/demo/clock.py: one zone, one now, one day, TZ for
  # xbind, the model and the seed) — kept with the set, so a --restart or a
  # --shots run later reads the day it was seeded on
  . "$REPO/hack/demo/lib.sh"
  if [[ -z "$mode" || "$mode" == --keep ]] || [[ ! -f "$HARNESS_DIR/demo-clock" ]]; then
    unset NOW_MS DEMO_DAY
    demo_clock
    mkdir -p "$HARNESS_DIR"
    printf 'DEMO_TZ=%s\nNOW_MS=%s\nDEMO_DAY=%s\n' "$DEMO_TZ" "$NOW_MS" "$DEMO_DAY" > "$HARNESS_DIR/demo-clock"
  else
    while IFS= read -r line; do
      [[ "$line" =~ ^(DEMO_TZ|NOW_MS|DEMO_DAY)=([A-Za-z0-9_/+:.-]+)$ ]] && export "${BASH_REMATCH[1]}=${BASH_REMATCH[2]}"
    done < "$HARNESS_DIR/demo-clock"
    export TZ="$DEMO_TZ"
  fi
  fake_args=(-script "$REPO/hack/demo/data/model-script.json" -day "$DEMO_DAY")
  [[ ${#passes[@]} -eq 0 ]] && passes=(demoStills)
elif [[ -n "$HARNESS_SEED" ]]; then
  echo "HARNESS_SEED=$HARNESS_SEED: unknown (demo, or unset for the test seed)" >&2; exit 1
fi
# a mode that starts xbind needs the rootfs before it builds or wipes anything
if [[ -n "$HARNESS_ISOLATE" && "$mode" != --stop && "$mode" != --shots && ! -x "$ROOTFS/bin/sh" ]]; then
  echo "HARNESS_ISOLATE=1: no rootfs at $ROOTFS (make rootfs, or ROOTFS=dir / XBIN_TEST_ROOTFS=dir)" >&2; exit 1
fi

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
# HARNESS_NO_OVERLAY=1: no --dev-overlay — the workspace's own scaffold copies
# are served (the oldScaffold pass swaps them for the last release's)
overlay_flags=(--dev-overlay "$REPO/workspace-template")
[[ -n "${HARNESS_NO_OVERLAY:-}" ]] && overlay_flags=()
# A workspace holds its own copies of the scaffold (xbind init); --dev-overlay
# serves the repo's workspace-template/ over them, so the shell and tiles
# under test are always the source tree (web/ is served from source by --dev).
start() {
  # XBIN_AGENT_FAKE registers the scripted "fake" agent provider (D74),
  # XBIN_BIN points the daemon at the bx it binds in as the agent host, and
  # XBIN_SDK_PATH lets Go backends (llm-gw, the agent template) build
  # against this checkout's sdk/. FSB_HARNESS_FAKE is the coding agent
  # "fake" apps/fakesbx (hack/fakesandbox as a tile) advertises on its image
  # (D147 §7.3): the scripted ACP agent with steering, an auto mode,
  # sign-in and persisted sessions — the agentHarness pass. A host path
  # works here: without --isolate fakesbx runs on the host, every sandbox
  # command is a host process, and a variable without the XBIN_ prefix
  # reaches its backend. Under HARNESS_ISOLATE neither holds (the agent
  # passes don't run there). fakebin/ leads xbind's PATH, which the
  # (host) shells inherit: its scripted `claude` is what the Agent tab's
  # guided sign-in runs (D178; the agentTab pass). The demo set gets none
  # of the three (fake_env, above).
  (cd "$REPO" && nohup bin/fakeopenai -addr "$FAKEOPENAI_ADDR" "${fake_args[@]}" > "$HARNESS_DIR/fakeopenai.log" 2>&1 < /dev/null &)
  (cd "$REPO" && env ${fake_env[@]+"${fake_env[@]}"} XBIN_BIN="$REPO/bin" XBIN_SDK_PATH="$REPO/sdk" \
      nohup bin/xbind --dev "${overlay_flags[@]}" --workspace "$WS" --listen "127.0.0.1:$PORT" \
      --ingress-listen "$INGRESS_ADDR" --external-url "$URL" "${asset_flags[@]}" "${iso_flags[@]}" > "$HARNESS_DIR/xbind.log" 2>&1 < /dev/null &)
  for _ in $(seq 1 60); do curl -sf -o /dev/null "$URL/login" && return 0; sleep 0.25; done
  echo "xbind did not come up; see $H/xbind.log" >&2; exit 1
}
build() {
  (cd "$REPO" && go build -o bin/xbind ./cmd/xbind && CGO_ENABLED=0 go build -o bin/bx ./cmd/bx && go build -o bin/fakeacp ./hack/fakeacp && go build -o bin/fakeopenai ./hack/fakeopenai)
  # VMs need the in-guest agent beside xbind (the demo set's coding
  # sandboxes are VMs; without it they stay empty)
  if [[ -n "$HARNESS_ISOLATE" ]]; then (cd "$REPO" && CGO_ENABLED=0 go build -o bin/xbin-vmagent ./cmd/xbin-vmagent); fi
}
# The seeded agent tiles keep their data in encrypted resources: without a
# gocryptfs binary xbind HOLDS them (every call a 502). Look for one the way
# xbind does (internal/resenc Resolve: $XBIN_GOCRYPTFS, next to bin/xbind,
# $PATH); none ⇒ HARNESS_NO_GOCRYPTFS says why and the passes that need those
# tiles print SKIP instead of timing out. A fresh worktree has no bin/gocryptfs
# (make gocryptfs builds it; copying one from another checkout's bin/ works).
if [[ -n "${XBIN_GOCRYPTFS:-}" ]]; then gcf=$XBIN_GOCRYPTFS; [[ -x "$gcf" && ! -d "$gcf" ]] || gcf=""
elif [[ -x "$REPO/bin/gocryptfs" ]]; then gcf=$REPO/bin/gocryptfs
else gcf=$(command -v gocryptfs || true); fi
if [[ -z "$gcf" ]]; then
  export HARNESS_NO_GOCRYPTFS="no gocryptfs (XBIN_GOCRYPTFS, $REPO/bin/gocryptfs, PATH): make gocryptfs, or point XBIN_GOCRYPTFS at one"
  echo "warning: $HARNESS_NO_GOCRYPTFS — the agent-template passes will SKIP" >&2
fi

case "$mode" in
  --stop) stop; exit 0 ;;
  --shots) if [[ "$HARNESS_SEED" == demo ]]; then demo_password "$WS"; fi ;;
  --restart) stop; build; start; if [[ "$HARNESS_SEED" == demo ]]; then demo_password "$WS"; fi ;;
  *)
    stop; build
    rm -rf "$WS"; "$REPO/bin/xbind" init "$WS" >/dev/null
    # the demo set's people's password: random, mode 600 at $WS.password
    # (hack/demo/lib.sh); the passes read it from DEMO_PASSWORD
    if [[ "$HARNESS_SEED" == demo ]]; then unset DEMO_PASSWORD; demo_password "$WS" new; fi
    # auth ON (no --no-auth): --dev seeds admin/admin, so other users can log in.
    start
    export TOKEN; TOKEN=$(cat "$WS/.xbin/token")
    bash "$seed_sh" > "$OUT/seed.log" 2>&1 || { echo "seed failed:"; tail -20 "$OUT/seed.log"; exit 1; }
    echo "seeded (log: $OUT/seed.log)" ;;
esac

# a failed default run stops xbind too (only --keep/--restart/--shots leave it up)
(cd "$H" && node shots.js "${passes[@]}") > "$OUT/shots.log" 2>&1 || {
  echo "shots failed:"; tail -30 "$OUT/shots.log"; if [[ -z "$mode" ]]; then stop; fi; exit 1; }
tail -40 "$OUT/shots.log"
# what this environment could not exercise, with the reason (checker skip())
grep -F '[shots] SKIP' "$OUT/shots.log" || true
[[ "$mode" == "" ]] && stop
echo "screenshots: $OUT"
ls "$OUT"
