#!/usr/bin/env bash
# hack/check-pins.sh — guard the pinned inputs that rot silently: vendored
# frontend files edited by hand, Go versions that drift apart between the
# five files that state one, an Alpine release ageing out of support (the
# 3.20 pin sat four months past EOL before anyone noticed), a Go or traefik
# tarball URL that stops being served. Run by deploy/publish-release.sh
# before every release build and by `make check` (offline part); runnable
# standalone any time.
#
#   hack/check-pins.sh [--offline] [--strict]
#     --offline   only the checks that need no network (make pins-offline)
#     --strict    warnings are fatal too
#
# Offline:
#   1. web/vendor ↔ hack/vendor.sha256 (every file listed, every hash matches)
#   2. Go versions agree: go.mod == install.sh XBIN_GO_VERSION; ci.yml's
#      go-version is go.mod's major.minor; the rootfs-baked toolchain
#      (docker/rootfs.Dockerfile) satisfies every go.mod.tile / sdk / example
#      `go` directive, since terminals build tiles with it
#   3. alpine pins agree across hack/build-*.sh and deploy/install.sh
# Online:
#   4. currency — pinned distro releases vs the endoflife.date API:
#      FAIL past EOL, WARN within 60 days (API unreachable = warn, not fail)
#   5. reachability — Alpine APKINDEX (both arches), the pinned Go tarballs
#      (installer + rootfs-baked toolchain), the traefik release tarball the
#      builtin tile's setup script downloads
# (golang:alpine, the gocryptfs builder image, floats with upstream — no
# version to rot, nothing to check.)
#
# GNU date required (date -d) — fine everywhere we build releases (Linux).
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
STRICT=0 OFFLINE=0
for a in "$@"; do
  case "$a" in
    --strict) STRICT=1 ;;
    --offline) OFFLINE=1 ;;
    -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
    *) echo "unknown flag: $a" >&2; exit 2 ;;
  esac
done

fails=0 warns=0
ok()   { printf '  \342\234\223 %s\n' "$*"; }
warn() { printf '  ! %s\n' "$*"; warns=$((warns + 1)); }
fail() { printf '  \342\234\227 %s\n' "$*"; fails=$((fails + 1)); }

head_ok() { curl -fsIL -o /dev/null --max-time 20 "$1"; }
eol_of()  { curl -fsSL --max-time 20 "https://endoflife.date/api/$1/$2.json" 2>/dev/null | sed -n 's/.*"eol":"\([0-9-]*\)".*/\1/p'; }

# semver-ish compare: vge A B → A >= B (numeric, dot-separated)
vge() {
  local a b i
  IFS=. read -r -a a <<< "$1"; IFS=. read -r -a b <<< "$2"
  for i in 0 1 2; do
    local x=${a[i]:-0} y=${b[i]:-0}
    [ "$x" -gt "$y" ] && return 0
    [ "$x" -lt "$y" ] && return 1
  done
  return 0
}

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

# --- 1. vendored frontend deps ---------------------------------------------
echo "== vendored frontend deps (web/vendor ↔ hack/vendor.sha256)"
if [ ! -f "$repo/hack/vendor.sha256" ]; then
  fail "hack/vendor.sha256 missing — run ./hack/vendor.sh"
else
  if (cd "$repo/web/vendor" && sha256sum -c --quiet --strict "$repo/hack/vendor.sha256" >/dev/null 2>&1); then
    ok "web/vendor matches hack/vendor.sha256 ($(grep -c . "$repo/hack/vendor.sha256") files)"
  else
    fail "web/vendor differs from hack/vendor.sha256 — $(cd "$repo/web/vendor" && sha256sum -c --quiet "$repo/hack/vendor.sha256" 2>&1 | tr '\n' ' ')(re-run ./hack/vendor.sh if the change is intended)"
  fi
  unlisted=""
  for f in "$repo"/web/vendor/*; do
    name=$(basename "$f")
    grep -q " $name\$" "$repo/hack/vendor.sha256" || unlisted="$unlisted $name"
  done
  [ -z "$unlisted" ] || fail "web/vendor has files not in hack/vendor.sha256:$unlisted"
fi

# --- 2. Go versions ---------------------------------------------------------
echo "== Go versions"
mod_go=$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$repo/go.mod" | head -1)
inst_go=$(sed -n 's/.*XBIN_GO_VERSION:-\([0-9.]*\).*/\1/p' "$repo/deploy/install.sh" | head -1)
ci_go=$(sed -n 's/.*go-version: *"\([0-9.]*\)".*/\1/p' "$repo/.github/workflows/ci.yml" | head -1)
rootfs_go=$(sed -n 's/^ARG GO_VERSION=\([0-9.]*\).*/\1/p' "$repo/docker/rootfs.Dockerfile" | head -1)
if [ "$mod_go" = "$inst_go" ]; then
  ok "go.mod and deploy/install.sh agree on Go $mod_go"
else
  fail "go.mod says Go $mod_go but deploy/install.sh installs ${inst_go:-?} (XBIN_GO_VERSION) — keep them equal"
fi
if [ "${mod_go%.*}" = "$ci_go" ]; then
  ok "ci.yml go-version $ci_go matches go.mod's ${mod_go%.*} (gofmt output differs across minors)"
else
  fail "ci.yml go-version is ${ci_go:-?} but go.mod is $mod_go — gofmt differs across minors; set go-version: \"${mod_go%.*}\""
fi
# The rootfs toolchain is what terminals (and agents in them) build tiles
# with: it must satisfy every shipped tile's / the sdk's `go` directive.
need=""
for f in "$repo"/sdk/go.mod "$repo"/builtin-tiles/*/go.mod.tile "$repo"/builtin-templates/*/*/go.mod.tile "$repo"/examples/*/go.mod; do
  [ -f "$f" ] || continue
  v=$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$f" | head -1)
  [ -n "$v" ] || continue
  if [ -z "$need" ] || ! vge "$need" "$v"; then need=$v; fi
done
if [ -z "$rootfs_go" ]; then
  warn "no ARG GO_VERSION in docker/rootfs.Dockerfile"
elif vge "$rootfs_go" "$need"; then
  ok "rootfs toolchain Go $rootfs_go satisfies every shipped go directive (max go $need)"
else
  fail "rootfs toolchain Go $rootfs_go is older than a shipped go directive (go $need) — tiles won't build in terminals; bump ARG GO_VERSION in docker/rootfs.Dockerfile"
fi

# --- 3. Alpine pin agreement -----------------------------------------------
echo "== pinned build inputs"
pins=$( { grep -rhoE 'alpine:[0-9]+\.[0-9]+' "$repo/hack" "$repo/deploy" 2>/dev/null || true
          sed -n 's/^ALPINE_PIN=\([0-9.]*\).*/alpine:\1/p' "$repo/deploy/install.sh"; } | sort -u)
if [ "$(printf '%s\n' "$pins" | grep -c .)" -gt 1 ]; then
  # shellcheck disable=SC2086
  fail "alpine pins disagree: $(echo $pins) — keep hack/build-*.sh and install.sh ALPINE_PIN in sync"
else
  ok "alpine pin agreed: ${pins:-none}"
fi
alp="${pins##*:}"
ub=$(sed -n 's|^FROM docker.io/library/ubuntu:\([0-9.]*\).*|\1|p' "$repo/docker/rootfs.Dockerfile" | head -1)
traefik=$(grep -oE 'V=[0-9]+\.[0-9]+\.[0-9]+' "$repo/builtin-tiles/traefik/xbin.json" | head -1 | cut -d= -f2)

if [ "$OFFLINE" = 1 ]; then
  echo "  (offline: skipping EOL and reachability checks)"
else
  # --- 4. currency ---------------------------------------------------------
  if [ -z "$alp" ]; then
    warn "no alpine pin found (grep came up empty — did the scripts move?)"
  else
    check_eol alpine-linux "$alp" "Alpine"
    for a in x86_64 aarch64; do
      url="https://dl-cdn.alpinelinux.org/alpine/v$alp/main/$a/APKINDEX.tar.gz"
      if head_ok "$url"; then ok "APKINDEX served: v$alp/$a"; else fail "APKINDEX unreachable: $url"; fi
    done
  fi
  if [ -n "$ub" ]; then check_eol ubuntu "$ub" "Ubuntu (rootfs base)"; else warn "no ubuntu pin found in docker/rootfs.Dockerfile"; fi

  # --- 5. reachability -----------------------------------------------------
  for v in $(printf '%s\n%s\n' "$inst_go" "$rootfs_go" | sort -u | grep . || true); do
    for a in amd64 arm64; do
      url="https://go.dev/dl/go${v}.linux-${a}.tar.gz"
      if head_ok "$url"; then ok "Go tarball served: $v ($a)"; else fail "Go tarball unreachable: $url"; fi
    done
  done
  if [ -n "$traefik" ]; then
    for a in amd64 arm64; do
      url="https://github.com/traefik/traefik/releases/download/v${traefik}/traefik_v${traefik}_linux_${a}.tar.gz"
      if head_ok "$url"; then ok "traefik tarball served: v$traefik ($a)"; else fail "traefik tarball unreachable (builtin-tiles/traefik setup): $url"; fi
    done
  else
    warn "no traefik version pin found in builtin-tiles/traefik/xbin.json"
  fi
fi

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
echo "OK: all pins agree and are served"
