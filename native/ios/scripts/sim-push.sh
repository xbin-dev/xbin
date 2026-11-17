#!/usr/bin/env bash
# native/ios/scripts/sim-push.sh <simulator name> < payload.apns — on the
# Mac: hand one notification (the JSON `xcrun simctl push` takes, with its
# "Simulator Target Bundle") to the booted simulator of that name. The iOS
# UI tests' pushes (XBIN_E2E_PUSH, D241): the development relay spools what
# xbind sends — sealed — and mac-remote.sh ships each file here, so the app's
# Notification Service Extension opens a real one. Runs under macOS's bash 3.2.
set -euo pipefail
name=$1
udid=$(xcrun simctl list devices booted -j | /usr/bin/python3 -c '
import json, sys
name = sys.argv[1]
for devs in json.load(sys.stdin)["devices"].values():
    for d in devs:
        if d.get("name") == name and d.get("state") == "Booted":
            print(d["udid"]); sys.exit(0)
sys.exit(1)' "$name") || { echo "sim-push: no booted simulator named $name" >&2; exit 1; }
f=$(mktemp "${TMPDIR:-/tmp}/sim-push.XXXXXX")
trap 'rm -f "$f"' EXIT
cat >"$f"
xcrun simctl push "$udid" dev.xbin.app "$f"
