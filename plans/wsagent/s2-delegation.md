# Spike S2 — delegation and admin change sets

> Status: **spike findings + prototype** (2026-11-14, branch
> `wsagent/s2-delegation`, on master 83c03745 + the plan commit). Feeds
> phase B3 of `plans/workspace-agent.md`. Nothing here is live: the sealed
> table is empty, no gate or log is wired at boot, no route is mounted.
> Line numbers are this branch's.

> **Superseded by the implementation (D228, 2026-11-14).** This is the
> spike's record; the contract is `docs/auth.md` §Delegation and
> `docs/protocol/delegation.md`. What changed after the owner's decisions:
> the holder lookup is one function set at boot (no compiled-in map); the
> scheme is honoured on the gateway socket only, and not without sandboxes;
> the cap names every route (an `admin-read` class, a session's log and
> diff, `DELETE` and driving only for sessions the agent opened); side
> effects need xbind-verified approvals (tickets, standing approvals);
> session modes and pickers come from the ticket or the person's
> preferences, not a fixed default-mode clamp; the org seam is
> `delegation.Policy`; reads are not coalesced in the audit log, and
> listing reads are not logged; change sets are built (section 7's design,
> with previews that call the handlers' own check functions instead of a
> dry-run flag). Sections 9 and 11 are history.

## 1. Result in short

Delegation fits the existing identity model with one new credential form,
one new principal field and a third route-class table. Three findings
change the plan's wording:

1. **A person cannot call a tile's API in person today** — only an admin
   can (`internal/broker/deploydata.go:244`, rule 3; everyone else gets
   `user:<id> is not granted access`, asserted in
   `internal/broker/partitionglobal_test.go:146`). People reach tile APIs
   through the tile's frame, which is the *tile* at its self-role with the
   person attributed (D29). So "tile APIs at the person's level" is a new
   reach, and its shape had to be chosen (section 4).
2. **An allowlist by route class is necessary and not sufficient.** Some
   routes mix classes in one pattern, some answer a person only through
   their tile's frame, and every tile-addressed read needs the org filter
   (section 3.3).
3. **"Cannot answer a permission request" needs a mode clamp too.** A
   session opened in a mode that never asks is the same act as answering
   (section 5).

## 2. The primitive

### 2.1 Who holds it
A compiled-in table, `sealedDelegates` (`internal/auth/delegate.go`), empty
in this branch; a test hook adds an entry. S1's sealed-component table
replaces it. No request, manifest, grant, binding, policy row or approval
reads or writes it. The name "delegated" is taken (`internal/broker/
delegated.go`, D26, an org admin's delegated approval): the code says
*delegate* / *delegation* and the docs should say "your workspace agent".

### 2.2 The credential and where the principal is minted
- The credential is the **instance token of the person's partition** of the
  holder — the token the runner already registers for (tile, partition,
  uid) (`internal/boot/partitionroute.go:117`,
  `internal/auth/partition.go:71`) — sent under its own scheme:
  `Authorization: XBin-Delegate <token>`.
  - As `Bearer`, the token is the component itself (its kv, vault, bus,
    notify, in its partition). Under the scheme it is the person. The
    scheme is the per-request opt-in; nothing the caller sends names the
    person.
  - The token already stops resolving when the person is deleted,
    disabled, re-created under the id, loses read on the component, or the
    tile's mode changes (`lookupInstance` → `partitionCovered`,
    `internal/auth/deployment.go:89`). So "who has an agent" is ordinary
    read access on the sealed component, as the plan says.
- **Minted in one place**: `Auth.DelegateFromRequest`
  (`internal/auth/delegate.go`). It requires: not on a device's tunnel; a
  live instance token; a *user* partition with a uid; a holder in the
  table; the main deployment; the person live; the off-switch hook
  answering nil. It returns `Principal{UserID, User, Access, Via:
  "delegate", Agent: <holder>}` — `Component` empty, no `Gen`, never
  `Impersonator`.
- **Asked in one place**: `authedAPI` (`internal/server/devicelogin.go:72`)
  hands every `/api/` request carrying the scheme to `serveDelegate`
  before any other credential on it is looked at. `fromRequest`
  (`internal/auth/auth.go:497`) does not know the scheme, so on `/c/`,
  `/ws/term`, `/ws/events`, `/docs/`, the `/xbin/*` pages, the tile
  origins and the login routes a delegate credential is nobody: 401
  (tested). That is the structural half of default-deny.
- **Never an admin**: `Principal.IsAdmin()` is false when `Agent` is set
  (`internal/auth/auth.go:129`). An admin's agent still holds the admin's
  tile levels, because `users.Access.tileLevel` gives an admin terminal
  level everywhere (`internal/users/orgs.go:1028`). Side effect, wanted: a
  tile agent an admin's delegate opens runs in the **restricted** sandbox
  tier (`internal/term/openopts.go:18`), less than the admin clicking.

Alternative considered: a second token minted at spawn
(`XBIN_DELEGATE_TOKEN`). Same theft exposure (same env), needs runner and
boot changes, and buys only separate revocation, which the per-request gate
already gives. Kept as an option if S1's runner wants it.

### 2.3 Off switches
`Auth.SetDelegationGate(func(component, userID) error)` is asked on every
request; no hook means nothing delegates. B3 fills it with: the workspace
setting; the person's pause (a per-user flag, PersonOnly route); the
"agent is set up" precondition (an LLM configured). The org switch is not
here: it depends on the *target* tile (section 3.4). Known gap: a response
stream already open (following a session's events) outlives a pause; B3
registers delegate streams per person and cancels them on pause.
WebSocket upgrades are refused outright for that reason.

## 3. The delegation cap

### 3.1 Mechanism
`internal/server/delegateclass.go` is a third table beside
`deployclass.go` (D127r) and `partitionclass.go` (PD-29): route pattern →
`DelegateRead` | `DelegateAct` | `DelegateNever` (with its reason) | no row
(refused). `serveDelegate` (`internal/server/delegate.go`) resolves the
person, classifies, answers a dry run, runs the call through the handlers
everyone uses (`handleAPI`, so the deployment and partition gates and the
governance audit line still apply; the component proxy, so the broker's
`Route` decides tile calls), and writes the audit row.

Defence in depth beside the table: `IsAdmin()` false; the partition
table's `PersonOnly` rule now also refuses a principal with `Agent`
(`internal/server/partitionclass.go:137`) — a person's consents are how
they let the agent in, so the agent must never give them; the proxy strips
the delegate credential before a backend sees it
(`internal/proxy/proxy.go`, `identify`).

### 3.2 Route-class table

| Class | Routes | Verdict | Why / at what level |
|---|---|---|---|
| UI reads | `GET /whoami`, `/components`, `/components/{path...}`, `/screens`, `/tile-report`, `/alerts`, `/templates`, `/agent/providers` | allowed | handlers already filter by the person's read (`CanReadTile`) |
| Logs | `GET /logs` | allowed | terminal level on the tile, as for the person (`internal/obs/logs.go:45`) |
| Tile status | `GET /tile-status` | listed, **unconverted** | handler answers admins and the tile itself only (`internal/boot/api.go:289`); needs "person with read" |
| Session directory, history | `GET /term/sessions`, `/term/sessions/{id}`, `/term/sessions/{id}/events`, `/agent/history`, `/agent/history/{id}/events` | allowed | the person's own (`HomeKey`), creator-or-terminal-level gates unchanged |
| Code reads | `GET /code/tree`, `/code/file` | allowed | read on the tile (`internal/broker/code.go:155`); used by the research sub-agent only (engine rule) |
| Tile API | `/api/<tile>/…` | allowed | read on the tile; role clamped to the person's level; section 4 |
| Agent sessions | `POST /term/sessions`, `…/prompt`, `…/queue`, `DELETE …/queue[/{qid}]`, `…/cancel` | allowed (act) | the person's terminal level; create narrowed (section 5) |
| Tile creation | `POST /templates/new` | allowed (act) | the person's own creation rule (`canCreateAt`, `internal/broker/policy.go:115`) |
| Answering a tile agent | `…/permissions/{pid}`, `…/elicitations/{eid}` | **never** | the person answers |
| A session's settings | `…/options`, `…/restart`, `PATCH …/{id}` | **never** | mode and pickers decide what the session asks |
| Ending, deleting | `DELETE /term/sessions/{id}`, `DELETE /agent/history/{id}`, `POST /lifecycle` | **never** | `/lifecycle` mixes enable, disable, offload and delete in one route |
| PTY | `/ws/term` | 401 | not on the API plane at all |
| Code writes | `/create`, `/clone`, `/git/import`, `/builtins/*`, `/code/prs`, `/code/pr/*` | **never** | |
| Account | `/account/password`, `/devices/*`, `/web-ticket`, `/login`, `/invite/redeem` | **never** | |
| Other identities | `GET /frame-token`, `POST /path-tickets`, `/impersonate*` | **never** | a frame token would be the tile at its self-role: an escalation over the clamp |
| The person's own decisions | `/partitions/consents`, `/partitions/binds`, `/partitions/share-log`, `/partitions/credential-confirm` | **never** | also `PersonOnly` |
| A tile's stores | `/kv`, `/blob`, `/vault`, `/bus/publish` | **never** | through the tile's API instead |
| Admin API | grants, bindings, limits, access, owner, users, orgs, policy, auth settings, vault, backup, workspace settings… | **never** | change sets (section 7) |
| Everything else (about 200 routes) | | refused (no row) | a new route is not delegated until someone decides |

The table has 24 delegated rows and 78 never rows. In the prototype a
delegate reaches 23 of the 306 inventory routes (`TestDelegateAuthzMatrix`;
the 24th is the unconverted one).

### 3.3 Is an allowlist by route class enough? No — three kinds of mixing
1. **One pattern, several acts, decided by the body.**
   `POST /term/sessions` (mode, options, VM/GPU, net, `resume`,
   `?deployment=`), `POST /templates/new` (owner, org, path),
   `POST /lifecycle` (kept whole on the never-list). These are narrowed in
   the handler or the layer below, where the act is known
   (`delegateOpenRefusal`, `internal/term/agent.go:231`).
2. **Handlers that answer a person only through their tile's frame.** They
   key on `p.Component` and refuse a person-shaped principal (fail closed):
   `GET /tile-status` today. Tracked in `delegateUnconverted`, as the
   partition plan tracked `partitionUnconverted`.
3. **Reads that name or list tiles, which the org filter must cover**
   ("calls are filtered by the tile's org"): tile-addressed reads
   (`/code/*`, `/logs`, `/tile-status`, session routes through the
   session's `cwd`) need the gate to know the tile; listings
   (`/components`, `/tile-report`, `/alerts`, `/term/sessions`,
   `/agent/history`, `/screens`) need per-row filtering in the handler.
   **Not built in the prototype**: only tile API calls pass the org seam.
   B3 adds a per-row `tileOf(r)` extractor to the table (which also fills
   the audit row's tile) and a `p.Agent` filter in the six listings.

### 3.4 Org constraints and policy rows
- Existing policy rows (`deny`, `mayCall`) are **caller-side** ceilings:
  `Ceiling(from)` holds the rows matching the *calling* tile
  (`internal/broker/policy.go:35`). The holder is workspace-owned, so no
  org's rows apply to it. A workspace row matching the holder's path with
  `mayCall` narrows every person's agent at once; `DelegateTile` asks it
  (`internal/broker/delegation.go`).
- The owner's rule is **target-side** (the tile's org decides). That is
  new: the seam `delegateTileGate(holder, user, tile)` is where §5.6's
  `Org.Agents` goes. The on/off part can be a new deny kind, `agent`, on
  the org's policy rows — `ingress` is already a target-side kind — with
  models and harnesses in `Org.Agents`. The same gate must be asked by
  `OpenAgentWith` for delegates (a `Manager` hook; not built).

## 4. What a tile sees

A delegate's call is attributed exactly like an F5 call
(`internal/broker/partitionglobal.go:63`), reusing `Decision.Attribute`:

| Header | Value |
|---|---|
| `X-XBin-From` | `user:<id>` |
| `X-XBin-User`, `X-XBin-User-Level` | the person, their level on the tile now |
| `X-XBin-Role` | `reader` for read, `writer` for write and terminal — **never `admin`**, an admin's agent included |
| `X-XBin-Agent` | the holder's path (new; stripped inbound like every `X-XBin-*`) |
| `X-XBin-Partition`, `-Id` | the person's partition, on a partitioned tile |

Why `From` is `user:<id>` and not the holder's path: the agent template in
the wild classifies callers in `principal()`
(`builtin-templates/agent/_backend/access.go:49`). A `From` that is another
component becomes `whoElement`, and `whoElement.manager()` is **true** —
every AgTT instance would treat the workspace agent as a tile manager,
whatever the person's level. `user:<id>` with a user falls into `whoUser`
at the person's level, in AgTT and in `sandbox-terminal`
(`builtin-tiles/sandbox-terminal/backend/tile.go:103`). `llm-gw`'s
"everything" view requires `User == ""`, so it is not reached.

Would a tile mistake a delegate for the person in a harmful way?
- **Yes, by design, for everything the person's level allows**: an old
  tile cannot tell the agent from the person. A mail tile's `POST /send`
  runs. This is the reason for the engine's approval on every non-GET tile
  call, and for the open question on xbind-verified approvals (9.2).
- **One implicit invariant breaks**: until now `From: user:<id>` reached a
  backend only for admins, always with `Role: admin`. A tile that reads
  the prefix as "an admin" without reading the role would over-trust a
  non-admin's agent. None of the shipped tiles do; the changelog must say
  it, and `xbin.Caller` should gain `Agent` plus a helper.
- `X-XBin-Agent` alone is **not enough for old tiles**. Proposal: a
  manifest knob `"agent": "off" | "read" | "full"` (additive), enforced by
  xbind in `DelegateTile`; default for a tile that says nothing is the
  owner's call (9.1). `GET`s with side effects stay the tile's bug either
  way.

Partitions and consent: a delegate reaches the person's **own** partition
(`addressedPartition`'s person branch,
`internal/broker/partitionroute.go:159`), and with `partitionConsent` on
it needs their consent for holder → tile like any other tile's code (new
case in the same function; tested). Finding: `consentOrAsk`
(`internal/broker/partitionconsent.go:80`) prompts only for edges an admin
approved; the holder has no grants, so B3 must treat a sealed holder's
edges as approved or the person is never asked. Each delegate tile call
should also be counted in the partition's egress ledger
(`partitionEdgeSeam`); not wired.

Other interactions:
- **View-as**: an admin viewing as X never gets X's delegate. The
  credential lives in the partition backend's env, never in a browser;
  view-as cannot open a partition (`partitionroute.go:190`), so it cannot
  reach or start X's agent; no delegate principal carries `Impersonator`
  and `DelegateTile` refuses one that does; the audit read refuses view-as.
- **Deployments**: only the main deployment's partition delegates; a
  delegate names no deployment and reaches primaries only. `?deployment=`
  on a session create still follows the person's gates — B3 should clamp
  it to the default.
- **Personal plane (D88)**: personal tiles are reached at the owner's
  level. Self-approving a grant on one's own tile is `POST /grants`: on
  the never-list, since an injected agent approving `net` for a personal
  tile is an exfiltration path. Personal binds are `PersonOnly`.

## 5. "Cannot answer a permission or a question"

Three layers, the first two tested on the real handlers
(`TestDelegateCannotAnswerOrLeaveTheAPI`):

1. `permissions/{pid}` and `elicitations/{eid}` are on the never-list; the
   person's own session reaches the same handlers
   (`internal/server/agentapi.go:480`, `:505`). The precedent is
   `driveOther` (`agentapi.go:180`), which refuses an agent session's own
   token for the same reason.
2. `options` and `restart` are on the never-list: a session's mode decides
   whether it asks at all.
3. `POST /term/sessions` by a delegate is refused unless the mode is the
   provider's default **and** `Provider.Safe(mode)`
   (`sdk/acp/providers.go:222`), with no option but `model`, no VM, no
   GPU. `acceptEdits` and `auto` are refused too: they are not "explicit"
   modes but they stop the asking for edits and commands.

B3: the person's server-side agent preferences (plan §5.3) say which modes
and which net/API pickers their agent may use; the delegate's request is
then checked against the preference, never trusted. Today **net and api
pass through at the person's own clamps** — see threat T4.

## 6. Audit log

- **Store**: `internal/delegation` — `data/delegation/<hex(user)>/
  audit-YYYY-MM.jsonl`, 0600, xbind's own directory (no sandbox writes it,
  D78; no tile reads it, the holder included).
- **Row**: time, user, agent, method, path **without query**, tile, class,
  side-effect flag, dry-run flag, status, refusal reason, coalesced count.
  Never a body, query or header.
- **Volume**: every side effect and every refusal is a row; successful
  reads are one row a minute per (method, path), the rest counted into the
  next row.
- **Retention**: 13 monthly files, pruned when a month starts. Proposal:
  a workspace setting for the length, and inclusion in `bx backup`.
- **Read API** (handler written, unmounted): `GET /delegate/audit
  [?before=&limit=&user=]` — the person's own session, app or device;
  an admin with `?user=`; never the agent, a tile, view-as or the root
  token. Partition class `PersonOnly`-like.
- Side effects and refusals also go to the daemon's `audit` line with
  `agent=`. Sessions the agent opens carry `origin: "agent"` in
  `SessionInfo`.

## 7. Admin change sets (design only)

**Rule**: a delegate never calls the admin API. It submits a batch; xbind
describes it; a person approves in their own session; xbind applies as
that person.

- **Opt-in**: a per-user flag, "let my agent propose admin changes", set
  by a `PersonOnly` route; only workspace admins and org admins can set it.
- **Kinds are a closed list**, each a Go type with `Validate`, `Preview`,
  `Apply`: `grant.add`, `grant.revoke`, `binding.set`, `binding.remove`,
  `org.share` (a tile → org level), `limits.set`. There is no generic
  "call this admin route" item, so passwords, devices, SSO, the vault and
  impersonation are excluded by construction, not by a filter.
- **Data model** (`data/delegation/<hex(user)>/changesets/<id>.json`):
  `{id, user, agent, title, created, expires, state, items[]}`; item:
  `{n, kind, args, why (the agent's words, shown as such), line (xbind's
  plain-language sentence, generated from kind+args), before, after,
  warnings[], blocked, basis (hash of the before-state), result}`.
  States: proposed → applying → applied | partial | rejected | expired
  (7 days). Limits: 20 items a set, 10 open sets a person.
- **Dry run / diff**: `Preview` reads the current state and runs the
  handler's own checks without writing. The handlers interleave checking
  and writing today (`grantMutation`, `internal/broker/broker.go:719`;
  `apiBindingSet`, `internal/broker/netfn.go:728`), so each kind needs its
  check split out: policy ceiling (`ceilingBlockMsg`), the partition
  warnings `POST /grants` already returns, "who gains or loses access",
  which backends restart, whether an inverse exists.
- **Apply**: in order, each item replayed through the **real handler**
  with the approver's principal (an internal request through `handleAPI`),
  so validation, ceilings, restarts, events and the governance audit line
  are the handler's. Each item is atomic as its handler is; the set is
  not. An item whose `basis` no longer matches is `stale` and not applied
  (the person approved a diff that no longer holds). On the first failure
  the rest are `skipped`; applied items stay, each with an xbind-generated
  inverse offered as a new change set ("undo").
- **Approval API**: `POST /delegate/changesets` (delegate; an act row),
  `GET /delegate/changesets[/{id}]` (the person; the delegate reads its
  own sets' state), `POST …/{id}/approve {items}` and `…/reject` (the
  person's own session, app or device with the rights the items need —
  never the delegate, a tile's frame, view-as or the root token),
  `DELETE` (withdraw). A `changeset` event to the person's own sockets and
  a push; cards in the dock and on iOS.
- **Audit**: the proposal and each decision in the person's delegate log;
  each applied item on the governance audit line as the admin, with
  `changeset=<id>`.
- **Estimate**: 900–1,200 lines of Go with tests, plus the four check
  splits (about 300 lines of careful refactoring in grants, bindings, org
  shares, limits).

## 8. Threats

**T1 — prompt injection drives the delegate** (it reads tile data, session
output, logs, code).
- xbind: the cap; `IsAdmin` false; no PTY, code write, identity or
  consent; side effects audited; tile agents open only in an asking mode;
  admin changes only as change sets with xbind-written lines and diffs;
  the org gate; the pause.
- engine: approval on every side effect with the dry-run preview; scope;
  "data, not instructions".
- **Residual**: reads are free, so data read from tile A can leave through
  an approved write to tile B (once "always for this tile" is given), and
  a tile's `GET` may have side effects. Proposals: rate-limit acts per
  person in the gate; 9.2.

**T2 — the token is stolen from the component's sandbox.**
- It is the partition backend's `XBIN_TOKEN`
  (`internal/runner/deployments.go:297`). The component is sealed (no
  terminal, code tab or PRs), and its engine class has no sandbox tools,
  so the model cannot read its own environment.
- A stolen token is worth the cap, not the person's session: 23 routes and
  tile calls at the clamped role, while that generation lives and
  delegation is on. It is stripped before any backend sees it (tested).
- **Residual**: without isolation (tier 1, `plans/auth.md` §9) a sibling
  tile reads env through `/proc`. Proposals: delegation refuses to run
  with isolation off outside `--dev`; honour the scheme only on the
  gateway socket (all tiles share `gateway.sock` today, so this stops use
  from outside the host, not from another sandbox); 9.2 makes a stolen
  token read-only.

**T3 — confused deputy.**
- *Into the holder*: another tile holding a grant on the holder would
  reach "alice's partition of the agent" as alice's data
  (`routePartition`). The sealed table must declare no exposed roles, and
  the grant and binding APIs must refuse a sealed target. Bus and mail
  deliveries into the holder are untrusted input.
- *Through a tile*: the called tile acts with its own grants, as when the
  person clicks; it cannot replay the delegate credential.
- *Wrong `From`*: section 4 (AgTT would have made the holder a manager).
- *Frame tokens and path tickets*: never minted for a delegate.

**T4 — a tile agent the delegate started has terminal-level power in that
tile.** Not an escalation over the person: it needs the person's terminal
level, it is the person's session (their directory, home and tile token),
and for an admin it is the restricted tier. But it is the one path from
the cap to shell and code writes, so:
- the mode clamp and the never-list (section 5) keep every ask with the
  person; the engine asks before starting one; `origin` marks it.
- **Residual**: in an asking mode a harness still reads files and runs
  read-only commands without asking. An injected brief can read the tile's
  code and data and the person's shared `$HOME` (`homes/<user>`: harness
  logins, whatever keys they left there) back into the agent's context,
  and the session's network picker is the person's clamp, not a default.
  Proposals for B3: pickers from server-side preferences only; `net` none
  beyond the LLM endpoint unless the person allowed more; consider a
  reduced `$HOME` for `origin: agent` sessions.

## 9. Open questions for the owner

1. **Tiles that say nothing about agents**: full access at the person's
   level (most useful; old mail tiles send on an approved call), or
   read-only until the tile declares `"agent": "full"`? My default: full,
   because the engine's approval covers writes — unless 2 is declined.
2. **Should xbind verify approvals of side effects?** Today the approval
   lives in the engine. With "write tickets" the person's own frame asks
   xbind for a one-shot ticket bound to (method, tile, path, body hash),
   and the gate refuses a side effect without one; standing approvals
   ("always for this tile") become xbind records the person can revoke.
   It makes a stolen token read-only and unifies with change sets. Cost:
   about 400 lines. I recommend it.
3. **Who approves a change set**: only the person whose agent proposed it
   (my default), or any admin?
4. **Step-up for approving a change set** (a recent sign-in, as for
   devices)?
5. **Admin reads**: an admin's agent sees the non-admin view (`IsAdmin`
   false). To propose bindings it needs to read grants, bindings, orgs and
   limits. Add an `admin-read` class, open only while the admin has opted
   in?
6. **Session reads beyond events**: `…/log` and `…/diff` are unlisted (the
   agent "can't read tile code"; a diff is code). Keep?
7. **Ending sessions**: the agent can cancel a turn but not end a session
   it started. Allow `DELETE` for `origin: agent` sessions only?
8. **Audit retention** (13 months) and whether reads are logged at all.
9. **The org switch as a policy deny kind** (`agent`) now, `Org.Agents`
   for models and harnesses later?

## 10. Prototype: what is proven, what is not

Proven by tests (all in this branch):
- `internal/auth/delegate_test.go` — the principal; every credential that
  delegates nothing; the off switches; nobody outside the gate.
- `internal/server/delegate_test.go` — the matrix over **all 306 routes ×
  10 principal kinds** (person, admin, view-as, frame, backend,
  non-primary deployment, nobody, two delegates, a non-delegate token
  under the scheme); the never-list; answering refused where the person's
  session is served; an agent session create as the person; a tile call
  handed to the proxy as the person; dry run; pause; audit rows and the
  read handler.
- `internal/broker/delegation_test.go` — `Route` for a delegate: clamp,
  attribution, partition, consent, the org seam, every refusal; people in
  person unchanged.
- `internal/proxy/delegate_test.go` — a delegate's read reaches a backend
  socket with exactly the headers of section 4.
- `internal/term/delegate_test.go` — a session opened by a delegate with
  the fake ACP agent: the person's directory, home, `origin`, mode clamp.

Not proven: a real sealed component end to end (needs S1); `make
integration`; the org filter on `/api/xbin` reads; streams cut on pause;
change sets; the consent prompt for the holder's edges.

Suites run: `go test ./internal/{auth,server,broker,proxy,term,apicheck,
delegation,confine}`. Not run: `make check`.

## 11. Change list for B3

| File | Change | Lines (code + tests) |
|---|---|---|
| `internal/auth/delegate.go`, `auth.go`, `partition.go` | done in the spike; swap the table for S1's | 150 + 160 (done) |
| `internal/server/delegate.go`, `delegateclass.go` | done; add `tileOf` extractors, stream registry, rate limit, mount the audit route | 500 + 560 (done), +250 |
| `internal/server/partitionclass.go`, `deployclass.go`, `openapi*.go` | rows for the new routes | 60 |
| `internal/broker/delegation.go`, `deploydata.go`, `partitionroute.go` | done; ledger count, sealed-target refusal in grants and bindings, consent prompt for holder edges | 90 + 130 (done), +200 |
| `internal/proxy/proxy.go`, `partitionroute.go` | done | 20 + 120 (done) |
| `internal/term/agent.go`, `sessions.go`, `term.go` | done; preferences-driven modes and pickers, the org hook, `origin` across restart | 50 + 90 (done), +200 |
| `internal/delegation/audit.go` | done; backup inclusion, retention setting | 200 + 80 (done), +80 |
| `internal/delegation/changeset*.go`, broker check splits | change sets (section 7) | 1,200–1,500 |
| `internal/users`, `internal/wssettings` | pause flag, admin opt-in, agent preferences, workspace switch, `Org.Agents` / deny kind | 400 |
| six listing handlers, `GET /tile-status` | `p.Agent` org filter; conversion | 200 |
| `internal/boot` | wire gate, log, policy | 60 |
| `internal/apicheck` | `TestDelegateRouteClasses` over the real inventory | 80 |
| `sdk/` | `Caller.Agent`, `xbin.AsUser()` client (zero-dependency) | 120 |
| `docs/auth.md`, `docs/protocol.md` (after its split), `docs/partitions.md`, `docs/changelog.md`, `plans/DECISIONS.md` | the contract | docs |

Done in the spike: about 1,000 lines of code and 1,150 of tests. Remaining
for B3: about 3,000–3,500 lines, half of it change sets.
