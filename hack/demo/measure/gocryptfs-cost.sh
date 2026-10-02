#!/usr/bin/env bash
# hack/demo/measure/gocryptfs-cost.sh — what a person's encrypted volume
# costs on this machine (hack/demo/measurements.md §5): gocryptfs init, a
# first mount and a remount with xbind's own flags (internal/resenc: -q
# -passfile /dev/stdin, -scryptn 10 for partition volumes), 20 times, one
# JSON line each, timed in a user namespace of its own (unshare -Urm), where
# FUSE mounts work without the setuid fusermount3.
#
#   hack/demo/measure/gocryptfs-cost.sh [out.jsonl]
#
# XBIN_GOCRYPTFS is the binary (default: the repo's bin/gocryptfs, which
# make helpers fetches).
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../.." && pwd)
G=${XBIN_GOCRYPTFS:-$repo/bin/gocryptfs}
OUT=${1:-/dev/stdout}
D=$(mktemp -d "${TMPDIR:-/tmp}/gocryptfs-cost-XXXXXX")
trap 'rm -rf "$D"' EXIT
export G D
unshare -Urm bash -c '
set -euo pipefail
ns() { date +%s%N; }
echo "gocryptfs: $("$G" -version 2>&1 | head -1)"
for i in $(seq 1 20); do
  c=$D/c$i; m=$D/m$i
  mkdir -p "$c" "$m"
  t0=$(ns)
  printf "measure-pw-%s" "$i" | "$G" -init -q -passfile /dev/stdin -scryptn 10 "$c" >/dev/null
  t1=$(ns)
  printf "measure-pw-%s" "$i" | "$G" -q -passfile /dev/stdin "$c" "$m"
  t2=$(ns)
  echo "a note" > "$m/n.txt"
  umount "$m"
  t3=$(ns)
  printf "measure-pw-%s" "$i" | "$G" -q -passfile /dev/stdin "$c" "$m"
  t4=$(ns)
  cat "$m/n.txt" >/dev/null
  umount "$m"
  echo "{\"i\":$i,\"initMs\":$(( (t1-t0)/1000 ))e-3,\"mountNewMs\":$(( (t2-t1)/1000 ))e-3,\"remountMs\":$(( (t4-t3)/1000 ))e-3}"
done
' > "$OUT"
