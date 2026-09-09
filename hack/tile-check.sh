#!/usr/bin/env bash
# hack/tile-check.sh — vet and test every builtin tile backend and the agent
# template's backend the way a workspace builds them: against go.mod.tile
# (restored to go.mod in a scratch copy) with the sdk replaced by this
# checkout. `make vet` compiles them against the ROOT go.mod, which is not
# what runs; this is. Needs network on first run (each tile's own deps).
#
#   hack/tile-check.sh            # every tile (make tile-check)
#   hack/tile-check.sh devbox     # one
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

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

fails=0
for d in "${dirs[@]}"; do
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
    go mod edit -replace "github.com/xbin-dev/xbin/sdk=$repo/sdk"
    GOFLAGS=-mod=mod go mod tidy >/dev/null 2>&1 || true
    if out=$(GOFLAGS=-mod=mod go vet ./... 2>&1 && GOFLAGS=-mod=mod go test ./... 2>&1); then
      echo "$out" | grep -v "no test files" || true
      echo "  ✓ $name"
    else
      echo "$out"
      echo "  ✗ $name"; exit 1
    fi
  ) || fails=$((fails + 1))
done
[ "$fails" -eq 0 ] && echo "tile-check: ${#dirs[@]} backend(s) vet + test against their own go.mod.tile" || { echo "tile-check: $fails failed"; exit 1; }
