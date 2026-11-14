# Protocol: delegation (a person's workspace agent)

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
how a sealed system component acts as its person (the delegate call), the
routes the person's clients and the agent call about it (`/delegate/…`):
the switches, approval requests and tickets, standing approvals, admin
change sets and the audit log. The rules — who may delegate, what a
delegate reaches, what a tile sees — are in
[auth.md §Delegation](/docs/auth.md#delegation-a-persons-workspace-agent).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it. Three callers appear:

- **the person** — their own top-level credential: a browser session, the
  app's session or a device's. Never a view-as session, a tile's
  credential or the root token. **Everything that grants is the person's
  alone**: approving a request, a standing approval, resuming a paused
  agent, the admin opt-in, deciding a change set, reading the log;
- **the agent's page** — the sealed component's own frame, when its token
  was minted under the person's own login. It shows the agent's cards, and
  it renders what the agent wrote — so it only **reads** (the state, the
  requests, the standing approvals, the change sets) and may **pause** the
  agent. A script injected into it can approve nothing. The approval
  itself is given in chrome xbind serves, or in the app;
- **the agent** — the sealed component's backend making a *delegate call*
  (below). It asks; it never answers itself.

Nothing on this page exists until an admin turns workspace agents on
(`PUT /delegate/workspace`), and no credential delegates on an xbind that
ships no sealed workspace-agent component.

## The delegate call

A person's partition of a sealed component that holds delegation calls
xbind **as that person** by sending its own instance token under another
scheme, on the gateway socket:

```
Authorization: XBin-Delegate <XBIN_TOKEN>
```

- The same token as `Bearer` is the component itself (its own kv, vault,
  bus…). Under `XBin-Delegate` it is the person the partition belongs to.
  Nothing the caller sends names the person.
- Honoured on the **gateway socket** only (`XBIN_GATEWAY`), and only on
  `/api/…`. Everywhere else — the console listener, `/c/`, `/ws/…`,
  `/docs/`, a device's tunnel — it is no credential: `401`.
- `403 {error, refusal}` while delegation is off: `paused` — the person
  paused their agent; `workspace-off` — the workspace's switch; `no-sandbox`
  — an xbind that runs tiles without sandboxes (`--isolate` off; `--dev`
  and `--no-auth` excepted).
- A request that carries the scheme is judged as the delegate's or refused,
  whatever else it carries.
- The method judged is the method made: `X-HTTP-Method-Override`,
  `X-HTTP-Method` and `X-Method-Override` are removed before a tile sees
  the call, and a call whose query carries `_method` is refused (`403`).

Request headers a delegate may add (xbind reads them at the gate and never
forwards them):

| Header | Meaning |
|---|---|
| `X-XBin-Agent-Model: <id>` | the model the agent's conversation is on now. The orgs' model rules are asked with it; xbind doesn't verify it |
| `X-XBin-Dry-Run: 1` | describe the call instead of making it (below) |
| `X-XBin-Ticket: <request id>` | the approved request this side effect runs under (below) |

**What a delegate reaches** is a closed list — the delegation cap:

| Class | Routes |
|---|---|
| reads, at the person's level | `GET /whoami`, `/components`, `/components/<path>`, `/tile-status`, `/tile-report`, `/logs`, `/templates`, `/agent/providers`, `/term/sessions`, `/term/sessions/<id>`, `…/events`, `…/log`, `…/diff`, `/agent/history`, `/agent/history/<id>/events`, `/code/tree`, `/code/file`, and its own `/delegate/state`, `/delegate/approvals`, `/delegate/requests/<id>`, `/delegate/changesets[/<id>]` |
| a tile's API | `/api/<tile>/…` where the person can read the tile: `GET`/`HEAD` freely; any other method needs the person's approval |
| acts | `POST /term/sessions` (approval, or the person's preference), `POST /templates/new` (approval); on sessions **it opened**: `…/prompt`, `…/queue`, `DELETE …/queue[/<qid>]`, `…/cancel`, `DELETE /term/sessions/<id>`; asking: `POST`/`DELETE /delegate/requests…`, `POST`/`DELETE /delegate/changesets…` |
| admin reads, only for an admin who opted in | `GET /grants`, `/bindings`, `/orgs` (without SSO group rules), `/limits` — answered as the person |
| refused | everything else: `403 {error, refusal: "not-delegated", class}`. In particular: answering a tile agent's permission request or question, a session's options and restart, terminals, deleting or disabling tiles, code writes and pull requests, the account, devices and sign-in tickets, frame tokens and path tickets, view-as, a person's consents, tile stores, the admin API, and every approval on this page |

Listings leave out the tiles whose org keeps the workspace agent out (on
the model it says it is on); a request about such a tile is `403 {refusal:
"tile"}` with the org's reason. No WebSocket upgrade (`403`). An admin's
agent is not an admin.

Refusals are `403 {error, refusal, class, docs}` with `refusal` one of
`not-delegated`, `tile`, `needs-approval`, `ticket`, `admin-read`, and the
off reasons above;
`413 {refusal: "too-large"}` for a ticketed body over 256 KiB; and
`429 {refusal: "rate-limited"}` + `Retry-After` past 30 acts a minute (a
burst of 30, one more every 2 seconds; reads and dry runs don't count).

**A call ends with what allowed it.** A call in flight (a followed stream,
a slow tile) is cut when the person pauses their agent or the workspace's
switch goes off — at once — and when the person is disabled or deleted,
loses the tile, the tile or its org turns agents away, or the standing
approval a write ran under is revoked: at once where xbind makes the
change, within 3 seconds otherwise.

### Dry run

Any allowed delegate call with `X-XBin-Dry-Run: 1` answers `200` and runs
nothing:

```
{dryRun: true, allowed, class, as, agent, sideEffect,
 tile?, owner?, level?, role?, agentAccess?,
 reversible?: false, needsApproval?, under?, refusal?}
```

`class` is `read`, `act`, `admin-read`, `tile-read` or `tile-write`;
`level` and `role` what a tile call would carry; `needsApproval` whether a
ticket is needed now (`false` with `under: "approval:<id>"` when a standing
approval covers it); `allowed: false` with `refusal` when the tile or its
org would refuse it. A call the cap refuses is refused as without the
header.

### What a tile sees

A delegate's call to `/api/<tile>/…` reaches the tile's primary — the
person's own partition of a partitioned tile — with:

| Header | Value |
|---|---|
| `X-XBin-From` | `user:<id>` |
| `X-XBin-User`, `X-XBin-User-Level` | the person and their level on the tile now |
| `X-XBin-Role` | `reader` for read, `writer` for write and terminal — never `admin` |
| `X-XBin-Agent` | the sealed component's path: the call is the person's agent's, not their own hand |
| `X-XBin-Partition`, `X-XBin-Partition-Id` | the person's partition, on a partitioned tile |

A tile limits what agents use of its API with `"agent"` in its manifest
(`"off"`, `"read"`, `"full"`), and its backend tells an agent's call from
the person's own by `X-XBin-Agent`
([elements.md → A person's agent](/docs/elements.md)).

`?xbin-partition=global` on such a call reaches a partitioned tile's global
instance under the same rules as the person's own partition: with
`partitionConsent` on it needs the person's consent for *agent → tile*, and
it is counted in their ledger.

## The switches

```
GET    /delegate/state          the person, their agent's page, or their
                                 agent. {user, workspace, paused,
                                 adminChanges, canAdminChanges,
                                 pendingRequests, standingApprovals,
                                 openChangeSets}: workspace is the
                                 workspace's switch; canAdminChanges
                                 whether the person is a workspace or org
                                 admin
PUT    /delegate/state          the person, or their agent's page —
                                 which may send {paused: true} and nothing
                                 else (403). {paused?, adminChanges?} → the
                                 state. Pausing refuses the agent's every
                                 call at once and cuts the calls running
                                 (a followed event stream ends). Resuming,
                                 and adminChanges — the agent may read what
                                 change sets cover and propose them — are
                                 the person's own session's; only a
                                 workspace or org admin turns adminChanges
                                 on (403)
GET    /delegate/workspace      admin. {enabled, by?, at?}: off until an
                                 admin turns it on
PUT    /delegate/workspace      admin. {enabled}. Off refuses every
                                 person's agent at once
```

The workspace's switch is kept with the rest of delegation's state
(`data/delegation/workspace.json`), not in `/workspace-settings`: the admin
console and `bx settings` list that store's entries by name, so a new one
would show in neither without its own UI. Set it with this route.

## Approval requests and tickets

A side effect that needs approval passes the gate only when **the person**
approved it, and xbind keeps the approval — a credential taken from the
agent's sandbox can ask and can read, and nothing more.

1. The agent posts what it wants to do, exactly as it will send it.
2. The person sees xbind's description beside the agent's words, and
   approves or denies it (a `delegate` event, a push of kind
   `agent.approval`).
3. Approved, the request **is a ticket**: for 15 minutes, once, the agent
   makes that call with `X-XBin-Ticket: <id>`. Another method, path, query,
   body or content type — or a path that now resolves to another tile — is
   `403 {refusal: "ticket"}` and spends nothing.

```
POST   /delegate/requests       the person's agent only. {method, path,
                                 body? | bodyBase64?, contentType?,
                                 summary?} → the request, pending. path is
                                 the call's path under /api/ with its query
                                 (at most 2048 bytes, no _method); the body
                                 at most 256 KiB; contentType the
                                 Content-Type the call will carry
                                 (application/json when there is a body and
                                 none is said). Only a call that needs
                                 approval (400 otherwise): a non-GET call
                                 to a tile's API, POST /term/sessions,
                                 POST /templates/new. 403 when the tile or
                                 its org refuses the call anyway; 409 past
                                 20 pending
GET    /delegate/requests       the person, or their agent's page.
                                 {requests}, oldest first; ?all=1 adds the
                                 ones decided in the last day
GET    /delegate/requests/<id>  the person, their agent's page, or their
                                 agent (it polls: state approved = a
                                 ticket)
POST   /delegate/requests/<id>/approve
                                 the person, in their own session, app or
                                 device — never the agent's page (403).
                                 {grant?: "once" | "hour" | "always",
                                 scope?: "call" | "tile"} → {request,
                                 approval?}. once is the default; hour and
                                 always (a tile API call only) also record
                                 a standing approval — of this call
                                 (scope "call", the default) or of every
                                 write to the tile (scope "tile"). 409 when
                                 no longer pending
POST   /delegate/requests/<id>/deny
                                 the person. → {request}
DELETE /delegate/requests/<id>  the person, or their agent. withdraws a
                                 pending or approved request
```

A request:

```
{id, user, agent, kind: "call" | "session" | "template",
 method, target, tile?, contentType?, body?, bodyShown, bodySize, bodySha256,
 summary?, preview: {class, tile?, owner?, level?, role?, standing?, session?},
 state: "pending" | "approved" | "denied" | "used" | "expired" | "withdrawn",
 grant?, created, decided?, expires, decidedVia?}
```

`body` is shown while the request is pending or approved, when it is text;
`summary` is the agent's own text — show it as the agent's. For a tile
call `preview.standing` is `{call, tile}`: xbind's sentence for what each
standing scope would allow from then on — show the one the person is about
to choose. For a session start `preview.session` is `{tile, provider,
providerName, mode, asks, options, net, api, vm, gpu, model, name,
resume}`: the harness and mode the session **will** run in — a resumed
session's come from its history when the request names none — and every
option the start carries; `asks: false` means the mode lets the tile agent
act without asking — say so on the card. A pending request expires after 24
hours; decided and withdrawn ones are kept a day, 200 a person at most.

**Starting a tile agent.** With a ticket the session opens as the person
approved it — the harness and mode on the card, which no option of the
start can change (an option that would set the mode is refused). Without
one it opens only when the person's agent preferences allow starts without
asking, and then the mode, network and API access are the preference's:
sending `mode`, `net` or `api` is `403`. Either way: the tile's default
deployment, a harness the tile's org allows, never a tile that is itself a
workspace agent, and the session's row carries `origin: "agent"`. An agent
resumes only past sessions it started.

## Standing approvals

```
GET    /delegate/approvals      the person, their agent's page, or their
                                 agent. {approvals: [{id, user, tile,
                                 scope, method?, path?, until?, created,
                                 via?, request?}]}
POST   /delegate/approvals      the person. {tile, grant: "hour" |
                                 "always", scope: "call", method, path} or
                                 {tile, grant, scope: "tile"} → the
                                 approval. scope is required (400). 404
                                 for a tile the person can't read
DELETE /delegate/approvals/<id> the person. 204; a write running under it
                                 is cut
```

A standing approval has a **scope**:

- `call` — one call, again: that `method` on that `path` of the tile's API
  (`/api/<tile>/…`, no query), with any body and query. What "always" means
  unless the person chose wider.
- `tile` — every write to the tile's API: any method, any path. A separate,
  explicit choice; say what it is before the person makes it
  (`preview.standing.tile`).

An approval goes when its tile is deleted or the person can no longer read
it, and does not come back with the access. A session start and a tile
create are approved each time.

## Change sets

An agent never calls the admin API. For an admin who turned `adminChanges`
on, it **proposes** a batch; the person approves all, some or none; xbind
applies the approved operations in order, as that person, through the same
handlers as a click (the same checks, restarts, events and audit).

```
POST   /delegate/changesets     the person's agent only. {title, items:
                                 [{kind, args, why?}]} → the change set,
                                 proposed. Nothing is applied. 403 without
                                 the opt-in; 400 for an unknown kind or
                                 arguments; at most 20 items; 409 past 10
                                 open sets
GET    /delegate/changesets     the person, their agent's page, or their
                                 agent. {changesets}, oldest first
GET    /delegate/changesets/<id>
                                 the person, their agent's page, or their
                                 agent
DELETE /delegate/changesets/<id>
                                 the person, or their agent. withdraws a
                                 proposal
POST   /delegate/changesets/<id>/approve
                                 the person whose agent proposed it, in
                                 their own session, app or device. {items?:
                                 [n, …], password?} → the set with each
                                 item's result. Without items: all of
                                 them; items: [] applies none (the set
                                 ends rejected). Needs a sign-in under 10
                                 minutes old, or the password in the body:
                                 403 {error, stepUp: "password" | "signin"}
                                 otherwise, as for adding a device
POST   /delegate/changesets/<id>/reject
                                 the person. → the set
POST   /delegate/changesets/<id>/undo
                                 the person. → a new change set that
                                 undoes the applied items of this one that
                                 can be undone, to approve like any other.
                                 409 when none can
```

Kinds — a closed list; there is no kind that names a route:

| `kind` | `args` | Applies as |
|---|---|---|
| `grant.add`, `grant.revoke` | `{from, target, role}` | `POST` / `DELETE /grants` |
| `binding.set` | `{component, slot, provider \| providers, host?, zone?, listen?, add?}` | `POST /bindings` |
| `binding.remove` | `{component, slot, provider?, host?, zone?, listen?}` | `DELETE /bindings` |
| `org.share` | `{org, tile, level}` (`""` stops sharing) | `PUT /access` |
| `limits.set` | `{tile, kind, value}` — a raise | `POST /limits` |

What a kind can't express — refused as no change at all (`400` at the
proposal, and again if it became true by the time the set is applied):

- an operation **on a workspace agent**: a grant from it or on it (its
  code too), a binding of it or to it, a share of it, its limits;
- a grant or revoke whose target is `xbin`, `xbin:*`, `cap:*`, `gpu:*`,
  the bare `code`, `net:*` or a pattern — those are given and taken away on
  the Grants page;
- a binding of an `@` pseudo-slot (`@archive`) or of the workspace default
  (`*`), a binding to the host's network (`host`, or a network set that
  says host), and `provider` together with `providers`;
- a share of a tile that itself holds `xbin`, `cap:*`, `gpu:*` or `code`.

A change set:

```
{id, user, agent?, title, undoOf?,
 state: "proposed" | "applying" | "applied" | "partial" | "rejected" | "expired" | "withdrawn",
 created, decided?, expires,
 items: [{n, kind, args, why?, line, before?, after?, effects?, warnings?,
          blocked?, inverse?, result?, error?}]}
```

- `line`, `before`, `after`, `effects` and `warnings` are **xbind's**: the
  sentence is written from the kind and its arguments, the state read by
  running the handler's own checks without writing. `why` is the agent's
  text. A binding's `line` states its `host`, `zone` and `listen`, and its
  `warnings` say what the routes it adds open: `PUBLIC: …` for a hostname
  or zone (with the endpoint's paths), `OPEN PORT: …` for a listen
  address, `EGRESS: …` for what a network slot will reach. Show warnings
  as prominently as the line.
- `blocked` is the handler's refusal: that item can't be applied.
- At approve, each chosen item is checked again. `result`: `applied`;
  `failed` (`error`: the handler's words); `stale` — the state, or
  xbind's line, effects or warnings for it, changed since the proposal:
  nothing applied; `skipped` — an earlier item failed;
  `rejected` — the person left it out; `blocked`.
- The set is not atomic: what was applied stays. `applied` when every
  chosen item applied, else `partial`.
- A proposal expires after 7 days.

## The audit log

```
GET    /delegate/audit          the person, in their own session, app or
                                 device; an admin with ?user=<id>. {user,
                                 calls}, newest first; ?before=<RFC 3339>,
                                 ?limit=<n> (default 200, at most 1000)
```

A row: `{ts, user, agent, method, path, tile?, class, effect?, dryRun?,
status, refused?, ticket?, under?, n?}`. Written for every side effect
that ran, every approval decision (`class: "decision:…"`) and every
applied change-set item (`class: "changeset:<kind>"`) — always — and for
tile reads, refusals, dry runs and reads of code, logs and admin state up
to 60 in a burst and one a second after that, per person: the next row's
`n` says how many were left out before it. Not for the listing reads a UI
makes. Never a body, a query or a header; a path is cut at 512 bytes.
Thirteen months are kept. The agent, its
page, tiles and view-as sessions can't read it.

## Events and pushes

`/ws/events` carries `{type: "delegate", data: {op, id?, user}}` to the
person's own sockets and their agent's page — `op`: `request` (one
appeared, was decided, withdrawn or used), `approval`, `changeset`,
`state`. Refetch the list it names. Pushes: `agent.approval` (a request
waits) and `agent.changeset` (a proposal waits). Their link is the path of
the sealed component that is the person's agent (the `agent` of the request
or change set); their text is xbind's, never the agent's.

**Developing an agent.** On an xbind started with `--dev` or `--no-auth`,
approving a change set needs no recent sign-in. Under `--no-auth`, where
nobody signs in, the implicit owner answers as the person on every route of
this page — for the person `?user=<id>` names (default `owner`) — and
approved changes are applied as the owner. Neither applies to any other
xbind.

Bus events and partition mail delivered **into** the sealed component are
other tiles' data: the agent treats them as untrusted input, like any tile
data it reads.
