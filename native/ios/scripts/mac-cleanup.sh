#!/usr/bin/env bash
# native/ios/scripts/mac-cleanup.sh — keep the CI Mac's disk and memory in
# check. mac-setup.sh installs a copy as ~/xbin-ci/bin/mac-cleanup.sh and
# runs it daily from a LaunchAgent (dev.xbin.ci-cleanup, 04:30, log in
# ~/Library/Logs/xbin-ci/), and as the Actions runner's job hook.
#
#   mac-cleanup.sh [--dry-run]   the daily sweep: skipped while a runner job
#                                or a mac-remote.sh run (build, run, the UI
#                                tests…) is going; shuts the simulators down,
#                                deletes unavailable ones, and removes what
#                                nobody touched for $XBIN_CLEANUP_DAYS days
#                                (default 7): Xcode's DerivedData folders,
#                                the self-hosted CI caches (ci-cache.sh:
#                                ~/Library/Caches/xbin-ci/<runner>/<xcode>),
#                                and mac-remote.sh's build and result trees
#   mac-cleanup.sh --job-hook    before and after every runner job: first
#                                REFUSE a job that is not this repository's
#                                ios.yml on a push to one of its branches or
#                                dispatched by hand (exit 1: the runner fails
#                                it before any step) — then shut the
#                                simulators down (a job starts with a cold,
#                                known simulator; 24 GB is not much for
#                                several). A simulator that won't shut down
#                                never fails the job.
#
# The refusal is what keeps a fork's pull request off this Mac (native/
# AGENTS.md → Security): the runner is the repository's, and a pull
# request's workflow runs from ITS merge commit — it can add a workflow
# file of its own that names this runner, or edit the checks in the
# repository that would object. Only the hook runs before its code, from
# this Mac's own files. XBIN_CI_REPO (owner/name, set by mac-setup.sh's
# job-hook.sh) is the repository; GITHUB_EVENT_NAME, GITHUB_WORKFLOW_REF,
# GITHUB_HEAD_REF and GITHUB_EVENT_PATH are what the runner says of the job.
set -uo pipefail

mode=sweep dry=0
case ${1:-} in
--job-hook) mode=hook ;;
--dry-run) dry=1 ;;
"") ;;
*) echo "mac-cleanup: unknown argument $1" >&2; exit 2 ;;
esac

say() { echo "$(date '+%Y-%m-%d %H:%M:%S') mac-cleanup: $*"; }

# job_allowed: whether this runner job may run here; why says what is wrong.
job_allowed() {
  local repo=${XBIN_CI_REPO:-}
  why=""
  case ${GITHUB_EVENT_NAME:-} in
  push | workflow_dispatch) ;;
  *) why="a ${GITHUB_EVENT_NAME:-job with no event} — only pushes to this repository's branches and manual dispatches run here"; return 1 ;;
  esac
  [ -z "${GITHUB_HEAD_REF:-}" ] || { why="a pull request's job (GITHUB_HEAD_REF=$GITHUB_HEAD_REF)"; return 1; }
  [ -n "$repo" ] || { why="no XBIN_CI_REPO: the hook doesn't know its repository (rerun mac-setup.sh)"; return 1; }
  [ -z "${GITHUB_REPOSITORY:-}" ] || [ "$GITHUB_REPOSITORY" = "$repo" ] ||
    { why="a job of $GITHUB_REPOSITORY, not $repo"; return 1; }
  case ${GITHUB_WORKFLOW_REF:-} in
  "$repo/.github/workflows/ios.yml@refs/heads/"*) ;;
  *) why="workflow ${GITHUB_WORKFLOW_REF:-(unnamed)} — only $repo's .github/workflows/ios.yml, on a branch, runs here"; return 1 ;;
  esac
  if [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ] &&
    python3 -c 'import json,sys; e=json.load(open(sys.argv[1])); sys.exit(0 if (e.get("repository") or {}).get("fork") or ((e.get("pull_request") or {}).get("head") or {}).get("repo", {}).get("fork") else 1)' \
      "$GITHUB_EVENT_PATH" 2>/dev/null; then
    why="a fork's event"
    return 1
  fi
  return 0
}

if [ "$mode" = hook ]; then
  if ! job_allowed; then
    echo "mac-cleanup: refusing this job on the self-hosted Mac: $why (native/AGENTS.md → Security)" >&2
    exit 1
  fi
  xcrun simctl shutdown all >/dev/null 2>&1 || true
  exit 0
fi

days=${XBIN_CLEANUP_DAYS:-7}
if pgrep -f 'Runner.Worker' >/dev/null 2>&1; then
  say "a runner job is running — skipped"
  exit 0
fi
# the ssh dev loop's commands on this Mac (not its `cleanup`, which is this)
if pgrep -f 'mac-remote\.sh --on-mac (toolchain|packages|build|snapshots|uitests|run|e2e)' >/dev/null 2>&1; then
  say "a mac-remote.sh run is going — skipped"
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
