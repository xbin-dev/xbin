#!/usr/bin/env bash
# native/ios/scripts/release-build.sh — archive the app for the App Store and
# export it (or upload it to TestFlight), signed by Xcode's automatic signing
# with an App Store Connect API key: the distribution certificate is Apple's
# cloud-managed one, so no distribution private key is made on this Mac.
# Scaffolding — nothing has been signed with it yet (native/AGENTS.md →
# "Mac mini" → "Signing and releases").
#
# Who and where: the standard user `release` (XBIN_RELEASE_USER), by hand
# over ssh, from its own clean checkout. Never as the CI user (XBIN_CI_USER,
# default ci), never an admin, never from a pull_request- or fork-triggered
# workflow; in Actions at all only with XBIN_RELEASE_FROM_ACTIONS=1 (a
# protected, manually dispatched workflow on a runner of this user — none
# exists).
#
#   release-build.sh --team T --key-id K --issuer I [--upload] [--version V]
#                    [--build N] [--key P] [--out DIR] [--allow-dirty] [--dry-run]
#
#   --team T        the Apple Developer team id (XBIN_TEAM_ID)
#   --key-id K      the API key's id (XBIN_ASC_KEY_ID)
#   --issuer I      the API keys' issuer id, a UUID (XBIN_ASC_ISSUER_ID)
#   --key P         the key (XBIN_ASC_KEY), default
#                   ~/.appstoreconnect/private_keys/AuthKey_<K>.p8: a file this
#                   user owns, mode 600 (or 400), outside any git checkout
#   --upload        export straight to App Store Connect (TestFlight) instead
#                   of an .ipa here
#   --version V     MARKETING_VERSION (default: project.yml's)
#   --build N       CURRENT_PROJECT_VERSION (default: the UTC time, YYYYMMDDHHMM)
#   --out DIR       default ~/xbin-release/<build>-<commit>, mode 700
#   --allow-dirty   build a tree with uncommitted changes (never for a real release)
#   --dry-run       the checks, then the commands it would run (nothing built)
#
# XBIN_XCODE picks the Xcode (else the selected one). The login keychain
# must be unlocked (over ssh: security unlock-keychain): the archive is
# signed with the user's development identity before the export re-signs it
# for distribution.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$here/ci-lib.sh"

team=${XBIN_TEAM_ID:-} kid=${XBIN_ASC_KEY_ID:-} issuer=${XBIN_ASC_ISSUER_ID:-} key=${XBIN_ASC_KEY:-}
upload=0 version="" build="" out="" allow_dirty=0 dry=0
while [ $# -gt 0 ]; do
  case $1 in
  --team) team=${2:?--team needs a value}; shift 2 ;;
  --key-id) kid=${2:?--key-id needs a value}; shift 2 ;;
  --issuer) issuer=${2:?--issuer needs a value}; shift 2 ;;
  --key) key=${2:?--key needs a path}; shift 2 ;;
  --upload) upload=1; shift ;;
  --version) version=${2:?}; shift 2 ;;
  --build) build=${2:?}; shift 2 ;;
  --out) out=${2:?}; shift 2 ;;
  --allow-dirty) allow_dirty=1; shift ;;
  --dry-run) dry=1; shift ;;
  -h | --help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
  *) echo "release-build: unknown argument $1 (--help)" >&2; exit 2 ;;
  esac
done

refuse() { echo "release-build: $*" >&2; exit 2; }
ci_user=${XBIN_CI_USER:-ci}
release_user=${XBIN_RELEASE_USER:-release}
me=$(id -un)

# ---- who, and from where -------------------------------------------------
[ "$(uname -s)" = Darwin ] || refuse "this runs on the Mac (uname: $(uname -s))"
[ "$me" != "$ci_user" ] || refuse "never as the CI user $ci_user: the runner runs code from any pushed branch, and the release key must be out of its reach"
[ "$me" = "$release_user" ] || refuse "run this as $release_user (the standard user that owns the App Store Connect key), not $me"
if dsmemberutil checkmembership -U "$me" -G admin 2>/dev/null | grep -q 'is a member'; then
  refuse "$me is an admin: the release user must be a standard user (System Settings → Users & Groups)"
fi
if [ -n "${GITHUB_ACTIONS:-}" ] || [ -n "${RUNNER_NAME:-}" ]; then
  case ${GITHUB_EVENT_NAME:-} in
  pull_request* | issue_comment | workflow_run)
    refuse "never from a ${GITHUB_EVENT_NAME} workflow: pull requests (forks included) must never reach a release key" ;;
  esac
  [ -z "${GITHUB_HEAD_REF:-}" ] || refuse "never from a pull request's workflow (GITHUB_HEAD_REF=${GITHUB_HEAD_REF})"
  if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ] &&
    python3 -c 'import json,sys; e=json.load(open(sys.argv[1])); sys.exit(0 if (e.get("repository") or {}).get("fork") or ((e.get("pull_request") or {}).get("head") or {}).get("repo", {}).get("fork") else 1)' \
      "$GITHUB_EVENT_PATH" 2>/dev/null; then
    refuse "never from a fork's workflow"
  fi
  [ "${XBIN_RELEASE_FROM_ACTIONS:-0}" = 1 ] ||
    refuse "not in a workflow: releases run by hand as $release_user (XBIN_RELEASE_FROM_ACTIONS=1 only for a protected, manually dispatched one)"
fi

# ---- the key and its ids -------------------------------------------------
printf '%s' "$team" | grep -Eq '^[A-Z0-9]{10}$' || refuse "--team: the 10-character team id (developer.apple.com → Membership details), got '${team}'"
printf '%s' "$kid" | grep -Eq '^[A-Z0-9]{10}$' || refuse "--key-id: the API key's 10-character id, got '${kid}'"
printf '%s' "$issuer" | grep -Eqi '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' ||
  refuse "--issuer: the issuer id (a UUID, above the keys in App Store Connect → Users and Access → Integrations), got '${issuer}'"
key=${key:-$HOME/.appstoreconnect/private_keys/AuthKey_$kid.p8}
if [ ! -f "$key" ] || [ -L "$key" ]; then
  refuse "no key file at $key (a regular file, not a link — native/AGENTS.md → Mac mini says how to put it there)"
fi
file_owner() { stat -c %U "$1" 2>/dev/null || stat -f %Su "$1"; }
file_mode() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
[ "$(file_owner "$key")" = "$me" ] || refuse "$key belongs to $(file_owner "$key"), not $me"
case $(file_mode "$key") in
600 | 400) ;;
*) refuse "$key is mode $(file_mode "$key"): chmod 600 (only $me may read it)" ;;
esac
keydir=$(cd "$(dirname "$key")" && pwd)
case $(file_mode "$keydir") in
700 | 500) ;;
*) ci_warn "$keydir is mode $(file_mode "$keydir"): chmod 700 keeps other users from even listing it" ;;
esac
if git -C "$keydir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  refuse "$key is inside a git checkout ($(git -C "$keydir" rev-parse --show-toplevel)): keep it in ~/.appstoreconnect/private_keys"
fi

# ---- the tree ------------------------------------------------------------
cd "$XBIN_REPO"
commit=$(git rev-parse --short=12 HEAD 2>/dev/null) || refuse "$XBIN_REPO is not a git checkout"
if [ -n "$(git status --porcelain 2>/dev/null)" ] && [ "$allow_dirty" = 0 ]; then
  refuse "the tree has uncommitted changes: release from a clean checkout of a tag (--allow-dirty for a test build)"
fi
build=${build:-$(date -u +%Y%m%d%H%M)}
printf '%s' "$build" | grep -Eq '^[0-9]+(\.[0-9]+){0,2}$' || refuse "--build: digits and dots, got '$build'"
[ -z "$version" ] || printf '%s' "$version" | grep -Eq '^[0-9]+(\.[0-9]+){0,2}$' || refuse "--version: like 1.2.3, got '$version'"
out=${out:-$HOME/xbin-release/$build-$commit}

if [ -n "${XBIN_XCODE:-}" ]; then export DEVELOPER_DIR=${XBIN_XCODE%/}/Contents/Developer; fi
xcodebuild -version >/dev/null 2>&1 || refuse "no working xcodebuild (XBIN_XCODE, or xcode-select)"

auth=(-allowProvisioningUpdates -authenticationKeyPath "$key" -authenticationKeyID "$kid" -authenticationKeyIssuerID "$issuer")
settings=(DEVELOPMENT_TEAM="$team" CODE_SIGN_STYLE=Automatic CURRENT_PROJECT_VERSION="$build")
[ -z "$version" ] || settings+=(MARKETING_VERSION="$version")
destination="export"
[ "$upload" = 0 ] || destination="upload"

echo "release-build: $commit, build $build${version:+, version $version}, team $team, key $kid → $destination"
echo "               as $me with $(xcodebuild -version 2>/dev/null | head -n 1), into $out"

# A distribution identity in this keychain would be used instead of the
# cloud-managed certificate — and is a distribution private key on disk.
if security find-identity -v -p codesigning 2>/dev/null | grep -q 'Apple Distribution'; then
  ci_warn "an Apple Distribution identity is in $me's keychain: delete it (Keychain Access) so the export uses the cloud-managed certificate and no distribution key stays on this Mac"
fi

if [ "$dry" = 1 ]; then
  echo "dry run — would run, in $XBIN_REPO/native/ios:"
  echo "  xcodegen generate --spec project.yml"
  echo "  xcodebuild archive -project Xbin.xcodeproj -scheme Xbin -configuration Release -destination generic/platform=iOS -archivePath $out/Xbin.xcarchive -derivedDataPath $out/derived ${auth[*]} ${settings[*]}"
  echo "  xcodebuild -exportArchive -archivePath $out/Xbin.xcarchive -exportPath $out/export -exportOptionsPlist $out/ExportOptions.plist ${auth[*]}"
  exit 0
fi

if ! security show-keychain-info "$HOME/Library/Keychains/login.keychain-db" >/dev/null 2>&1; then
  if [ -t 0 ]; then
    echo "the login keychain is locked (an ssh session): unlocking it for the signing"
    security unlock-keychain "$HOME/Library/Keychains/login.keychain-db"
  else
    refuse "the login keychain is locked: security unlock-keychain ~/Library/Keychains/login.keychain-db, then run this again"
  fi
fi

umask 077
mkdir -p "$out"
chmod 700 "$out"
cat >"$out/ExportOptions.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>method</key>
	<string>app-store-connect</string>
	<key>destination</key>
	<string>$destination</string>
	<key>teamID</key>
	<string>$team</string>
	<key>signingStyle</key>
	<string>automatic</string>
	<key>uploadSymbols</key>
	<true/>
	<key>manageAppVersionAndBuildNumber</key>
	<false/>
</dict>
</plist>
EOF

cd native/ios
proj=""
ci_project
ci_metal
ci_xcodebuild "$out/archive.log" archive -project "$proj" -scheme Xbin -configuration Release \
  -destination generic/platform=iOS -archivePath "$out/Xbin.xcarchive" -derivedDataPath "$out/derived" \
  "${auth[@]}" "${settings[@]}"
[ -d "$out/Xbin.xcarchive" ] || refuse "no archive at $out/Xbin.xcarchive (see $out/archive.log)"
ci_xcodebuild "$out/export.log" -exportArchive -archivePath "$out/Xbin.xcarchive" -exportPath "$out/export" \
  -exportOptionsPlist "$out/ExportOptions.plist" "${auth[@]}"
rm -rf "$out/derived"

if security find-identity -v -p codesigning 2>/dev/null | grep -q 'Apple Distribution'; then
  ci_warn "the export left an Apple Distribution identity in $me's keychain (the key had no cloud-signing access?): delete it, and check the key's role (native/AGENTS.md → Mac mini)"
fi
if [ "$upload" = 1 ]; then
  echo "release-build: uploaded build $build to App Store Connect (TestFlight processes it next); archive and logs in $out"
else
  ipa=$(find "$out/export" -maxdepth 1 -name '*.ipa' 2>/dev/null | head -n 1 || true)
  [ -n "$ipa" ] || refuse "no .ipa in $out/export (see $out/export.log)"
  echo "release-build: $ipa ($(ci_sha256 <"$ipa"))"
fi
