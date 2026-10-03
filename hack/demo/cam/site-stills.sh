#!/usr/bin/env bash
# hack/demo/cam/site-stills.sh — the website's stills of the demo film set
# (hack/demo/README.md): each site-* shot (shots/) on a desk, 1440×900 at
# device scale 2, and on a phone, 390×844 at 3, then shots.json beside them
# (stills-manifest.js: file, pixel size, what it shows, the marks logged).
#
#   hack/demo/cam/site-stills.sh --out DIR --ws WORKSPACE [--url URL] [--only desk|phone]
#                                [--theme dark|light|both] [shot…]
#
#   --url   the film set (default $URL, else http://127.0.0.1:$PORT, PORT 9400)
#   --ws    its workspace directory (site-live edits a tile's files there;
#           its password is WORKSPACE.password; default $DEMO_WS)
#   --theme the workspace's light or dark (D184; default $DEMO_THEME, else
#           dark): a light take is NN-shot-size-light (shots.json says which)
#   shot…   canvas live agent terminal sandboxes network admin partitions
#           phone (default: all, in that order; phone is phone-only)
#
# The set as seeded: the stills sign in as its people (company.json) and put
# their screens back first (data/layouts.json). The browser runs in the
# set's time zone (DEMO_TZ, as seeded; default the company's) and reads the
# set's times of day in it. PLAYWRIGHT_DIR as for the UI harness. A shot
# that fails is said so at the end; the others still run. The directory is
# what gets published: the sidecars in it carry no URL or local path
# (stills-manifest.js).
set -euo pipefail
C="$(cd "$(dirname "$0")" && pwd)"

out="" ws="${DEMO_WS:-}" url="${URL:-http://127.0.0.1:${PORT:-9400}}" only="" theme="${DEMO_THEME:-dark}" shots=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) out=$2; shift 2 ;;
    --ws) ws=$2; shift 2 ;;
    --url) url=$2; shift 2 ;;
    --only) only=$2; shift 2 ;;
    --theme) theme=$2; shift 2 ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    -*) echo "unknown option $1" >&2; exit 2 ;;
    *) shots+=("$1"); shift ;;
  esac
done
[[ -n "$out" ]] || { echo "--out DIR is required" >&2; exit 2; }
[[ -n "$ws" && -d "$ws" ]] || { echo "--ws WORKSPACE (the set's workspace directory) is required" >&2; exit 2; }
ws=$(cd "$ws" && pwd)
all=(canvas live agent terminal sandboxes network admin partitions phone)
[[ ${#shots[@]} -gt 0 ]] || shots=("${all[@]}")
case "$theme" in
  dark|light) themes=("$theme") ;;
  both) themes=(dark light) ;;
  *) echo "--theme is dark, light or both, not $theme" >&2; exit 2 ;;
esac
mkdir -p "$out"

# the set's password (random per set: hack/demo/lib.sh demo_password) and
# its time zone, for the browser
. "$C/../lib.sh"
demo_password "$ws"
export TZ="${DEMO_TZ:-$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["company"]["timeZone"])' "$C/../company.json")}"

# a person's instances (partitions) started minutes ahead of the
# partitions still, not seconds (shots/site-warm.js; no still)
if [[ " ${shots[*]} " == *" partitions "* ]]; then
  warm=$(mktemp -d "${TMPDIR:-/tmp}/site-warm.XXXXXX")
  node "$C/shot.js" site-warm --mode still --theme dark --size 1440x900 --dpr 1 --take warm --out "$warm" --url "$url" --set ws="$ws" ||
    echo "(the warm-up failed: the partitions still may show instances just started)" >&2
  rm -rf -- "$warm"
fi

failed=()
for s in "${shots[@]}"; do
  n=""
  for i in "${!all[@]}"; do [[ "${all[$i]}" == "$s" ]] && n=$(printf '%02d' $((i + 1))); done
  [[ -n "$n" ]] || { echo "no shot $s (${all[*]})" >&2; exit 2; }
  for t in "${themes[@]}"; do
    sfx=""; [[ "$t" == dark ]] || sfx="-$t"
    for size in desk phone; do
      [[ -n "$only" && "$size" != "$only" ]] && continue
      [[ "$s" == phone && "$size" == desk ]] && continue
      if [[ "$size" == desk ]]; then geo=(--size 1440x900 --dpr 2); else geo=(--size 390x844 --dpr 3); fi
      echo "== $n $s ($size, $t)"
      if ! node "$C/shot.js" "site-$s" --mode still --theme "$t" "${geo[@]}" --take "$n-$s-$size$sfx" --out "$out" --url "$url" --set ws="$ws"; then
        failed+=("$s/$size/$t")
      fi
    done
  done
done
node "$C/stills-manifest.js" "$out"
if [[ ${#failed[@]} -gt 0 ]]; then echo "failed: ${failed[*]}" >&2; exit 1; fi
