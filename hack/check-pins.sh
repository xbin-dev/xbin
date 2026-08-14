#!/usr/bin/env bash
# hack/check-pins.sh — guard the REMOTE inputs our build scripts pin, which
# rot silently: an Alpine release ages out of support (the 3.20 pin sat four
# months past EOL before anyone noticed), a Go tarball URL stops being served,
# pins drift apart between scripts. Run by deploy/publish-release.sh before
# every release build; runnable standalone any time.
#
#   hack/check-pins.sh [--strict]     # --strict: warnings are fatal too
#
# Checks:
#   1. drift — every alpine pin in hack/ + deploy/install.sh ALPINE_PIN agrees
#   2. currency — pinned distro releases vs the endoflife.date API:
#      FAIL past EOL, WARN within 60 days (API unreachable = warn, not fail)
#   3. reachability — Alpine APKINDEX (both arches) and the pinned Go
#      tarballs (installer + rootfs-baked toolchain) answer a HEAD request
# (golang:alpine, the gocryptfs builder image, floats with upstream — no
# version to rot, nothing to check.)
#
# GNU date required (date -d) — fine everywhere we build releases (Linux).
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
STRICT=0
[ "${1:-}" = --strict ] && STRICT=1

fails=0 warns=0
ok()   { printf '  \342\234\223 %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*"; warns=$((warns + 1)); }
fail() { printf '  \342\234\227 %s\n' "$*"; fails=$((fails + 1)); }

head_ok() { curl -fsIL -o /dev/null --max-time 20 "$1"; }
eol_of()  { curl -fsSL --max-time 20 "https://endoflife.date/api/$1/$2.json" 2>/dev/null | sed -n 's/.*"eol":"\([0-9-]*\)".*/\1/p'; }

check_eol() { # product version label
  local eol days
  eol=$(eol_of "$1" "$2")
  if [ -z "$eol" ]; then
    warn "$3 $2: no EOL data (endoflife.date unreachable, or unknown release)"
    return
  fi
  days=$(( ($(date -d "$eol" +%s) - $(date +%s)) / 86400 ))
  if [ "$days" -lt 0 ]; then
    fail "$3 $2 went EOL on $eol ($((-days)) days ago) — bump the pin"
  elif [ "$days" -lt 60 ]; then
    warn "$3 $2 reaches EOL on $eol — $days days left; plan the bump"
  else
    ok "$3 $2 supported until $eol"
  fi
}

echo "== pinned build inputs"

# --- Alpine: static-binary build containers + the installer's egress probe --
pins=$( { grep -rhoE 'alpine:[0-9]+\.[0-9]+' "$repo/hack" "$repo/deploy" 2>/dev/null || true
          sed -n 's/^ALPINE_PIN=\([0-9.]*\).*/alpine:\1/p' "$repo/deploy/install.sh"; } | sort -u)
if [ "$(printf '%s\n' "$pins" | grep -c .)" -gt 1 ]; then
  # shellcheck disable=SC2086
  fail "alpine pins disagree: $(echo $pins) — keep hack/build-*.sh and install.sh ALPINE_PIN in sync"
fi
alp="${pins##*:}"
if [ -z "$alp" ]; then
  warn "no alpine pin found (grep came up empty — did the scripts move?)"
else
  check_eol alpine-linux "$alp" "Alpine"
  for a in x86_64 aarch64; do
    url="https://dl-cdn.alpinelinux.org/alpine/v$alp/main/$a/APKINDEX.tar.gz"
    if head_ok "$url"; then ok "APKINDEX served: v$alp/$a"; else fail "APKINDEX unreachable: $url"; fi
  done
fi

# --- Ubuntu: the rootfs base image ------------------------------------------
ub=$(sed -n 's|^FROM docker.io/library/ubuntu:\([0-9.]*\).*|\1|p' "$repo/docker/rootfs.Dockerfile" | head -1)
if [ -n "$ub" ]; then check_eol ubuntu "$ub" "Ubuntu (rootfs base)"; else warn "no ubuntu pin found in docker/rootfs.Dockerfile"; fi

# --- Go tarballs: installer toolchain + the rootfs-baked toolchain ----------
inst_go=$(sed -n 's/.*XBIN_GO_VERSION:-\([0-9.]*\).*/\1/p' "$repo/deploy/install.sh" | head -1)
rootfs_go=$(sed -n 's/^ARG GO_VERSION=\([0-9.]*\).*/\1/p' "$repo/docker/rootfs.Dockerfile" | head -1)
for v in $(printf '%s\n%s\n' "$inst_go" "$rootfs_go" | sort -u | grep . || true); do
  for a in amd64 arm64; do
    url="https://go.dev/dl/go${v}.linux-${a}.tar.gz"
    if head_ok "$url"; then ok "Go tarball served: $v ($a)"; else fail "Go tarball unreachable: $url"; fi
  done
done

echo
if [ "$fails" -gt 0 ]; then
  echo "FAIL: $fails hard failure(s)$([ "$warns" -gt 0 ] && printf ', %s warning(s)' "$warns")"
  exit 1
fi
if [ "$warns" -gt 0 ]; then
  echo "OK with $warns warning(s)$([ "$STRICT" = 1 ] && echo ' — fatal under --strict')"
  [ "$STRICT" = 1 ] && exit 1
  exit 0
fi
echo "OK: all pins current and served"
