#!/usr/bin/env bash
# Seed the harness workspace (env: URL, WS, TOKEN). Idempotent enough to re-run.
set -uo pipefail
A="Authorization: Bearer $TOKEN"
J="Content-Type: application/json"
say() { printf '\n== %s\n' "$*"; }
api() { # method path [json]
  local m=$1 p=$2 d=${3:-}
  if [[ -n "$d" ]]; then curl -s -X "$m" -H "$A" -H "$J" -d "$d" "$URL/api/xbin$p"
  else curl -s -X "$m" -H "$A" "$URL/api/xbin$p"; fi
  echo
}

say "network sets"
api PUT /net-sets/devs-net  '{"rules":["internet","lan:10.42.0.0/16","internet:*.github.com:443"]}'
api PUT /net-sets/infra-net '{"rules":["lan:10.0.0.0/8","host"]}'
api PUT /net-sets/sales-net '{"rules":["internet"]}'
api PUT /net-sets/vpn-only  '{"rules":["provider:apps/*"]}'
say "grammar refusal (expected 400)"
api PUT /net-sets/bad '{"rules":["lan:db.internal"]}'

say "orgs"
for o in devs infra sales exec; do api POST /orgs "{\"id\":\"$o\",\"name\":\"${o^}\"}" >/dev/null; done
api PATCH /orgs/devs  '{"netSets":["devs-net"]}' >/dev/null
api PATCH /orgs/infra '{"netSets":["infra-net"]}' >/dev/null
api PATCH /orgs/sales '{"netSets":["devs-net","sales-net"]}' >/dev/null   # narrowed later → inert
api PATCH /orgs/sales '{"allow":["net:internet"]}' >/dev/null

say "users"
api POST /users '{"id":"dev1","name":"Dev One","role":"user","password":"devpass123","termNet":false,
  "tiles":{"tiles/organisations":"read","apps/leads":"terminal","apps/deployy":"terminal"},
  "orgs":[{"org":"devs","level":"terminal","create":true,"admin":true}]}' | head -c 300; echo
api POST /users '{"id":"sales1","name":"Sales One","role":"user","password":"salespass123","termNet":true,
  "tiles":{"tiles/organisations":"read"},
  "orgs":[{"org":"sales","level":"terminal","create":true,"admin":true}]}' | head -c 200; echo
api POST /users '{"id":"infra1","name":"Infra One","role":"user","password":"infrapass123",
  "tiles":{"tiles/organisations":"read","apps/deployy":"read","apps/reloady":"read"},
  "orgs":[{"org":"infra","level":"terminal","create":true,"admin":true}]}' | head -c 200; echo

say "tiles"
mk() { # path owner [runtime]
  api POST /create "{\"path\":\"$1\",\"owner\":\"$2\",\"runtime\":\"${3:-static}\",\"title\":\"$1\"}" | head -c 200; echo
  local m="$WS/$1/xbin.json"
  if [[ "${3:-static}" == static ]]; then
    printf '{\n  "interfaces": { "net": { "kind": "net" } }\n}\n' > "$m"
  else
    printf '{\n  "runtime": "%s",\n  "interfaces": { "net": { "kind": "net" } }\n}\n' "$3" > "$m"
  fi
}
mk apps/crawler org:devs node
mk apps/pinned  org:devs
mk apps/offline org:devs
mk apps/racks   org:infra node
# an http endpoint to publish — the ingressMulti pass: one endpoint, many hostnames (D79)
printf '{\n  "runtime": "node",\n  "interfaces": { "net": { "kind": "net" } },\n  "exposes": { "web": { "kind": "http", "paths": ["/"] } }\n}\n' > "$WS/apps/racks/xbin.json"
mk apps/leads   org:sales
mk apps/dev1-notes user:dev1
# a tile whose document grabs focus on every load — the "reload hoists the
# tile over my terminal" case (shots.js reloadFocus)
mk apps/focusy org:devs
cat > "$WS/apps/focusy/index.html" <<'EOF'
<!doctype html><meta charset="utf-8"><title>focusy</title>
<p>this tile focuses its input on every load</p>
<input id="i" placeholder="steals focus">
<script>
  const grab = () => document.getElementById('i').focus();
  grab(); addEventListener('load', grab);
</script>
EOF
# a tile whose link opens in a new tab — dead until cap:open-links is
# granted (shots.js openLinks)
mk apps/linky org:devs
printf '{\n  "uses": [{ "target": "cap:open-links", "role": "writer" }]\n}\n' > "$WS/apps/linky/xbin.json"
cat > "$WS/apps/linky/index.html" <<'EOF'
<!doctype html><meta charset="utf-8"><title>linky</title>
<p>a link that wants a new tab:</p>
<a id="ext" href="/docs/" target="_blank" rel="noopener">docs ↗</a>
EOF
# the tile dev lifecycle fixtures (passes/livereload.js, passes/deployments.js).
# Both static, created without mk's `net` slot so they add no row to the root
# page's pending-bindings panel. apps/reloady: org:devs, so dev1 (a devs admin)
# manages it and infra1 reads it. apps/deployy: org:sales (sales1 manages it;
# dev1 has terminal level without managing it, infra1 reads it), with a `uses`
# edge on apps/leads, approved here so it is a grant row, not a pending request.
api POST /create '{"path":"apps/reloady","owner":"org:devs","title":"apps/reloady"}' | head -c 200; echo
cat > "$WS/apps/reloady/index.html" <<'EOF'
<!doctype html><meta charset="utf-8"><title>reloady</title>
<p id="v">reloady: the seeded page</p>
EOF
api POST /create '{"path":"apps/deployy","owner":"org:sales","title":"apps/deployy"}' | head -c 200; echo
printf '{\n  "uses": [{ "target": "apps/leads", "role": "reader" }]\n}\n' > "$WS/apps/deployy/xbin.json"
cat > "$WS/apps/deployy/index.html" <<'EOF'
<!doctype html><meta charset="utf-8"><title>deployy</title>
<p id="v">deployy: the seeded page</p>
EOF
api POST /grants '{"from":"apps/deployy","target":"apps/leads","role":"reader"}'
# an http provider with a deliberately long path + a consumer with a multi
# slot — the tile-admin window's overflow case (D56)
LONG=apps/a-provider-with-a-deliberately-long-component-path-for-overflow
api POST /create "{\"path\":\"$LONG\",\"owner\":\"org:devs\",\"title\":\"feed provider\"}" | head -c 120; echo
printf '{\n  "provides": { "feed": { "kind": "http", "service": "feed" } }\n}\n' > "$WS/$LONG/xbin.json"
api POST /create '{"path":"apps/consumer","owner":"org:devs","title":"consumer"}' | head -c 120; echo
printf '{\n  "interfaces": { "net": { "kind": "net" }, "feeds": { "kind": "http", "service": "feed", "multi": true } }\n}\n' > "$WS/apps/consumer/xbin.json"
sleep 2

say "shared screens + folders (D37/D55)"
api PUT /screens/org '{"org":"devs","name":"Devs HQ","edit":"write","tiles":[{"path":"apps/crawler","x":0,"y":0,"w":576,"h":384},{"path":"apps/pinned","x":576,"y":0,"w":576,"h":384}]}'
api PUT /screens/org '{"org":"sales","name":"Sales board","edit":"admins","tiles":[{"path":"apps/leads","x":0,"y":0,"w":576,"h":384}]}'
api PUT /screens/folders '{"scope":"org:devs","folders":[{"id":"f1","name":"Crawling","icon":"🕷","items":["apps/crawler","apps/offline"]}],"rev":0}'
api PUT /screens/folders '{"scope":"ws","folders":[{"id":"w1","name":"Docs","icon":"📚","items":["tiles/apidocs","apps/welcome"]}],"rev":0}'

say "bindings"
api POST /bindings '{"component":"apps/pinned","slot":"net","provider":"internet:api.github.com:443"}'
api POST /bindings '{"component":"apps/offline","slot":"net","provider":"none"}'
api POST /bindings '{"component":"apps/leads","slot":"net","provider":"lan:10.42.7.0/24"}'
say "uncovered bind (expected 400 naming the set)"
api POST /bindings '{"component":"apps/pinned","slot":"net","provider":"lan:192.168.1.0/24"}'
say "org on a personal tile (expected 400)"
api POST /bindings '{"component":"apps/dev1-notes","slot":"net","provider":"org"}'
say "narrow sales → apps/leads goes inert"
api PATCH /orgs/sales '{"netSets":["sales-net"]}' >/dev/null
sleep 1

say "a scripted sandbox manager: apps/fakesbx (the agentSandbox pass)"
# hack/fakesandbox as a Go tile — the sandbox-manager contract (D115,
# docs/sandbox-manager.md "Wiring") with every sandbox a directory under its
# own `boxes` filesystem resource and every command a HOST process (a test
# fixture: nothing is isolated), terminals host PTYs. Its source is copied;
# it needs the SDK's sdk/ws (the workspace go.work resolves the sdk to this
# checkout: run.sh's XBIN_SDK_PATH). Bound to the agent's `sandboxes` slot
# below, once the agent exists.
api POST /create '{"path":"apps/fakesbx","runtime":"go","title":"fake sandboxes"}' | head -c 200; echo
FSB="$WS/apps/fakesbx"
mkdir -p "$FSB/backend"
cp "$REPO/hack/fakesandbox/fsb.go" "$REPO/hack/fakesandbox/main.go" "$FSB/backend/"
printf 'module fakesbx\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n' > "$FSB/go.mod"
printf '{\n  "resources": { "boxes": { "type": "filesystem" } }\n}\n' > "$FSB/scope.json"
cat > "$FSB/xbin.json" <<'EOF'
{
  // hack/fakesandbox as a tile (seeded by hack/ui-harness/seed.sh)
  "runtime": "go",
  "uses": [{ "target": "res:apps/fakesbx/boxes", "role": "writer" }],
  "provides": { "sandboxes": { "kind": "http", "service": "sandbox-manager", "role": "consumer" } },
  "expose": { "roles": { "consumer": "Use this tile's sandboxes (the sandbox-manager contract)" } }
}
EOF
cat > "$FSB/index.html" <<'EOF'
<!doctype html><meta charset="utf-8"><title>fake sandboxes</title>
<p>hack/fakesandbox: a scripted sandbox manager for the UI harness. Every sandbox is a host directory; nothing is isolated.</p>
EOF

say "agent template → llm-gw → fakeopenai (the agentTemplate pass)"
# llm-gw (host egress: fakeopenai listens on loopback) with one backend
# "fake"; an agent instance whose model is fake/fake-chat (Chat Completions;
# the pass switches to fake/gpt-5-fake for the Responses wire).
api POST /builtins/import '{"name":"llm-gw"}' | head -c 300; echo
api POST /bindings '{"component":"apps/llm-gw","slot":"net","provider":"host"}'
api PUT /vault/apps/llm-gw/api-token-fake '{"value":"sk-fake"}'
# unpartitioned (every agent pass, and the isolated sandbox passes whose
# managers lack `partitions`, are written for one instance), unless
# HARNESS_AGENT_PARTITION=1 keeps the template's partitioned default (run.sh)
agent_new='{"source":"agent","path":"apps/agent","partition":false}'
[[ -n "${HARNESS_AGENT_PARTITION:-}" ]] && agent_new='{"source":"agent","path":"apps/agent"}'
api POST /templates/new "$agent_new" | head -c 300; echo
# the agent's models come through its `llm` interface (D111): bound to
# llm-gw — the binding is the grant
api POST /bindings '{"component":"apps/agent","slot":"llm","providers":["apps/llm-gw"]}'
# its coding sandboxes (D115) come from apps/fakesbx: the binding grants the
# agent the manager's consumer role
api POST /bindings '{"component":"apps/agent","slot":"sandboxes","providers":["apps/fakesbx"]}'
api POST /grants '{"from":"apps/agent","target":"cap:open-links","role":"writer"}'
# dev1 may open (and chat with) the agent — the agentConvs pass: per-user
# conversations and sharing (D83)
api PUT /access '{"tile":"apps/agent","kind":"user","id":"dev1","level":"read"}'
# no web egress: keeps the root page's pending-bindings panel (which pushes
# the canvas down for every pass) one row shorter — only its mcp slot shows
api POST /bindings '{"component":"apps/agent","slot":"net","provider":"none"}'
gw() { curl -s -X "$1" -H "$A" -H "$J" ${3:+-d "$3"} "$URL/api/apps/$2"; echo; }
# (the polls below read every byte: `grep -q` stops early, curl then dies of
# SIGPIPE on a long answer, and under pipefail the poll never succeeds)
for _ in $(seq 1 120); do gw GET llm-gw/config | grep backends >/dev/null && break; sleep 1; done
gw PUT llm-gw/config/backend "{\"name\":\"fake\",\"baseURL\":\"http://${FAKEOPENAI_ADDR:-127.0.0.1:18977}\"}"
for _ in $(seq 1 180); do gw GET agent/config | grep '"system"' >/dev/null && break; sleep 1; done
gw GET agent/config | python3 -c 'import json,sys
c=json.load(sys.stdin); c.update(model="fake/fake-chat", subagents=True, maxActiveRuns=4)
print(json.dumps(c))' > "$WS/.agent-config.json"
gw PUT agent/config "$(cat "$WS/.agent-config.json")" | head -c 200; echo
rm -f "$WS/.agent-config.json"

say "the builtin sandbox manager on its test backend: apps/coding-sandbox (the codingSandbox pass)"
# a copy of the coding-sandbox template (D122) whose backend also carries the
# template's `fake` backend — its fake_*_test.go files, renamed: every
# sandbox a host directory under a `boxes` resource of its own, every
# command a host process (a test fixture: nothing is isolated). The pass
# switches its config to it and binds it to the agent beside apps/fakesbx.
api POST /templates/new '{"source":"coding-sandbox","path":"apps/coding-sandbox"}' | head -c 300; echo
CS="$WS/apps/coding-sandbox"
for f in backend exec files tty; do cp "$REPO/builtin-templates/coding-sandbox/_backend/fake_${f}_test.go" "$CS/_backend/fake_${f}.go"; done
sed -i 's|"db": { "type": "sqlite" }|"db": { "type": "sqlite" }, "boxes": { "type": "filesystem" }|' "$CS/scope.json"
# (a copy's xbin.json is plain JSON: the template block and comments go)
python3 - "$CS/xbin.json" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
m["uses"].append({"target": "res:apps/coding-sandbox/boxes", "role": "writer"})
json.dump(m, open(sys.argv[1], "w"), indent=2)
PY
# dev1 may open its page with read access: a read-only view (D122 addendum)
api PUT /access '{"tile":"apps/coding-sandbox","kind":"user","id":"dev1","level":"read"}'

say "messaging bridge + webhooks → the agent (the channels pass)"
# a copy of the agent-messaging-bridge template (alwaysOn; no platform added,
# so its console plays one) and the webhooks tile (/hook/* on the ingress
# listener as hooks.test), both bound to the agent's inbox. dev1 may open the
# bridge: they link their chat account there.
api POST /templates/new '{"source":"agent-messaging-bridge","path":"apps/bridge"}' | head -c 300; echo
api POST /builtins/import '{"name":"webhooks"}' | head -c 300; echo
api POST /bindings '{"component":"apps/bridge","slot":"agent","provider":"apps/agent"}'
api POST /bindings '{"component":"apps/webhooks","slot":"agents","provider":"apps/agent"}'
api POST /bindings '{"component":"apps/webhooks","slot":"hooks","provider":"runtime","host":"hooks.test"}'
api PUT /access '{"tile":"apps/bridge","kind":"user","id":"dev1","level":"read"}'

SSHA=${SBXTERM_SSH_ADDR:-127.0.0.1:8699}
say "sandbox-terminal → apps/fakesbx, SSH on $SSHA (the sandboxTerminal pass)"
# the builtin people's-terminals tile (D121): bound to the fake manager (the
# binding grants it the manager's consumer role), its SSH expose published on
# a host port, and the address people type set by its owner (the tile can't
# see xbind's port binding). Without --isolate its backend listens on the
# host's :2222 (the manifest's port) behind the relay.
api POST /builtins/import '{"name":"sandbox-terminal"}' | head -c 300; echo
api POST /bindings '{"component":"apps/sandbox-terminal","slot":"sandboxes","providers":["apps/fakesbx"]}'
api POST /bindings "{\"component\":\"apps/sandbox-terminal\",\"slot\":\"ssh\",\"provider\":\"runtime\",\"listen\":\"$SSHA\"}"
for _ in $(seq 1 180); do gw GET sandbox-terminal/me | grep '"listening":true' >/dev/null && break; sleep 1; done
gw PUT sandbox-terminal/settings "{\"sshAddress\":\"$SSHA\"}"
# dev1 may open it (read): nothing is shared with dev1 — the empty state
api PUT /access '{"tile":"apps/sandbox-terminal","kind":"user","id":"dev1","level":"read"}'

say "state"
api GET /orgs | python3 -c 'import json,sys
for o in json.load(sys.stdin)["orgs"]: print(o["id"], o.get("netSets"), o.get("resolvedNet"), "host" if o.get("netHost") else "")'
api GET /bindings | python3 -c 'import json,sys
d=json.load(sys.stdin); print("bindings:", d["bindings"]); print("inert:", d["inert"])
for p in d["pending"]:
  if p["kind"]=="net": print("pending", p["component"], "default=", p.get("default"), [o["id"]+" | "+o["label"] for o in p["options"]])'
for t in apps/crawler apps/racks apps/leads apps/dev1-notes; do
  printf '%s term-net(admin): ' "$t"; api GET "/term-net?tile=$t"
  printf '%s tile-status.net: ' "$t"; api GET "/tile-status?component=$t" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("net"))'
done
