#!/usr/bin/env bash
# native/ios/scripts/e2e-xbind.sh — the xbind the UI tests (native/ios/
# UITests) run against, on this (Linux) box: a fresh workspace with the
# native counter (examples/counter-go, native.js included) and the scripted
# "fake" agent, started the way the UI harness starts it (hack/ui-harness/
# run.sh). mac-remote.sh e2e starts it, tunnels its port to the Mac and
# hands the URL and the owner token to the tests.
#
#   e2e-xbind.sh start [--port P]   build bin/{xbind,bx,fakeacp}, init a fresh
#                                   workspace, start xbind on 127.0.0.1:P and
#                                   wait until the counter's backend answers
#   e2e-xbind.sh stop               stop it (and drop any mount it left)
#   e2e-xbind.sh env                XBIN_E2E_URL=… and XBIN_E2E_TOKEN=… lines
#   e2e-xbind.sh smoke              what the UI tests need of the server,
#                                   checked over HTTP: the token, the counter
#                                   (read, +1), the fake agent answering
#
#   XBIN_E2E_PORT       default 9871 (the tunnel keeps the same port on the
#                       Mac, so the workspace's origin is the same on both
#                       sides: http://127.0.0.1:P)
#   XBIN_E2E_DIR        default ${TMPDIR:-/tmp}/xbin-e2e: ws/ (the workspace),
#                       xbind.log, xbind.pid, env
#   XBIN_E2E_XBIND_ARGS extra xbind flags, e.g. "--isolate --rootfs …"
#
# The flags, for doing it by hand: `xbind init <ws>`; cp -r
# examples/counter-go <ws>/apps/counter; XBIN_AGENT_FAKE=bin/fakeacp
# XBIN_BIN=bin XBIN_SDK_PATH=sdk bin/xbind --dev --dev-overlay
# workspace-template --workspace <ws> --listen 127.0.0.1:P --external-url
# http://127.0.0.1:P; the owner token is <ws>/.xbin/token (--dev also
# seeds admin/admin).
set -euo pipefail

repo=$(cd "$(dirname "$0")/../../.." && pwd)
cmd=${1:-}
[ $# -gt 0 ] && shift
port=${XBIN_E2E_PORT:-9871}
while [ $# -gt 0 ]; do
  case $1 in
  --port) port=$2; shift 2 ;;
  *) echo "e2e-xbind: unknown argument $1" >&2; exit 2 ;;
  esac
done
dir=${XBIN_E2E_DIR:-${TMPDIR:-/tmp}/xbin-e2e}
ws=$dir/ws
url="http://127.0.0.1:$port"

say() { echo "e2e-xbind: $*" >&2; }

running() { [ -f "$dir/xbind.pid" ] && kill -0 "$(cat "$dir/xbind.pid")" 2>/dev/null; }

stop() {
  if [ -f "$dir/xbind.pid" ]; then
    pid=$(cat "$dir/xbind.pid")
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      # xbind unmounts encrypted resources on its way out; give it the time.
      for _ in $(seq 1 40); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$dir/xbind.pid"
  fi
  # A killed xbind can leave FUSE mounts under the workspace; a stale one
  # makes the next start's rm -rf fail.
  if [ -r /proc/self/mounts ]; then
    { grep -F " $ws/" /proc/self/mounts 2>/dev/null || true; } | cut -d' ' -f2 | while read -r m; do
      fusermount3 -uz "$m" 2>/dev/null || fusermount -uz "$m" 2>/dev/null || true
    done
  fi
}

token() { tr -d '[:space:]' <"$ws/.xbin/token"; }

# api <method> <path> [body] — the workspace API as the owner; prints the
# body, fails on a non-2xx.
api() {
  local m=$1 p=$2
  if [ $# -gt 2 ]; then
    curl -fsS -X "$m" -H "Authorization: Bearer $(token)" -H 'Content-Type: application/json' -d "$3" "$url$p"
  else
    curl -fsS -X "$m" -H "Authorization: Bearer $(token)" "$url$p"
  fi
}

wait_for() { # wait_for <seconds> <what> <command…>
  local secs=$1 what=$2 i=0
  shift 2
  until "$@" >/dev/null 2>&1; do
    i=$((i + 1))
    if [ "$i" -gt $((secs * 2)) ]; then
      say "$what did not come up in ${secs}s — $dir/xbind.log:"
      tail -n 30 "$dir/xbind.log" >&2 || true
      return 1
    fi
    sleep 0.5
  done
}

# node: open a shell on apps/welcome, wait for the prompt, type what
# XbinE2ETests.test04TerminalEcho types, wait for the title to come back.
term_check='
const [url, token] = process.argv.slice(1);
const ws = new WebSocket(url.replace(/^http/, "ws") + "/ws/term?cwd=apps/welcome", { headers: { Authorization: "Bearer " + token } });
ws.binaryType = "arraybuffer";
let out = "", sent = false, sid = "";
const done = (code) => fetch(url + "/ws/term?session=" + sid, { method: "DELETE", headers: { Authorization: "Bearer " + token } }).finally(() => process.exit(code));
setTimeout(() => done(1), 20000);
ws.onerror = () => process.exit(1);
ws.onmessage = (e) => {
  if (typeof e.data === "string") {
    const m = JSON.parse(e.data);
    if (m.op === "session") { sid = m.id; ws.send(JSON.stringify({ op: "resize", cols: 80, rows: 24 })); }
    return;
  }
  out += new TextDecoder().decode(e.data);
  if (!sent && out.length > 0) {
    sent = true;
    setTimeout(() => ws.send(new TextEncoder().encode("printf \x27\\033]0;xbin-e2e-%d\\007\x27 $((40+2))\r")), 500);
  }
  if (out.includes("\x1b]0;xbin-e2e-42\x07")) done(0);
};
'

case $cmd in
start)
  command -v go >/dev/null 2>&1 || { say "needs go"; exit 1; }
  stop
  mkdir -p "$dir"
  say "building bin/xbind, bin/bx, bin/fakeacp"
  (cd "$repo" && go build -o bin/xbind ./cmd/xbind && CGO_ENABLED=0 go build -o bin/bx ./cmd/bx &&
    go build -o bin/fakeacp ./hack/fakeacp)
  rm -rf "$ws"
  "$repo/bin/xbind" init "$ws" >/dev/null
  cp -r "$repo/examples/counter-go" "$ws/apps/counter"
  [ -f "$ws/apps/counter/native.js" ] || { say "examples/counter-go has no native.js"; exit 1; }
  # XBIN_AGENT_FAKE registers the scripted "fake" agent provider (D74),
  # XBIN_BIN is where the daemon finds the bx it binds in as the agent
  # host, XBIN_SDK_PATH builds the counter's Go backend against this sdk/.
  extra=()
  # shellcheck disable=SC2206 # extra flags, split on purpose
  [ -n "${XBIN_E2E_XBIND_ARGS:-}" ] && extra=(${XBIN_E2E_XBIND_ARGS})
  (cd "$repo" && XBIN_AGENT_FAKE="$repo/bin/fakeacp" XBIN_BIN="$repo/bin" XBIN_SDK_PATH="$repo/sdk" \
    nohup "$repo/bin/xbind" --dev --dev-overlay "$repo/workspace-template" --workspace "$ws" \
    --listen "127.0.0.1:$port" --external-url "$url" ${extra[@]+"${extra[@]}"} \
    >"$dir/xbind.log" 2>&1 </dev/null &
    echo $! >"$dir/xbind.pid")
  wait_for 30 "xbind" curl -fsS -o /dev/null "$url/healthz"
  # The counter's Go backend builds on first use (a cold build can take a
  # minute or two); the UI tests should not wait for it.
  wait_for 240 "the counter's backend" api GET /api/apps/counter/count
  {
    echo "XBIN_E2E_URL=$url"
    echo "XBIN_E2E_TOKEN=$(token)"
  } >"$dir/env"
  chmod 600 "$dir/env"
  say "up: $url (workspace $ws, log $dir/xbind.log); env in $dir/env"
  ;;
stop)
  stop
  say "stopped"
  ;;
env)
  running || { say "not running ($dir)"; exit 1; }
  cat "$dir/env"
  ;;
smoke)
  running || { say "not running ($dir)"; exit 1; }
  fail=0
  check() { if "$@" >/dev/null 2>&1; then echo "ok   $what"; else echo "FAIL $what"; fail=1; fi; }
  what="the token reads whoami"
  check api GET /api/xbin/whoami
  what="apps/counter's runtime document (/c/apps/counter/?native=1)"
  check curl -fsS -o /dev/null -H "Authorization: Bearer $(token)" "$url/c/apps/counter/?native=1"
  what="apps/welcome, the web tile, is served"
  check curl -fsS -o /dev/null -H "Authorization: Bearer $(token)" "$url/c/apps/welcome/"
  n=$(api GET /api/apps/counter/count | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')
  api POST /api/apps/counter/count >/dev/null
  m=$(api GET /api/apps/counter/count | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')
  what="the counter's +1 ($n → $m)"
  check [ "$m" = $((n + 1)) ]
  has_fake() { api GET /api/xbin/agent/providers | grep -Eq '"id": ?"fake"'; }
  what="the fake agent is a provider"
  check has_fake
  sid=$(api POST /api/xbin/term/sessions '{"cwd":"apps/welcome","kind":"agent","provider":"fake"}' |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
  # the session starts, then takes one prompt
  for _ in $(seq 1 40); do api POST "/api/xbin/term/sessions/$sid/prompt" '{"text":"hello from the smoke test"}' >/dev/null 2>&1 && break; sleep 0.5; done
  got=""
  for _ in $(seq 1 40); do
    got=$(api GET "/api/xbin/term/sessions/$sid/events" 2>/dev/null || true)
    case $got in *"echo: hello from the smoke test"*) break ;; esac
    sleep 0.5
  done
  what="an agent session on apps/welcome answers (echo: …)"
  case $got in *"echo: hello from the smoke test"*) check true ;; *) check false ;; esac
  api DELETE "/api/xbin/term/sessions/$sid" >/dev/null 2>&1 || true
  # The terminal test's command, typed into a shell on apps/welcome over
  # /ws/term: the title it sets must come back (node's WebSocket).
  what="a terminal on apps/welcome runs the UI test's command (its OSC title comes back)"
  if command -v node >/dev/null 2>&1; then
    check node --input-type=module -e "$term_check" "$url" "$(token)"
  else
    echo "skip $what — no node"
  fi
  exit "$fail"
  ;;
*)
  sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
  exit 2
  ;;
esac
