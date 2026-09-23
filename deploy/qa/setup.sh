#!/usr/bin/env bash
# setup.sh — stand up the auto-updating xbin QA box (deploy/qa). Run as root on
# a fresh Ubuntu VPS, from a directory that also holds the prebuilt linux
# xbin-qa-proxy binary (built on the dev box: GOOS=linux GOARCH=amd64 go build
# -o xbin-qa-proxy ./deploy/qa/proxy) plus this directory's *.sh / *.service /
# *.timer files.
#
# It: (1) makes rootless user namespaces work (fresh Ubuntu restricts them),
# (2) installs the latest xbin release via the official installer with vault
# auto-unseal (hands-off restarts), (3) installs the front proxy on
# 127.0.0.1:9988 and the release-polling updater timer. Everything binds to
# localhost — reach it through an SSH port-forward to 9988. Idempotent.
set -euo pipefail

[ "$(id -u)" = 0 ] || { echo "run as root (sudo bash setup.sh)"; exit 1; }
SRC="$(cd "$(dirname "$0")" && pwd)"
QA_DIR=/opt/xbin-qa
ENV_FILE=/etc/xbin/xbin.env

say() { printf '\n== %s\n' "$*"; }

say "enabling rootless user namespaces (needed by xbind --isolate)"
install -d /etc/sysctl.d
cat >/etc/sysctl.d/99-xbin.conf <<'EOF'
kernel.apparmor_restrict_unprivileged_userns=0
kernel.unprivileged_userns_clone=1
EOF
sysctl --system >/dev/null 2>&1 || true

say "installing the latest xbin release (prebuilt bundle, vault auto-unseal)"
install -d -m 0700 /etc/xbin
# Reuse an existing passphrase (keeps an existing vault openable); else generate.
PASS="$(sed -n 's/^XBIN_VAULT_PASSPHRASE=//p' "$ENV_FILE" 2>/dev/null | tail -1 || true)"
[ -n "$PASS" ] || PASS="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | base64 | tr -dc a-f0-9 | head -c 48)"
XBIN_VAULT_MODE=auto XBIN_VAULT_PASSPHRASE="$PASS" \
  bash -c 'curl -fsSL https://xbin.dev/install.sh | bash -s -- --yes --system --prebuilt-rootfs'

say "installing the QA front proxy + auto-updater"
install -d "$QA_DIR"
[ -f "$SRC/xbin-qa-proxy" ] || { echo "missing $SRC/xbin-qa-proxy (build it: GOOS=linux GOARCH=amd64 go build -o xbin-qa-proxy ./deploy/qa/proxy)"; exit 1; }
install -m 0755 "$SRC/xbin-qa-proxy"      "$QA_DIR/xbin-qa-proxy"
install -m 0755 "$SRC/xbin-qa-update.sh"  "$QA_DIR/xbin-qa-update.sh"
install -m 0644 "$SRC/xbin-qa-proxy.service"  /etc/systemd/system/xbin-qa-proxy.service
install -m 0644 "$SRC/xbin-qa-update.service" /etc/systemd/system/xbin-qa-update.service
install -m 0644 "$SRC/xbin-qa-update.timer"   /etc/systemd/system/xbin-qa-update.timer
systemctl daemon-reload
systemctl enable --now xbin-qa-proxy.service
systemctl enable --now xbin-qa-update.timer

say "waiting for xbind to answer /healthz"
for _ in $(seq 1 60); do curl -fsS -o /dev/null http://127.0.0.1:8642/healthz && break; sleep 1; done

TOKEN="$(cat /opt/xbin/workspace/.xbin/token 2>/dev/null || true)"
cat <<EOF

== done. xbind :8642, proxy :9988 (both localhost only); update timer armed.

From your laptop:
  ssh -L 9988:localhost:9988 ubuntu@<this-host>
then open:
  http://localhost:9988/login?token=${TOKEN:-<see: sudo cat /opt/xbin/workspace/.xbin/token>}

Status:  systemctl status xbin xbin-qa-proxy xbin-qa-update.timer
Updater: journalctl -u xbin-qa-update -f   (polls releases every ~2 min)
EOF
