#!/bin/sh
# xbin — macOS installer: a Lima Linux VM sized by you, xbin installed inside.
#
#   curl -fsSL https://xbin.dev/install.sh | sh                 # from a Mac
#   XBIN_VM_DISK=64GiB XBIN_VM_MEM=8GiB ... | sh -s -- --yes
#
# xbin's per-tile sandboxing is built on Linux kernel primitives (namespaces,
# seccomp, Landlock), so on macOS it runs inside a lightweight Linux VM:
#   - Lima (via Homebrew) manages the VM using Apple's Virtualization
#     framework — no Docker Desktop, no kernel extensions;
#   - you pick the VM size at install time (defaults: 32GiB disk, thin-
#     provisioned; 4GiB RAM; 4 CPUs);
#   - inside the VM the regular xbin Linux installer runs in system mode,
#     pinned to the same release as this script;
#   - the UI is forwarded to http://127.0.0.1:8642 on the Mac, loopback-only.
#
# Like the Linux installer, it prints a numbered plan of exactly what it will
# do and asks once before touching anything. Idempotent: re-run to upgrade
# (re-runs the installer inside the existing VM). Flags: --check-only, --yes,
# --help. Requires XBIN_VERSION (the bootstrap at https://xbin.dev/install.sh
# sets it to the latest release; export it yourself to pin one).
set -eu

# ---- Args -----------------------------------------------------------------
CHECK_ONLY=0
ASSUME_YES="${XBIN_ASSUME_YES:-0}"
for a in "$@"; do case "$a" in
  --check-only|--check) CHECK_ONLY=1 ;;
  --yes|-y) ASSUME_YES=1 ;;
  --system) ;; # a VM install IS system mode inside the VM
  --user) printf 'error: --user does not apply on macOS — the VM install runs xbin as a system service INSIDE the VM,\n' >&2
          printf '       which never touches your macOS account beyond the Lima VM itself.\n' >&2; exit 1 ;;
  -h|--help) sed -n '2,21p' "$0" 2>/dev/null || true; exit 0 ;;
  *) printf 'error: unknown argument: %s\n' "$a" >&2; exit 1 ;;
esac; done

# ---- Output helpers (POSIX sh; matches deploy/install.sh's voice) ---------
if [ -t 1 ]; then
  B=$(printf '\033[1m'); R=$(printf '\033[0m'); G=$(printf '\033[32m')
  Y=$(printf '\033[33m'); RED=$(printf '\033[31m'); C=$(printf '\033[36m')
else B=; R=; G=; Y=; RED=; C=; fi
info() { printf '%s==>%s %s\n' "$C" "$R" "$*"; }
ok()   { printf '  %s✓%s %s\n' "$G" "$R" "$*"; }
warn() { printf '  %s!%s %s\n' "$Y" "$R" "$*"; }
die()  { printf '%serror:%s %s\n' "$RED" "$R" "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

TTY=/dev/tty
have_tty() { [ -e "$TTY" ] && { : >>"$TTY"; } 2>/dev/null; }
ask() { # ask "prompt" "default" -> stdout
  p="$1"; def="${2:-}"; ans=
  if [ "$ASSUME_YES" = 1 ] || ! have_tty; then printf '%s' "$def"; return; fi
  printf '%s' "$p" >"$TTY"; IFS= read -r ans <"$TTY" || ans=
  if [ -n "$ans" ]; then printf '%s' "$ans"; else printf '%s' "$def"; fi
}
confirm() { # confirm "prompt" -> 0 yes / 1 no
  [ "$ASSUME_YES" = 1 ] && return 0; have_tty || return 0
  printf '%s [Y/n] ' "$1" >"$TTY"; IFS= read -r ans <"$TTY" || ans=
  case "$ans" in n|N|no|NO|No) return 1;; *) return 0;; esac
}

# ---- Preflight ------------------------------------------------------------
[ "$(uname -s)" = Darwin ] || die "this is the macOS installer — on Linux use deploy/install.sh (the https://xbin.dev/install.sh one-liner picks for you)"

VERSION="${XBIN_VERSION:-}"
[ -n "$VERSION" ] || die "XBIN_VERSION is not set — run via 'curl -fsSL https://xbin.dev/install.sh | sh' (which resolves the latest release), or pin one: XBIN_VERSION=vX.Y.Z sh $0"
INSTALL_URL="https://raw.githubusercontent.com/xbin-dev/xbin/${VERSION}/deploy/install.sh"

ARCH="$(uname -m)"
case "$ARCH" in
  arm64)  LIMA_ARCH=aarch64; IMG_ARCH=arm64 ;;
  x86_64) LIMA_ARCH=x86_64;  IMG_ARCH=amd64 ;;
  *) die "unsupported architecture: $ARCH" ;;
esac
IMG_URL="https://cloud-images.ubuntu.com/releases/26.04/release/ubuntu-26.04-server-cloudimg-${IMG_ARCH}.img"

# Apple's Virtualization framework (vz) needs macOS 13+; older Macs fall back
# to qemu (an extra brew package).
OSVER="$(sw_vers -productVersion 2>/dev/null || echo 0)"
OSMAJOR="${OSVER%%.*}"
VMTYPE=vz
NEED_QEMU=0
if [ "${OSMAJOR:-0}" -lt 13 ] 2>/dev/null; then VMTYPE=qemu; NEED_QEMU=1; fi

info "${B}xbin macOS installer${R}  →  release ${VERSION}, ${ARCH} Mac, VM runtime: ${VMTYPE}"
echo

info "Preflight checks"
if ! have brew; then
  printf '%serror:%s Homebrew is required to install Lima (the VM manager) and this script never installs Homebrew for you.\n\n' "$RED" "$R" >&2
  {
    echo "  Install Homebrew first (the official instruction, from https://brew.sh):"
    echo '    /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
    echo "  then Lima:"
    echo "    brew install lima"
    echo "  and re-run this installer."
  } >&2
  exit 1
fi
ok "Homebrew present"

NEED_LIMA=0
if have limactl; then ok "Lima present ($(limactl --version 2>/dev/null | head -1))"; else NEED_LIMA=1; warn "Lima not installed — the plan includes 'brew install lima'"; fi
if [ "$NEED_QEMU" = 1 ]; then
  if have qemu-system-"$LIMA_ARCH"; then ok "qemu present (macOS $OSVER predates the vz runtime)"; else warn "macOS $OSVER predates the vz runtime — the plan includes 'brew install qemu'"; fi
fi

UPGRADE=0
if have limactl && limactl list --format '{{.Name}}' 2>/dev/null | grep -qx xbin; then
  UPGRADE=1
  ok "existing Lima VM 'xbin' found — this run is an UPGRADE (re-runs the xbin installer inside it; VM size unchanged)"
fi

# ---- Sizing (fresh installs only) -----------------------------------------
HOST_CPUS="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"
DEF_CPUS=4; [ "$HOST_CPUS" -lt 4 ] 2>/dev/null && DEF_CPUS="$HOST_CPUS"
VM_DISK="${XBIN_VM_DISK:-32GiB}"
VM_MEM="${XBIN_VM_MEM:-4GiB}"
VM_CPUS="${XBIN_VM_CPUS:-$DEF_CPUS}"
if [ "$UPGRADE" = 0 ] && [ "$CHECK_ONLY" = 0 ]; then
  echo
  info "VM size (Enter keeps the default; disk is thin-provisioned — it grows with use, up to the size)"
  VM_DISK="$(ask "  disk  [$VM_DISK]: " "$VM_DISK")"
  VM_MEM="$(ask  "  memory [$VM_MEM]: " "$VM_MEM")"
  VM_CPUS="$(ask "  cpus   [$VM_CPUS]: " "$VM_CPUS")"
fi

# ---- Plan -----------------------------------------------------------------
echo
info "${B}This will (macOS · Lima VM):${R}"
i=0
step() { i=$((i+1)); printf '  %2d. %s\n' "$i" "$1"; }
[ "$NEED_LIMA" = 1 ] && step "install Lima: brew install lima"
[ "$NEED_QEMU" = 1 ] && ! have qemu-system-"$LIMA_ARCH" && step "install qemu: brew install qemu (macOS $OSVER has no vz runtime)"
if [ "$UPGRADE" = 1 ]; then
  step "start the existing Lima VM 'xbin' if stopped (limactl start xbin)"
  step "re-run the xbin Linux installer inside it, pinned to ${VERSION} (system mode, in-place upgrade)"
else
  step "create Lima VM 'xbin': Ubuntu 26.04 (${IMG_ARCH}), ${VMTYPE} runtime, disk ${VM_DISK} (thin), ${VM_MEM} RAM, ${VM_CPUS} cpus, no Mac directories shared into it"
  step "forward the VM's 127.0.0.1:8642 to this Mac's 127.0.0.1:8642 (loopback-only, not on your network)"
  step "inside the VM: run the xbin Linux installer in system mode pinned to ${VERSION} (builds from source — expect 10-20 minutes on first run; follow along: limactl shell xbin sudo tail -f /var/log/cloud-init-output.log)"
fi
step "wait for http://127.0.0.1:8642/healthz and print your one-time login URL"
echo "  VM lifecycle afterwards: limactl stop|start xbin · shell: limactl shell xbin · disk lives in ~/.lima/xbin · uninstall: limactl delete xbin"

if [ "$CHECK_ONLY" = 1 ]; then echo; info "check-only: no changes made"; exit 0; fi
echo
confirm "Proceed?" || die "aborted"

# ---- Execute --------------------------------------------------------------
total=$i; i=0
run() { i=$((i+1)); info "[$i/$total] $1"; }

if [ "$NEED_LIMA" = 1 ]; then
  run "brew install lima"
  brew install lima
fi
if [ "$NEED_QEMU" = 1 ] && ! have qemu-system-"$LIMA_ARCH"; then
  run "brew install qemu"
  brew install qemu
fi

if [ "$UPGRADE" = 1 ]; then
  run "starting the Lima VM 'xbin'"
  limactl start xbin --tty=false || true
  run "upgrading xbin inside the VM (pinned to ${VERSION})"
  limactl shell xbin sudo env XBIN_ASSUME_YES=1 XBIN_REF="$VERSION" \
    sh -c "curl -fsSL '$INSTALL_URL' | bash -s -- --system --yes"
else
  run "creating the Lima VM 'xbin' (${VM_DISK} disk · ${VM_MEM} RAM · ${VM_CPUS} cpus)"
  tmpl="$(mktemp /tmp/xbin-lima.XXXXXX.yaml)"
  trap 'rm -f "$tmpl"' EXIT
  cat >"$tmpl" <<EOF
# xbin Lima VM (generated by install-macos.sh for release ${VERSION})
vmType: "${VMTYPE}"
arch: "${LIMA_ARCH}"
images:
  - location: "${IMG_URL}"
    arch: "${LIMA_ARCH}"
cpus: ${VM_CPUS}
memory: "${VM_MEM}"
disk: "${VM_DISK}"
# No Mac directories are shared into the VM — the workspace lives inside.
mounts: []
containerd:
  system: false
  user: false
portForwards:
  - guestIP: "127.0.0.1"
    guestPort: 8642
    hostIP: "127.0.0.1"
    hostPort: 8642
provision:
  - mode: system
    script: |
      #!/bin/bash
      set -eu
      # Lima runs mode:system provisioning on EVERY boot — only install on
      # the first one; upgrades go through the installer's limactl path.
      [ -x /opt/xbin/bin/xbind ] && exit 0
      curl -fsSL '${INSTALL_URL}' | XBIN_ASSUME_YES=1 XBIN_REF='${VERSION}' bash -s -- --system --yes
EOF
  # First boot provisions + builds xbin from source inside the VM.
  limactl start --name=xbin --tty=false --timeout=40m "$tmpl"
fi

# Lima reports READY even when provisioning FAILED (it logs a warning and
# moves on) — verify the install actually landed before waiting on health.
if ! limactl shell xbin -- test -x /opt/xbin/bin/xbind 2>/dev/null; then
  fail "provisioning did not complete — the in-VM installer failed. Last log lines:"
  limactl shell xbin -- sudo tail -n 40 /var/log/cloud-init-output.log 2>/dev/null || true
  echo
  echo "  full log : limactl shell xbin -- sudo less /var/log/cloud-init-output.log"
  echo "  retry    : limactl shell xbin -- sudo bash -c \"curl -fsSL ${INSTALL_URL} | XBIN_ASSUME_YES=1 XBIN_REF=${VERSION} bash -s -- --system --yes\""
  echo "  clean up : limactl delete xbin   (then re-run this installer)"
  exit 1
fi

run "waiting for xbin on http://127.0.0.1:8642"
n=0
until curl -fsS http://127.0.0.1:8642/healthz >/dev/null 2>&1; do
  n=$((n+1))
  if [ "$n" -ge 120 ]; then
    echo
    fail "xbin did not become healthy — recent service log from the VM:"
    limactl shell xbin -- sudo journalctl -u xbin -n 40 --no-pager 2>/dev/null || true
    echo "  follow : limactl shell xbin -- sudo journalctl -u xbin -f"
    exit 1
  fi
  printf '.'; sleep 2
done
echo

LOGIN="$(limactl shell xbin sudo journalctl -u xbin --no-pager -o cat 2>/dev/null | grep -oE 'http://[^ ]+/login\?token=[A-Za-z0-9._-]+' | tail -1 || true)"
echo
info "${B}xbin is installed in the 'xbin' Lima VM.${R}"
echo "    UI      : http://127.0.0.1:8642  (forwarded from the VM; loopback-only)"
if [ -n "$LOGIN" ]; then
  echo "  ${B}Log in:${R}  $LOGIN"
else
  echo "  ${B}Log in:${R}  limactl shell xbin sudo journalctl -u xbin | grep -i login   # one-time token URL"
fi
echo
echo "  VM lifecycle:  limactl stop xbin · limactl start xbin · limactl shell xbin"
echo "  Disk lives in  ~/.lima/xbin (thin — grows with use, up to ${VM_DISK})"
echo "  Upgrade      : re-run this installer (re-runs the xbin installer inside the VM)"
echo "  Uninstall    : limactl delete xbin   (removes the VM and everything in it)"
