# shellcheck shell=bash
# hack/demo/lib.sh — what up.sh and reset.sh share (sourced): finding and
# stopping the xbind that serves a workspace, starting it again, and the
# user-namespace trick some sandboxes need for FUSE.

DEMO_LIB="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEMO_REPO="$(cd "$DEMO_LIB/../.." && pwd)"

# need_userns: true when this process can't mount FUSE, which the
# workspace's encrypted resources (gocryptfs) need — inside a user
# namespace that maps only our own uid, fusermount3's setuid bit has no
# root to become (it shows as owned by nobody). A user namespace of our own,
# where we are root, mounts FUSE directly instead. DEMO_USERNS=0 opts out,
# =1 forces it.
need_userns() {
  case "${DEMO_USERNS:-}" in 0) return 1 ;; 1) [[ "$(id -u)" != 0 ]]; return ;; esac
  [[ "$(id -u)" == 0 ]] && return 1
  local fm
  fm=$(command -v fusermount3 || command -v fusermount || true)
  [[ -n "$fm" && "$(stat -c %u "$fm")" != 0 ]]
}

# reexec_userns "$0" "$@": run this script again inside its own user and
# mount namespace when need_userns; xbind and its gocryptfs mounts live there
# (the namespace lasts as long as they do).
reexec_userns() {
  if [[ -z "${DEMO_IN_USERNS:-}" ]] && need_userns; then
    echo "(this namespace can't mount FUSE: running in a user namespace of its own)" >&2
    DEMO_IN_USERNS=1 exec unshare --user --map-root-user --mount -- "$@"
  fi
}

# re_quote S: S as a literal in an extended regular expression.
re_quote() { printf '%s' "$1" | sed 's/[][\.*^$+?(){}|]/\\&/g'; }
# proc_rx PROGRAM ARGS-REGEX: a pgrep/pkill -f pattern for processes running
# PROGRAM (its argv[0] ends in it) — never a shell whose command line merely
# mentions it.
proc_rx() { printf '^[^ ]*%s %s' "$(re_quote "$1")" "$2"; }

# xbind_pid WS: the xbind serving that workspace (none: empty).
xbind_pid() { pgrep -f -- "$(proc_rx xbind ".*--workspace $(re_quote "$1")( |\$)")" | head -1 || true; }

# stop_xbind WS: stop it (SIGTERM: it unmounts its gocryptfs views on the
# way out), then whatever it left behind.
stop_xbind() {
  local ws=$1 pid
  pid=$(xbind_pid "$ws")
  if [[ -n "$pid" ]]; then
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 80); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
    kill -9 "$pid" 2>/dev/null || true
  fi
  # a killed xbind's gocryptfs mounts outlive it
  pkill -f -- "$(proc_rx gocryptfs ".* $(re_quote "$ws")/")" 2>/dev/null || true
  { grep -F " $ws/" /proc/self/mounts 2>/dev/null || true; } | cut -d' ' -f2 | while read -r m; do
    fusermount3 -uz "$m" 2>/dev/null || fusermount -uz "$m" 2>/dev/null || umount -l "$m" 2>/dev/null || true
  done
}

# wait_up URL: until xbind answers its sign-in page (60 s).
wait_up() {
  for _ in $(seq 1 240); do curl -sf -o /dev/null "$1/login" && return 0; sleep 0.25; done
  echo "xbind did not come up at $1" >&2
  return 1
}

# load_devmk: the machine's test environment that hack/dev-setup.sh wrote
# (.dev.mk: the rootfs, gocryptfs, fuse-overlayfs, the VM assets), from this
# checkout or, in a git worktree, from the main checkout. PATH stays ours.
load_devmk() {
  local mk="$DEMO_REPO/.dev.mk" main
  if [[ ! -f "$mk" ]]; then
    main=$(git -C "$DEMO_REPO" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
    [[ -n "$main" && -f "${main%/.git}/.dev.mk" ]] && mk="${main%/.git}/.dev.mk"
  fi
  [[ -f "$mk" ]] || return 0
  local name value
  while IFS= read -r line; do
    [[ "$line" =~ ^export\ ([A-Z_]+)\ :=\ (.*)$ ]] || continue
    name=${BASH_REMATCH[1]} value=${BASH_REMATCH[2]}
    [[ "$name" == PATH || -n "${!name:-}" ]] && continue
    export "$name=$value"
  done < "$mk"
  [[ -n "${DEMO_IN_USERNS:-}" ]] || echo "(machine settings from $mk)" >&2
}
