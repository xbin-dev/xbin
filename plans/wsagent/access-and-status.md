# Workspace agent — a person's access, one read for a tile, the context budget

> Status: live — the three items the owner chose from the real-model
> feedback (`plans/wsagent/real-model-feedback-2026-11-17.md` §5.1, §5.2,
> §5.3, with the small items of §5.5–5.6 that belong with them). Decision
> D246. Branch `wsagent/access-status`.

The feedback's job 9 ("give Dave read access to Orders") spent 45 model
calls finding that no change names a person; jobs 3, 4, 6 and 10 spent
3–10 calls a tile assembling where it stands, and job 4 left a page one
grant short because nothing showed the grant was pending; and with the
window known the agent's history budget became 60 % of it. This is what
is built for each, the rule each keeps, and where it is tested.

## 1. `access.set` — a person's level on a tile

A change-set kind, in `admin_propose` sets and in plans:

```
access.set {tile, user, level}     PUT /access {tile, kind: "user", id, level}
level: "none" | "read" | "write" | "terminal" | ""   ("" removes the person's own entry)
```

**Applied through the by-hand path, never a parallel one.** The line's
call is the one the tile's access dialog (the shell's tile settings,
`/xbin/system`'s access rows, `bx access`) sends: `PUT /access` with
`kind: "user"` — the handler's `accessCheck` (who manages the tile's
access: its owner, the owning org's admins, a workspace admin) and the
users store's `SetUserTile` (the level, a system component's read-or-none,
the account). Applying runs it through `handleAPI` as the approving
person, with their account and access read again at that moment
(`currentPerson`), so a right lost between the proposal and Apply fails
the line. The admin console's People tab writes a person's whole map
(`PATCH /users/{id}`); one tile's entry is `PUT /access`, which is what
the line is.

**An exact entry is what it sets.** A person's own entry on a tile is
authoritative (D31): `none` excludes them whatever their orgs and the
workspace defaults give, a level sets it outright, and `""` removes the
entry — their access is then what their orgs, patterns and the defaults
give. The line's before and after are the person's *effective* level,
computed with the entry changed (`users.Access.WithEntry`), so "read →
read (from the org sales)" is said when removing an entry changes nothing.

**Personal or an admin's — decided by the by-hand rule.** A line is
*personal* exactly when the proposing person is the tile's user-owner
(`approverSelf`): the branch of `mayManageTile` that holds with no admin
right, the same rule that makes a binding of one's own tile personal
(D88). Everyone else who may change the tile's access by hand does it
with an admin's right — a workspace admin, or an admin of the org that
owns the tile — and so their line is an *admin's*: unticked on xbin's
review, ticked and applied on a recent sign-in. A person who may not
change it at all gets "needs an admin" and **Ask an admin**. Proven by
`TestAccessLinePersonalIsByHand`: over random people (owner or not,
workspace admin, org admin of the owner or another org, member), tiles
(owned by a person, by an org, by the workspace) and targets, a line is
personal ⇔ `PUT /access` by that person *with every admin right taken
away* succeeds by hand — and an admin's line is one the person's full
rights pass.

**Warned, unticked, and held by xbind** — three kinds of line carry a
warning, so they start unticked on every review (web and app):

- raising to `terminal` — `TERMINAL: … can run code in <tile> and change it`;
- any change on a system component (`xbin/…`) — `SYSTEM COMPONENT: …`;
- the proposing person's own level — `YOUR OWN ACCESS: …`. For anyone but
  a workspace admin such a line is **blocked** (it can't be ticked, asked
  or applied: one's own access is changed by an admin on xbin's own pages,
  or asked for with the tile's Request access); for a workspace admin it is
  an admin's line, warned, never personal — deliberate: an unticked line,
  their own tick and a recent sign-in.

The approve call names lines explicitly (`items`, nothing applied by
default — already the rule), and a line of a kind newer than the client
is refused (below): xbind, not the review's default ticks, is what
holds a line nobody chose.

**Refused as no change at all** (400 at the proposal, checked again at
Apply), as `org.share` is: the workspace agent itself (`xbin/wsagent`:
who has an agent is its setup's — `notOnAgent`), a tile that holds `xbin`,
`cap:*`, `gpu:*` or the bare `code` when the level is above `none` (who
may use such a tile is its Access page's, by an admin), a tile an earlier
line of the plan creates (a person's access is set on a tile that exists —
it never waits and is never deferred), and names that aren't names
(`lineNames`).

**The org rules and the tile's agent level apply**: in a plan, every tile
a line names is held to what the proposing conversation may touch
(`lineAgentRefusal`: readable, in scope, not `agent: "off"`, its orgs let
the agent in); `admin_propose` holds an `access.set` line to the same
check.

**The line is xbind's, with before and after**:
`“Dave Smith” (dave): no access → read on apps/invoices`. The display name
is quoted, stripped of control and format characters and cut at 60
characters; the id is what xbind enforces. The tile is named by its path
(a tile's title is its author's words, never xbind's).

### Whose account — resolving the name

The `user` argument is what the person said: an account id or a display
name. Who resolves it, and how, is what the person may already see of
accounts in xbin's own UI:

| The person | Sees of other accounts today | The line resolves `user` |
|---|---|---|
| a workspace admin | everyone (`GET /users`) | exact id, else exact display name (case-insensitive) — unique, or blocked "more than one account is named …" / "no account is named …" |
| an org admin | every account's id and name (`GET /users-directory`, for adding members) | the same |
| anyone else | nothing: the tile owner's access dialog takes an id typed by hand | not at all: the name is used as an id, as typed — the by-hand route's own answer at Apply says whether it is one |

So for a person who sees no accounts a proposal says nothing of whether
one exists: their personal line (their own tile) is applied by the by-hand
route, which answers "no such user" exactly as the dialog does; their
admin's line goes to the admins as typed, and an admin's review — as the
admin — resolves it and shows who it is (an unknown or ambiguous name is
a blocked line there, never applied).

### The agent learns applied or failed, never the access list

`setFor` — what the agent, its conversation and its page read of a set —
gives of an `access.set` line, **always** (an admin who opted in to admin
reads included: `GET /access` is no admin read of an agent's),
`n`, `kind`, `args`, `why`, the flags, `needs`, `result`, and a line
written from the arguments alone ("Set the access of “Dave” on
apps/invoices to read") — never xbind's resolved line (it holds the
before), the display name, `before`, `after`, their words, `effects`,
`warnings`, `basis` (a hash of the before: a level is one of five),
`inverse` or `error`; a blocked line reads the one fixed sentence. This
holds for the proposal's answer, a plan's dry run, both GETs and a
withdrawal. `TestAccessLineRedactedForAgent` fails without it.

### An older client can't apply a kind it doesn't know

`POST …/approve {items, knows?: ["access.set"]}`: a chosen line of a kind
newer than the eight a client of before this knows is applied only when
`knows` names that kind — otherwise `409 {error, kinds}` and nothing is
applied. The web chrome and the app send it; an older app (which ticks
any unblocked line of a non-plan set, and shows a plan's unknown line
untickable) gets "update the app". `TestApproveRefusesAKindTheClientDoesNotKnow`.

### The lookup

```
POST /delegate/accounts/lookup   the person's agent, under its session. {name}
                                 → {name, exists?, id?, displayName?, ambiguous?, unseen?, why?}
```

- **Who gets an answer:** a conversation whose person is a workspace admin
  or an org admin — people who can read every account's id and name in
  xbin already (`GET /users-directory`). Anyone else gets `{name, unseen:
  true, why}` for every name alike: "your person can't see the
  workspace's accounts: propose the line with the name as they gave it;
  an admin's review resolves it".
- **Exact match only:** the account whose id is the name (as ids are
  normalised), else the accounts whose display name equals it ignoring
  case and the spaces around it. No prefix, no substring, no list.
- **At most** `{exists, id, displayName}`; two or more by display name:
  `{exists: true, ambiguous: true}` — "more than one — ask your person
  which (by account id)", never the list.
- **Rate-limited and audited:** an act of the gate (counted in the 30 a
  minute, a row in the person's log with the path and never the name),
  and a share of its own per conversation (10 at once, one more every 6 s,
  `429`).
- **Enumeration:** `TestLookupNoOracle` — for N random names, an account
  that exists and a name that doesn't answer byte-for-byte alike to a
  person who sees no accounts (status, body, headers but `Date`), and the
  lookup's answers never differ between a name that exists and one the
  person may not see.

## 2. `GET /delegate/tile` — where one tile stands

```
GET /delegate/tile?tile=<path>   the person's agent, under its session.
  → {tile, title, kind, runtime, orgs, level, agent, agentRequested?, mcp?,
     owner: {kind, id, name?}, backend: {state, error?, restarts?,
     retry?: {attempt, of, at}, crashLoop?, words}, log?: {lines, shown,
     why?}, slots: [{slot, kind, service?, required, multi?, bound?}],
     wiring?: false, uses: [{target, role, granted?, pending?}],
     provides: [{slot, kind, service?, role?}], exposes: [{slot, kind,
     paths?}], cron?: [{name, schedule, lastRun?, nextRun?, lastResult?}],
     sessions: [{id, by, status, waits, turn?, last?, mode?, provider?}],
     words: [...]}
```

**Class:** `read`, `audit` (it carries a log's tail), `tile:byTileQuery`
— the gate's own checks for a read that names a tile: the person can read
it (one answer for a tile that isn't there and one they can't see), it
is in the conversation's scope ("hidden by you"), its orgs' rules let the
agent in on the conversation's model; then the conversation is tainted
with the tile's orgs, as any read. Partition `neutral`, deployments
`primary only` (the primary, as the reads it replaces), a system
component `refused` (`?tile`: the agent acts on tiles, never on xbin's
own components).

**Nothing the reads it replaces wouldn't have given this agent** — each
part is the read the agent (or the person, where noted) makes today,
under that read's own gate:

| Part | The read it replaces | Its gate, unchanged |
|---|---|---|
| title, kind, orgs, agent level, mcp | `GET /delegate/map` | the map's row (readable, in scope, not blocked) |
| runtime | `GET /components/<path>` | readable |
| level | the person's level (`X-XBin-User-Level` of a call; `/whoami`) | readable |
| owner | `GET /owner?tile=` (any reader); a person's display name only to someone who sees accounts (§1), else the id | readable |
| backend, restarts, retry, crash loop | `GET /tile-status` | readable (a delegate reads a tile its person reads) |
| the log's tail | `GET /logs` | terminal level on the tile, as for the person; else `shown: false` and why |
| slots: declared, required/optional | the manifest (`GET /code/file?file=xbin.json`) | readable |
| slots: what each is bound to | the person's own `GET /bindings` view | that view's rule: a tile they can write, own, or whose org they admin — else `wiring: false` and nothing of the bindings |
| uses: granted or pending | the person's own `GET /grants` view (rows and `pending`) | that view's rule, as above |
| provides, exposes | the manifest | readable |
| scheduled jobs | `GET /cron/jobs` from a terminal on the tile (`bx cron`) — a tile's own credential reads its jobs | an admin, or terminal level on the tile; else absent |
| coding-agent sessions | the person's `GET /term/sessions?compact=1` | their own sessions (terminal levels), less the tiles the orgs keep the agent out of; `last` only as the compact listing gives it |

The `agent` level of the tile is honoured exactly as those reads honour
it: none of them is refused at `off` (a tile's level governs its API and
its plan lines), so this one isn't either — and it says the level, as the
map does. Proven by `TestTileStandingIsUnionOfReads`: for a tile at each
agent level (`off`, `read`, `full`), for a person at read, write and
terminal level and as owner, every field of the answer is checked against
the union of those reads made by the same delegate (or person, where the
table says) — and a hidden tile, one outside the scope and one an org
blocks answer the gate's refusals, with the conversation tainted only
where the read went through.

**Crash loops are said plainly.** The runner's own states: `failed` with
its error; a start retrying (D242: `retry {attempt, of, at}`) is
"crashing on start: <last reason> — xbin retries it (2 of 3) at 14:05"
with the log's last lines; the crash-loop breaker's error ("exited N
times") is "keeps exiting"; `restarts` since its last start counts exits.
`idle` says what it says and doesn't: not running now, starts on the
first call.

**Not in it:** the tile's template (xbind keeps no record of it outside
the tile's git history, which is not read for an agent — D78), anyone's
access to the tile (`GET /access` is never an agent's), other people's
sessions, the tile's data.

### The agent's tools

- `tile_status` becomes this read: one call says what the tile is, its
  backend in words (a crash loop first), its slots and what each is bound
  to, its uses with **granted** or **pending — a plan's `grant.add` line
  asks for it**, what it provides and exposes, its scheduled jobs, the
  log's last lines and its coding-agent sessions. Against an xbind before
  the route it answers as before.
- `tile_agent_result` carries the live state (status, what it waits on,
  held messages) before the report; `tile_agent_status` is no longer
  offered and still answers (stored conversations call it) — for one
  release.
- `account_lookup {name}` — the lookup; `access.set` in `admin_propose`
  and `plan_propose`, their descriptions saying what it is for and that
  one's own access isn't the agent's to change.
- §5.6: a start's line and sheet say an asking mode "asks before it edits
  or runs anything" — xbind's line, the web chrome's words and the app's
  (the Swift table follows the JS by the generated vectors).

## 3. The context budget

The owner's decision: "Context budget - keep at 1M default, models can
deal with that, cost is fine".

- **A workspace setting**, beside the agent's web and MCP policy:
  `PUT /delegate/workspace {contextBudget: N}` (32 000 … 10 000 000
  tokens; `null` back to the default), default **1 000 000**.
  `GET /delegate/workspace` answers `contextBudget` (the admin's, absent
  when never set), `policy.contextBudget` (in force) and `budget: {model,
  window, keeps, margin}` for the connector's default model of the agent
  role. `GET /delegate/state` gives the person's agent `contextBudget`.
- **Host-presettable and lockable** like the web and MCP modes: the host
  file's `workspaceAgent.contextBudget` is a preset and a ceiling —
  `locks.contextBudget {locked, reason, ceiling}`, a `PUT` above it `409`,
  an admin may set less.
- **Effective budget** (the agent template, workspace mode only):
  `min(setting, cap(window))`, `cap(w) = max(today's rule, w − 64 000)`
  where today's rule is 60 % of the window, at least 32 000 and never
  above 80 % of it. The **margin is 64 000 tokens**: compaction is judged
  before each model call from the size the provider reported for the
  previous call, so the next call carries what one step added since —
  the model's reply, the results of every tool it called (each of the
  workspace agent's tool results is capped at a few thousand tokens) —
  plus room for the next reply; 64 000 covers a step of a dozen tool
  calls and a long answer, and doesn't grow with the window. Below a
  160 000-token window 60 % is the smaller cap, so nothing changes there.
  Unknown window: 32 000, as today (`min(setting, 32 000)`).
- Values: a 1 048 576-token model keeps 984 576; a 262 144-token model
  198 144; an unknown window 32 000. Compaction still happens at the
  bound; the hidden-output stub is unchanged ("only its first and last
  words, not a summary").
- **Shown** on `/xbin/system`'s workspace-agent card ("this model: 256k
  window → keeps up to 198 144 tokens of a conversation") and in the
  agent's own state (`GET /ws/state budget {tokens, from}`, the scope
  panel's model line).
- **Not a spend limit.** Per-person metering and quotas (the connector's)
  bound spend; the budget only decides how much of a conversation is sent
  again with each call. Said in the docs and on the card.

## Every new route, field, kind and setting

| What | Class (delegate golden) | Rule it keeps |
|---|---|---|
| kind `access.set` | — (a change-set kind) | by-hand path at Apply with current rights; personal = by-hand without admin rights; never waits; self / terminal / system warned, self blocked for non-admins; agent reads applied/failed only |
| `knows` on `…/approve` | the person's own (never delegated) | an older client never applies a kind it doesn't know |
| `POST /delegate/accounts/lookup` | `act` | answers only people who see accounts; exact match; no list; rate-limited, audited; same answer for unknown and unseen |
| `GET /delegate/tile?tile=` | `read`, audit, `tile:byTileQuery` | ⊆ the reads it replaces, per agent level, scope, hidden, org rule; taints like a read |
| `contextBudget` on `GET`/`PUT /delegate/workspace`, `policy`, `locks`, `budget` | never (admin's) | additive; host preset is a ceiling |
| `contextBudget` on `GET /delegate/state` | read | the setting in force, nothing else |
| `workspaceAgent.contextBudget` (host file) | — | preset and ceiling |
| `budget` on `GET /ws/state` (template) | the page's | — |
| tool `account_lookup`; `access.set` in `admin_propose` / `plan_propose`; `tile_status` rebuilt; `tile_agent_status` folded | — | prompt unchanged (at its cap): descriptions and results only |

## Not done

- A person's access through an org (`org.share` exists); a person's
  access to many tiles at once (a pattern) — an admin's page.
- The template a tile came from in the status read (above).
- Search by part of a name: exact only, by design.
