#!/usr/bin/env bash
# native/ios/scripts/ci-cache.sh — where a job's DerivedData and SwiftPM
# clones live and how they outlive the job (an ios.yml step before the
# build; the scripts after it read XBIN_CI_DERIVED and XBIN_CI_SPM):
#
#   ci-cache.sh <job> <DerivedData subdir>…     e.g. ci-cache.sh app app
#
# XBIN_CI_CACHE (ios.yml sets it from the repository variable of that name):
#   auto     (default) a GitHub-hosted runner → actions, a self-hosted one
#            (RUNNER_ENVIRONMENT=self-hosted) → disk
#   actions  under $RUNNER_TEMP; the workflow's actions/cache step restores
#            them before the build and saves them after a green job
#   disk     on the runner's own disk, $XBIN_CI_CACHE_ROOT (default
#            ~/Library/Caches/xbin-ci)/<runner name>/<xcode version>/: kept
#            between jobs, never uploaded; mac-cleanup.sh prunes what goes
#            unused
#   off      under $RUNNER_TEMP, nothing restored or saved (to rule a
#            stale cache out)
#
# Exports XBIN_CI_DERIVED and XBIN_CI_SPM to the job's later steps
# ($GITHUB_ENV) and sets step outputs:
#   mode     actions | disk | off — the cache step runs only for actions
#   key      ios-<job>-<xcode>-<hash>: the hash covers native/ios/project.yml,
#            every Package.swift and Package.resolved under native/ios and
#            XBIN_CI_CACHE_VERSION (bump it to drop every entry)
#   restore  ios-<job>-<xcode>- — without an exact hit, the newest entry for
#            this Xcode (the build is incremental; a stale one beats none)
#   paths    the SwiftPM clones and each DerivedData subdir, minus its Logs
#            and Index.noindex (multi-line, as actions/cache's `path` takes)
set -euo pipefail
# shellcheck source=SCRIPTDIR/ci-lib.sh
. "$(dirname "$0")/ci-lib.sh"

job=${1:?usage: ci-cache.sh <job> <DerivedData subdir>…}
shift
[ $# -gt 0 ] || { ci_error "ci-cache.sh $job: name the DerivedData subdirs the job builds into"; exit 2; }

mode=${XBIN_CI_CACHE:-auto}
case $mode in
auto)
  if [ "${RUNNER_ENVIRONMENT:-}" = self-hosted ]; then mode=disk; else mode=actions; fi
  ;;
actions | disk | off) ;;
*)
  ci_error "XBIN_CI_CACHE=$mode: expected auto, actions, disk or off"
  exit 2
  ;;
esac

xcode=$(ci_xcode_slug)
temp=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
if [ "$mode" = disk ]; then
  # One tree per runner (two runners on one Mac never share a build) and
  # per Xcode (a new Xcode starts clean; the old tree ages out).
  runner=$(printf '%s' "${RUNNER_NAME:-runner}" | tr -c 'A-Za-z0-9._-' '_')
  root="${XBIN_CI_CACHE_ROOT:-$HOME/Library/Caches/xbin-ci}/$runner/$xcode"
  derived=$root/derived
else
  derived=$temp/xbin-derived
fi
spm=$derived/SourcePackages
mkdir -p "$derived" "$spm"
if [ "$mode" = disk ]; then
  # mac-cleanup.sh deletes trees nobody touched for days.
  touch "$root"
fi

# The inputs that decide what SwiftPM resolves and how the project is
# built, hashed in a stable order.
inputs() {
  local ios="$XBIN_REPO/native/ios" f
  {
    [ -f "$ios/project.yml" ] && echo "$ios/project.yml"
    find "$ios" \( -name .build -o -name DerivedData -o -name '*.xcodeproj' \) -prune -o \
      \( -name Package.swift -o -name Package.resolved \) -type f -print
  } | LC_ALL=C sort | while IFS= read -r f; do
    printf '%s %s\n' "${f#"$XBIN_REPO"/}" "$(ci_sha256 <"$f")"
  done
  echo "cache-version ${XBIN_CI_CACHE_VERSION:-1}"
}
hash=$(inputs | ci_sha256 | cut -c1-16)
key="ios-$job-$xcode-$hash"
restore="ios-$job-$xcode-"

paths="$spm"
for sub in "$@"; do
  paths="$paths
$derived/$sub
!$derived/$sub/Logs
!$derived/$sub/Index.noindex"
done

if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "XBIN_CI_DERIVED=$derived"
    echo "XBIN_CI_SPM=$spm"
  } >>"$GITHUB_ENV"
fi
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "mode=$mode"
    echo "key=$key"
    echo "restore=$restore"
    echo "paths<<XBIN_CI_PATHS"
    printf '%s\n' "$paths"
    echo "XBIN_CI_PATHS"
  } >>"$GITHUB_OUTPUT"
fi

echo "cache: $mode — DerivedData $derived, SwiftPM $spm"
case $mode in
actions) echo "key $key (restore $restore*)" ;;
disk) ci_summary "**Cache:** on this runner's disk, \`$derived\`." ;;
off) ci_summary "**Cache:** off (XBIN_CI_CACHE=off) — a clean build." ;;
esac
