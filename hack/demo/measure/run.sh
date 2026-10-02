#!/usr/bin/env bash
# hack/demo/measure/run.sh — take the measurements behind the promo film's
# and the website's numbers (hack/demo/measurements.md: the method, the
# results, the claims they support). Every sample lands in $MEASURE_OUT as
# JSON lines (<name>.jsonl), with a summary per measurement
# (<name>.summary.json), the go test log (<which>.log) and this machine's
# spec and the commit measured (machine-<which>.txt).
#
#   hack/demo/measure/run.sh [vm|save|swap|partition|browser|all]
#
# MEASURE_OUT  where the raw data goes (default: .film-media/measure in the
#              checkout, which git ignores)
# MEASURE_PORT the first of the ports it listens on (default 9341; it uses
#              that one and the next few: xbind, the latency proxy)
#
# Needs: user namespaces, the base rootfs and helpers (.dev.mk from
# hack/dev-setup.sh), /dev/kvm (vm), Playwright with Chromium in
# PLAYWRIGHT_DIR (browser). On a dev box run it with the Bash tool's
# sandbox off. Set TMPDIR short (unix socket paths live under it).
set -euo pipefail
repo=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$repo"
if [ -f .dev.mk ]; then
  set -a
  eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v '^PATH')"
  set +a
fi
export MEASURE_OUT=${MEASURE_OUT:-$repo/.film-media/measure}
export MEASURE_PORT=${MEASURE_PORT:-9341}
mkdir -p "$MEASURE_OUT"

# MEASURE_SINGLE_UID=1: a session that itself runs in a single-uid user
# namespace (an agent's tool sandbox) can't use the setuid newuidmap /
# newgidmap, so xbind's range mode (picked whenever they are on PATH and
# /etc/subuid delegates a range) fails every sandbox start. This hides the
# two from PATH: xbind then runs its sandboxes in single-uid mode, as on a
# host without a sub-uid range. PATH becomes go's directory plus a copy of
# /usr/bin (symlinks) without them.
if [ "${MEASURE_SINGLE_UID:-}" = 1 ]; then
  nb="${TMPDIR:-/tmp}/measure-single-uid-bin"
  mkdir -p "$nb"
  for f in /usr/bin/*; do
    n=${f##*/}
    case "$n" in newuidmap|newgidmap) continue ;; esac
    [ -e "$nb/$n" ] || [ -L "$nb/$n" ] || ln -s "$f" "$nb/$n"
  done
  PATH="$(dirname "$(command -v go)"):$nb"
  export PATH
  hash -r
  if command -v newuidmap >/dev/null; then echo "MEASURE_SINGLE_UID: newuidmap still on PATH" >&2; exit 1; fi
fi

which=${1:-all}
case "$which" in
  vm) re='^TestVM$' ;;
  save) re='^TestSaveToLive$' ;;
  swap) re='^TestSwap$' ;;
  partition) re='^TestPartition$' ;;
  browser) re='^TestBrowser$' ;;
  all) re='^(TestVM|TestSaveToLive|TestSwap|TestPartition|TestBrowser)$' ;;
  *) echo "usage: $0 [vm|save|swap|partition|browser|all]" >&2; exit 2 ;;
esac

{
  echo "date: $(date)"
  echo "commit: $(git rev-parse HEAD) ($(git describe --tags --always --dirty))"
  # the product measured: what differs from the merge base with origin/master
  # outside the measuring tools themselves (hack/demo, test/xbindtest)
  base=$(git merge-base HEAD origin/master 2>/dev/null || git rev-parse HEAD)
  if git diff --quiet "$base" -- . ':!hack/demo' ':!test/xbindtest'; then
    echo "product code: identical to $base (origin/master's merge base; only the measuring tools differ)"
  else
    echo "product code: DIFFERS from $base outside hack/demo and test/xbindtest"
  fi
  echo "kernel: $(uname -srm)"
  echo "cpu: $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //') × $(nproc) threads"
  echo "memory: $(awk '/MemTotal/ {printf "%.0f GiB", $2/1048576}' /proc/meminfo)"
  echo "kvm: $(if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then echo usable; else echo none; fi)"
  if command -v nvidia-smi >/dev/null; then echo "gpu: $(nvidia-smi --query-gpu=name --format=csv,noheader | paste -sd, -)"; fi
  echo "go: $(go version)"
  echo "node: $(node --version 2>/dev/null || echo none)"
  if [ -n "${XBIN_FIRECRACKER:-}" ] && [ -x "$XBIN_FIRECRACKER" ]; then echo "firecracker: $("$XBIN_FIRECRACKER" --version 2>/dev/null | head -1)"; fi
  echo "rootfs: ${XBIN_TEST_ROOTFS:-$repo/.rootfs}"
  echo "load at start: $(cat /proc/loadavg)"
} > "$MEASURE_OUT/machine-$which.txt"
cat "$MEASURE_OUT/machine-$which.txt"

set +e
go test -tags xbinmeasure -count=1 -v -timeout 120m -run "$re" ./hack/demo/measure/ 2>&1 | tee "$MEASURE_OUT/$which.log"
rc=${PIPESTATUS[0]}
set -e
echo "load at end: $(cat /proc/loadavg)" >> "$MEASURE_OUT/machine-$which.txt"
echo "raw data: $MEASURE_OUT"
exit "$rc"
