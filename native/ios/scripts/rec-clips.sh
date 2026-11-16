#!/usr/bin/env bash
# native/ios/scripts/rec-clips.sh <dir> — cuts a filmed UI test run into its
# clips (XBIN_E2E_RECORD=1, ci-uitests.sh): <dir>/recording.mov from the
# moment in <dir>/recording.start, the marks in <dir>/clips.tsv
# (`name<TAB>start|end<TAB>epoch seconds<TAB>title`, XbinDuoRecordingTests),
# each clip trimmed and transcoded with macOS's avconvert into
# <dir>/clips/<name>.mp4 (H.264), its title beside it in <name>.title.
#
#   REC_PRESET   avconvert preset (default Preset1280x720: the long side at
#                most 1280, H.264)
#   REC_PAD      seconds kept before a clip's start mark (default 0.3)
#
# macOS only (avconvert); bash 3.2.
set -euo pipefail
dir=${1:?usage: rec-clips.sh <dir with recording.mov, recording.start, clips.tsv>}
preset=${REC_PRESET:-Preset1280x720}
pad=${REC_PAD:-0.3}
say() { echo "rec-clips: $*" >&2; }

[ -f "$dir/recording.mov" ] || { say "no $dir/recording.mov"; exit 2; }
[ -f "$dir/recording.start" ] || { say "no $dir/recording.start"; exit 2; }
[ -f "$dir/clips.tsv" ] || { say "no $dir/clips.tsv: no clips were marked"; exit 0; }
command -v avconvert >/dev/null 2>&1 || { say "avconvert not found (macOS)"; exit 2; }
mkdir -p "$dir/clips"

# name, start offset, duration, title — one line per clip that has both marks
python3 - "$dir/recording.start" "$dir/clips.tsv" "$pad" >"$dir/clips/cuts.tsv" <<'PY'
import sys
t0 = float(open(sys.argv[1]).read().strip())
pad = float(sys.argv[3])
starts, titles, order = {}, {}, []
for line in open(sys.argv[2], encoding="utf-8"):
    parts = line.rstrip("\n").split("\t")
    if len(parts) < 3:
        continue
    name, event, at = parts[0], parts[1], float(parts[2])
    if event == "start":
        starts[name] = at
        titles[name] = parts[3] if len(parts) > 3 else name
    elif event == "end" and name in starts:
        a = max(0.0, starts[name] - t0 - pad)
        order.append((name, a, at - t0 - a, titles[name]))
for name, a, d, title in order:
    print("%s\t%.3f\t%.3f\t%s" % (name, a, d, title))
PY

n=0
while IFS=$'\t' read -r name start dur title; do
  [ -n "$name" ] || continue
  out="$dir/clips/$name.mp4"
  if avconvert --source "$dir/recording.mov" --preset "$preset" --output "$out" --replace \
    --start "$start" --duration "$dur" >"$dir/clips/$name.log" 2>&1; then
    printf '%s\n' "$title" >"$dir/clips/$name.title"
    n=$((n + 1))
    say "$name: ${dur}s from ${start}s → $(du -h "$out" | cut -f1)"
  else
    say "$name: avconvert failed (clips/$name.log)"
  fi
done <"$dir/clips/cuts.tsv"
say "$n clip(s) in $dir/clips"
