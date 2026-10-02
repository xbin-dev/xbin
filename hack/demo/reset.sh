#!/usr/bin/env bash
# hack/demo/reset.sh — put the seeded demo film set back between takes.
#
#   hack/demo/reset.sh --snapshot [WS]   take the snapshot (up.sh does, once,
#                                        right after seeding)
#   hack/demo/reset.sh [WS]              restore it: stop xbind, swap the
#                                        workspace for the snapshot, start
#                                        xbind again; prints how long it took
#
# WS is the workspace directory: by default up.sh's ($DEMO_DIR/ws, i.e.
# .demo/$PORT/ws); for the UI harness's set pass "$HARNESS_DIR/ws". The
# snapshot sits beside it (WS.snap): a btrfs snapshot when WS is a btrfs
# subvolume (up.sh makes one where it may), else a reflink copy (cp
# --reflink=always), else a plain copy (tmpfs and other filesystems without
# reflinks). xbind is stopped for both, so the snapshot is consistent, and
# restarted as it was running (its command line, directory and environment,
# less anything secret-shaped, kept in WS.snap.meta/). The scripted model
# (hack/fakeopenai) is started again only if it isn't running.
set -euo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib.sh"

mode=restore ws=""
for a in "$@"; do
  case "$a" in
    --snapshot) mode=snapshot ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    -*) echo "unknown option: $a" >&2; exit 2 ;;
    *) ws=$a ;;
  esac
done
WS=$(realpath -m "${ws:-${DEMO_DIR:-$DEMO_REPO/.demo/${PORT:-9400}}/ws}")
SNAP="$WS.snap" META="$WS.snap.meta"

t0=$(date +%s.%N)
elapsed() { python3 -c "import sys,time; print(f'{time.time() - float(sys.argv[1]):.1f}')" "$t0"; }
is_subvol() { [[ "$(stat -f -c %T "$1")" == btrfs && "$(stat -c %i "$1")" == 256 ]]; }
remove() { btrfs subvolume delete "$1" >/dev/null 2>&1 || rm -rf "$1"; }

# record: how the running xbind (and the scripted model) were started
record() {
  local pid=$1 listen fpid
  rm -rf "$META"; mkdir -m 700 "$META"
  cat "/proc/$pid/cmdline" > "$META/xbind.argv"
  readlink "/proc/$pid/cwd" > "$META/xbind.cwd"
  { readlink "/proc/$pid/fd/1" 2>/dev/null || echo /dev/null; } > "$META/xbind.log"
  # the environment, less anything secret-shaped: secrets live in the
  # workspace's vault, never in a file here
  tr '\0' '\n' < "/proc/$pid/environ" | grep -Ev '^[A-Za-z0-9_]*(KEY|TOKEN|SECRET|PASS|CREDENTIAL)[A-Za-z0-9_]*=' | tr '\n' '\0' > "$META/xbind.env"
  listen=$(tr '\0' '\n' < "$META/xbind.argv" | grep -A1 -x -- --listen | tail -1)
  printf '%s' "$listen" > "$META/listen"
  # the scripted model sits at the xbind port + 10280 (up.sh, the harness)
  fpid=$(pgrep -f -- "$(proc_rx fakeopenai "-addr $(re_quote "127.0.0.1:$((${listen##*:} + 10280))")( |\$)")" | head -1 || true)
  if [[ -n "$fpid" ]]; then
    cat "/proc/$fpid/cmdline" > "$META/fake.argv"
    readlink "/proc/$fpid/cwd" > "$META/fake.cwd"
  fi
  date +%F > "$META/taken"
}

# start_recorded: xbind as it ran — in a user namespace of its own where
# this one can't mount FUSE (lib.sh need_userns), which lives as long as it
# does. (Only xbind goes there: from inside, the xbind we stop, a process of
# another namespace, could not be read.)
start_recorded() {
  local -a argv env fargv userns=()
  mapfile -d '' argv < "$META/xbind.argv"
  mapfile -d '' env < "$META/xbind.env"
  # (each starts as the background subshell itself, exec'd: no shell is left
  # waiting on it with this script's output open)
  if [[ -f "$META/fake.argv" ]]; then
    mapfile -d '' fargv < "$META/fake.argv"
    if ! pgrep -f -- "$(proc_rx fakeopenai "-addr $(re_quote "${fargv[2]}")( |\$)")" >/dev/null; then
      (cd "$(cat "$META/fake.cwd")" && exec nohup "${fargv[@]}" >> /dev/null 2>&1 < /dev/null) &
    fi
  fi
  need_userns && userns=(unshare --user --map-root-user --mount --)
  (cd "$(cat "$META/xbind.cwd")" && exec nohup "${userns[@]}" env -i "${env[@]}" "${argv[@]}" >> "$(cat "$META/xbind.log")" 2>&1 < /dev/null) &
  wait_up "http://$(cat "$META/listen")"
}

if [[ "$mode" == snapshot ]]; then
  pid=$(xbind_pid "$WS")
  [[ -n "$pid" ]] || { echo "no xbind serves $WS: start it first (up.sh)" >&2; exit 1; }
  record "$pid"
  stop_xbind "$WS"
  [[ -e "$SNAP" ]] && remove "$SNAP"
  if is_subvol "$WS" && btrfs subvolume snapshot "$WS" "$SNAP" >/dev/null 2>&1; then how="a btrfs snapshot"
  elif cp -a --reflink=always "$WS" "$SNAP" 2>/dev/null; then how="a reflink copy"
  else rm -rf "$SNAP"; cp -a "$WS" "$SNAP"; how="a copy (no reflinks on $(stat -f -c %T "$WS"))"; fi
  echo "$how" > "$META/how"
  start_recorded
  echo "snapshot of $WS taken ($how) in $(elapsed) s; xbind is back up"
  exit 0
fi

[[ -d "$SNAP" && -f "$META/xbind.argv" ]] || { echo "no snapshot of $WS: run hack/demo/reset.sh --snapshot first (up.sh does)" >&2; exit 1; }
stop_xbind "$WS"
old="$WS.old.$$"
[[ -e "$WS" ]] && mv "$WS" "$old"
if is_subvol "$SNAP" && btrfs subvolume snapshot "$SNAP" "$WS" >/dev/null 2>&1; then how="btrfs snapshot"
elif cp -a --reflink=always "$SNAP" "$WS" 2>/dev/null; then how="reflink copy"
else rm -rf "$WS"; cp -a "$SNAP" "$WS"; how="copy"; fi
start_recorded
up=$(elapsed)
# the apps the stills and the footage open first answer (their backends
# start on first use)
TOKEN=$(cat "$WS/.xbin/token")
for p in apps/crm/summary apps/ops-report/reports apps/lark/me apps/llm-gw/config "apps/calendar/events?day=$(date +%F)"; do
  for _ in $(seq 1 120); do
    curl -sf -o /dev/null -H "Authorization: Bearer $TOKEN" "http://$(cat "$META/listen")/api/$p" && break
    sleep 0.25
  done
done
echo "restored $WS from its snapshot ($how): xbind up in $up s, the apps answering in $(elapsed) s"
if [[ "$(cat "$META/taken")" != "$(date +%F)" ]]; then
  echo "note: the snapshot is from $(cat "$META/taken"); the set's relative times (today's meetings, '18 min ago') belong to that day — run up.sh again on the shooting day" >&2
fi
[[ -e "$old" ]] && (nohup bash -c "btrfs subvolume delete '$old' >/dev/null 2>&1 || rm -rf '$old'" > /dev/null 2>&1 < /dev/null &)
exit 0
