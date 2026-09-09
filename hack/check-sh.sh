#!/usr/bin/env bash
# hack/check-sh.sh — shellcheck every shell script in the repo at warning
# level (make shellcheck). Uses a local shellcheck when present, else the
# pinned upstream container image (docker/podman), else says so and passes:
# CI installs shellcheck, so a dev box without it is never the last line.
#
#   hack/check-sh.sh            # all scripts
#   hack/check-sh.sh file…      # just these
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

SC_VERSION=v0.10.0
SC_IMAGE="docker.io/koalaman/shellcheck:$SC_VERSION"

if [ $# -gt 0 ]; then
  files=("$@")
else
  files=(deploy/*.sh hack/*.sh hack/ui-harness/*.sh .githooks/*)
fi

if command -v shellcheck >/dev/null; then
  echo "shellcheck ($(shellcheck --version | sed -n 's/^version: //p')): ${#files[*]} scripts"
  exec shellcheck -S warning -x "${files[@]}"
fi

engine=""
for e in podman docker; do
  if command -v "$e" >/dev/null; then engine=$e; break; fi
done
if [ -z "$engine" ]; then
  echo "shellcheck: SKIPPED — no shellcheck binary and no container engine (apt install shellcheck)" >&2
  exit 0
fi
echo "shellcheck ($SC_IMAGE via $engine): ${#files[*]} scripts"
exec "$engine" run --rm -v "$repo:/mnt:ro" -w /mnt "$SC_IMAGE" -S warning -x "${files[@]}"
