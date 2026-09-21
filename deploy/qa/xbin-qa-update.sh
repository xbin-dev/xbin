#!/usr/bin/env bash
# xbin-qa-update.sh — the QA box's auto-updater (deploy/qa).
#
# Polls GitHub for the newest xbin release tag; when it is newer than the
# installed xbind, re-runs the official installer to fetch that tagged prebuilt
# bundle, then restarts xbind. A systemd timer runs this every couple of
# minutes (see xbin-qa-update.timer); flock keeps runs from overlapping. It
# writes /run/xbin-qa/state throughout so the front proxy (xbin-qa-proxy) can
# show an "Updating…" page with the target version.
#
# Env (all optional): XBIN_QA_REPO (default xbin-dev/xbin), XBIN_QA_STATE,
# XBIN_QA_XBIND, XBIN_QA_HEALTH, XBIN_QA_ENV (the daemon env file).
set -euo pipefail

REPO="${XBIN_QA_REPO:-xbin-dev/xbin}"
STATE="${XBIN_QA_STATE:-/run/xbin-qa/state}"
XBIND="${XBIN_QA_XBIND:-/opt/xbin/bin/xbind}"
HEALTH="${XBIN_QA_HEALTH:-http://127.0.0.1:8642/healthz}"
ENV_FILE="${XBIN_QA_ENV:-/etc/xbin/xbin.env}"

log() { echo "xbin-qa-update: $*"; }
set_state() { mkdir -p "$(dirname "$STATE")"; printf '%s\n' "$*" >"$STATE"; }
clear_state() { rm -f "$STATE"; }

# One run at a time — the timer must never stack updates.
mkdir -p "$(dirname "$STATE")"
exec 9>"$(dirname "$STATE")/update.lock"
flock -n 9 || { log "another update is already running"; exit 0; }

latest_tag() {
  curl -fsSL "https://api.github.com/repos/$REPO/tags" \
    | grep -oE '"name": *"v[0-9]+\.[0-9]+\.[0-9]+"' \
    | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | sort -V | tail -1
}
current_ver() { "$XBIND" version 2>/dev/null | awk '{print $2}'; }
# Preserve the vault passphrase across the reinstall (the installer rewrites the
# env file); pass it back so auto-unseal keeps working after the restart.
vault_pass() { sed -n 's/^XBIN_VAULT_PASSPHRASE=//p' "$ENV_FILE" 2>/dev/null | tail -1; }

latest="$(latest_tag || true)"
[ -n "$latest" ] || { log "could not resolve the latest release tag"; exit 0; }
current="$(current_ver || true)"
log "installed=${current:-none} latest=$latest"

if [ "$current" = "$latest" ]; then
  log "already up to date"; exit 0
fi
# Only move forward: if the box is somehow ahead of the newest tag, do nothing.
newest="$(printf '%s\n%s\n' "${current:-v0.0.0}" "$latest" | sort -V | tail -1)"
[ "$newest" = "$latest" ] || { log "installed ($current) is newer than $latest — skipping"; exit 0; }

log "updating $current -> $latest"
set_state "Updating to $latest — downloading…"
pass="$(vault_pass)"
if XBIN_VERSION="$latest" XBIN_VAULT_MODE=auto XBIN_VAULT_PASSPHRASE="$pass" \
     bash -c 'curl -fsSL https://xbin.dev/install.sh | bash -s -- --yes --system'; then
  set_state "Updating to $latest — restarting…"
  systemctl restart xbin || true
  for _ in $(seq 1 90); do
    curl -fsS -o /dev/null "$HEALTH" && break
    sleep 1
  done
  log "now running $(current_ver)"
else
  log "install failed — staying on $current"
fi
clear_state
