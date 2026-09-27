# 11 — Contract: the exact wire

> Status: live — the authoritative wire spec for tile deployments and paused live reload: routes, URL routing, events, headers, env, SDK, tokens, `bx` and on-disk formats (part of [plans/dev-lifecycle](README.md))

This document turns [05-model.md](05-model.md) into bytes. It fixes every
route, field, header, env var, token field, event, `bx` command and file that
anything outside xbind's own code can observe: a browser, a tile's code, `bx`,
the shipped iOS app, or an older or newer xbind. Vocabulary is
[01-glossary.md](01-glossary.md)'s, verbatim.

Where a number, key scheme or mechanism belongs to another document, this one
links it instead of restating it:
- runtime mechanics, caps, build and cgroup layout: [07-runtime.md](07-runtime.md);
- data namespaces, vault keys, seeding, backup, GC: [08-data.md](08-data.md);
- edge kinds and their defaults, the read clamp, route classes: [09-fabric.md](09-fabric.md);
- UI copy and glyphs: [10-ux.md](10-ux.md);
- compat proofs, downgrade and fixtures: [12-compat.md](12-compat.md).

The baseline is master: D112 is landed, and D113 is designed
([plans/tile-sandboxes.md](../tile-sandboxes.md)). Code references are
against this checkout and were re-checked for this document. They come from
[research/builder-contract.md](research/builder-contract.md),
[research/serving-fabric.md](research/serving-fabric.md),
[research/identity-resources.md](research/identity-resources.md),
[research/inbound-edges.md](research/inbound-edges.md),
[research/ui-surfaces.md](research/ui-surfaces.md),
[research/delivery-infra.md](research/delivery-infra.md) and
[research/sandbox-visibility.md](research/sandbox-visibility.md).

## 0. Rules every surface follows

### 0.1 Absent means today (P5, P17)

A tile with no deployment record produces exactly today's bytes on every
surface in this document. Everything new is one of:
- a new route or a new event type;
- a field, header, env var or meta that is absent for the zero state;
- a URL form that resolves only for tiles that have a record (§2.2).

There is no manifest key, no boot migration and no write at boot. Nothing
this document adds, dry runs and diffs included, creates a file for a tile
without a record, except the committed opt-ins of §1.2.

### 0.2 Two naming rules

Markers follow one of two rules. The rules agree while `main` is the primary,
which is always the case in the zero state and stays so until a tile manager
reassigns the primary (flow F).

| Rule | Markers | Present when | Why |
|---|---|---|---|
| **Role rule**: absent means the primary | `X-XBin-Deployment` (§4); `XBIN_DEPLOYMENT` (§5); `<meta name="xbin-deployment">` and `xbin.deployment` (§6); whoami `deployment` (§7.7) | the deployment concerned is not the primary | They tell callees and tile code "this is not what the bare URL serves". Old clients keep treating the primary as the tile. |
| **Name rule**: absent means `main` | the frame-token claim (§7.2); tile-origin labels, tickets and cookies (§2.6, §7.5); registry and `/sandboxes` ids and `deployment` (§8); `deployment` on `bus` events (§3.2); every storage key ([08-data.md](08-data.md)) | the deployment is not `main` | They bind a credential or stored state to one deployment. They must not change meaning when routing moves (P6, P12). |

Consequences:
- Today's event types (`reload`, `build-*`, `status`, notify) never carry a
  marker: they speak only of the primary, with the bare component (§3).
  Everything about a non-primary deployment travels in the `deployments`
  type.
- A primary reassignment restarts (blue/green) the running backends of the old
  and the new primary, so `XBIN_DEPLOYMENT` stays true for the whole life of
  every backend process (P17; mechanics in [07-runtime.md](07-runtime.md)
  §8.8). It restarts the sessions named after either of them too (§7.4).
- Descriptive fields on admin views (`deployment` on `/backends` and
  `/runtime` rows) follow neither rule. They are present on every row of a
  tile that has a record, and absent otherwise. `/sandboxes` rows follow the
  registry, which uses the name rule (§8).

### 0.3 Tile refs, deployment names, checkpoint ids

```
name          = ^[a-z][a-z0-9-]{0,23}$   ; the glossary grammar; "main" always exists and can't be added
tile-ref      = <tile path> [ "+" name ] ; "apps/crm", "apps/crm+dev"
checkpoint-id = "c:" 7*64 lowercase hex  ; a unique prefix of the checkpoint's tree object id
```

- A **tile ref** is resolved by §2.2 wherever it is accepted: URL paths under
  `/c/` and `/api/`, the `tile` field of the `/deployments` routes, `bx`
  arguments, and `<bx-frame src>`.
- Existing routes that take a tile path in a parameter (`component=`,
  `tile=`, `cwd=`) keep taking the **bare** path, byte for byte. They gain a
  separate `deployment` parameter instead. A qualified string there is looked
  up literally, as today.
- JSON names a deployment with a separate field (`deployment`, `from`, `to`).
  No `component` field ever carries the qualified string, in events or in
  rows. It appears only in `Deployment.url` and `Deployment.api` (§1.1).
- A checkpoint id in a response is the shortest unique prefix of at least 7
  digits at that moment. Clients that keep ids use `Checkpoint.hash`.

### 0.4 Which deployment a call acts on

- **DR1, self.** A call by one of the tile's own principals (§7.1) on its own
  tile acts on the principal's **bound deployment**:
  - `/api/<tile>/…` self-calls (P12);
  - the self-scoped xbind routes: vault, kv, blob, bus publish, cron, bus
    subscriptions, iface-instances, ingress-hosts, tile-report, notify, prefs,
    and (once D113 is built) the tile-managed sandboxes API;
  - `/logs` and `/tile-status` without a `deployment` parameter.

  A self-scoped **write** is never directed at another deployment of the tile
  (403). Frame and instance principals read `/logs` and `/tile-status` of
  their bound deployment only ([09-fabric.md](09-fabric.md) §4.2). The tile's
  terminal and agent tokens may name any deployment of the tile in those two
  reads, as the humans who drive them may.
- **DR2, named.** A call that names a tile acts on its **primary**, unless it
  names a deployment (a `deployment` parameter or a qualified URL). Naming a
  non-primary deployment needs write at the human's current level, or a frame
  or instance principal bound to that deployment. The tile's terminal and
  agent tokens may also name any deployment in xbind's own reads (`/logs`,
  `/tile-status`, `/deployments`); their calls to `/api/<tile>+<name>/` follow
  §2.3, which admits only their target. Other tiles' principals never reach a
  non-primary deployment (P7).
- **DR3, bx.** `bx` sends an explicit deployment when the user gave one. For
  read commands only, it falls back to `$XBIN_DEPLOYMENT` (§9.1). A command
  that moves code never takes its target from the env.

### 0.5 Authority vocabulary

- **Levels** (D16): read and write are `Principal.CanReadTile` and
  `CanWriteTile` (`internal/auth/auth.go:102-103`). **Terminal level** is
  `CanTerminalTileVia` (`internal/auth/sessiongate.go:7-21`):
  - humans with terminal level, and admins;
  - the tile's own terminal and agent-session tokens, while their driving
    user keeps terminal level.

  A tile's frame and instance tokens never have terminal level, so a tile's
  frontend or backend can't operate its own deployments.
- **A human session** is a principal with `p.Component == ""`: a session
  cookie, a device or app token, the owner token or the bootstrap token.
  Terminal, agent, frame and instance tokens, and tile-origin cookies, are
  element principals (`Component` set).
- **Tile managers** (D24/D33), in the deployments plane: a human session
  **and** (`Broker.IsAdmin(p)` or `mayManageTile(p, tile)`). The first clause
  is explicit, because both halves of the second pass element principals
  today: `Broker.IsAdmin` passes any element of a tile granted `xbin` admin
  (`internal/broker/broker.go:466-475`), and `mayManageTile`
  (`internal/broker/orgsapi.go:427-443`) calls `canManageUsers`, which passes
  any element of a tile granted `xbin` admin or `xbin:users` writer
  (`internal/broker/usersapi.go:37-50`), before `humanID` is consulted
  (`internal/broker/orgsapi.go:73-78`). So no element principal passes the
  manager gate here, whatever grants its tile holds; terminal and agent tokens
  are refused even when their driver is a manager, because agents share them.
  Every manager route answers such a caller 403 with §1.14's human-session
  text.
- **The tile's own principals**: element principals whose `Component` is the
  tile (frame tokens, instance tokens, terminal and agent tokens, the
  tile-origin cookie). Each is bound to one deployment (§7.1).
- **Audiences** decide who learns what (events §3.4, state §1.3, logs §8):
  - the **reader audience** of tile T: everyone who may read T today: humans
    with read, admins, and T's own principals of any binding;
  - the **write audience** of T: admins; humans with at least write on T, at
    their current level, checked on every request and every event delivery;
    and T's terminal and agent tokens while their driving user holds at least
    write on T;
  - **D's own principals**, for a non-primary deployment D: T's frame and
    instance principals bound to D. A frame principal that carries a user
    counts only while that user holds at least write on T.

  A fact about a non-primary deployment D (its name, status, builds and
  compiler output, logs, would-notify lines, data operations, deploy entries,
  registrations) reaches only T's write audience and D's own principals.
  Facts about the primary reach the reader audience, as today. The primary's
  frame and instance principals are readers here: a frame token minted for a
  reader is never an own principal of a non-primary deployment. Other tiles'
  principals receive no non-primary fact.
- **Route classes (P26).** Every `/api/xbin/*` route is classified
  deployment-scoped, primary-only or neutral
  ([09-fabric.md](09-fabric.md) §6). A principal bound to a non-primary
  deployment is refused, 403, on any route without a class, reads included.
  Every route in this document is deployment-scoped: its handler applies the
  gates written here.
- **View-as** (D64): every POST in this document is refused with 403 by the
  existing middleware (`internal/server/server.go:229`,
  `internal/server/impersonate.go:23-29`). GETs answer as for the viewed user.

### 0.6 Wire hygiene

- **New routes, not new fields on existing bodies.** `server.DecodeJSON`
  rejects unknown fields (`internal/server/api.go:71-76`), so an added body
  field would 400 on an older server and would break lagging clients.
  Existing routes gain only query parameters and response fields. Where an
  older server would silently ignore a new parameter, the answer echoes it and
  clients check the echo (§7.4, §8). `POST /backup`, `POST /restore` and
  `POST /backup-schedule` keep their bodies; per-deployment backup, restore
  and schedules are new routes (§1.8).
- **Errors** go through `server.WriteError`: `{"error": msg, "docs": page}`
  (`internal/server/api.go:53-59`, D60), whose shape never grows a field. New
  handlers never use `http.Error`, as `/backends` still does
  (`internal/boot/api.go:21-27`).
- **Audit:** every POST under `/deployments` is audited by the existing rule
  (`internal/server/server.go:648-667`).
- **Encoding:** JSON everywhere except the diff (text/x-diff) and the git
  remote (git's files). Times are RFC 3339 UTC strings, as in `/term/sessions`.
  `by` is `user:<id>` or `owner`, as `Principal.From` spells humans
  (`internal/auth/auth.go:196-207`).

## 1. HTTP API

### 1.1 Types

```jsonc
// State: the answer of GET /deployments and of every POST /deployments/*. This is the full view (§1.3).
{
  "tile": "apps/crm",
  "selected": "dev",                  // present when the request's tile ref was qualified
  "record": true,                     // false = the zero state: the rest is synthesized, nothing is written
  "schema": 1,                        // the record's schema number (§10.1); 0 in the zero state
  "seq": 18,                          // the record's sequence number; 0 in the zero state
  "features": ["deployments/1"],      // what this xbind speaks (NP-11-13)
  "owner": "org:devs",                // the tile's owner (D24), as /components reports it: refusal texts name its managers
  "view": "full",                     // full | deployment | reader (§1.3)
  "primary": "main",
  "liveReload": "dev",                // the live reload target; "" = paused
  "lastLiveReload": "dev",            // where resume and reload now go
  "liveReloadSince": {"at": "…", "by": "user:ana"},   // the last attach, pause or resume of live reload ("paused by ana 12m ago")
  "workTree": {"changed": 3, "since": "c:3f2a1c9"},  // while paused: files that differ from lastLiveReload's checkpoint
  "protectedPrimary": false,
  "allowed": {"pause": {"ok": true}, "deployments": {"ok": false, "why": "…", "kind": "policy"}},  // what the TILE may do
  "caps": {"tile": 3, "tileUsed": 1, "workspace": 24, "workspaceUsed": 7},  // non-primary deployments; numbers: 07-runtime.md §10.3
  "deployments": [ /* Deployment */ ],   // "main" first, then by creation
  "edges": [ /* Edge */ ],
  "caller": { /* Caller */ }
}

// Can: one permission, computed by the policy that will judge the request
{"ok": false, "why": "tile managers only, in a person's own session: ask an admin of org devs, or a workspace admin", "kind": "authority"}
//  kind (with ok:false): authority (level, tile manager, human session, protected primary, view-as) | policy (the tile
//  or this xbind can't: chrome, xbin:* capability, no isolation, a cap, an edge that can't be read-clamped)
//  | state (not now: live reload not paused, already primary, no record yet, …)

// Deployment
{
  "name": "dev",
  "primary": false,
  "liveReload": true,                 // follows the work tree
  "checkpoint": null,                 // a Checkpoint, or null exactly when liveReload is true
  "status": {
    "state": "healthy",               // idle | building | healthy | failed | crash-looping | static (no backend)
    "gen": 3,
    "serving": "work-tree",           // what the current generation runs: "work-tree" or a checkpoint id (07-runtime.md §8.2)
    "error": "…",                     // the last build/start/health failure, at most 500 bytes (full text: GET /logs)
    "deploying": {"id": 43, "how": "deploy", "result": "running", "phase": "build"},  // the deploy in flight, if any
    "queued": [{"id": 44, "how": "deploy"}]                                             // FIFO (§1.2)
  },
  "url": "/c/apps/crm+dev/",          // the primary's is the bare "/c/apps/crm/"
  "api": "/api/apps/crm+dev/",        // absent for a tile without a backend
  "origin": "https://t-….example.com",   // --tile-assets=origins only (§2.6)
  "data": {"state": "seeded", "from": "main", "at": "…", "by": "user:ana", "reset": false, "busy": "seeding"},
                                      // derived from the namespace's ns.json (08-data.md), never stored in the record;
                                      // state: original (main: today's keys) | empty | seeded | restored | partial;
                                      // at, by: when that began; reset: empty because of a reset;
                                      // busy: seeding | resetting | restoring, while it runs
  "vault": {"keys": 4, "placeholders": 1},   // placeholders: key names with no value yet (P14)
  "limits": {"memMiB": 1024, "pids": 512, "diskGiB": 50, "overrides": ["memMiB"]},
                                      // effective resource limits (P22, §1.7); overrides: the ones a manager lowered
  "deliveries": false,                // the primary: always true, not a switch
  "alwaysOn": false,                  // the primary: the manifest's value, not a switch
  "alwaysOnDeclared": true,           // this deployment's code declares "alwaysOn"
  "backup": {"schedule": "0 3 * * *", "retention": 3},   // non-primary only; absent without a schedule (§1.8)
  "registrations": [ /* Registration */ ],
  "wouldNotify": [{"at": "…", "to": "user:bob", "title": "…"}],   // non-primary: the last 20 suppressed notifications
  "lastDeploy": {"id": 41, "how": "promote", "at": "…", "by": "user:ana", "result": "ok"},
  "created": "…", "by": "user:ana",
  "can": {"open": Can, "deploy": Can, "restart": Can, "promoteTo": Can, "rollback": Can, "attach": Can,
          "remove": Can, "reset": Can, "seed": Can, "vaultCopy": Can, "deliveries": Can, "alwaysOn": Can,
          "limits": Can, "backup": Can, "runNow": Can, "primary": Can}
}

// Checkpoint
{"id": "c:3f2a1c9", "hash": "<full tree object id>", "feed": "work-tree", "at": "…", "by": "user:ana"}

// Registration: a cron job, bus push subscription, interface instance or ingress host of ONE deployment
{"kind": "cron", "name": "nightly", "schedule": "0 3 * * *", "path": "/tick", "dormant": true}
{"kind": "bus", "name": "trig-12", "resource": "res:apps/crm/events", "prefix": "orders/", "path": "/trigger/bus/12", "dormant": true}
{"kind": "iface-instance", "name": "acme", "prefix": "/t/acme", "dormant": true}
{"kind": "ingress-host", "name": "crm.example.com", "dormant": true}

// Edge: one outbound edge of the tile, with its policy for the non-primary deployments
{"id": "slot:llm", "kind": "http", "to": "apps/llm-gw", "role": "writer", "policy": "read", "default": "read",
 "values": ["read", "block"], "set": false, "refused": 0, "clamped": 12}
{"id": "slot:net", "kind": "net", "to": "", "policy": "inherit", "default": "inherit", "values": ["inherit", "block"],
 "set": false, "effective": "block", "why": "this tile's network shares the host's: non-primary deployments get no egress",
 "refused": 3, "clamped": 0}
//  id: "slot:<slot>" (an interface binding, the net slot included) | "grant:<target>" (a call grant, a res:, code, gpu: or
//  cap: grant), as 09-fabric.md §5.1 spells them. kind, to, role: descriptive. values: what this edge's kind and role
//  take (09-fabric.md §5.1): read|block where the role can be read-clamped; inherit|block for the net slot and
//  capability grants; block alone for an edge that can't be read-clamped (a custom role with no path to reader,
//  stream and lan-ingress slots, a net slot bound to a provider tile; P23). default: 09-fabric.md §5.1's, among
//  them inherit for the net slot and capability grants except gpu:*, which is block, and block for every edge
//  that can't be read-clamped. set: an override is stored.
//  effective: what applies now, when it differs from policy, with why: a net slot whose tile network resolves to host
//  sharing is block (P23); a stored value this xbind doesn't know, or one no longer valid for the edge, is block (P27).
//  refused, clamped: calls this edge refused, and calls it passed with the role clamped to reader, since xbind started.

// Caller: what the requesting principal may do at tile level (per deployment: Deployment.can)
{"level": "terminal", "manager": false, "humanSession": false, "readOnly": false, "bound": "dev",
 "can": {"pause": Can, "resume": Can, "reloadNow": Can, "add": Can, "edges": Can, "protect": Can}}
//  level: read | write | terminal | admin | tile (a frame or instance token of the tile: no operation rights)
//  manager: passes the tile-manager gate of §0.5 (so always false for element principals)
//  bound: present for the tile's own principals (§7.1); "" for a session following the primary

// DeployEntry: one deploy attempt, queued, in flight or in the deploy log
{"id": 42, "deployment": "main", "how": "promote", "from": "dev",
 "checkpoint": "c:3f2a1c9", "previous": "c:77aa01b", "followsWorkTree": false, "feed": "work-tree",
 "by": "user:ana", "via": "session", "agent": false, "session": "<session id>",
 "requestedAt": "…", "finishedAt": "…", "result": "ok", "phase": "swap", "error": "…"}
//  how: deploy | promote | rollback | reload-now | resume | pause | attach | add | protect | reassign | restart
//  result: queued | running | ok | failed | cancelled; phase, while running: checkpoint | materialize | build | start | swap
//  followsWorkTree: resume and attach; the checkpoint is then the capture taken at that moment
//  via: the principal's Via, verbatim; agent and session: a terminal or agent session token acted
//  error: failed only, at most 500 bytes; the full text is in the deployment's log (GET /logs)

// Impact: the answer of a dry run (§1.2), what the confirmation dialog and bx's prompt render
{"code": {"deployment": "main", "from": "c:3f2a1c9", "to": "c:7b19e02", "files": 3, "added": 40, "removed": 12,
          "workTreeAt": "…"},                // null when no capture was taken (a tile without a record, §1.2);
                                            // workTreeAt: the capture time when the code comes from the work tree
 "data": "none",                            // none | seed | erase | restore: what happens to deployment data
 "joins": {"scope": "apps/shop", "state": "seeded", "by": "user:ana", "at": "…"},   // add: the (scope, name)
                                            // namespace already exists; absent when the deployment starts empty
 "placeholders": ["STRIPE_KEY"],            // primary: vault keys the new primary has no value for (08-data.md §10)
 "pausesLiveReload": false,
 "stops": [],                               // deployments that stop (remove, seed, reset, restore), sibling tiles' included
 "affects": "everyone",                     // everyone (the primary's viewers) | deployment (one non-primary's) | nobody
 "reloads": ["main"]}                       // deployments whose open frames reload once
```

Fields a view leaves out are listed in §1.3. The reader view never carries a
field of this block that names a non-primary deployment.

### 1.2 Conventions of the `/deployments` family

- **Base path** `/api/xbin/deployments`. It is reachable over the console, the
  gateway socket and tile origins, like every `/api/xbin` route
  (`internal/server/server.go:563-590`).
- **Bodies** are JSON, decoded strictly. Every body has `tile`, a tile ref. An
  operation on one deployment takes it from `deployment` or from the ref's
  qualifier; both given and different is a 400.
- **`seq`** is the record sequence the caller acted on. It is optional on
  every POST, except an operation onto a protected primary, which requires
  it. A moved record answers 409, so a confirmation dialog is exact.
- **`confirm`** tokens guard data (NP-11-5). A missing or wrong token answers
  400 and names the token.

  | Route | Token |
  |---|---|
  | `remove` | `"erase"` |
  | `reset`, and `restore` into a deployment that has data | `"erase-data"` |
  | `seed`, and `add` with `data:"seed"` | `"copy-data"` |
  | `primary` | `"data-stays"` |

- **Reviewed code (`checkpoint`, `expect`).** `expect` is the checkpoint the
  caller reviewed, typically the `X-XBin-Checkpoint-To` of a diff (§1.11) or
  the `impact.code.to` of a dry run. The server moves exactly that code or
  answers 409: when the code would come from the work tree, the fresh capture
  must equal `expect`; when it comes from a pinned deployment, that
  deployment's checkpoint must equal it. `expect` is optional on `deploy`
  (without `checkpoint`), `promote`, `live-reload/now`, `primary` and
  `protect`.
- **Onto a protected primary** every code move names the checkpoint its actor
  reviewed (P21; 05-model §5, *Reviewed operations*): `checkpoint` on
  `deploy` and `rollback`, `expect` on `promote`, `live-reload/now` and
  `primary`, plus `seq` on all of them. Without them the answer is 400
  (§1.14) and nothing is captured. The server commits only while `seq` is
  unchanged, re-checks the actor's authority at commit, and never deploys a
  capture the request did not name.
- **`dryRun: true`** is accepted by every POST (NP-11-20). The request is
  judged exactly as for real, including refusals, and nothing changes. The
  answer is `200 {"state": State, "impact": Impact}`. On a tile with a record,
  a dry run that needs the work tree takes its checkpoint (content-addressed,
  collectable by GC), so `impact.code.to` can be sent back as `expect` or
  `checkpoint`. This follows the D39 `GET /owner/preview` precedent.
- **A tile without a record** accepts three POSTs, the opt-ins:
  `live-reload/pause`, `add` and `protect`. Their dry runs capture nothing and
  create no store: `impact.code` is `null`, and the report says the code is
  the work tree as it is when the request commits. Pausing live reload moves
  nothing a save wasn't about to move (05-model §5), so it takes no `expect`.
  Every other POST on such a tile answers 409 (§1.14). The checkpoint store
  is created by the first committed opt-in, never before (05-model §5).
- **Answer:** `200 {"state": State, "deploy"?: DeployEntry, …}` once the
  record change is committed. A request that moves no code, because the
  deployment already runs that checkpoint and is healthy, answers
  `"unchanged": true` and no `deploy`.
- **Deploys are asynchronous.** `deploy` carries the entry with `result`
  `queued` or `running`. Progress arrives as `deployments` events (§3.3) and
  through `GET /deployments/log?id=` (§1.10).
- **Queueing** is [07-runtime.md](07-runtime.md) §8.3's: one deploy in flight
  per deployment, the rest in a FIFO of at most 8. A request whose checkpoint
  equals the queue's tail is merged into it, and the answer carries that
  entry. A full queue answers 409. The checkpoint is taken at request time.
  Removing the deployment or disabling the tile ends its queued and in-flight
  deploys `cancelled`.
- **The record's `checkpoint`** changes after a successful swap for a pinned
  deployment, and at request time when a deployment leaves the work tree
  (07-runtime.md §8.2). A crash or restart mid-deploy therefore starts the
  record's code (P9).

### 1.3 Reading the state

```
GET /api/xbin/deployments?tile=<tile-ref>
200        State, in the caller's view (below)
authority  the tile's reader audience (§0.5)
errors     400 need ?tile= · 403 can't read the tile · 403 a qualified ref naming a non-primary deployment, for
           a caller outside that deployment's audience (whether or not it exists) · 404 no such tile, or no
           such deployment (callers in the audience) · 409 the record's schema is newer than this xbind (§10.1)
notes      The zero state answers record:false and writes nothing (P5). A plain 404 or 405 (Go's mux,
           not JSON) means an older xbind: that is how clients detect the feature (12-compat.md NP-12-2).
```

The answer depends only on the caller's audience, never on how many
deployments exist, so a reader can't count them:

| View | Who gets it | What it holds |
|---|---|---|
| `full` | T's write audience | the State of §1.1, every field |
| `deployment` | D's own principals (a non-primary D) | the full view restricted to the primary and D: other deployments are left out, `liveReload` and `lastLiveReload` read `""` when they name one of them, and `edges`, `caps`, `allowed` and `workTree` are omitted |
| `reader` | the rest of T's reader audience: humans with read only, the primary's frame and instance principals | the primary's facts only (below) |

The reader view:

```jsonc
{
  "tile": "apps/crm", "record": true, "schema": 1, "features": ["deployments/1"], "owner": "org:devs",
  "view": "reader",
  "primary": "main",
  "liveReload": "",                   // the primary's name while it follows the work tree; "" otherwise
                                      // (live reload paused, or saves reach another deployment: readers can't
                                      // tell which)
  "protectedPrimary": false,
  "deployments": [{                   // the primary alone
    "name": "main", "primary": true, "liveReload": false,
    "checkpoint": {"id": "c:3f2a1c9", "hash": "…", "at": "…", "by": "user:ana"},
    "status": {"state": "healthy", "gen": 12, "serving": "c:3f2a1c9",
               "deploying": {"result": "running", "phase": "build"}},
    "url": "/c/apps/crm/", "api": "/api/apps/crm/", "origin": "…",
    "lastDeploy": {"at": "…", "by": "user:ana", "result": "ok"}
  }],
  "caller": {"level": "read", "manager": false, "humanSession": true, "readOnly": false,
             "can": { /* every Can false, kind authority, why "needs write access" */ }}
}
```

- Left out of the reader view: `selected` (except the primary alias), `seq`,
  `lastLiveReload`, `liveReloadSince`, `workTree`, `allowed`, `caps`,
  `edges`; every non-primary deployment; and on the primary, `status.error`,
  `status.queued`, `data`, `vault`, `limits`, `deliveries`, `alwaysOn`,
  `alwaysOnDeclared`, `registrations`, `wouldNotify`, `created`, `by`, `can`,
  and `id`, `how`, `from` and `session` on its deploy entries: a promotion
  names the deployment it came from, and deploy ids are counted across every
  deployment of the tile.
- A zero-state answer (`record:false`) is the same for every view, as today.
- [10-ux.md](10-ux.md)'s reader rendering and its harness assert this view,
  not a 403.

### 1.4 Live reload

```
POST /api/xbin/deployments/live-reload/pause
body       {"tile": "apps/crm", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}              how: "pause"
effect     liveReload = ""; lastLiveReload = X, the former target. X is pinned to a fresh checkpoint of the
           work tree: normally a code-identical swap, or the newest work tree once if it moved (model §5).
           On a tile without a record this is an opt-in: the record and the store are created (§1.2).
authority  terminal level. With X the primary this pins the primary: parity (P4).
idempotent yes: live reload already paused answers 200 with no deploy
errors     403 not terminal level · 409 a backend tile without --isolate (P18) · 409 stale seq
```

```
POST /api/xbin/deployments/live-reload/resume
body       {"tile": "apps/crm", "deployment"?: "dev", "seq"?: 18}     default deployment: lastLiveReload
200        {"state": State, "deploy": DeployEntry}                     how: "resume", followsWorkTree: true
effect     liveReload = Y. Y deploys from the work tree and follows every save. Resuming onto main, when
           main is the only deployment and every setting is at its default, removes the record: the answer
           has record:false (P5, §10.5).
authority  terminal level; the primary as Y is parity (P4)
idempotent yes: live reload already on Y answers 200 with no deploy
errors     403 not terminal level · 404 no such deployment · 409 live reload is on another deployment
           (use attach) · 409 Y is a protected primary, for everyone (P21) · 409 no record · 409 stale seq
```

```
POST /api/xbin/deployments/live-reload/now
body       {"tile": "apps/crm", "expect"?: "c:<id>", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}              how: "reload-now"
effect     Checkpoint the work tree and deploy it once to lastLiveReload, which stays pinned. A work tree
           identical to lastLiveReload's checkpoint answers unchanged:true.
authority  terminal level. With lastLiveReload the primary: parity. A protected primary takes a tile manager
           in a human session, with expect and seq (§1.2).
errors     400 onto a protected primary without expect or seq · 403 not terminal level · 403 protected
           primary · 409 live reload isn't paused · 409 expect mismatch · 409 no record · 409 stale seq
```

```
POST /api/xbin/deployments/live-reload/attach
body       {"tile": "apps/crm", "deployment": "dev", "seq"?: 18}
200        {"state": State, "deploy": DeployEntry}               Y's entry: how "attach", followsWorkTree: true
effect     The work tree is checkpointed and the former target X is pinned to that checkpoint (its entry is
           how "attach" too). Y deploys from the work tree and follows it.
authority  terminal level; the primary as Y is parity (P4)
idempotent yes: already on Y answers 200 with no deploy
errors     403 · 404 no such deployment · 409 live reload is paused (use resume) · 409 Y is a protected
           primary · 409 no record · 409 stale seq
```

### 1.5 Adding and removing deployments

```
POST /api/xbin/deployments/add
body       {"tile": "apps/crm", "deployment": "dev",
            "from"?: "work-tree" | "primary" | "c:<id>",  default "work-tree" (a fresh checkpoint)
            "data"?: "empty" | "seed",                    default "empty"
            "attach"?: false,                              true: attach live reload to the new deployment
            "confirm"?: "copy-data",                       required with data:"seed"
            "seq"?: 18}
200        {"state": State, "deploy": DeployEntry, "joins"?: {…}}   how: "add" (and "attach" entries when attach:true)
effect     Creates the deployment with its code, a data namespace, a vault holding the primary's key names as
           placeholders, deliveries and alwaysOn off, limits at the tile's (model §5). The namespace is
           (scope, name) (08-data.md §6): when a sibling tile of the scope already claims it, the new
           deployment joins it, and the answer's joins names its state and who seeded or restored it.
           On a tile without a record this is an opt-in (§1.2).
authority  terminal level. data:"seed" is a tile manager's act. Joining a namespace whose state is seeded,
           restored or partial is a tile manager's act on this tile (08-data.md §6.2); joining an empty one
           is not.
errors     400 bad name · 400 missing confirm · 403 · 403 joining seeded data · 404 unknown checkpoint
           · 409 "main" always exists · 409 the name is taken · 409 a component exists at <tile>+<name>
           · 409 a cap is reached · 409 the tile can't have deployments (chrome, an xbin:* capability; P19)
           · 409 a backend tile without --isolate (P18) · 409 stale seq
```

```
POST /api/xbin/deployments/remove
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "erase", "seq"?: 18}
200        {"state": State}
effect     Stops Y. Deletes its vault, logs, registrations and built artifacts, and its (scope, name) data
           namespace when this tile is the namespace's last claimant; otherwise the namespace stays with the
           siblings that claim it (08-data.md §9.2). Detaches live reload if Y held it; lastLiveReload then
           becomes the primary. Checkpoints stay until GC (08-data.md).
authority  terminal level on this tile. Removing touches no sibling's data, so it needs nothing on them.
errors     400 missing confirm · 403 · 404 · 409 Y is main · 409 Y is the primary · 409 stale seq
```

### 1.6 Moving code

```
POST /api/xbin/deployments/deploy
body       {"tile": "apps/crm", "deployment": "dev",
            "checkpoint"?: "c:<id>",      absent: a fresh checkpoint of the work tree
            "expect"?: "c:<id>",          only without checkpoint: the capture must equal it
            "restart"?: true,             restart X's current code; no checkpoint or expect with it
            "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}         how: "deploy", or "restart"
effect     Puts the checkpoint on X. X was the live reload target: live reload pauses (lastLiveReload = X).
           A checkpoint X already runs: a new generation from the kept artifact when X is failed,
           crash-looping or down, clearing its crash breaker (how "restart"); unchanged:true otherwise.
           restart:true always starts a new generation of X's current code (its checkpoint, or the work
           tree's current build while X follows it; live reload stays attached) and clears the breaker.
authority  terminal level. Onto the primary: parity. Onto a protected primary: a tile manager in a human
           session, with checkpoint and seq (§1.2). restart:true moves no code, so it is terminal level on
           every deployment, a protected primary included: it does what a crash restart does.
errors     400 checkpoint and expect together · 400 restart with checkpoint or expect · 400 onto a protected
           primary without checkpoint or seq · 403 · 403 protected primary · 404 · 409 expect mismatch
           · 409 ambiguous checkpoint id · 409 isolation (P18) · 409 deploy queue full · 409 no record
           · 409 stale seq
```

```
POST /api/xbin/deployments/promote
body       {"tile": "apps/crm", "from": "dev", "to": "main", "expect"?: "c:<id>", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}         how: "promote", from: "dev"
effect     B receives A's current code: A's checkpoint, or a fresh checkpoint of the work tree when A follows
           it. Code only (P10). B was the live reload target: live reload pauses.
authority  as deploy, for the target B; onto a protected primary, expect and seq are required
errors     400 onto a protected primary without expect or seq · 403 · 404 · 409 from = to · 409 expect
           mismatch · 409 no record · 409 stale seq
```

```
POST /api/xbin/deployments/rollback
body       {"tile": "apps/crm", "deployment": "main", "checkpoint"?: "c:<id>", "seq"?: 18}
           absent checkpoint: the newest "ok" entry in X's deploy log whose checkpoint differs from X's current
           one; onto a protected primary checkpoint is required
200        {"state": State, "deploy"?: DeployEntry}         how: "rollback"
effect     Deploys C to X. X was the live reload target: live reload pauses. Data stays (flow D).
authority  as deploy
errors     400 onto a protected primary without checkpoint or seq · 403 · 404 unknown checkpoint · 409 nothing
           earlier in the deploy log · 409 no record · 409 stale seq
```

### 1.7 Governance

Every route in this section is a tile manager's act (§0.5): a human session,
never a terminal, agent, frame or instance token.

```
POST /api/xbin/deployments/primary                          (M2)
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "data-stays", "expect"?: "c:<id>", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry, "inactiveHosts"?: ["crm.example.com"]}
effect     Routing moves (model §5, §7). Data does not move, and the root xbin.json is never written. Y starts
           as the primary first; then the old primary is restarted or stopped, its long-lived streams cut.
           The old primary's registrations become dormant and Y's activate; Y's dormant ingress hosts are
           re-validated, and any that conflict stay inactive and are listed in inactiveHosts. Sessions named
           after either deployment restart (§7.4). When Y follows the work tree and the primary is protected,
           Y is pinned to exactly expect first (how "reassign"). Limits follow the deployment: Y keeps its own
           (the limits route below).
authority  tile manager. While the primary is protected, expect and seq are required (§1.2).
idempotent yes: Y already primary answers 200 and changes nothing
errors     400 missing confirm · 400 protected without expect or seq · 403 · 404 · 409 Y isn't healthy
           · 409 the reassignment would split a scope's data (08-data.md §6.5) · 409 expect mismatch
           · 409 no record · 409 stale seq
```

```
POST /api/xbin/deployments/protect
body       {"tile": "apps/crm", "on": true, "expect"?: "c:<id>", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}
effect     on: while live reload is on the primary, the primary is pinned in place to a fresh checkpoint of
           the work tree (how "protect"), equal to expect when given; bx and the panel always send the dry
           run's impact.code.to. Sessions following the primary restart onto P24's default (§7.4). On a tile
           without a record this is an opt-in (§1.2). off: nothing moves; the primary stays pinned.
authority  tile manager
idempotent yes
errors     403 · 409 expect mismatch · 409 stale seq
```

```
POST /api/xbin/deployments/edge
body       {"tile": "apps/crm", "edge": "grant:apps/calendar",
            "policy": "read" | "block" | "inherit" | "default", "seq"?: 18}
           The values each edge takes are its Edge.values (09-fabric.md §5.1). "default" removes the override.
           match is not accepted in v1: it arrives with a feature string (NP-11-13).
200        {"state": State}
effect     Applies to every non-primary deployment of the tile at their next call. inherit never reaches host
           networking or a provider splice: on a tile whose net resolves to host sharing, the net slot's
           effective value is block whatever is stored, and Edge.why says so.
authority  tile manager
idempotent yes
errors     400 a value this edge doesn't take (for example read on a custom role with no path to reader,
           such as the role through which the agent tile reaches its sandbox managers) · 403 · 404 no such
           edge on the tile · 409 no record
```

```
POST /api/xbin/deployments/deliveries
POST /api/xbin/deployments/always-on
body       {"tile": "apps/crm", "deployment": "dev", "on": true, "seq"?: 18}
200        {"state": State}
effect     deliveries: Y's dormant cron jobs and bus push subscriptions become active for Y (interface
           instances and ingress hosts never do). always-on: honours D84 for Y.
authority  tile manager
idempotent yes
errors     403 · 404 · 409 Y is the primary (its registrations are always active)
           · 409 (always-on) Y's code doesn't declare "alwaysOn"
```

```
POST /api/xbin/deployments/limits
body       {"tile": "apps/crm", "deployment": "dev",
            "limits": {"memMiB"?: 1024 | null, "pids"?: 512 | null, "diskGiB"?: 20 | null}, "seq"?: 18}
           null removes an override; an absent key is left as it is
200        {"state": State}
effect     P22: each deployment's resource limits default to the tile's ceilings, which are today's
           per-component caps: memory XBIN_LIMIT_MEM (internal/boot/boot.go:567, default 2 GiB), pids
           max(512, 8 × CPUs) (boot.go:570), disk XBIN_LIMIT_DISK per scope (internal/boot/config.go:63,
           default 50 GiB). An override lowers one for this deployment. memMiB and pids apply to its backend
           leaf from its next generation (07-runtime.md §10.3); diskGiB is the quota of its (scope, name)
           namespace (08-data.md §12), from the next measurement.
authority  tile manager
errors     400 a value above the tile's ceiling, or not a positive integer · 403 · 404 · 409 diskGiB on a
           tile that doesn't root its scope (the namespace's quota is set on the scope's root tile)
           · 409 no record · 409 stale seq
```

### 1.8 Data

```
POST /api/xbin/deployments/seed
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "copy-data", "stop"?: true, "seq"?: 18}
POST /api/xbin/deployments/reset
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "erase-data", "vault"?: true, "seq"?: 18}
200        {"state": State}          Y.data.busy is "seeding" or "resetting" until done; completion
                                      is a deployments op:"data" event (§3.3)
effect     seed: Y's namespace (S, Y) is filled from the primary's, consistently (08-data.md §8); stop:true
           takes the stopped, point-in-time mode (08-data.md §8.4), which some resources require anyway.
           data.state becomes "seeded". reset: (S, Y) is emptied, data.state becomes "empty" with
           reset: true; vault:true also empties Y's vault, so every key is a placeholder again. Both stop
           every deployment that addresses (S, Y), sibling tiles' included.
authority  seed: tile manager. reset: terminal level, except resetting main, which is a tile manager's act.
           Either needs its level on EVERY claimant tile of (S, Y) (08-data.md §6.3); the 403 names the
           first tile that blocks it.
errors     400 missing confirm · 403 · 404 · 409 Y is the primary · 409 data busy · 409 no record
           · 409 stale seq
```

```
POST /api/xbin/deployments/vault-copy
body       {"tile": "apps/crm", "deployment": "dev", "keys"?: ["STRIPE_KEY"], "all"?: true, "seq"?: 18}
           exactly one of keys and all
200        {"state": State, "copied": ["STRIPE_KEY"], "missing": []}
effect     Copies the values from the primary's vault into Y's, overwriting the named keys, never toward the
           primary (08-data.md §10). The audit line names keys, never values.
authority  tile manager
idempotent yes
errors     400 neither or both of keys and all · 403 · 404 · 409 Y is the primary · the vault routes' answer
           while the vault is sealed
```

Per-deployment backups are new routes beside today's, whose bodies stay as
they are (§0.6). They use the tile's bound `@archive` provider and the archive
keys and manifest of [08-data.md](08-data.md) §11. Every one echoes
`deployment` in its answer.

```
POST /api/xbin/deployments/backup
body       {"tile": "apps/crm", "deployment": "dev"}
200        {"ok": "true", "deployment": "dev", "version": "…"}      today's /backup shape plus the echo
effect     Archives (S, Y)'s data now, under Y's archive key (08-data.md §11.3). Opt-in for non-primary data;
           a primary that isn't main is already archived by every backup of the tile (08-data.md §11.1).

GET  /api/xbin/deployments/backups?tile=<tile>&deployment=<name>
200        {"deployment": "dev", "versions": [{"version", "time", "size"}], "archiver": "…"}   empty versions
           without an archiver, as today's /backups

POST /api/xbin/deployments/restore
body       {"tile": "apps/crm", "deployment": "dev", "version"?: "…", "into"?: "dev", "replace"?: true,
            "confirm"?: "erase-data"}
           deployment: whose archive (main: the tile's main archive, data part only); version: default latest;
           into: the target deployment, default the archive's own; replace: meaningful only when the target
           is main (08-data.md §11.4): any other target is always replaced; confirm: required when the
           target has data
200        {"ok": "true", "deployment": "dev", "into": "dev", "restored": "…", "skipped": []}
effect     Restores data only, never the work tree (that is POST /restore). Stops every deployment that
           addresses the target namespace, sibling tiles' included; data.state becomes "restored".

POST /api/xbin/deployments/backup-schedule
body       {"tile": "apps/crm", "deployment": "dev", "schedule": "0 3 * * *" | "", "retention"?: 3}
           schedule "" removes the schedule; retention default 3 (08-data.md §11.3)
200        {"state": State}

authority  admin, in a human session (today's backup routes are admin-only, internal/broker/backup.go:516-553).
           restore also needs the reset level on every claimant tile of the target (S, into).
errors     400 missing confirm · 403 · 404 no such deployment, or no archive · 409 the target deployment
           doesn't exist (the answer lists the choices) · 409 data busy · 409 no record · 502 no archiver
           bound, or the archiver failed (today's text, internal/broker/backup.go:399, :415)
```

### 1.9 Run now

```
POST /api/xbin/deployments/run-now
body       {"tile": "apps/crm", "deployment": "dev", "job": "nightly"}
200        {"state": State, "delivery": {"status": 200, "ms": 812}}   status: what the handler answered
effect     Delivers Y's cron job once to Y, as xbin/cron with the job's role, dormant or not, through the
           same dispatch a tick uses. Waits for the handler, at most 2 minutes. One run in flight per job.
authority  terminal level
errors     403 · 404 no such deployment or job · 409 Y is the primary (its jobs fire on schedule)
           · 409 a run of this job is in flight · 502 Y's backend can't start (the proxy's shape, with detail)
```

### 1.10 The deploy log

```
GET /api/xbin/deployments/log?tile=<tile-ref>&deployment=<name>&limit=<n>&before=<id>
200        {"tile": "apps/crm", "entries": [DeployEntry], "more": true}
           Newest first. Without deployment: every deployment's entries the caller may see, by id.
           limit: default 50, max 200.
GET /api/xbin/deployments/log?tile=<tile-ref>&id=<id>&wait=<seconds>
200        {"entry": DeployEntry}
           One attempt: queued and in-flight ones from memory, finished ones from the store. wait (at most 25)
           holds the answer until result leaves queued/running, or the wait runs out.
authority  T's write audience. D's own principals see D's entries only. Readers, and the primary's frame
           and instance principals, are refused.
errors     403 · 404 no such tile, deployment or id · 409 no record
```

The log holds every finished attempt, failed ones included. It is kept in the
checkpoint store (§10.3). Queued and in-flight entries are journaled
(§10.1's deploy journal) and logged at the next boot after a restart:
queued ones as `cancelled`, swapped ones as `ok`, running ones as `failed`
with the error `interrupted: xbind restarted mid-deploy; <name> runs
<code>`. The deployment keeps the code its record names (Q3).

### 1.11 Diff

```
GET /api/xbin/deployments/diff?tile=<tile-ref>&from=<spec>&to=<spec>&path=<file>&stat=1
spec       c:<id> | deployment:<name> | work-tree
           defaults: from = deployment:<primary>, to = work-tree. A deployment that follows the work tree
           means work-tree. work-tree captures a checkpoint into the store (content-addressed, collectable).
200        text/x-diff: a git patch (renames detected, binary files named, not inlined)
           X-XBin-Checkpoint-From: c:<id>     the resolved checkpoints (for work-tree: the capture)
           X-XBin-Checkpoint-To:   c:<id>
           X-Truncated: true                  the patch was cut at 16 MiB
200 stat=1 {"from": "c:…", "to": "c:…", "files": [{"path", "status": "A|M|D|R|T", "added", "removed",
            "binary"?}], "truncated": false}    at most 5000 files
authority  T's write audience; a work-tree side, which captures, needs terminal level. Frame and instance
           principals are refused.
errors     400 bad spec, or path not a clean tile-relative path · 403 · 404 · 409 no record · 429 a diff is
           running for this tile and another is waiting · 504 took longer than 30 s (narrow it with path
           or stat)
```

- A tile without a record has no checkpoints and runs its work tree, so it
  has nothing to diff: the answer is 409 (§1.14), and nothing is captured and
  no store is created (05-model §5).
- Every diff and capture runs in confine (D78, P16). One diff runs per tile,
  with one more waiting, and two run across xbind; a request past that answers
  429. That follows `GET /term/sessions/{id}/diff`
  (`internal/server/openapi.go:145`), bounded so that no caller holds the
  slots other tiles' confirmations need.

### 1.12 The checkpoint remote

```
GET /api/xbin/checkpoints/<tile>.git/<git path>          route pattern: GET /checkpoints/{rest...}
git path   HEAD | info/refs | objects/info/packs | objects/pack/pack-<hex>.(pack|idx) | objects/<2 hex>/<38–62 hex>
200        the file, served from the tile's view repository, data/checkpoints/<TileKey>.view.git (§10.3);
           everything else 404
authority  the tile's own terminal and agent tokens, while their driving user holds at least write on the
           tile; humans with at least write on the tile (admins included), at their current level. Frame and
           instance principals and code: grants are refused: dumb HTTP can't filter objects per caller.
errors     403 · 404 no such tile, a tile without a record, or a path outside the allow-list
```

- The route is **read-only dumb HTTP**, like the template remote
  (`internal/broker/templates.go:30`). Nothing is ever pushed here, and
  `?service=` is ignored, so git falls back to the dumb protocol.
- **The view repository, never the store** (05-model §3). It holds only
  `refs/heads/deploy/<name>` for each pinned deployment, pointing at the
  **git view** of the checkpoint it runs, and `HEAD` naming
  `refs/heads/deploy/<primary>` (dangling while the primary follows the work
  tree). It holds no other object, so no `refs/xbin/*` ref, no deploy-log
  entry and no ignored file is ever fetchable. The layout is §10.3's.
- **Serving:** each file of the allow-list is opened beneath the view
  repository with `fsutil.OpenBeneath` (`internal/fsutil/beneath.go:34`),
  refusing every symlink. It is never passed to `http.ServeFile`, which the
  template remote uses (`internal/broker/templaterepo.go:214-227`).
- **Splitting `<tile>.git`:** the handler takes the last segment ending in
  `.git` whose prefix is a registered component with a record. A tile path may
  itself contain `.git`-suffixed segments.
- **Terminal injection:** sessions opened while the tile has a record get
  two more `GIT_CONFIG_*` pairs after today's two (`internal/term/term.go:787-793`).
  `GIT_CONFIG_COUNT` becomes 4:

  ```
  GIT_CONFIG_KEY_2=remote.xbin-deploy.url     GIT_CONFIG_VALUE_2=http://xbin/api/xbin/checkpoints/<tile>.git
  GIT_CONFIG_KEY_3=remote.xbin-deploy.fetch   GIT_CONFIG_VALUE_3=+refs/heads/deploy/*:refs/deploy/*
  ```

  The existing `insteadOf` rewrite and bearer header carry the fetch. After
  `git fetch xbin-deploy`, `deploy/<name>` resolves in the tile's repository,
  so flow H's `git checkout -b hotfix deploy/main` works verbatim. Nothing is
  ever written into the tile's `.git/config`. Zero-state sessions keep today's
  two pairs.
- **Sessions opened before the record existed** (the usual M1 path: pausing
  live reload from a terminal window that already has a shell or an agent
  open) lack the two pairs, since a session's env is fixed at spawn.
  `bx deployment fetch [<tile>]` (§9.2) works in any session: it runs
  `git fetch http://xbin/api/xbin/checkpoints/<tile>.git '+refs/heads/deploy/*:refs/deploy/*'`,
  which today's two pairs already rewrite and authorize, and writes no
  configuration.

### 1.13 Every operation, one route, one command

| Operation (model §5) | Route | `bx` |
|---|---|---|
| Pause live reload | `POST /deployments/live-reload/pause` | `bx live-reload pause [<tile>]` |
| Resume live reload | `POST /deployments/live-reload/resume` | `bx live-reload resume [<tile>] [--to <name>]` |
| Reload now | `POST /deployments/live-reload/now` | `bx live-reload now [<tile>]` |
| Attach live reload to Y | `POST /deployments/live-reload/attach` | `bx live-reload attach [<tile>] --to <name>` |
| Add deployment Y | `POST /deployments/add` | `bx deployment add [<tile>] <name>` |
| Deploy to X | `POST /deployments/deploy` | `bx deploy [<tile>] --to <name>` |
| Restart X's current code | `POST /deployments/deploy` with `restart` | `bx deployment restart [<tile>] <name>` |
| Promote A → B | `POST /deployments/promote` | `bx promote [<tile>] <a> <b>` |
| Roll back X to C | `POST /deployments/rollback` | `bx rollback [<tile>] --to <name> [--checkpoint c:<id>]` |
| Remove deployment Y | `POST /deployments/remove` | `bx deployment rm [<tile>] <name>` |
| Reassign primary to Y (M2) | `POST /deployments/primary` | `bx deployment primary [<tile>] --to <name>` |
| Protect / unprotect primary | `POST /deployments/protect` | `bx deployment protect [<tile>] on\|off` |
| Seed Y | `POST /deployments/seed` | `bx deployment seed [<tile>] <name>` |
| Reset Y | `POST /deployments/reset` | `bx deployment reset [<tile>] <name>` |
| Vault copy to Y | `POST /deployments/vault-copy` | `bx deployment vault-copy [<tile>] <name> --keys <k1,k2>\|--all` |
| Deliveries on/off for Y | `POST /deployments/deliveries` | `bx deployment set [<tile>] <name> --deliveries on\|off` |
| alwaysOn on/off for Y | `POST /deployments/always-on` | `bx deployment set [<tile>] <name> --always-on on\|off` |
| Resource limits of Y | `POST /deployments/limits` | `bx deployment set [<tile>] <name> --mem\|--pids\|--disk <n>\|default` |
| Set edge policy | `POST /deployments/edge` | `bx deployment edge [<tile>] <edge> read\|block\|inherit\|default` |
| Run now | `POST /deployments/run-now` | `bx deployment run-now [<tile>] <name> <job>` |
| Back up Y's data | `POST /deployments/backup` | `bx deployment backup [<tile>] <name>` |
| (read) Y's archives | `GET /deployments/backups` | `bx deployment backups [<tile>] <name>` |
| Restore Y's data | `POST /deployments/restore` | `bx deployment restore [<tile>] <name>` |
| Y's backup schedule | `POST /deployments/backup-schedule` | `bx deployment backup-schedule [<tile>] <name>` |
| (read) state | `GET /deployments` | `bx deployment ls [<tile>]` |
| (read) deploy log | `GET /deployments/log` | `bx deployment log [<tile>] [<name>]` |
| (read) diff | `GET /deployments/diff` | `bx deployment diff [<tile>] [<from> [<to>]]` |
| (read) checkpoints over git | `GET /checkpoints/{rest...}` | `git fetch xbin-deploy`, or `bx deployment fetch [<tile>]` |

The routes are listed without the `/api/xbin` prefix, as protocol.md's API
fence writes them.

### 1.14 Error catalogue

Implementations use these texts. They may append detail after ` — `. `docs`
is `/docs/auth.md` for 403 and `/docs/protocol.md` otherwise, as the proxy
does (`internal/proxy/proxy.go:304-317`).

| Condition | Status | `error` |
|---|---|---|
| unreadable body, unknown field | 400 | `bad request body: <decoder error>` |
| bad deployment name | 400 | `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters` |
| missing `confirm` | 400 | `<consequence>: send confirm:"<token>" to proceed` |
| protected primary, code not named | 400 | `the primary of <tile> is protected: name the checkpoint you reviewed (send <checkpoint\|expect> and seq)` |
| `restart` with a checkpoint | 400 | `restart runs <name>'s current code: send no checkpoint or expect with it` |
| edge value | 400 | `<edge> takes <values>: <reason>` (for example `a custom role with no path to reader can't be read-clamped`) |
| limit above ceiling | 400 | `<limit> can't exceed the tile's ceiling (<n>)` |
| unknown tile | 404 | `no such tile: <tile>` |
| unknown deployment | 404 | `<tile> has no deployment "<name>"` |
| unknown checkpoint | 404 | `<tile> has no checkpoint <id>` |
| unknown edge | 404 | `<tile> has no edge <id>` |
| needs write | 403 | `deployments of <tile> need write access` |
| deployment URL for a reader | 403 | `deployment URLs need write access on <tile>` |
| needs terminal level | 403 | `<operation> needs terminal-level access on <tile>` |
| needs a tile manager | 403 | `<operation> is a tile manager's act: the tile's owner, its org's admins, or a workspace admin` |
| manager act from a tile credential | 403 | `<operation> is a tile manager's act, done in a person's own session: terminal, agent and tile credentials can't do it` |
| needs an admin | 403 | `<operation> is a workspace admin's act, done in a person's own session` |
| protected primary | 403 | `the primary of <tile> (<name>) is protected: only tile managers change its code, and not from a terminal or agent session` |
| session target protected | 403 | `the primary of <tile> is protected: terminal and agent sessions can't target it` |
| a claimant blocks | 403 | `<operation> of <scope>'s "<name>" data needs <level> on <tile> too` |
| joining seeded data | 403 | `<scope>'s "<name>" data was <seeded\|restored> by <by> <when>: joining it is a tile manager's act` |
| cross-deployment self write | 403 | `a tile's own credentials act only on their own deployment (<bound>)` |
| unclassified route | 403 | `this route isn't available to a non-primary deployment's credentials yet (<deployment>)` |
| no record | 409 | `<tile> has no deployments yet: pause live reload or add a deployment first` |
| diff without a record | 409 | `<tile> has no deployments: its work tree is what runs, so there is nothing to diff` |
| name taken, `main` | 409 | `<tile> already has a deployment "<name>"` |
| component at `<tile>+<name>` | 409 | `a tile exists at <tile>+<name>; pick another name` |
| creating a tile at a deployment URL | 403 | `can't create <P>+<N>: <P> has a deployment "<N>", and that is its URL — pick another path` |
| cap | 409 | `<tile> has <n> non-primary deployments, the most allowed here` |
| tile can't have deployments | 409 | `<tile> can't have non-primary deployments: <reason>` |
| isolation | 409 | `pinning a backend to a checkpoint needs isolation (--isolate)` |
| not paused | 409 | `live reload is attached to <name>; <operation> works while it is paused` |
| paused | 409 | `live reload is paused: resume it onto <name> instead` |
| primary or main operand | 409 | `<name> is the primary of <tile>` / `main can't be removed` |
| unhealthy | 409 | `<name> isn't healthy (<state>); only a healthy deployment can become the primary` |
| scope split | 409 | `reassigning the primary of <tile> would split <scope>'s data: only a tile alone in its scope, or in the workspace scope, can change its primary in this release` |
| stale `seq` | 409 | `the deployments of <tile> changed (seq <n>); reload and retry` |
| `expect` mismatch | 409 | `the code changed since you reviewed <expect> (now <actual>)` |
| nothing to roll back to | 409 | `<name> has no earlier checkpoint in its deploy log` |
| ambiguous id | 409 | `checkpoint id <id> is ambiguous in <tile>; use more digits` |
| data busy | 409 | `<name>'s data is being <seeded\|reset\|restored>` |
| deploy queue full | 409 | `<name> already has 8 deploys waiting; try again when one finishes` |
| run in flight | 409 | `<job> is already running on <name>` |
| disk limit off the scope root | 409 | `the quota of <scope>'s "<name>" data is set on <root tile>` |
| newer record | 409 | `<tile>'s deployment record was written by a newer xbind (schema <n>)` |
| xbin grant with deployments | 409 | `<tile> has non-primary deployments: remove them before granting it <target> (P19)` |
| diff queue | 429 | `<tile> already has a diff running and one waiting; retry shortly` |

### 1.15 Registration obligations

Every route above is four edits in one commit:

1. A `RegisterAPI("METHOD /path")` literal where the fixture mounts it. That is
   `internal/boot/<x>.go`, read by `mainRoutes`
   (`internal/apicheck/apicheck_test.go:153`), or a plane registered from
   `broker.Register`, mounted by `mount` (`apicheck_test.go:122`).
   `TestRouteInventory` (`apicheck_test.go:244`) enforces 1 to 3.
2. An `ep` row in `endpoints()` (`internal/server/openapi.go:19-29`, `:83`)
   with these capability strings:
   - `read on the tile, or the tile itself (readers get the primary only)` for
     `GET /deployments`;
   - `write on the tile, or its terminal/agent sessions` for the log and the
     diff;
   - `terminal-level on the tile (its own terminal/agent tokens count)` for the
     live-reload, add, remove, deploy, promote, rollback, reset and run-now
     routes;
   - `tile manager in a person's own session (the tile's owner, its org's
     admins, a workspace admin)` for primary, protect, edge, deliveries,
     always-on, limits, seed and vault-copy;
   - `admin in a person's own session` for backup, backups, restore and
     backup-schedule;
   - `the tile's terminal/agent sessions, or write on the tile` for the
     checkpoint remote.

   The capability list in `apiInfo` (`openapi.go:66-79`) gains the new
   phrases.
3. A column-0 row inside the API fence of `docs/protocol.md` (`:423`).
   Optional queries are written `?x=<y>`, never `[?x]`.
4. A row in the route-class table (`internal/server/deployclass.go`), kept
   complete by `TestDeploymentRouteClasses` ([09-fabric.md](09-fabric.md) §6,
   P26).

## 2. Qualified URL routing

### 2.1 Grammar and the `+` reservation

The deployment URL is `/c/<tile>+<name>/…` and `/api/<tile>+<name>/…`
(glossary). The qualifier sits **inside the last segment of the tile path**,
never in a segment of its own. A separate segment would read as a tile
sub-path, a nested tile or an `xbin.window` path, and would trip prefix-based
frame reloads ([research/terminology-census.md](research/terminology-census.md)).

Two narrow refusals keep the URL unambiguous, for **every** creator, admins
and `xbin:writer` elements included:
- **No tile at `<P>+<N>` while `P` has deployment `N`.** Every tile creation
  path (create, template instantiation, import, clone, builtin import)
  goes through `canCreateAt`, which checks it before its admin early return
  (`internal/broker/policy.go:106-108`), beside the `:` refusal of
  `newTilePathOK` (`internal/broker/policy.go:162-166`). The refusal is the
  creation routes' usual 403 (`internal/broker/create.go:54-56`), with
  §1.14's text naming the deployment.
- **No deployment `N` on `P` while a component exists at `<P>+<N>`:** `add`
  answers 409 (§1.5).

Any other new tile name containing `+` is created as today, and its answer
gains a `warnings` entry for one release, the D82 way:
`"+" in tile names is reserved for deployment URLs (/c/<tile>+<name>/); a tile
named <path> may be hard to tell from one`. It is never refused. A directory
created outside the API at `<P>+<N>` wins resolution (§2.2) and shadows the
deployment URL; the deployments panel reports the clash
([12-compat.md](12-compat.md) §7.3).

### 2.2 Resolution

One resolver serves `/c/`, `/api/`, the asset-token plane, tile origins and the
`/deployments` routes' `tile` field. It runs before today's per-plane logic:
before `owningComponent` in the static handler (`internal/server/static.go:105`)
and in place of `Reg.Resolve` in the proxy (`internal/proxy/proxy.go:130`).
A qualified URL resolves only for tiles with a record, and only when today's
resolution doesn't already answer it.

```go
// ResolveRef maps a cleaned path (util.SafeJoin already applied) or a tile ref to
// (component, deployment, rest). qualified reports that the path named a deployment.
func ResolveRef(p string) (c *Component, dep string, qualified bool, rest string, err error) {
	base, baseRest, baseOK := Reg.Resolve(p) // today's longest-prefix answer (registry.go:519-536)
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		j := strings.LastIndexByte(segs[i], '+')
		if j < 1 || !NameOK(segs[i][j+1:]) {
			continue
		}
		tile := path.Join(append(slices.Clone(segs[:i]), segs[i][:j])...)
		t, ok := Reg.Component(tile)
		if !ok || !HasRecord(tile) {
			continue // a zero-state tile: '+' means nothing (P5)
		}
		if baseOK && depth(base.Path) >= i+1 || PathExists(path.Join(segs[:i+1]...)) {
			break // today's answer wins: a component at least as deep, or anything on disk at the
			      // full candidate "<tile>+<name>" (the D82 way; 05-model §7). Lstat only.
		}
		name := segs[i][j+1:]
		if !HasDeployment(tile, name) {
			return nil, "", true, "", ErrNoDeployment // 404 `<tile> has no deployment "<name>"` (§2.3)
		}
		return t, name, true, path.Join(segs[i+1:]...), nil
	}
	if !baseOK {
		return nil, "", false, "", ErrNoComponent
	}
	return base, Primary(base.Path), false, baseRest, nil
}
```

- `<tile>+<primary>` resolves to the primary with `qualified` set: the
  **alias**. It serves what the bare URL serves, with identical authorization
  for humans and the tile's own principals. Other tiles' principals get 403
  on the alias (§2.3), so no tile hard-codes a name a reassignment would
  break.
- The pinned or live reload root that `dep` names comes from the record.
  Opening files beneath it is [07-runtime.md](07-runtime.md)'s job. `pathAllowed`
  (`internal/server/static.go:347-361`) still applies to `rest`.
- An unknown name on a tile with a record answers 404 to callers who may know
  the tile's deployments, instead of falling back to a shorter prefix that
  would serve `crm+dev/…` as a file of a parent tile. Everyone else gets the
  403 of §2.3, whether or not the name exists.
- **Symlinks inside a checkpoint** are never followed on disk. A
  `deps/<name>/…` path is served by re-dispatching it to the target tile's
  `/c/` plane, which serves that tile's primary; any other symlink that leaves
  the checkpoint answers 404 ([07-runtime.md](07-runtime.md) §4.1).
- Every URL that resolves today resolves identically: a zero-state tile never
  splits, and a component or on-disk path at the full candidate always wins.
  A `+` in a query string is never the qualifier.

### 2.3 Who may use which URL

| Caller | bare `/c/<T>/…` | `/c/<T>+<N>/…`, N non-primary |
|---|---|---|
| humans with read | the primary, as today | 403 `deployment URLs need write access on <T>`, whether or not N exists |
| humans with write or terminal, admins | the primary | N |
| T's own principals bound to N | the primary's files; a document's frame token is not minted (§7.2) | N, with N's frame token |
| T's own principals bound elsewhere (the primary's included) | the primary; the frame token only when bound to the primary | documents: 403; in legacy mode, subresources as the tile's code (§2.6) |
| other tiles' principals | as today | 403 |
| credential-less legacy subresource load | as today | N, by the legacy rule (§2.6) |

| Caller | bare `/api/<T>/…` | `/api/<T>+<primary>/…` | `/api/<T>+<N>/…`, N non-primary |
|---|---|---|---|
| humans | the primary (policy as today) | as bare | write or terminal: N; read: 403 |
| the tile's own principal bound to B | B, a self-call (P12) | B = primary: the primary; else 403 | B = N: N; else 403 |
| other tiles' principals | the primary; the caller's edge policy applies on the caller side (09-fabric.md) | 403 | 403 |
| cron and bus deliveries | the delivering registration's deployment | — | — |
| ingress | the primary (unchanged) | — | — |

- Bare `/c/` always means the primary, whoever asks. Only `/api/` has the
  self-call rule.
- A terminal or agent token is never bound to a protected primary (P24,
  §7.4). A token that follows the primary gets 403 on every self-call from the
  moment the primary is protected until its session restarts (§1.14's
  session-target text).
- A non-primary document's absolute `/c/<self>/…` URLs in markup load the
  primary's files. Relative URLs, the documented portable form, stay inside
  the deployment. Module imports are remapped (§2.6).
- **A non-primary deployment's documents are never chrome**, whatever its
  code declares: bx-frame sandboxes them, and xbind serves them with a
  non-chrome tile's CSP sandbox. Whether the bare URL is chrome follows the
  primary's code (§2.8).
- The call's role and headers are the bare URL's: the qualified form adds only
  the gates above. Lifecycle is the tile's: 409 with `X-XBin-Lifecycle`
  exactly as today (`internal/proxy/proxy.go:148-159`).

### 2.4 Nested tiles

- A qualified URL never enters a nested component. If `<T>/<rest>` resolves to
  a registered component deeper than T, the answer is 404
  `<path> is a tile of its own; its deployments are /c/<path>+<name>/`.
  That matches checkpoint contents, which exclude nested components.
- A nested tile's own deployments carry the qualifier on their own last
  segment: `/c/apps/crm/widgets+dev/`.
- The parent's deployment never speaks for the child.

### 2.5 `xbin.window` sub-paths and `<bx-frame src>`

- A deployment frame is `<bx-frame src="<tile>+<name>">`, and its iframe loads
  `/c/<tile>+<name>/`. With the qualified path as `src`, `xbin.window` spawns
  work unchanged: bx-frame dispatches `from: this.src`
  (`web/bx-frame.js:502-507`), and even an old scaffold shell builds
  `from + '/' + spec.path` (`workspace-template/shell/bx-shell.js:677`), so
  the window resolves inside the deployment.
- **Reloads and build overlays** of a deployment frame come from the
  `deployments` event, because no event of today's types ever names a
  non-primary deployment (§3). bx-frame, which the binary serves, reloads a
  frame whose `src` is `<T>+<N>` on op `reload` with `deployment: N`, and sets
  or clears its build overlay on op `build` ([10-ux.md](10-ux.md) §6.1).
  Frames of the primary keep today's handling of `reload` and `build-*`
  (`web/bx-frame.js:366-395`, `isReloadTarget` in
  `web/events-socket.js:40-49`). An ancestor frame therefore never reloads
  for a nested tile's non-primary deployment.
- Tile-level events (`grants`, `pr`, `term`, `deployments`) keep the bare
  component. A client showing a deployment frame compares the tile part of its
  `src`.
- `frame-info.js` resolves a qualified `src` the way §2.2 does: an exact
  component in `/components` wins, else split the last `+`. Today its
  `infoFor` matches by prefix only (`web/frame-info.js:55-62`). It mints
  bootstrap tokens with `?component=<tile>&deployment=<name>` (§8).

### 2.6 Asset modes

- **legacy** (the default):
  - The credential-less subresource rule (`internal/server/static.go:286-315`)
    authorizes by URL path, so it covers `/c/<T>+<N>/…` subresources exactly as
    it covers the tile's. This is legacy mode's known looseness: it serves
    tile code, never documents.
  - Non-primary documents get two additions inside the D4 injection:
    `<meta name="xbin-deployment" content="<N>">`, and one import-map entry
    `"/c/<T>/": "/c/<T>+<N>/"`.
  - Primary documents stay byte-identical: `TestLegacyInjectionUnchanged`
    (`internal/server/tileassets_test.go:187`) keeps pinning them.
- **tokens:**
  - The document's `<base>` comes from the request path (`tokenBase`,
    `internal/server/tileassets.go:378`), so it is
    `/c/~<tok>/<T>+<N>/<dir>/` with no new code.
  - `assetHead` (`tileassets.go:338`) remaps `/c/<T>/` to `/c/~<tok>/<T>+<N>/`.
  - `serveAssetToken` (`tileassets.go:275`) resolves the path after the token
    with §2.2. For a non-primary N it checks, live, that the token's user
    **writes** T.
  - The asset token itself is unchanged: the path carries the deployment.
- **origins** (NP-11-18):
  - The label of `main` is today's `TileHostID(T)`
    (`internal/auth/assettoken.go:182-188`), unchanged. Any other deployment's
    label is:

    ```
    "t-" + lowercase(base32-nopad(HMAC-SHA256(.xbin/secret, "xbin-tile-host-v1" ‖ 0x00 ‖ T ‖ 0x00 ‖ N)[:10]))
    ```

    A tile path never contains NUL, so no label collides, and the format is
    the same 18 characters that `tileHostOf` accepts.
  - Storage follows the deployment: each deployment has its own localStorage
    and IndexedDB.
  - Every `TileHostID(x) == id` comparison
    (`internal/server/tileorigin.go:163`, `:296`, `:309`, `:375`, `:411`;
    `internal/server/tilenav.go:217`) becomes a label → (T, N) lookup, built
    from the records.
  - On the origin of (T, N), both `/c/<T>/…` and `/c/<T>+<N>/…` serve N, and
    `/api/<T>/…` acts as N's tile principal. A path naming another deployment
    of T is 302'd to the workspace, like another tile's page.
  - A workspace navigation to bare `/c/<T>/` goes to the **primary's** origin.
    A navigation to `/c/<T>+<N>/` goes to N's origin.
  - `/components` `origin` is the primary's origin. `GET /deployments` names
    each deployment's origin to the callers who see that deployment.

### 2.7 Native runtime document

- `/c/<T>/?native=1` is generated from the **primary's** code: the work tree
  while the primary follows it, its checkpoint while pinned. It always agrees
  with `/components`' `native` (§8).
- `/c/<T>+<N>/?native=1` is generated from N's code: N's manifest `native`
  entry and files. It is served to the same principals as N's documents.
- The shipped app only ever requests bare URLs (`/c/<T>/`,
  `/c/<T>/?native=1`), so it always gets the primary (compat rule 10).

### 2.8 The inbound surface follows the primary's code

`template`, `exposes`, `expose.roles`, `provides` and `chrome` describe what
the tile exposes and how it is framed, so they come from the primary's code:
the work tree while the primary follows it, its checkpoint while pinned
(05-model §6, P9; the readers are [07-runtime.md](07-runtime.md) §5.1's).
Observable consequences on the wire:
- the bare `/api/<T>/` answers its "is a template" 404
  (`internal/proxy/proxy.go:135-139`) from the primary's code;
- whether bare `/c/<T>/` documents are chrome, and so unsandboxed, follows the
  primary's code; a non-primary document never is (§2.3);
- ingress routes and interface bindings to the tile follow the primary's
  `exposes` and `provides`, and a caller's role is checked against the
  primary's `expose.roles`.

A save while live reload is paused, or while it is attached to a non-primary
deployment, changes none of these. `/components` keeps reporting `roles` from
the work tree (§8).

## 3. Events

### 3.1 The primary: today's types, unchanged

Every event about the primary keeps today's type, shape and bare `component`
(`internal/events/events.go:11-17`, `docs/protocol.md:2259-2313`). That holds
whether the primary is `main` or not.
- `build-*` keep today's meaning, for the primary only: builds of the work
  tree while the primary follows it, and restarts of the code it already runs.
- A deploy that puts a checkpoint on the primary emits no `build-*`: its
  phases and result ride `deployments` op `deploy` (§3.3). A failed one paints
  no frame overlay; the primary keeps serving its previous code.
- A successful swap on the primary emits one `reload` for the bare component,
  only when the primary's code changed, and the status plane clears the
  primary's reported status at that swap, as the build-start watcher does for
  a rebuild today (`internal/obs/status.go:129-144`).

### 3.2 Non-primary activity never uses today's types

No `reload`, `build-start`, `build-error`, `build-ok`, `status` or notify
event ever speaks of a non-primary deployment, and no `component` field on any
event ever carries a qualified string: not for deploys, not for a primary
reassignment, not for lifecycle changes. A qualified `component` would not
protect old clients:
- ancestor frames and the shipped app match `component` exactly or as a prefix
  followed by `/` (`web/bx-frame.js:366-395`, `web/events-socket.js:40-49`,
  and the app's reload targeting, D99), so `apps/shop/admin+dev` would reload
  an open `apps/shop` frame, even one of a zero-state tile;
- the old shell refetches `/components` on any `reload`
  (`workspace-template/shell/bx-shell.js:194-196`);
- the old shell stores, toasts and title-marks a `status` for any component
  (`workspace-template/shell/bx-shell.js:1257-1266`, `:1278-1283`).

Everything about a non-primary deployment travels in the `deployments` type
(§3.3). The status plane keeps non-primary statuses per deployment and never
publishes a `status` event for them; `GET /tile-report` never lists them.

The one existing type that gains a field is `bus`, whose events of a
non-`main` namespace carry the namespace's deployment (the name rule, §0.2):

```jsonc
{"type":"bus","topic":"res:apps/crm/events/orders","deployment":"dev","data":…}   // (scope, dev)'s bus (08-data.md §4.3)
```

`events.Event` gains ``Deployment string `json:"deployment,omitempty"` ``,
set only on such `bus` events.

### 3.3 The `deployments` type

A tile-level type. `component` is always the bare tile path, and deployments
are named inside `data`. Old clients ignore unknown types: bx-frame's switch
has no default, and the app maps unknown types to `.other` (compat rule 10).

```jsonc
// full forms, for the audiences of §3.4
{"type":"deployments","component":"apps/crm","data":{"op":"record","seq":19,"by":"user:ana","session":"<id>",
  "what":["liveReload","primary","protectedPrimary","edges","deployments","deliveries","alwaysOn","limits"]}}  // what changed
{"type":"deployments","component":"apps/crm","data":{"op":"deploy","id":43,"deployment":"main","how":"promote",
  "from":"dev","checkpoint":"c:3f2a1c9","result":"running","phase":"build","by":"user:ana","session":"<id>"}}
                                                                                // each result or phase change
{"type":"deployments","component":"apps/crm","data":{"op":"reload","deployment":"dev"}}   // a non-primary deployment's
                                                                                // frames reload once
{"type":"deployments","component":"apps/crm","data":{"op":"build","deployment":"dev","phase":"error",
  "text":"compiler output"}}                                                    // phase: start | error | ok; a
                                                                                // non-primary build of the work tree
                                                                                // or restart of current code
{"type":"deployments","component":"apps/crm","data":{"op":"work-tree","changed":3}}   // while paused: the count moved
{"type":"deployments","component":"apps/crm","data":{"op":"data","deployment":"dev","busy":"","state":"seeded"}}
{"type":"deployments","component":"apps/crm","data":{"op":"status","deployment":"dev",
  "level":"error","message":"…","ts":1790000000,"transient":false}}             // a non-primary tile-report
{"type":"deployments","component":"apps/crm","data":{"op":"notify","deployment":"dev",
  "to":"user:bob","title":"…","at":"…"}}                                         // a notification that was not pushed (P13)

// reader forms, for the reader audience
{"type":"deployments","component":"apps/crm","data":{"op":"record","what":["liveReload"]}}
                                                                                // sent only when the reader view of
                                                                                // State changed; what names its fields
{"type":"deployments","component":"apps/crm","data":{"op":"deploy","deployment":"main",
  "checkpoint":"c:3f2a1c9","result":"running","phase":"build","by":"user:ana"}}  // deploys onto the primary only;
                                                                                // no id, how, from or session
```

- `deploy` events never carry compiler output. It is in the deploy log's
  `error` and in the deployment's log. Op `build`'s `text` carries it for a
  non-primary build, as `build-error` does for the primary.
- `session` names the terminal or agent session that acted, so its own window
  can skip the notice it would otherwise print ([10-ux.md](10-ux.md) §9).
- The `work-tree` op is debounced like the watcher, and carries the same count
  as `State.workTree.changed`. It also tells a code panel that the work tree
  moved while nothing reloads.

### 3.4 Who receives what

Today every non-bus event reaches every subscriber, except `pr`, `term` and
`session` (`internal/server/server.go:592-612`; side finding #2). The
deployments events are filtered by the audiences of §0.5, checked at each
delivery against the subscriber's current level. T is the event's tile.

| Event | Delivered to |
|---|---|
| the primary's events, today's types | as today |
| `deployments` op `record` | full form: T's write audience. Reader form: the rest of T's reader audience, only when the reader view changed |
| `deployments` op `deploy` onto the primary | full form: T's write audience, and D's own principals when `from` is D. Reader form: the rest of T's reader audience |
| `deployments` op `deploy` onto a non-primary D; ops `reload`, `build`, `data`, `status`, `notify` naming D | T's write audience; D's own principals |
| `deployments` op `work-tree` | T's write audience |
| `bus` with `deployment` | admins; principals bound to that namespace (08-data.md §4.3), never a primary-bound principal of the same scope |
| `term` | as today (per user); `data` gains `deployment` for a session with a named target (§7.4) |

The primary's frame token, which is minted for readers, receives reader forms
only. Other tiles' principals never receive a `deployments` event naming a
non-primary deployment, nor a `bus` event of a namespace they aren't bound to.

### 3.5 What publishes what

| Trigger | Events |
|---|---|
| a save, live reload on the primary | today's: `reload`, then the runner's `build-*`, bare |
| a save, live reload on non-primary N | op `reload` for N, then op `build` phases for N; nothing of today's types |
| a save while paused | op `work-tree` when the count moved, and nothing else |
| a resume or attach onto X (X now follows the work tree) | op `deploy`; X primary: today's `build-*`, bare, and one bare `reload` after a swap that changed its code; X non-primary: op `build`, and op `reload` after such a swap |
| a deploy that puts a checkpoint on X (deploy, promote, roll back, reload now, and the pin that pausing live reload, attaching it elsewhere, protecting or reassigning takes) | op `deploy` on each result or phase change; after a swap that changed X's code, one bare `reload` when X is the primary, op `reload` otherwise; never `build-*` |
| a restart of X's current code (crash, reap, grant change, alwaysOn, `restart:true`) | X primary: today's `build-*`, bare; X non-primary: op `build` |
| any record change | op `record` |
| a primary reassignment | op `record`, plus one bare `reload`, since the bare URL now serves other code |
| a lifecycle change | the bare `reload`, as today (`internal/broker/lifecycle.go:114`); op `reload` for each non-primary deployment |
| seed, reset or restore progress | op `data` |
| a non-primary tile-report or notify | op `status` or `notify` (never the `status` type, never a push) |

A pinned deployment's frames never reload on saves (model §8).

## 4. Headers

```
X-XBin-Deployment: <name>
```

- **Injected** by `identify` (`internal/proxy/proxy.go:275-302`) on every
  proxied request whose caller is one of a tile's own principals bound to a
  **non-primary** deployment. That covers calls to other tiles' primaries under
  the edge policy and the deployment's own self-calls. The value is the
  caller's deployment name. The header is absent on calls from the primary,
  from humans, from `xbin/cron`, `xbin/bus` and `ingress`: the primary's
  callees see exactly today's headers.
- **Stripped inbound** by the existing loop that removes every `X-Xbin-*`
  (`internal/proxy/proxy.go:276-280`; for ingress
  `internal/proxy/ingress.go:47`). Receiving it therefore means xbind set it.
  No new stripping code is needed.
- `X-XBin-From` stays the bare tile path (docs/auth.md). `X-XBin-Role` is the
  clamped role under `read` ([09-fabric.md](09-fabric.md) §5.5).
- **Response header (NP-11-12):** a proxied response from a non-primary
  deployment carries `X-XBin-Deployment: <name>`, set by xbind over any value
  the backend set. A terminal `curl -i` can then tell which deployment
  answered. Primary responses are unchanged.

## 5. Env

| Variable | Who gets it | Value | Absent means |
|---|---|---|---|
| `XBIN_DEPLOYMENT` | a backend whose deployment is not the primary at spawn | its name | the primary |
| `XBIN_DEPLOYMENT` | a terminal or agent session whose target is a named deployment (§7.4) | the target's name | the session follows the primary, or has no tile API |

- **Backends** add it next to `XBIN_COMPONENT` (`internal/runner/runner.go:425`).
  A primary reassignment restarts the affected backends, so the value holds
  for a process's life (§0.2). Everything else keeps its name and meaning:
  - `XBIN_COMPONENT` is the bare tile path, so `Self()`, vault URLs and
    `res:${self}` ids keep working;
  - `XBIN_TOKEN` is per generation and carries the deployment server-side (§7.3);
  - `XBIN_RES_*` values are **identical** to the primary's, because the
    deployment's data namespace is bound at the primary's paths (model §12).
- **Sessions** add it next to `XBIN_COMPONENT` in both session env paths: the
  host shell (`internal/term/term.go:460`) and the sandbox env
  (`internal/term/term.go:740`). It is fixed at session start, like the
  network scope. `XBIN_URL` and `XBIN_TOKEN` are unchanged; the token carries
  the target.
- **Tile-managed sandboxes** (D113, once built) get nothing new: they hold no
  token and no API ([../tile-sandboxes.md](../tile-sandboxes.md) §2).
- Every statement of the env contract is updated together, as repo AGENTS.md's
  grep rule requires ([research/builder-contract.md](research/builder-contract.md) §2).

## 6. SDK and web client

**Go SDK** (`sdk/`, zero dependencies; compat rule 8, permissive only):

```go
// Deployment returns this backend's tile deployment when it is not the tile's
// primary, "" otherwise. Self() stays the tile path.
func Deployment() string { return os.Getenv("XBIN_DEPLOYMENT") }

type CallerInfo struct {
	// …existing fields (sdk/xbin.go:85-103)…
	// Deployment is the calling tile's deployment when the call comes from one
	// of its non-primary deployments (X-XBin-Deployment); "" otherwise.
	Deployment string
}
```

`Caller` (`sdk/xbin.go:107-114`) adds
`Deployment: r.Header.Get("X-XBin-Deployment")`. No existing function
changes: the server resolves the deployment from the instance token.

**Web client** (`/vendor/xbin-client.js`, shipped with the binary):
- `xbin.deployment` is added to the `Object.freeze`d `window.xbin`
  (`web/xbin-client.js:412`) only when `<meta name="xbin-deployment">` is
  present, following the `native` precedent. It is the document's deployment
  name.
- `xbin.self` stays the tile path (`web/xbin-client.js:50`).
- Token renewal (`web/xbin-client.js:81-92`) is unchanged: the server copies
  the claim from the renewing token (§7.2).
- `xbin.events` delivers `deployments` events like any other type; a frame at
  a deployment URL listens for its deployment's ops `reload` and `build`
  (§2.5).

## 7. Tokens and principals

### 7.1 Bound deployment

When a request is authorized, each of a tile's own principals has one concrete
bound deployment:

| Principal | Bound to |
|---|---|
| frame token, tile-origin cookie | the claim or origin; no claim means `main` (§7.2, §7.5) |
| instance token | the deployment whose generation it was minted for (§7.3) |
| terminal and agent tokens | the session's target; a session that follows the primary is bound to the current primary, evaluated per request. While the primary is protected such a token is bound to nothing: every self-call and self-scoped route answers 403 until the session restarts (§7.4) |

Humans, the owner token, `xbin/cron` and `xbin/bus` are bound to nothing. DR1
and DR2 (§0.4) are decided on this.

### 7.2 Frame tokens

```
5 fields (main, and every token minted today):  b64url(component)|b64url(user)|exp|gen|hmac
6 fields (a deployment other than main):         b64url(component)|b64url(user)|exp|gen|b64url(deployment)|hmac
hmac = base64url(HMAC-SHA256(.xbin/secret, <the preceding fields joined with "|">))
```

- **Verification** (`verifyFrame`, `internal/auth/frametoken.go:252-294`)
  gains a 6-field case: `gen` must be valid, and the deployment must match the
  name grammar and must not be `main` (one spelling per meaning). The 4-field
  legacy form and the 5-field form mean `main`.
- **Downgrade:** a pre-feature verifier accepts only 4 or 5 fields
  (`internal/auth/frametoken.go:255-265`), so it refuses deployment tokens
  (fail closed).
- **A removed deployment:** the token still verifies. The request then fails
  resolution with 404.
- **Minting** in the D4 injection (`internal/server/static.go:431-467`):
  - the claim names the document's deployment under the name rule (§0.2);
  - `mayMintFrameToken` (`static.go:494-503`) mints, for **every** document,
    bare or qualified, only for:
    1. a human who may open that deployment: read for the primary, write for a
       non-primary deployment, checked at mint and at every renewal;
    2. a frame, terminal or agent principal of the tile whose bound deployment
       (§7.1) is the document's deployment;
    3. today's navigation rule across one directory tree (`static.go:502`,
       `writersCover` at `:512`), only for a frame bound to its own tile's primary
       and only toward another tile's primary document.
  - Anyone else gets the document with `content=""`. A frame of `main` that
    navigates to or fetches `dev`'s URL gets no `dev` token, and a frame of
    `dev` that fetches the bare `/c/<tile>/` gets no primary token (P12).
    Backend principals never get one, as today (`backendPrincipal`,
    `static.go:555-557`).
  - `xbin.window` sub-path documents inherit their opener's deployment.
- **Renewal** (`GET /frame-token`, `internal/server/api.go:241-253`) copies the
  caller's claim when a tile renews itself; a tile principal can renew only
  its own bound deployment's token. A human asks with
  `?component=<tile>&deployment=<name>`, which needs write for a non-primary
  deployment.
- The token stays opaque to frontends. The field-count paragraph in
  `docs/protocol.md:26-39` gains the 6-field form.

### 7.3 Instance tokens

- The token map becomes token → (component, deployment)
  (`internal/auth/auth.go:219`). `RegisterInstance(token, component,
  deployment)` is called at spawn (`internal/runner/runner.go:488`).
- The principal is `Principal{Component, Via: "instance"}` plus its bound
  deployment. It is revoked at process exit, as today.
- Vault values stay readable only by an instance token (D30), and only the
  bound deployment's vault.

### 7.4 Terminal and agent tokens, and session targets

- `termID` (`internal/auth/auth.go:242-245`) gains `deployment`: a name, or
  `""` for "follows the primary". `MintTerminal`
  (`internal/auth/auth.go:471`, called at `internal/term/term.go:338`) takes it.
- **The target is chosen in the terminal window's tile-API dropdown** (P24),
  which today offers `🔌 tile API` and `⛔ no API` (`web/frame-titlebar.js:162-167`).
  With a record it offers one entry per deployment the user may reach, then
  "no API"; a protected primary is never offered. There is no separate target
  picker. For a tile without a record, or with only an unprotected `main`, the
  dropdown and the wire are exactly today's.
- **Choosing the target** at session start (P24):
  1. `api=0` (the `⛔ no API` entry): no terminal token, as today
     (`internal/term/term.go:188-190`).
  2. A requested `deployment=<name>`: the current primary's name stores `""`,
     unless the primary is protected, which answers 403 (§1.14); a
     non-primary deployment stores its name; an unknown one answers 404.
  3. Nothing requested: the primary (`""`), unless it is protected; then the
     live reload target, stored by name; when there is none (a protected
     primary with live reload paused), no API: the session opens without a
     token and echoes `api: false`.

  Old clients that send no `deployment` (the shipped app's terminal, an old
  `bx agent run`, a stale tab) therefore get the primary, as today, or the
  fallback of step 3 when it is protected.
- **Requesting a target:** the target is fixed for the session's life, like the
  network scope. Changing it restarts the session.

  | Surface | Request |
  |---|---|
  | shell | `GET /ws/term?cwd=<tile>&deployment=<name>` (new sessions; ignored on reattach), or `api=0` |
  | agent session | `POST /api/xbin/term/sessions?deployment=<name>` (the body is unchanged, `internal/server/agentapi.go:72`; `api: false` in it means no API, as today) |
  | restart an agent onto a target | `POST /api/xbin/term/sessions/{id}/restart?deployment=<name>` |

- **Echo:** the `session` control frame, `SessionInfo`
  (`internal/term/sessions.go:20-41`) and `term` events carry
  `deployment: "<name>"` for a named target, beside today's `api`. An older
  xbind ignores the parameter, so clients **must** check the echo: a missing
  echo means that xbind can't target deployments, and the client ends the
  session and says so.
- **Protecting the primary** restarts every session that follows it, with
  the target chosen again by step 3: an agent session as `…/restart` does
  (its conversation resumes), a shell by ending it with a notice
  ([10-ux.md](10-ux.md) §9). Until then its token is bound to nothing (§7.1).
- **A primary reassignment** restarts the sessions whose target is named
  after the old or the new primary, the same way, so `XBIN_DEPLOYMENT` stays
  true and no session is left on a protected primary. Sessions following the
  primary follow the new one without a restart.
- A target that stops existing (the deployment is removed) makes the session's
  self-calls answer 404. The session isn't killed.

### 7.5 Tile-origin tickets and cookies

- `main`'s origin keeps today's `x1` tickets and `c1` cookies, byte-identical
  (`internal/auth/tilebinding.go:39-44`, `internal/auth/assettoken.go:32-38`).
- A deployment origin uses `x2` and `c2`, under the purposes
  `xbin-tile-ticket-v2` and `xbin-tile-origin-v2`. They carry the deployment as
  one more MAC-covered field:

  ```
  c2.b64(tile).b64(user).exp.b64(gen).b64(deployment).mac
  ```

  A pre-feature verifier requires exactly six parts and its own prefix
  (`internal/auth/assettoken.go:110-114`), so it refuses them.
- `TilePrincipal` (`internal/auth/assettoken.go:215-225`) carries the bound
  deployment.

### 7.6 Asset tokens

Unchanged (`a1…`). The qualified path under `/c/~<tok>/` carries the
deployment (§2.6).

### 7.7 whoami

- Element principals (`internal/broker/whoami.go:62-67`) gain `deployment`
  when bound to a non-primary deployment (role rule).
- A terminal token following the primary reports no `deployment`.
- Human answers are unchanged.

## 8. Additive extensions to existing routes

| Route | Change | Why this shape |
|---|---|---|
| `GET /backends` (`internal/boot/api.go:21-27`) | The row stays the **primary's** `{state, gen, error?}`. It gains `deployment` (the primary's name) and `deployments: {"<name>": {state, gen, error?}}` for non-primary deployments, both only when the tile has a record. Admin only, as today. | The old shell counts `Object.values(/backends)` and `state === 'healthy'` for its footer (`workspace-template/shell/bx-shell.js:1300`, `:1326`). New keys would inflate it. |
| `GET /runtime` (`internal/boot/api.go:30`) | `backends[]` keeps one row per tile, the primary, which gains `deployment` when the tile has a record. A row whose running generation runs a checkpoint gains `checkpoint`, its full tree id (absent while it runs the work tree). Non-primary rows go to a new top-level `deploymentBackends[]`: `runner.Backend` (`internal/runner/inspect.go:26-60`) plus `deployment`. | The admin console maps rows by `path` (`workspace-template/tiles/admin/tabs/runtime.js:336-340`). A second row per path would overwrite the primary's. |
| `GET /tile-status` (`internal/boot/api.go:87-117`) | `?deployment=<name>`; DR1 and DR2 pick the default. The gate stays admins or the tile's own principals, narrowed by DR1: frame and instance principals read their bound deployment only; admins and the tile's terminal and agent tokens may name any. The answer gains `deployment` (the reported one) when the tile has a record, and, for admins and terminal and agent tokens only, `deployments: {primary, liveReload, items: [{name, state, gen, checkpoint?}]}`. | `bx status` renders it. |
| `GET /logs` (`internal/obs/logs.go:39-80`) | `?deployment=<name>`; DR1 and DR2 pick the default. 404 for an unknown deployment. A non-primary answer carries the `X-XBin-Deployment` response header, the echo of a text/plain answer. The gate (`canReadLogs`, `logs.go:42-47`: the tile itself or terminal level) is narrowed by DR1: frame and instance principals read their bound deployment's log only; the tile's terminal and agent tokens, humans with terminal level and admins read any deployment's. | Per-deployment log files are internal (07-runtime.md §9). `.xbin` is masked in isolated terminals. |
| `GET /components`, `/components/<path>` (`internal/server/api.go:129-160`, `:162-198`, `:202`) | Entries of a tile with a record gain the primary summary `deployments: {"primary": "main", "pinned": true, "protected": false}` (pinned: the primary doesn't follow the work tree), the same for every caller who sees the row: no non-primary name and no count. `runtime`, `hasIndex` and `native` describe the primary's code (its checkpoint when pinned), so `native` always agrees with what `/c/<tile>/?native=1` serves (§2.7); `chrome` and `template` follow the primary's code too (§2.8). `manifestError`, `roles`, `uses` and `deps` keep describing the work tree. `origin` is the primary's origin. Deployments are **never** rows. | Old shells and the app parse leniently and list rows as tiles. The frame chip and the scaffold read this summary, so a closed terminal window costs no request ([10-ux.md](10-ux.md) §6.4). Today's fields come from the work-tree component (`api.go:174-180`). |
| `GET /frame-token` (`internal/server/api.go:241-253`) | `?deployment=<name>` for humans (§7.2). The answer gains `deployment`, the echo. | |
| `GET /whoami` | `deployment` for bound element principals (§7.7). | |
| `GET /term/sessions`, `GET /status` terminals | Rows gain `deployment` for a named target. | |
| `POST /term/sessions`, `POST /term/sessions/{id}/restart`, `GET /ws/term` | `?deployment=<name>`, echoed (§7.4). | Adding a body field would 400 on older servers (§0.6). |
| `GET /sandboxes` (`internal/boot/sandboxes.go:42-49`) | Follows the D112 registry, name rule. `main`'s rows are byte-identical to today's, even when the tile has a record: id `backend:<CompKey>:g<gen>` (`internal/runner/sbx.go:63`), no `deployment` field; only their `leaf` changes, once the tile runs a non-main deployment ([07-runtime.md](07-runtime.md) §10.3). Every other deployment's backend rows have the id `backend+<name>:<CompKey>:g<gen>`, carry `Entry.Deployment` as `deployment`, and have `stats.scope` `"deployment"` (`sandboxes.go:62-69`). `?deployment=<name>` narrows. VM `usedBy` stays per tile (D112: every deployment is charged to the tile). D113 `tile` rows, once built, carry their deployment when it isn't `main`. | The admin tab groups by tile and counts one `"tile"` scope per tile (`workspace-template/tiles/admin/tabs/sandboxes.js:211-233`). |
| `GET /tile-report` | Unchanged: it never lists non-primary status. | §3.2 |
| `POST /tile-report`, `POST /notify` from a non-primary principal | Stored per deployment or suppressed. Answers are unchanged, plus `suppressed: true` on notify. Published as `deployments` op `status` or `notify`. | P13 |
| `GET`/`PUT`/`DELETE /cron/jobs`, `/bus/subscriptions` | A non-primary principal lists and writes its own deployment's files (§10.2). Rows gain `deployment` and `dormant`. `?deployment=<name>` is for admins. A PUT answer gains `dormant: true`. | The SDK subscribes at every start and must keep succeeding (compat rule 8). |
| `PUT /iface-instances`, `/ingress-hosts` from a non-primary principal | Stored, dormant, never routed. No `grants` event, no restart, no reconcile. The answer gains `dormant: true`. | These PUTs replace the whole map today (`docs/protocol.md:1707`, `:1721`). |
| `/vault/<component>` routes | DR1: a tile principal acts on its bound deployment's vault. Tile managers and admins use `?deployment=<name>`. | Vault URLs are built from `Self()` (`sdk/xbin.go:82`). |
| kv, blob, bus publish | No wire change: calls resolve in the caller's namespace (08-data.md). | |
| `POST /grants` (approve or add) | Granting `xbin` or an `xbin:*` target to a tile that has non-primary deployments answers 409 (§1.14, P19). | Governance has no deployment dimension (05-model §12). |
| every other `/api/xbin/*` route | A principal bound to a non-primary deployment gets 403 on a route without a class (§0.5, P26). | Default-deny: an unconverted handler would act on the primary's state. |

**The echo rule** (12-compat.md NP-12-2). Every `deployment` parameter sent
to an existing route is echoed in the answer: a `deployment` field, or the
`X-XBin-Deployment` response header on a non-JSON answer. An older xbind
ignores the parameter and answers for the primary. A client that finds no
echo must treat the answer as the primary's and say so, never show it as the
deployment's.

## 9. `bx`

This section is authoritative for `bx`'s commands, flags, output and exit
codes; [10-ux.md](10-ux.md) §8 adopts them verbatim and adds the human side.

### 9.1 Conventions

- **Files:** four new files, each command family registering in `moreCmds`
  from an `init()`:
  - `cmd/bx/livereload.go` for `live-reload`;
  - `cmd/bx/deploy.go` for `deploy`, `promote` and `rollback`;
  - `cmd/bx/deployment.go` for the `deployment` family;
  - `cmd/bx/deployclient.go` for the shared plumbing: the dry run, the prompt,
    waiting, the exit codes.

  Dispatch is `cmd/bx/agent.go:30-33`, the map `cmd/bx/native.go:28-32`.
  Usage text joins the `+nativeUsage` concatenation (`cmd/bx/main.go:174`), a
  line-neutral edit: `main.go` is at 1150/1150.
- **Prep:** `bx status` and `bx logs` change, so their functions
  (`cmdStatus`, `cmd/bx/main.go:349`; `cmdLogs`, `:494`) first move out
  verbatim ([13-surfaces.md](13-surfaces.md)).
- **Tile refs:** wherever `<tile> <name>` is accepted, `<tile>+<name>` is too,
  and the qualifier also fills a missing `--to`. `bx` sends the ref unresolved
  and the server applies §2.2.
- **Positionals are right-aligned:** the first one is the tile only when the
  count is the command's full arity; otherwise the tile is `$XBIN_COMPONENT`.
  With neither, bx prints usage and exits 2. The glossary's tile-first
  spellings have the full count, so they stay valid. A positional containing
  `/` or `+` is always a tile ref, since deployment names can't contain
  either. A `c:` or `work-tree` argument is always a checkpoint or diff spec.
- **Destinations are named with `--to`** (resume, attach, primary, deploy,
  rollback). `bx deployment primary <tile> <name>`, the glossary's form, is
  accepted too.
- **Defaults:** a read command's deployment defaults to `$XBIN_DEPLOYMENT`
  when the tile is `$XBIN_COMPONENT`. A code move never defaults its target:
  `--to` is required, and `promote` names both deployments (DR3).
- **Flags:** `--flag value` and `--flag=value` (compat rule 6). An unknown flag
  is an error (`unknownFlag`, `cmd/bx/args.go:14`). A value-taking flag last
  on the line is `--x needs a value` (`nextArg`, `cmd/bx/args.go:28`).
- **Before acting**, every changing command fetches `GET /deployments` and, if
  the operation's `Can` is false, prints its `why` and exits without sending
  (exit 3 for `authority` and `policy`, 1 for `state`).
- **`--dry-run`** sends `dryRun: true`, prints the impact report and exits 0.
- **The report:** every changing command sends its dry run first and prints
  the impact report, in [10-ux.md](10-ux.md) §8.1's labelled lines.
- **Confirmation, for guarded commands only.** The guarded commands are the
  data-bearing ones (`seed`, `reset`, `rm`, `vault-copy`, `restore`,
  `primary`), `protect`, and every code move onto the primary: `deploy`,
  `promote` and `rollback` to it, and `live-reload now`, `resume` and
  `attach` when they move code onto it. On a terminal bx asks the
  operation's question, `Promote dev → main? [y/N]`, as `bx owner --transfer`
  does; declining exits 4. Without a terminal, a guarded command needs
  `--yes`, else bx exits 4 after the report. So an agent can't ship to the
  primary or touch data without saying `--yes`, its statement that the user
  asked. Every other changing command prints the report and proceeds.
  `--yes` also sends the operation's `confirm` token, and skips the prompt,
  never the report.
- **Naming the reviewed code:** before a code move from the work tree, bx
  sends the dry run's `impact.code.to` as `expect`, so what the report showed
  is what ships. Onto a protected primary bx always sends `seq`, and
  `checkpoint` (deploy, rollback) or `expect` (promote, now, primary) from the
  dry run (§1.2).
- **`--json`** prints the route's JSON answer verbatim, one object and nothing
  else on stdout. Refusals are the server's `{error, docs}`, or the `Can`
  object bx refused on. Progress goes to stderr.
- **Waiting:** code-moving commands poll
  `GET /deployments/log?id=<id>&wait=25` until a final result, printing each
  phase to stderr, for at most 20 minutes. The 25 s fits bx's 30 s HTTP
  timeout (`transport`, `cmd/bx/main.go:239-254`). `--no-wait` returns once
  the request is accepted, printing the id.
- **An old xbind:** every new command calls the `/deployments` family first.
  A 404 or 405 whose body is not a JSON object with `error` is Go's mux: the
  route is missing. bx then prints
  `this xbind has no tile deployments (no /api/xbin/deployments); upgrade xbind`
  and exits 6. `bx status` and `bx logs` resolve a named deployment through
  `GET /deployments` before using their old routes, and check the echo (§8).
  An old xbind therefore can't silently answer for the primary.
- **Hints:** `apiJSON`'s scoped-terminal hint (`cmd/bx/main.go:256-283`) gains
  one case: on a protected-primary 403, or a manager act refused to a tile
  credential, it adds `deploy from the terminal window's deployments panel as
  a tile manager, or with bx on the host`.

| Exit | Meaning | An agent reads it as |
|---|---|---|
| 0 | done: the deploy finished `ok`, nothing needed doing, a dry run, or `--no-wait` and the request was accepted | — |
| 1 | failed: a deploy that ran and failed (the deployment keeps its previous code), an invalid state, a network error, any other refusal | read the message, fix, retry |
| 2 | usage | fix the command |
| 3 | refused by authority or policy: a `Can` of kind `authority` or `policy`, or HTTP 403 | not yours to do: tell the user who can (the message names them) |
| 4 | not confirmed: declined, or a guarded command with no terminal and no `--yes` | ask the user |
| 5 | still running when bx stopped waiting; `bx deployment log` shows it | check later |
| 6 | this xbind has no tile deployments | don't retry here |

Existing commands keep exiting 1 on every error and 2 on usage.

### 9.2 Commands

```
bx live-reload [<tile>]                      state: target or paused (by whom, when), the last target, what it
                                             runs, work-tree files changed since (State.workTree)
bx live-reload pause  [<tile>]
bx live-reload resume [<tile>] [--to <name>] default: where live reload was last attached
bx live-reload now    [<tile>]
bx live-reload attach [<tile>] --to <name>   from attached only; while paused, bx points at resume

bx deployment ls  [<tile>]                   one tile, marking this session's target; with no tile and no
                                             $XBIN_COMPONENT: every tile whose /components row has a summary
bx deployment add [<tile>] <name> [--from work-tree|primary|c:<id>] [--seed] [--attach]
bx deployment rm  [<tile>] <name>
bx deployment restart [<tile>] <name>        restart its current code (deploy with restart:true)
bx deployment primary [<tile>] --to <name>   also: bx deployment primary <tile> <name>
bx deployment protect [<tile>] on|off
bx deployment seed  [<tile>] <name> [--stop]
bx deployment reset [<tile>] <name> [--vault]
bx deployment vault-copy [<tile>] <name> (--keys <k1,k2> | --all)      --keys may repeat
bx deployment set [<tile>] <name> [--deliveries on|off] [--always-on on|off]
                  [--mem <MiB>|default] [--pids <n>|default] [--disk <GiB>|default]
bx deployment edge [<tile>] [<edge> read|block|inherit|default]   no edge: list the edges, policies and counts
bx deployment run-now [<tile>] <name> <job>
bx deployment log  [<tile>] [<name>] [--limit <n>]
bx deployment diff [<tile>] [<from> [<to>]] [--stat] [--path <file>]
                                             from/to: a deployment name, c:<id>, or work-tree;
                                             defaults: the primary, and the work tree
bx deployment fetch [<tile>]                 git fetch of the checkpoint remote into refs/deploy/* (§1.12)
bx deployment backup  [<tile>] <name>
bx deployment backups [<tile>] <name>
bx deployment restore [<tile>] <name> [--version <v>] [--into <name>] [--replace]
bx deployment backup-schedule [<tile>] <name> (--every 24h | --cron "<expr>") [--keep <n>] | --rm

bx deploy   [<tile>] --to <name> [--checkpoint c:<id>] [--no-wait]
bx promote  [<tile>] <from> <to> [--no-wait]
bx rollback [<tile>] --to <name> [--checkpoint c:<id>] [--no-wait]   default: the previous checkpoint in its log
```

Every changing command takes `--yes`, `--dry-run` and `--json`. Every read
command takes `--json`.

- **`set`** calls the deliveries, always-on and limits routes, in that order,
  for the flags given, and stops at the first refusal. Each operation still
  has one route. `default` sends `null` for that limit.
- **`backup-schedule`** takes today's `bx backup-schedule` flags
  (`cmd/bx/main.go:662-665`), so both spell schedules alike.
- **`fetch`** runs `git fetch` in the current directory's repository, as
  §1.12 spells it, and prints which `deploy/<name>` refs it updated. It needs
  no `--yes`: it changes nothing in xbind.
- **Refinements of the glossary's proposals** (the nouns are kept; NP-11-7):
  - roll back gets its own `bx rollback`, logged `rollback`.
    `bx deploy --checkpoint` remains the deploy of a named checkpoint, logged
    `deploy`. Both put the same code on the deployment.
  - deliveries, alwaysOn and limits are `set` flags; protection and restart
    have their own subcommands, since each is its own operation.

Text output, for example:

```
$ bx deployment ls apps/crm
apps/crm · primary main · live reload → dev
  NAME  ROLE     CODE                STATUS       DATA
  main  primary  pinned c:3f2a1c9    healthy g12  original
  dev   -        follows work tree   building     seeded from main 2026-09-27   ← this terminal
$ bx promote apps/crm dev main --yes
Promote dev → main (the primary of apps/crm)
  Code     main runs c:8d01e2a (the work tree at 14:02) · 3 files, +41 −7 against c:3f2a1c9
  Data     only code moves
  Affects  everyone using apps/crm: frames reload once
apps/crm: promote 44 dev → main c:8d01e2a … build … start … swap … ok (38s)
```

The labels and report lines are [10-ux.md](10-ux.md) §8.1's and §12's. They
are rendered from `State` and `Impact`, never computed by bx.

### 9.3 Changes to existing commands

- `bx status [<tile>[+<name>]] [--deployment <name>] [--all]` calls
  `/tile-status?component=&deployment=`. It prints a `deployments:` line when
  the tile has a record. Every other argument is still taken as the component,
  last one wins (compat rule 6).
- `bx logs [-f] <tile>[+<name>] [--deployment <name>]`: with a deployment
  named, or given by `$XBIN_DEPLOYMENT`, it streams
  `GET /logs?component=&deployment=[&follow=1]`. Otherwise it behaves as today.
- `bx agent run … [--deployment <name>]`, also `--tile <tile>+<name>`, sends
  `POST /term/sessions?deployment=` and verifies the echo (§7.4).
- `docs/bx.md` §Environment (`docs/bx.md:381-387`) gains `XBIN_COMPONENT` and
  `XBIN_DEPLOYMENT`.

## 10. On-disk formats

Everything here lives under `data/` or `.xbin/`. It is never in the root
`xbin.json` or the work tree (P15), and it is masked from every terminal. The
on-disk shape of `data/` is not a compat promise (docs/compat.md §What this
does not promise). What matters is that no older binary misreads it
([12-compat.md](12-compat.md)).

**`<TileKey>`** names every store this design adds (05-model §3):

```
TileKey(path) = lowercase-hex(SHA-256("xbin-tile-key-v1" ‖ 0x00 ‖ path)[:16])     ; 32 hex digits, 128 bits
```

It replaces `util.CompKey` (`internal/util/util.go:137-144`), which keeps 32
bits of hash and can be ground, for the new stores only: `data/deployments/`,
`data/checkpoints/` and `.xbin/deploy/`. Existing stores keep their keys:
`main`'s logs, build output, sockets, cgroup leaves and registry ids stay
`CompKey`-keyed. Every new store also records the full tile path and refuses a
load whose path doesn't match (06-security C10).

### 10.1 The deployment record: `data/deployments/<TileKey>.json`

```jsonc
{
  "schema": 1,                          // bumped only when an older reader would misroute
  "tile": "apps/crm",                   // must equal the path it is read for
  "owner": "org:devs",                  // the tile's owner ref when the record was made; rewritten by transfer (D39)
  "created": "2026-09-27T10:12:03Z",
  "seq": 18,                            // +1 on every committed change
  "liveReload": "dev",                  // "" = paused
  "lastLiveReload": "dev",
  "liveReloadSince": {"at": "…", "by": "user:ana"},
  "primary": "main",
  "protectedPrimary": false,
  "edges": {"slot:llm": "block", "grant:apps/calendar": "read"},   // overrides only
  "nextDeploy": 45,                     // the next deploy id
  "deployments": {
    "main": {"checkpoint": "<full tree id>", "created": "…", "by": "user:ana"},
    "dev":  {"checkpoint": null, "deliveries": false, "alwaysOn": false, "limits": {"memMiB": 1024},
             "created": "…", "by": "user:ana"},
    "exp":  {"checkpoint": "<full tree id>", "state": "failed", "created": "…", "by": "user:ana"}
                                        // state "failed": the attempted checkpoint of a failed move off the work tree
  }
}
```

- **Writes:** only the deployments API writes it, with
  `fsutil.WriteFileAtomic` (D60), mode 0600.
- **Binding to its tile (P29):** a record whose `tile` isn't the path it is
  read for, or whose `owner` isn't the tile's current owner ref, is ignored:
  the tile behaves as zero-state, and the record is inert until an admin
  adopts or clears it ([12-compat.md](12-compat.md)). Every tile creation path
  removes a record left at the path before it creates the tile (05-model §11).
- **Deployment data state** is not stored here: it is derived from the
  namespace's `ns.json` ([08-data.md](08-data.md) §3), because sibling tiles
  share that namespace.
- **Unknown fields**, at the top level and per deployment, are preserved on
  every rewrite. The root `xbin.json`'s struct re-marshal drops them
  (`internal/registry/registry.go:586-595`), and this file must not repeat
  that hazard.
- **Invariants:** model §4. Stored checkpoints are full tree ids; only
  answers and input use the `c:` short form (07-runtime.md §2.3).
- **A reader that sees a higher `schema` holds the tile.** Its deployments'
  backends don't start, `/c/` and `/api/` answer 503 with the 409 text of
  §1.14, and every `/deployments` write answers 409. It never falls back to
  the work tree (P9).
- A binary without the feature never reads `data/deployments/`. The downgrade
  story is [12-compat.md](12-compat.md)'s.

**The deploy journal,** `data/deployments/<TileKey>/pending.json` (Q3,
NP-07-13): every accepted attempt until its deploy-log entry is written,
mode 0600, written atomically.

```jsonc
{
  "schema": 1,
  "tile": "apps/crm",
  "next": 44,                           // the next deploy id: never below the record's nextDeploy or past the log's newest
  "attempts": [                         // the deploy entry's fields (§1.1) with full tree ids, plus:
    {"id": 43, "deployment": "main", "how": "reload-now", "checkpoint": "<full tree id>", "previous": "<full tree id>",
     "by": "user:ana", "requestedAt": "…", "result": "running", "phase": "build",
     "pointer": "request",              // the record's pointer was written at request time (a move off the work tree)
     "swapped": false}                  // the swap committed the pointer
  ]
}
```

Boot reconciles it into the deploy log (§1.10) and never rewrites the record.
The file goes with the record at an opt-out. Backups leave it out: a
restored tile has no deploy in flight.

### 10.2 Per-deployment registration files: `data/deployments/<TileKey>/<name>/`

One directory per deployment other than `main`, created on its first
registration:

```
cron.json               {"schema": 1, "jobs": [{"name", "resource", "schedule", "path", "role"}]}
bus-subscriptions.json  {"schema": 1, "subscriptions": [{"name", "resource", "prefix", "path", "role"}]}
iface-instances.json    {"schema": 1, "instances": {"<id>": "<path prefix>"}}
ingress-hosts.json      {"schema": 1, "hosts": ["crm.example.com"]}
backup-schedule.json    the deployment's backup schedule, in today's row shape (08-data.md)
sandboxes.json          (D113, once built) the deployment's sandbox definitions, in D113's shape
```

- The rows are today's shapes without `component`.
- `main`'s registrations stay in `data/cron-jobs.json`,
  `data/bus-subscriptions.json` and the root `xbin.json` maps, whether or not
  `main` is the primary.
- An older xbind never loads these files, so it can't fire them as `main`'s
  (P13).
- Which set is active follows the primary role, plus deliveries (model §7).
- Vault files, data namespaces, logs and built artifacts are keyed by
  [08-data.md](08-data.md) and [07-runtime.md](07-runtime.md).

### 10.3 The checkpoint store and the view repository

**The store,** `data/checkpoints/<TileKey>.git`: a bare repository in git's
default object format, so a tile's own history can be referenced. Only
confined git reads or writes it (D78, P16), and it is never served.

```
HEAD                              ref: refs/heads/deploy/<primary>  (dangling while the primary follows the work tree)
config                            core.bare=true, gc.auto=0; no hooks, remotes, alternates or fsmonitor
refs/heads/deploy/<name>          the checkpoint commit a PINNED deployment runs; absent while it follows the work tree
refs/xbin/checkpoints/<tree id>   one per retained checkpoint: its commit (reachability; GC deletes these, 08-data.md)
refs/xbin/views/<tree id>         that checkpoint's git-view commit (below)
refs/xbin/log/<name>              the deployment's deploy log, a commit chain
objects/, packed-refs             confined git
```

**The view repository,** `data/checkpoints/<TileKey>.view.git`: a bare
repository derived from the store, the only thing the checkpoint remote
(§1.12) serves. It exists only while the tile has a record, and is removed
with it.

```
HEAD                              ref: refs/heads/deploy/<primary>  (dangling while the primary follows the work tree)
config                            core.bare=true, gc.auto=0; nothing else
refs/heads/deploy/<name>          the git-view commit of the checkpoint <name> is pinned to; pinned deployments only
objects/                          only objects reachable from those refs
info/refs, objects/info/packs     written by a confined `git update-server-info`
```

After every change to the pinned set, a confined run rebuilds the view
repository from the store's `refs/xbin/views/*` commits into a fresh
directory, runs `git update-server-info` there, and renames it into place. It
therefore never holds an object that a current `deploy/<name>` doesn't reach,
and a deleted or retired view leaves nothing fetchable behind.

**Checkpoint commit** ([07-runtime.md](07-runtime.md) §2.3). There is one
parentless commit per tree, made at the tree's first capture. The checkpoint
id is `c:` plus a prefix of the **tree** id, so identical content is one
checkpoint. Pausing live reload is normally a code-identical swap, and it
names the same checkpoint again.

```
tree       <the captured tree>
author     xbin <xbin@localhost> <at>
committer  xbin <xbin@localhost> <at>

checkpoint of apps/crm

Xbin-Tile: apps/crm
Xbin-Feed: work-tree
Xbin-By: user:ana                       (the acting principal's From())
Xbin-At: 2026-09-27T10:12:03Z
Xbin-Work-Tree-Head: <commit id>        (absent without a repository or with an unborn HEAD; untrusted)
```

**Git-view commit.** One parentless commit per checkpoint, made in the same
confined run as the capture. Its tree is the checkpoint's tree minus every
path the tile's own ignore rules exclude, evaluated in confine at capture, so
a branch made from `deploy/<name>` never tracks `node_modules`, `.env` or
build output.

```
tree       <the captured tree minus ignored paths>
author     xbin <xbin@localhost> <at>
committer  xbin <xbin@localhost> <at>

checkpoint c:3f2a1c9 of apps/crm (git view: ignored files left out)

Xbin-Tile: apps/crm
Xbin-Checkpoint: <full tree id>
Xbin-Work-Tree-Head: <commit id>        (as above)
```

Being parentless, a git view fetched over the remote shares no history with
the tile's own commits. `Xbin-Work-Tree-Head` names the commit it was taken
on, so a hotfix branch can be rebased onto that commit (flow H).

**Deploy log entry.** There is one commit per finished attempt, appended to
`refs/xbin/log/<name>`. Its tree is the attempted checkpoint's, so
`git log -p refs/xbin/log/main` in the store shows what each deploy changed.
The chain also keeps every logged checkpoint reachable. It never reaches the
view repository.

```
tree       <the attempted checkpoint's tree>
parent     <the previous entry of this deployment> (none for the first)
author     xbin <xbin@localhost> <finished at>
committer  xbin <xbin@localhost> <finished at>

promote c:3f2a1c9 → main: ok

Xbin-Deploy-Id: 44
Xbin-Deployment: main
Xbin-How: promote
Xbin-From: dev
Xbin-Checkpoint: <full tree id>
Xbin-Previous: <full tree id>
Xbin-Feed: work-tree
Xbin-Follows-Work-Tree: false
Xbin-By: user:ana
Xbin-Via: session
Xbin-Agent: false
Xbin-Session: <session id>              (only when a terminal or agent session acted)
Xbin-Requested-At: 2026-09-27T10:12:03Z
Xbin-Result: ok
Xbin-Error: <first line, at most 500 bytes; failed only>
```

- Trailers are single-line. xbind reads them with git's trailer parsing, in
  confine.
- `DeployEntry` (§1.1) is these trailers, spelled in camelCase. Stored ids are
  full; answers abbreviate them (§0.3).
- Trimming a log rewrites its ref to a shorter chain. GC then prunes
  ([08-data.md](08-data.md)).
- The log is git-native because only confined tools may write the store;
  [07-runtime.md](07-runtime.md) §2.3 writes it this way.

### 10.4 Materialized checkpoints: `.xbin/deploy/<TileKey>/`

```
<tree id>/          the checkpoint's files: directories 0755, files 0444 (0555 when the exec bit is kept),
                    symlinks as symlinks. Extracted by confined git into .tmp-<random>/, then renamed
                    into place, so a present tree is complete. Opened beneath, never followed out (P16).
.tmp-<random>/      in-progress extractions, removed at boot
d/<name>/           a non-main deployment's derived state: its backend log (d/<name>/backend.log), and
                    later its D113 sandboxes (d/<name>/sbx/) (07-runtime.md §9)
```

- The directory is named by the full tree id, never the short id, which can
  grow.
- Deployments pinned to the same checkpoint share one tree.
- Directories stay owner-writable, so `rm -rf .xbin` and `os.RemoveAll` work
  without a chmod. Trees are read-only to backends and builds through the
  read-only sandbox bind ([07-runtime.md](07-runtime.md) §10.4), not through
  host modes. xbind never opens, stats or walks inside a tree with a
  following call on the host (06-security C5).
- `.xbin/` is derived state (`docs/protocol.md:2409-2430`). Deleting
  `.xbin/deploy/` while xbind is stopped is safe: trees are rematerialized from
  the store on demand.

### 10.5 Lifetime

- **Opting out:** resuming onto `main`, when it is the only deployment and
  every setting is at its default, removes the record and the view
  repository. The store and its deploy log stay, inert: no remote serves them
  (the checkpoint route answers 404 for a tile without a record), no event
  names the tile, and dry runs and diffs never capture into them. They keep
  the history for the next opt-in, under normal GC; a tile manager may purge
  them.
- **Removing a deployment** removes its `data/deployments/<TileKey>/<name>/`
  and `.xbin/deploy/<TileKey>/d/<name>/`.
- **Removing the tile:** the record, the store and the per-deployment
  directories are path-keyed leftovers on D82's refusal list (model §11,
  [08-data.md](08-data.md)).

## 11. Contract items, compat rules, doc pages

| Contract item | docs/compat.md rule it satisfies | Documented in |
|---|---|---|
| The zero state is unchanged on every surface (§0.1); dry runs and diffs create nothing on it (§1.2, §1.11) | 1, 2, 9: nothing written at boot, the root `xbin.json` untouched | [/docs/compat.md](/docs/compat.md), [/docs/elements.md](/docs/elements.md) |
| The `/deployments` routes, per-deployment backups and the checkpoint remote (§1) | 2: new routes only; existing bodies unchanged | [/docs/protocol.md](/docs/protocol.md) (+ openapi.go rows) |
| Qualified URLs; the two narrow `+` refusals, and a one-release warning for other `+` names (§2) | 2, 3, 11: existing URLs keep resolving; an exact component wins; nothing that works today becomes an error without a warning | [/docs/protocol.md](/docs/protocol.md) `/c/` and `/api/`, [/docs/elements.md](/docs/elements.md), [/docs/auth.md](/docs/auth.md) |
| `<bx-frame src="<tile>+<name>">`, `xbin.window` inside a deployment (§2.5) | 3: no vendor URL moves; old shells keep working | [/docs/elements.md](/docs/elements.md) §bx-frame, [/docs/frontend-kit.md](/docs/frontend-kit.md) |
| Origins-mode labels per deployment (§2.6) | 2: `main`'s label and cookies unchanged | [/docs/auth.md](/docs/auth.md) §Tile asset gating, [/docs/elements.md](/docs/elements.md) §Asset URLs |
| The inbound surface and `/components`' deployment-level fields follow the primary's code (§2.7, §2.8, §8) | 10: the shipped app's `native` always matches what `?native=1` serves | [/docs/elements.md](/docs/elements.md) (deployment-level, inbound-surface and tile-level fields, model §6) |
| The `deployments` event type; today's types only ever speak of the primary (§3) | 2, 10: receivers ignore unknown types; no old client or shipped app reloads, toasts or title-marks for a non-primary deployment | [/docs/protocol.md](/docs/protocol.md) §/ws/events, [/docs/sdk.md](/docs/sdk.md) (`xbin.events`) |
| `X-XBin-Deployment` (§4) | 2, 8: absent for the primary; stripped inbound | [/docs/protocol.md](/docs/protocol.md), [/docs/auth.md](/docs/auth.md), [/docs/sdk.md](/docs/sdk.md) |
| `XBIN_DEPLOYMENT` (§5) | 8: a new variable; existing ones keep their meaning | [/docs/elements.md](/docs/elements.md), [/docs/protocol.md](/docs/protocol.md) §Backend contract, [/docs/sdk.md](/docs/sdk.md), [/docs/overview/09-terminals.md](/docs/overview/09-terminals.md), [/docs/bx.md](/docs/bx.md) |
| `xbin.Deployment()`, `CallerInfo.Deployment`, `xbin.deployment` (§6) | 8: additive, zero-dependency | [/docs/sdk.md](/docs/sdk.md) |
| The 6-field frame token, the `x2`/`c2` tile-origin credentials (§7.2, §7.5) | 2: tokens stay opaque; old forms verify unchanged | [/docs/protocol.md](/docs/protocol.md) §Authentication, [/docs/auth.md](/docs/auth.md) |
| Session targets in the tile-API dropdown, echoed (§7.4) | 2: query parameters and response fields only; old clients get the primary | [/docs/protocol.md](/docs/protocol.md) §/ws/term, [/docs/overview/09-terminals.md](/docs/overview/09-terminals.md) |
| whoami `deployment` (§7.7) | 2 | [/docs/protocol.md](/docs/protocol.md) |
| `/backends`, `/runtime`, `/tile-status`, `/logs`, `/components`, `/sandboxes`, `/term/sessions` fields (§8) | 2: fields on rows, never new keys or rows; `main`'s registry rows byte-identical | [/docs/protocol.md](/docs/protocol.md) |
| Dormant registrations behind the same routes (§8, §10.2) | 8: a call that succeeds today keeps succeeding | [/docs/resources.md](/docs/resources.md), [/docs/protocol.md](/docs/protocol.md) |
| The `bx` commands, exit codes 3–6, new flags on `status`/`logs`/`agent run` (§9) | 6: a superset; existing invocations and exit codes unchanged | [/docs/bx.md](/docs/bx.md) + usage strings |
| No manifest key (§0.1) | 7 | [/docs/elements.md](/docs/elements.md) |
| On-disk formats (§10) | not promised; old binaries must not misread them | [/docs/protocol.md](/docs/protocol.md) §Filesystem contract, [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md) (backups) |
| Every item above | 11: a `docs/changelog.md` entry; no migration note, since nothing breaks | [/docs/changelog.md](/docs/changelog.md) |

## Divergences from the model

Resolved since the previous draft (ids kept):
- **DIV-1** (old clients and qualified events): resolved by 05-model §8 and
  rule C2 of [12-compat.md](12-compat.md) §3.1: today's types never speak of a non-primary
  deployment (§3.2).
- **DIV-2** (P17's two meanings of "absent"): resolved by P17, which now names
  the role rule for signals and the name rule for credentials and stored
  state (§0.2).
- **DIV-3** (no deployment segment in the registration path): resolved by
  05-model §3 (`data/deployments/<TileKey>/<name>/`).
- **DIV-4** (materialized trees named by short id): resolved by 05-model §3
  (the full tree hash).
- **DIV-5** (an incomplete record): resolved by 05-model §4 (`seq`,
  `lastLiveReload`, `liveReloadSince`, `schema`; `nextDeploy` and full ids are
  wire detail, §10.1).
- **DIV-6** (P21 and session targets): resolved by P24: a protected primary is
  never a session's target (§7.4).
- **DIV-7** (the primary alias for other tiles): resolved by the glossary's
  Deployment URL and 05-model §7: the alias is for humans and the tile's own
  principals.

Still open:
- **DIV-8. A tile has no creation stamp to match.** 05-model §3 and P29 say
  the record is ignored unless path, owner ref and creation stamp all match
  the tile it is read for. A tile carries no creation stamp today, so there is
  nothing to compare `created` with. **Fix:** §10.1 verifies `tile` and
  `owner`, keeps `created` for the adopt-or-clear admin act, and relies on
  every creation path removing a leftover record first (05-model §11), which
  covers re-creation by the same owner. If a check on `created` is wanted, the
  tile needs a stamp of its own, written at creation, and the model should say
  where it lives.

## New proposals

Resolved since the previous draft (ids kept, not renumbered):
- **NP-11-1** (two naming rules): resolved by P17.
- **NP-11-3** (`lastLiveReload`, `liveReloadSince`): resolved by 05-model §4.
- **NP-11-4** (review, then deploy with `expect`): resolved by 05-model §5,
  *Reviewed operations*; this document owns the fields (§1.2).
- **NP-11-6** (a git-native deploy log of every attempt): resolved by
  05-model §3 and the glossary's Deploy log.
- **NP-11-8** (event visibility): resolved by 05-model §8, *Delivery is
  filtered* (§3.4).
- **NP-11-9** (non-primary status only as op `status`): resolved by 05-model
  §8 and 12-compat's rule C2.
- **NP-11-10** (run now at terminal level): resolved by 05-model §10.
- **NP-11-11** (resetting `main`'s data is a manager's act): resolved by
  05-model §5 and §10.
- **NP-11-14** (qualified `/api/` URLs for humans and own principals):
  resolved by the glossary and 05-model §7.
- **NP-11-15** (the checkpoint remote's gate): resolved by 05-model §3: the
  view repository, write+ and the tile's own sessions (§1.12).
- **NP-11-16** (opting out keeps the store): resolved by 05-model §2 and the
  glossary's Checkpoint store.
- **NP-11-17** (bx exit codes): resolved: this document owns the `bx`
  contract, and 10-ux adopts it (14-implementation §0.3).
- **NP-11-19** (what readers get): resolved by 05-model §8, *What readers
  see*; the reader view is §1.3's.
- **NP-11-21** (`build-*` keep today's meaning; checkpoint deploys ride op
  `deploy`): resolved by 05-model §8.

Still open:
- **NP-11-2.** The session target's wire (§7.4), under P24's choice:
  - it is stored as a name, or as "follows the primary"; requesting the
    current primary stores the latter;
  - it is requested with a `deployment` query parameter on `/ws/term`,
    `POST /term/sessions` and `…/restart`, beside today's `api` switch;
  - the answer echoes it, and clients must check the echo;
  - protecting the primary, and reassigning it, restart the affected sessions
    with the target chosen again.
- **NP-11-5.** `confirm` tokens (`erase`, `erase-data`, `copy-data`,
  `data-stays`) on the data-bearing routes. In `bx`, `--yes` sends them, and
  the guarded commands of §9.1 need `--yes` without a terminal.
- **NP-11-7.** `bx rollback` is its own command. Deliveries, alwaysOn and
  limits are flags of `bx deployment set`; protection and restart have their
  own subcommands. Destinations are named with `--to`, and positionals are
  right-aligned. The nouns are the glossary's.
- **NP-11-12.** Proxied responses from a non-primary deployment carry
  `X-XBin-Deployment`.
- **NP-11-13.** `GET /deployments` states `features` (`deployments/1`).
  Clients check it before sending anything a later xbind adds, such as `match`
  or feeds, since the bodies are decoded strictly.
- **NP-11-18.** Origins mode: per-deployment labels are name-based (`main`
  keeps today's). The bare URL goes to the primary's origin, and storage
  follows the deployment. Gated by 14-implementation R-6.
- **NP-11-20.** Every POST takes `dryRun: true` and answers an `Impact`
  (code, data, joins, placeholders, pausesLiveReload, stops, affects,
  reloads). The confirmation dialog and bx's report render it, following
  D39's `GET /owner/preview`.
- **NP-11-22.** The echo rule (§8): every `deployment` parameter on an existing
  route comes back in the answer, a field or the `X-XBin-Deployment` response
  header ([12-compat.md](12-compat.md) NP-12-2).
- **NP-11-23.** Restart without new code (§1.6): `restart: true` on `deploy`,
  and deploying the checkpoint a failed, crash-looping or down deployment
  already runs, start a new generation from the kept artifact and clear the
  crash breaker, logged `how: "restart"`. It moves no code, so it is terminal
  level even on a protected primary. `bx deployment restart`. Without it, a
  pinned primary taken down by crashes can't be revived except by shipping new
  code, since saves never reach it ([07-runtime.md](07-runtime.md) §8.7).
- **NP-11-24.** `bx deployment fetch` (§1.12): the portable form of
  `git fetch xbin-deploy`, through today's two injected git settings, for
  sessions opened before the tile had a record. It writes no configuration.
- **NP-11-25.** A tile without a record accepts only the three opt-ins
  (pause live reload, add, protect); every other POST answers 409, and a diff answers 409
  without capturing (§1.2, §1.11). This fixes the choice 05-model §5 leaves to
  this document.
- **NP-11-26.** `--yes` is scoped to the guarded commands (§9.1): data-bearing
  routes, protect, and code moves onto the primary. Routine work on a
  non-primary deployment proceeds after printing its report, so `--yes` keeps
  meaning "the user asked".
- **NP-11-27.** The wire of P22's per-deployment limits (§1.7): `memMiB`,
  `pids` and `diskGiB`, each at most the tile's ceiling (today's
  `XBIN_LIMIT_MEM`, pids cap and `XBIN_LIMIT_DISK`), set by tile managers;
  `diskGiB` only on a tile that roots its scope, since the namespace is the
  scope's.
- **NP-11-28.** Per-deployment backup, archive listing, restore and schedule
  as routes under `/deployments` (§1.8), admin-only in a human session like
  today's backup routes, with `vault: true` on reset, `all: true` on vault
  copy and `stop: true` on seed.
- **NP-11-29.** The `TileKey` spelling of §10: 32 hex digits of a
  domain-separated SHA-256 of the tile path.
- **NP-11-30.** Frame-token minting is deployment-bound for every document
  (§7.2): a tile principal mints only its own bound deployment's token, and a
  frame of a non-primary deployment never mints another tile's token, not even
  by today's navigation rule.
- **NP-11-31.** Diffs are bounded: one running and one waiting per tile, two
  running across xbind, 429 beyond that (§1.11).
- **NP-11-32.** Edges report `values`, `effective` with `why`, and `refused`
  and `clamped` counts (§1.1), so the panel can show why a non-primary
  deployment can't reach a provider: for example the agent tile's sandbox
  managers, reached through a custom role that can't be read-clamped, or
  llm-gw's `writer` completions under the read clamp.
