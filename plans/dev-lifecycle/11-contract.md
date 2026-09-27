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
- edge kinds and their defaults, the read clamp: [09-fabric.md](09-fabric.md);
- UI copy and glyphs: [10-ux.md](10-ux.md);
- compat proofs, downgrade and fixtures: [12-compat.md](12-compat.md).

Code references are against this checkout (master plus `sandbox-visibility`:
D112 landed, D113 designed) and were re-checked for this document. They come
from [research/builder-contract.md](research/builder-contract.md),
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

There is no manifest key, no boot migration and no write at boot.

### 0.2 Two naming rules

Markers follow one of two rules. The rules agree while `main` is the primary,
which is always the case in the zero state and stays so until a tile manager
reassigns the primary (flow F).

| Rule | Markers | Present when | Why |
|---|---|---|---|
| **Role rule**: absent means the primary | qualified `component` plus `deployment` on `reload` and `build-*` (§3); `X-XBin-Deployment` (§4); `XBIN_DEPLOYMENT` (§5); `<meta name="xbin-deployment">` and `xbin.deployment` (§6); whoami `deployment` (§7.7) | the deployment concerned is not the primary | They tell old clients, callees and tile code "this is not what the bare URL serves". Old clients keep treating the primary as the tile. |
| **Name rule**: absent means `main` | the frame-token claim (§7.2); tile-origin labels, tickets and cookies (§2.6, §7.5); `/sandboxes` ids (§8); every storage key ([08-data.md](08-data.md)) | the deployment is not `main` | They bind a credential or stored state to one deployment. They must not change meaning when routing moves (P6, P12). |

Consequences:
- A primary reassignment restarts (blue/green) the running backends of the old
  and the new primary, so `XBIN_DEPLOYMENT` stays true for the whole life of
  every backend process (NP-11-1; mechanics in [07-runtime.md](07-runtime.md)).
- Descriptive fields on admin and runtime views (`deployment` on `/backends`,
  `/runtime` and `/sandboxes` rows) follow neither rule. They are present on
  every row of a tile that has a record, and absent otherwise.

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
  The qualified string appears in JSON only as an event's `component` (§3).
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
  (403). A read of `/logs` or `/tile-status` may name any deployment of it.
- **DR2, named.** A call that names a tile acts on its **primary**, unless it
  names a deployment (a `deployment` parameter or a qualified URL). Naming a
  non-primary deployment needs write (humans) or a matching binding (the
  tile's own principals). Other tiles' principals never reach one (P7).
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
- **Tile managers** (D24/D33): `IsAdmin(p) || mayManageTile(p, tile)`
  (`internal/broker/orgsapi.go:427-443`). The gate is human-only by
  construction: `humanID` is `""` for every element principal
  (`internal/broker/orgsapi.go:73-78`). No terminal, agent, frame or instance
  token is ever a tile manager, whoever drives it.
- **The tile's own principals**: element principals whose `Component` is the
  tile (frame tokens, instance tokens, terminal and agent tokens, the tile-origin
  cookie). Each is bound to one deployment (§7.1).
- **View-as** (D64): every POST in this document is refused with 403 by the
  existing middleware (`internal/server/server.go:229`,
  `internal/server/impersonate.go:23-29`). GETs answer as for the viewed user.

### 0.6 Wire hygiene

- **New routes, not new fields on existing bodies.** `server.DecodeJSON`
  rejects unknown fields (`internal/server/api.go:71-76`), so an added body
  field would 400 on an older server and would break lagging clients.
  Existing routes gain only query parameters and response fields. Where an
  older server would silently ignore a new parameter, the answer echoes it and
  clients check the echo (§7.4).
- **Errors** go through `server.WriteError`: `{"error": msg, "docs": page}`
  (`internal/server/api.go:53-59`, D60). New handlers never use `http.Error`,
  as `/backends` still does (`internal/boot/api.go:21-27`).
- **Audit:** every POST under `/deployments` is audited by the existing rule
  (`internal/server/server.go:648-667`).
- **Encoding:** JSON everywhere except the diff (text/x-diff) and the git
  remote (git's files). Times are RFC 3339 UTC strings, as in `/term/sessions`.
  `by` is `user:<id>` or `owner`, as `Principal.From` spells humans
  (`internal/auth/auth.go:196-207`).

## 1. HTTP API

### 1.1 Types

```jsonc
// State: the answer of GET /deployments and of every POST /deployments/*
{
  "tile": "apps/crm",
  "selected": "dev",                  // present when the request's tile ref was qualified
  "record": true,                     // false = the zero state: the rest is synthesized, nothing is written
  "schema": 1,                        // the record's schema number (§10.1); 0 in the zero state
  "seq": 18,                          // the record's sequence number; 0 in the zero state
  "features": ["deployments/1"],      // what this xbind speaks (NP-11-13)
  "owner": "org:devs",                // the tile's owner (D24), as /components reports it: refusal texts name its managers
  "primary": "main",
  "liveReload": "dev",                // the live reload target; "" = paused
  "lastLiveReload": "dev",            // where resume and reload now go (NP-11-3)
  "liveReloadSince": {"at": "…", "by": "user:ana"},   // the last attach, pause or resume ("paused by ana 12m ago")
  "workTree": {"changed": 3, "since": "c:3f2a1c9"},  // while paused: files that differ from lastLiveReload's checkpoint
  "protectedPrimary": false,
  "allowed": {"pause": {"ok": true}, "deployments": {"ok": false, "why": "…", "kind": "policy"}},  // what the TILE may do
  "limits": {"tile": 4, "tileUsed": 1, "workspace": 32, "workspaceUsed": 7},  // non-primary deployments; numbers: 07-runtime.md
  "deployments": [ /* Deployment */ ],   // "main" first, then by creation
  "edges": [ /* Edge */ ],             // writers only
  "caller": { /* Caller */ }
}

// Can: one permission, computed by the policy that will judge the request
{"ok": false, "why": "tile managers only: ask an admin of org devs, or a workspace admin", "kind": "authority"}
//  kind (with ok:false): authority (level, tile manager, protected primary, view-as) | policy (the tile or this
//  xbind can't: chrome, xbin:* capability, cgi, no isolation, a cap) | state (not now: not paused, already primary, …)

// Deployment
{
  "name": "dev",
  "primary": false,
  "liveReload": true,                 // follows the work tree
  "checkpoint": null,                 // a Checkpoint, or null exactly when liveReload is true
  "status": {
    "state": "healthy",               // idle | building | healthy | failed | static (no backend)
    "gen": 3,
    "serving": "work-tree",           // what the current generation runs: "work-tree" or a checkpoint id (07-runtime.md §8.2)
    "error": "…",                     // writers only: the last build/start/health failure, at most 500 bytes (full text: GET /logs)
    "deploying": {"id": 43, "how": "deploy", "result": "running", "phase": "build"},  // the deploy in flight, if any
    "queued": [{"id": 44, "how": "deploy"}]                                             // FIFO (§1.2)
  },
  "url": "/c/apps/crm+dev/",          // the primary's is the bare "/c/apps/crm/"
  "api": "/api/apps/crm+dev/",        // absent for a tile without a backend
  "origin": "https://t-….example.com",   // --tile-assets=origins only (§2.6)
  "data": {"state": "seeded", "from": "main", "at": "…", "by": "user:ana", "reset": false, "busy": "seeding"},
                                      // state: original (main: today's keys) | empty | seeded; at, by: when that began;
                                      // reset: empty because of a reset; busy: seeding | resetting, while it runs
  "vault": {"keys": 4, "placeholders": 1},   // writers only; placeholders: key names with no value yet (P14)
  "deliveries": false,                // the primary: always true, not a switch
  "alwaysOn": false,                  // the primary: the manifest's value, not a switch
  "alwaysOnDeclared": true,           // this deployment's code declares "alwaysOn"
  "registrations": [ /* Registration */ ],   // writers only
  "wouldNotify": [{"at": "…", "to": "user:bob", "title": "…"}],   // writers only; non-primary: the last 20 suppressed notifications
  "lastDeploy": {"id": 41, "how": "promote", "at": "…", "by": "user:ana", "result": "ok"},
  "created": "…", "by": "user:ana",
  "can": {"open": Can, "deploy": Can, "promoteTo": Can, "rollback": Can, "attach": Can, "remove": Can,
          "reset": Can, "seed": Can, "vaultCopy": Can, "deliveries": Can, "alwaysOn": Can,
          "runNow": Can, "primary": Can}
}

// Checkpoint
{"id": "c:3f2a1c9", "hash": "<full tree object id>", "feed": "work-tree", "at": "…", "by": "user:ana"}

// Registration: a cron job, bus push subscription, interface instance or ingress host of ONE deployment
{"kind": "cron", "name": "nightly", "schedule": "0 3 * * *", "path": "/tick", "dormant": true}
{"kind": "bus", "name": "trig-12", "resource": "res:apps/crm/events", "prefix": "orders/", "path": "/trigger/bus/12", "dormant": true}
{"kind": "iface-instance", "name": "acme", "prefix": "/t/acme", "dormant": true}
{"kind": "ingress-host", "name": "crm.example.com", "dormant": true}

// Edge: one outbound edge of the tile, with its policy for the non-primary deployments
{"id": "slot:llm", "kind": "http", "to": "apps/llm-gw", "role": "reader", "policy": "read", "default": "read", "set": false}
//  id: "slot:<slot>" (an interface binding, the net slot included) | "grant:<target>" (a call grant, a res:, code or cap: grant)
//  kind, to, role: descriptive. policy: read | block | inherit, as 09-fabric.md §5.1 allows per kind. set: an override is stored.

// Caller: what the requesting principal may do at tile level (per deployment: Deployment.can)
{"level": "terminal", "manager": false, "readOnly": false, "bound": "dev",
 "can": {"pause": Can, "resume": Can, "reloadNow": Can, "add": Can, "edges": Can, "protect": Can}}
//  level: read | write | terminal | admin | tile (a frame or instance token of the tile: no operation rights)
//  bound: present for the tile's own principals (§7.1)

// DeployEntry: one deploy attempt, queued, in flight or in the deploy log
{"id": 42, "deployment": "main", "how": "promote", "from": "dev",
 "checkpoint": "c:3f2a1c9", "previous": "c:77aa01b", "followsWorkTree": false, "feed": "work-tree",
 "by": "user:ana", "via": "session", "agent": false, "session": "<session id>",
 "requestedAt": "…", "finishedAt": "…", "result": "ok", "phase": "swap", "error": "…"}
//  how: deploy | promote | rollback | reload-now | resume | pause | attach | add | protect
//  result: queued | running | ok | failed | cancelled; phase, while running: checkpoint | materialize | build | start | swap
//  followsWorkTree: resume and attach; the checkpoint is then the capture taken at that moment
//  via: the principal's Via, verbatim; agent and session: a terminal or agent session token acted
//  error: failed only, at most 500 bytes, writers only; the full text is in the deployment's log (GET /logs)

// Impact: the answer of a dry run (§1.2), what the confirmation dialog and bx's prompt render
{"code": {"deployment": "main", "from": "c:3f2a1c9", "to": "c:7b19e02", "files": 3, "added": 40, "removed": 12,
          "workTreeAt": "…"},                // workTreeAt: the capture time when the code comes from the work tree
 "data": "none",                            // none | seed | erase: what happens to deployment data
 "pausesLiveReload": false,
 "stops": [],                               // deployments that stop (remove, seed)
 "affects": "everyone",                     // everyone (the primary's viewers) | deployment (one non-primary's) | nobody
 "reloads": ["apps/crm"]}                   // event components whose frames reload once
```

### 1.2 Conventions of the `/deployments` family

- **Base path** `/api/xbin/deployments`. It is reachable over the console, the
  gateway socket and tile origins, like every `/api/xbin` route
  (`internal/server/server.go:563-584`).
- **Bodies** are JSON, decoded strictly. Every body has `tile`, a tile ref. An
  operation on one deployment takes it from `deployment` or from the ref's
  qualifier; both given and different is a 400.
- **`seq`** is optional on every POST: the record sequence the caller acted
  on. A moved record answers 409, so a confirmation dialog is exact.
- **`confirm`** tokens guard data (NP-11-5). A missing or wrong token answers
  400 and names the token.

  | Route | Token |
  |---|---|
  | `remove` | `"erase"` |
  | `reset` | `"erase-data"` |
  | `seed`, and `add` with `data:"seed"` | `"copy-data"` |
  | `primary` | `"data-stays"` |

- **`expect`** on `deploy` and `promote` (NP-11-4) is the checkpoint the caller
  reviewed, typically the `X-XBin-Checkpoint-To` of a diff (§1.11) or the
  `impact.code.to` of a dry run. If the code that would move is different, the
  answer is 409.
- **`dryRun: true`** is accepted by every POST (NP-11-20). The request is
  judged exactly as for real, including refusals, and nothing changes. The
  answer is `200 {"state": State, "impact": Impact}`. A dry run that needs the
  work tree takes its checkpoint (content-addressed and collectable), so
  `impact.code.to` can be sent back as `expect`. This follows the D39
  `GET /owner/preview` precedent.
- **Answer:** `200 {"state": State, "deploy"?: DeployEntry, …}` once the
  record change is committed. A request that moves no code, because the
  deployment already runs that checkpoint, answers `"unchanged": true` and no
  `deploy`.
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
200        State
authority  everyone who can read the tile, admins, the tile's own principals (any binding).
           Readers get the State without the fields marked "writers only" in §1.1 (NP-11-19).
errors     400 need ?tile= · 403 can't read the tile · 404 no such tile, or no such deployment (qualified ref)
           · 409 the record's schema is newer than this xbind (§10.1)
notes      The zero state answers record:false and writes nothing (P5). A plain 404 or 405 (Go's mux,
           not JSON) means an older xbind: that is how clients detect the feature (12-compat.md NP-12-2).
```

### 1.4 Live reload

```
POST /api/xbin/deployments/live-reload/pause
body       {"tile": "apps/crm", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}              how: "pause"
effect     liveReload = ""; lastLiveReload = X, the former target. X is pinned to a fresh checkpoint of the
           work tree: normally a code-identical swap, or the newest work tree once if it moved (model §5).
authority  terminal level. With X the primary this pins the primary: parity (P4).
idempotent yes: already paused answers 200 with no deploy
errors     403 not terminal level · 409 this tile can't pause (cgi; a backend tile without --isolate, P18)
           · 409 stale seq
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
           (use attach) · 409 Y is a protected primary (P21) · 409 stale seq
```

```
POST /api/xbin/deployments/live-reload/now
body       {"tile": "apps/crm", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}              how: "reload-now"
effect     Checkpoint the work tree and deploy it once to lastLiveReload, which stays pinned. A work tree
           identical to its checkpoint answers unchanged:true.
authority  terminal level. With lastLiveReload the primary: parity, but a protected primary takes a tile
           manager, never via a terminal or agent token.
errors     403 not terminal level · 403 protected primary · 409 live reload isn't paused · 409 stale seq
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
           primary · 409 stale seq
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
200        {"state": State, "deploy": DeployEntry}         how: "add" (and "attach" entries when attach:true)
effect     Creates the deployment with its code, an empty or seeded data namespace, a vault holding the
           primary's key names as placeholders, deliveries and alwaysOn off (model §5).
authority  terminal level; data:"seed" is a tile manager's act
errors     400 bad name · 400 missing confirm · 403 · 404 unknown checkpoint
           · 409 "main" always exists · 409 the name is taken · 409 a tile exists at <tile>+<name>
           · 409 a cap is reached · 409 the tile can't have deployments (chrome, xbin:* capability, cgi; P19)
           · 409 a backend tile without --isolate (P18) · 409 stale seq
```

```
POST /api/xbin/deployments/remove
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "erase", "seq"?: 18}
200        {"state": State}
effect     Stops Y. Deletes its data namespace, vault, logs, registrations and built artifacts. Detaches
           live reload if Y held it; lastLiveReload then becomes the primary. Checkpoints stay until GC
           (08-data.md).
authority  terminal level
errors     400 missing confirm · 403 · 404 · 409 Y is main · 409 Y is the primary · 409 stale seq
```

### 1.6 Moving code

```
POST /api/xbin/deployments/deploy
body       {"tile": "apps/crm", "deployment": "dev",
            "checkpoint"?: "c:<id>",      absent: a fresh checkpoint of the work tree
            "expect"?: "c:<id>",          only without checkpoint: the capture must equal it
            "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}         how: "deploy"
effect     Puts the checkpoint on X. X was the live reload target: live reload pauses (lastLiveReload = X).
authority  terminal level. Onto the primary: parity; a protected primary takes a tile manager, never via
           a terminal or agent token (P21).
errors     400 checkpoint and expect together · 403 · 403 protected primary · 404 · 409 expect mismatch
           · 409 ambiguous checkpoint id · 409 isolation (P18) · 409 deploy queue full · 409 stale seq
```

```
POST /api/xbin/deployments/promote
body       {"tile": "apps/crm", "from": "dev", "to": "main", "expect"?: "c:<id>", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}         how: "promote", from: "dev"
effect     B receives A's current code: A's checkpoint, or a fresh checkpoint of the work tree when A follows
           it. Code only (P10). B was the live reload target: live reload pauses.
authority  as deploy, for the target B
errors     403 · 404 · 409 from = to · 409 expect mismatch · 409 stale seq
```

```
POST /api/xbin/deployments/rollback
body       {"tile": "apps/crm", "deployment": "main", "checkpoint"?: "c:<id>", "seq"?: 18}
           absent checkpoint: the newest "ok" entry in X's deploy log whose checkpoint differs from X's current one
200        {"state": State, "deploy"?: DeployEntry}         how: "rollback"
effect     Deploys C to X. X was the live reload target: live reload pauses. Data stays (flow D).
authority  as deploy
errors     403 · 404 unknown checkpoint · 409 nothing earlier in the deploy log · 409 stale seq
```

### 1.7 Governance

```
POST /api/xbin/deployments/primary
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "data-stays", "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}
effect     Routing moves atomically (model §7). Data does not move. The old primary's registrations become
           dormant and Y's activate. The running backends of both restart so XBIN_DEPLOYMENT stays true
           (§0.2). With the primary protected and live reload on Y, Y is pinned in place (how "protect").
authority  tile manager
idempotent yes: Y already primary answers 200 and changes nothing
errors     400 missing confirm · 403 · 404 · 409 Y isn't healthy · 409 stale seq
```

```
POST /api/xbin/deployments/protect
body       {"tile": "apps/crm", "on": true, "seq"?: 18}
200        {"state": State, "deploy"?: DeployEntry}         turning it on while live reload is on the primary
                                                            pins the primary in place: how "protect"
authority  tile manager
idempotent yes
```

```
POST /api/xbin/deployments/edge
body       {"tile": "apps/crm", "edge": "grant:apps/calendar",
            "policy": "read" | "block" | "inherit" | "default", "seq"?: 18}
           The values each edge kind takes are 09-fabric.md §5.1's (inherit: the net slot and capability
           grants). "default" removes the override. match is not accepted in v1: it arrives with a feature
           string (NP-11-13).
200        {"state": State}
authority  tile manager
idempotent yes
errors     400 a policy this edge's kind doesn't take · 403 · 404 no such edge on the tile
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

### 1.8 Data

```
POST /api/xbin/deployments/seed
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "copy-data", "seq"?: 18}
POST /api/xbin/deployments/reset
body       {"tile": "apps/crm", "deployment": "dev", "confirm": "erase-data", "seq"?: 18}
200        {"state": State}          Y.data.busy is "seeding" or "resetting" until done; completion
                                      is a deployments op:"data" event (§3.3)
effect     seed: Y stops, the primary's deployment data is copied into Y's namespace consistently (08-data.md),
           data.state becomes "seeded". reset: Y's namespace is emptied, data.state becomes "empty" with
           reset: true.
authority  seed: tile manager. reset: terminal level, except resetting main, which is a tile
           manager's act (NP-11-11).
errors     400 missing confirm · 403 · 404 · 409 Y is the primary · 409 data busy · 409 stale seq
```

```
POST /api/xbin/deployments/vault-copy
body       {"tile": "apps/crm", "deployment": "dev", "keys": ["STRIPE_KEY"], "seq"?: 18}
200        {"state": State, "copied": ["STRIPE_KEY"], "missing": []}
authority  tile manager
idempotent yes
errors     403 · 404 · 409 Y is the primary · the vault routes' answer while the vault is sealed
```

### 1.9 Run now

```
POST /api/xbin/deployments/run-now
body       {"tile": "apps/crm", "deployment": "dev", "job": "nightly"}
200        {"state": State, "delivery": {"status": 200, "ms": 812}}   status: what the handler answered
effect     Delivers Y's cron job once to Y, as xbin/cron with the job's role, dormant or not, through the
           same dispatch a tick uses. Waits for the handler, at most 2 minutes.
authority  terminal level (NP-11-10)
errors     403 · 404 no such deployment or job · 409 Y is the primary (its jobs fire on schedule)
           · 502 Y's backend can't start (the proxy's shape, with detail)
```

### 1.10 The deploy log

```
GET /api/xbin/deployments/log?tile=<tile-ref>&deployment=<name>&limit=<n>&before=<id>
200        {"tile": "apps/crm", "entries": [DeployEntry], "more": true}
           Newest first. Without deployment: every deployment's entries, by id. limit: default 50, max 200.
GET /api/xbin/deployments/log?tile=<tile-ref>&id=<id>&wait=<seconds>
200        {"entry": DeployEntry}
           One attempt: queued and in-flight ones from memory, finished ones from the store. wait (at most 25)
           holds the answer until result leaves queued/running, or the wait runs out.
authority  humans with write on the tile, admins, the tile's own principals
errors     403 · 404 no such tile, deployment or id
```

The log holds every finished attempt, failed ones included (NP-11-6). It is
kept in the checkpoint store (§10.3). Queued and in-flight entries live in
memory and are lost with an xbind restart; the deployment then keeps its
previous code.

### 1.11 Diff

```
GET /api/xbin/deployments/diff?tile=<tile-ref>&from=<spec>&to=<spec>&path=<file>&stat=1
spec       c:<id> | deployment:<name> | work-tree
           defaults: from = deployment:<primary>, to = work-tree. A deployment that follows the work tree
           means work-tree. work-tree captures a checkpoint (content-addressed, collectable by GC).
200        text/x-diff: a git patch (renames detected, binary files named, not inlined)
           X-XBin-Checkpoint-From: c:<id>     the resolved checkpoints (for work-tree: the capture)
           X-XBin-Checkpoint-To:   c:<id>
           X-Truncated: true                  the patch was cut at 16 MiB
200 stat=1 {"from": "c:…", "to": "c:…", "files": [{"path", "status": "A|M|D|R|T", "added", "removed",
            "binary"?}], "truncated": false}    at most 5000 files
authority  humans with write on the tile, admins, the tile's own principals
errors     400 bad spec, or path not a clean tile-relative path · 403 · 404 · 504 took longer than 30 s
           (narrow it with path or stat)
```

Every diff and capture runs in confine (D78, P16). At most one diff runs per
tile and two across xbind; later requests wait. That is the precedent of
`GET /term/sessions/{id}/diff` (`internal/server/openapi.go:145`).

### 1.12 The checkpoint remote

```
GET /api/xbin/checkpoints/<tile>.git/<git path>          route pattern: GET /checkpoints/{rest...}
git path   HEAD | info/refs | objects/info/packs | objects/pack/pack-<hex>.(pack|idx) | objects/<2 hex>/<38–62 hex>
200        the file, served from data/checkpoints/<CompKey>.git (§10.3); everything else 404
           (config, objects/info/alternates and http-alternates, the store's other files)
authority  the tile's own principals, humans with write on the tile, admins, and code[:<tile>] grants
           (CM-3's code-read rule). NP-11-15.
```

- The route is **read-only dumb HTTP**, like the template remote
  (`internal/broker/templates.go:30`,
  `internal/broker/templaterepo.go:214-227`). Nothing is ever pushed here.
  `?service=` is ignored, so git falls back to the dumb protocol.
- **Splitting `<tile>.git`:** the handler takes the last segment ending in
  `.git` whose prefix is a registered component with a store. A tile path may
  itself contain `.git`-suffixed segments.
- **Terminal injection:** sessions opened while the tile has a record get
  two more `GIT_CONFIG_*` pairs after today's two (`internal/term/term.go:787-792`).
  `GIT_CONFIG_COUNT` becomes 4:

  ```
  GIT_CONFIG_KEY_2=remote.xbin-deploy.url     GIT_CONFIG_VALUE_2=http://xbin/api/xbin/checkpoints/<tile>.git
  GIT_CONFIG_KEY_3=remote.xbin-deploy.fetch   GIT_CONFIG_VALUE_3=+refs/heads/deploy/*:refs/deploy/*
  ```

  The existing `insteadOf` rewrite and bearer header carry the fetch. After
  `git fetch xbin-deploy`, `deploy/<name>` resolves in the tile's repository,
  so flow H's `git checkout -b hotfix deploy/main` works verbatim. Zero-state
  sessions keep today's two pairs.

### 1.13 Every operation, one route, one command

| Operation (model §5) | Route | `bx` |
|---|---|---|
| Pause live reload | `POST /deployments/live-reload/pause` | `bx live-reload pause [<tile>]` |
| Resume live reload | `POST /deployments/live-reload/resume` | `bx live-reload resume [<tile>] [--to <name>]` |
| Reload now | `POST /deployments/live-reload/now` | `bx live-reload now [<tile>]` |
| Attach live reload to Y | `POST /deployments/live-reload/attach` | `bx live-reload attach [<tile>] --to <name>` |
| Add deployment Y | `POST /deployments/add` | `bx deployment add [<tile>] <name>` |
| Deploy to X | `POST /deployments/deploy` | `bx deploy [<tile>] --to <name>` |
| Promote A → B | `POST /deployments/promote` | `bx promote [<tile>] <a> <b>` |
| Roll back X to C | `POST /deployments/rollback` | `bx rollback [<tile>] --to <name> [--checkpoint c:<id>]` |
| Remove deployment Y | `POST /deployments/remove` | `bx deployment rm [<tile>] <name>` |
| Reassign primary to Y | `POST /deployments/primary` | `bx deployment primary [<tile>] --to <name>` |
| Protect / unprotect primary | `POST /deployments/protect` | `bx deployment protect [<tile>] on\|off` |
| Seed Y | `POST /deployments/seed` | `bx deployment seed [<tile>] <name>` |
| Reset Y | `POST /deployments/reset` | `bx deployment reset [<tile>] <name>` |
| Vault copy to Y | `POST /deployments/vault-copy` | `bx deployment vault-copy [<tile>] <name> --keys <k1,k2>` |
| Deliveries on/off for Y | `POST /deployments/deliveries` | `bx deployment set [<tile>] <name> --deliveries on\|off` |
| alwaysOn on/off for Y | `POST /deployments/always-on` | `bx deployment set [<tile>] <name> --always-on on\|off` |
| Set edge policy | `POST /deployments/edge` | `bx deployment edge [<tile>] <edge> read\|block\|inherit\|default` |
| Run now | `POST /deployments/run-now` | `bx deployment run-now [<tile>] <name> <job>` |
| (read) state | `GET /deployments` | `bx deployment ls [<tile>]` |
| (read) deploy log | `GET /deployments/log` | `bx deployment log [<tile>] [<name>]` |
| (read) diff | `GET /deployments/diff` | `bx deployment diff [<tile>] [<from> [<to>]]` |
| (read) checkpoints over git | `GET /checkpoints/{rest...}` | `git fetch xbin-deploy` |

The routes are listed without the `/api/xbin` prefix, as protocol.md's API
fence writes them.

### 1.14 Error catalogue

Implementations use these texts. They may append detail after ` — `. `docs`
is `/docs/auth.md` for 403 and `/docs/protocol.md` otherwise, as the proxy
does (`internal/proxy/proxy.go:307-317`).

| Condition | Status | `error` |
|---|---|---|
| unreadable body, unknown field | 400 | `bad request body: <decoder error>` |
| bad deployment name | 400 | `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters` |
| missing `confirm` | 400 | `<consequence>: send confirm:"<token>" to proceed` |
| unknown tile | 404 | `no such tile: <tile>` |
| unknown deployment | 404 | `<tile> has no deployment "<name>"` |
| unknown checkpoint | 404 | `<tile> has no checkpoint <id>` |
| unknown edge | 404 | `<tile> has no edge <id>` |
| needs write | 403 | `deployments of <tile> need write access` |
| needs terminal level | 403 | `<operation> needs terminal-level access on <tile>` |
| needs a tile manager | 403 | `<operation> is a tile manager's act: the tile's owner, its org's admins, or a workspace admin` |
| protected primary | 403 | `the primary of <tile> (<name>) is protected: only tile managers change its code, and not from a terminal or agent session` |
| cross-deployment self write | 403 | `a tile's own credentials act only on their own deployment (<bound>)` |
| name taken, `main` | 409 | `<tile> already has a deployment "<name>"` |
| component at `<tile>+<name>` | 409 | `a tile exists at <tile>+<name>; pick another name` |
| cap | 409 | `<tile> has <n> non-primary deployments, the most allowed here` |
| tile can't have deployments | 409 | `<tile> can't have non-primary deployments: <reason>` |
| isolation | 409 | `pinning a backend to a checkpoint needs isolation (--isolate)` |
| cgi | 409 | `cgi backends run on the host: they can't be pinned or have non-primary deployments` |
| not paused | 409 | `live reload is attached to <name>; <operation> works while it is paused` |
| paused | 409 | `live reload is paused: resume it onto <name> instead` |
| primary or main operand | 409 | `<name> is the primary of <tile>` / `main can't be removed` |
| unhealthy | 409 | `<name> isn't healthy (<state>); only a healthy deployment can become the primary` |
| stale `seq` | 409 | `the deployments of <tile> changed (seq <n>); reload and retry` |
| `expect` mismatch | 409 | `the code changed since you reviewed <expect> (now <actual>)` |
| nothing to roll back to | 409 | `<name> has no earlier checkpoint in its deploy log` |
| ambiguous id | 409 | `checkpoint id <id> is ambiguous in <tile>; use more digits` |
| data busy | 409 | `<name>'s data is being <seeded\|reset>` |
| deploy queue full | 409 | `<name> already has 8 deploys waiting; try again when one finishes` |
| newer record | 409 | `<tile>'s deployment record was written by a newer xbind (schema <n>)` |

### 1.15 Registration obligations

Every route above is three edits in one commit, enforced by
`TestRouteInventory` (`internal/apicheck/apicheck_test.go:244`):

1. A `RegisterAPI("METHOD /path")` literal where the fixture mounts it. That is
   `internal/boot/<x>.go`, read by `mainRoutes` (`apicheck_test.go:153`), or a
   plane registered from `broker.Register`, mounted by `mount`
   (`apicheck_test.go:122`).
2. An `ep` row in `endpoints()` (`internal/server/openapi.go:19-29`, `:83`)
   with these capability strings:
   - `read on the tile, or the tile itself (readers get less)` for
     `GET /deployments`;
   - `write on the tile, or the tile itself` for the log and the diff;
   - `terminal-level on the tile (its own terminal/agent tokens count)` for the
     live-reload, add, remove, deploy, promote, rollback, reset and run-now
     routes;
   - `tile manager (the tile's owner, its org's admins, a workspace admin)` for
     primary, protect, edge, deliveries, always-on, seed and vault-copy;
   - `the tile itself, write on the tile, or code[:<tile>]` for the checkpoint
     remote.

   The capability list in `apiInfo` (`openapi.go:66-79`) gains the new
   phrases.
3. A column-0 row inside the API fence of `docs/protocol.md` (`:423`).
   Optional queries are written `?x=<y>`, never `[?x]`.

## 2. Qualified URL routing

### 2.1 Grammar

The deployment URL is `/c/<tile>+<name>/…` and `/api/<tile>+<name>/…`
(glossary). The qualifier sits **inside the last segment of the tile path**,
never in a segment of its own. A separate segment would read as a tile
sub-path, a nested tile or an `xbin.window` path, and would trip prefix-based
frame reloads ([research/terminology-census.md](research/terminology-census.md)).
`+` is reserved in new tile names the D82 way:
- non-admin creation refuses `+` beside `:` (`internal/broker/policy.go:162-166`);
- an existing component always wins resolution (below);
- `add` refuses a name when a component exists at `<tile>+<name>`.

### 2.2 Resolution

One resolver serves `/c/`, `/api/`, the asset-token plane, tile origins and the
`/deployments` routes' `tile` field. It runs before today's per-plane logic:
before `owningComponent` in the static handler (`internal/server/static.go:105`)
and in place of `Reg.Resolve` in the proxy (`internal/proxy/proxy.go:130`).

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
			      // full candidate "<tile>+<name>" (the D82 way; 12-compat.md NP-12-1). Lstat only.
		}
		name := segs[i][j+1:]
		if !HasDeployment(tile, name) {
			return nil, "", true, "", ErrNoDeployment // 404 `<tile> has no deployment "<name>"`
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
  for humans (§2.3).
- The pinned or live reload root that `dep` names comes from the record.
  Opening files beneath it is [07-runtime.md](07-runtime.md)'s job. `pathAllowed`
  (`internal/server/static.go:347-361`) still applies to `rest`.
- An unknown name on a tile with a record answers 404, instead of falling back
  to a shorter prefix that would serve `crm+dev/…` as a file of a parent tile.
- Every URL that resolves today resolves identically: a zero-state tile never
  splits, and a component or on-disk path at the full candidate always wins.
  A `+` in a query string is never the qualifier.

### 2.3 Who may use which URL

| Caller | bare `/c/<T>/…` | `/c/<T>+<N>/…`, N non-primary |
|---|---|---|
| humans with read | the primary, as today | 403 `deployment URLs need write access on <T>` |
| humans with write or terminal, admins | the primary | N |
| the tile's own principals | the primary (static reads as today) | N; a document's frame token only if bound to N (§7.2) |
| other tiles' principals | as today | 403 |
| credential-less legacy subresource load | as today | N, by the legacy rule (§2.6) |

| Caller | bare `/api/<T>/…` | `/api/<T>+<primary>/…` | `/api/<T>+<N>/…`, N non-primary |
|---|---|---|---|
| humans | the primary (policy as today) | as bare | write or terminal: N; read: 403 |
| the tile's own principal bound to B | B, a self-call (P12) | B = primary: the primary; else 403 | B = N: N; else 403 |
| other tiles' principals | the primary; the caller's edge policy applies on the caller side (09-fabric.md) | 403 (NP-11-14) | 403 |
| cron and bus deliveries | the delivering registration's deployment | — | — |
| ingress | the primary (unchanged) | — | — |

- Bare `/c/` always means the primary, whoever asks. Only `/api/` has the
  self-call rule.
- A dev document's absolute `/c/<self>/…` URLs in markup load the primary's
  files. Relative URLs, the documented portable form, stay inside the
  deployment. Module imports are remapped (§2.6).
- The call's role and headers are the bare URL's: the qualified form adds only
  the gates above. Lifecycle is the tile's: 409 with `X-XBin-Lifecycle`
  exactly as today (`internal/proxy/proxy.go:148-158`).

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
  `/c/<tile>+<name>/`. With the qualified path as `src`, three things work
  unchanged:
  - `xbin.window` spawns: bx-frame dispatches `from: this.src`
    (`web/bx-frame.js:503-507`) and even an old scaffold shell builds
    `from + '/' + spec.path` (`workspace-template/shell/bx-shell.js:677`).
    The window resolves inside the deployment.
  - Reload targeting: `isReloadTarget` compares `src`
    (`web/events-socket.js:40-49`).
  - Build overlays: bx-frame's `e.component === this.src`
    (`web/bx-frame.js:366-395`).
- Tile-level events (`grants`, `pr`, `term`, `deployments`) keep the bare
  component. A client showing a deployment frame compares the tile part of its
  `src`.
- `frame-info.js` resolves a qualified `src` the way §2.2 does: an exact
  component in `/components` wins, else split the last `+`
  (`web/frame-info.js:55-62`). It mints bootstrap tokens with
  `?component=<tile>&deployment=<name>` (§8).

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
    each deployment's origin.

### 2.7 Native runtime document

- `/c/<T>+<N>/?native=1` is generated from N's code: N's manifest `native`
  entry and files. It is served to the same principals as N's documents.
- The shipped app only ever requests bare URLs (`/c/<T>/`,
  `/c/<T>/?native=1`), so it always gets the primary (compat rule 10).

## 3. Events

### 3.1 The primary: unchanged

Every event about the primary keeps today's shape and bare `component`
(`internal/events/events.go:11-17`, `docs/protocol.md:2259-2313`). That holds
whether the primary is `main` or not.

### 3.2 Non-primary deployments on the existing types

`events.Event` gains ``Deployment string `json:"deployment,omitempty"` ``. The
field is present exactly when `component` is qualified.

```jsonc
{"type":"reload","component":"apps/crm+dev","deployment":"dev"}
{"type":"build-start","component":"apps/crm+dev","deployment":"dev"}
{"type":"build-error","component":"apps/crm+dev","deployment":"dev","text":"compiler output"}
{"type":"build-ok","component":"apps/crm+dev","deployment":"dev"}
{"type":"bus","topic":"res:apps/crm/events/orders","deployment":"dev","data":…}   // a non-primary namespace (08-data.md)
```

- Old consumers match `component` exactly, or as a prefix followed by `/`, so
  they ignore these:
  - bx-frame (`web/bx-frame.js:366-395`);
  - `isReloadTarget` (`web/events-socket.js:40-49`);
  - the iOS app's reload targeting (D99).
- An old shell refetches `/components` on any `reload`
  (`workspace-template/shell/bx-shell.js:194-196`). That is harmless and
  happens today on every save of any tile.
- The build-start watcher clears the status stored under the event's own
  component (`internal/obs/status.go:129-148`), so a non-primary build never
  clears the primary's. Non-primary statuses live in the deployments plane
  (below), and the watcher must never publish a `status` event for a
  qualified component.
- **`build-*` keep today's meaning:** builds of the work tree, and restarts of
  the code a deployment already runs. That covers every live-reload rebuild,
  resume or attach onto a deployment, and every crash or idle restart, pinned
  deployments included. A deploy that puts a **checkpoint** on a deployment
  reports only through `deployments` op `deploy` (§3.3): deploy, promote, roll
  back, reload now, and the pin of pause, attach and protect. If it fails, the
  previous code keeps serving and no frame overlay is painted. When it swaps,
  the status plane clears that deployment's reported status, as `build-start`
  does for a rebuild (NP-11-21).
- **No `status` event is ever published for a non-primary deployment.** Old
  shells process `status` for any component: a transient one becomes a toast,
  and any stored one puts 🔴 or 🟡 in every user's page title
  (`workspace-template/shell/bx-shell.js:1257-1266`, `:1278-1283`). Non-primary
  status rides `deployments` (§3.3). `GET /tile-report` never lists it.

### 3.3 The `deployments` type

A tile-level type. `component` is always the bare tile path, and deployments
are named inside `data`. Old clients ignore unknown types: bx-frame's switch
has no default, and the app maps unknown types to `.other` (compat rule 10).

```jsonc
{"type":"deployments","component":"apps/crm","data":{"op":"record","seq":19,"by":"user:ana","session":"<id>",
  "what":["liveReload","primary","protectedPrimary","edges","deployments","deliveries","alwaysOn"]}}  // what changed
{"type":"deployments","component":"apps/crm","data":{"op":"deploy","id":43,"deployment":"main","how":"promote",
  "from":"dev","checkpoint":"c:3f2a1c9","result":"running","phase":"build","by":"user:ana","session":"<id>"}}
                                                                                // each result or phase change
{"type":"deployments","component":"apps/crm","data":{"op":"work-tree","changed":3}}   // while paused: the count moved
{"type":"deployments","component":"apps/crm","data":{"op":"data","deployment":"dev","busy":"","state":"seeded"}}
{"type":"deployments","component":"apps/crm","data":{"op":"status","deployment":"dev",
  "level":"error","message":"…","ts":1790000000,"transient":false}}             // a non-primary tile-report
{"type":"deployments","component":"apps/crm","data":{"op":"notify","deployment":"dev",
  "to":"user:bob","title":"…","at":"…"}}                                         // a notification that was not pushed (P13)
```

- `deploy` events never carry compiler output. It is in the deploy log's
  `error` and in the deployment's log.
- `session` names the terminal or agent session that acted, so its own window
  can skip the notice it would otherwise print ([10-ux.md](10-ux.md)).
- The `work-tree` op is debounced like the watcher, and carries the same count
  as `State.workTree.changed`. It also tells a code panel that the work tree
  moved while nothing reloads.

### 3.4 Who receives what

Today every non-bus event reaches every subscriber, except `pr`, `term` and
`session` (`internal/server/server.go:592-610`; side finding #2). Non-primary
events are narrower (NP-11-8). In this table T is the event's tile: the
component minus `+<deployment>`, or the bare component.

| Event | Delivered to |
|---|---|
| the primary's events | as today |
| `reload`, `build-*` with `deployment` | admins; humans with write on T; T's own principals |
| `deployments` op `record`, `deploy` | admins; humans with read on T; T's own principals |
| `deployments` op `work-tree`, `data`, `status`, `notify` | admins; humans with write on T; T's own principals |
| `bus` with `deployment` | admins; principals bound to that namespace (08-data.md) |
| `term` | as today (per user); `data` gains `deployment` for a session with a named target (§7.4) |

"T's own principals" means element principals with `Component == T`. Other
tiles' principals never receive a non-primary event.

### 3.5 What publishes what

| Trigger | Events |
|---|---|
| a save, live reload on the primary | today's: `reload`, then the runner's `build-*`, bare |
| a save, live reload on non-primary N | `reload` and `build-*` for `<T>+<N>` |
| a save while paused | `deployments` op `work-tree` when the count moved, and nothing else |
| a resume or attach onto X (X now follows the work tree) | today's `build-*` for X's component, plus `deployments` op `deploy` |
| a deploy that puts a checkpoint on X (deploy, promote, roll back, reload now, the pin of pause, attach or protect) | `deployments` op `deploy` on each result or phase change; after a swap that changed X's served code, one `reload` for X's component; no `build-*` |
| a restart of X's current code (crash, reap, grant change, alwaysOn) | today's `build-*` for X's component |
| any record change | `deployments` op `record` |
| a primary reassignment | op `record`, plus `reload` for the bare component and for `<T>+<old>` and `<T>+<new>` |
| a lifecycle change | `reload` for the bare component as today (`internal/broker/lifecycle.go:114`), and for each non-primary deployment's qualified component |
| seed or reset progress | op `data` |
| a non-primary tile-report or notify | op `status` or `notify` (never `status`, never a push) |

X's component is bare when X is the primary, `<T>+<X>` otherwise. A pinned
deployment's frames never reload on saves (model §8).

## 4. Headers

```
X-XBin-Deployment: <name>
```

- **Injected** by `identify` (`internal/proxy/proxy.go:275-305`) on every
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
  clamped role under `read` ([09-fabric.md](09-fabric.md)).
- **Response header (NP-11-12):** a proxied response from a non-primary
  deployment carries `X-XBin-Deployment: <name>`, set by xbind over any value
  the backend set. A terminal `curl -i` can then tell which deployment
  answered. Primary responses are unchanged.
- `cgi` gets no deployment: it is excluded from pinning and non-primary
  deployments (model §12).

## 5. Env

| Variable | Who gets it | Value | Absent means |
|---|---|---|---|
| `XBIN_DEPLOYMENT` | a backend whose deployment is not the primary at spawn | its name | the primary |
| `XBIN_DEPLOYMENT` | a terminal or agent session with a named target (§7.4) | the target's name | the session follows the primary |

- **Backends** add it next to `XBIN_COMPONENT` (`internal/runner/runner.go:425`).
  A primary reassignment restarts the affected backends, so the value holds
  for a process's life (§0.2). Everything else keeps its name and meaning:
  - `XBIN_COMPONENT` is the bare tile path, so `Self()`, vault URLs and
    `res:${self}` ids keep working;
  - `XBIN_TOKEN` is per generation and carries the deployment server-side (§7.3);
  - `XBIN_RES_*` values are **identical** to the primary's, because the
    deployment's data namespace is bound at the primary's paths (model §12).
- **Sessions** add it next to `XBIN_COMPONENT` (`internal/term/term.go:460`).
  It is fixed at session start, like the network scope. `XBIN_URL` and
  `XBIN_TOKEN` are unchanged; the token carries the target.
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

## 7. Tokens and principals

### 7.1 Bound deployment

When a request is authorized, each of a tile's own principals has one concrete
bound deployment:

| Principal | Bound to |
|---|---|
| frame token, tile-origin cookie | the claim or origin; no claim means `main` (§7.2, §7.5) |
| instance token | the deployment whose generation it was minted for (§7.3) |
| terminal and agent tokens | the session's target; a session that follows the primary is bound to the current primary (§7.4) |

Humans, the owner token, `xbin/cron` and `xbin/bus` are bound to nothing. DR1
and DR2 (§0.4) are decided on this.

### 7.2 Frame tokens

```
5 fields (main, and every token minted today):  b64url(component)|b64url(user)|exp|gen|hmac
6 fields (a deployment other than main):         b64url(component)|b64url(user)|exp|gen|b64url(deployment)|hmac
hmac = base64url(HMAC-SHA256(.xbin/secret, <the preceding fields joined with "|">))
```

- **Verification** (`internal/auth/frametoken.go:252-294`) gains a 6-field
  case: `gen` must be valid, and the deployment must match the name grammar
  and must not be `main` (one spelling per meaning). The 4-field legacy form
  and the 5-field form mean `main`.
- **Downgrade:** a pre-feature verifier accepts only 4 or 5 fields
  (`internal/auth/frametoken.go:255-265`), so it refuses deployment tokens
  (fail closed).
- **A removed deployment:** the token still verifies. The request then fails
  resolution with 404.
- **Minting** in the D4 injection (`internal/server/static.go:431-466`):
  - the claim names the document's deployment under the name rule (§0.2);
  - `mayMintFrameToken` (`static.go:494-503`) mints a non-primary document's
    token only for humans with write, and for the tile's own principals bound
    to that deployment. A frame of `main` navigating to `dev`'s URL gets the
    document with `content=""` (P12).
  - `xbin.window` sub-path documents inherit their opener's deployment.
- **Renewal** (`GET /frame-token`, `internal/server/api.go:241-252`) copies the
  caller's claim when a tile renews itself. A human asks with
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
- **Choosing the target** at session start:
  - the requested name; requesting the current primary's name stores `""`;
  - else the live reload target, if it is a non-primary deployment;
  - else `""` (NP-11-2).

  This matches the glossary's default.
- **Requesting a target:** the target is fixed for the session's life, like the
  network scope. Changing it restarts the session.

  | Surface | Request |
  |---|---|
  | shell | `GET /ws/term?cwd=<tile>&deployment=<name>` (new sessions; ignored on reattach) |
  | agent session | `POST /api/xbin/term/sessions?deployment=<name>` (the body is unchanged, `internal/server/agentapi.go:72`) |
  | restart an agent onto a target | `POST /api/xbin/term/sessions/{id}/restart?deployment=<name>` |

- **Echo:** the `session` control frame, `SessionInfo`
  (`internal/term/sessions.go:20-41`) and `term` events carry
  `deployment: "<name>"` for a named target. An older xbind ignores the
  parameter, so clients **must** check the echo: a missing echo means that
  xbind can't target deployments, and the client ends the session and says so.
- A target that stops existing (the deployment is removed) makes the session's
  self-calls answer 404. The session isn't killed.
- **A protected primary** refuses deploy, promote, roll back, reload now and
  resume by terminal and agent tokens, whoever drives them. A session may
  still target it (DIV-6).

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
| `GET /backends` (`internal/boot/api.go:21-27`) | The row stays the **primary's** `{state, gen, error?}`. It gains `deployment` (the primary's name) and `deployments: {"<name>": {state, gen, error?}}` for non-primary deployments, both only when the tile has a record. | The old shell counts `Object.values(/backends)` and `state === 'healthy'` for its footer (`workspace-template/shell/bx-shell.js:1300`, `:1326`). New keys would inflate it. |
| `GET /runtime` (`internal/boot/api.go:30`) | `backends[]` keeps one row per tile, the primary, which gains `deployment` when the tile has a record. Non-primary rows go to a new top-level `deploymentBackends[]`: `runner.Backend` (`internal/runner/inspect.go:26-60`) plus `deployment`. | The admin console maps rows by `path` (`workspace-template/tiles/admin/tabs/runtime.js:336-340`). A second row per path would overwrite the primary's. |
| `GET /tile-status` (`internal/boot/api.go:87-117`) | `?deployment=<name>`; DR1 and DR2 pick the default. It gains `deployment` (the reported one) and `deployments: {primary, liveReload, items: [{name, state, gen, checkpoint?}]}`, only when the tile has a record. The gate is unchanged. | `bx status` renders it. |
| `GET /logs` (`internal/obs/logs.go:39-80`) | `?deployment=<name>`; DR1 and DR2 pick the default. 404 for an unknown deployment. A non-primary answer carries the `X-XBin-Deployment` response header, the echo of a text/plain answer. The gate is unchanged (`logs.go:42-47`). | Per-deployment log files are internal (07-runtime.md). `.xbin` is masked in isolated terminals. |
| `GET /components`, `/components/<path>` (`internal/server/api.go:130-160`) | Entries gain `deployments: {"primary": "main", "liveReload": "dev", "count": 2, "pending": 3, "operate": true}`, only when the tile has a record. `pending` is `State.workTree.changed` while paused; `operate` says the caller has terminal level on the tile. `origin` is the primary's origin. Deployments are **never** rows. | Old shells and the app parse leniently and list rows as tiles. The frame chip reads this summary, so a closed terminal window costs no request ([10-ux.md](10-ux.md)). |
| `GET /frame-token` (`internal/server/api.go:241-252`) | `?deployment=<name>` for humans (§7.2). The answer gains `deployment`, the echo. | |
| `GET /whoami` | `deployment` for bound element principals (§7.7). | |
| `GET /term/sessions`, `GET /status` terminals | Rows gain `deployment` for a named target. | |
| `POST /term/sessions`, `POST /term/sessions/{id}/restart`, `GET /ws/term` | `?deployment=<name>`, echoed (§7.4). | Adding a body field would 400 on older servers (§0.6). |
| `GET /sandboxes` (`internal/boot/sandboxes.go:42-49`) | Rows of a tile with a record gain `deployment`. `main` keeps `backend:<CompKey>:g<gen>` (`internal/runner/sbx.go:63`); any other deployment's backend ids are `backend+<name>:<CompKey>:g<gen>` ([07-runtime.md](07-runtime.md) §10.1). Their `stats.scope` is `"deployment"` (`sandboxes.go:62-69`). `?deployment=<name>` narrows. VM `usedBy` stays per tile (D112: every deployment is charged to the tile). D113 `tile` rows, once built, carry their deployment. | The admin tab groups by tile and counts one `"tile"` scope per tile (`workspace-template/tiles/admin/tabs/sandboxes.js:211-233`). |
| `GET /tile-report` | Unchanged: it never lists non-primary status. | §3.2 |
| `POST /tile-report`, `POST /notify` from a non-primary principal | Stored per deployment or suppressed. Answers are unchanged, plus `suppressed: true` on notify. Published as `deployments` op `status` or `notify`. | P13 |
| `GET`/`PUT`/`DELETE /cron/jobs`, `/bus/subscriptions` | A non-primary principal lists and writes its own deployment's files (§10.2). Rows gain `deployment` and `dormant`. `?deployment=<name>` is for admins. A PUT answer gains `dormant: true`. | The SDK subscribes at every start and must keep succeeding (compat rule 8). |
| `PUT /iface-instances`, `/ingress-hosts` from a non-primary principal | Stored, dormant, never routed. No `grants` event, no restart, no reconcile. The answer gains `dormant: true`. | These PUTs replace the whole map today (`docs/protocol.md:1707`, `:1721`). |
| `/vault/<component>` routes | DR1: a tile principal acts on its bound deployment's vault. Tile managers and admins use `?deployment=<name>`. | Vault URLs are built from `Self()` (`sdk/xbin.go:82`). |
| kv, blob, bus publish | No wire change: calls resolve in the caller's namespace (08-data.md). | |

**The echo rule** (12-compat.md NP-12-2). Every `deployment` parameter sent
to an existing route is echoed in the answer: a `deployment` field, or the
`X-XBin-Deployment` response header on a non-JSON answer. An older xbind
ignores the parameter and answers for the primary. A client that finds no
echo must treat the answer as the primary's and say so, never show it as the
deployment's.

## 9. `bx`

### 9.1 Conventions

- **Files:** three new files, each registering its commands in `moreCmds` from
  an `init()`:
  - `cmd/bx/deployment.go` for the `deployment` family;
  - `cmd/bx/deploy.go` for `deploy`, `promote` and `rollback`;
  - `cmd/bx/livereload.go`.

  Dispatch is `cmd/bx/agent.go:30-33`, the map `cmd/bx/native.go:26-32`.
  Usage text joins the `+nativeUsage` concatenation (`cmd/bx/main.go:174`), a
  line-neutral edit: `main.go` is at 1150/1150.
- **Prep:** `bx status` and `bx logs` change, so their functions
  (`cmd/bx/main.go:349-376`, `:494-530`) first move out verbatim
  ([13-surfaces.md](13-surfaces.md)).
- **Tile refs:** wherever `<tile> <name>` is accepted, `<tile>+<name>` is too,
  and the qualifier also fills a missing `--to`. `bx` sends the ref unresolved
  and the server applies §2.2.
- **Positionals are right-aligned** ([10-ux.md](10-ux.md) §8.2): the first one
  is the tile only when the count is the command's full arity; otherwise the
  tile is `$XBIN_COMPONENT`. With neither, bx prints usage and exits 2. The
  glossary's tile-first spellings have the full count, so they stay valid. A
  positional containing `/` or `+` is always a tile ref, since deployment names
  can't contain either. A `c:` or `work-tree` argument is always a checkpoint
  or diff spec.
- **Destinations are named with `--to`** (resume, attach, primary, deploy,
  rollback). `bx deployment primary <tile> <name>`, the glossary's form, is
  accepted too.
- **Defaults:** a read command's deployment defaults to `$XBIN_DEPLOYMENT`
  when the tile is `$XBIN_COMPONENT`. A code move never defaults its target:
  `--to` is required, and `promote` names both deployments (DR3).
- **Flags:** `--flag value` and `--flag=value` (compat rule 6). An unknown flag
  is an error (`unknownFlag(cmd, flag, false)`, `cmd/bx/args.go`). A
  value-taking flag last on the line is `--x needs a value` (`nextArg`).
- **Before acting**, every changing command fetches `GET /deployments` and, if
  the operation's `Can` is false, prints its `why` and exits without sending
  (exit 3 for `authority` and `policy`, 1 for `state`).
- **`--dry-run`** sends `dryRun: true`, prints the impact report and exits 0.
- **Confirmation:** every changing command prints the impact report (a dry
  run) first. On a terminal bx then asks `proceed? [y/N]`. Without a terminal,
  `--yes` is required, else bx exits 4. Agents run bx without a terminal, so
  shipping is always an explicit `--yes` (CM-2). `--yes` also sends the
  operation's `confirm` token.
- **Before a code move** from the work tree, bx sends the dry run's
  `impact.code.to` as `expect`, so what the report showed is what ships
  (NP-11-4).
- **`--json`** prints the route's JSON answer verbatim, one object and nothing
  else on stdout. Refusals are the server's `{error, docs}`, or the `Can`
  object bx refused on. Progress goes to stderr.
- **Waiting:** code-moving commands poll
  `GET /deployments/log?id=<id>&wait=25` until a final result, printing each
  phase to stderr, for at most 20 minutes. The 25 s fits bx's 30 s HTTP
  timeout (`cmd/bx/main.go:239-254`). `--no-wait` returns once the request is
  accepted, printing the id.
- **An old xbind:** every new command calls the `/deployments` family first.
  A 404 or 405 whose body is not a JSON object with `error` is Go's mux: the
  route is missing. bx then prints
  `this xbind has no tile deployments (no /api/xbin/deployments); upgrade xbind`
  and exits 6. `bx status` and `bx logs` resolve a named deployment through
  `GET /deployments` before using their old routes, and check the echo (§8).
  An old xbind therefore can't silently answer for the primary.
- **Hints:** `apiJSON`'s scoped-terminal hint (`cmd/bx/main.go:256-283`) gains
  one case: on a protected-primary 403 it adds `deploy from the terminal
  window's deployments panel as a tile manager, or with bx on the host`.

| Exit | Meaning | An agent reads it as |
|---|---|---|
| 0 | done: the deploy finished `ok`, nothing needed doing, a dry run, or `--no-wait` and the request was accepted | — |
| 1 | failed: a deploy that ran and failed (the deployment keeps its previous code), an invalid state, a network error, any other refusal | read the message, fix, retry |
| 2 | usage | fix the command |
| 3 | refused by authority or policy: a `Can` of kind `authority` or `policy`, or HTTP 403 | not yours to do: tell the user who can (the message names them) |
| 4 | not confirmed: declined, or no terminal and no `--yes` | ask the user |
| 5 | still running when bx stopped waiting; `bx deployment log` shows it | check later |
| 6 | this xbind has no tile deployments | don't retry here |

Codes 0 to 4 are [10-ux.md](10-ux.md)'s (NP-10-5). Existing commands keep
exiting 1 on every error.

### 9.2 Commands

```
bx live-reload [<tile>]                      state: target or paused (by whom, when), the last target, what it
                                             runs, work-tree files changed since (State.workTree)
bx live-reload pause  [<tile>]
bx live-reload resume [<tile>] [--to <name>] default: where live reload was last attached
bx live-reload now    [<tile>]
bx live-reload attach [<tile>] --to <name>   from attached only; while paused, bx points at resume

bx deployment ls  [<tile>]                   one tile, marking this session's target; with no tile and no
                                             $XBIN_COMPONENT: every tile with deployments, from /components
bx deployment add [<tile>] <name> [--from work-tree|primary|c:<id>] [--seed] [--attach]
bx deployment rm  [<tile>] <name>
bx deployment primary [<tile>] --to <name>   also: bx deployment primary <tile> <name>
bx deployment protect [<tile>] on|off
bx deployment seed  [<tile>] <name>
bx deployment reset [<tile>] <name>
bx deployment vault-copy [<tile>] <name> --keys <k1,k2>      --keys may repeat
bx deployment set [<tile>] <name> [--deliveries on|off] [--always-on on|off]
bx deployment edge [<tile>] [<edge> read|block|inherit|default]   no edge: list the edges and their policies
bx deployment run-now [<tile>] <name> <job>
bx deployment log  [<tile>] [<name>] [--limit <n>]
bx deployment diff [<tile>] [<from> [<to>]] [--stat] [--path <file>]
                                             from/to: a deployment name, c:<id>, or work-tree;
                                             defaults: the primary, and the work tree

bx deploy   [<tile>] --to <name> [--checkpoint c:<id>] [--no-wait]
bx promote  [<tile>] <from> <to> [--no-wait]
bx rollback [<tile>] --to <name> [--checkpoint c:<id>] [--no-wait]   default: the previous checkpoint in its log
```

Every changing command takes `--yes`, `--dry-run` and `--json`. Every read
command takes `--json`.

- **`set`** calls the deliveries route, then the always-on route, for the
  flags given, and stops at the first refusal. Each operation still has one
  route and one command.
- **Refinements of the glossary's proposals** (the nouns are kept; NP-11-7):
  - roll back gets its own `bx rollback`, logged `rollback`.
    `bx deploy --checkpoint` remains the deploy of a named checkpoint, logged
    `deploy`. [10-ux.md](10-ux.md) §8.1 reads `--checkpoint` as a roll back;
    both put the same code on the deployment.
  - deliveries and alwaysOn are `set` flags, and protection has its own
    subcommand, since it is its own operation.

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

The labels and report lines are [10-ux.md](10-ux.md)'s §8.3 and §12. They
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

### 10.1 The deployment record: `data/deployments/<CompKey>.json`

```jsonc
{
  "schema": 1,                          // bumped only when an older reader would misroute
  "tile": "apps/crm",
  "seq": 18,                            // +1 on every change
  "liveReload": "dev",                  // "" = paused
  "lastLiveReload": "dev",
  "primary": "main",
  "protectedPrimary": false,
  "edges": {"slot:llm": "block", "grant:apps/calendar": "read"},   // overrides only
  "nextDeploy": 45,                     // the next deploy id
  "liveReloadSince": {"at": "…", "by": "user:ana"},
  "deployments": {
    "main": {"checkpoint": "<full tree id>", "created": "…", "by": "user:ana"},
    "dev":  {"checkpoint": null, "deliveries": false, "alwaysOn": false,
             "data": {"state": "seeded", "from": "main", "at": "…", "by": "user:ana"},
             "created": "…", "by": "user:ana"}
  }
}
```

- **Writes:** only the deployments API writes it, with
  `fsutil.WriteFileAtomic` (D60), mode 0600.
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

### 10.2 Per-deployment registration files: `data/deployments/<CompKey>/<name>/`

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

### 10.3 The checkpoint store: `data/checkpoints/<CompKey>.git`

A bare repository in git's default object format, so a tile's own history can
be referenced. Only confined git reads or writes it (D78, P16).

```
HEAD                              ref: refs/heads/deploy/<primary>  (dangling while the primary follows the work tree)
config                            core.bare=true, gc.auto=0; no hooks, remotes, alternates or fsmonitor
refs/heads/deploy/<name>          the checkpoint commit a PINNED deployment runs; absent while it follows the work tree
refs/xbin/checkpoints/<tree id>   one per retained checkpoint: its commit (reachability; GC deletes these, 08-data.md)
refs/xbin/log/<name>              the deployment's deploy log, a commit chain
objects/, packed-refs             confined git
info/refs, objects/info/packs     refreshed by a confined `git update-server-info` after every ref change
                                  (the dumb-HTTP files §1.12 serves)
```

**Checkpoint commit** ([07-runtime.md](07-runtime.md) §2.3). There is one
parentless commit per tree, made at the tree's first capture. The checkpoint
id is `c:` plus a prefix of the **tree** id, so identical content is one
checkpoint. A pause is normally a code-identical swap, and it names the same
checkpoint again.

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

Being parentless, a checkpoint fetched over the remote shares no history with
the tile's own commits. `Xbin-Work-Tree-Head` names the commit it was taken
on, so a hotfix branch can be rebased onto that commit.

**Deploy log entry.** There is one commit per finished attempt, appended to
`refs/xbin/log/<name>`. Its tree is the attempted checkpoint's, so
`git log -p refs/xbin/log/main` shows what each deploy changed. The chain also
keeps every logged checkpoint reachable.

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
- The log is git-native because the glossary lets only confined tools write
  the store. [07-runtime.md](07-runtime.md) §2.3 instead appends a JSON-lines
  file there. Open question for the integrator.

### 10.4 Materialized checkpoints: `.xbin/deploy/<CompKey>/`

```
<tree id>/          the checkpoint's files: directories 0555, files 0444 (0555 when the exec bit is kept),
                    symlinks as symlinks. Extracted by confined git into .tmp-<random>/, then renamed
                    into place, so a present tree is complete. Opened beneath, never followed out (P16).
.tmp-<random>/      in-progress extractions, removed at boot
d/<name>/           a non-main deployment's derived state: its backend log, and later its D113 sandboxes
                    (07-runtime.md)
```

- The directory is named by the full tree id, never the short id, which can
  grow.
- Deployments pinned to the same checkpoint share one tree.
- `.xbin/` is derived state (`docs/protocol.md:2409-2430`). Deleting
  `.xbin/deploy/` while xbind is stopped is safe: trees are rematerialized from
  the store on demand.

### 10.5 Lifetime

- **Opting out:** resuming onto `main`, when it is the only deployment and
  every setting is at its default, removes the record. The store and its
  deploy log stay; they change no behaviour and keep the history for the next
  opt-in (NP-11-16).
- **Removing a deployment** removes its `data/deployments/<CompKey>/<name>/`.
- **Removing the tile:** the record, the store and the per-deployment
  directories are path-keyed leftovers on D82's refusal list (model §11,
  [08-data.md](08-data.md)).

## 11. Contract items, compat rules, doc pages

| Contract item | docs/compat.md rule it satisfies | Documented in |
|---|---|---|
| The zero state is unchanged on every surface (§0.1) | 1, 2, 9: nothing written at boot, the root `xbin.json` untouched | [/docs/compat.md](/docs/compat.md), [/docs/elements.md](/docs/elements.md) |
| The `/deployments` routes and the checkpoint remote (§1) | 2: new routes only | [/docs/protocol.md](/docs/protocol.md) (+ openapi.go rows) |
| Qualified URLs, `+` reserved the D82 way (§2) | 2, 3: existing URLs keep resolving; an exact component wins | [/docs/protocol.md](/docs/protocol.md) `/c/` and `/api/`, [/docs/elements.md](/docs/elements.md), [/docs/auth.md](/docs/auth.md) |
| `<bx-frame src="<tile>+<name>">`, `xbin.window` inside a deployment (§2.5) | 3: no vendor URL moves; old shells keep working | [/docs/elements.md](/docs/elements.md) §bx-frame, [/docs/frontend-kit.md](/docs/frontend-kit.md) |
| Origins-mode labels per deployment (§2.6) | 2: `main`'s label and cookies unchanged | [/docs/auth.md](/docs/auth.md) §Tile asset gating, [/docs/elements.md](/docs/elements.md) §Asset URLs |
| Qualified `component` + `deployment` on `reload`/`build-*`; the `deployments` type; no non-primary `status` (§3) | 2, 10: receivers ignore unknown types and fields; the shipped app never reloads for a non-primary deployment | [/docs/protocol.md](/docs/protocol.md) §/ws/events, [/docs/sdk.md](/docs/sdk.md) (`xbin.events`) |
| `X-XBin-Deployment` (§4) | 2, 8: absent for the primary; stripped inbound | [/docs/protocol.md](/docs/protocol.md), [/docs/auth.md](/docs/auth.md), [/docs/sdk.md](/docs/sdk.md) |
| `XBIN_DEPLOYMENT` (§5) | 8: a new variable; existing ones keep their meaning | [/docs/elements.md](/docs/elements.md), [/docs/protocol.md](/docs/protocol.md) §Backend contract, [/docs/sdk.md](/docs/sdk.md), [/docs/overview/09-terminals.md](/docs/overview/09-terminals.md), [/docs/bx.md](/docs/bx.md) |
| `xbin.Deployment()`, `CallerInfo.Deployment`, `xbin.deployment` (§6) | 8: additive, zero-dependency | [/docs/sdk.md](/docs/sdk.md) |
| The 6-field frame token, the `x2`/`c2` tile-origin credentials (§7.2, §7.5) | 2: tokens stay opaque; old forms verify unchanged | [/docs/protocol.md](/docs/protocol.md) §Authentication, [/docs/auth.md](/docs/auth.md) |
| Session targets, echoed (§7.4) | 2: query parameters and response fields only | [/docs/protocol.md](/docs/protocol.md) §/ws/term, [/docs/overview/09-terminals.md](/docs/overview/09-terminals.md) |
| whoami `deployment` (§7.7) | 2 | [/docs/protocol.md](/docs/protocol.md) |
| `/backends`, `/runtime`, `/tile-status`, `/logs`, `/components`, `/sandboxes`, `/term/sessions` fields (§8) | 2: fields on rows, never new keys or rows | [/docs/protocol.md](/docs/protocol.md) |
| Dormant registrations behind the same routes (§8, §10.2) | 8: a call that succeeds today keeps succeeding | [/docs/resources.md](/docs/resources.md), [/docs/protocol.md](/docs/protocol.md) |
| The `bx` commands, exit codes 3–6, new flags on `status`/`logs`/`agent run` (§9) | 6: a superset; existing invocations and exit codes unchanged | [/docs/bx.md](/docs/bx.md) + usage strings |
| No manifest key (§0.1) | 7 | [/docs/elements.md](/docs/elements.md) (deployment-level vs tile-level fields, model §6) |
| On-disk formats (§10) | not promised; old binaries must not misread them | [/docs/protocol.md](/docs/protocol.md) §Filesystem contract, [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md) (backups) |
| Every item above | 11: a `docs/changelog.md` entry; no migration note, since nothing breaks | [/docs/changelog.md](/docs/changelog.md) |

## Divergences from the model

- **DIV-1. Old clients don't ignore every qualified event.** Model §8 says old
  clients' exact or prefix matching ignores events with
  `component: "<tile>+<name>"`. That is true for bx-frame, `isReloadTarget`
  and the app. It is false for the scaffold shell's `status` handling, which
  toasts or title-marks any component
  (`workspace-template/shell/bx-shell.js:1257-1266`, `:1278-1283`). It is also
  false for its `reload` handling, which refetches `/components` on any
  component (`bx-shell.js:194-196`); that part is harmless.
  **Fix:** non-primary status never uses the `status` type. It rides
  `deployments` op `status` (§3.2, §3.3).
- **DIV-2. P17's "absent for main" versus "non-primary only".** P17 says the
  markers are absent for `main`. The glossary and model §7 and §8 say env,
  header and events mark non-primary deployments. The two differ after a
  primary reassignment.
  **Fix:** the role rule for signals, the name rule for credentials and
  storage-bound ids (§0.2). A reassignment restarts both backends so
  `XBIN_DEPLOYMENT` stays true (NP-11-1). The frame-token claim keeps §7's
  "absent means main".
- **DIV-3. No deployment segment in the registration path.** Model §3's example
  `data/deployments/<CompKey>/cron.json` has none.
  **Fix:** one directory per deployment,
  `data/deployments/<CompKey>/<name>/cron.json` (§10.2), so files never mix
  deployments' rows.
- **DIV-4. Materialized checkpoints named by short id.** Model §3 materializes
  under `.xbin/deploy/<CompKey>/<id>/`, but a short id grows when prefixes
  collide.
  **Fix:** name the directory by the full tree id (§10.4).
- **DIV-5. The record in model §4 is incomplete.** It can't answer "resume onto
  the last target" or "reload now" while paused (model §5), because
  `liveReload` is then `""`. It also has no sequence number for exact
  confirmations.
  **Fix:** `lastLiveReload`, `liveReloadSince`, `seq`, `schema` and
  `nextDeploy`, with full tree ids for checkpoints (§10.1).
- **DIV-6. P21 is ambiguous.** "terminal/agent tokens cannot target it" could
  mean a session's target deployment. That reading would refuse the model's
  own default target (the primary, while paused) on every protected tile.
  **Fix:** read it as the target of deploy, promote, roll back, reload now and
  resume (§1.4, §1.6, §7.4). Sessions may still target a protected primary.
- **DIV-7. The primary alias for other tiles.** The glossary says
  "`<tile>+<primary name>` also works". For other tiles' principals that would
  let a tile hard-code a deployment name that breaks after a reassignment.
  **Fix:** the alias is for humans and the tile's own principals. Other tiles
  must use the bare URL (§2.3, NP-11-14).

## New proposals

- **NP-11-1.** Two naming rules (§0.2): role rule for event, header, env, meta
  and whoami markers; name rule for credentials, origin labels, sandbox ids and
  storage. A primary reassignment restarts the old and new primary's running
  backends.
- **NP-11-2.** Session targets (§7.4):
  - the target is stored as a name, or as "follows the primary"; requesting
    the current primary stores the latter;
  - it is fixed for the session's life, and changing it restarts the session;
  - it is requested with a `deployment` query parameter on `/ws/term`,
    `POST /term/sessions` and `…/restart`;
  - the answer echoes it, and clients must check the echo.
- **NP-11-3.** The record keeps `lastLiveReload`, the default for resume and
  reload now, and `liveReloadSince`, who last attached or paused it and when.
- **NP-11-4.** Review, then deploy: a diff or dry run against the work tree
  captures a checkpoint and names it (`X-XBin-Checkpoint-To`,
  `impact.code.to`). `deploy` and `promote` accept `expect` and answer 409 when
  the code moved since ([10-ux.md](10-ux.md) NP-10-4).
- **NP-11-5.** `confirm` tokens (`erase`, `erase-data`, `copy-data`,
  `data-stays`) on the data-bearing routes. In `bx`, `--yes` sends them, and
  every changing command needs `--yes` without a terminal.
- **NP-11-6.** The deploy log records every finished attempt, failed ones
  included, with `how` values for pausing and attaching live reload too
  ([10-ux.md](10-ux.md) NP-10-11). It is kept git-native in the store
  (`refs/xbin/log/<name>`, trailers, §10.3), written only by confined git, so
  `git log -p` shows each deploy's change.
- **NP-11-7.** `bx rollback` is its own command. Deliveries and alwaysOn are
  flags of `bx deployment set`, and protection has `bx deployment protect`.
  Destinations are named with `--to`, and positionals are right-aligned
  ([10-ux.md](10-ux.md) NP-10-15). The nouns are the glossary's.
- **NP-11-8.** Event visibility (§3.4): non-primary `reload`/`build-*` and the
  `deployments` ops `work-tree`, `data`, `status` and `notify` go to writers and
  the tile's own principals only. `record` and `deploy` go to readers. Other
  tiles never receive any of them.
- **NP-11-9.** Non-primary `status` is published only as `deployments` op
  `status`, never as `status` (DIV-1).
- **NP-11-10.** Run now is a terminal-level act on any non-primary
  deployment's cron job. Model §10 assigns it to no one.
- **NP-11-11.** Resetting `main`'s data while it is non-primary is a tile
  manager's act. It holds today's keys: the former primary's data.
- **NP-11-12.** Proxied responses from a non-primary deployment carry
  `X-XBin-Deployment`.
- **NP-11-13.** `GET /deployments` states `features` (`deployments/1`).
  Clients check it before sending anything a later xbind adds, such as `match`
  or feeds, since the bodies are decoded strictly.
- **NP-11-14.** Qualified `/api/` URLs are for humans and the tile's own
  principals only (DIV-7).
- **NP-11-15.** The checkpoint remote's authority: the tile's own principals,
  write, admin, `code[:<tile>]`. It is injected as `xbin-deploy` only into
  sessions opened while the tile has a record, with the fetch refspec
  `+refs/heads/deploy/*:refs/deploy/*`, so flow H's `deploy/main` resolves
  verbatim.
- **NP-11-16.** Opting out keeps the checkpoint store and deploy log.
- **NP-11-17.** New `bx` commands use [10-ux.md](10-ux.md)'s exit codes (3
  refused by authority or policy, 4 not confirmed), plus 5 (still running when
  bx stopped waiting) and 6 (this xbind has no tile deployments). Existing
  commands keep 1.
- **NP-11-18.** Origins mode: per-deployment labels are name-based (`main`
  keeps today's). The bare URL goes to the primary's origin, and storage
  follows the deployment.
- **NP-11-19.** `GET /deployments` answers everyone who can read the tile
  (as [10-ux.md](10-ux.md) NP-10-2 asks), without the writers-only fields:
  build errors, registrations, suppressed notifications, vault counts and
  edges. The deploy log and the diff need write or the tile's own principal.
  `/components` carries a per-tile summary (`primary`, `liveReload`, `count`,
  `pending`, `operate`) for the frame chip and the scaffold.
- **NP-11-20.** Every POST takes `dryRun: true` and answers an `Impact`
  (code, data, pausesLiveReload, stops, affects, reloads). The confirmation
  dialog and bx's prompt render it, following D39's `GET /owner/preview`
  ([10-ux.md](10-ux.md) NP-10-3).
- **NP-11-21.** `build-*` keep today's meaning: work-tree builds and restarts
  of current code. A deploy that puts a checkpoint on a deployment reports
  only through `deployments` op `deploy`, with phases, and paints no frame
  overlay when it fails ([10-ux.md](10-ux.md) NP-10-12).
  [07-runtime.md](07-runtime.md) §8.1 step 1 and §8.4 emit `build-*` for such
  deploys and must follow this.
- **NP-11-22.** The echo rule (§8): every `deployment` parameter on an existing
  route comes back in the answer, a field or the `X-XBin-Deployment` response
  header ([12-compat.md](12-compat.md) NP-12-2).
