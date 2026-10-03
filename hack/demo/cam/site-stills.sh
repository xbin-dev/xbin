#!/usr/bin/env bash
# hack/demo/cam/site-stills.sh — the website's stills of the demo film set
# (hack/demo/README.md): each site-* shot (shots/) on a desk, 1440×900 at
# device scale 2, and on a phone, 390×844 at 3, then shots.json beside them
# (stills-manifest.js: file, pixel size, what it shows, the marks logged).
#
#   hack/demo/cam/site-stills.sh --out DIR --ws WORKSPACE [--url URL] [--only desk|phone] [shot…]
#
#   --url   the film set (default $URL, else http://127.0.0.1:$PORT, PORT 9400)
#   --ws    its workspace directory (site-live edits a tile's files there;
#           default $DEMO_WS)
#   shot…   canvas live agent terminal sandboxes network admin partitions
#           phone (default: all, in that order; phone is phone-only)
#
# The set as seeded: the stills sign in as its people (company.json) and put
# their screens back first (data/layouts.json). Run it in the set's time
# zone (TZ, the same as DEMO_TZ when it was seeded): the browser's clock
# reads the set's times of day. PLAYWRIGHT_DIR as for the UI harness. A
# shot that fails is said so at the end; the others still run.
set -euo pipefail
C="$(cd "$(dirname "$0")" && pwd)"

out="" ws="${DEMO_WS:-}" url="${URL:-http://127.0.0.1:${PORT:-9400}}" only="" shots=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    --ws) ws=$2; shift 2 ;;
    --url) url=$2; shift 2 ;;
    --only) only=$2; shift 2 ;;
    -h|--help) sed -n '2,21p' "$0"; exit 0 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) shots+=("$1"); shift ;;
  esac
done
[[ -n "$out" ]] || { echo "--out DIR is required" >&2; exit 2; }
[[ -n "$ws" && -d "$ws" ]] || { echo "--ws WORKSPACE (the set's workspace directory) is required" >&2; exit 2; }
all=(canvas live agent terminal sandboxes network admin partitions phone)
[[ ${#shots[@]} -gt 0 ]] || shots=("${all[@]}")
mkdir -p "$out"

failed=()
for s in "${shots[@]}"; do
  n=""
  for i in "${!all[@]}"; do [[ "${all[$i]}" == "$s" ]] && n=$(printf '%02d' $((i + 1))); done
  [[ -n "$n" ]] || { echo "no shot $s (${all[*]})" >&2; exit 2; }
  for size in desk phone; do
    [[ -n "$only" && "$size" != "$only" ]] && continue
    [[ "$s" == phone && "$size" == desk ]] && continue
    if [[ "$size" == desk ]]; then geo=(--size 1440x900 --dpr 2); else geo=(--size 390x844 --dpr 3); fi
    echo "== $n $s ($size)"
    if ! node "$C/shot.js" "site-$s" --mode still "${geo[@]}" --take "$n-$s-$size" --out "$out" --url "$url" --set ws="$ws"; then
      failed+=("$s/$size")
    fi
  done
done
node "$C/stills-manifest.js" "$out"
if [[ ${#failed[@]} -gt 0 ]]; then echo "failed: ${failed[*]}" >&2; exit 1; fi
