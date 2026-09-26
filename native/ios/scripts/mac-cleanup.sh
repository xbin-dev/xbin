#!/usr/bin/env bash
# native/ios/scripts/mac-cleanup.sh — keep the CI Mac's disk and memory in
# check. mac-setup.sh installs a copy as ~/xbin-ci/bin/mac-cleanup.sh and
# runs it daily from a LaunchAgent (dev.xbin.ci-cleanup, 04:30, log in
# ~/Library/Logs/xbin-ci/), and as the Actions runner's job hook.
#
#   mac-cleanup.sh [--dry-run]   the daily sweep: skipped while a runner job
#                                is running; shuts the simulators down,
#                                deletes unavailable ones, and removes what
#                                nobody touched for $XBIN_CLEANUP_DAYS days
#                                (default 7): Xcode's DerivedData folders,
#                                the self-hosted CI caches (ci-cache.sh:
#                                ~/Library/Caches/xbin-ci/<runner>/<xcode>),
#                                and mac-remote.sh's build and result trees
#   mac-cleanup.sh --job-hook    before and after every runner job: shut the
#                                simulators down (a job starts with a cold,
#                                known simulator; 24 GB is not much for
#                                several). Never fails the job.
set -uo pipefail

mode=sweep dry=0
case ${1:-} in
--job-hook) mode=hook ;;
--dry-run) dry=1 ;;
"") ;;
*) echo "mac-cleanup: unknown argument $1" >&2; exit 2 ;;
esac

say() { echo "$(date '+%Y-%m-%d %H:%M:%S') mac-cleanup: $*"; }

if [ "$mode" = hook ]; then
  xcrun simctl shutdown all >/dev/null 2>&1 || true
  exit 0
fi

days=${XBIN_CLEANUP_DAYS:-7}
if pgrep -f 'Runner.Worker' >/dev/null 2>&1; then
  say "a runner job is running — skipped"
  exit 0
fi

# prune <dir> — delete the entries of <dir> not modified for $days days.
prune() {
  local d=$1 e
  [ -d "$d" ] || return 0
  find "$d" -mindepth 1 -maxdepth 1 -mtime +"$days" 2>/dev/null | while IFS= read -r e; do
    if [ "$dry" = 1 ]; then
      say "would remove $e"
    else
      say "removing $e"
      rm -rf "$e"
    fi
  done
}

say "sweep (older than $days days$([ "$dry" = 1 ] && echo ', dry run'))"
if [ "$dry" = 0 ]; then
  xcrun simctl shutdown all >/dev/null 2>&1 || true
  xcrun simctl delete unavailable 2>&1 || true
fi
prune "$HOME/Library/Developer/Xcode/DerivedData"
# ci-cache.sh's trees: <root>/<runner>/<xcode>; the runner dir itself stays.
root=${XBIN_CI_CACHE_ROOT:-$HOME/Library/Caches/xbin-ci}
if [ -d "$root" ]; then
  for r in "$root"/*/; do
    [ -d "$r" ] || continue
    case $(basename "$r") in tools) continue ;; esac
    prune "${r%/}"
  done
fi
# mac-remote.sh's tree on this Mac: its builds and pulled-from results.
remote=${XBIN_MAC_BASE:-$HOME/xbin-remote}
prune "$remote/out"
if [ -d "$remote/derived" ] && [ -z "$(find "$remote/derived" -maxdepth 0 -mtime -"$days" 2>/dev/null)" ]; then
  if [ "$dry" = 1 ]; then say "would remove $remote/derived"; else say "removing $remote/derived"; rm -rf "$remote/derived"; fi
fi
df -h "$HOME" 2>/dev/null | tail -n 1 | while read -r _ size used avail _; do say "disk: $used used, $avail free of $size"; done
exit 0
