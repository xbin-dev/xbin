#!/usr/bin/env bash
# The iOS app's help screenshots ("where do I find the QR code?"): a fresh
# harness workspace, the appHelp pass (passes/apphelp.js: the settings menu
# with "add a device" ringed, the add-device panel with its QR code and the
# address field), then the two PNGs copied into
# native/ios/App/Resources/Help/. Rerun it whenever the shell's top bar,
# settings menu or device panel changes, and look at both images before
# committing them (native/AGENTS.md → "The help screenshots").
#
#   PLAYWRIGHT_DIR=… PORT=8931 hack/ui-harness/app-help-shots.sh
#
# Env as run.sh (PORT, HARNESS_DIR, PLAYWRIGHT_DIR). Don't pipe it: the
# harness leaves xbind up until the --stop below.
set -euo pipefail
H="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$H/../.." && pwd)"
export HARNESS_DIR=${HARNESS_DIR:-${TMPDIR:-/tmp}/xbin-app-help}
DEST="$REPO/native/ios/App/Resources/Help"
status=0
"$H/run.sh" --keep appHelp || status=$?
"$H/run.sh" --stop
[[ $status == 0 ]] || exit "$status"
mkdir -p "$DEST"
cp "$HARNESS_DIR/out/app-help-1-settings.png" "$DEST/help-1-settings.png"
cp "$HARNESS_DIR/out/app-help-2-add-device.png" "$DEST/help-2-add-device.png"
echo "wrote $DEST/help-1-settings.png and help-2-add-device.png — look at them before committing"
