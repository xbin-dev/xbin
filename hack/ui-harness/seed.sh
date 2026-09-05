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
  "tiles":{"tiles/organisations":"read","apps/leads":"terminal"},
  "orgs":[{"org":"devs","level":"terminal","create":true,"admin":true}]}' | head -c 300; echo
api POST /users '{"id":"sales1","name":"Sales One","role":"user","password":"salespass123","termNet":true,
  "tiles":{"tiles/organisations":"read"},
  "orgs":[{"org":"sales","level":"terminal","create":true,"admin":true}]}' | head -c 200; echo
api POST /users '{"id":"infra1","name":"Infra One","role":"user","password":"infrapass123",
  "tiles":{"tiles/organisations":"read"},
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
mk apps/leads   org:sales
mk apps/dev1-notes user:dev1
sleep 2

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
