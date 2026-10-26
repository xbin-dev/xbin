#!/usr/bin/env bash
# native/ios/scripts/ci-pick-xcode.sh <iOS SDK version> — the installed
# Xcode whose iOS SDK is <version> (27.1: the iPhone Duo APIs, D200),
# handed to the later steps as XBIN_XCODE (GITHUB_ENV in Actions; printed
# either way). It looks at /Applications/Xcode*.app — a hosted runner's
# Xcode_27.1.app, the Mac mini's Xcode-beta.app — or XBIN_XCODE_ROOT.
# None has it: an error listing the Xcodes there.
#
#   ci-pick-xcode.sh 27.1        → /Applications/Xcode-beta.app
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

want=${1:?usage: ci-pick-xcode.sh <iOS SDK version, e.g. 27.1>}
root=${XBIN_XCODE_ROOT:-/Applications}
for x in "$root"/Xcode*.app; do
  [ -d "$x/Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs/iPhoneOS$want.sdk" ] || continue
  if [ -n "${GITHUB_ENV:-}" ]; then
    echo "XBIN_XCODE=$x" >>"$GITHUB_ENV"
  fi
  echo "$x"
  exit 0
done
ci_error "no Xcode in $root has the iOS $want SDK; installed:"
ls -d "$root"/Xcode*.app 2>/dev/null || true
exit 1
