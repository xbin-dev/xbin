#!/usr/bin/env bash
# hack/demo/up.sh — the demo film set (README.md): build xbind from this
# checkout, start it on a fresh workspace with the scripted model beside it,
# dress the workspace as Larkspan (seed.sh), and snapshot the result so
# reset.sh can put it back in seconds between takes.
#
#   hack/demo/up.sh --isolate [--no-build]   start (PORT, default 9400)
#   hack/demo/up.sh --unsandboxed [--no-build]
#   hack/demo/up.sh --stop                   stop it
#
#   --isolate      xbind --isolate: tile sandboxes, people's partitions (the
#                  agent, apps/expenses), coding sandboxes and VMs on xbind's
#                  own runtime. Uses the machine settings hack/dev-setup.sh
#                  wrote (.dev.mk: the rootfs, fuse-overlayfs, the VM assets).
#   --unsandboxed  no sandboxes: every person's terminal is a shell as you
#                  on this machine. Only for a box nobody else reaches —
#                  never tunnel or expose such a set (footage.sh refuses).
#   --no-build     reuse bin/ as it is
#
# The people's password is random per set: printed once at the end, kept
# mode 600 in DEMO_DIR/ws.password (DEMO_PASSWORD sets one).
#
# env: PORT (9400), DEMO_DIR (.demo/<PORT> in this checkout: the workspace is
#      DEMO_DIR/ws, its snapshot DEMO_DIR/ws.snap, logs beside them; up.sh
#      only ever stops or remakes a directory it made — the marker
#      .larkspan-set says so), DEMO_TZ (the set's time zone; default the
#      company's, company.json), ROOTFS (--isolate; default
#      XBIN_TEST_ROOTFS), DEMO_LLM=real with ANTHROPIC_API_KEY /
#      OPENAI_API_KEY (seed.sh), XBIN_GOCRYPTFS
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib.sh"
export REPO=$DEMO_REPO
cd "$REPO"

isolate="" sandboxing="" build=1 mode=up
for a in "$@"; do
  case "$a" in
    --isolate) isolate=1 sandboxing=1 ;;
    --unsandboxed) isolate="" sandboxing=0 ;;
    --no-build) build="" ;;
    --stop) mode=stop ;;
    -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
    *) echo "unknown argument: $a (--isolate, --unsandboxed, --no-build, --stop)" >&2; exit 2 ;;
  esac
done
export PORT=${PORT:-9400}
DEMO_DIR=$(realpath -m "${DEMO_DIR:-$REPO/.demo/$PORT}")
WS="$DEMO_DIR/ws"
URL="http://127.0.0.1:$PORT"
FAKE="127.0.0.1:$((PORT + 10280))"

# stop_all: the set's xbind and scripted model — only a set up.sh made
stop_all() {
  is_demo_dir "$DEMO_DIR" || return 0
  stop_xbind "$WS"
  pkill -f -- "$(proc_rx fakeopenai "-addr $(re_quote "$FAKE")( |\$)")" 2>/dev/null || true
}
if [[ "$mode" == stop ]]; then
  is_demo_dir "$DEMO_DIR" || { echo "$DEMO_DIR is no film set up.sh made (no $DEMO_MARKER): left alone" >&2; exit 1; }
  stop_all; echo "stopped the demo on :$PORT"; exit 0
fi
if [[ -z "$sandboxing" ]]; then
  echo "say how the set runs: --isolate (sandboxes: what to film, and what may ever be tunnelled)," >&2
  echo "or --unsandboxed (every person's terminal is a shell as you on this machine)" >&2
  exit 2
fi
# a directory up.sh didn't make is someone's: never stop, wipe or reuse it
if [[ -e "$DEMO_DIR" ]] && ! is_demo_dir "$DEMO_DIR" && [[ -n "$(ls -A "$DEMO_DIR" 2>/dev/null)" ]]; then
  echo "$DEMO_DIR holds something up.sh didn't make (no $DEMO_MARKER): pick an empty or new DEMO_DIR" >&2
  exit 1
fi

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
touch "$DEMO_DIR/$DEMO_MARKER"
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
  btrfs subvolume delete "$WS" >/dev/null 2>&1 || rm -rf --one-file-system -- "$WS"
fi
if [[ "$(stat -f -c %T "$DEMO_DIR")" == btrfs ]] && btrfs subvolume create "$WS" >/dev/null 2>&1; then
  echo "workspace: a btrfs subvolume at $WS"
else
  mkdir -p "$WS"
  echo "workspace: $WS"
fi
bin/xbind init "$WS" >/dev/null

# one zone, one now, one day for the seed, git, xbind and the scripted model
demo_clock
echo "the set's day: $DEMO_DAY ($DEMO_TZ)"
demo_password "$WS" new

nohup bin/fakeopenai -addr "$FAKE" -script "$REPO/hack/demo/data/model-script.json" -day "$DEMO_DAY" > "$DEMO_DIR/fakeopenai.log" 2>&1 < /dev/null &
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

python3 - "$REPO/hack/demo/company.json" "$URL" "$PORT" "$WS.password" "$isolate" <<'PY'
import json, sys
c = json.load(open(sys.argv[1])); url, port, pwfile, isolate = sys.argv[2:6]
p = c["personas"]
name = {x["id"]: x["name"] for x in c["people"]}
pw = open(pwfile).read().strip()
print(f"""
{c["company"]["name"]} is up at {url}
  sign in as {p["admin"]} ({name[p["admin"]]}, an admin) or {p["person"]} ({name[p["person"]]}); password {pw}
  (everyone in hack/demo/company.json signs in the same way; the password is in {pwfile})
  between takes:  PORT={port} hack/demo/reset.sh
  stop:           PORT={port} hack/demo/up.sh --stop""")
if not isolate:
    print("  UNSANDBOXED: every person's terminal is a shell as you here — keep this set on this machine")
PY
