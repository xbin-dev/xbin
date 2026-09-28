#!/usr/bin/env bash
# hack/dev-setup.sh — make this machine run xbin's checks as completely as
# CI does, and faster: check everything first, show what's missing and what
# each gap costs (which tests skip), then fix it — one sudo for the root
# steps, the rest as you — and check again.
#
#   hack/dev-setup.sh            check, plan, ask, apply (sudo once), check
#   hack/dev-setup.sh --check    report only; exit 1 when a required item is missing
#   hack/dev-setup.sh --yes      apply without asking
#   hack/dev-setup.sh --no-root  never sudo: print the root commands instead
#   hack/dev-setup.sh --skip a,b / --only a,b   by item id (the report's first column)
#   hack/dev-setup.sh --env      print .dev.mk as shell exports: eval "$(hack/dev-setup.sh --env)"
#
# Linux (apt, dnf, yum, pacman, zypper; WSL2 with systemd) gets the whole
# list: tools, Go, Node, Playwright, Swift, a container engine, user
# namespaces, sub-uid ranges, FUSE, tun, KVM, cgroup delegation, inotify,
# AppArmor, and the repo's own assets (prebuilt helpers, the base rootfs, the
# previous release's xbind for the downgrade tests, git hooks). macOS gets
# the tools, Go, Node and Playwright, and native/ios/scripts/mac-setup.sh
# (--no-runner) for Xcode; xbind's sandboxes are Linux's, so the integration
# suite needs a Linux machine or VM there.
#
# What it finds lands in .dev.mk (gitignored), which the Makefile includes:
# XBIN_TEST_ROOTFS, XBIN_GOCRYPTFS, XBIN_FUSE_OVERLAYFS, XBIN_DOWNGRADE_BIN,
# PLAYWRIGHT_DIR, GOFMT (CI's Go's), GOMODCACHE when yours is read-only, and
# PATH for a toolchain installed here — so `make check` and `make
# integration` use them without anything exported by hand. Downloads go to
# ${XDG_CACHE_HOME:-~/.cache}/xbin-dev (XBIN_DEV_CACHE); nothing is written
# outside it, the repo, and — as root, after you said yes — the system
# files each root step names.
set -uo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
CACHE=${XBIN_DEV_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/xbin-dev}
DEVMK=$repo/.dev.mk
PLAYWRIGHT_VERSION=${XBIN_PLAYWRIGHT_VERSION:-1.61.1}
SUBID_COUNT=65536

# ---- output ----
if [ -t 1 ]; then B=$'\033[1m' G=$'\033[32m' Y=$'\033[33m' RED=$'\033[31m' D=$'\033[2m' R=$'\033[0m'
else B='' G='' Y='' RED='' D='' R=''; fi
say() { printf '%s\n' "$*"; }
head1() { printf '\n%s==> %s%s\n' "$B" "$*" "$R"; }
die() { printf '%serror:%s %s\n' "$RED" "$R" "$*" >&2; exit 2; }
have() { command -v "$1" >/dev/null 2>&1; }
ask() { # ask "question" → 0 yes
  [ "$YES" = 1 ] && return 0
  [ -r /dev/tty ] || return 1
  local a; printf '%s [Y/n] ' "$1" >/dev/tty; read -r a </dev/tty || return 1
  case "$a" in ''|y|Y|yes) return 0 ;; *) return 1 ;; esac
}

# ---- arguments ----
MODE=apply YES=0 NOROOT=0 SKIP=, ONLY=
ROOTPHASE=
while [ $# -gt 0 ]; do
  case "$1" in
    --check) MODE=check ;;
    --yes|-y) YES=1 ;;
    --no-root) NOROOT=1 ;;
    --skip) SKIP=",$2,"; shift ;;
    --only) ONLY=",$2,"; shift ;;
    --env) MODE=print-env ;;
    --root-phase) ROOTPHASE=$2; shift ;;
    -h|--help) sed -n '2,/^set -uo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument $1 (--help)" ;;
  esac
  shift
done
wanted() { # id → whether this run looks at it
  case "$SKIP" in *",$1,"*) return 1 ;; esac
  [ -z "$ONLY" ] && return 0
  case "$ONLY" in *",$1,"*) return 0 ;; esac
  return 1
}

# ---- platform ----
OS=$(uname -s)
case "$(uname -m)" in x86_64|amd64) GOARCH=amd64 NODEARCH=x64 ;; aarch64|arm64) GOARCH=arm64 NODEARCH=arm64 ;; *) GOARCH='' NODEARCH='' ;; esac
case "$OS" in Linux) GOOS=linux ;; Darwin) GOOS=darwin ;; *) die "unsupported OS $OS: xbin develops on Linux (and on macOS for the app and the web)" ;; esac
PKG=
if [ "$OS" = Darwin ]; then
  # Homebrew may be installed but off a non-login shell's PATH (ssh)
  for b in /opt/homebrew/bin /usr/local/bin; do have brew || { [ -x "$b/brew" ] && PATH="$b:$PATH"; }; done
  have brew && PKG=brew
elif have apt-get; then PKG=apt
elif have dnf; then PKG=dnf
elif have yum; then PKG=yum
elif have pacman; then PKG=pacman
elif have zypper; then PKG=zypper
fi
WSL=0; grep -qi microsoft /proc/version 2>/dev/null && WSL=1
USER_NAME=${XBIN_DEV_USER:-$(id -un)}

# The versions CI uses (kept in one place: the workflow and go.mod).
ci_go=$(sed -n 's/^ *go-version: *"\{0,1\}\([0-9.]*\)"\{0,1\}.*/\1/p' "$repo/.github/workflows/ci.yml" | head -n 1)
ci_node=$(sed -n 's/^ *node-version: *"\{0,1\}\([0-9]*\)"\{0,1\}.*/\1/p' "$repo/.github/workflows/ci.yml" | head -n 1)
ci_prev=$(sed -n 's/^ *prev=\(v[0-9.]*\).*/\1/p' "$repo/.github/workflows/ci.yml" | head -n 1)
mod_go=$(sed -n 's/^go \([0-9.]*\)$/\1/p' "$repo/go.mod")
swift_want=$(sed -n 's/^want=\${XBIN_SWIFT_VERSION:-\([0-9.]*\)}.*/\1/p' "$repo/native/ios/scripts/ci-linux-swift.sh")
SWIFTLY_BIN=${SWIFTLY_BIN_DIR:-${SWIFTLY_HOME_DIR:-$HOME/.local/share/swiftly}/bin}

# ver_ge A B: dotted version A >= B
ver_ge() {
  local a b i x y
  IFS=. read -r -a a <<<"$1"; IFS=. read -r -a b <<<"$2"
  for i in 0 1 2; do
    x=${a[$i]:-0} y=${b[$i]:-0}; x=${x%%[!0-9]*} y=${y%%[!0-9]*}
    [ "${x:-0}" -gt "${y:-0}" ] && return 0
    [ "${x:-0}" -lt "${y:-0}" ] && return 1
  done
  return 0
}
minor() { printf '%s' "$1" | cut -d. -f1,2; }
gover() { go env GOVERSION 2>/dev/null | sed 's/^go//; s/-.*//'; } # the go on PATH, bare (1.27.0)
dl() { curl -fsSL --retry 3 --connect-timeout 15 "$@"; }
sha256() { if have sha256sum; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

# ---- packages (Linux distros, Homebrew) ----
# pkg_of BINARY → this distro's package that provides it
pkg_of() {
  case "$PKG:$1" in
    *:git|*:make|*:curl|*:tar|*:zstd) echo "$1" ;;
    brew:*) case "$1" in shellcheck|gh) echo "$1" ;; python3) echo python ;; *) echo "$1" ;; esac ;;
    *:unshare) echo util-linux ;;
    *:fusermount3) echo fuse3 ;;
    apt:newuidmap|apt:newgidmap) echo uidmap ;;
    dnf:newuidmap|dnf:newgidmap|yum:newuidmap|yum:newgidmap) echo shadow-utils ;;
    pacman:newuidmap|pacman:newgidmap|zypper:newuidmap|zypper:newgidmap) echo shadow ;;
    *:getfattr) echo attr ;;
    apt:setcap) echo libcap2-bin ;;
    zypper:setcap) echo libcap-progs ;;
    *:setcap) echo libcap ;;
    apt:shellcheck|pacman:shellcheck) echo shellcheck ;;
    *:shellcheck) echo ShellCheck ;;
    pacman:python3) echo python ;;
    *:python3) echo python3 ;;
    apt:pyyaml) echo python3-yaml ;;
    dnf:pyyaml|yum:pyyaml) echo python3-pyyaml ;;
    pacman:pyyaml) echo python-yaml ;;
    zypper:pyyaml) echo python3-PyYAML ;;
    *:podman) echo podman ;;
    *) echo "$1" ;;
  esac
}
pkg_install() { # as root
  case "$PKG" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ;;
    dnf) dnf install -y -q "$@" ;;
    yum) yum install -y -q "$@" ;;
    pacman) pacman -S --needed --noconfirm "$@" ;;
    zypper) zypper --non-interactive install "$@" ;;
    *) return 1 ;;
  esac
}
pkg_hint() {
  case "$PKG" in
    apt) echo "sudo apt-get install -y $*" ;; dnf) echo "sudo dnf install -y $*" ;; yum) echo "sudo yum install -y $*" ;;
    pacman) echo "sudo pacman -S --needed $*" ;; zypper) echo "sudo zypper install $*" ;; brew) echo "brew install $*" ;;
    *) echo "install with your package manager: $*" ;;
  esac
}

# ---- items ----
# Each item is c_<id> (the check) and, when it can be fixed, f_<id> (as
# you) or r_<id> (as root, in the sudo phase). A check sets:
#   S   ok | warn | miss | na   (warn: works, but less than CI; miss: a gap)
#   M   what it found
#   L   what the gap costs (the tests that skip, the target that fails)
#   K   root | user | post (root, after the user steps) | manual | ''
#   F   the fix, in words (or the command, for manual)
#   REQ 1 when `make check`/`make integration` need it, 0 when optional
# and may add packages to PKGS (installed first in the root phase).
LINUX_ITEMS="tools engine go gofmt node userns subids fuse tun kvm cgroup inotify apparmor wsl gomodcache helpers-containerfs helpers-vm firecracker rootfs downgrade playwright swift hooks"
MAC_ITEMS="tools go gofmt node xcode gomodcache playwright hooks linux"
PKGS='' BREW=''
RELOGIN=''

want_pkg() { local p; for p in "$@"; do case " $PKGS " in *" $p "*) ;; *) PKGS="$PKGS $p" ;; esac; done; }

c_tools() {
  REQ=1 K='' F='' L=''
  local b miss='' opt='' req='git make curl tar'
  [ "$OS" = Linux ] && req="$req zstd unshare fusermount3 newuidmap newgidmap"
  for b in $req; do have "$b" || miss="$miss $b"; done
  # no shellcheck binary is fine where a container engine works: make check runs a pinned image of it (hack/check-sh.sh)
  local sc=shellcheck; { { have docker && docker info >/dev/null 2>&1; } || { have podman && podman info >/dev/null 2>&1; }; } && sc=''
  for b in $sc python3 $([ "$OS" = Linux ] && echo getfattr setcap); do have "$b" || opt="$opt $b"; done
  if have python3 && ! python3 -c 'import yaml' 2>/dev/null; then opt="$opt pyyaml"; fi
  local lx=''; [ "$OS" = Linux ] && lx=', zstd, unshare, fusermount3, newuidmap, attr, libcap'
  local scw='shellcheck'; have shellcheck || scw='shellcheck (its container)'
  if [ -z "$miss$opt" ]; then S=ok M="git, make, curl, tar$lx, $scw, python3 + yaml"; return; fi
  local p; BREW=''
  for b in $miss $opt; do p=$(pkg_of "$b"); if [ "$PKG" = brew ]; then BREW="$BREW $p"; else want_pkg "$p"; fi; done
  if [ -n "$miss" ]; then S=miss M="missing:$miss${opt:+ (and optional:$opt)}"; L="the build, the sandboxes and make check need them"
  else S=warn M="optional missing:$opt"; L="shellcheck (make check falls back to a container), the xattr/fscap tests, ci-local-check's workflow parse"; fi
  if [ -z "$PKG" ]; then K=manual F="install:$miss$opt"
  elif [ "$PKG" = brew ] && [ ! -w "$(brew --prefix)/Cellar" ]; then K=manual F="ask Homebrew's owner ($(stat -f %Su "$(brew --prefix)/Cellar" 2>/dev/null)): $(pkg_hint $BREW)"
  elif [ "$PKG" = brew ]; then K=user F="$(pkg_hint $BREW)"
  else K=root F="$(pkg_hint $PKGS)"; fi
}
f_tools() { [ "$PKG" = brew ] && brew install $BREW; }

c_engine() {
  REQ=0 K='' F='' L='make rootfs, make helpers-build (and shellcheck without the binary)'
  if { have docker && docker info >/dev/null 2>&1; } || { have podman && podman info >/dev/null 2>&1; }; then
    S=ok M="$(have docker && docker info >/dev/null 2>&1 && echo docker || echo podman) works as you"; return
  fi
  if have docker; then
    S=warn M="docker is installed but refuses you"
    if [ "$OS" = Linux ] && getent group docker >/dev/null 2>&1; then K=root F="add $USER_NAME to the docker group (root-equivalent: prefer rootless podman if that matters)"; RELOGIN="$RELOGIN docker"
    else K=manual F="start Docker (Docker Desktop on macOS)"; fi
    return
  fi
  S=warn M="no docker or podman"
  if [ "$OS" = Linux ] && [ -n "$PKG" ]; then want_pkg podman; K=root F="install podman (rootless, uses your sub-uid range)"
  else K=manual F="install Docker Desktop or podman"; fi
}
r_engine() { getent group docker >/dev/null 2>&1 && have docker && usermod -aG docker "$USER_NAME"; return 0; }

go_cached() { printf '%s/go-%s' "$CACHE" "$1"; }
c_go() {
  REQ=1 L='everything Go' K='' F=''
  if have go; then
    local v; v=$(gover)
    local here=''; case "$(command -v go)" in "$CACHE"/*) here=', downloaded here (make: via .dev.mk)' ;; esac
    if ver_ge "$v" "$mod_go"; then S=ok M="go $v (go.mod wants ≥ $mod_go$here)"; return; fi
    S=miss M="go $v is older than go.mod's $mod_go"
  elif [ -x "$(go_cached "$mod_go")/bin/go" ]; then S=ok M="go $mod_go from $(go_cached "$mod_go") (on make's PATH via .dev.mk)"; return
  else S=miss M="no go on PATH"; fi
  K=user F="download Go $mod_go from go.dev into $(go_cached "$mod_go") (checksum-verified; make uses it through .dev.mk)"
}
f_go() { fetch_go "$mod_go"; }
fetch_go() {
  local v=$1 dir; dir=$(go_cached "$1")
  [ -x "$dir/bin/go" ] && return 0
  [ -n "$GOARCH" ] || { say "  no Go download for $(uname -m)"; return 1; }
  local f="go$v.$GOOS-$GOARCH.tar.gz" tmp; tmp=$(mktemp -d)
  say "  go $v ← https://go.dev/dl/$f"
  if dl -o "$tmp/$f" "https://dl.google.com/go/$f" && [ "$(sha256 "$tmp/$f")" = "$(dl "https://dl.google.com/go/$f.sha256")" ]; then
    mkdir -p "$dir" && tar -xzf "$tmp/$f" -C "$dir" --strip-components=1 && rm -rf "$tmp" && return 0
  fi
  rm -rf "$tmp"; say "  ${RED}Go $v didn't download or its checksum didn't match${R}"; return 1
}

c_gofmt() {
  REQ=1 K='' F='' L='make fmt-check: CI runs Go '"$ci_go""'s gofmt, and gofmt output differs across versions"
  local v; v=$(gover)
  if [ -n "$v" ] && [ "$(minor "$v")" = "$ci_go" ]; then S=ok M="gofmt is Go $v's, CI's minor ($ci_go)"; return; fi
  if [ -x "$(go_cached "$mod_go")/bin/gofmt" ] && [ "$(minor "$mod_go")" = "$ci_go" ]; then S=ok M="CI's gofmt (Go $mod_go) via .dev.mk GOFMT; your go is ${v:-none}"; return; fi
  S=warn M="your Go is ${v:-missing}, CI formats with $ci_go"
  K=user F="download Go $mod_go (CI's minor) for its gofmt only; make fmt-check uses it (GOFMT in .dev.mk)"
}
f_gofmt() { fetch_go "$mod_go"; }

node_dir() { printf '%s/node' "$CACHE"; }
c_node() {
  REQ=1 K='' F='' L='make js-check, js-test, native-check; the UI harness'
  local v=''
  if have node; then v=$(node --version | sed 's/^v//'); fi
  local here=''; case "$(command -v node)" in "$CACHE"/*) here=', downloaded here (make: via .dev.mk)' ;; esac
  if [ -n "$v" ] && ver_ge "$v" "$ci_node"; then S=ok M="node $v (CI: $ci_node$here)"; return; fi
  if [ -x "$(node_dir)/bin/node" ]; then S=ok M="node $("$(node_dir)/bin/node" --version) from $(node_dir) (via .dev.mk)"; return; fi
  if [ -n "$v" ]; then S=miss M="node $v is older than CI's $ci_node"; else S=miss M="no node"; fi
  K=user F="download the newest Node $ci_node.x from nodejs.org into $(node_dir) (checksum-verified)"
}
f_node() {
  [ -n "$NODEARCH" ] || return 1
  local v os tmp f; os=$([ "$OS" = Darwin ] && echo darwin || echo linux)
  v=$(dl https://nodejs.org/dist/index.json | grep -o "\"version\":\"v$ci_node\.[0-9.]*\"" | head -n 1 | cut -d'"' -f4) || return 1
  [ -n "$v" ] || return 1
  f="node-$v-$os-$NODEARCH.tar.gz" tmp=$(mktemp -d)
  say "  node $v ← https://nodejs.org/dist/$v/$f"
  if dl -o "$tmp/$f" "https://nodejs.org/dist/$v/$f" && dl "https://nodejs.org/dist/$v/SHASUMS256.txt" | grep -q "^$(sha256 "$tmp/$f")  $f\$"; then
    rm -rf "$(node_dir)" && mkdir -p "$(node_dir)" && tar -xzf "$tmp/$f" -C "$(node_dir)" --strip-components=1
  else say "  ${RED}Node $v didn't download or its checksum didn't match${R}"; rm -rf "$tmp"; return 1; fi
  rm -rf "$tmp"
}

# userns: the load-bearing one (every sandbox, every isolated test)
in_single_uid_ns() { [ "$(wc -l </proc/self/uid_map 2>/dev/null)" = 1 ] && ! grep -q '^ *0 *0 ' /proc/self/uid_map; }
c_userns() {
  REQ=1 K='' F='' L='every sandbox: make dev, the isolated tests, the harness with isolation'
  if unshare -Ur true 2>/dev/null; then S=ok M="unprivileged user namespaces work"; return; fi
  S=miss M="unshare -Ur fails"
  local f
  for f in /proc/sys/kernel/apparmor_restrict_unprivileged_userns /proc/sys/kernel/unprivileged_userns_clone /proc/sys/user/max_user_namespaces; do
    [ -r "$f" ] && M="$M; $(basename "$f")=$(cat "$f")"
  done
  K=root F="lift the sysctls that block them (/etc/sysctl.d/99-xbin-dev.conf)"
}
r_userns() {
  local c=/etc/sysctl.d/99-xbin-dev.conf
  [ "$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null)" = 1 ] && sysctl_set kernel.apparmor_restrict_unprivileged_userns 0 "$c"
  [ "$(cat /proc/sys/kernel/unprivileged_userns_clone 2>/dev/null)" = 0 ] && sysctl_set kernel.unprivileged_userns_clone 1 "$c"
  [ "$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 1)" = 0 ] && sysctl_set user.max_user_namespaces 15000 "$c"
  return 0
}
sysctl_set() { # key value file (as root): persisted and applied
  touch "$3"
  if grep -q "^$1=" "$3"; then sed -i "s|^$1=.*|$1=$2|" "$3"; else printf '%s=%s\n' "$1" "$2" >>"$3"; fi
  sysctl -q -w "$1=$2" >/dev/null 2>&1 || true
}

c_subids() {
  REQ=0 K='' F='' L='range mode (a sandbox'"'"'s users map onto your sub-uid range): the isolated tests'"'"' range cases, rootless podman'
  local f miss=''
  for f in /etc/subuid /etc/subgid; do grep -Eq "^($USER_NAME|$(id -u "$USER_NAME")):" "$f" 2>/dev/null || miss="$miss $(basename "$f")"; done
  local nm; nm=$(command -v newuidmap || true)
  local helper=1; [ -n "$nm" ] && { [ -u "$nm" ] || { have getcap && getcap "$nm" 2>/dev/null | grep -q cap_setuid; }; } || helper=0
  if [ -z "$miss" ] && [ "$helper" = 1 ]; then
    S=ok M="$USER_NAME has sub-uid/gid ranges; newuidmap may use them"
    if in_single_uid_ns; then
      local u; u=$(awk '{print $1}' /proc/self/uid_map)
      S=warn M="$M — but this shell is inside a user namespace that maps only uid $u: range mode can't run from here (a normal login shell can)"; K=manual F="run the tests from a login shell"
    fi
    return
  fi
  local nh=''; [ "$helper" = 0 ] && nh='newuidmap missing or not setuid'
  S=miss M="${miss:+no range in$miss}${miss:+${nh:+; }}$nh"
  K=root F="give $USER_NAME $SUBID_COUNT sub-uids/gids after the highest range in use; make newuidmap setuid"
}
r_subids() {
  local f start
  start=$(awk -F: 'BEGIN{m=100000} {e=$2+$3; if (e>m) m=e} END{print m}' /etc/subuid /etc/subgid 2>/dev/null)
  for f in /etc/subuid /etc/subgid; do
    touch "$f"
    grep -Eq "^($USER_NAME|$(id -u "$USER_NAME")):" "$f" || printf '%s:%s:%s\n' "$USER_NAME" "$start" "$SUBID_COUNT" >>"$f"
  done
  local nm; nm=$(command -v newuidmap || true)
  [ -n "$nm" ] && [ ! -u "$nm" ] && ! { have getcap && getcap "$nm" | grep -q cap_setuid; } && chmod u+s "$nm" "$(command -v newgidmap)"
  return 0
}

c_fuse() {
  REQ=1 K='' F='' L='fuse-overlayfs roots, gocryptfs (encrypted resources), the containerfs suite'
  local m='' fm; fm=$(command -v fusermount3 || true)
  [ -e /dev/fuse ] || m="$m /dev/fuse missing;"
  { [ -n "$fm" ] && [ -u "$fm" ]; } || m="$m fusermount3 missing or not setuid;"
  grep -q '^user_allow_other' /etc/fuse.conf 2>/dev/null || m="$m no user_allow_other in /etc/fuse.conf (container-store tests);"
  if [ -z "$m" ]; then S=ok M="/dev/fuse, setuid fusermount3, user_allow_other"; return; fi
  m=${m# }; S=$([ -e /dev/fuse ] && [ -n "$fm" ] && echo warn || echo miss) M="${m%;}"
  K=root F="load fuse (and at boot), allow user_allow_other"
}
r_fuse() {
  modprobe fuse 2>/dev/null || true
  modload fuse
  grep -q '^user_allow_other' /etc/fuse.conf 2>/dev/null || printf 'user_allow_other\n' >>/etc/fuse.conf
  local fm; fm=$(command -v fusermount3 || true); [ -n "$fm" ] && [ ! -u "$fm" ] && chmod u+s "$fm"
  return 0
}
modload() { install -d /etc/modules-load.d && touch /etc/modules-load.d/xbin-dev.conf; grep -qx "$1" /etc/modules-load.d/xbin-dev.conf || printf '%s\n' "$1" >>/etc/modules-load.d/xbin-dev.conf; }

c_tun() {
  REQ=0 K='' F='' L='sandbox egress (the relay), the terminal internet scope tests'
  if [ -e /dev/net/tun ]; then S=ok M="/dev/net/tun present"; return; fi
  S=miss M="/dev/net/tun missing" K=root F="load tun (and at boot)"
}
r_tun() { modprobe tun 2>/dev/null || true; modload tun; }

kvm_rw() { { : <>/dev/kvm; } 2>/dev/null; }
c_kvm() {
  REQ=0 K='' F='' L='VM sandboxes on KVM (internal/vm, the VM runs of test/isolated); without it they run emulated, several times slower'
  local virt=''; grep -qw vmx /proc/cpuinfo 2>/dev/null && virt=kvm_intel; grep -qw svm /proc/cpuinfo 2>/dev/null && virt=kvm_amd
  if [ -e /dev/kvm ] && kvm_rw; then S=ok M="/dev/kvm is yours"; return; fi
  if [ -z "$virt" ] && [ ! -e /dev/kvm ]; then S=warn M="no hardware virtualization (or nested virtualization is off in this VM): VMs run emulated only"; K=manual F="enable VT-x/AMD-V (or nested virtualization on the host)"; return; fi
  if [ ! -e /dev/kvm ]; then S=miss M="/dev/kvm missing ($virt not loaded)"
  else S=miss M="/dev/kvm isn't readable+writable by $USER_NAME"; fi
  if getent group kvm >/dev/null 2>&1; then K=root F="load $virt (and at boot); add $USER_NAME to the kvm group (log in again after)"
  else K=root F="load $virt; no kvm group here — a udev rule makes /dev/kvm 0666 (as CI does)"; fi
}
r_kvm() {
  local virt=''; grep -qw vmx /proc/cpuinfo && virt=kvm_intel; grep -qw svm /proc/cpuinfo && virt=kvm_amd
  [ -n "$virt" ] && { modprobe "$virt" 2>/dev/null || true; modload "$virt"; }
  if getent group kvm >/dev/null 2>&1; then
    id -nG "$USER_NAME" | tr ' ' '\n' | grep -qx kvm || { usermod -aG kvm "$USER_NAME" && RELOGIN="$RELOGIN kvm"; }
  else
    install -d /etc/udev/rules.d && printf 'KERNEL=="kvm", MODE="0666"\n' >/etc/udev/rules.d/99-xbin-dev-kvm.rules
    have udevadm && { udevadm control --reload-rules; udevadm trigger --name-match=kvm; } 2>/dev/null
  fi
  return 0
}

delegation() { # the controllers a Delegate=yes scope of your systemd manager gets, or nothing
  systemd-run --user --scope -p Delegate=yes --quiet -- sh -c 'cat "/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup)/cgroup.controllers"' 2>/dev/null
}
c_cgroup() {
  REQ=0 K='' F='' L='the cgroup tests (internal/cgroup leaves, the tile sandboxes'"'"' leaves and memory.max kills, flow C) — they skip without a delegated cgroup'
  if [ ! -f /sys/fs/cgroup/cgroup.controllers ]; then S=miss M="cgroup v2 isn't the unified hierarchy"; K=manual F="boot with systemd.unified_cgroup_hierarchy=1"; return; fi
  if ! have systemd-run; then S=miss M="no systemd-run"; K=manual F="the cgroup tests need a systemd user manager"; return; fi
  local c; c=$(delegation)
  if [ -z "$c" ]; then
    S=miss M="your systemd user manager can't make a delegated scope (systemctl --user unreachable?)"
    K=root F="loginctl enable-linger $USER_NAME, and delegate cpu, io, memory, pids to user@.service (log in again after)"; return
  fi
  local m='' x; for x in cpu memory pids; do case " $c " in *" $x "*) ;; *) m="$m $x" ;; esac; done
  if [ -z "$m" ]; then S=ok M="Delegate=yes scopes get: $c (make integration runs the cgroup tests in one)"; return; fi
  S=warn M="a delegated scope lacks:$m" K=root F="delegate cpu, cpuset, io, memory, pids to user@.service (log in again after)"
}
r_cgroup() {
  loginctl enable-linger "$USER_NAME" 2>/dev/null || true
  have systemctl || return 1
  install -d /etc/systemd/system/user@.service.d
  printf '[Service]\nDelegate=cpu cpuset io memory pids\n' >/etc/systemd/system/user@.service.d/xbin-dev-delegate.conf
  systemctl daemon-reload
  RELOGIN="$RELOGIN cgroup"
}

c_inotify() {
  REQ=0 K='' F='' L='big workspaces (make dev, the harness) stop seeing saves'
  local w; w=$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null || echo 0)
  if [ "$w" -ge 524288 ]; then S=ok M="max_user_watches=$w"; return; fi
  S=warn M="fs.inotify.max_user_watches=$w" K=root F="raise it to 524288"
}
r_inotify() { sysctl_set fs.inotify.max_user_watches 524288 /etc/sysctl.d/99-xbin-dev.conf; }

# AppArmor confining fusermount3 (Ubuntu): encrypted resources mount under
# a workspace's .xbin/resenc — the dev workspaces (the repo's devws*, the
# tests' $TMPDIR) must be under a path the profile allows.
AA_PROFILE=/etc/apparmor.d/fusermount3 AA_LOCAL=/etc/apparmor.d/local/fusermount3
AA_BEGIN='# BEGIN xbin dev (hack/dev-setup.sh)' AA_END='# END xbin dev'
aa_on() { [ "$(cat /sys/module/apparmor/parameters/enabled 2>/dev/null)" = Y ] && [ -f "$AA_PROFILE" ]; }
aa_covered() { # dir: under a path the stock profile allows
  local g
  case "$1" in /home/*/*|/root/*) g='@{HOME}/' ;; /mnt/*) g='/mnt/' ;; /media/*) g='/media/' ;; /tmp/*) g='/tmp/' ;; *) return 1 ;; esac
  grep -qF -- "-> $g" "$AA_PROFILE"
}
aa_dirs() { printf '%s\n%s\n' "$repo" "$(cd "${TMPDIR:-/tmp}" && pwd -P)"; }
c_apparmor() {
  REQ=0 K='' F='' L='encrypted resources in make dev and the tests (gocryptfs through fusermount3)'
  if ! aa_on; then S=na M="AppArmor doesn't confine fusermount3 here"; return; fi
  local d un=''
  while read -r d; do aa_covered "$d" || grep -qF "$d/" "$AA_LOCAL" 2>/dev/null || un="$un $d"; done < <(aa_dirs)
  if [ -z "$un" ]; then S=ok M="fusermount3 may mount under the repo and \$TMPDIR"; return; fi
  S=miss M="fusermount3's profile forbids FUSE mounts under$un" K=root F="allow them in $AA_LOCAL (a marked block) and reload the profile"
}
r_apparmor() {
  local d tmp; tmp=$(mktemp)
  { [ -f "$AA_LOCAL" ] && awk -v b="$AA_BEGIN" -v e="$AA_END" 'index($0,b)==1{s=1;next} s&&index($0,e)==1{s=0;next} !s' "$AA_LOCAL"
    printf '%s\n' "$AA_BEGIN"
    while read -r d; do aa_covered "$d" && continue
      printf 'mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> "%s/**/",\numount "%s/**/",\n' "$d" "$d"
    done < <(aa_dirs)
    printf '%s\n' "$AA_END"; } >"$tmp"
  install -d /etc/apparmor.d/local && install -m 0644 "$tmp" "$AA_LOCAL" && rm -f "$tmp"
  apparmor_parser -r -W "$AA_PROFILE"
}

c_wsl() {
  REQ=0 K='' F='' L='WSL2 needs systemd as init for the user manager and cgroup delegation'
  if [ "$WSL" = 0 ]; then S=na M="not WSL"; return; fi
  if [ -d /run/systemd/system ]; then S=ok M="WSL2 with systemd"; return; fi
  S=miss M="WSL2 without systemd" K=manual F="add [boot] systemd=true to /etc/wsl.conf, then wsl --shutdown"
}

c_gomodcache() {
  REQ=1 K='' F='' L='go builds that need a module not in the cache'
  have go || { S=na M="no go yet"; return; }
  local d e; d=$(go env GOMODCACHE)
  if grep -q '^export GOMODCACHE' "$DEVMK" 2>/dev/null; then S=ok M="$CACHE/gomodcache (via .dev.mk)"; return; fi
  e=$d; while [ ! -e "$e" ] && [ "$e" != / ]; do e=$(dirname "$e"); done # the nearest existing ancestor
  if [ -w "$e" ]; then S=ok M="$d is writable"; return; fi
  S=warn M="$d is read-only here" K=user F="use $CACHE/gomodcache (GOMODCACHE + GOFLAGS=-modcacherw in .dev.mk)"
}
f_gomodcache() { mkdir -p "$CACHE/gomodcache"; GOMODCACHE_OVERRIDE=$CACHE/gomodcache; }

helpers_status() { # group → ok | stale | missing
  # shellcheck source=hack/helpers-lib.sh
  ( . "$repo/hack/helpers-lib.sh" >/dev/null 2>&1
    k=$(helpers_key "$1" 2>/dev/null) || { echo missing; exit; }
    [ -f "$repo/bin/.helpers-$1" ] || { echo missing; exit; }
    [ "$(cut -d' ' -f1 "$repo/bin/.helpers-$1")" = "$k" ] && echo ok || echo stale )
}
c_helpers-containerfs() {
  REQ=1 K='' F='' L='gocryptfs and fuse-overlayfs: encrypted resources and fuse roots in the tests and make dev'
  local s; s=$(helpers_status containerfs)
  case $s in ok) S=ok M="bin/ gocryptfs, fuse-overlayfs (current)" ;; stale) S=warn M="bin/ helpers are from older build inputs" ;; *) S=miss M="no bin/gocryptfs, bin/fuse-overlayfs" ;; esac
  [ "$S" = ok ] || { K=user F="make helpers HELPERS=containerfs (the pinned prebuilt set; builds with docker when unpublished)"; }
}
f_helpers-containerfs() { make -C "$repo" helpers HELPERS=containerfs; }
c_helpers-vm() {
  REQ=0 K='' F='' L='VM sandboxes: the guest kernel, mkfs.erofs, QEMU for the emulated runs'
  local s; s=$(helpers_status vm)
  case $s in ok) S=ok M="bin/ vmlinux, mkfs.erofs, qemu, vhost-device-vsock (current)" ;; stale) S=warn M="bin/ vm helpers are from older build inputs" ;; *) S=miss M="no vm helpers in bin/" ;; esac
  [ "$S" = ok ] || { K=user F="make helpers HELPERS=vm"; }
}
f_helpers-vm() { make -C "$repo" helpers HELPERS=vm; }
c_firecracker() {
  REQ=0 K='' F='' L='VM sandboxes on KVM'
  if [ -x "$repo/bin/firecracker" ]; then S=ok M="bin/firecracker"; return; fi
  S=miss M="no bin/firecracker" K=user F="hack/fetch-firecracker.sh bin (the pinned release)"
}
f_firecracker() { "$repo/hack/fetch-firecracker.sh" "$repo/bin"; }

c_rootfs() {
  REQ=0 K='' F='' L='make dev, the isolated tests and harness (they skip or build it), agents in sandboxes'
  local os="$repo/.rootfs/etc/os-release" want have_v
  # the build records the hash of what built it (hack/build-rootfs.sh)
  want=$(cat "$repo/docker/rootfs.Dockerfile" "$repo/hack/build-rootfs.sh" | { if have sha256sum; then sha256sum; else shasum -a 256; fi; } | cut -c1-12)
  have_v=$(cat "$repo/.rootfs/etc/xbin-base-version" 2>/dev/null)
  if [ -e "$os" ] && [ "$have_v" = "$want" ]; then S=ok M=".rootfs is current (base $want)"; return; fi
  if [ -e "$os" ]; then S=warn M=".rootfs was built from another Dockerfile (base ${have_v:-unknown}, now $want)"; else S=miss M="no .rootfs"; fi
  K=user F="make rootfs (docker or podman; several GB, a while — once)"
}
f_rootfs() {
  local eng=docker; { have docker && docker info >/dev/null 2>&1; } || eng=podman
  DOCKER=$eng make -C "$repo" rootfs
}

prev_bin() { printf '%s/downgrade/%s/bin/xbind' "$CACHE" "$ci_prev"; }
c_downgrade() {
  REQ=0 K='' F='' L="the downgrade tests (test/downgrade_test.go) skip without the previous release's xbind"
  [ "$OS" = Linux ] && [ "$GOARCH" = amd64 ] || { S=na M="the release bundles are linux-amd64"; return; }
  if [ -x "$(prev_bin)" ]; then S=ok M="$ci_prev's xbind ($(prev_bin))"; return; fi
  S=miss M="no $ci_prev xbind" K=user F="download bin/xbind from the $ci_prev bundle (only the head of it, as CI does)"
}
f_downgrade() {
  local d; d=$(dirname "$(dirname "$(prev_bin)")"); mkdir -p "$d"
  # tar stops at bin/xbind (the bundle's first entry), so curl ends on a
  # closed pipe: only the file says whether it worked
  dl "https://github.com/xbin-dev/xbin/releases/download/$ci_prev/xbin-$ci_prev-linux-amd64.tar.zst" 2>/dev/null | zstd -dc 2>/dev/null |
    tar -x --occurrence=1 -C "$d" bin/xbind 2>/dev/null
  [ -x "$(prev_bin)" ]
}

pw_dir() { # the Playwright install make would use
  if [ -n "${PLAYWRIGHT_DIR:-}" ] && [ -d "$PLAYWRIGHT_DIR/node_modules/playwright" ]; then echo "$PLAYWRIGHT_DIR"
  else echo "$CACHE/playwright"; fi
}
node_bin() { if [ -x "$(node_dir)/bin/node" ]; then echo "$(node_dir)/bin/node"; else command -v node || true; fi; }
c_playwright() {
  REQ=0 K='' F='' L='the UI harness (hack/ui-harness): the only JS regression tests'
  local n; n=$(node_bin); [ -n "$n" ] || { S=miss M="no node" K=user F="after node: npm install playwright@$PLAYWRIGHT_VERSION, chromium"; return; }
  local d; d=$(pw_dir)
  if [ ! -d "$d/node_modules/playwright" ]; then S=miss M="no Playwright" K=user F="npm install playwright@$PLAYWRIGHT_VERSION into $CACHE/playwright, and its Chromium"; return; fi
  local out
  if out=$(cd "$d" && "$n" -e 'setTimeout(()=>{console.log("no answer in 60 s");process.exit(1)},60000).unref();require("playwright").chromium.launch().then(b=>b.close()).then(()=>console.log("ok"),e=>{console.log(String(e.message).split("\n")[0]);process.exit(1)})' 2>&1); then
    local ver; ver=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$d/node_modules/playwright/package.json" | head -n 1)
    S=ok M="Playwright $ver in $d launches Chromium"; return
  fi
  S=miss M="Chromium doesn't launch: $(printf '%s' "$out" | head -c 160)"
  if printf '%s' "$out" | grep -qi "executable doesn't exist\|playwright install"; then K=user F="playwright install chromium"
  elif [ "$OS" = Linux ] && { [ "$PKG" = apt ] || [ "$PKG" = dnf ]; }; then K=post F="playwright install-deps chromium (its system libraries, as root)"
  else K=manual F="install Chromium's system libraries (see Playwright's docs for this distro)"; fi
}
f_playwright() {
  local n d; n=$(node_bin); d=$CACHE/playwright
  [ -n "$n" ] || return 1
  local npm; npm="$(dirname "$n")/npm"
  if [ ! -d "$(pw_dir)/node_modules/playwright" ]; then
    mkdir -p "$d" && (cd "$d" && PATH="$(dirname "$n"):$PATH" "$npm" install --no-audit --no-fund --silent "playwright@$PLAYWRIGHT_VERSION") || return 1
  fi
  d=$(pw_dir)
  (cd "$d" && PATH="$(dirname "$n"):$PATH" ./node_modules/.bin/playwright install chromium)
}
r_playwright() {
  local d; d=${XBIN_DEV_PWDIR:-}; [ -n "$d" ] && [ -x "$d/node_modules/.bin/playwright" ] || return 0
  PATH="$(dirname "${XBIN_DEV_NODE:-node}"):$PATH" "$d/node_modules/.bin/playwright" install-deps chromium
}

c_swift() {
  REQ=0 K='' F='' L='make swift-test and swift-stubcheck (the app on Linux); make check skips them without swift'
  local v=''; [ -x "$SWIFTLY_BIN/swift" ] && v=$("$SWIFTLY_BIN/swift" --version 2>/dev/null | sed -n 's/.*Swift version \([0-9.]*\).*/\1/p' | head -n 1)
  if [ -n "$v" ] && { [ "$v" = "$swift_want" ] || [ "$v" = "${swift_want%.0}" ]; }; then S=ok M="Swift $v in $SWIFTLY_BIN (on make's PATH via .dev.mk)"; return; fi
  if [ -n "$v" ]; then S=miss M="Swift $v, CI's is $swift_want"; else S=miss M="no Swift $swift_want (swiftly)"; fi
  local deps; deps=$("$repo/native/ios/scripts/ci-linux-swift.sh" --missing-deps 2>/dev/null || true)
  if [ -n "$deps" ]; then want_pkg $deps; fi
  K=user F="native/ios/scripts/ci-linux-swift.sh (swiftly + Swift $swift_want, checksum- and signature-verified)${deps:+; its packages as root:$deps}"
}
f_swift() { "$repo/native/ios/scripts/ci-linux-swift.sh" --post-install; }

c_hooks() {
  REQ=0 K='' F='' L='the pre-commit hook (fmt-check, js-check, large-files) catches what CI would'
  if [ "$(git -C "$repo" config core.hooksPath 2>/dev/null)" = .githooks ]; then S=ok M="pre-commit hook active"; return; fi
  S=warn M="the repo's hooks aren't active" K=user F="make hooks (git config core.hooksPath .githooks)"
}
f_hooks() { make -C "$repo" hooks >/dev/null; }

c_xcode() {
  REQ=0 K='' F='' L='the iOS app: builds, snapshots, UI tests (native/AGENTS.md)'
  if "$repo/native/ios/scripts/mac-setup.sh" --check --no-runner >/dev/null 2>&1; then S=ok M="mac-setup.sh --check passes"; return; fi
  S=miss M="mac-setup.sh --check reports gaps (run it for the list)" K=user F="native/ios/scripts/mac-setup.sh --no-runner (Xcode itself is yours to install)"
}
f_xcode() { "$repo/native/ios/scripts/mac-setup.sh" --no-runner; }

c_linux() {
  REQ=0 S=warn K=manual L='make integration, make dev, the isolated harness: xbind'"'"'s sandboxes are Linux'"'"'s'
  M="macOS runs make check's Go, JS and native parts; the sandboxed parts need Linux"
  F="a Linux machine, or a VM (e.g. brew install lima && limactl start template://ubuntu), running this script there"
}

# The toolchains this script downloaded, on this run's PATH where yours is
# missing or too old — as make gets them through .dev.mk.
use_cached_toolchains() {
  local g; g=$(go_cached "$mod_go")/bin
  if [ -x "$g/go" ] && { ! have go || ! ver_ge "$(gover)" "$mod_go"; }; then PATH="$g:$PATH"; fi
  local n; n=$(node_dir)/bin
  if [ -x "$n/node" ] && { ! have node || ! ver_ge "$(node --version | sed 's/^v//')" "$ci_node"; }; then PATH="$n:$PATH"; fi
  export PATH
}

# ---- the check round ----
ids() { if [ "$OS" = Darwin ]; then echo "$MAC_ITEMS"; else echo "$LINUX_ITEMS"; fi; }
cid() { printf '%s' "$1" | tr '-' '_'; } # a variable-safe id
run_checks() { # → the table; sets R_<id>, K_<id>, F_<id>, REQ_<id>
  local id v mark
  use_cached_toolchains
  PKGS='' MISSING_REQ=0
  for id in $(ids); do
    wanted "$id" || continue
    S=na M='' L='' K='' F='' REQ=0
    "c_$id"
    v=$(cid "$id")
    printf -v "R_$v" '%s' "$S"; printf -v "K_$v" '%s' "$K"; printf -v "F_$v" '%s' "$F"; printf -v "REQ_$v" '%s' "$REQ"
    case $S in
      ok) mark="${G}✓${R}" ;; na) mark="${D}-${R}" ;; warn) mark="${Y}!${R}" ;; *) mark="${RED}✗${R}" ;;
    esac
    printf '  %s %-20s %s\n' "$mark" "$id" "$M"
    if [ "$S" = warn ] || [ "$S" = miss ]; then
      printf '    %s%s%s\n' "$D" "without it: $L" "$R"
      [ "$S" = miss ] && [ "$REQ" = 1 ] && MISSING_REQ=1
    fi
  done
}
plan_of() { # kind → the ids whose fix is of that kind
  local id v out=''
  for id in $(ids); do
    wanted "$id" || continue
    v=$(cid "$id"); eval "local r=\${R_$v:-} k=\${K_$v:-}"
    # shellcheck disable=SC2154 # set by the eval above
    { [ "$r" = warn ] || [ "$r" = miss ]; } && [ "$k" = "$1" ] && out="$out $id"
  done
  echo "${out# }"
}
fix_of() { local v; v=$(cid "$1"); eval "printf '%s' \"\${F_$v:-}\""; }

# ---- .dev.mk ----
write_devmk() {
  local tmp; tmp=$(mktemp)
  {
    echo "# hack/dev-setup.sh wrote this (gitignored): this machine's test environment,"
    echo "# which the Makefile includes. Run the script again to refresh it."
    [ -e "$repo/.rootfs/etc/os-release" ] && echo "export XBIN_TEST_ROOTFS := $repo/.rootfs"
    [ -x "$repo/bin/gocryptfs" ] && echo "export XBIN_GOCRYPTFS := $repo/bin/gocryptfs"
    [ -x "$repo/bin/fuse-overlayfs" ] && echo "export XBIN_FUSE_OVERLAYFS := $repo/bin/fuse-overlayfs"
    [ -x "$(prev_bin)" ] && echo "export XBIN_DOWNGRADE_BIN := $(prev_bin)"
    # the VM pieces, for the daemons test/ starts itself (flow C's VM half;
    # test/isolated's harness finds bin/ on its own)
    local kv
    for kv in XBIN_VM_KERNEL:vmlinux XBIN_VM_AGENT:xbin-vmagent XBIN_MKFS_EROFS:mkfs.erofs XBIN_FIRECRACKER:firecracker \
      XBIN_QEMU:qemu-system-x86_64 XBIN_VHOST_VSOCK:vhost-device-vsock; do
      [ -f "$repo/bin/${kv#*:}" ] && echo "export ${kv%%:*} := $repo/bin/${kv#*:}"
    done
    [ -d "$(pw_dir)/node_modules/playwright" ] && echo "export PLAYWRIGHT_DIR := $(pw_dir)"
    if [ -n "${GOMODCACHE_OVERRIDE:-}" ] || { [ -d "$CACHE/gomodcache" ] && have go && [ ! -w "$(go env GOMODCACHE)" ]; }; then
      echo "export GOMODCACHE := $CACHE/gomodcache"; echo "export GOFLAGS := -modcacherw"
    fi
    local v; v=$(gover)
    if [ "$(minor "${v:-0}")" != "$ci_go" ] && [ -x "$(go_cached "$mod_go")/bin/gofmt" ]; then echo "GOFMT := $(go_cached "$mod_go")/bin/gofmt"; fi
    local p=''
    [ -x "$SWIFTLY_BIN/swift" ] && p="$SWIFTLY_BIN"
    # the downloaded Node and Go, where this run put them first (use_cached_toolchains)
    case "$(command -v node)" in "$CACHE"/*) p="${p:+$p:}$(node_dir)/bin" ;; esac
    case "$(command -v go)" in "$CACHE"/*) p="${p:+$p:}$(go_cached "$mod_go")/bin" ;; esac
    [ -n "$p" ] && echo "export PATH := $p:\$(PATH)"
  } >"$tmp"
  mv "$tmp" "$DEVMK"
}
env_out() { # .dev.mk as shell exports
  [ -f "$DEVMK" ] || { echo "# no $DEVMK yet: run hack/dev-setup.sh" >&2; exit 1; }
  sed -n 's/^export \([A-Z_]*\) := \(.*\)$/export \1="\2"/p' "$DEVMK" | sed 's/\$(PATH)/$PATH/'
}

# ---- root phase (under sudo) ----
root_phase() {
  [ "$(id -u)" = 0 ] || die "--root-phase runs as root"
  local id
  if [ -n "${XBIN_DEV_PKGS:-}" ] && [ -n "$PKG" ] && [ "$PKG" != brew ]; then
    say "  packages:$XBIN_DEV_PKGS"
    # shellcheck disable=SC2086 # a list of package names
    if ! pkg_install $XBIN_DEV_PKGS; then
      say "  ${RED}the package install failed${R}"
      case "$PKG" in
        pacman) say "  (a stale package database 404s: sudo pacman -Syu — a full upgrade — then run this again)" ;;
        apt) say "  (apt-get update ran; see the error above)" ;;
      esac
    fi
  fi
  for id in $ROOTPHASE; do
    [ "$id" = tools ] && continue
    say "  $id"
    "r_$id" || say "  ${RED}$id didn't apply${R}"
  done
  [ -n "$RELOGIN" ] && printf '%s\n' "$RELOGIN" >"${XBIN_DEV_RELOGIN:-/dev/null}"
  return 0
}
sudo_phase() { # ids…
  local ids="$*" relog; relog=$(mktemp)
  [ -z "$ids" ] && [ -z "${PKGS# }" ] && return 0
  if [ "$NOROOT" = 1 ]; then
    head1 "as root (--no-root): run this yourself — it does exactly the root steps above"
    local pk=${PKGS# }
    say "  sudo env XBIN_DEV_USER=$USER_NAME${pk:+ XBIN_DEV_PKGS='$pk'} bash $repo/hack/dev-setup.sh --root-phase '$ids'"
    return 0
  fi
  if [ "$(id -u)" = 0 ]; then
    XBIN_DEV_PKGS="$PKGS" XBIN_DEV_RELOGIN="$relog" ROOTPHASE="$ids" root_phase
  else
    have sudo || die "the root steps need sudo (or run them yourself: --no-root)"
    sudo env XBIN_DEV_USER="$USER_NAME" XBIN_DEV_PKGS="$PKGS" XBIN_DEV_RELOGIN="$relog" \
      XBIN_DEV_PWDIR="${XBIN_DEV_PWDIR:-}" XBIN_DEV_NODE="${XBIN_DEV_NODE:-}" TMPDIR="${TMPDIR:-/tmp}" \
      bash "$repo/hack/dev-setup.sh" --root-phase "$ids"
  fi
  RELOGIN="$RELOGIN $(cat "$relog" 2>/dev/null)"; rm -f "$relog"
}

# ---- main ----
if [ -n "$ROOTPHASE" ]; then root_phase; exit 0; fi
if [ "$MODE" = print-env ]; then env_out; exit 0; fi

head1 "xbin dev setup: $(uname -sr), ${PKG:-no package manager}; Go $mod_go (CI $ci_go), Node $ci_node, Swift ${swift_want:-?}"
run_checks
if [ "$MODE" = check ]; then
  [ -f "$DEVMK" ] || say "  ${D}(no .dev.mk yet: the full run writes it)${R}"
  exit "$MISSING_REQ"
fi

roots=$(plan_of root) posts=$(plan_of post) users=$(plan_of user) manual=$(plan_of manual)
if [ -z "$roots$posts$users" ] && [ -z "${PKGS# }" ]; then
  write_devmk
  head1 "nothing to fix — .dev.mk refreshed"
  [ -n "$manual" ] && for id in $manual; do say "  ${Y}by hand${R} $id: $(fix_of "$id")"; done
  exit 0
fi

head1 "plan"
[ -n "${PKGS# }" ] && say "  as root:  packages:$PKGS"
for id in $roots; do [ "$id" = tools ] || say "  as root:  $id — $(fix_of "$id")"; done
for id in $users; do say "  as you:   $id — $(fix_of "$id")"; done
for id in $posts; do say "  as root, after: $id — $(fix_of "$id")"; done
for id in $manual; do say "  by hand:  $id — $(fix_of "$id")"; done
sudo_note=''; [ -n "$roots$posts${PKGS# }" ] && [ "$NOROOT" = 0 ] && sudo_note=' (sudo asks once for the root ones)'
ask "Apply the steps above$sudo_note?" || { say "nothing changed."; exit 1; }

if [ -n "$roots" ] || [ -n "${PKGS# }" ]; then [ "$NOROOT" = 1 ] || head1 "root steps (sudo)"; sudo_phase $roots; fi
if [ -n "$users" ]; then
  head1 "your steps"
  for id in $users; do
    say "  ${B}$id${R}: $(fix_of "$id")"
    "f_$id" || say "  ${RED}$id didn't finish (see above)${R}"
  done
fi
# Chromium's system libraries can only be judged once Playwright is here.
if wanted playwright && [ "$OS" = Linux ]; then
  c_playwright
  [ "$K" = post ] && case " $posts " in *" playwright "*) ;; *) posts="${posts:+$posts }playwright" ;; esac
fi
if [ -n "$posts" ]; then
  [ "$NOROOT" = 1 ] || head1 "root steps after yours (sudo)"
  XBIN_DEV_PWDIR=$(pw_dir) XBIN_DEV_NODE=$(node_bin) PKGS='' sudo_phase $posts
fi
write_devmk

head1 "check again"
run_checks
say ""
say "  wrote $DEVMK (make reads it; for your shell: eval \"\$(hack/dev-setup.sh --env)\")"
for id in $manual; do say "  ${Y}by hand${R} $id: $(fix_of "$id")"; done
case "$RELOGIN" in *[a-z]*) say "  ${Y}log out and in again${R} (or reboot) for:$RELOGIN — group and delegation changes reach new sessions only" ;; esac
exit "$MISSING_REQ"
