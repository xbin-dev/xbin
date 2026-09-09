#!/usr/bin/env bash
# hack/release.sh — the whole release in one command:  make release TAG=vX.Y.Z
#
#   1. preflight: tag shape, clean tree, on master, gh authenticated
#   2. make check (skip with --no-check)
#   3. annotated tag (or verify an existing one points at HEAD), push the
#      branch and the tag
#   4. build + publish from a DETACHED WORKTREE of the tag —
#      deploy/publish-release.sh builds whatever checkout it runs in, so an
#      edit made on master during the 5–15 min build must not leak into the
#      bundles (this bit two releases before the worktree rule)
#   5. watch the CI runs for the commit (tag + branch), show the GitHub
#      release, prune dist/ to the bundles of the newest --keep tags
#
#   hack/release.sh vX.Y.Z [--dry-run] [--no-check] [--arch amd64,arm64]
#                          [--no-watch] [--keep N] [--allow-branch]
set -euo pipefail

TAG="" DRY=0 CHECK=1 ARCH="" WATCH=1 KEEP=2 ANYBRANCH=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --no-check) CHECK=0 ;;
    --arch) ARCH="$2"; shift ;;
    --arch=*) ARCH="${1#*=}" ;;
    --no-watch) WATCH=0 ;;
    --keep) KEEP="$2"; shift ;;
    --keep=*) KEEP="${1#*=}" ;;
    --allow-branch) ANYBRANCH=1 ;;
    -h|--help) sed -n '2,16p' "$0"; exit 0 ;;
    v*) TAG="$1" ;;
    *) echo "error: unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

die()  { echo "release: $*" >&2; exit 1; }
info() { echo ">> $*" >&2; }
run()  { if [ "$DRY" = 1 ]; then echo "   \$ $*"; else "$@"; fi; }

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

# --- 1. preflight -----------------------------------------------------------
[ -n "$TAG" ] || die "usage: make release TAG=vX.Y.Z"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "tag must look like vX.Y.Z (got $TAG)"
git diff --quiet && git diff --cached --quiet || die "working tree is not clean — commit or stash first"
branch=$(git rev-parse --abbrev-ref HEAD)
if [ "$branch" != master ] && [ "$ANYBRANCH" = 0 ]; then
  die "on branch $branch, not master (pass --allow-branch to release from here)"
fi
command -v gh >/dev/null || die "gh not found"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated (gh auth login)"
head=$(git rev-parse HEAD)
if git rev-parse -q --verify "refs/tags/$TAG^{commit}" >/dev/null; then
  at=$(git rev-parse "$TAG^{commit}")
  [ "$at" = "$head" ] || die "tag $TAG already exists at ${at:0:12}, HEAD is ${head:0:12}"
  info "tag $TAG already points at HEAD"
  NEWTAG=0
else
  NEWTAG=1
fi
prev=$(git describe --tags --abbrev=0 2>/dev/null || true)
info "releasing $TAG from $branch @ ${head:0:12}${prev:+ (previous: $prev)}"

# --- 2. checks --------------------------------------------------------------
if [ "$CHECK" = 1 ]; then
  info "make check"
  run make -s check
fi

# --- 3. tag + push ----------------------------------------------------------
if [ "$NEWTAG" = 1 ]; then
  run git tag -a "$TAG" -m "$TAG"
fi
run git push origin "$branch" "$TAG"

# --- 4. build + publish from a detached worktree -----------------------------
W="${TMPDIR:-/tmp}/xbin-release-$TAG"
cleanup() { git worktree remove --force "$W" >/dev/null 2>&1 || true; git worktree prune >/dev/null 2>&1 || true; }
if [ "$DRY" = 0 ]; then trap cleanup EXIT; fi
[ -e "$W" ] && cleanup
run git worktree add --detach "$W" "$TAG"
publish=(./deploy/publish-release.sh --tag "$TAG" --out "$repo/dist")
[ -n "$ARCH" ] && publish+=(--arch "$ARCH")
if [ "$DRY" = 1 ]; then
  echo "   \$ (cd $W && ${publish[*]})"
else
  (cd "$W" && "${publish[@]}")
fi

# --- 5. CI, release, prune ---------------------------------------------------
if [ "$WATCH" = 1 ]; then
  info "waiting for CI runs of ${head:0:12} (tag + branch)"
  if [ "$DRY" = 1 ]; then
    echo "   \$ gh run list --commit $head --json databaseId,headBranch; gh run watch <id> --exit-status (each)"
  else
    ids=""
    for _ in $(seq 1 24); do
      ids=$(gh run list --commit "$head" --json databaseId --jq '.[].databaseId' 2>/dev/null | tr '\n' ' ')
      [ -n "$ids" ] && break
      sleep 5
    done
    [ -n "$ids" ] || die "no CI runs found for $head after 2 minutes"
    for id in $ids; do
      gh run watch "$id" --exit-status || die "CI run $id failed — the release assets are published; fix forward"
    done
  fi
fi
run gh release view "$TAG"

if [ -d "$repo/dist" ]; then
  keep=$(ls "$repo"/dist/xbin-v*-linux-*.tar.zst 2>/dev/null | sed -E 's|.*/xbin-(v[0-9.]+)-linux.*|\1|' | sort -uV | tail -n "$KEEP" | tr '\n' ' ')
  for f in "$repo"/dist/xbin-v*-linux-*.tar.zst; do
    [ -e "$f" ] || continue
    t=$(echo "$f" | sed -E 's|.*/xbin-(v[0-9.]+)-linux.*|\1|')
    case " $keep " in *" $t "*) ;; *) info "pruning $(basename "$f")"; run rm -f "$f" ;; esac
  done
fi
info "done: $TAG"
