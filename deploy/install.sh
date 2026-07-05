#!/usr/bin/env bash
# xbin — VM/host installer.
#
#   curl -fsSL https://raw.githubusercontent.com/magik6k/xbin/master/deploy/install.sh | sudo bash
#
# Installs xbind as an unprivileged systemd service on a Linux host it controls
# (see README → "Deployment on a VM" for the model). It:
#   - preflight-checks the kernel features rootless sandboxing needs,
#   - installs dependencies (uidmap, fuse3, git, and — to build — Go + podman),
#   - builds xbind, bx, a static fuse-overlayfs, and the base rootfs from source
#     (or uses prebuilt artifacts — see XBIN_PREBUILT_BIN / XBIN_ROOTFS_DIR),
#   - creates the `xbin` system user with home /opt/xbin and a delegated
#     /etc/subuid+subgid range,
#   - installs + starts the xbin.service unit, and prints your login URL.
#
# Idempotent: re-run it to upgrade in place. Flags: --check-only (just run the
# preflight checks), --yes (non-interactive), --help. Everything below is
# overridable via the environment (see "Config").
set -euo pipefail

# ---- Config (override via env) --------------------------------------------
PREFIX="${XBIN_PREFIX:-/opt/xbin}"
XBIN_USER="${XBIN_USER:-xbin}"
WORKSPACE="${XBIN_WORKSPACE:-$PREFIX/workspace}"
LISTEN="${XBIN_LISTEN:-127.0.0.1:8642}"
REPO_URL="${XBIN_REPO_URL:-https://github.com/magik6k/xbin}"
REF="${XBIN_REF:-master}"
SUBID_START="${XBIN_SUBID_START:-100000}"
SUBID_COUNT="${XBIN_SUBID_COUNT:-65536}"
GO_VERSION="${XBIN_GO_VERSION:-1.26.3}"   # go.mod requires >= this
GO_MIN="1.26.3"
BUILD_DIR="${XBIN_BUILD_DIR:-/var/tmp/xbin-build}"

# Optional: skip building by pointing at prebuilt artifacts.
XBIN_SRC="${XBIN_SRC:-}"                 # existing repo checkout
XBIN_PREBUILT_BIN="${XBIN_PREBUILT_BIN:-}"   # dir with xbind, bx, fuse-overlayfs
XBIN_ROOTFS_DIR="${XBIN_ROOTFS_DIR:-}"       # prebuilt unpacked base rootfs

ASSUME_YES="${XBIN_ASSUME_YES:-0}"
CHECK_ONLY=0

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

# ---- Args -----------------------------------------------------------------
for a in "$@"; do case "$a" in
  --check-only|--check) CHECK_ONLY=1 ;;
  --yes|-y) ASSUME_YES=1 ;;
  -h|--help) sed -n '2,26p' "$0" 2>/dev/null || true; exit 0 ;;
  *) die "unknown argument: $a" ;;
esac; done

# ---- Platform / package manager -------------------------------------------
[ "$(uname -s)" = Linux ] || die "xbin runs on Linux only"
[ "$(id -u)" = 0 ] || die "run as root:  curl -fsSL <url> | sudo bash   (or: sudo bash install.sh)"
have systemctl || die "systemd is required (systemctl not found)"

PKG=
if   have apt-get; then PKG=apt
elif have dnf;     then PKG=dnf
elif have pacman;  then PKG=pacman
elif have zypper;  then PKG=zypper
fi
pkg_install() {
  case "$PKG" in
    apt)    DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y "$@" ;;
    dnf)    dnf install -y "$@" ;;
    pacman) pacman -Sy --needed --noconfirm "$@" ;;
    zypper) zypper --non-interactive install -y "$@" ;;
    *) return 1 ;;
  esac
}
# Distro-specific package names.
case "$PKG" in
  apt)    UIDMAP_PKG=uidmap;        FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl tar";        PODMAN_PKG=podman ;;
  dnf)    UIDMAP_PKG=shadow-utils;  FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl tar";        PODMAN_PKG=podman ;;
  pacman) UIDMAP_PKG=shadow;        FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl";            PODMAN_PKG=podman ;;
  zypper) UIDMAP_PKG=shadow;        FUSE_PKG=fuse3; RUN_PKGS="git ca-certificates curl tar";        PODMAN_PKG=podman ;;
  *)      UIDMAP_PKG=; FUSE_PKG=; RUN_PKGS=; PODMAN_PKG= ;;
esac
go_arch() { case "$(uname -m)" in x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) die "unsupported arch $(uname -m)" ;; esac; }

BUILD_FROM_SOURCE=1
[ -n "$XBIN_PREBUILT_BIN" ] && [ -n "$XBIN_ROOTFS_DIR" ] && BUILD_FROM_SOURCE=0

# ---- Preflight checks -----------------------------------------------------
PREFLIGHT_FATAL=0
preflight() {
  info "Preflight checks"

  # Unprivileged user namespaces — the load-bearing one.
  local uc=/proc/sys/kernel/unprivileged_userns_clone
  local mn=/proc/sys/user/max_user_namespaces
  if [ -r "$uc" ] && [ "$(cat "$uc")" = 0 ]; then
    fail "kernel.unprivileged_userns_clone=0 — unprivileged user namespaces are DISABLED"
    warn "  enable: echo 'kernel.unprivileged_userns_clone=1' >/etc/sysctl.d/99-userns.conf && sysctl --system"
    PREFLIGHT_FATAL=1
  elif [ -r "$mn" ] && [ "$(cat "$mn")" = 0 ]; then
    fail "user.max_user_namespaces=0 — user namespaces are DISABLED"; PREFLIGHT_FATAL=1
  else
    ok "unprivileged user namespaces appear enabled (functional test runs after user creation)"
  fi

  # cgroup v2 — optional (accounting only).
  if [ -f /sys/fs/cgroup/cgroup.controllers ]; then ok "cgroup v2 present (per-component accounting)"
  else warn "cgroup v2 not found — per-component CPU/mem/pids accounting will be unavailable (non-fatal)"; fi

  # /dev/fuse — fuse-overlayfs rootfs; falls back to kernel overlay if absent.
  if [ ! -e /dev/fuse ]; then modprobe fuse 2>/dev/null || true; fi
  if [ -e /dev/fuse ]; then ok "/dev/fuse present (fuse-overlayfs; apt in terminals works)"
  else warn "/dev/fuse missing — xbind falls back to kernel overlayfs (apt installs in sandboxes may fail)"; fi

  # /dev/net/tun — egress relay.
  if [ ! -e /dev/net/tun ]; then modprobe tun 2>/dev/null || true; fi
  if [ -e /dev/net/tun ]; then ok "/dev/net/tun present (egress relay / terminal internet scope)"
  else warn "/dev/net/tun missing — no component egress or terminal internet scope until 'modprobe tun'"; fi

  # uid-mapping helpers.
  if have newuidmap && have newgidmap; then ok "newuidmap / newgidmap available (full uid-range mapping)"
  else warn "newuidmap/newgidmap not found yet — will install $UIDMAP_PKG"; fi

  if [ "$PKG" = "" ] && [ "$BUILD_FROM_SOURCE" = 1 ]; then
    warn "unrecognized package manager — install deps yourself (see README) or pass XBIN_PREBUILT_BIN/XBIN_ROOTFS_DIR"
  fi

  [ "$PREFLIGHT_FATAL" = 1 ] && die "preflight failed — fix the items above and re-run"
  ok "preflight OK"
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
  info "installing Go ${GO_VERSION} to /usr/local/go"
  rm -rf /usr/local/go
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" | tar -C /usr/local -xzf -
  export PATH=/usr/local/go/bin:$PATH
  ok "Go $(/usr/local/go/bin/go version | awk '{print $3}')"
}
ensure_engine() {
  if have podman; then ENGINE=podman
  elif have docker; then ENGINE=docker
  else info "installing podman (build tool for the base rootfs)"; pkg_install "$PODMAN_PKG" || die "could not install a container engine (podman/docker) — needed to build the rootfs"; ENGINE=podman; fi
  ok "container build engine: $ENGINE"
}
install_deps() {
  info "Installing dependencies"
  if [ -n "$PKG" ]; then
    # shellcheck disable=SC2086
    pkg_install $UIDMAP_PKG $FUSE_PKG $RUN_PKGS || warn "some packages failed to install — continuing"
  else
    warn "no known package manager; assuming uidmap/fuse3/git are already present"
  fi
  have newuidmap && ok "newuidmap present" || warn "newuidmap still missing — full-range uid mapping (apt/sudo in sandboxes) will be degraded"
  if [ "$BUILD_FROM_SOURCE" = 1 ]; then
    install_go
    ensure_engine
    have git || die "git is required to fetch the source"
  fi
}

# ---- Fetch + build --------------------------------------------------------
SRC=
fetch_source() {
  [ "$BUILD_FROM_SOURCE" = 1 ] || return 0
  if [ -n "$XBIN_SRC" ]; then SRC="$XBIN_SRC"
  elif [ -f ./go.mod ] && grep -q 'module github.com/magik6k/xbin' ./go.mod 2>/dev/null; then SRC="$(pwd)"
  else
    SRC="$BUILD_DIR/xbin"
    info "fetching source: $REPO_URL@$REF"
    if [ -d "$SRC/.git" ]; then git -C "$SRC" fetch --depth 1 origin "$REF" && git -C "$SRC" checkout -q FETCH_HEAD
    else mkdir -p "$BUILD_DIR"; git clone --depth 1 --branch "$REF" "$REPO_URL" "$SRC"; fi
  fi
  ok "source: $SRC"
}
build_artifacts() {
  [ "$BUILD_FROM_SOURCE" = 1 ] || { info "using prebuilt artifacts"; return 0; }
  export PATH=/usr/local/go/bin:$PATH
  info "Building xbind, bx, fuse-overlayfs, and the base rootfs (first run pulls toolchains — several minutes)"
  make -C "$SRC" build DOCKER="$ENGINE"
  rm -rf "$BUILD_DIR/rootfs"
  make -C "$SRC" rootfs DOCKER="$ENGINE" ROOTFS="$BUILD_DIR/rootfs"
  XBIN_PREBUILT_BIN="$SRC/bin"
  XBIN_ROOTFS_DIR="$BUILD_DIR/rootfs"
  ok "build complete"
}

# ---- System setup ---------------------------------------------------------
create_user() {
  if id "$XBIN_USER" >/dev/null 2>&1; then ok "user $XBIN_USER exists"
  else
    info "Creating system user $XBIN_USER (home $PREFIX)"
    useradd --system --create-home --home-dir "$PREFIX" --shell /bin/bash "$XBIN_USER"
    ok "created $XBIN_USER"
  fi
  install -d -m 0755 -o "$XBIN_USER" -g "$XBIN_USER" "$PREFIX"
}
setup_subids() {
  local f
  for f in /etc/subuid /etc/subgid; do
    touch "$f"
    if grep -q "^$XBIN_USER:" "$f"; then ok "$(basename "$f"): $XBIN_USER range present"
    else printf '%s:%s:%s\n' "$XBIN_USER" "$SUBID_START" "$SUBID_COUNT" >>"$f"; ok "$(basename "$f"): delegated $SUBID_START+$SUBID_COUNT to $XBIN_USER"; fi
  done
}
setup_kernel() {
  info "Kernel modules + sysctl"
  modprobe fuse 2>/dev/null || true; modprobe tun 2>/dev/null || true
  printf 'fuse\ntun\n' >/etc/modules-load.d/xbin.conf
  # Big workspaces exhaust the default inotify budget; bx doctor checks this.
  printf 'fs.inotify.max_user_watches=524288\n' >/etc/sysctl.d/99-xbin.conf
  sysctl -q -p /etc/sysctl.d/99-xbin.conf 2>/dev/null || true
  ok "fuse+tun autoload persisted; inotify watches raised"
}
test_userns() {
  if runuser -u "$XBIN_USER" -- unshare -Ur true 2>/dev/null; then
    ok "user namespaces work for $XBIN_USER"
  else
    fail "could not create a user namespace as $XBIN_USER"
    warn "  sandboxing will not come up. Check unprivileged userns are enabled and not blocked by AppArmor/seccomp."
  fi
}
install_files() {
  info "Installing to $PREFIX"
  systemctl is-active --quiet xbin 2>/dev/null && { info "stopping running xbin for upgrade"; systemctl stop xbin; }
  install -d -m 0755 "$PREFIX/bin"
  local b; for b in xbind bx fuse-overlayfs gocryptfs; do
    [ -f "$XBIN_PREBUILT_BIN/$b" ] || die "missing artifact: $XBIN_PREBUILT_BIN/$b"
    install -m 0755 "$XBIN_PREBUILT_BIN/$b" "$PREFIX/bin/$b"
  done
  info "installing base rootfs (this copies a few GB)"
  rm -rf "$PREFIX/rootfs.new"
  cp -a "$XBIN_ROOTFS_DIR" "$PREFIX/rootfs.new"
  rm -rf "$PREFIX/rootfs"; mv "$PREFIX/rootfs.new" "$PREFIX/rootfs"
  install -d -m 0755 "$WORKSPACE"
  ln -sf "$PREFIX/bin/bx" /usr/local/bin/bx
  chown -R "$XBIN_USER:$XBIN_USER" "$PREFIX"
  ok "binaries in $PREFIX/bin, rootfs in $PREFIX/rootfs, workspace $WORKSPACE"
}
configure_vault() {
  info "Vault (encrypts element secrets at rest)"
  install -d -m 0700 /etc/xbin
  local mode="${XBIN_VAULT_MODE:-}"
  if [ -z "$mode" ] && [ "$ASSUME_YES" != 1 ] && have_tty; then
    { echo "  1) auto-unseal — store a passphrase in /etc/xbin/xbin.env (hands-off restarts)"
      echo "  2) manual unseal — store nothing; an admin unseals after each boot (stronger)"; } >"$TTY"
    mode=$(ask "  choose [1/2] (default 2): " 2)
  fi
  case "${mode:-2}" in
    1|auto)
      local pass="${XBIN_VAULT_PASSPHRASE:-}"
      [ -z "$pass" ] && have_tty && pass=$(ask_secret "  vault passphrase: ")
      if [ -n "$pass" ]; then
        ( umask 077; printf 'XBIN_VAULT_PASSPHRASE=%s\n' "$pass" >/etc/xbin/xbin.env )
        chmod 600 /etc/xbin/xbin.env
        ok "auto-unseal configured (/etc/xbin/xbin.env, mode 600)"
      else warn "no passphrase — using manual unseal"; rm -f /etc/xbin/xbin.env; fi ;;
    *) rm -f /etc/xbin/xbin.env; ok "manual unseal — run 'bx vault unseal' or use the admin console after first login" ;;
  esac
}
install_unit() {
  info "Installing systemd unit"
  cat >/etc/systemd/system/xbin.service <<UNIT
# xbin workspace daemon — generated by deploy/install.sh. See README.
# Do NOT add namespace/filesystem hardening (PrivateUsers, RestrictNamespaces,
# ProtectSystem=strict, NoNewPrivileges=yes, ...) — xbind builds rootless
# sandboxes itself and those directives break it.
[Unit]
Description=xbin self-modifying workspace daemon
Documentation=https://github.com/magik6k/xbin
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$XBIN_USER
Group=$XBIN_USER
WorkingDirectory=$PREFIX
Environment=HOME=$PREFIX
Environment=PATH=$PREFIX/bin:/usr/local/go/bin:$PREFIX/rootfs/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
Environment=XBIN_ROOTFS=$PREFIX/rootfs
Environment=XBIN_FUSE_OVERLAYFS=$PREFIX/bin/fuse-overlayfs
Environment=XBIN_GOCRYPTFS=$PREFIX/bin/gocryptfs
EnvironmentFile=-/etc/xbin/xbin.env
ExecStart=$PREFIX/bin/xbind --isolate --rootfs $PREFIX/rootfs --workspace $WORKSPACE --listen $LISTEN
Restart=on-failure
RestartSec=2
# tmpfs run dir (/run/xbin) for IPC sockets — bind-mounted RW into sandboxes,
# so it must be tmpfs not host disk (plans/isolation.md).
RuntimeDirectory=xbin
RuntimeDirectoryMode=0700
Delegate=yes
LimitNOFILE=1048576
TasksMax=infinity
OOMPolicy=continue

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  ok "unit written to /etc/systemd/system/xbin.service"
}
start_service() {
  info "Starting xbin"
  systemctl enable --now xbin
  local url="http://${LISTEN}/healthz" i=0
  printf '  waiting for /healthz'
  until curl -fsS "$url" >/dev/null 2>&1; do
    i=$((i+1)); [ "$i" -ge 60 ] && { printf '\n'; fail "not healthy after 60s — see: journalctl -u xbin -n 80"; return 1; }
    printf '.'; sleep 1
  done
  printf '\n'; ok "xbind is healthy"
}
show_summary() {
  local login
  login=$(journalctl -u xbin --no-pager -o cat 2>/dev/null | grep -oE 'http://[^ ]+/login\?token=[A-Za-z0-9._-]+' | tail -1 || true)
  echo
  info "${B}xbin is installed and running.${R}"
  echo "    service : systemctl status xbin   |   logs: journalctl -u xbin -f"
  echo "    workspace: $WORKSPACE   (auto-initialized on first boot)"
  echo "    listen  : $LISTEN   (loopback — not reachable from the network yet)"
  echo
  if [ -n "$login" ]; then
    echo "  ${B}Log in:${R}  $login"
  else
    echo "  ${B}Log in:${R}  journalctl -u xbin | grep -i login   # one-time token URL"
  fi
  echo
  echo "  Reach it (never raw-expose the port):"
  echo "    • Tailscale:  tailscale serve --bg $LISTEN     (or map the port on the tailnet)"
  echo "    • TLS proxy:  point Caddy/Traefik at $LISTEN   (cookie flips to Secure behind X-Forwarded-Proto: https)"
  echo
  echo "  Upgrade: re-run this script (rebuilds + swaps in place)."
  echo "  Remove : systemctl disable --now xbin; rm /etc/systemd/system/xbin.service; userdel -r $XBIN_USER"
}

# ---- Main -----------------------------------------------------------------
echo "${B}xbin installer${R}  →  prefix=$PREFIX user=$XBIN_USER listen=$LISTEN"
[ "$BUILD_FROM_SOURCE" = 1 ] && echo "  building from source ($REPO_URL@$REF)" || echo "  using prebuilt artifacts"
preflight
[ "$CHECK_ONLY" = 1 ] && { info "check-only: done"; exit 0; }
confirm "Proceed with installation?" || die "aborted"

install_deps
fetch_source
build_artifacts
create_user
setup_subids
setup_kernel
test_userns
install_files
configure_vault
install_unit
start_service || true
show_summary
