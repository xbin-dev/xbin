#!/usr/bin/env bash
# hack/ci-apt.sh — install the named Debian packages a CI job needs (ci.yml),
# only those the runner image lacks: an image that has them all skips the
# apt-get update and install (8–18 s on every job otherwise).
#
#   hack/ci-apt.sh attr libcap2-bin fuse3 shellcheck zstd
set -euo pipefail
need=()
for p in "$@"; do
  if ! dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed'; then
    need+=("$p")
  fi
done
if [ ${#need[@]} -eq 0 ]; then
  echo "ci-apt: the image has them all: $*"
  exit 0
fi
echo "ci-apt: installing ${need[*]}"
sudo apt-get update
sudo apt-get install -y "${need[@]}"
