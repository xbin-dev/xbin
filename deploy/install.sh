#!/usr/bin/env bash
# xbin — VM/host installer.
#
#   system-wide:  curl -fsSL https://xbin.dev/install.sh | sudo bash
#   user-only:    curl -fsSL https://xbin.dev/install.sh | bash -s -- --user
#
# Two modes:
#   --system  (default as root)     system service, dedicated `xbin` user,
#                                   /opt/xbin, subuid delegation, packages
#                                   installed via your distro's manager.
#   --user    (default as non-root) no root anywhere: runs as YOU, installs to
#                                   ~/.local/opt/xbin, a systemd *user* unit
#                                   (+ linger so it survives logout). Missing
#                                   distro packages or subuid ranges are
#                                   reported with the exact root command to
#                                   run, then the installer stops untouched.
#
# Before touching anything it prints a numbered plan of exactly what it will
# do on THIS run (steps already in place are listed as such) and asks once.
# Idempotent: re-run to upgrade in place. Flags: --check-only (preflight +
# plan, no changes), --yes (skip the prompt), --help. Everything below is
# overridable via the environment (see "Config").
set -euo pipefail

# ---- Args -----------------------------------------------------------------
MODE=""
CHECK_ONLY=0
ASSUME_YES="${XBIN_ASSUME_YES:-0}"
for a in "$@"; do case "$a" in
  --system) MODE=system ;;
  --user) MODE=user ;;
  --check-only|--check) CHECK_ONLY=1 ;;
  --yes|-y) ASSUME_YES=1 ;;
  -h|--help) sed -n '2,22p' "$0" 2>/dev/null || true; exit 0 ;;
  *) printf 'error: unknown argument: %s\n' "$a" >&2; exit 1 ;;
esac; done

# ---- Output helpers -------------------------------------------------------
if [ -t 1 ]; then B=$'\e[1m'; R=$'\e[0m'; G=$'\e[32m'; Y=$'\e[33m'; RED=$'\e[31m'; C=$'\e[36m'
else B=; R=; G=; Y=; RED=; C=; fi
info() { printf '%s==>%s %s\n' "$C" "$R" "$*"; }
ok()   { printf '  %s✓%s %s\n' "$G" "$R" "$*"; }
warn() { printf '  %s!%s %s\n' "$Y" "$R" "$*"; }
fail() { printf '  %s✗%s %s\n' "$RED" "$R" "$*"; }
die()  { printf '%serror:%s %s\n' "$RED" "$R" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

TTY=/dev/tty
have_tty() { [ -e "$TTY" ] && { : >>"$TTY"; } 2>/dev/null; }
ask() { # ask "prompt" "default" -> stdout
  local p="$1" def="${2:-}" ans=
  if [ "$ASSUME_YES" = 1 ] || ! have_tty; then printf '%s' "$def"; return; fi
  printf '%s' "$p" >"$TTY"; IFS= read -r ans <"$TTY" || ans=
  [ -n "$ans" ] && printf '%s' "$ans" || printf '%s' "$def"
}
ask_secret() { local p="$1" ans=; printf '%s' "$p" >"$TTY"; IFS= read -rs ans <"$TTY" || ans=; printf '\n' >"$TTY"; printf '%s' "$ans"; }
confirm() { # confirm "prompt" -> 0 yes / 1 no
  local ans; [ "$ASSUME_YES" = 1 ] && return 0; have_tty || return 0
  printf '%s [Y/n] ' "$1" >"$TTY"; IFS= read -r ans <"$TTY" || ans=
  case "$ans" in n|N|no|NO|No) return 1;; *) return 0;; esac
}

# ---- Platform / mode ------------------------------------------------------
case "$(uname -s)" in
  Linux) ;;
  Darwin)
    printf '%serror:%s xbin cannot run on macOS.\n\n' "$RED" "$R" >&2
    {
      echo "  xbin's per-tile sandboxing is built directly on Linux kernel primitives"
      echo "  (user/mount/pid/net namespaces, seccomp, Landlock). macOS has no"
      echo "  equivalent, and xbin has no non-sandboxed mode — a Linux server or VM"
      echo "  is required. Easy paths from this Mac:"
      echo "    • a local Linux VM (UTM / Lima / OrbStack — an Ubuntu 24.04 VM works well)"
      echo "    • any cloud or home server"
      echo "  Install there, then open the UI from this Mac over Tailscale or an SSH tunnel."
    } >&2
    exit 1 ;;
  *)
    die "unsupported OS: $(uname -s) — xbin's sandboxing needs a Linux kernel (namespaces, seccomp, Landlock). Install on a Linux server or VM." ;;
esac

EUID_NOW="$(id -u)"
if [ -z "$MODE" ]; then
  if [ "$EUID_NOW" = 0 ]; then MODE=system; else MODE=user; fi
fi
if [ "$MODE" = system ] && [ "$EUID_NOW" != 0 ]; then
  die "--system needs root:  curl -fsSL <url> | sudo bash   (or: sudo bash install.sh --system). For a no-root install under your own account, use --user."
fi
if [ "$MODE" = user ] && [ "$EUID_NOW" = 0 ]; then
  die "--user as root is a confusion trap (a root-owned \"user\" install). Run it as your normal account, or use --system for the system-wide service."
fi

have systemctl || die "systemd is required (systemctl not found)"
# systemctl existing isn't enough — systemd must be PID 1 (WSL2 ships systemd
# disabled by default; enable it rather than fail on every systemctl call).
[ -d /run/systemd/system ] || die "systemd is installed but not running as init. On WSL2: add '[boot]' + 'systemd=true' to /etc/wsl.conf, then 'wsl --shutdown' and reopen."

# sc: the mode's systemctl; user units live in the caller's systemd manager.
sc() { if [ "$MODE" = user ]; then systemctl --user "$@"; else systemctl "$@"; fi }

# ---- Config (override via env) --------------------------------------------
RUN_USER="$(id -un)"
if [ "$MODE" = system ]; then
  PREFIX="${XBIN_PREFIX:-/opt/xbin}"
  XBIN_USER="${XBIN_USER:-xbin}"
  BUILD_DIR="${XBIN_BUILD_DIR:-/var/tmp/xbin-build}"
  UNIT_PATH=/etc/systemd/system/xbin.service
  ENV_FILE=/etc/xbin/xbin.env
  BX_LINK=/usr/local/bin/bx
  GO_PREFIX=/usr/local
  JOURNAL_HINT="journalctl -u xbin"
else
  PREFIX="${XBIN_PREFIX:-$HOME/.local/opt/xbin}"
  XBIN_USER="$RUN_USER"
  BUILD_DIR="${XBIN_BUILD_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/xbin-build}"
  UNIT_PATH="$HOME/.config/systemd/user/xbin.service"
  ENV_FILE="$HOME/.config/xbin/xbin.env"
  BX_LINK="$HOME/.local/bin/bx"
  GO_PREFIX="$HOME/.local"
  JOURNAL_HINT="journalctl --user -u xbin"
fi
WORKSPACE="${XBIN_WORKSPACE:-$PREFIX/workspace}"
LISTEN="${XBIN_LISTEN:-127.0.0.1:8642}"
REPO_URL="${XBIN_REPO_URL:-https://github.com/xbin-dev/xbin}"
REF="${XBIN_REF:-master}"
SUBID_START="${XBIN_SUBID_START:-100000}"
SUBID_COUNT="${XBIN_SUBID_COUNT:-65536}"
GO_VERSION="${XBIN_GO_VERSION:-1.26.3}"   # go.mod requires >= this
GO_MIN="1.26.3"

# Optional: skip building by pointing at prebuilt artifacts.
XBIN_SRC="${XBIN_SRC:-}"                 # existing repo checkout
XBIN_PREBUILT_BIN="${XBIN_PREBUILT_BIN:-}"   # dir with xbind, bx, fuse-overlayfs
XBIN_ROOTFS_DIR="${XBIN_ROOTFS_DIR:-}"       # prebuilt unpacked base rootfs

# ---- Package manager ------------------------------------------------------
PKG=
if   have apt-get; then PKG=apt
elif have dnf;     then PKG=dnf
elif have yum;     then PKG=yum     # RHEL-likes without dnf (Amazon Linux 2, CentOS 7)
elif have pacman;  then PKG=pacman
elif have zypper;  then PKG=zypper
fi
pkg_install() {
  case "$PKG" in
    apt)    DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" ;;
    dnf)    dnf install -y "$@" ;;
    yum)    yum install -y "$@" ;;
    pacman) pacman -Sy --needed --noconfirm "$@" ;;
    zypper) zypper --non-interactive install "$@" ;;   # --non-interactive answers yes; `install -y` is not portable across zypper versions
    *) return 1 ;;
  esac
}
# pkg_install_hint <pkgs...>: the exact root command for this distro.
pkg_install_hint() {
  case "$PKG" in
    apt)    echo "sudo apt-get install -y $*" ;;
    dnf)    echo "sudo dnf install -y $*" ;;
    yum)    echo "sudo yum install -y $*" ;;
    pacman) echo "sudo pacman -Sy --needed $*" ;;
    zypper) echo "sudo zypper install $*" ;;
    *)      echo "install with your package manager: $*" ;;
  esac
}
# Distro-specific package names.
case "$PKG" in
  apt)     UIDMAP_PKG=uidmap;        FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl tar";        PODMAN_PKG=podman ;;
  dnf|yum) UIDMAP_PKG=shadow-utils;  FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl tar";        PODMAN_PKG=podman ;;
  pacman)  UIDMAP_PKG=shadow;        FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl";            PODMAN_PKG=podman ;;
  zypper)  UIDMAP_PKG=shadow;        FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl tar";        PODMAN_PKG=podman ;;
  *)       UIDMAP_PKG=; FUSE_PKG=; RUN_PKGS=; PODMAN_PKG= ;;
esac
go_arch() { case "$(uname -m)" in x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) die "unsupported arch $(uname -m)" ;; esac; }

BUILD_FROM_SOURCE=1
[ -n "$XBIN_PREBUILT_BIN" ] && [ -n "$XBIN_ROOTFS_DIR" ] && BUILD_FROM_SOURCE=0

# ---- Preflight checks -----------------------------------------------------
PREFLIGHT_FATAL=0
preflight() {
  info "Preflight checks ($MODE mode)"

  # Unprivileged user namespaces — the load-bearing one.
  local uc=/proc/sys/kernel/unprivileged_userns_clone
  local mn=/proc/sys/user/max_user_namespaces
  local ar=/proc/sys/kernel/apparmor_restrict_unprivileged_userns
  if [ -r "$uc" ] && [ "$(cat "$uc")" = 0 ]; then
    fail "kernel.unprivileged_userns_clone=0 — unprivileged user namespaces are DISABLED"
    warn "  enable (root): echo 'kernel.unprivileged_userns_clone=1' >/etc/sysctl.d/99-userns.conf && sysctl --system"
    PREFLIGHT_FATAL=1
  elif [ -r "$mn" ] && [ "$(cat "$mn")" = 0 ]; then
    fail "user.max_user_namespaces=0 — user namespaces are DISABLED"; PREFLIGHT_FATAL=1
  elif [ -r "$ar" ] && [ "$(cat "$ar")" = 1 ]; then
    if [ "$MODE" = system ]; then
      warn "AppArmor restricts unprivileged user namespaces (Ubuntu 23.10+) — installer will lift kernel.apparmor_restrict_unprivileged_userns"
    else
      fail "AppArmor restricts unprivileged user namespaces (Ubuntu 23.10+) — a user install can't lift this"
      warn "  fix (root): echo 'kernel.apparmor_restrict_unprivileged_userns=0' | sudo tee /etc/sysctl.d/99-xbin.conf && sudo sysctl --system"
      PREFLIGHT_FATAL=1
    fi
  else
    ok "unprivileged user namespaces appear enabled (functional test runs before install)"
  fi

  # cgroup v2 — optional (accounting only).
  if [ -f /sys/fs/cgroup/cgroup.controllers ]; then ok "cgroup v2 present (per-component accounting)"
  else warn "cgroup v2 not found — per-component CPU/mem/pids accounting will be unavailable (non-fatal)"; fi

  # /dev/fuse — fuse-overlayfs rootfs; falls back to kernel overlay if absent.
  if [ ! -e /dev/fuse ] && [ "$MODE" = system ]; then modprobe fuse 2>/dev/null || true; fi
  if [ -e /dev/fuse ]; then ok "/dev/fuse present (fuse-overlayfs; apt in terminals works)"
  elif [ "$MODE" = user ]; then warn "/dev/fuse missing — ask root for 'modprobe fuse' (falls back to kernel overlayfs; apt installs in sandboxes may fail)"
  else warn "/dev/fuse missing — xbind falls back to kernel overlayfs (apt installs in sandboxes may fail)"; fi

  # /dev/net/tun — egress relay.
  if [ ! -e /dev/net/tun ] && [ "$MODE" = system ]; then modprobe tun 2>/dev/null || true; fi
  if [ -e /dev/net/tun ]; then ok "/dev/net/tun present (egress relay / terminal internet scope)"
  elif [ "$MODE" = user ]; then warn "/dev/net/tun missing — ask root for 'modprobe tun' (no component egress until then)"
  else warn "/dev/net/tun missing — no component egress or terminal internet scope until 'modprobe tun'"; fi

  if [ "$MODE" = user ]; then
    preflight_user
  else
    # uid-mapping helpers (root installs them later if missing).
    if have newuidmap && have newgidmap; then ok "newuidmap / newgidmap available (full uid-range mapping)"
    else warn "newuidmap/newgidmap not found yet — will install $UIDMAP_PKG"; fi
    if [ "$PKG" = "" ] && [ "$BUILD_FROM_SOURCE" = 1 ]; then
      warn "unrecognized package manager — install deps yourself (see README) or pass XBIN_PREBUILT_BIN/XBIN_ROOTFS_DIR"
    fi
  fi

  [ "$PREFLIGHT_FATAL" = 1 ] && die "preflight failed — fix the items above and re-run"
  ok "preflight OK"
}

# preflight_user: a --user install can't escalate, so anything that needs root
# is CHECKED here and reported with the exact root command — nothing has been
# touched yet, so aborting is safe.
preflight_user() {
  # The user's systemd manager must be reachable (ssh sessions without a
  # session bus can't manage --user units).
  if systemctl --user show-environment >/dev/null 2>&1; then
    ok "systemd user manager reachable (systemctl --user)"
  else
    fail "systemd user manager not reachable — user units need a session bus"
    warn "  over ssh, enable lingering first (root): sudo loginctl enable-linger $RUN_USER — then log in again"
    PREFLIGHT_FATAL=1
  fi

  # Distro packages can't be installed without root: check the binaries and
  # report one exact install command for whatever is missing.
  local missing_bins=() missing_pkgs=()
  need() { # bin pkg
    have "$1" && return 0
    missing_bins+=("$1"); [ -n "$2" ] && missing_pkgs+=("$2"); return 0
  }
  need newuidmap "$UIDMAP_PKG"
  need newgidmap ""            # same package as newuidmap
  need fusermount3 "$FUSE_PKG"
  need git git
  need curl curl
  need tar tar
  if [ "$BUILD_FROM_SOURCE" = 1 ]; then
    need make make
    if ! have podman && ! have docker; then missing_bins+=("podman"); missing_pkgs+=("$PODMAN_PKG"); fi
  fi
  if [ ${#missing_bins[@]} -gt 0 ]; then
    # shellcheck disable=SC2086
    local uniq; uniq=$(printf '%s\n' ${missing_pkgs[*]:-} | awk 'NF && !seen[$0]++' | tr '\n' ' ')
    fail "missing tools (a user install can't add packages): ${missing_bins[*]}"
    warn "  run once as root:  $(pkg_install_hint ${uniq})"
    PREFLIGHT_FATAL=1
  else
    ok "required tools present (newuidmap, fusermount3, git, curl, tar$([ "$BUILD_FROM_SOURCE" = 1 ] && echo ", make, podman/docker"))"
  fi

  # Subordinate ids for THIS user (sandbox uid ranges + rootless podman). Most
  # distros pre-provision them for normal users.
  local f missing_sub=0
  for f in /etc/subuid /etc/subgid; do
    if grep -Eq "^($RUN_USER|$(id -u)):" "$f" 2>/dev/null; then
      ok "$(basename "$f"): range present for $RUN_USER"
    else
      fail "$(basename "$f"): no range for $RUN_USER"
      missing_sub=1
    fi
  done
  if [ "$missing_sub" = 1 ]; then
    warn "  run once as root:  sudo usermod --add-subuids ${SUBID_START}-$((SUBID_START+SUBID_COUNT-1)) --add-subgids ${SUBID_START}-$((SUBID_START+SUBID_COUNT-1)) $RUN_USER"
    PREFLIGHT_FATAL=1
  fi

  # inotify budget: can't be raised without root; xbind runs, bx doctor nags.
  local watches; watches=$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null || echo 0)
  if [ "$watches" -lt 65536 ]; then
    warn "fs.inotify.max_user_watches=$watches is low — recommended (root): echo 'fs.inotify.max_user_watches=524288' | sudo tee /etc/sysctl.d/99-xbin.conf && sudo sysctl --system"
  fi
}

# ---- Dependencies ---------------------------------------------------------
go_ok() { # true if a `go` >= $GO_MIN is on PATH
  have go || return 1
  local v; v=$(go env GOVERSION 2>/dev/null | sed 's/^go//') || return 1
  [ -n "$v" ] || return 1
  printf '%s\n%s\n' "$GO_MIN" "$v" | sort -V -C
}
install_go() {
  go_ok && { ok "Go $(go env GOVERSION | sed 's/^go//') already satisfies >= $GO_MIN"; return; }
  local arch; arch=$(go_arch)
  info "installing Go ${GO_VERSION} to $GO_PREFIX/go"
  rm -rf "$GO_PREFIX/go"
  mkdir -p "$GO_PREFIX"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" | tar -C "$GO_PREFIX" -xzf -
  export PATH="$GO_PREFIX/go/bin:$PATH"
  ok "Go $("$GO_PREFIX/go/bin/go" version | awk '{print $3}')"
}
ensure_engine() {
  if have podman; then ENGINE=podman
  elif have docker; then ENGINE=docker
  elif [ "$MODE" = system ]; then
    info "installing podman (build tool for the base rootfs)"
    pkg_install "$PODMAN_PKG" || die "could not install a container engine (podman/docker) — needed to build the rootfs"
    ENGINE=podman
  else
    die "no container engine — preflight should have caught this ($(pkg_install_hint "$PODMAN_PKG"))"
  fi
  ok "container build engine: $ENGINE"
}
install_deps() { # system mode only (user mode verified everything in preflight)
  if [ -n "$PKG" ]; then
    # shellcheck disable=SC2086
    pkg_install $UIDMAP_PKG $FUSE_PKG $RUN_PKGS || warn "some packages failed to install — continuing"
  else
    warn "no known package manager; assuming uidmap/fuse3/git are already present"
  fi
  have newuidmap && ok "newuidmap present" || warn "newuidmap still missing — full-range uid mapping (apt/sudo in sandboxes) will be degraded"
  if [ "$BUILD_FROM_SOURCE" = 1 ]; then
    have make || pkg_install make || warn "could not install make automatically"
    have make || die "make is required to build from source (install it and re-run)"
  fi
}

# ---- Fetch + build --------------------------------------------------------
SRC=
SRC_KIND=   # env | cwd | clone
resolve_source() { # read-only: decide where source comes from (for the plan)
  [ "$BUILD_FROM_SOURCE" = 1 ] || return 0
  if [ -n "$XBIN_SRC" ]; then SRC="$XBIN_SRC"; SRC_KIND=env
  elif [ -f ./go.mod ] && grep -q 'module github.com/xbin-dev/xbin' ./go.mod 2>/dev/null; then SRC="$(pwd)"; SRC_KIND=cwd
  else SRC="$BUILD_DIR/xbin"; SRC_KIND=clone; fi
}
fetch_source() {
  [ "$BUILD_FROM_SOURCE" = 1 ] || return 0
  if [ "$SRC_KIND" = clone ]; then
    if [ -d "$SRC/.git" ]; then git -C "$SRC" fetch --depth 1 origin "$REF" && git -C "$SRC" checkout -q FETCH_HEAD
    else mkdir -p "$BUILD_DIR"; git clone --depth 1 --branch "$REF" "$REPO_URL" "$SRC"; fi
  fi
  ok "source: $SRC"
}
build_artifacts() {
  [ "$BUILD_FROM_SOURCE" = 1 ] || { info "using prebuilt artifacts"; return 0; }
  export PATH="$GO_PREFIX/go/bin:$PATH"
  make -C "$SRC" build DOCKER="$ENGINE"
  rm -rf "$BUILD_DIR/rootfs"
  make -C "$SRC" rootfs DOCKER="$ENGINE" ROOTFS="$BUILD_DIR/rootfs"
  XBIN_PREBUILT_BIN="$SRC/bin"
  XBIN_ROOTFS_DIR="$BUILD_DIR/rootfs"
  XBIN_SDK_SRC="$SRC/sdk"
  ok "build complete"
}

# ---- System setup ---------------------------------------------------------
create_user() { # system mode
  if id "$XBIN_USER" >/dev/null 2>&1; then ok "user $XBIN_USER exists"
  else
    useradd --system --create-home --home-dir "$PREFIX" --shell /bin/bash "$XBIN_USER"
    ok "created $XBIN_USER"
  fi
  install -d -m 0755 -o "$XBIN_USER" -g "$XBIN_USER" "$PREFIX"
}
setup_subids() { # system mode (user mode: verified in preflight)
  local f
  for f in /etc/subuid /etc/subgid; do
    touch "$f"
    if grep -q "^$XBIN_USER:" "$f"; then ok "$(basename "$f"): $XBIN_USER range present"
    else printf '%s:%s:%s\n' "$XBIN_USER" "$SUBID_START" "$SUBID_COUNT" >>"$f"; ok "$(basename "$f"): delegated $SUBID_START+$SUBID_COUNT to $XBIN_USER"; fi
  done
}
setup_kernel() { # system mode
  modprobe fuse 2>/dev/null || true; modprobe tun 2>/dev/null || true
  printf 'fuse\ntun\n' >/etc/modules-load.d/xbin.conf
  # Big workspaces exhaust the default inotify budget; bx doctor checks this.
  printf 'fs.inotify.max_user_watches=524288\n' >/etc/sysctl.d/99-xbin.conf
  # Ubuntu 23.10+/24.04 gate unprivileged user namespaces behind an AppArmor
  # knob (kernel.apparmor_restrict_unprivileged_userns=1). With it on, even a
  # correctly-subuid'd user can't create a userns and NO sandbox comes up. This
  # host is dedicated to running sandboxed workspaces, so lift the restriction.
  local userns_lifted=
  if [ -e /proc/sys/kernel/apparmor_restrict_unprivileged_userns ]; then
    printf 'kernel.apparmor_restrict_unprivileged_userns=0\n' >>/etc/sysctl.d/99-xbin.conf
    userns_lifted=1
  fi
  sysctl -q -p /etc/sysctl.d/99-xbin.conf 2>/dev/null || true
  if [ -n "$userns_lifted" ]; then
    ok "fuse+tun autoload persisted; inotify raised; AppArmor userns restriction lifted"
  else
    ok "fuse+tun autoload persisted; inotify watches raised"
  fi
}
test_userns() {
  local ok_ns=0
  if [ "$MODE" = system ]; then
    runuser -u "$XBIN_USER" -- unshare -Ur true 2>/dev/null && ok_ns=1
  else
    unshare -Ur true 2>/dev/null && ok_ns=1
  fi
  if [ "$ok_ns" = 1 ]; then
    ok "user namespaces work for $XBIN_USER"
  else
    fail "could not create a user namespace as $XBIN_USER"
    warn "  sandboxing will not come up. Common causes:"
    warn "    kernel.apparmor_restrict_unprivileged_userns=1 (Ubuntu 23.10+)"
    warn "    kernel.unprivileged_userns_clone=0, or AppArmor/seccomp policy blocking unshare"
  fi
}
install_files() {
  sc is-active --quiet xbin 2>/dev/null && { info "stopping running xbin for upgrade"; sc stop xbin; }
  install -d -m 0755 "$PREFIX" "$PREFIX/bin"
  local b; for b in xbind bx fuse-overlayfs gocryptfs; do
    [ -f "$XBIN_PREBUILT_BIN/$b" ] || die "missing artifact: $XBIN_PREBUILT_BIN/$b"
    install -m 0755 "$XBIN_PREBUILT_BIN/$b" "$PREFIX/bin/$b"
  done
  info "installing base rootfs (this copies a few GB)"
  rm -rf "$PREFIX/rootfs.new"
  cp -a "$XBIN_ROOTFS_DIR" "$PREFIX/rootfs.new"
  # Preserve the current base as rootfs-<version> when the version changes, so
  # terminals still pinned to it keep resolving — xbind refuses to stack a
  # terminal's overlay on a different base (plans/component-env.md). Unreferenced
  # preserved bases are GC'd by xbind once every terminal has upgraded.
  if [ -d "$PREFIX/rootfs" ]; then
    oldver=$(cat "$PREFIX/rootfs/etc/xbin-base-version" 2>/dev/null || echo v0)
    newver=$(cat "$PREFIX/rootfs.new/etc/xbin-base-version" 2>/dev/null || echo v0)
    if [ "$oldver" != "$newver" ] && [ ! -e "$PREFIX/rootfs-$oldver" ]; then
      info "preserving old base image as rootfs-$oldver (for terminals not yet upgraded)"
      mv "$PREFIX/rootfs" "$PREFIX/rootfs-$oldver"
    else
      rm -rf "$PREFIX/rootfs"
    fi
  fi
  mv "$PREFIX/rootfs.new" "$PREFIX/rootfs"
  # The Go SDK source: xbind's generated go.work resolves
  # github.com/xbin-dev/xbin/sdk here (deps.SDKPath → $PREFIX/sdk), and
  # terminals get it as a read-only bind so `go build` works offline.
  if [ -n "${XBIN_SDK_SRC:-}" ] && [ -d "$XBIN_SDK_SRC" ]; then
    rm -rf "$PREFIX/sdk.new"
    cp -a "$XBIN_SDK_SRC" "$PREFIX/sdk.new"
    rm -rf "$PREFIX/sdk"; mv "$PREFIX/sdk.new" "$PREFIX/sdk"
  elif [ ! -d "$PREFIX/sdk" ]; then
    warn "no SDK source staged — Go component builds will try the network for the sdk module"
  fi
  install -d -m 0755 "$WORKSPACE"
  mkdir -p "$(dirname "$BX_LINK")"
  ln -sf "$PREFIX/bin/bx" "$BX_LINK"
  if [ "$MODE" = user ]; then
    case ":$PATH:" in *":$(dirname "$BX_LINK"):"*) ;; *) warn "$(dirname "$BX_LINK") is not on your PATH — add it to use bx" ;; esac
  else
    chown -R "$XBIN_USER:$XBIN_USER" "$PREFIX"
  fi
  ok "binaries in $PREFIX/bin, rootfs in $PREFIX/rootfs, sdk in $PREFIX/sdk, workspace $WORKSPACE"
}
configure_vault() {
  install -d -m 0700 "$(dirname "$ENV_FILE")"
  local mode="${XBIN_VAULT_MODE:-}"
  if [ -z "$mode" ] && [ "$ASSUME_YES" != 1 ] && have_tty; then
    { echo "  1) auto-unseal — store a passphrase in $ENV_FILE (hands-off restarts)"
      echo "  2) manual unseal — store nothing; an admin unseals after each boot (stronger)"; } >"$TTY"
    mode=$(ask "  choose [1/2] (default 2): " 2)
  fi
  case "${mode:-2}" in
    1|auto)
      local pass="${XBIN_VAULT_PASSPHRASE:-}"
      [ -z "$pass" ] && have_tty && pass=$(ask_secret "  vault passphrase: ")
      if [ -n "$pass" ]; then
        ( umask 077; printf 'XBIN_VAULT_PASSPHRASE=%s\n' "$pass" >"$ENV_FILE" )
        chmod 600 "$ENV_FILE"
        ok "auto-unseal configured ($ENV_FILE, mode 600)"
      else warn "no passphrase — using manual unseal"; rm -f "$ENV_FILE"; fi ;;
    *) rm -f "$ENV_FILE"; ok "manual unseal — run 'bx vault unseal' or use the admin console after first login" ;;
  esac
}
install_unit() {
  local tmp
  tmp=$(mktemp)
  mkdir -p "$(dirname "$UNIT_PATH")"
  {
    cat <<UNIT
# xbin workspace daemon — generated by deploy/install.sh ($MODE mode). See README.
# Do NOT add namespace/filesystem hardening (PrivateUsers, RestrictNamespaces,
# ProtectSystem=strict, NoNewPrivileges=yes, ...) — xbind builds rootless
# sandboxes itself and those directives break it.
[Unit]
Description=xbin self-modifying workspace daemon
Documentation=https://github.com/xbin-dev/xbin
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
UNIT
    if [ "$MODE" = system ]; then
      cat <<UNIT
User=$XBIN_USER
Group=$XBIN_USER
Environment=HOME=$PREFIX
UNIT
    fi
    cat <<UNIT
WorkingDirectory=$PREFIX
Environment=PATH=$PREFIX/bin:$GO_PREFIX/go/bin:$PREFIX/rootfs/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
Environment=XBIN_ROOTFS=$PREFIX/rootfs
Environment=XBIN_FUSE_OVERLAYFS=$PREFIX/bin/fuse-overlayfs
Environment=XBIN_GOCRYPTFS=$PREFIX/bin/gocryptfs
Environment=XBIN_SDK_PATH=$PREFIX/sdk
EnvironmentFile=-$ENV_FILE
ExecStart=$PREFIX/bin/xbind --isolate --rootfs $PREFIX/rootfs --workspace $WORKSPACE --listen $LISTEN
Restart=on-failure
# A workspace spawns sandboxed children (tiles, rootless podman) in a delegated
# cgroup subtree; give it time to drain before the new instance attaches, or the
# immediate restart can fail with 219/CGROUP ("cgroup busy") and flap.
RestartSec=15
# tmpfs run dir for IPC sockets — bind-mounted RW into sandboxes, so it must be
# tmpfs not host disk (plans/isolation.md).
RuntimeDirectory=xbin
RuntimeDirectoryMode=0700
Delegate=yes
LimitNOFILE=1048576
TasksMax=infinity
OOMPolicy=continue

[Install]
UNIT
    if [ "$MODE" = system ]; then echo "WantedBy=multi-user.target"; else echo "WantedBy=default.target"; fi
  } >"$tmp"
  if [ -f "$UNIT_PATH" ] && ! cmp -s "$tmp" "$UNIT_PATH"; then
    cp -a "$UNIT_PATH" "$UNIT_PATH.bak"
    info "previous unit differed — saved to $UNIT_PATH.bak"
  fi
  mv "$tmp" "$UNIT_PATH"; chmod 644 "$UNIT_PATH"
  # SELinux (Fedora/RHEL): mv from /tmp keeps tmp_t on the unit, and the rootfs
  # cp -a preserved /var/tmp contexts — restore proper labels where enforcing.
  if have getenforce && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
    info "SELinux enforcing — restoring file contexts (can take a moment on the rootfs)"
    restorecon "$UNIT_PATH" 2>/dev/null || true
    restorecon -R "$PREFIX" 2>/dev/null || true
  fi
  sc daemon-reload
  ok "unit written to $UNIT_PATH"
}
enable_linger() { # user mode: keep the user manager (and xbind) alive after logout
  if loginctl show-user "$RUN_USER" 2>/dev/null | grep -q '^Linger=yes'; then
    ok "lingering already enabled for $RUN_USER"
    return 0
  fi
  if loginctl enable-linger "$RUN_USER" 2>/dev/null; then
    ok "lingering enabled — xbind keeps running after you log out"
  else
    warn "could not enable lingering without root — xbind stops when your last session ends"
    warn "  run once as root:  sudo loginctl enable-linger $RUN_USER"
  fi
}
install_needrestart_dropin() { # system mode
  # needrestart (Debian/Ubuntu; the thing unattended-upgrades runs) auto-restarts
  # every service whose libraries a security update replaced. Restarting xbind
  # mid-run seals the vault — and, because it manages a delegated cgroup subtree
  # of sandboxes/containers, the immediate restart can fail 219/CGROUP and flap.
  # Exclude it: upgrades still INSTALL, but you restart xbind (and unseal) on your
  # own schedule with `systemctl restart xbin`. Only relevant where needrestart is
  # (or may be) present — skip other distros.
  if [ "$PKG" != apt ] && ! have needrestart && [ ! -d /etc/needrestart ]; then
    return 0
  fi
  install -d -m 0755 /etc/needrestart/conf.d
  cat >/etc/needrestart/conf.d/xbin.conf <<'NR'
# Managed by deploy/install.sh. Keep OS security updates installing, but do NOT
# let needrestart / unattended-upgrades auto-restart the xbin daemon — a mid-run
# restart seals the vault (and can leave a busy sandbox cgroup). Restart it
# yourself after patch days:  sudo systemctl restart xbin
$nrconf{override_rc}{qr(^xbin\.service$)} = 0;
NR
  ok "needrestart: xbin.service excluded from auto-restart (/etc/needrestart/conf.d/xbin.conf)"
}
start_service() {
  sc enable --now xbin || { fail "could not enable/start the unit — see: $JOURNAL_HINT -n 80"; return 0; }
  local url="http://${LISTEN}/healthz" i=0
  printf '  waiting for /healthz'
  until curl -fsS "$url" >/dev/null 2>&1; do
    i=$((i+1)); [ "$i" -ge 60 ] && { printf '\n'; fail "not healthy after 60s — see: $JOURNAL_HINT -n 80"; return 0; }
    printf '.'; sleep 1
  done
  printf '\n'; ok "xbind is healthy"
}
show_summary() {
  local login jc
  if [ "$MODE" = user ]; then jc="journalctl --user -u xbin"; else jc="journalctl -u xbin"; fi
  login=$($jc --no-pager -o cat 2>/dev/null | grep -oE 'http://[^ ]+/login\?token=[A-Za-z0-9._-]+' | tail -1 || true)
  echo
  info "${B}xbin is installed and running ($MODE mode).${R}"
  if [ "$MODE" = user ]; then
    echo "    service : systemctl --user status xbin   |   logs: $jc -f"
  else
    echo "    service : systemctl status xbin   |   logs: $jc -f"
  fi
  echo "    workspace: $WORKSPACE   (auto-initialized on first boot)"
  echo "    listen  : $LISTEN   (loopback — not reachable from the network yet)"
  echo
  if [ -n "$login" ]; then
    echo "  ${B}Log in:${R}  $login"
  else
    echo "  ${B}Log in:${R}  $jc | grep -i login   # one-time token URL"
  fi
  echo
  echo "  Reach it (never raw-expose the port):"
  echo "    • Tailscale:  tailscale serve --bg $LISTEN     (or map the port on the tailnet)"
  echo "    • TLS proxy:  point Caddy/Traefik at $LISTEN   (cookie flips to Secure behind X-Forwarded-Proto: https)"
  echo
  echo "  Upgrade: re-run this script (rebuilds + swaps in place)."
  if [ "$MODE" = user ]; then
    echo "  Remove : systemctl --user disable --now xbin; rm $UNIT_PATH; rm -rf $PREFIX"
  else
    echo "  Updates: OS security updates install but won't auto-restart xbind (that would seal the vault)."
    echo "           After patch days, restart on your schedule:  sudo systemctl restart xbin"
    echo "  Remove : systemctl disable --now xbin; rm $UNIT_PATH; userdel -r $XBIN_USER"
  fi
}

# ---- Plan (what THIS run will do) -----------------------------------------
# Every mutating step is declared here with a description built from the SAME
# variables execution uses; run_plan executes exactly this list, numbered.
STEPS=()     # "func::description"
INPLACE=()   # "already-done description"
plan()    { STEPS+=("$1::$2"); }
inplace() { INPLACE+=("$1"); }

build_plan() {
  # Toolchain.
  if [ "$BUILD_FROM_SOURCE" = 1 ]; then
    if [ "$MODE" = system ]; then
      if [ "$UPGRADE" = 0 ]; then
        plan install_deps "install packages via $PKG: $UIDMAP_PKG $FUSE_PKG $RUN_PKGS (+make if missing)"
      fi
      if go_ok; then inplace "Go $(go env GOVERSION 2>/dev/null | sed 's/^go//') ≥ $GO_MIN"
      else plan install_go "install Go $GO_VERSION to $GO_PREFIX/go"; fi
      if have podman || have docker; then
        plan ensure_engine "use container build engine: $(have podman && echo podman || echo docker)"
      else
        plan ensure_engine "install $PODMAN_PKG and use it as the container build engine"
      fi
    else
      # user mode: distro tools were verified in preflight; only Go may install.
      inplace "distro tools verified (no root needed from here on)"
      if go_ok; then inplace "Go $(go env GOVERSION 2>/dev/null | sed 's/^go//') ≥ $GO_MIN"
      else plan install_go "install Go $GO_VERSION to $GO_PREFIX/go (no root: user-dir tarball)"; fi
      plan ensure_engine "use container build engine: rootless $(have podman && echo podman || echo docker)"
    fi
    case "$SRC_KIND" in
      env) inplace "source checkout: $SRC (XBIN_SRC)"; plan fetch_source "use source at $SRC" ;;
      cwd) inplace "source checkout: $SRC (current directory)"; plan fetch_source "use source at $SRC" ;;
      clone) plan fetch_source "fetch $REPO_URL@$REF into $SRC" ;;
    esac
    plan build_artifacts "build xbind, bx, fuse-overlayfs, gocryptfs + the base rootfs (podman/docker; several minutes on first run)"
  else
    inplace "prebuilt artifacts: $XBIN_PREBUILT_BIN + $XBIN_ROOTFS_DIR (no build)"
  fi

  # One-time system setup.
  if [ "$MODE" = system ]; then
    if [ "$UPGRADE" = 0 ]; then
      if id "$XBIN_USER" >/dev/null 2>&1; then inplace "system user $XBIN_USER"
      else plan create_user "create system user $XBIN_USER (home $PREFIX)"; fi
      if grep -q "^$XBIN_USER:" /etc/subuid 2>/dev/null && grep -q "^$XBIN_USER:" /etc/subgid 2>/dev/null; then
        inplace "subuid/subgid range for $XBIN_USER"
      else
        plan setup_subids "delegate subuid/subgid $SUBID_START+$SUBID_COUNT to $XBIN_USER (/etc/subuid, /etc/subgid)"
      fi
    fi
    plan setup_kernel "persist fuse+tun autoload + raise inotify watches$([ -e /proc/sys/kernel/apparmor_restrict_unprivileged_userns ] && echo ' + lift the AppArmor userns restriction') (/etc/modules-load.d, /etc/sysctl.d)"
  else
    inplace "runs as $RUN_USER — no system user, no /etc changes"
  fi
  plan test_userns "verify a user namespace comes up for $XBIN_USER"

  # Files + service.
  plan install_files "install binaries → $PREFIX/bin, base rootfs → $PREFIX/rootfs (copies a few GB), SDK → $PREFIX/sdk; workspace dir $WORKSPACE$([ -x "$PREFIX/bin/xbind" ] && echo ' (upgrade in place: stop service, swap, old base preserved if version changes)'); link bx → $BX_LINK"
  if [ "$UPGRADE" = 0 ]; then
    plan configure_vault "choose vault unseal mode (auto → passphrase stored in $ENV_FILE mode 600, or manual unseal each boot)"
  else
    inplace "vault configuration"
  fi
  plan install_unit "write systemd unit $UNIT_PATH + daemon-reload$([ -f "$UNIT_PATH" ] && echo ' (existing unit backed up on change)')"
  if [ "$MODE" = user ]; then
    plan enable_linger "enable lingering for $RUN_USER (service survives logout; prints the sudo command if refused)"
  fi
  if [ "$MODE" = system ] && { [ "$PKG" = apt ] || have needrestart || [ -d /etc/needrestart ]; }; then
    plan install_needrestart_dropin "exclude xbin.service from needrestart auto-restart (/etc/needrestart/conf.d/xbin.conf)"
  fi
  plan start_service "enable + start the unit, wait for /healthz on $LISTEN"
}

print_plan() {
  echo
  local i=0 s
  info "${B}This will ($MODE mode):${R}"
  for s in "${STEPS[@]}"; do
    i=$((i+1))
    printf '  %2d. %s\n' "$i" "${s#*::}"
  done
  if [ ${#INPLACE[@]} -gt 0 ]; then
    local d
    echo "  already in place (skipped this run):"
    for d in "${INPLACE[@]}"; do printf '      · %s\n' "$d"; done
  fi
  echo "  then: print the one-time login URL ($JOURNAL_HINT)"
}

run_plan() {
  local total=${#STEPS[@]} i=0 s
  for s in "${STEPS[@]}"; do
    i=$((i+1))
    info "[$i/$total] ${s#*::}"
    "${s%%::*}"
  done
}

# ---- Main -----------------------------------------------------------------
# Upgrade mode: an installed xbind + unit means the one-time setup (user,
# subids, vault choice, dependency packages) is done — just rebuild and swap.
# XBIN_FULL_INSTALL=1 forces the full path (e.g. to re-run vault config).
UPGRADE=0
if [ "${XBIN_FULL_INSTALL:-0}" != 1 ] && [ -x "$PREFIX/bin/xbind" ] && [ -f "$UNIT_PATH" ]; then
  UPGRADE=1
fi

echo "${B}xbin installer${R}  →  mode=$MODE prefix=$PREFIX user=$XBIN_USER listen=$LISTEN"
[ "$BUILD_FROM_SOURCE" = 1 ] && echo "  building from source ($REPO_URL@$REF)" || echo "  using prebuilt artifacts"
[ "$UPGRADE" = 1 ] && echo "  existing install detected → ${B}upgrade${R} (rebuild + swap binaries/rootfs/sdk/unit, restart; user, subids, vault, and workspace untouched — XBIN_FULL_INSTALL=1 forces the full path)"
preflight
resolve_source
build_plan
print_plan
if [ "$CHECK_ONLY" = 1 ]; then echo; info "check-only: no changes made"; exit 0; fi
echo
confirm "Proceed?" || die "aborted"
run_plan
show_summary
