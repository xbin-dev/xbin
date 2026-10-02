#!/usr/bin/env bash
# hack/demo/up.sh — the demo film set (README.md): build xbind from this
# checkout, start it on a fresh workspace with the scripted model beside it,
# dress the workspace as Larkspan (seed.sh), and snapshot the result so
# reset.sh can put it back in seconds between takes.
#
#   hack/demo/up.sh [--isolate] [--no-build]   start (PORT, default 9400)
#   hack/demo/up.sh --stop                     stop it
#
#   --isolate    xbind --isolate: tile sandboxes, people's partitions (the
#                agent, apps/expenses), coding sandboxes and VMs on xbind's
#                own runtime. Uses the machine settings hack/dev-setup.sh
#                wrote (.dev.mk: the rootfs, fuse-overlayfs, the VM assets).
#   --no-build   reuse bin/ as it is
#
# env: PORT (9400), DEMO_DIR (.demo/<PORT> in this checkout: the workspace is
#      DEMO_DIR/ws, its snapshot DEMO_DIR/ws.snap, logs beside them),
#      ROOTFS (--isolate; default XBIN_TEST_ROOTFS), DEMO_LLM=real with
#      ANTHROPIC_API_KEY / OPENAI_API_KEY (seed.sh), XBIN_GOCRYPTFS
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib.sh"
export REPO=$DEMO_REPO
cd "$REPO"

isolate="" build=1 mode=up
for a in "$@"; do
  case "$a" in
    --isolate) isolate=1 ;;
    --no-build) build="" ;;
    --stop) mode=stop ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a (--isolate, --no-build, --stop)" >&2; exit 2 ;;
  esac
done
export PORT=${PORT:-9400}
DEMO_DIR=${DEMO_DIR:-$REPO/.demo/$PORT}
WS="$DEMO_DIR/ws"
URL="http://127.0.0.1:$PORT"
FAKE="127.0.0.1:$((PORT + 10280))"

stop_all() {
  stop_xbind "$WS"
  pkill -f -- "$(proc_rx fakeopenai "-addr $(re_quote "$FAKE")( |\$)")" 2>/dev/null || true
}
if [[ "$mode" == stop ]]; then stop_all; echo "stopped the demo on :$PORT"; exit 0; fi

load_devmk
reexec_userns "$0" "$@"
if [[ -n "$isolate" ]]; then
  ROOTFS=${ROOTFS:-${XBIN_TEST_ROOTFS:-$REPO/.rootfs}}
  [[ -x "$ROOTFS/bin/sh" ]] || { echo "--isolate: no rootfs at $ROOTFS (make rootfs, or ROOTFS=dir)" >&2; exit 1; }
fi
# encrypted resources need gocryptfs: without it xbind holds every tile
# with a file-backed resource (the CRM, the agent…)
if [[ -z "${XBIN_GOCRYPTFS:-}" ]]; then
  if [[ -x "$REPO/bin/gocryptfs" ]]; then export XBIN_GOCRYPTFS=$REPO/bin/gocryptfs
  elif command -v gocryptfs >/dev/null; then export XBIN_GOCRYPTFS; XBIN_GOCRYPTFS=$(command -v gocryptfs)
  else echo "no gocryptfs (make gocryptfs, or XBIN_GOCRYPTFS=…): xbind would hold the demo's tiles" >&2; exit 1; fi
fi

if [[ -n "$build" ]]; then
  echo "building xbind, bx, fakeopenai…"
  go build -o bin/xbind ./cmd/xbind
  CGO_ENABLED=0 go build -o bin/bx ./cmd/bx
  go build -o bin/fakeopenai ./hack/fakeopenai
  [[ -n "$isolate" ]] && CGO_ENABLED=0 go build -o bin/xbin-vmagent ./cmd/xbin-vmagent
fi

stop_all
mkdir -p "$DEMO_DIR"
# xbind holds 10% of its disk free and, below that, tells everyone on every
# screen ("Workspace disk low"): a set on a fuller disk films the banner
free_pct=$(df -P "$DEMO_DIR" | awk 'NR == 2 { printf "%d", 100 * $4 / $2 }')
if (( free_pct < 10 )); then
  echo "warning: the disk under $DEMO_DIR is ${free_pct}% free — every screen will show xbind's" \
    "'Workspace disk low' banner (under 10%); put the set on a roomier disk: DEMO_DIR=/path $0" >&2
fi
# a fresh workspace: a btrfs subvolume where the directory is on btrfs and
# we may make one (reset.sh then snapshots it in no time), else a directory
if [[ -e "$WS" ]]; then
  btrfs subvolume delete "$WS" >/dev/null 2>&1 || rm -rf "$WS"
fi
if [[ "$(stat -f -c %T "$DEMO_DIR")" == btrfs ]] && btrfs subvolume create "$WS" >/dev/null 2>&1; then
  echo "workspace: a btrfs subvolume at $WS"
else
  mkdir -p "$WS"
  echo "workspace: $WS"
fi
bin/xbind init "$WS" >/dev/null

nohup bin/fakeopenai -addr "$FAKE" -script "$REPO/hack/demo/data/lark-script.json" > "$DEMO_DIR/fakeopenai.log" 2>&1 < /dev/null &
flags=(--dev --workspace "$WS" --listen "127.0.0.1:$PORT" --ingress-listen "127.0.0.1:$((PORT + 1))" --external-url "$URL")
[[ -n "$isolate" ]] && flags+=(--isolate --rootfs "$ROOTFS")
XBIN_BIN="$REPO/bin" XBIN_SDK_PATH="$REPO/sdk" nohup bin/xbind "${flags[@]}" > "$DEMO_DIR/xbind.log" 2>&1 < /dev/null &
wait_up "$URL"

echo "seeding (log: $DEMO_DIR/seed.log)…"
if ! URL=$URL WS=$WS TOKEN=$(cat "$WS/.xbin/token") DEMO_ISOLATE=$isolate FAKEOPENAI_ADDR=$FAKE \
    bash "$REPO/hack/demo/seed.sh" > "$DEMO_DIR/seed.log" 2>&1; then
  echo "seed failed:"; grep -E '^!!' "$DEMO_DIR/seed.log" | head -20; tail -3 "$DEMO_DIR/seed.log"; exit 1
fi
tail -1 "$DEMO_DIR/seed.log"
"$REPO/hack/demo/reset.sh" --snapshot "$WS"

python3 - "$REPO/hack/demo/company.json" "$URL" "$PORT" <<'PY'
import json, sys
c = json.load(open(sys.argv[1])); url, port = sys.argv[2], sys.argv[3]
p = c["personas"]
name = {x["id"]: x["name"] for x in c["people"]}
print(f"""
{c["company"]["name"]} is up at {url}
  sign in as {p["admin"]} ({name[p["admin"]]}, an admin) or {p["person"]} ({name[p["person"]]}); password {c["password"]}
  (everyone in hack/demo/company.json signs in the same way)
  between takes:  PORT={port} hack/demo/reset.sh
  stop:           PORT={port} hack/demo/up.sh --stop""")
PY
