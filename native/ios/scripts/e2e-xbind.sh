#!/usr/bin/env bash
# native/ios/scripts/e2e-xbind.sh — the xbind the UI tests (native/ios/
# UITests) run against, on this (Linux) box: a fresh workspace with the
# native counter (examples/counter-go, native.js included) and the scripted
# "fake" agent, started the way the UI harness starts it (hack/ui-harness/
# run.sh). mac-remote.sh e2e starts it, tunnels its port to the Mac and
# hands the URL and an account's name and password to the tests, which sign
# in through the app's Log in screens as a person does (the app has no
# token login).
#
#   e2e-xbind.sh start [--port P]   build bin/{xbind,bx,fakeacp}, init a fresh
#                                   workspace (+ testdata/e2e-tiles/* as
#                                   apps/*), start xbind on 127.0.0.1:P,
#                                   create the admin account e2e with a random
#                                   password, delete the login --dev seeds
#                                   (admin/admin) and wait until the counter's
#                                   backend answers
#   e2e-xbind.sh stop               stop it (and drop any mount it left)
#   e2e-xbind.sh env                XBIN_E2E_URL=…, XBIN_E2E_USER=… and
#                                   XBIN_E2E_PASSWORD=… lines
#   e2e-xbind.sh invite             create an account with an invite and print
#                                   its link (the tests make their own)
#   e2e-xbind.sh smoke              what the UI tests need of the server,
#                                   checked over HTTP: the account signs in,
#                                   the sign-in methods, the counter (read,
#                                   +1), the fake agent answering, an invite
#                                   checked — and that admin/admin is gone
#
#   XBIN_E2E_PORT       default 9871 (the tunnel keeps the same port on the
#                       Mac, so the workspace's origin is the same on both
#                       sides: http://127.0.0.1:P)
#   XBIN_E2E_DIR        default ${TMPDIR:-/tmp}/xbin-e2e: ws/ (the workspace),
#                       xbind.log, xbind.pid, env
#   XBIN_E2E_XBIND_ARGS extra xbind flags, e.g. "--isolate --rootfs …" —
#                       without --isolate its terminals are shells as you on
#                       this box, for whoever signs in (the e2e account's
#                       password; the tunnel makes the port reachable on
#                       the Mac: never tunnel it to the runner's user,
#                       native/AGENTS.md)
#
# The flags, for doing it by hand: `xbind init <ws>`; cp -r
# examples/counter-go <ws>/apps/counter; XBIN_AGENT_FAKE=bin/fakeacp
# XBIN_BIN=bin XBIN_SDK_PATH=sdk bin/xbind --dev --dev-overlay
# workspace-template --workspace <ws> --listen 127.0.0.1:P --external-url
# http://127.0.0.1:P; the owner token is <ws>/.xbin/token (it stays on this
# box: start uses it to create the e2e account, POST /api/xbin/users, and
# to delete the admin/admin --dev seeds, DELETE /api/xbin/users/admin).
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
  dir=$(cd "$dir" && pwd) # absolute: the start below runs from the repo
  ws=$dir/ws
  say "building bin/xbind, bin/bx, bin/fakeacp"
  (cd "$repo" && go build -o bin/xbind ./cmd/xbind && CGO_ENABLED=0 go build -o bin/bx ./cmd/bx &&
    go build -o bin/fakeacp ./hack/fakeacp)
  rm -rf "$ws"
  "$repo/bin/xbind" init "$ws" >/dev/null
  cp -r "$repo/examples/counter-go" "$ws/apps/counter"
  [ -f "$ws/apps/counter/native.js" ] || { say "examples/counter-go has no native.js"; exit 1; }
  # The UI tests' own pages (XbinE2ETests.test06): apps/wide, desktop-first
  # and wider than a phone, and apps/phone, with a mobile viewport.
  for t in "$repo"/native/ios/scripts/testdata/e2e-tiles/*/; do cp -r "$t" "$ws/apps/$(basename "$t")"; done
  # XBIN_AGENT_FAKE registers the scripted "fake" agent provider (D74),
  # XBIN_BIN is where the daemon finds the bx it binds in as the agent
  # host, XBIN_SDK_PATH builds the counter's Go backend against this sdk/.
  extra=()
  # shellcheck disable=SC2206 # extra flags, split on purpose
  [ -n "${XBIN_E2E_XBIND_ARGS:-}" ] && extra=(${XBIN_E2E_XBIND_ARGS})
  # A simple command in the background, so $! is xbind itself (a `(cd &&
  # … &)` list would leave a shell as its parent, holding our stdout, and
  # the pid file would name that shell).
  cd "$repo"
  XBIN_AGENT_FAKE="$repo/bin/fakeacp" XBIN_BIN="$repo/bin" XBIN_SDK_PATH="$repo/sdk" \
    nohup "$repo/bin/xbind" --dev --dev-overlay "$repo/workspace-template" --workspace "$ws" \
    --listen "127.0.0.1:$port" --external-url "$url" ${extra[@]+"${extra[@]}"} \
    >"$dir/xbind.log" 2>&1 </dev/null &
  echo $! >"$dir/xbind.pid"
  wait_for 30 "xbind" curl -fsS -o /dev/null "$url/healthz"
  # The tests' account: an admin (the tests read the counter and end their
  # sessions with it, and make invites), with a random password that
  # travels to the Mac on ssh's stdin as the owner token used to.
  password=$(head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)
  api POST /api/xbin/users "{\"id\":\"e2e\",\"name\":\"E2E Tester\",\"role\":\"admin\",\"password\":\"$password\"}" >/dev/null ||
    { say "could not create the e2e account — stopping"; stop; exit 1; }
  # --dev seeds admin/admin into a workspace with no users: anything that
  # reaches the port (the tunnel's end on the Mac is every local user's)
  # could sign in with it. Only the e2e account's random password opens
  # this one (and the owner token, which stays here).
  api DELETE /api/xbin/users/admin >/dev/null || { say "could not delete the dev login admin/admin — stopping"; stop; exit 1; }
  # The counter's Go backend builds on first use (a cold build can take a
  # minute or two); the UI tests should not wait for it.
  wait_for 240 "the counter's backend" api GET /api/apps/counter/count
  {
    echo "XBIN_E2E_URL=$url"
    echo "XBIN_E2E_USER=e2e"
    echo "XBIN_E2E_PASSWORD=$password"
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
invite)
  running || { say "not running ($dir)"; exit 1; }
  id="invitee-$(date +%s)"
  api POST /api/xbin/users "{\"id\":\"$id\",\"name\":\"Invited Tester\"}" |
    python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("inviteLink") or sys.argv[1] + d["inviteUrl"])' "$url"
  ;;
smoke)
  running || { say "not running ($dir)"; exit 1; }
  fail=0
  check() { if "$@" >/dev/null 2>&1; then echo "ok   $what"; else echo "FAIL $what"; fail=1; fi; }
  e2e_user=$(sed -n 's/^XBIN_E2E_USER=//p' "$dir/env")
  e2e_password=$(sed -n 's/^XBIN_E2E_PASSWORD=//p' "$dir/env")
  signs_in() { # the e2e account's password (as the app's Log in sends it)
    curl -fsS -X POST -H 'Content-Type: application/json' \
      -d "{\"username\":\"$e2e_user\",\"password\":\"$e2e_password\"}" "$url/api/xbin/login" | grep -q '"token"'
  }
  what="the e2e account signs in with its password (POST /api/xbin/login)"
  check signs_in
  refused() { # the dev login --dev seeds does not open it
    [ "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
      -d '{"username":"admin","password":"admin"}' "$url/api/xbin/login")" = 401 ]
  }
  what="admin/admin, --dev's seed, is gone"
  check refused
  methods() { curl -fsS "$url/api/xbin/login/methods" | grep -Eq '"enabled": ?true'; }
  what="the sign-in methods answer, password on (GET /api/xbin/login/methods)"
  check methods
  invite_checked() { # a fresh invite reads back, unspent (POST /api/xbin/invite/check)
    local link tok
    link=$("$0" invite --port "$port") && tok=${link##*invite=} &&
      curl -fsS -X POST -H 'Content-Type: application/json' -d "{\"invite\":\"$tok\"}" "$url/api/xbin/invite/check" |
      grep -q '"Invited Tester"'
  }
  what="an invite made here reads back (POST /api/xbin/invite/check)"
  check invite_checked
  what="apps/counter's runtime document (/c/apps/counter/?native=1)"
  check curl -fsS -o /dev/null -H "Authorization: Bearer $(token)" "$url/c/apps/counter/?native=1"
  what="apps/welcome, the web tile, is served"
  check curl -fsS -o /dev/null -H "Authorization: Bearer $(token)" "$url/c/apps/welcome/"
  what="apps/wide and apps/phone, the viewport pages, are served"
  check sh -c 'curl -fsS -o /dev/null -H "Authorization: Bearer $1" "$2/c/apps/wide/" && curl -fsS -o /dev/null -H "Authorization: Bearer $1" "$2/c/apps/phone/"' _ "$(token)" "$url"
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
