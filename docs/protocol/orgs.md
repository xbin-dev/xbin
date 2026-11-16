# Protocol: organisations, access, policy and limits

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
organisations and their members, permission sets, network
sets, tile ownership, access and access requests, policy ceilings, agent
constraints, and tile resource limits.

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Organisations, permission sets and network sets

```
GET    /orgs                      management view (docs/auth.md, ownership):
                                   admin/xbin:users → all orgs; a signed-in
                                   org admin → their orgs. {orgs:[{id,name,
                                   members:[{id,level,create,admin,
                                   suspended?,via?,viaGroups?}],tiles,
                                   sets,allow,policy,ssoGroups:[{group,
                                   level,create,admin}],resolvedAllow,
                                   ownedTiles,netSets:[…],resolvedNet:[…],
                                   netHost?}]}. via "sso" = the row was
                                   created by a group rule (D53) and
                                   follows the IdP; ssoGroups = the org's
                                   rules; netSets = attached network sets
                                   (D54), resolvedNet their rule union,
                                   netHost whether it grants host
                                   networking
POST   /orgs                      admin/xbin:users. create {id, name?}
                                   (id: [a-z0-9._-], immutable; "workspace"
                                   reserved)
PATCH  /orgs/<org>                admin/xbin:users, or that org's admin:
                                   {name?, members?} — members replaces the
                                   whole list (provenance via/viaGroups is
                                   store-owned: kept from the previous row,
                                   ignored in the body); a member entry's
                                   suspended:true pauses the membership
                                   (confers nothing, stays listed; D34).
                                   WS-ADMIN ONLY fields:
                                   {sets?, allow?, netSets?} — delegation
                                   is granted from above (D26/D28); xbin/
                                   xbin:* never valid in allow; netSets
                                   attaches network sets (D54) and
                                   restarts the org's net-declaring tiles
PUT    /orgs/<org>/members/<user> admin/xbin:users, or that org's admin.
                                   upsert ONE membership (D53): {level?,
                                   create?, admin?, suspended?, via?: ""}.
                                   Present fields overlay; a new row starts
                                   at read. via:"" DETACHES a synced row
                                   (manual from then on); any other via is
                                   refused — provenance is written by SSO
                                   sync only. → the org view; 404 unknown
                                   org/user
DELETE /orgs/<org>/members/<user> same gate. remove ONE membership → {org,
                                   removed, note?}; 404 when not a member.
                                   A synced row returns at the user's next
                                   sign-in while its rule stands — note
                                   says so
PUT    /orgs/<org>/sso-groups     admin/xbin:users (ws-admin — rules grant
                                   power). replace the org's IdP-group
                                   rules: {rules: [{group, level?, create?,
                                   admin?}]} — group = OIDC claim value,
                                   GitHub org/team-slug, or Google group
                                   email; matching rules union. Applied at
                                   each member's next SSO sign-in
                                   (docs/auth.md §Group sync, D53) → the
                                   org view
PUT    /orgs/<org>/limits         admin/xbin:users. the org's per-tile
                                   resource ceilings (D203): {memMiB?:
                                   n|null, diskGiB?, vmMemMiB?, vmVCPUs?,
                                   sbxMemMiB?, sbxVCPUs?, sbxDiskGiB?} —
                                   the most its admins may approve for one
                                   tile it owns (null or 0 removes a kind;
                                   unnamed kinds keep theirs) → {limits}.
                                   GET /orgs carries each org's limits
PUT    /orgs/<org>/agents         admin/xbin:users, or an admin of the org
                                   in their own session (§Agent constraints
                                   below). the org's constraints on agents
                                   (D229) → {agents, pin, bounds}. GET /orgs
                                   carries each org's agents and agentsPin
                                   (absent: none)
PUT    /orgs/<org>/agents/pin     admin/xbin:users. the rules a workspace
                                   admin pins on the org: its admins narrow
                                   below them, never loosen them
DELETE /orgs/<org>                admin/xbin:users; refused while the org
                                   still OWNS tiles (transfer first)
GET    /permission-sets           admin/xbin:users. {sets:{name:{allow,
                                   policy,termApi,termNet}}, attachedTo:
                                   {name:[orgIds]}, heldBy:{name:
                                   ["user:<id>"|"personal-defaults"|
                                   "new-accounts"]}} (D28, D88)
PUT    /permission-sets/<name>    admin/xbin:users. replace one set (same
                                   allow grammar/floor as org allow)
DELETE /permission-sets/<name>    admin/xbin:users; refused while attached
                                   to any org or held by a user, the
                                   personal defaults or the seed (D88)
GET    /net-sets                  admin/xbin:users. organisation network
                                   sets (D54): {sets:{name:{rules,
                                   created}}, attachedTo:{name:[orgIds]},
                                   boundBy:{name:[tiles bound to
                                   set:<name>, D65]}, heldBy:{name:
                                   [user:<id> | personal-defaults |
                                   new-accounts]} (D88: users' personal
                                   networks)}.
                                   Rules are the net: allowance grammar
                                   without the prefix — internet |
                                   internet:<host|host-glob|ip|cidr>[:port]
                                   | lan:<ip|cidr>[:port] | host |
                                   provider:<tile-glob>. Attached to an
                                   org, the union is at once the CEILING on
                                   its tiles' net bindings, what its admins
                                   may bind without an allowance, the
                                   default binding (`org`) of its tiles
                                   that declare net, and the egress of
                                   terminals opened on them. Held by a
                                   user (or the personal defaults) it is
                                   part of their personal network: the
                                   default (`personal`) of the tiles they
                                   own, a terminal scope there, what they
                                   may bind — no ceiling (D88)
PUT    /net-sets/<name>           admin/xbin:users. {rules:[…]} — create/
                                   replace (400 on grammar; one destination
                                   per rule; globs only in internet: host
                                   rules; lan: rules are addresses/CIDRs);
                                   attached orgs' net tiles and every tile
                                   bound to set:<name> restart →
                                   {name, rules, created, attachedTo}
DELETE /net-sets/<name>           admin/xbin:users; 409 while attached to
                                   an org, bound by a tile (D65), or held
                                   by a user, the personal defaults or the
                                   seed (D88)
```

## Ownership and access

```
GET    /owner?tile=<path>         any principal that can READ the tile.
                                   {tile, owner: "user:<id>"|"org:<id>"|""}
GET    /owner/preview             ?tile=&to= — transfer impact report
                                   (D39, never mutates): {allowed,
                                   callerLevel:{before,after},
                                   deadBindings, deadGrants, planeChanges}
                                   — what dies under the NEW owner's
                                   ceilings and what happens to YOUR level
POST   /owner                     transfer (D24/D39): {tile, to}. GIVE:
                                   ws-admin / the user-owner / an
                                   owning-org admin. RECEIVE into org:X
                                   needs X's Create knob ("transfer into ≡
                                   create in"); user:<self> with the GIVE
                                   right; user:<other> and workspace are
                                   ws-admin acts. Side effects: fully
                                   ceiling-dead binding slots are UNBOUND,
                                   affected backends restart, and the
                                   response carries the executed report
                                   (+unbound). The tile's deployments move
                                   with it: its deployment record's owner
                                   ref is rewritten in the same step, so a
                                   pinned primary stays pinned; a record
                                   that can't be written answers 500 and
                                   nothing moves. A transfer keeps every
                                   deployment, its settings and the edge
                                   policy, and restarts each running one
                                   under the new owner's ceilings
GET    /access?tile=<path>        ws-admin, the tile's USER-OWNER, or an
                                   owning-org admin. {tile, owner, entries:
                                   [{kind:user|org, id, level, source:
                                   exact|pattern:<pat>}], defaults: [{level,
                                   source}]} — defaults: the workspace
                                   defaults that reach it (everyone has
                                   them, no entry of their own; none on an
                                   org-owned tile). A system component is
                                   reached only by an entry naming it, so
                                   it lists no pattern in either (the
                                   workspace agent's `who: everyone` is
                                   its exact default)
PUT    /access                    same gate (sharing is an ownership right,
                                   D24). set/clear one EXACT entry: {tile,
                                   kind:user|org, id, level:
                                   read|write|terminal|""}; kind user also
                                   takes "none" = explicit EXCLUSION. Exact
                                   user entries are AUTHORITATIVE (D31):
                                   they override org level, shares, pattern
                                   entries and defaults — down as well as up
GET    /access/<user>             element: a tile's BACKEND (instance
                                   token), about ITSELF. {user, level:
                                   none|read|write|terminal, active} — the
                                   level X-XBin-User-Level would carry for
                                   that person now; none for a disabled or
                                   unknown account (active: false). For a
                                   credential the tile keeps past a call (an
                                   SSH key registered on its page): is its
                                   person still one of its users? Never
                                   another tile; never 404 (an unknown id is
                                   level none); frame/terminal tokens,
                                   people and admins 403. "user:<id>"
                                   accepted. SDK xbin.AccessOf
GET    /access-matrix             admin/xbin:users. users×components
                                   effective levels with provenance:
                                   {users, components, matrix:{user:{tile:
                                   {level, explain:[{level,source}]}}},
                                   owners} — sources: admin | owner |
                                   org-admin:<org> | exact (authoritative,
                                   D31) | org-member:<org> |
                                   org-share:<org>:<pat> | direct:<pat> |
                                   default:<pat>
GET    /access-requests           signed-in users / admin. pending human
                                   access requests (D36), scoped to the
                                   viewer: {requests: [{user, tile, level,
                                   note?, created, mine?, manage?}]} —
                                   mine = you filed it; manage = you may
                                   approve it (the tile's owner, its org's
                                   admins, or ws-admin)
POST   /access-requests           signed-in users. file {tile, level:
                                   read|write|terminal, note?} (≤20
                                   pending each; same-tile refiles
                                   replace; refuses levels you already
                                   hold, tiles you're explicitly excluded
                                   from (exact `none`), and re-files
                                   within 24h of a manager's dismissal). A signed-in human navigating to
                                   an unreadable /c/ tile gets this as a
                                   page (owner named + one-click request)
                                   instead of a bare 403
POST   /access-requests/approve   the tile's manager set. {user, tile,
                                   level?} — writes the exact ACL entry
                                   (level defaults to the requested one)
                                   and removes the request
DELETE /access-requests           {user?, tile} — withdraw your own (no
                                   cooldown); a MANAGER dismissing someone
                                   else's starts the 24h re-file cooldown
GET    /users-directory           admin/xbin:users, or any org admin. the
                                   minimal people list for pickers:
                                   {users:[{id,name}]} — identity only
```

## Policy ceilings

```
GET    /policy                    admin/xbin:users. workspace policy-ceiling
                                   rows {policy:[{tiles,deny?,mayCall?}]}
                                   (deny kinds net|gpu|xbin-caps|ingress)
PUT    /policy                    admin/xbin:users. replace the rows
GET    /orgs/<org>/policy         admin/xbin:users, or that org's admins
                                   (read-only — the ceilings their
                                   approvals can trip on). Rows apply to
                                   tiles the org OWNS
PUT    /orgs/<org>/policy         admin/xbin:users. replace them
```

## Agent constraints

An org says what agents may do on its tiles, and the workspace sets the
outer bounds (D229, [auth.md](/docs/auth.md) §Agent constraints). One
shape for both:

```jsonc
{"workspaceAgent": "on",              // "on" | "off": may people's workspace agents work on these tiles
 "harnesses": "any",                  // "any" | ["claude", "codex"] — ids of GET /agent/providers; [] allows none
 "models": "any",                     // "any" | "private" | ["local", "hosted/small"] — LLM connector ids:
                                      //   an upstream (all its models) or upstream/model; "private": a model
                                      //   of an upstream the connector marks private
 "web": "ask",                        // "ask" | "allow" | "never": a workspace agent's conversation that holds
                                      //   this org's data reads only URLs its person approves, reads freely,
                                      //   or never reads the web (D236); omitted when unset
 "mcp": "any"}                        // "any" | "org": such a conversation uses any MCP server the workspace
                                      //   allows, or only the MCP tiles under this org; omitted when unset
```

`web` and `mcp` are asked for every org whose data a workspace agent's
conversation holds — its session's taint — not for the tile a call names
([protocol/delegation.md](/docs/protocol/delegation.md) §Web and MCP).
Strictest wins: `never` over `ask` over `allow`, `org` over `any`; an org
can't loosen the bounds or its pin (`allow` under an `ask` is 400).

A call is filtered by the **tile's** orgs — the org that owns it and every
org it is shared into, by an exact share or a pattern — and by the
workspace's bounds: every one of them must allow it, so a tile under
several orgs gets the strictest and a tile in no org follows the bounds
alone. With nothing set, everything is allowed. A rule decides what starts
next: a session already running is never ended.

An org has two sets of these rules: its **own** (its admins') and a
**pin** (a workspace admin's). Both are asked on every call, so the org's
own rules narrow below the pin and can never loosen it. A refusal's reason
says where the rule comes from: `… (pinned by a workspace admin)` for a
pinned rule, and `… (this tile is under Finance by its share apps/*)` when
the tile isn't the org's own — so a rule that reaches a tile through a
broad share can be traced.

**The model rule binds agent sessions too.** Where the `models` rule of
any of a tile's layers is anything but `any`, an agent session on that tile
runs on the workspace's LLM connector only — a harness on a person's own
sign-in would send the tile's code to a model xbind can't see — and the
connector refuses a model the rule doesn't allow. A harness that can't run
on the connector isn't offered there, and starting it answers 403 with the
reason (`GET /agent/providers?cwd=`, `POST /term/sessions`). The harness
list and this rule govern the agent sessions xbind starts; what a person
runs by hand in a terminal on the tile is governed by access levels and
network sets, as before.

```
GET    /agents/policy             a person, or admin/xbin:users (a tile's
                                   own credentials: 403). the workspace's
                                   bounds: {workspaceAgent, harnesses,
                                   models, credentials, knownHarnesses,
                                   canSet}. credentials: whose sign-in
                                   serves a coding agent's model calls —
                                   "own-first" (the default: the person's
                                   own when they have one, else the LLM
                                   connector), "connector-first" (the
                                   connector, unless the person chose their
                                   own sign-in for the harness or the
                                   session) or "connector-only" (own
                                   sign-ins are not used; a harness that
                                   can't run on the connector isn't
                                   offered). "connector" is read as
                                   connector-only.
                                   knownHarnesses: the ids a list may name;
                                   canSet: the caller may PUT
PUT    /agents/policy             admin/xbin:users. {workspaceAgent?,
                                   harnesses?, models?, web?, mcp?,
                                   credentials?}: each
                                   key named is set, the rest keep their
                                   value, null puts a rule back to its
                                   default → the view above. 400 an unknown
                                   harness, a malformed id. Orgs' own rules
                                   are left alone: every call is asked
                                   against the bounds too, so a bound
                                   tightened later holds at once. Audited;
                                   `users` and `agents` events
PUT    /orgs/<org>/agents         admin/xbin:users for any org; an admin of
                                   the org, signed in themselves, for their
                                   own — never a tile driven by one, a
                                   terminal's or an agent session's token,
                                   or view-as (403). {workspaceAgent?,
                                   harnesses?, models?, web?, mcp?}, as
                                   above → {agents,
                                   pin, bounds}. The result must sit within
                                   the workspace's bounds and the org's
                                   pin: an org narrows them, never widens —
                                   a list entry they don't cover is 400,
                                   naming it ("any" is always within: it
                                   adds no rule of the org's own, and bounds
                                   and pin still hold). 404 no such org.
                                   Audited; `users` and `agents` events
PUT    /orgs/<org>/agents/pin     admin/xbin:users only (an org's own admin:
                                   403). {workspaceAgent?, harnesses?,
                                   models?, web?, mcp?}: the pinned rules,
                                   each key
                                   named set, null back to none → {agents,
                                   pin, bounds}. Within the workspace's
                                   bounds (400). The org's own rules are
                                   left as they are: the pin holds at every
                                   call. 404 no such org. Audited; `users`
                                   and `agents` events
```

`GET /agents/connection` — what one person's workspace agent session is
connected to under these rules — is in
[protocol/agents.md](/docs/protocol/agents.md).

## Limits

```
POST   /partitions/limits         admin (the admin console: when the
                                   person driving it is one); a tile
                                   manager (with their own session, app or
                                   device) may lower their own tile's. {tile?, maxRunning?,
                                   partitionBytes?}: without tile, the
                                   workspace's cap on people's partition
                                   instances running at once (maxRunning,
                                   admin only; partitionBytes 400); with
                                   tile, that tile's cap and the byte ceiling
                                   of each person's partition of it — an
                                   admin's values, or a manager's lower ones
                                   (above the admin's value or the default:
                                   403). 0 clears a value; maxRunning is
                                   ≤ 4096, partitionBytes ≥ 1 MiB. The caps
                                   default from host memory M (per tile
                                   clamp(M/4 ÷ E, 4, 32), workspace
                                   clamp(M/2 ÷ E, 8, 128), E = 96 MiB; M
                                   is MemTotal, or xbind's own cgroup
                                   memory limit when lower), the ceiling
                                   to the tile's per-namespace one. →
                                   {tile?, limits?:
                                   {maxRunning, partitionBytes}, workspace:
                                   {maxRunning}, defaults: {maxRunning,
                                   workspaceMaxRunning}} (effective values;
                                   partitionBytes 0 = the default). The
                                   tile's own credentials 403, an unknown
                                   tile 404. Kept in
                                   data/partition-limits.json (a file xbind
                                   can't read: the defaults apply and POST
                                   answers 500); applies at the next start;
                                   audited (docs/partitions.md)
GET    /limits                    any caller; what it lists depends on who
                                   asks. Tile resource limits (D203,
                                   docs/isolation.md): every value an
                                   integer in its kind's unit — kinds
                                   memMiB, diskGiB (the scope's quota, on
                                   the tile that roots it), vmMemMiB,
                                   vmVCPUs, sbxMemMiB, sbxVCPUs, sbxDiskGiB.
                                   → {tiles: [Row], defaults, enforced,
                                   orgCeilings, canSetCeilings}: rows for
                                   every tile to a workspace admin, an org
                                   admin's org's, a person's own or managed
                                   tiles, a tile credential's own. Row:
                                   {tile, scope, scopeKey, rootsScope,
                                   org?, limits: {<kind>: {effective,
                                   default, max, enforced, ask?, covered?,
                                   pending?, set?, approved? (decisions
                                   {value, by, at, source}), dormant?,
                                   usage?, applied?, appliesAt?, hit?,
                                   note?, approvers, canDecide?,
                                   canRevert?, choices? [{pct, value,
                                   fits, why?, approvable, default?}],
                                   askBlocked?}}} — the kinds that apply,
                                   and those the manifest asks where they
                                   can't (note says why); askBlocked: why
                                   a viewer who decides the kind can't
                                   approve its pending ask (an org admin
                                   above the org's ceiling). ?tile=<t> →
                                   {tile: Row, capacity?} (choices and capacity:
                                   approvers only; readers and the tile's
                                   own credentials read the row, 403
                                   otherwise). ?pending=1 → {pending:
                                   [{tile, scope?, kind, unit, why: ask|
                                   cap, ask?, hit?, effective, approvable,
                                   mine, approvers, blocked?}]}: what the
                                   caller may approve or manages; blocked:
                                   why an admin of the owning org can't
                                   approve it (the org's ceiling, or none
                                   for the kind). The shell's frame
                                   reads as the person driving it
POST   /limits                    a person (own session, app or device,
                                   the root token, the shell's frame, the
                                   admin console driven by a workspace
                                   admin): a workspace admin, or an admin
                                   of the owning org up to the org's
                                   ceiling; never tile code. {tile? |
                                   scope? (diskGiB: path, data key, or
                                   workspace), kind, value, source:
                                   manifest-ask|raise|admin-set, restart?,
                                   force?} → {tile: Row}. manifest-ask
                                   approves the manifest's ask (applies
                                   while it asks, up to what it asks);
                                   raise/admin-set hold regardless. 400:
                                   bad kind/value, value ≤ the default
                                   (DELETE instead), past the maximum, a
                                   manifest-ask above the ask; 403: not
                                   an approver, above the org's ceiling,
                                   force by a non-workspace-admin; 409:
                                   doesn't fit the workspace (why), unless
                                   force by a workspace admin; 500:
                                   data/tile-limits.json can't be read
                                   (never overwritten). Clears the kind's
                                   hit; publishes `limits`; audited
DELETE /limits?tile=&kind=        the tile's managers (user-owner, the
                                   owning org's admins) or a workspace
                                   admin, as a person (or scope= for a
                                   scope's disk). Both decisions go back
                                   to the default; a standing manifest ask
                                   pends again → {tile: Row}
POST   /limits/dismiss?tile=&kind= an approver of the kind, as a person
                                   (or scope=): clears the sticky at-cap
                                   hit for everyone → 204. An alert's
                                   dismiss names it
```
