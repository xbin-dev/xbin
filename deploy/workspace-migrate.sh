#!/usr/bin/env bash
# workspace-migrate.sh — update tile source after the module-path move
#   github.com/magik6k/xbin  ->  github.com/xbin-dev/xbin
#
# An existing workspace has tiles whose Go imports / go.mod still reference the
# OLD module path. Once xbind is rebuilt from the new path, those tiles fail to
# build until their refs are updated (the scaffolder only fixes NEW tiles). This
# walks a workspace and rewrites the references in place.
#
#   sudo -u xbin deploy/workspace-migrate.sh [WORKSPACE_DIR]
#
# BACK UP THE WORKSPACE FIRST. Idempotent (safe to re-run); --dry-run previews
# without writing. Override paths with XBIN_OLD_MODULE / XBIN_NEW_MODULE.
set -euo pipefail

OLD="${XBIN_OLD_MODULE:-github.com/magik6k/xbin}"
NEW="${XBIN_NEW_MODULE:-github.com/xbin-dev/xbin}"
DRY=0
WS=""
for a in "$@"; do
  case "$a" in
    -n|--dry-run) DRY=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $a" >&2; exit 1 ;;
    *)  WS="$a" ;;
  esac
done
WS="${WS:-${XBIN_WORKSPACE:-/opt/xbin/workspace}}"
[ -d "$WS" ] || { echo "error: no such workspace dir: $WS" >&2; exit 1; }

# Escape regex metacharacters in OLD for sed (the '#' delimiter means '/' is
# literal; NEW is a plain replacement with no & or \).
OLD_RE=$(printf '%s' "$OLD" | sed 's/[.[\]^$*]/\\&/g')

echo "workspace : $WS"
echo "rename    : $OLD  ->  $NEW"
[ "$DRY" = 1 ] && echo "mode      : DRY RUN (no writes)"
echo

# Source + module files that can carry the import path. `grep -r` does NOT
# descend symlinks, so the deps/ symlink trees are skipped; we also exclude the
# non-source trees and never rewrite go.sum (renaming a path there would break
# its hash — the SDK is a local, replaced module with no go.sum entry anyway).
mapfile -d '' -t files < <(
  grep -rlZ --fixed-strings "$OLD" "$WS" \
    --include='*.go' --include='go.mod' --include='go.work' \
    --include='go.mod.tile' --include='*.go.tile' \
    --exclude-dir=.git --exclude-dir=.xbin --exclude-dir=data \
    --exclude-dir=node_modules --exclude-dir=vendor --exclude-dir=deps \
    2>/dev/null || true
)

if [ "${#files[@]}" -eq 0 ]; then
  echo "nothing to change — no files reference $OLD (already migrated?)."
  exit 0
fi

changed=0
for f in "${files[@]}"; do
  [ -L "$f" ] && continue                    # never rewrite through a symlink
  n=$(grep -c --fixed-strings "$OLD" "$f" || true)
  if [ "$DRY" = 1 ]; then
    printf '  would update (%s)  %s\n' "$n" "${f#"$WS"/}"
  else
    sed -i "s#${OLD_RE}#${NEW}#g" "$f"
    printf '  updated (%s)  %s\n' "$n" "${f#"$WS"/}"
  fi
  changed=$((changed + 1))
done

echo
echo "$([ "$DRY" = 1 ] && echo 'would update' || echo 'updated') ${changed} file(s)."
if [ "$DRY" != 1 ]; then
  echo
  echo "xbind rebuilds each tile on its next change/spawn. To force a clean"
  echo "rebuild of everything now:  sudo systemctl restart xbin"
  echo "(then unseal the vault if you run sealed)."
fi
