#!/usr/bin/env bash
# native/ios/scripts/ci-linux-swift.sh — Swift on a Linux CI runner (ci.yml's
# native job): the pinned swiftly, checked against its SHA-256, and the
# pinned toolchain through it — the Swift the Apple CI's Xcode carries, so
# `make swift-test` here and `swift test` there compile the same language.
# Idempotent: with the toolchain already in SWIFTLY_HOME_DIR (the job
# caches that directory, keyed on this file) it only puts it on PATH.
#
#   XBIN_SWIFT_VERSION    default 6.4.0
#   SWIFTLY_HOME_DIR      default ~/.local/share/swiftly
#
# The system packages the toolchain needs (swift.org's list for Ubuntu
# 24.04) are installed with sudo apt-get when missing and running in CI;
# elsewhere the script only names them. In Actions the toolchain's bin/
# goes onto GITHUB_PATH and SWIFTLY_HOME_DIR into GITHUB_ENV.
set -euo pipefail

SWIFTLY_VERSION=1.2.0
# sha256 of https://download.swift.org/swiftly/linux/swiftly-$SWIFTLY_VERSION-<arch>.tar.gz
SWIFTLY_SHA256_x86_64=0b26b568811404374c7d7f07276b6aad4145db5d1893a45f307de6a94ca8d9c3
SWIFTLY_SHA256_aarch64=590d855e03807791f1b84aab77471bf7e31f9e3b22151972526077da296ecf96
want=${XBIN_SWIFT_VERSION:-6.4.0}
home=${SWIFTLY_HOME_DIR:-$HOME/.local/share/swiftly}
bindir=${SWIFTLY_BIN_DIR:-$home/bin}
export SWIFTLY_HOME_DIR=$home SWIFTLY_BIN_DIR=$bindir

say() { echo "ci-linux-swift: $*"; }
ci() { [ "${GITHUB_ACTIONS:-}" = true ]; }

# swift.org's dependencies for Ubuntu 24.04 (what swiftly's post-install
# script asks for).
deps="binutils git gnupg2 libc6-dev libcurl4-openssl-dev libedit2 libgcc-13-dev libncurses-dev libpython3-dev
libsqlite3-0 libstdc++-13-dev libxml2-dev libz3-dev pkg-config tzdata unzip zip zlib1g-dev"
if command -v dpkg-query >/dev/null 2>&1 && grep -qiE '^ID(_LIKE)?=.*(ubuntu|debian)' /etc/os-release 2>/dev/null; then
  missing=""
  for p in $deps; do
    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q "install ok installed" || missing="$missing $p"
  done
  if [ -n "$missing" ]; then
    if ci; then
      say "installing:$missing"
      sudo apt-get update -qq
      # shellcheck disable=SC2086 # a list of package names
      sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends $missing
    else
      say "missing system packages (sudo apt-get install$missing)"
    fi
  fi
fi

# `swift --version` names a .0 release without its patch ("Swift version 6.4").
short=$want
case $want in *.0) short=${want%.0} ;; esac
have() {
  local v
  [ -x "$bindir/swift" ] || return 1
  v=$("$bindir/swift" --version 2>/dev/null | sed -n 's/.*Swift version \([0-9.]*\).*/\1/p' | head -n 1)
  [ "$v" = "$want" ] || [ "$v" = "$short" ]
}

if ! have; then
  arch=$(uname -m)
  case $arch in
  x86_64) sum=$SWIFTLY_SHA256_x86_64 ;;
  aarch64 | arm64) arch=aarch64 sum=$SWIFTLY_SHA256_aarch64 ;;
  *) say "no swiftly for $arch"; exit 1 ;;
  esac
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/swiftly.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  url="https://download.swift.org/swiftly/linux/swiftly-$SWIFTLY_VERSION-$arch.tar.gz"
  say "swiftly $SWIFTLY_VERSION ($url)"
  curl -fsSL --retry 3 -o "$tmp/swiftly.tar.gz" "$url"
  got=$(sha256sum "$tmp/swiftly.tar.gz" | cut -d' ' -f1)
  if [ "$got" != "$sum" ]; then
    say "swiftly-$SWIFTLY_VERSION-$arch.tar.gz: SHA-256 $got, expected $sum"
    exit 1
  fi
  tar -xzf "$tmp/swiftly.tar.gz" -C "$tmp"
  "$tmp/swiftly" init --assume-yes --no-modify-profile --skip-install --quiet-shell-followup
  # swiftly checks the toolchain's PGP signature itself (--verify, its default).
  "$bindir/swiftly" install --assume-yes --use --post-install-file "$tmp/post-install.sh" "$want"
  if [ -s "$tmp/post-install.sh" ]; then
    say "the toolchain asks for more (above list missed something):"
    cat "$tmp/post-install.sh"
    if ci; then sudo bash "$tmp/post-install.sh"; fi
  fi
fi

have || { say "swift $want did not install ($("$bindir/swift" --version 2>&1 | head -n 1))"; exit 1; }
"$bindir/swift" --version 2>&1 | head -n 1
if [ -n "${GITHUB_PATH:-}" ]; then echo "$bindir" >>"$GITHUB_PATH"; fi
if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "SWIFTLY_HOME_DIR=$home"
    echo "SWIFTLY_BIN_DIR=$bindir"
  } >>"$GITHUB_ENV"
fi
