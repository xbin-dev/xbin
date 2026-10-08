#!/usr/bin/env bash
# hack/tile-check.sh — vet and test every builtin tile backend and the agent
# template's backend the way a workspace builds them: against go.mod.tile
# (restored to go.mod in a scratch copy) and go.sum as shipped, read-only,
# through a go.work of their own shaped as xbind's build renders one (D166:
# the module, the sdk replaced by this checkout, the go line the highest of
# 1.24 and the module's). A requirement or checksum the module lacks fails
# here as in the build: `go mod tidy` its go.mod.tile and go.sum (in a copy
# named go.mod, the sdk replaced). `make vet` compiles them against the ROOT
# go.mod, which is not what runs; this is. Needs network on first run (each
# tile's own deps).
#
#   hack/tile-check.sh            # every tile (make tile-check)
#   hack/tile-check.sh agent      # one
#   TILE_TEST_FLAGS="-race -count=1" hack/tile-check.sh agent   # extra go test flags
#                                 (-race without -timeout gets -timeout 30m: the
#                                 agent template's tests take ~13 min under it,
#                                 past go test's default 10)
#   TILE_CHECK_JOBS=1 hack/tile-check.sh   # one at a time (default: one per CPU)
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

test_flags=${TILE_TEST_FLAGS:-}
case " $test_flags " in
*" -race "* | *" -race=true "*)
  case " $test_flags " in *" -timeout"*) ;; *) test_flags="$test_flags -timeout 30m" ;; esac ;;
esac

dirs=()
if [ $# -gt 0 ]; then
  for n in "$@"; do
    if [ -f "$repo/builtin-tiles/$n/go.mod.tile" ]; then dirs+=("$repo/builtin-tiles/$n")
    elif [ -f "$repo/builtin-templates/$n/go.mod.tile" ]; then dirs+=("$repo/builtin-templates/$n")
    else echo "tile-check: no go.mod.tile under builtin-tiles/$n or builtin-templates/$n" >&2; exit 2; fi
  done
else
  for f in "$repo"/builtin-tiles/*/go.mod.tile "$repo"/builtin-templates/*/go.mod.tile; do
    [ -f "$f" ] && dirs+=("$(dirname "$f")")
  done
fi

# check_one <dir>: vet + test one backend in its scratch copy; its output
# on stdout, its verdict the exit status.
check_one() {
  local d=$1 name work
  name=$(basename "$d")
  work="$scratch/$name"
  mkdir -p "$work"
  # the backend sources + the tile's module file, nothing else (no deps/, data/)
  # a template ships its backend as _backend (Go ignores _dirs, so the
  # blueprint never builds in place); instantiation renames it — so do we
  [ -d "$d/backend" ] && cp -r "$d/backend" "$work/backend"
  [ -d "$d/_backend" ] && cp -r "$d/_backend" "$work/backend"
  cp "$d/go.mod.tile" "$work/go.mod"
  for s in go.sum.tile go.sum; do [ -f "$d/$s" ] && cp "$d/$s" "$work/go.sum" && break; done
  (
    cd "$work"
    gol=$(sed -n 's/^go[[:space:]][[:space:]]*\([0-9][0-9.]*\).*/\1/p' go.mod | head -1)
    gol=$(printf '%s\n' 1.24 "${gol:-1.24}" | sort -V | tail -1)
    printf 'go %s\n\nuse .\n\nreplace github.com/xbin-dev/xbin/sdk => "%s"\n' "$gol" "$repo/sdk" > go.work
    export GOWORK="$work/go.work" GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=readonly"
    if out=$(go vet ./... 2>&1 && go test $test_flags ./... 2>&1); then
      echo "$out" | grep -v "no test files" || true
      echo "  ✓ $name"
    else
      echo "$out"
      echo "  ✗ $name"; exit 1
    fi
  )
}

# The backends are independent: TILE_CHECK_JOBS of them at once (default:
# one per CPU), each into a log of its own, printed in order.
jobs_max=${TILE_CHECK_JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)}
pids=()
for d in "${dirs[@]}"; do
  while [ "$(jobs -rp | wc -l)" -ge "$jobs_max" ]; do sleep 0.2; done
  check_one "$d" > "$scratch/$(basename "$d").log" 2>&1 &
  pids+=($!)
done
fails=0
for i in "${!dirs[@]}"; do
  wait "${pids[$i]}" || fails=$((fails + 1))
  cat "$scratch/$(basename "${dirs[$i]}").log"
done
[ "$fails" -eq 0 ] && echo "tile-check: ${#dirs[@]} backend(s) vet + test against their own go.mod.tile" || { echo "tile-check: $fails failed"; exit 1; }
