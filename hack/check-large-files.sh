#!/usr/bin/env bash
# hack/check-large-files.sh — keep big and native binaries out of git (make
# large-files, part of `make check`, and the pre-commit hook). Fails on any
# file in the index over 1 MiB, and on any ELF / Mach-O / PE binary, unless
# hack/large-files.allow names it. Built helpers live in the helpers bucket
# (make helpers), bundles on release tags, build output under the ignored
# bin/ and dist/ — none of it belongs in history, where it stays forever.
#
# It reads the index, not the work tree, so the hook checks what is about to
# be committed.
#
#   hack/check-large-files.sh
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
LIMIT=$((1024 * 1024))
allowfile=hack/large-files.allow

# The allowlist: one glob per line, each with a "# why" on the same line.
allow=()
n=0 bad=0
while IFS= read -r line || [ -n "$line" ]; do
  n=$((n + 1))
  case "$line" in ''|'#'*) continue ;; esac
  pat=${line%%#*}; pat=${pat%"${pat##*[![:space:]]}"}
  if [[ $line != *'#'* ]] || [ -z "$pat" ] || [ -z "${line#*#}" ]; then
    echo "$allowfile:$n: want '<glob>  # why it must be in git'" >&2; bad=1; continue
  fi
  allow+=("$pat")
done < <(cat "$allowfile" 2>/dev/null || true)
[ "$bad" = 0 ] || exit 1

allowed() {
  local p
  for p in "${allow[@]}"; do
    # shellcheck disable=SC2053 # $p is a glob on purpose
    [[ $1 == $p ]] && return 0
  done
  return 1
}

fails=()
# Sizes of every regular file in the index, one git process.
while read -r size path; do
  [ "$size" -gt "$LIMIT" ] || continue
  allowed "$path" || fails+=("$path: $((size / 1024)) KiB (over 1 MiB)")
done < <(git -c core.quotepath=off ls-files -s \
          | awk -F'\t' '{ split($1, m, " "); if (m[1] == "100644" || m[1] == "100755") print m[2], $2 }' \
          | git cat-file --batch-check='%(objectsize) %(rest)')

# Native executables: only files git itself calls binary need a look.
while IFS= read -r path; do
  magic=$( { git cat-file blob ":$path" 2>/dev/null || true; } | od -An -tx1 -N4 | tr -d ' \n')
  case "$magic" in
    7f454c46) kind=ELF ;;
    feedface|feedfacf|cefaedfe|cffaedfe|cafebabe) kind=Mach-O ;;
    4d5a*) kind=PE ;;
    *) continue ;;
  esac
  allowed "$path" || fails+=("$path: an $kind binary")
done < <(git -c core.quotepath=off ls-files --eol | awk -F'\t' '$1 ~ /^i\/-text/ { print $2 }')

if [ ${#fails[@]} -gt 0 ]; then
  echo "large-files: these don't belong in git:" >&2
  printf '  %s\n' "${fails[@]}" >&2
  echo "Build output goes under bin/ or dist/ (ignored); helper binaries in the helpers bucket (make helpers-publish). A file that must be tracked goes in $allowfile with the reason." >&2
  exit 1
fi
echo "large-files: nothing over 1 MiB, no native binaries$([ ${#allow[@]} -gt 0 ] && echo " (${#allow[@]} allowlisted)")"
