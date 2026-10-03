#!/usr/bin/env bash
# hack/demo/seed.sh — dress a fresh workspace as Larkspan, the demo film
# set's fictional company (hack/demo/README.md): its people and teams, the
# workspace's branding, network sets, and the apps a company like it runs,
# filled with the frozen content under hack/demo/data/. Everything goes
# through xbind's API and the tiles' own APIs, like hack/ui-harness/seed.sh;
# the only direct writes are tile sources laid over a fresh tile's scaffold
# (and those tiles' git history), as a person editing in its terminal would.
#
# env: URL, WS, TOKEN (the owner token), REPO — set by hack/demo/up.sh and by
#      hack/ui-harness/run.sh (HARNESS_SEED=demo); PORT (derived from URL)
#      DEMO_ISOLATE=1 (HARNESS_ISOLATE alike)  xbind runs --isolate: tile
#                      sandboxes, people's partitions (apps/expenses, the
#                      agent), the coding sandboxes on xbind's own runtime
#      FAKEOPENAI_ADDR  the scripted model the seed's conversations are played
#                      by (hack/fakeopenai -script hack/demo/data/model-script.json)
#      DEMO_LLM=real    after seeding, point the gateway at real providers: an
#                      ANTHROPIC_API_KEY and/or OPENAI_API_KEY from the
#                      environment (DEMO_MODEL_LARGE / DEMO_MODEL_SMALL pick
#                      the models; README.md). Seeding itself never calls one.
#      DEMO_KEEP_ADMIN=1  keep xbind --dev's own admin/admin account
#      DEMO_PASSWORD    the people's password (up.sh and the harness make a
#                      random one per set; lib.sh demo_password)
#      DEMO_TZ, NOW_MS, DEMO_DAY  the set's clock (clock.py; up.sh and the
#                      harness compute it once and export TZ with it): the
#                      time zone the set's times of day are in (default the
#                      company's), its "now" and its day
set -uo pipefail
D="$(cd "$(dirname "$0")" && pwd)"
REPO=${REPO:-$(cd "$D/../.." && pwd)}
. "$D/lib.sh"
: "${URL:?URL (the workspace) is required}" "${WS:?WS (the workspace directory) is required}" "${TOKEN:?TOKEN (the owner token) is required}"
: "${DEMO_PASSWORD:?DEMO_PASSWORD (the password of the set: hack/demo/up.sh makes one) is required}"
DATA="$D/data" TILES="$D/tiles" COMPANY="$D/company.json"
DEMO_ISOLATE=${DEMO_ISOLATE:-${HARNESS_ISOLATE:-}}
[[ "$DEMO_ISOLATE" == 0 ]] && DEMO_ISOLATE=""
FAKE=${FAKEOPENAI_ADDR:-127.0.0.1:18977}
PORT=${PORT:-${URL##*:}}
TMP=$(mktemp -d "${TMPDIR:-/tmp}/demo-seed.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
started=$(date +%s)

py() { python3 - "$@"; }
# cfield EXPR: a value of company.json (a python expression on c)
cfield() { python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$COMPANY" "$1"; }
PASS=$DEMO_PASSWORD
DOMAIN=$(cfield 'c["company"]["domain"]')
GIT_DOMAIN=$(cfield 'c["company"]["gitDomain"]')
ADMIN=$(cfield 'c["personas"]["admin"]')
AGENT_TILE=$(cfield 'c["agent"]["tile"]') AGENT_NAME=$(cfield 'c["agent"]["name"]') AGENT_ID=$(cfield 'c["agent"]["id"]')
# The demo's clock (clock.py): one time zone — the company's; the tiles'
# backends read the fixtures' times of day in it ("15:10", the 05:30 run; a
# tile sandbox under --isolate runs on UTC), git and this script too (TZ) —
# one "now" that relative times in the fixtures ("18 minutes ago") hang
# off, anchored to working hours (now on a weekday between 08:00 and 19:00,
# else the last working day's late afternoon, so a set seeded at night
# still reads like a working day, never one in the future), and one day:
# NOW_MS's date.
demo_clock || exit 1
export NOW_MS DEMO_TZ DEMO_DAY
# Fixture text names days relative to the demo's day, counted in working
# days: {{weekday:+1}} "Monday", {{date:+4}} "Oct 9", {{longdate:+4}}
# "October 9", {{nth:+4}} "9th", {{iso:-2}} "2026-10-01" (the calendar's
# dates). hack/fakeopenai fills the same placeholders in the model's script
# (up.sh starts it with -day DEMO_DAY), so the agent's answers agree with
# the calendar and the CRM. The fixtures are read from a filled copy.
mkdir "$TMP/data"
python3 - "$D/data" "$TMP/data" <<'PY'
import datetime as dt, os, re, shutil, sys
src, dst = sys.argv[1], sys.argv[2]
day = dt.date.fromisoformat(os.environ["DEMO_DAY"])
def shift(n):
    d, step = day, (1 if n >= 0 else -1)
    for _ in range(abs(n)):
        d += dt.timedelta(days=step)
        while d.weekday() >= 5: d += dt.timedelta(days=step)
    return d
def nth(k): return f"{k}{'th' if 11 <= k <= 13 else {1: 'st', 2: 'nd', 3: 'rd'}.get(k % 10, 'th')}"
MONTHS = "January February March April May June July August September October November December".split()
DAYS = "Monday Tuesday Wednesday Thursday Friday Saturday Sunday".split()
fmt = {"weekday": lambda d: DAYS[d.weekday()], "date": lambda d: f"{MONTHS[d.month - 1][:3]} {d.day}",
       "longdate": lambda d: f"{MONTHS[d.month - 1]} {d.day}", "nth": lambda d: nth(d.day), "iso": lambda d: d.isoformat()}
rx = re.compile(r"\{\{\s*(weekday|date|longdate|nth|iso):([+-]?\d+)\s*\}\}")
for f in os.listdir(src):
    if f.endswith(".json"):
        text = open(os.path.join(src, f)).read()
        open(os.path.join(dst, f), "w").write(rx.sub(lambda m: fmt[m[1]](shift(int(m[2]))), text))
    else:
        shutil.copy(os.path.join(src, f), dst)
PY
DATA="$TMP/data"

fails=0
say() { printf '\n== %s\n' "$*"; }

# call [-u user [-f tile]] [-s] METHOD PATH [BODY] — one API call; the body
# lands in $R and the status in $C (never a subshell, so failures count). As
# the owner token, or with -u as that person (their own session), and -f
# with their frame token for that tile — how the tile's own page calls it,
# so the tile sees the person in X-XBin-User (and, on a partitioned tile,
# the call reaches that person's partition). BODY is JSON text or @file. A
# status ≥ 400 is printed and counted unless -s (soft: the caller checks $C).
R="" C=0
call() {
  local user="" tile="" soft=""
  while [[ "${1:-}" == -* ]]; do
    case "$1" in -u) user=$2; shift 2 ;; -f) tile=$2; shift 2 ;; -s) soft=1; shift ;; *) break ;; esac
  done
  local m=$1 p=$2 body=${3:-}
  local args=(-sS -X "$m" -w '\n%{http_code}' --max-time 180)
  if [[ -n "$user" ]]; then
    login "$user" || { R="" C=0; return 1; }
    args+=(-b "$TMP/jar.$user")
    if [[ -n "$tile" ]]; then frame_token "$user" "$tile" || { R="" C=0; return 1; }; args+=(-H "X-XBin-Frame-Token: $FT"); fi
  else
    args+=(-H "Authorization: Bearer $TOKEN")
  fi
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data-binary "$body")
  local out
  out=$(curl "${args[@]}" "$URL$p" 2>&1) || true
  C=${out##*$'\n'} R=${out%$'\n'*}
  [[ "$C" =~ ^[0-9]+$ ]] || C=0
  if (( C >= 200 && C < 300 )); then printf '  %s %s%s → %s\n' "$m" "$p" "${user:+ (as $user)}" "$C"; return 0; fi
  if [[ -z "$soft" ]]; then
    printf '!! %s %s%s → %s: %s\n' "$m" "$p" "${user:+ (as $user${tile:+ in $tile})}" "$C" "${R:0:400}"
    fails=$((fails + 1))
  fi
  return 1
}
api() { call "$1" "/api/xbin$2" "${3:-}"; }
# jget EXPR: a value out of the last answer ($R), by a python expression on d
jget() { printf '%s' "$R" | python3 -c 'import json,sys
try: d=json.load(sys.stdin)
except Exception: d=None
try: v=eval(sys.argv[1])
except Exception: v=""
print("" if v is None else v)' "$1"; }

# login user: a session cookie for a demo person (once).
login() {
  [[ -s "$TMP/jar.$1" ]] && return 0
  local code
  # (the password on curl's stdin, never in its argv)
  code=$(printf '%s' "$PASS" | curl -sS -o /dev/null -w '%{http_code}' -c "$TMP/jar.$1" --data-urlencode "username=$1" --data-urlencode "password@-" "$URL/login")
  if ! grep -q xbin "$TMP/jar.$1" 2>/dev/null; then echo "!! login $1 → $code"; fails=$((fails + 1)); rm -f "$TMP/jar.$1"; return 1; fi
}
# frame_token user tile: $FT, that person's frame token for the tile (once).
declare -A FTOK=()
frame_token() {
  local k="$1|$2"
  if [[ -z "${FTOK[$k]:-}" ]]; then
    local out
    out=$(curl -sS -b "$TMP/jar.$1" "$URL/api/xbin/frame-token?component=$2")
    FTOK[$k]=$(printf '%s' "$out" | python3 -c 'import json,sys
try: print(json.load(sys.stdin).get("token",""))
except Exception: print("")')
    [[ -z "${FTOK[$k]}" ]] && { echo "!! frame token for $1 on $2: ${out:0:200}"; fails=$((fails + 1)); return 1; }
  fi
  FT=${FTOK[$k]}
}

# until_ok [call options…] METHOD PATH [BODY] — retry a call until it answers
# 2xx (a backend building, a binding restarting it), up to $TRIES seconds.
TRIES=150
until_ok() {
  local i
  for ((i = 0; i < TRIES; i++)); do
    call -s "$@" >/dev/null && { printf '  %s → %s (after %ss)\n' "${*: -1}" "$C" "$i"; return 0; }
    sleep 1
  done
  echo "!! never answered: $* (last $C: ${R:0:300})"; fails=$((fails + 1)); return 1
}

# wait_manifest PATH [TARGET]: until xbind lists the tile — using TARGET when
# given, i.e. with the manifest just laid down rather than the scaffold's (a
# first request before that would start the scaffold's backend)
wait_manifest() {
  local i
  for ((i = 0; i < 60; i++)); do
    call -s GET /api/xbin/components >/dev/null
    [[ "$(jget "next((1 for c in d if c['path'] == '$1' and ('$2' == '' or any((u.get('target') if isinstance(u, dict) else u) == '$2' for u in (c.get('uses') or [])))), 0)")" == 1 ]] && return 0
    sleep 0.5
  done
  echo "!! xbind never listed $1${2:+ using $2}"; fails=$((fails + 1))
}

# new_tile PATH OWNER RUNTIME SRC — create a tile (OWNER "org:<id>", or ""
# for the workspace), lay the demo source over its scaffold with the shared
# page/backend helpers it imports, and wait until xbind has read its manifest.
new_tile() {
  local path=$1 owner=$2 runtime=$3 src=$4
  api POST /create "{\"path\":\"$path\",\"runtime\":\"$runtime\"${owner:+,\"owner\":\"$owner\"}}"
  cp -r "$src/." "$WS/$path/"
  [[ -f "$src/index.html" && ! -f "$src/ui.js" ]] && cp "$TILES/_lib/ui.js" "$WS/$path/ui.js"
  if [[ "$runtime" == node ]]; then cp "$TILES/_lib/tile.js" "$WS/$path/backend/tile.js"; fi
  wait_manifest "$path" "$(python3 -c 'import json,re,sys
m = json.loads(re.sub(r"^\s*//.*$", "", open(sys.argv[1]).read(), flags=re.M))
print((m.get("uses") or [{}])[0].get("target", ""))' "$src/xbin.json")"
}

# commit_as PATH "Name <email>" WHEN MESSAGE — the tile's own repo records
# its current content as that person's work, at WHEN: "<±N> days HH:MM",
# N days from the set's day (one on a weekend: the Friday before) at that
# time of day in the set's zone (TZ) — so git's dates agree with the
# fixtures' and carry the company's UTC offset.
commit_as() {
  local dir="$WS/$1" who=$2 when msg=$4
  when=$(python3 -c 'import datetime as dt, os, re, sys
m = re.fullmatch(r"\s*([+-]?\d+)\s+days?\s+(\d{1,2}):(\d{2})\s*", sys.argv[1])
if not m: sys.exit(f"commit_as: WHEN is \"<+-N> days HH:MM\", got {sys.argv[1]!r}")
d = dt.date.fromisoformat(os.environ["DEMO_DAY"]) + dt.timedelta(days=int(m[1]))
while d.weekday() >= 5: d -= dt.timedelta(days=1)
print(f"{d.isoformat()}T{int(m[2]):02d}:{m[3]}:00")' "$3") || { echo "!! $1: $when"; fails=$((fails + 1)); return 1; }
  local name=${who% <*} email=${who#*<}
  email=${email%>}
  git -C "$dir" add -A >/dev/null 2>&1
  GIT_AUTHOR_NAME=$name GIT_AUTHOR_EMAIL=$email GIT_COMMITTER_NAME=$name GIT_COMMITTER_EMAIL=$email \
    GIT_AUTHOR_DATE=$when GIT_COMMITTER_DATE=$when git -C "$dir" commit -q --allow-empty -m "$msg" >/dev/null 2>&1 || true
}
# person ID: their git identity, "Name <id@gitDomain>" (company.json)
person() { cfield "next(f'{p[\"name\"]} <{p[\"id\"]}@{c[\"company\"][\"gitDomain\"]}>' for p in c['people'] if p['id']=='$1')"; }

# =========================================================================
say "people and teams ($(cfield 'c["company"]["name"]'))"
py "$COMPANY" > "$TMP/orgs" <<'PY'
import json, sys
for o in json.load(open(sys.argv[1]))["orgs"]: print(json.dumps({"id": o["id"], "name": o["name"]}))
PY
while read -r o; do api POST /orgs "$o"; done < "$TMP/orgs"
# Every member works at terminal level on their org's tiles; each org's
# admins manage it (Leadership reads the other teams' tiles: below). They
# sign in with a password, so their accounts carry no email (an account's
# email is its SSO binding, docs/auth.md): none shows in the admin console.
# (The password never rides an argv: python reads it from its environment
# and each account goes to curl as a file.)
mkdir -m 700 "$TMP/users"
py "$COMPANY" "$TMP/users" <<'PY'
import json, os, sys
c = json.load(open(sys.argv[1])); pw = os.environ["DEMO_PASSWORD"]
for i, p in enumerate(c["people"]):
    orgs = [{"org": o, "level": "terminal", "create": True, "admin": lvl == "admin"} for o, lvl in p["orgs"].items()]
    json.dump({"id": p["id"], "name": p["name"], "role": p["role"], "password": pw, "termNet": True, "orgs": orgs},
              open(os.path.join(sys.argv[2], f"{i:02d}-{p['id']}.json"), "w"))
PY
for u in "$TMP"/users/*.json; do api POST /users "@$u"; done

say "branding: the company's name and mark in place of xbin's"
ICON=$(base64 -w0 < "$DATA/brand-icon.svg")
api PUT /branding "{\"title\":\"$(cfield 'c["company"]["name"]')\",\"icon\":\"data:image/svg+xml;base64,$ICON\"}"

say "network sets, attached to the teams"
api PUT /net-sets/internet     '{"rules":["internet"]}'
api PUT /net-sets/office-lan   '{"rules":["lan:10.20.0.0/16"]}'
# (no hostnames: the map and telematics vendors' would be real companies' —
# the routing APIs are HTTPS anywhere, the depots' gateways are on the LAN
# and their broker speaks MQTT over TLS)
api PUT /net-sets/routing-apis '{"rules":["internet:443"]}'
api PUT /net-sets/depot-iot    '{"rules":["lan:10.20.64.0/20","internet:8883"]}'
# Operations' tiles may also go out through the egress approver (below),
# where each new destination waits for a person
api PUT /net-sets/approved-egress '{"rules":["provider:apps/egress-approver"]}'
api PATCH /orgs/engineering '{"netSets":["internet","office-lan","routing-apis"]}'
api PATCH /orgs/operations  '{"netSets":["office-lan","depot-iot","approved-egress"]}'
api PATCH /orgs/sales       '{"netSets":["internet"]}'
api PATCH /orgs/leadership  '{"netSets":["internet"]}'

# =========================================================================
say "CRM (apps/crm): accounts, deals, contacts, activity, and MCP tools for the agent"
new_tile apps/crm org:sales node "$TILES/crm"
commit_as apps/crm "$(person hana)" "-41 days 15:20" "CRM: pipeline board, accounts and activity feed"
py "$DATA/crm.json" "$COMPANY" > "$TMP/crm.json" <<'PY'
import json, os, sys
fx = json.load(open(sys.argv[1])); c = json.load(open(sys.argv[2]))
fx["team"] = [{"id": p["id"], "name": p["name"], "title": p["title"]} for p in c["people"]]
fx["now"], fx["tz"] = int(os.environ["NOW_MS"]), os.environ["DEMO_TZ"]
fx.pop("_comment", None)
print(json.dumps(fx))
PY
until_ok GET /api/apps/crm/summary
call POST /api/apps/crm/import "@$TMP/crm.json"

say "the nightly ops report (apps/ops-report): 14 nights of history, the 05:30 cron job"
new_tile apps/ops-report org:operations node "$TILES/ops-report"
commit_as apps/ops-report "$(person owen)" "-33 days 11:05" "Nightly ops report: per-customer routes, platform health, incidents"
until_ok GET /api/apps/ops-report/reports
py "$DATA/ops-report.json" > "$TMP/ops-report.json" <<'PY'
import json, os, sys
fx = json.load(open(sys.argv[1])); fx.pop("_comment", None)
fx["tz"] = os.environ["DEMO_TZ"]
print(json.dumps(fx))
PY
call POST /api/apps/ops-report/import "@$TMP/ops-report.json"

say "calendar and email (examples/): the team calendar and the team inbox"
api POST /create '{"path":"apps/calendar","runtime":"go"}'
cp -r "$REPO/examples/calendar/." "$WS/apps/calendar/"
cp "$TILES/calendar/index.html" "$WS/apps/calendar/index.html"
api POST /create '{"path":"apps/email","runtime":"go"}'
cp -r "$REPO/examples/email/." "$WS/apps/email/"
cp -r "$TILES/email/." "$WS/apps/email/"
commit_as apps/calendar "$(person sofia)" "-60 days 10:40" "Calendar: a week view for the team"
commit_as apps/email "$(person sofia)" "-58 days 16:10" "Team inbox: threads in kv, today's meetings from the calendar"
wait_manifest apps/calendar res:apps/calendar/events
wait_manifest apps/email res:apps/email/mail
api POST /grants '{"from":"apps/email","target":"apps/calendar","role":"reader"}'
api POST /grants '{"from":"apps/email","target":"res:apps/calendar/bus","role":"reader"}'
TRIES=240 until_ok GET "/api/apps/calendar/events?day=$(date +%F)"
# the one-offs on their (filled) dates, the weekly ones from last week's
# Monday through the next two weeks, in time order within a day
py "$DATA/calendar.json" > "$TMP/events" <<'PY'
import json, os, sys, datetime as dt
fx = json.load(open(sys.argv[1]))
day = dt.date.fromisoformat(os.environ["DEMO_DAY"])
monday = day - dt.timedelta(days=day.weekday() + 7)
evs = [{"day": e["date"], "time": e["time"], "title": e["title"]} for e in fx["events"]]
for w in fx["weekly"]:
    for i in range(28):
        d = monday + dt.timedelta(days=i)
        if d.weekday() in w["weekdays"]:
            evs.append({"day": d.isoformat(), "time": w["time"], "title": w["title"]})
for e in sorted(evs, key=lambda e: (e["day"], e["time"])):
    print(json.dumps(e))
PY
while read -r ev; do call POST /api/apps/calendar/events "$ev"; done < "$TMP/events"
py "$DATA/email.json" > "$TMP/threads" <<'PY'
import json, os, sys
fx = json.load(open(sys.argv[1]))
now = int(os.environ["NOW_MS"])
for t in fx["threads"]:
    for m in t["messages"]:
        m["at"] = now - m["at"] * 60_000
    print(t["id"] + "\t" + json.dumps(t))
PY
while IFS=$'\t' read -r id body; do api PUT "/kv/res:apps/email/mail/thread/$id" "$body"; done < "$TMP/threads"

# =========================================================================
say "LLM gateway (apps/llm-gw), playing the scripted model while seeding"
# hack/fakeopenai on the host's loopback (host networking), with the demo
# script: a local inference endpoint as far as the gateway can tell. With
# DEMO_LLM=real the real providers replace it at the end (below).
api POST /builtins/import '{"name":"llm-gw"}'
api POST /bindings '{"component":"apps/llm-gw","slot":"net","provider":"host"}'
until_ok GET /api/apps/llm-gw/config
call PUT /api/apps/llm-gw/config/backend "{\"name\":\"inference\",\"baseURL\":\"http://$FAKE/v1\",\"token\":\"local-inference\"}"
call -s DELETE /api/apps/llm-gw/config/backend/default
for use in agent chat coding pipeline; do call PUT /api/apps/llm-gw/config/preferred "{\"use\":\"$use\",\"model\":\"assistant-large\"}"; done
call PUT /api/apps/llm-gw/config/preferred '{"use":"summarizing","model":"assistant-small"}'

say "chat (builtin) on the gateway"
api POST /builtins/import '{"name":"chat"}'
api POST /bindings '{"component":"apps/chat","slot":"llm","providers":["apps/llm-gw"]}'
api POST /bindings '{"component":"apps/chat","slot":"mcp","providers":["apps/crm","apps/ops-report"]}'

say "metrics (builtin prometheus-viewer) on two real sources: the host and the gateway"
new_tile apps/host-exporter org:engineering node "$TILES/host-exporter"
commit_as apps/host-exporter "$(person owen)" "-21 days 09:30" "Host exporter: load, memory, CPU, network and disk for the dashboard"
api POST /builtins/import '{"name":"prometheus-viewer","path":"apps/metrics","owner":"org:engineering"}'
api POST /bindings '{"component":"apps/metrics","slot":"sources","providers":["apps/host-exporter","apps/llm-gw"]}'

# =========================================================================
say "coding sandboxes (builtin template): $([[ -n "$DEMO_ISOLATE" ]] && echo "xbind's own runtime" || echo "host directories (no --isolate)")"
api POST /templates/new '{"source":"coding-sandbox","path":"apps/coding-sandbox","owner":"org:engineering"}'
CS="$WS/apps/coding-sandbox"
api POST /grants '{"from":"apps/coding-sandbox","target":"cap:sandboxes","role":"writer"}'
api POST /bindings '{"component":"apps/coding-sandbox","slot":"internet","provider":"internet"}'
api POST /bindings '{"component":"apps/coding-sandbox","slot":"open","provider":"none"}'
if [[ -z "$DEMO_ISOLATE" ]]; then
  # Without --isolate there is no sandbox runtime: the template's own test
  # backend stands in (every sandbox a host directory under a `boxes`
  # resource, as the UI harness runs it), named for what it is here: local.
  for f in "$REPO"/builtin-templates/coding-sandbox/_backend/fake_*_test.go; do
    b=${f##*/}
    sed 's/registerBackend("fake"/registerBackend("local"/; s/Mode: "fake"/Mode: "local"/; s/Accel: "fake"/Accel: "none"/' "$f" > "$CS/_backend/${b%_test.go}.go"
  done
  sed -i 's|"db": { "type": "sqlite" }|"db": { "type": "sqlite" }, "boxes": { "type": "filesystem" }|' "$CS/scope.json"
  # (the manifest is JSON with comments: one more uses line after the db's)
  sed -i '/"target": "res:apps\/coding-sandbox\/db"/a\    { "target": "res:apps/coding-sandbox/boxes", "role": "writer" },' "$CS/xbin.json"
  wait_manifest apps/coding-sandbox res:apps/coding-sandbox/boxes
  TRIES=300 until_ok GET /api/apps/coding-sandbox/ops/state
  call PUT /api/apps/coding-sandbox/ops/config '{"backend":"local","backendConfig":{"root":"res:boxes"}}'
  sandboxes=1
else
  # The manager makes VM sandboxes (its operators' mode): VM tile sandboxes
  # are an admin's switch, on where xbind can run VMs (the installer turns
  # it on with KVM; a fresh `xbind init` leaves it off)
  sandboxes=""
  call -s GET /api/xbin/vm >/dev/null
  if [[ "$(jget 'd["status"]["available"]')" == True ]]; then api PUT /vm/policy '{"tiles":true}' && sandboxes=1
  else echo "  (no VMs here: $(jget 'd["status"].get("reason", "")') — the coding sandboxes stay empty)"; fi
fi
# The engineers' sandboxes, made from the tile's own page (their own, each
# private to them; operators see them all): two running, one stopped.
# sbx USER NAME SIZE START
sbx() {
  call -u "$1" -f apps/coding-sandbox POST "/api/apps/coding-sandbox/sbx/sandboxes?wait=110" \
    "$(python3 -c 'import json,sys; print(json.dumps({"name": sys.argv[1], "size": sys.argv[2], "egress": "internet", "start": sys.argv[3] == "1"}))' "$2" "$3" "$4")"
}
if [[ -n "$sandboxes" ]] && TRIES=300 until_ok -u tomas -f apps/coding-sandbox GET /api/apps/coding-sandbox/sbx/hello; then
  # the team's quotas, so the operators' page shows usage against them
  call PUT /api/apps/coding-sandbox/ops/config '{"quotas":{"consumer":{"sandboxes":24,"running":10,"memMiB":65536,"vcpus":32,"diskGiB":600},"person":{"sandboxes":4,"running":2,"memMiB":16384,"vcpus":8,"diskGiB":120}}}'
  sbx lukas planner-oom-repro medium 1
  sbx tomas routing-engine small 1
  sbx hana driver-app-4-12 small 0
fi

say "$AGENT_NAME, the team's agent (builtin agent template, $AGENT_TILE)"
if [[ -n "$DEMO_ISOLATE" ]]; then api POST /templates/new "{\"source\":\"agent\",\"path\":\"$AGENT_TILE\"}"   # partitioned: each person's own
else api POST /templates/new "{\"source\":\"agent\",\"path\":\"$AGENT_TILE\",\"partition\":false}"; fi
api POST /grants "{\"from\":\"$AGENT_TILE\",\"target\":\"cap:open-links\",\"role\":\"writer\"}"
api POST /bindings "{\"component\":\"$AGENT_TILE\",\"slot\":\"llm\",\"providers\":[\"apps/llm-gw\"]}"
api POST /bindings "{\"component\":\"$AGENT_TILE\",\"slot\":\"mcp\",\"providers\":[\"apps/crm\",\"apps/ops-report\"]}"
api POST /bindings "{\"component\":\"$AGENT_TILE\",\"slot\":\"sandboxes\",\"providers\":[\"apps/coding-sandbox\"]}"
api POST /bindings "{\"component\":\"$AGENT_TILE\",\"slot\":\"net\",\"provider\":\"none\"}"
TRIES=300 until_ok GET "/api/$AGENT_TILE/config"
# its models under the gateway's aliases, which the composer's picker shows
# by name: "assistant-large" reads as what it is, the team's assistant
py "$COMPANY" > "$TMP/agent-config.json" 3<<<"$R" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
cfg = json.load(open("/dev/fd/3"))
co, ag = c["company"], c["agent"]
cfg["model"] = "assistant-large"
cfg.setdefault("models", {}).update({"general": "assistant-large", "code": "assistant-large", "memory": "assistant-small", "vlm": "assistant-large"})
cfg["system"] = (f"You are {ag['name']}, the assistant of {co['name']} ({co['tagline'].lower()}). {co['description']}\n\n"
    "The team's CRM and the nightly ops report are bound to you as MCP tools: look things up there before answering "
    "questions about customers, deals, renewals or how the platform ran, and say where a number came from. "
    "Be brief and concrete; people read you between meetings.\n\n" + (cfg.get("system") or ""))
print(json.dumps(cfg))
PY
call PUT "/api/$AGENT_TILE/config" "@$TMP/agent-config.json"

say "the team chat (agent-messaging-bridge template; its console stands in for the chat platform)"
api POST /templates/new '{"source":"agent-messaging-bridge","path":"apps/team-chat"}'
api POST /bindings "{\"component\":\"apps/team-chat\",\"slot\":\"agent\",\"provider\":\"$AGENT_TILE\"}"

# =========================================================================
say "expenses (apps/expenses): one book per person$([[ -n "$DEMO_ISOLATE" ]] && echo ', partitioned' || echo ' (one instance without --isolate)')"
new_tile apps/expenses "" node "$TILES/expenses"
# people's partitions need --isolate: without it the tile runs as one
# instance (its backend keys books by person either way)
[[ -z "$DEMO_ISOLATE" ]] && sed -i '/^  "partition": \["user"\],$/d' "$WS/apps/expenses/xbin.json"
commit_as apps/expenses "$(person elena)" "-75 days 14:45" "Expenses: a book per person, submit for approval"

say "the onboarding tracker (apps/onboarding), built by $AGENT_NAME from Priya's brief"
api POST /create '{"path":"apps/onboarding","owner":"org:sales"}'
# Its history, as it happened: Priya's brief, then Merrow's commits building
# the tile up file by file (and one of Priya's). Built beside the workspace
# and swapped in, so the tile is never without its manifest.
OB="$TMP/onboarding-repo" SRC="$TILES/onboarding" AGENT_GIT="$AGENT_NAME <$AGENT_ID@$GIT_DOMAIN>"
mkdir -p "$OB" && git -C "$OB" init -q -b main
step() { WS=$TMP commit_as onboarding-repo "$1" "$2" "$3"; }
cp "$SRC/BRIEF.md" "$OB/"
step "$(person priya)" "-9 days 15:48" "Brief for $AGENT_NAME: an onboarding tracker"
cp "$SRC/"{index.html,styles.js,scope.json} "$OB/"
sed 's/go-live/launch/g' "$SRC/board.js" > "$OB/board.js"
grep -v '"apps/crm"' "$SRC/xbin.json" | sed 's|"role": "writer" },|"role": "writer" }|' > "$OB/xbin.json"
step "$AGENT_GIT" "-9 days 16:05" "Scaffold the onboarding tracker: a card per customer, soonest launch first"
cp "$SRC/store.js" "$OB/"
step "$AGENT_GIT" "-9 days 16:12" "Keep each customer's checklist in the tile's kv resource

No backend: the page reads and writes customer/<id> keys in
res:apps/onboarding/state directly."
cp "$SRC/crm.js" "$SRC/xbin.json" "$OB/"
step "$AGENT_GIT" "-9 days 16:31" "Read plan, fleet size and health from the CRM

Adds a reader grant on apps/crm; Tomás approved it."
cp "$SRC/checklist.js" "$OB/"
step "$AGENT_GIT" "-8 days 10:02" "Checklist per customer: late steps in red, finished ones folded"
cp "$SRC/board.js" "$OB/"
step "$(person priya)" "-7 days 09:41" "Say go-live, not launch"
cp "$SRC/templates.js" "$OB/"
step "$AGENT_GIT" "-7 days 09:55" "Checklist templates for new customers, new depots and driver-app rollouts"
cp "$SRC/README.md" "$OB/"
step "$AGENT_GIT" "-2 days 14:30" "Document where the tracker keeps its data"
cp "$OB/"* "$WS/apps/onboarding/"
rm -rf "$WS/apps/onboarding/.git" && cp -r "$OB/.git" "$WS/apps/onboarding/.git"
api POST /grants '{"from":"apps/onboarding","target":"apps/crm","role":"reader"}'
wait_manifest apps/onboarding res:apps/onboarding/state
py "$DATA/onboarding.json" "$COMPANY" > "$TMP/onboarding.kv" <<'PY'
import datetime as dt, json, os, sys
fx = json.load(open(sys.argv[1])); c = json.load(open(sys.argv[2]))
names = {p["id"]: p["name"] for p in c["people"]}
now = dt.datetime.fromtimestamp(int(os.environ["NOW_MS"]) / 1000)
def at(d):
    # a day from now (a weekend one moves to a weekday), or a date at noon
    if isinstance(d, str): t = dt.datetime.fromisoformat(d).replace(hour=12)
    else:
        t = now + dt.timedelta(days=d)
        while d and t.weekday() >= 5: t += dt.timedelta(days=1 if d > 0 else -1)
    return int(t.timestamp() * 1000)
for cu in fx["customers"]:
    out = {k: cu[k] for k in ("id", "name", "title", "kind", "csm")}
    out.update(csmName=names.get(cu["csm"], cu["csm"]), kickoff=at(cu["kickoff"]), goLive=at(cu["goLive"]),
               steps=[{"title": s["title"], "due": at(s["due"]), **({"done": True, "doneAt": at(s["done"])} if "done" in s else {})} for s in cu["steps"]])
    print(cu["id"] + "\t" + json.dumps(out))
PY
while IFS=$'\t' read -r id body; do api PUT "/kv/res:apps/onboarding/state/customer/$id" "$body"; done < "$TMP/onboarding.kv"

# =========================================================================
say "egress approver and public HTTPS (builtins)"
api POST /builtins/import '{"name":"egress-approver"}'
api POST /grants '{"from":"apps/egress-approver","target":"cap:net-admin","role":"writer"}'
api POST /bindings '{"component":"apps/egress-approver","slot":"net","provider":"internet"}'
# the team chat's egress (a chat platform's API, once it speaks one) goes
# through the approver: every new destination waits for a person
api POST /bindings '{"component":"apps/team-chat","slot":"net","provider":"apps/egress-approver"}'
api POST /builtins/import '{"name":"traefik"}'
api POST /bindings '{"component":"apps/traefik","slot":"net","provider":"internet"}'
api POST /bindings "{\"component\":\"apps/traefik\",\"slot\":\"web\",\"provider\":\"runtime\",\"listen\":\"${DEMO_HTTP_ADDR:-127.0.0.1:$((PORT + 3))}\"}"
api POST /bindings "{\"component\":\"apps/traefik\",\"slot\":\"websecure\",\"provider\":\"runtime\",\"listen\":\"${DEMO_HTTPS_ADDR:-127.0.0.1:$((PORT + 4))}\"}"
TRIES=60 until_ok GET /api/apps/traefik/state
call -s POST /api/apps/traefik/settings "{\"email\":\"ops@$DOMAIN\",\"staging\":true}" || echo "  (traefik's settings: $C ${R:0:200})"

say "telematics feeds (apps/telematics): Operations' newest tile, its network through the egress approver"
# A new tile with a net interface (its `egress` slot): an admin decides
# where its egress goes (the shell's "interfaces to bind" — the network still
# unbinds it again to show that). Here: through the egress approver. It
# makes no network calls. (Its providers are the set's own fiction: no real
# vendor's name goes into a fixture or a commit — hack/demo-fiction.test.mjs.)
new_tile apps/telematics org:operations node "$TILES/telematics"
cp "$DATA/telematics.json" "$WS/apps/telematics/feeds.json"
commit_as apps/telematics "$(person owen)" "-1 days 17:05" "Telematics feeds: Fleetgrid and Routewise van positions, Galewatch weather alerts"
# (xbind takes the slot once it has read the manifest laid over the
# scaffold's: until then the bind is refused)
TRIES=60 until_ok POST /api/xbin/bindings '{"component":"apps/telematics","slot":"egress","provider":"apps/egress-approver"}'

# =========================================================================
say "who sees what: the shared apps for everyone, the teams' own for their members"
api PUT /defaults "{\"defaultTiles\":{\"apps/calendar\":\"read\",\"apps/email\":\"read\",\"apps/chat\":\"read\",\"$AGENT_TILE\":\"read\",\"apps/expenses\":\"read\"}}"
api POST /lifecycle '{"component":"apps/welcome","state":"hidden"}'
for t in apps/crm apps/onboarding apps/ops-report apps/metrics; do
  for u in maya ingrid ruth; do api PUT /access "{\"tile\":\"$t\",\"kind\":\"user\",\"id\":\"$u\",\"level\":\"read\"}"; done
done
api PUT /access '{"tile":"apps/crm","kind":"user","id":"maya","level":"write"}'
api PUT /access '{"tile":"apps/ops-report","kind":"user","id":"lukas","level":"read"}'
api PUT /access '{"tile":"apps/team-chat","kind":"user","id":"ingrid","level":"read"}'
if [[ -n "$DEMO_ISOLATE" ]]; then
  # Merrow keeps a partition per person, and the CRM, the ops report and the
  # coding sandboxes it calls are code their teams can change: they run a
  # checkpoint rather than their live work trees, so no one's saves reach
  # everyone's calls unreviewed (else xbind warns on every screen:
  # docs/partitions.md §Trust warnings)
  for t in apps/crm apps/ops-report apps/coding-sandbox; do api POST /deployments/live-reload/pause "{\"tile\":\"$t\"}"; done
fi

say "expense books, each filled by its owner (into their own partition, when partitioned)"
for u in $(cfield '" ".join(c["personas"]["expenses"])'); do
  py "$DATA/expenses.json" "$COMPANY" "$u" > "$TMP/expenses.$u" <<'PY'
import json, os, sys
fx = json.load(open(sys.argv[1])); c = json.load(open(sys.argv[2])); u = sys.argv[3]
name = next(p["name"] for p in c["people"] if p["id"] == u)
print(json.dumps({"name": name, "items": fx["people"][u]["items"], "now": int(os.environ["NOW_MS"]), "tz": os.environ["DEMO_TZ"]}))
PY
  until_ok -u "$u" -f apps/expenses GET /api/apps/expenses/expenses
  call -u "$u" -f apps/expenses POST /api/apps/expenses/import "@$TMP/expenses.$u"
done

# =========================================================================
say "a working week with $AGENT_NAME: conversations, automations, the team chat"
# agent USER METHOD PATH [BODY]: the agent's API as that person, the way its
# own page calls it (their frame token: their own partition, when partitioned)
agent() { local u=$1; shift; call -u "$u" -f "$AGENT_TILE" "$1" "/api/$AGENT_TILE$2" "${3:-}"; }
jtext() { python3 -c 'import json,sys; print(json.dumps({"text": sys.argv[1], "class": "internal"}))' "$1"; }
# agent_wait USER RUN: until the run is idle again (its turn answered)
agent_wait() {
  local u=$1 id=$2 st=""
  for _ in $(seq 1 120); do
    call -s -u "$u" -f "$AGENT_TILE" GET "/api/$AGENT_TILE/runs/$id" >/dev/null
    st=$(jget 'd["run"]["status"]')
    case "$st" in idle|done) echo "  run $id ($u): $st"; return 0 ;; error|canceled) break ;; esac
    sleep 1
  done
  echo "!! run $id ($u) ended '$st': ${R:0:300}"; fails=$((fails + 1)); return 1
}
agent_ask() {
  local u=$1 id
  agent "$u" POST /ask "$(jtext "$2")" || return 1
  id=$(jget 'd["id"]')
  agent_wait "$u" "$id" && agent "$u" POST "/runs/$id/read"
}
TRIES=240 until_ok -u "$ADMIN" -f "$AGENT_TILE" GET "/api/$AGENT_TILE/me"
agent_ask tomas "Anything in last night's ops report I should look at before the 9:30 standup?"
agent_ask priya "Which renewals are coming up in the next 90 days, and are any at risk?"
agent_ask priya "Prep me for the Brightwell renewal call at 10:00. Where do things stand, and what should I raise?"
agent_ask daniel "Draft a follow-up to Dana at Brightwell after today's call: we agreed on a three-year term covering all 202 vans and a two-week driver-app pilot in Dayton before the rollout."
agent_ask maya "How is our pipeline looking this quarter?"

vis=team; [[ -n "$DEMO_ISOLATE" ]] && vis=private   # a person's partition keeps its automations its own
schedule() { # user name cron goal [run]: "run" runs it once now, its first brief
  local body
  body=$(python3 -c 'import json,sys; print(json.dumps({"name": sys.argv[1], "cron": sys.argv[2], "goal": sys.argv[3], "class": "internal", "visibility": sys.argv[4]}))' "$2" "$3" "$4" "$vis")
  agent "$1" POST /schedules "$body" || return 1
  [[ "${5:-}" == run ]] || return 0
  local id; id=$(jget 'd["id"]')
  agent "$1" POST "/schedules/$id/trigger"
}
schedule ruth "Morning ops brief" "30 7 * * 1-5" \
  "Morning ops brief: read last night's ops report and write five lines for the ops team: volumes, on-time rate, platform health, and anything that needs a person today." run
schedule ingrid "Friday pipeline digest" "0 16 * * 5" \
  "Friday pipeline digest: open pipeline and forecast, the deals in negotiation with their next steps, what moved this week, and renewals at risk." run
# Tomás's own (the stills film his automations, and under --isolate his
# partition holds only his): set up this week, first run on Monday. A
# cadence the page puts in words ("every Monday at 9:00") and a goal its
# card shows whole (the card cuts at 140 characters).
schedule tomas "Monday platform review" "0 9 * * 1" \
  "Monday platform review: API p95, uptime and plan time from last night's ops report, the support queue, and what engineering should pick up."

# the team chat: the bridge says hello, Tomás claims the channel; #sales and
# #ops are trusted groups (their conversations may read the CRM and the ops
# report), DMs pair first
for _ in $(seq 1 120); do
  call -s -u "$ADMIN" -f "$AGENT_TILE" GET "/api/$AGENT_TILE/automations" >/dev/null
  CHAN=$(jget 'next((i["id"] for i in d["items"] if i["kind"] == "channel"), "")')
  [[ -n "$CHAN" ]] && break
  sleep 1
done
if [[ -z "$CHAN" ]]; then echo "!! the team chat never announced its channel"; fails=$((fails + 1))
else
  agent "$ADMIN" POST "/channels/$CHAN/claim" '{"name":"Team chat","visibility":"team","policy":{"dm":{"policy":"pairing"},"groups":{"policy":"allowlist","allow":["C-sales","C-ops"]},"privateLane":true,"trustedGroups":["C-sales","C-ops"]}}'
  for _ in $(seq 1 30); do
    call -s GET /api/apps/team-chat/status >/dev/null
    [[ "$(jget 'd["state"]["accounts"][0]["claimed"]')" == active ]] && break
    sleep 1
  done
  call POST /api/apps/team-chat/console/send "{\"as\":{\"id\":\"U-ingrid\",\"name\":\"Ingrid Halvorsen\"},\"conversation\":{\"id\":\"C-sales\",\"type\":\"channel\",\"name\":\"sales\"},\"mentioned\":true,\"text\":\"@$AGENT_NAME when is the Northgate security questionnaire due, and what is still open?\"}"
  for _ in $(seq 1 60); do
    call -s GET /api/apps/team-chat/console/transcript >/dev/null
    [[ "$(jget 'sum(1 for l in d["lines"] if l["dir"] == "out")')" -ge 1 ]] && { echo "  the team chat got its answer"; break; }
    sleep 1
  done
  # its conversation, named like the others (a partitioned agent keeps it at
  # its global instance, where a channel's conversations live)
  at=""; [[ -n "$DEMO_ISOLATE" ]] && at="xbin-partition=global"
  call -s -u "$ADMIN" -f "$AGENT_TILE" GET "/api/$AGENT_TILE/runs?roots=1${at:+&$at}" >/dev/null
  run=$(jget 'next((r["id"] for r in (d if isinstance(d, list) else d.get("runs", [])) if r.get("origin") == "channel"), "")')
  if [[ -n "$run" ]]; then agent "$ADMIN" PATCH "/runs/$run${at:+?$at}" '{"title":"#sales: Northgate security questionnaire"}'
  else echo "!! the team chat's conversation isn't listed"; fails=$((fails + 1)); fi
fi

# =========================================================================
say "what each person sees: screens, the sidebar, a larger font (17px)"
py "$DATA/layouts.json" > "$TMP/default-screen" <<'PY'
import json, sys
print(json.dumps({"tiles": json.load(open(sys.argv[1]))["default"]}))
PY
api PUT /screens/default "@$TMP/default-screen"
py "$DATA/layouts.json" > "$TMP/layouts" <<'PY'
import json, sys
L = json.load(open(sys.argv[1]))
for who, p in L["people"].items():
    screens = [{"id": "s-" + s["name"].lower(), "name": s["name"], "tiles": s["tiles"]} for s in p["screens"]]
    side = {"width": p.get("sideWidth", 216), "collapsed": bool(p.get("collapsed")), "folders": []}
    print(who + "\t" + json.dumps({"screens": screens, "active": screens[0]["id"], "side": side,
                                   "tabOrder": [s["id"] for s in screens], "hiddenOrg": {}, "recent": [], "drafts": {}}))
PY
while IFS=$'\t' read -r u body; do call -u "$u" PUT /api/xbin/prefs/layout "$body"; done < "$TMP/layouts"
# (17 px, or a person's own size in layouts.json: Maya's overview screen
# fits four apps at 15)
py "$COMPANY" "$DATA/layouts.json" > "$TMP/fonts" <<'PY'
import json, sys
c = json.load(open(sys.argv[1])); L = json.load(open(sys.argv[2]))["people"]
for p in c["people"]:
    print(p["id"] + "\t" + json.dumps({"fontSize": L.get(p["id"], {}).get("fontSize", 17)}))
PY
while IFS=$'\t' read -r u body; do call -u "$u" PUT /api/xbin/prefs/settings "$body"; done < "$TMP/fonts"

# =========================================================================
if [[ "${DEMO_LLM:-fake}" == real ]]; then
  say "real providers for the gateway (DEMO_LLM=real); the seeded conversations stay as they are"
  # Their OpenAI-compatible endpoints; the aliases keep the names the agent
  # and the chat already use (assistant-large / assistant-small).
  large="" small=""
  # backend NAME BASE-URL KEY-VAR: the gateway backend, its key read from the
  # environment and sent on curl's stdin (never in an argv or a file)
  backend() {
    call PUT /api/apps/llm-gw/config/backend @- <<<"$(python3 -c 'import json, os, sys
print(json.dumps({"name": sys.argv[1], "baseURL": sys.argv[2], "token": os.environ[sys.argv[3]]}))' "$@")"
  }
  if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
    backend anthropic https://api.anthropic.com/v1 ANTHROPIC_API_KEY
    large="anthropic/${DEMO_MODEL_LARGE:-claude-opus-5}" small="anthropic/${DEMO_MODEL_SMALL:-claude-haiku-4-5}"
  fi
  if [[ -n "${OPENAI_API_KEY:-}" ]]; then
    backend openai https://api.openai.com/v1 OPENAI_API_KEY
    [[ -z "$large" ]] && large="openai/${DEMO_MODEL_LARGE:-gpt-5}" small="openai/${DEMO_MODEL_SMALL:-gpt-5-mini}"
  fi
  if [[ -z "$large" ]]; then echo "!! DEMO_LLM=real needs ANTHROPIC_API_KEY or OPENAI_API_KEY"; fails=$((fails + 1))
  else
    call PUT /api/apps/llm-gw/config "{\"aliases\":{\"assistant-large\":\"$large\",\"assistant-small\":\"$small\"}}"
    call DELETE /api/apps/llm-gw/config/backend/inference
    api POST /bindings '{"component":"apps/llm-gw","slot":"net","provider":"internet"}'
  fi
fi

if [[ -z "${DEMO_KEEP_ADMIN:-}" ]]; then
  say "xbind --dev's own admin/admin account goes (the company's admins are $(cfield '", ".join(p["id"] for p in c["people"] if p["role"] == "admin")'))"
  api DELETE /users/admin
fi

say "done in $(( $(date +%s) - started )) s, $fails failure(s)"
(( fails == 0 ))
