# 02 — Identity and routing

## Current behaviour

- **Principal** (`internal/auth/auth.go:54`): `Owner`, `UserID`, `User`,
  `Access`, `Component`, `Via` (`cookie|bearer|instance|frame|cron|terminal|
  session|device|app`), `Gen`, `Role`, `Impersonator`, `Deployment`.
  `Deployment` comes only from xbind state or a signed claim, never a header
  (`internal/auth/deployment.go:1-24`).
- **Credentials that carry a person:** frame tokens (component, user, exp,
  gen[, deployment]) (`internal/auth/frametoken.go:17-47`, `:348`
  `framePrincipal`); terminal/agent tokens `termID{component, userID,
  deployment}` (`auth.go:263`, `:505` `terminalPrincipal`); path tickets
  (`internal/auth/pathticket.go:61` claims incl. `u`); sessions.
  **Credentials without a person:** instance tokens `instanceID{component,
  deployment}` (`deployment.go:52`), cron and bus principals
  (`internal/broker/dormant.go:339` `cronPrincipal`,
  `internal/broker/bussubs.go:440`), ingress, the root token.
- **Routing:** `proxy.ServeHTTP` (`internal/proxy/proxy.go:170`) resolves the
  tile (`ResolveRef`), asks `decide` → broker `Route`
  (`internal/broker/deploydata.go:227`, rules 1-4), gates template/runtime/
  lifecycle, then `identify` (`proxy.go:416`) strips every inbound
  `X-XBin-*`, sets From/Role/Deployment/User/User-Level/Viewed-By, and
  `EnsureDeployment(ctx, comp, target)` (`proxy.go:260`).
- **Self-calls** of a tile land with role admin (`deploydata.go:257`); a
  tile that sees its own `X-XBin-From` and no user treats the call as the
  tile itself (the agent: `whoSystem`, "everything",
  `builtin-templates/agent/_backend/access.go:63-64`).
- **API class gate:** `classGate` (`internal/server/deployclass.go:376`) with
  the `routeClasses` table (`:84`) and `TestDeploymentRouteClasses`
  (`internal/apicheck/deployclass_test.go`).
- **Events:** bus events reach admins unconditionally
  (`internal/server/server.go:678-684`), others via `busFilter`
  (`internal/broker/resources.go:575`); `session` events (every agent-session
  event, `internal/server/agentapi.go:47-52`) and `term` events pass admins
  in `termEventFor` (`internal/server/termsessions.go:81-84`); the tile
  status headline is a tile-wide `status` event (`internal/obs/status.go:140-146`);
  runner events (`internal/runner/deploy.go:222-226`) carry the tile path
  and, on a crash loop, the log path (`internal/runner/states.go:220`).
- **View-as:** an impersonating session reads as the user; only non-GET is
  refused (`internal/server/server.go:230`).

## The change

### 1. The partition value

```go
// util
type Partition string              // "" | "global" | "user:<id>"
const PartitionGlobal Partition = "global"
func UserPartition(id string) Partition
func (p Partition) User() (id string, ok bool)
func ParsePartition(s string) (Partition, error) // strict: "global" or "user:" + a stored user id
```

`auth.Principal` gains:

```go
// Partition is the partition of a partitioned tile this principal acts in:
// "global" or "user:<id>". Set from xbind state only: an instance token's
// registration, a cron/bus/mail registration, or — for the tile's own
// frames, terminals, agent sessions and path tickets — derived from the
// credential's person by the broker (addressedPartition). "" for every
// principal of a tile that isn't partitioned, and for people and other tiles
// (their partition on a target is decided per call).
Partition util.Partition
```

### 2. Instance tokens (S13)

`instanceID` gains `partition` and, for user partitions, the person's `uid`;
`RegisterInstancePartition(token, tile, dep, part, uid)`;
`RegisterInstanceDeployment` stays (partition ""). The runner calls it from
its own state in `registerInstance` (`internal/runner/deployments.go:271`) —
never from env. **The global instance registers with partition "" and reads
as `global`** (it is today's instance, PD-04).

The registered partition is **part of the token's identity**: a token
registered as `user:<id>` authenticates (401 otherwise) only while
(a) the tile's effective state is `Partitioned` with `user` in its spec,
(b) the person exists with the same uid and is enabled. `PartitionsChanged`
(01 §6) and the people hooks (06 §9) revoke those tokens **synchronously,
before** the new state is published, so no window exists in which a
person's instance token resolves to partition "" (= the tile's main
instance, main's data, global's vault).

No other token wire format changes: frame tokens, terminal tokens and path
tickets already carry the person, and the partition is derived from it
(PD-01).

### 3. `addressedPartition(p, tile) (util.Partition, error)` — the one function

New, in the broker (beside `addressed`, `deploydata.go:116`), installed like
`AddressedDeployment` so the server's planes (frame mint, class gate, events)
share it. For a tile that isn't `Partitioned` it returns `("", nil)` for
everyone — every existing answer is unchanged. For a non-primary deployment
of a partitioned tile it returns `global` (its one instance, PD-17).

For the primary of a partitioned tile `t` with spec `S`:

| Principal | Answer |
|---|---|
| cron/bus/mail delivery (`Component` = `xbin/cron`/`xbin/bus`/`xbin/mail`) | its registration's (addressee's) `Partition` (§5) — liveness-checked (PD-20) |
| `t`'s instance token | its registration: `user:<id>`, or `global` when "" |
| `t`'s frame / path ticket / tile-origin credential with `UserID` | `user:<UserID>` if that person can still read `t`; 403 otherwise |
| `t`'s frame / terminal minted under the root token (no `UserID`) | `global` if `S.Global`, else 403 `sign in as a person: <t> keeps each person's data apart` (PD-10) |
| `t`'s terminal / agent session with `UserID` | `user:<UserID>` |
| any principal with `Impersonator != ""` (view-as) | 403 `<t> keeps <user>'s data private: view-as can't open it` (PD-08); only F5 reaches `global` (05 §6) |
| a person (session/app/device), admin or not | `user:<UserID>` if they can read `t` (an admin reaches **their own** partition, PD-11) |
| the root token (`Owner`, no user) | `global` if `S.Global`, else 403 (PD-10) |
| another tile `X`'s principal | `callerPartition(p)` mapped onto `t` (§4 rule 4, 05 §1) |
| ingress | handled outside Route: `global` or 503 (§7) |

`callerPartition(p)`: the partition `p` acts in on its **own** tile
(`addressedPartition(p, p.Component)`), "" when that tile isn't partitioned.

### 4. `Route` (`deploydata.go:227`) — Decision gains `Partition`

`broker.Decision` and `proxy.Decision` (`proxy.go:82`) gain
`Partition util.Partition` (the target's partition the call reaches; "" for
an unpartitioned target), `CallerPartition util.Partition` (§6),
`Attribute *Attribution` (F5, §6) and `Background bool` (§7). Boot's
converter copies them. Rules, in today's order:

1. **cron/bus/mail delivery** → registration's deployment (today) **and**
   partition; refused (`Deny`, 403) when the registration's person is gone,
   disabled or lost read (so a dormant registration never runs code);
   `Background = true`.
2. **the tile's own principal** → `addressed` (today) and
   `addressedPartition`. A self-call never leaves its partition (as D127g
   keeps self-calls in their deployment). The only exception is F5
   (`?xbin-partition=global`, PD-16, 05 §6), toward `global` only, never from
   a path ticket (a fixed-prefix credential for its own partition) and never
   from a delivery principal (403); an F5 request from a user partition
   carries an **Attribution** to that partition's person (§6).
3. **admin** → the qualifier's deployment or the primary (today); partition
   = `addressedPartition` (the admin's own). **No admin qualifier for a
   partition exists.**
4. **anyone else** → the primary (today), with the partition taken from
   the edge matrix (05 §1):
   - a caller in partition `user:<id>` → the same key on `t`, if the grant
     allows and the person can read `t` (PD-13, decided). When the workspace
     policy `partitionConsent` is on, the person's consent record for
     (caller tile → `t`) must also hold; otherwise 403 `<id> hasn't let
     <caller> use their <t> data`, with a consent prompt to the person
     (05 §2);
   - a caller in `global`, or a non-partitioned caller → `global` if
     `S.Global`, else 403 `<t> is partitioned: only partitioned tiles reach
     its people's data, and it has no global instance`.

   The grant check is `grantedRoleIn(caller, callerPart, t)`. A **personal
   bind** (05 §3) grants only when `callerPart` is the provider owner's
   partition. Every allowed cross-tile partition call is counted in the
   caller partition's egress ledger (06 §6).

`resolveTarget` (`deploydata.go:294`) is unchanged: authority stays the
tile's (a partition is not a principal of its own, like D127f); partitions
only choose *which instance and data*.

### 5. Synthetic principals

- `cronPrincipal(dep, role)` → `cronPrincipal(dep, part, role)`
  (`dormant.go:339`); `fireDep`/main fire paths pass the job's partition.
- Bus deliveries: `auth.Principal{Component: BusPrincipal, Via: "bus", Role,
  Deployment: owner, Partition: ownerPart}` (`bussubs.go:440`).
- Mail doorbells (new, 04 §3): `auth.Principal{Component: "xbin/mail", Via:
  "mail", Role: "writer", Deployment, Partition: addressee}`.
- None ever carries a `UserID` (no person drives them), and none may use F5.

### 6. `identify` (`proxy.go:416`)

`identify` gains the `Decision` argument (today it takes `role` only). After
the existing strip and headers:

```go
if cp := d.CallerPartition; cp != "" {
    r.Header.Set(HeaderPartition, string(cp))          // "user:<id>" | "global"
    if pk := d.CallerPartitionID; pk != "" {
        r.Header.Set(HeaderPartitionID, pk)            // the caller's pkey (user partitions only)
    }
}
if a := d.Attribute; a != nil {                        // F5 from a user partition (S3)
    r.Header.Set(HeaderUser, a.UserID)
    r.Header.Set(HeaderUserLevel, a.Level)             // the person's level on the tile
    r.Header.Set(HeaderRole, a.Role)                   // clamped to that level, never self-admin
}
```

- **`X-XBin-Partition`** (new const beside `HeaderDeployment`, `proxy.go:60`)
  names **the partition the caller acts in**: the caller's own partition for
  a partitioned tile's principals (instances, frames, terminals, tickets, its
  cron/bus/mail deliveries), including on calls to non-partitioned tiles; for
  a person calling a partitioned tile directly, `user:<id>`; `global` for the
  global instance's outbound calls and the root token reaching a global
  instance. Absent for everything else, so no existing callee sees a new
  header.
- **`X-XBin-Partition-Id`** (new, C11): the caller's pkey, for user
  partitions only — opaque, stable for the person's incarnation. Providers
  key per-caller state on (`X-XBin-From`, `X-XBin-Deployment`,
  `X-XBin-Partition-Id`) and show `X-XBin-Partition` as the display name, so
  a deleted-then-recreated `alice` never inherits the old alice's records.
- **F5 attribution (S3):** an F5 request from a user partition — whatever
  the credential (frame, terminal, instance token) — reaches global as its
  **person**: `X-XBin-User=<id>`, `X-XBin-User-Level=<the person's level on
  the tile>`, `X-XBin-Role` clamped to that level instead of the self-call's
  admin (`deploydata.go:257`). Existing person-ACL code (the agent's
  `principal`, `access.go:57-62`) then applies unchanged; a user partition's
  code is never "the tile itself" at global. docs/partitions.md: *global must
  never treat `X-XBin-Partition: user:*` as the tile itself.*
- Inbound values of both headers are stripped like every `X-XBin-*`.
  `X-XBin-User` semantics are otherwise unchanged: a partition backend's own
  outbound (non-F5) calls carry no user, exactly as today.

### 7. Proxy plumbing

- `ServeHTTP`: after `decide`, a partitioned target calls
  `Runner.EnsurePartition(ctx, comp, dep, d.Partition, class)` and
  `TrackPartition(tile, dep, part, passive)` instead of
  `EnsureDeployment`/`TrackDeployment` (`proxy.go:260-278`). `class` is
  `background` for rule-1 deliveries and `interactive` otherwise;
  `passive` is set for responses with `Content-Type: text/event-stream`
  (a live stream alone doesn't make a partition busy for eviction, 03 §A.5).
- The runtime/backend gates (`proxy.go:215-226`) also consult the tile's
  partition state (01 §2: pending/invalid → 409 with the pending body).
  The tile's documents under `/c/` get the switch page from
  `handleComponentStatic` (`internal/server/static.go:70`, 01 §2.4).
- `ForwardIngress` (`internal/proxy/ingress.go:39`): for a partitioned tile,
  `Runner.Ensure` becomes `EnsurePartition(…, global)`; no global → 503
  "this site is not being served right now" (same text as today's disabled
  case). `X-XBin-Partition` is never set on ingress.
- Bus dispatch (`DispatchBodyViaProxy`, `bussubs.go:143`), cron dispatch and
  mail doorbells go through the proxy and inherit the rules.

### 8. The API class gate — a second table

`/api/xbin/*` handlers key on `p.Component`; a user-partition principal
reaching an unconverted handler would act on the tile's (global's) state.
Mirror D127r:

```go
// internal/server/partitionclass.go
type PartitionClass uint8 // Unclassified | PartitionScoped | GlobalOnlyDormant | GlobalOnlyRefused | PersonOnly | Neutral
var partitionClasses = map[string]PartitionClass{ … }
func (s *Server) partitionGate(r2 *http.Request) (part util.Partition, deny http.HandlerFunc)
```

`handleAPI` runs `partitionGate` right after `classGate`. A principal whose
`addressedPartition` on its own tile is `user:*` asks the table (global =
today's instance: never gated, like the primary in D127r). Unclassified →
403 `this route isn't available to a partition's credentials yet`. The guard
`TestPartitionRouteClasses` (`internal/apicheck`) fails on **any route
without a row**, on **any `DeploymentScoped` route of `routeClasses` without
a partition row**, and on any route that `web/xbin-client.js` or `sdk/` calls
without a row (C10/S12). Initial classes:

| Class | Routes |
|---|---|
| PartitionScoped | `GET/PUT/DELETE /kv/…`, `/blob/…`, `POST /bus/publish`, `GET/PUT/DELETE /bus/subscriptions…`, `GET/PUT/DELETE /cron/jobs…`, `GET/PUT/DELETE /vault/…`, `GET /logs`, `GET /tile-status`, **`GET/POST /tile-report`** (stored per partition; its `status` event goes to that person's sockets only and never becomes the tile card's status; global keeps today's behaviour), `GET /frame-token`, `POST /path-tickets`, `POST /notify` (clamped, 06 §8), `POST /term/sessions*`, `GET/PUT/DELETE /prefs…` (already per person), `GET /partitions` (own row), `POST /partitions/{stop,reset}` (own partition), `GET /partitions/mail`, `POST /partitions/mail` (to `global` only), `POST /partitions/mail/ack` |
| GlobalOnlyDormant → stored dormant, answer success (start-up code keeps working, as D127h did) | `PUT /iface-instances`, `PUT /ingress-hosts`, `GET /ingress-routes` |
| GlobalOnlyRefused | every `/sandboxes…` route (PD-28), `POST /code/pr/state`, everything D127r made primary-only, and the data-namespace acts of the deployments API: `POST /deployments/{seed,reset,vault-copy,backup,restore,backup-schedule,run-now}`, `GET /deployments/backups` (S12) |
| PersonOnly → only a person's own session/app/device credential, never a tile principal of any partition (tile code must not be able to perform them) | `GET/POST/DELETE /partitions/consents`, `POST/DELETE /partitions/share-log`, `POST/DELETE /partitions/binds` (personal binds, 05 §3), `POST /partitions/credential-confirm` (06 §9) |
| Neutral | reads of workspace facts, `whoami`, `components`, `GET /workspace-policies`, the person's own account routes, and the read/admin-judged `/deployments/*` routes not listed above (their handlers already refuse instance and frame principals, `deployclass.go:167-168`). Also the governance acts: `POST /partitions/{mode,limits,purge}` (`mode` = keep/switch, 01 §2.5), `PUT /workspace-policies`, `/backup-keys/*`, and a manager's `stop`/`reset` of another person's partition. Their handlers judge the person (`mayManageTile` or admin) and refuse instance and frame principals of every tile **and the target tile's own terminals and agent sessions**, so a partitioned tile's code can never decide its own mode switch |

### 9. Events (`/ws/events`)

- `events.Event` gains `Partition string` (beside `Deployment`).
- `apiBusPublish` (`resources.go:527`) stamps the partition namespace the
  event is in (04 §2).
- `server.go:678-684`: before `if p.IsAdmin() { return true }`, an event with
  `Partition` set is delivered only when `BusAllows` says so — **admins lose
  the blanket pass for partitioned events** (G2).
- `busFilter` (`resources.go:575`): a subscriber reaches a partitioned event
  only when `addressedPartition(p, tile) == e.Partition`; a shared-bus event
  reaches every principal that may read the bus (today).
- **`session`/`term` events (S2):** `termEventFor`
  (`internal/server/termsessions.go:81-84`) drops the admin pass for events
  whose `Component` is a partitioned tile: only the session's own person
  (and their terminal on the tile) receives them.
- **Runner events (S12):** a user partition's reload/build/crash events are
  published as `partitions` op `state` to that person's sockets only; a
  crash loop names the partition (`user:alice's instance`), never the log
  path (`crashLoopError`, `states.go:220`, gains a partition variant, C15);
  admins get the metadata (`crashLoop: true`) in `GET /partitions`.
- **Status (S12/C10):** a user partition's `POST /tile-report` publishes its
  `status` event with `Partition` set, delivered only to that person.
- New event type `partitions` (06 §6): op `mode` to the tile's write
  audience; ops `state`, `consent-needed`, `notice` to the person's own
  sockets.

### 10. Frame documents and renewal

- `static.go:526` adds `<meta name="xbin-partition" content="<key>">` to a
  partitioned tile's documents when the viewer resolves to a partition; a
  view-as viewer gets the document with a notice and no frame token (the API
  would refuse anyway, §3). A viewer resolving to `global` (root token,
  `--no-auth`) gets `content="global"` (the agent's third UI state, 08 §11).
- Renewal (`/api/xbin/frame-token`) unchanged: the token names the person,
  the partition follows.

## Wire (for docs/protocol.md)

- Headers **`X-XBin-Partition: user:<id> | global`** and
  **`X-XBin-Partition-Id: <pkey>`** (identity header table,
  protocol.md:73-100); F5 attribution semantics.
- `403` texts listed in §3/§4; `401` for an instance token whose partition is
  no longer covered; `409` for pending/invalid (01 §2.3).
- `<meta name="xbin-partition">` in partitioned tiles' documents.
- `events.Event.partition`; the `partitions` event type (06 §6); narrowed
  `session`/`term`/`status` delivery on partitioned tiles.
- `/api/xbin/*` default-deny for user-partition credentials (the class list);
  PersonOnly routes (consents, share-log, personal binds, credential
  confirmations).

## Tests

- `auth`: `RegisterInstancePartition` round trip; an invalid partition
  registers nothing (fail closed, like `claimName`); **flag off with a live
  partition: its token can't read main's kv** (401), revoked before the new
  state is visible (S13); a recreated user's uid mismatch → 401.
- `broker`: `TestAddressedPartition` — the whole §3 table, incl. view-as,
  root token with/without global, a person without read, a disabled person, a
  deleted person, frames of the owner token, a non-primary deployment
  (→ global).
- `broker`: `TestRoutePartitions` — rules 1-4 × {partitioned, not} × {global,
  no global} × {consent policy off, on with consent, on without}, plus a
  personal bind from the owner's partition, another partition and global;
  `TestZeroStateRoute` still byte-identical for unpartitioned tiles
  (existing guard).
- `proxy`: `identify` sets `X-XBin-Partition`/`-Id` only in the listed cases;
  spoofed inbound ones never survive; **F5 from a user-partition instance
  token arrives at global with `X-XBin-User`, the person's level and a
  clamped role**; F5 from a path ticket or a cron/bus/mail principal → 403;
  `ForwardIngress` → global or 503; SSE responses tracked passive.
- `server`: `TestPartitionRouteClasses` (apicheck; the three coverage rules),
  `partitionGate` refusals, PersonOnly refuses frame/terminal/instance
  tokens; admin shell socket receives neither a partitioned bus event nor a
  partitioned tile's `session`/`term` events nor a partition's `status`; the
  own frame does.
