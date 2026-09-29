# 05 — Fabric edges, consent, bind types and the global instance

## Current behaviour

- **Authority is per tile.** `grantedRole`/`resolveTarget`
  (`internal/broker/deploydata.go:294`) decide it; a deployment is not a
  principal (D127f).
- **Binding records.** Bindings live in the workspace `xbin.json` as
  `bindings[component][slot]` (`internal/registry/registry.go:300`). Each
  is an ordered `Binding []BindRef` (`:367`), and `BindRef{Ref, Host, Zone,
  Listen}` (`:325-335`). There is no kind, requester deployment or
  provenance field. `validateBinding` is at
  `internal/broker/netfn.go:861-981`.
- **Who binds** (`netfn.go:780-794`): an admin (`Broker.IsAdmin`,
  `internal/broker/broker.go:500-509`); otherwise `orgAdminMayBind`
  (`internal/broker/delegated.go:276-371`). That covers org admins on the
  requester or provider side (D26/D33) and, through `selfMayBind`
  (`internal/broker/personal.go:63-87`), the user owner of the *requesting*
  tile (D88). Write level alone doesn't bind.
- **Bindings as env.** `EnvFor(c)` (`internal/broker/resources.go:100`,
  http slots at `:135-159`) builds `XBIN_IFACE_<SLOT>_URL` (single slot) or
  `XBIN_IFACE_<SLOT>=[{provider,url,service,instance?}]` (multi slot) from
  `HTTPSlots(comp)` (`netfn.go:325-365`). Frames get the same data through
  the `xbin-interfaces` meta (`HTTPInterfaces`, `netfn.go:371-392`;
  `internal/server/static.go:510-512`). Nothing filters it per principal or
  per deployment. The env is captured at spawn, and a bind restarts the
  requester (`netfn.go:710-718`).
- **A binding is the call grant.** `grantedRole` (`broker.go:452-484`)
  accepts an http binding at `:467-470` through `httpBindingRole`
  (`netfn.go:290-295`), `httpSlotsTo` and `httpSlotEdge`
  (`internal/broker/edgepolicy.go:204-247`). It is read live per call and
  covers the whole provider.
- **Personal tiles.** A personal tile has the owner `user:<id>` in the users
  store (`users.Store.owners`, `internal/users/users.go:232`; `Owner(path)`,
  `internal/users/orgs.go:169-173`). Its owner gets terminal level (D24).
- **Callees and per-caller state.** Callees see `X-XBin-From` = the tile
  path. Providers that keep per-caller state key on it: sandbox managers
  (`builtin-templates/coding-sandbox/_backend/manager.go:312-341`) and the
  agent's channels. The agent treats any other tile as a manager
  (`builtin-templates/agent/_backend/access.go:65-66`, `:76`). NP-09-19
  (plans/dev-lifecycle/09-fabric.md:1541-1545) warns that such providers
  must key on more than From.

## The change

### 1. The edge matrix (Route rule 4 and the self rules; PD-12/13/14)

| Caller ↓ / Target → | not partitioned | partitioned, with global | partitioned, no global |
|---|---|---|---|
| principal of a non-partitioned tile | today | **global** | 403 |
| user partition `user:A` of tile Z (instance, frame, terminal, ticket, its cron/bus/mail deliveries) | through a global or A's personal bind (§3): today + `X-XBin-Partition: user:A`, `X-XBin-Partition-Id` | **same** (`user:A`) if A can read the target (+ A's consent when the policy is on, §2); else 403 | **same**, idem |
| global instance of tile Z | today + `X-XBin-Partition: global` | **global** | 403 |
| the tile's own principal (self-call) | — | its own partition (F5: `global`, attributed to the person, §6) | its own partition |
| a person (session/app/device, admin or not) | today | `user:<self>` if they can read it | idem |
| the root token | today | **global** | 403 |
| view-as (any principal with `Impersonator`) | today | 403 on user partitions; F5 to global allowed | 403 |
| ingress | today | **global** (no header) | 503 |
| xbind cron/bus/mail deliveries | their registration's partition | idem | idem |

Every cell still needs today's grant or binding. The matrix only adds
refusals and picks the instance. The refusal texts name the reason.

### 2. Cross-tile partition edges: read access, plus consent by policy (PD-13, decided)

**The rule.** A call (Route rule 4) or a resource bind or reach (03 §B.2)
from `user:A` of Z into `user:A` of X needs:
- the grant (admin-approved; for a partitioned Z on a shared X, §3's
  admin-only rule applies);
- **A's read access on X**, which stops Z creating data for A in a tile A
  can't open.
- **only when the workspace policy `partitionConsent` is on**, A's consent
  record for (Z → X).

**The policy (PD-55).**
- Storage: an xbind-owned file, `data/workspace-policies.json`, `{"schema":
  1, "partitionConsent": false, "credentialResetConfirm": false}`. It is
  separate from `data/users.json`, because an older xbind that rewrites the
  users store would drop an unknown key (precedent: `data/branding.json`,
  `internal/boot/boot.go:680`).
- API: `GET /api/xbin/workspace-policies` (any signed-in person; tile
  principals get 403) and `PUT` (admin only). The PUT has deployment class
  PrimaryOnly, like `PUT /native-runtime` (`internal/server/deployclass.go:308`),
  and partition class Neutral (02 §8). A change publishes a `policies`
  event. `bx policies [set partition-consent on|off]`.
- UI: a new **workspace → policies** tab in the admin tile (06 §12.3).
- **Off** (the default): nothing is prompted and no consent records are
  written. The approval warning (below) and the ledger still apply.
- **Turning it on:** edges without consent are refused from the next call,
  and each person is prompted on their first refusal. Before confirming,
  the tab shows how many people used each partition edge in the last 30
  days (ledger totals).
- **Turning it off:** the records are kept, unused, and apply again if the
  policy returns.

**Consent while the policy is on** (the rev. 2 design, unchanged):
- A consent is xbind state at `data/partitions/consents/<uid>.json`
  (`{schema: 1, user, uid, edges: {"<Z>→<X>": {at, via}}}`). It is keyed by
  uid, so a recreated id inherits none.
- Only **A's own credentials** can give it (session, app or device: the
  PersonOnly class, 02 §8). Tile frames are sandboxed without same-origin
  (`web/frame-info.js:35-45`), so tile code can't. The routes are `POST
  /api/xbin/partitions/consents {from: Z, to: X}` and `DELETE`, or `bx
  partition consent Z X`.
- **Prompted:** a refused call publishes `partitions` op `consent-needed
  {from, to}` to A's sockets (at most once a day per edge) and sends a push
  that links to `/xbin/partitions`. The refusal text is `<A> hasn't let <Z>
  use their <X> data`.
- **Revocable at once:** it is checked at every call and bind. A bind held
  by a running Z partition is dropped by restarting that instance.
- **Visible:** A's partitions page lists each consent. X's own row in
  `GET /partitions` lists "tiles that can use your X data".

Neither mail nor cron can bootstrap an edge: mail never starts a never-run
partition (04 §3).

**Approval warning (S1), in both settings.** Approving a `uses X` grant
where both Z and X are partitioned shows, in the approval UI and `bx grants
approve`:
- policy off: "Z's code — and everyone who can change it — will be able to
  read and write the X data of every person who can read X";
- policy on: "… of every person who allows it".

**Ledger (S1, S10), in both settings.** Every allowed cross-tile partition
call or bind is counted in the caller partition's egress ledger (06 §6.1).

### 3. Bind types: global and personal (PD-16 decided, PD-54)

A bind wires a requester's slot to a provider. A **partitioned** requester
has two kinds:

| | **Global bind** | **Personal bind** (new) |
|---|---|---|
| Record | today's `bindings` in the workspace `xbin.json` (`registry.go:300`), unchanged shape | xbind state `data/partitions/binds/<uid>.json`: `{schema: 1, user, uid, binds: [{id, requester, slot, provider, at}]}` |
| Who creates it | **admins only** when the requester is partitioned (`Broker.IsAdmin`). The delegated paths (org admins D26/D33, personal owners D88) answer 403. Unpartitioned requesters keep today's rules | the **owner of a user-owned provider** (`Owner(provider) == "user:<id>"`), for **their own** partition of a partitioned requester they can read, with their own credential (PersonOnly). Admins may list and delete, never create: an admin's bind is always global |
| Provider | any tile (shared, or partitioned, per §1) | a user-owned (personal) tile, not partitioned |
| Slot | any http/net/stream slot, as today | multi http slots (v1) whose service the provider provides (`validateBinding`'s http checks, `netfn.go:947-968`); the ceiling rules apply |
| Seen in `XBIN_IFACE_*` / `xbin-interfaces` by | the global instance and every user partition (as today) | only `user:<id>`'s partition instance and that person's frames |
| Counts as the call grant for | every instance of the requester | a call from the requester acting in `user:<id>` whose uid matches, while `<id>` still owns the provider |
| Routing | the edge matrix (§1) | the provider's primary, with `X-XBin-From: <requester>`, `X-XBin-Partition: user:<id>`, `X-XBin-Partition-Id` |
| Removed by | admins | the person, an admin, a provider transfer (the owner changes), user delete (uid), the requester's mode switch (01 §2.6) |

**Why global binds on partitioned requesters are admin-only** (the owner's
rule). A global bind puts the provider in *every* person's trust base: it
sees what each partition sends (PD-23, S7). Admins "could also change that
shared tile's code", so their bind adds no new trust. The same rule covers
**approving a partitioned tile's `uses` grant on a non-partitioned target**
(`grantMutation`, `broker.go:710-764`), because an http binding is a grant
(`broker.go:467-470`); otherwise the rule could be sidestepped. The refusal
text: `<requester> keeps each person's data apart: only a workspace admin
wires it to shared tiles (people add their own tiles with a personal bind)`.

**Existing global binds** of a tile that becomes partitioned stay: they
are wiring, and a switch doesn't delete them. The trust panel and
`bx doctor` list them for review.

**Personal binds in the env** (per partition, PD-54):

| Env/meta of | global binds | alice's personal binds | bob's personal binds |
|---|---|---|---|
| the global instance | yes | no | no |
| alice's partition (instance, and her frames' meta) | yes | yes | no |
| bob's partition | yes | no | yes |
| a non-primary deployment's instance | yes (today) | no | no |

- `PartitionEnv(c, dep, part)` (03 §B.4) appends the person's entries to
  the multi slot's JSON, `{provider, url, service, personal: true}`
  (additive).
- `HTTPInterfaces` for a partitioned tile's document adds the viewer's
  entries.
- Creating or deleting a personal bind restarts only that person's
  partition instance, whose env was captured at spawn.

**Routing filter.** `grantedRole(caller, target)` becomes
`grantedRoleIn(caller, callerPart, target)`. Global binds and grants count
as today. A personal bind counts only when all of these hold:
- `callerPart == user:<id>` for the record's user;
- the record's uid is that person's live uid;
- `Owner(target) == "user:<id>"`, read live.

Any other partition, global included, gets no grant from it: 403, exactly
as if unbound. xbind thus filters what reaches a personal tile by matching
its owner to the caller's partition.

**Routes.** `GET/POST/DELETE /api/xbin/partitions/binds {requester, slot,
provider}` (PersonOnly for POST; GET is the person's own rows, or every row
for admins); `bx bind --personal <requester> <slot>=<provider>`.

**Later.** The record keys binds by owner; org-owned tiles with `org`
partitions ("groups") would add `org:<id>` rows without a relayout.

### 4. Providers that key state on the caller (PD-14, C11)

`X-XBin-Partition` tells a non-partitioned provider which partition of the
caller tile is calling, and `X-XBin-Partition-Id` gives a stable key. The
rule for providers (docs/partitions.md, docs/sandbox-manager.md,
docs/agent-inbox.md) is:
- **a provider that keeps per-caller state keys it on (`X-XBin-From`,
  `X-XBin-Deployment`, `X-XBin-Partition-Id`);**
- it treats an absent partition and `global` as the same consumer;
- it uses `X-XBin-Partition` for display only.

Enforcement is on the **consumer** side for the contracts xbin ships. The
sandbox-manager contract gains `caps.partitions: 1` (07 §4). The agent's
user partitions don't use a manager without it; they degrade instead
(C12, 08 §6).

**Providers are in the partition's trust base (PD-23, decided):** every
non-partitioned provider a partition calls sees what it sends. `bx doctor`
and the trust panel (06 §4, §12) list bound providers, their writer counts
and their last code change. Global binds on partitioned requesters are
admin-only (§3).

### 5. The global instance

- **Identity:** today's instance of the tile (PD-04). Env
  `XBIN_PARTITION=global`. The primary's network wiring applies to it only.
- **Serves:**
  - non-partitioned callers, ingress (`exposes`) and interface instances;
  - `alwaysOn` and the root token;
  - F5 requests from the tile's own principals, attributed (§6).
- **Holds:** its own copies of partitioned resources (today's keys),
  shared resources, and its mail inbox. On a tile switched to partitions
  these start empty (01 §2.6).
- **Never:**
  - calls a user partition;
  - reads a user partition's resources or inbox;
  - receives user-partition bus events;
  - sees personal binds.
- **May:** mail a person's partition (04 §3).
- **Without `global`:** today's instance never runs, and the tile has no
  public surface.

### 6. F5 — the tile's own principals addressing their global instance (PD-16, decided)

```
GET /api/apps/agent/runs/42?xbin-partition=global        (from alice's frame)
```

- **When it is honoured:** the target is partitioned **and** declares
  `global`, **and** the caller is one of the target's own principals acting
  in a user partition (frame, terminal, agent session, user-partition
  instance) or a person who can read it. Otherwise:
  - no global: 404 `ErrNoPartition`;
  - other tiles: 403;
  - **path tickets and cron/bus/mail delivery principals:** 403 (S3).
- **Attribution (S3).** At global the request is **the person's**, whatever
  the credential: `X-XBin-User: <id>`, `X-XBin-User-Level`, `X-XBin-Role`
  clamped to that level (never the self-call's admin, `deploydata.go:257`),
  `X-XBin-From: <tile>` and `X-XBin-Partition: user:<id>`.
- The proxy consumes the parameter only for partitioned targets.
- One way only: user → global.
- JS: `xbin.fetch(path, {partition: 'global'})`; Go SDK:
  `xbin.GlobalURL(path)`.

### 7. Deployments × partitions (PD-17, C13)

- v1: **user partitions exist only on the primary.** A non-primary
  deployment runs one instance on its own namespace, with
  `XBIN_PARTITION=global` when its own code asks for partitions, reachable
  only by writers.
- Switching the primary to another deployment is refused (01 §2.8); promote
  and rollback keep people's partitions. A promote or rollback to code that
  asks for a different mode opens a switch request (01 §2.7).
- Key layouts keep the `<dep>` segment (03).

### 8. Governance exclusions (PD-28)

A partitioned tile can't hold `xbin`/`xbin:*`, `cap:sandboxes`,
`cap:net-admin` or `cap:containers`, and can't be chrome or `vm`. Partition
principals never satisfy a governance check.

## Wire (for docs/protocol.md)

- Route refusals:
  - `403 <t> is partitioned: only partitioned tiles reach its people's
    data, and it has no global instance`;
  - `403 <user> can't use <t>`;
  - `403 <user> hasn't let <Z> use their <t> data` (consent policy on);
  - `409` for the bind-time refusal;
  - `404 <t> has no global instance`;
  - `403` for the admin-only global-bind rule.
- `GET/PUT /api/xbin/workspace-policies` and the `policies` event.
- `GET/POST/DELETE /api/xbin/partitions/consents` (PersonOnly).
- `GET/POST/DELETE /api/xbin/partitions/binds`.
- `XBIN_IFACE_<SLOT>` multi entries gain `personal: true` (additive), and
  the `xbin-interfaces` meta likewise.
- `?xbin-partition=global` (F5).
- `X-XBin-Partition` and `X-XBin-Partition-Id` on calls to non-partitioned
  providers (02 §6).

## Tests

- broker `TestPartitionEdgeMatrix`: every cell, with and without grants,
  with a person lacking read, and **with the consent policy off** (read
  suffices) **and on** (no consent → 403 and a prompt; consent → pass;
  revoked mid-run → the next call is refused and the bind dropped; a
  recreated user inherits nothing).
- broker policies:
  - `PUT` is admin only;
  - the file survives an older-format rewrite of `users.json`;
  - an off → on → off cycle keeps the records.
- broker bind types:
  - on a partitioned requester, an org admin's or personal owner's global
    bind gives 403 and an admin's succeeds; the same for `uses` approval of
    a non-partitioned target;
  - an unpartitioned requester keeps today's rules (golden);
  - a personal bind by the provider's owner succeeds, by anyone else 403,
    by an admin for someone else 403, and on a non-multi slot 409;
  - env: alice's partition sees her entry, while bob's partition and
    global don't;
  - calls: alice's partition → her tile passes; bob's partition, global
    and a guessed URL get 403;
  - after a provider transfer the bind is dropped; after user delete and
    recreate nothing is inherited; a mode switch removes the tile's
    personal binds;
  - only the owner's partition restarts on a change.
- broker `validateBinding`: 409 for a non-partitioned requester →
  partitioned provider without global; accepted with global; the approval
  warning texts in both policy settings.
- proxy F5: consumed only for partitioned targets; refused for other tiles,
  path tickets and delivery principals; the headers at global carry the
  person, their level and a clamped role, also for a user-partition
  instance token.
- deployments: a writer reaches `+dev`'s single instance; no user partition
  is ever created for `+dev`.
