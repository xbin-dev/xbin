# Protocol: vault and resources

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page: the vault and the shared resources — kv, blob, bus (publish
and push subscriptions) and cron. The builder page is
[resources.md](/docs/resources.md).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Overviews


```
GET    /vaults                     admin. [{component, keys}] across all vaults.
                                   A partitioned tile's row is its global
                                   instance's keys plus partitions: how many
                                   people's partitions keep a vault — a
                                   count, never their key names (absent when
                                   none)
GET    /resources                  admin. declared resources [{id,scope,name,type}]
```

## Vault


```
GET    /vault-status              admin. {initialized, sealed, mode, insecure}
                                   mode: unsealed|sealed|unconfigured|plaintext
POST   /vault-rekey               admin. body {current, new} — change the
                                   passphrase (re-wraps the data key; needs
                                   unsealed + the current passphrase).
                                   Every backup key bundle exported before
                                   is stale from then on (GET /backup-keys
                                   passphraseChanged, the backup-keys alert)
POST   /vault-unseal              admin. body {passphrase} — unseal (or init
                                   the barrier on first use). {created}
POST   /vault-seal                admin. drop the key from memory
GET    /vault/<component>          backend/terminal self, or admin. {keys:
                                   […]} — list keys (admin
                                   may list any vault)
GET    /vault/<component>/<key>    BACKEND ONLY (instance token, D30).
                                   {value} — a secret's value is readable
                                   only by the owning tile's backend;
                                   admins AND the tile's own terminals get
                                   403 (they list + set, never read)
PUT    /vault/<component>/<key>    backend/terminal self, or admin. body
                                   {value} (write-only management — D30;
                                   frame tokens can't reach the vault API)
DELETE /vault/<component>/<key>    backend/terminal self, or admin.
                                   (all vault get/set → 503 when sealed)
                                   Each deployment of a tile has its own
                                   vault. ?deployment=<name> on each vault
                                   route: a tile's own credentials act on
                                   their bound deployment's vault (a backend
                                   on its deployment's, a terminal or agent
                                   session on its target's; naming another is
                                   403); admins and tile managers, in their
                                   own session, name any deployment of the
                                   tile, except that a tile manager who
                                   isn't an admin reaches only non-primary
                                   deployments' vaults; other tiles' admin
                                   credentials reach only the primary's. A
                                   named deployment is echoed as deployment.
                                   A non-primary deployment's list is {keys,
                                   placeholders}: the primary's key names it
                                   has no value for, computed on each
                                   request; reading one → 404 "vault key
                                   "<k>" has no value in deployment <d>; a
                                   tile manager can copy it from the
                                   primary" (POST /deployments/vault-copy).
                                   Values stay readable only by the backend
                                   of the deployment whose vault it is. While
                                   the primary is protected its vault is
                                   written only by its own backend and by
                                   tile managers in their own session.
                                   A partitioned tile's (docs/partitions.md
                                   §Vault and registrations): its own
                                   credentials acting in a person's
                                   partition — that partition's backend, the
                                   person's terminals and agent sessions —
                                   reach that partition's own vault (values:
                                   its backend only); everyone else, admins
                                   included, reaches the global instance's.
                                   ?partition= on a partitioned tile's vault,
                                   cron and bus-subscription routes → 400 "a
                                   partition's vault and registrations are
                                   reached only from inside it: …", for
                                   everyone; ignored on other tiles. While
                                   the tile is paused (its partition mode
                                   pending or invalid, or a switch deleting
                                   its data) a partition's vault writes →
                                   409
```

## kv, blob, bus and cron


```
GET    /kv/res:<scope>/<name>/?prefix=   reader. {keys}
GET    /kv/res:<scope>/<name>/<key>      reader. raw bytes
PUT    /kv/res:<scope>/<name>/<key>      writer. body = value (≤1 MiB). 507 if
                                         the scope is over its disk quota
DELETE /kv/res:<scope>/<name>/<key>      writer.

GET    /blob/res:<scope>/<name>/[path]   reader. file bytes | {entries} for dirs
PUT    /blob/res:<scope>/<name>/<path>   writer. body = content (≤256 MiB)
DELETE /blob/res:<scope>/<name>/<path>   writer.

POST   /bus/publish                      writer on the resource.
                                         body {resource, topic, data?}
                                         Partitioned tiles (partitions.md):
                                         in a partitioned scope these kv,
                                         blob and bus routes reach the
                                         namespace the caller's partition
                                         holds: a person's own partition
                                         (their session, and the tile's
                                         frames, terminals and backend in
                                         their partition), the global
                                         instance's at today's keys, and a
                                         shared resource's ("shared": true |
                                         "read") at today's keys for
                                         everyone. An event on a partitioned
                                         scope's own (not shared) bus carries
                                         "partition" — the publisher's,
                                         "global" or "user:<id>" — and
                                         reaches only subscribers acting in
                                         that partition; a shared bus's
                                         carries none. Their answers add:
                                         403 "res:<scope>/<name> is
                                         read-only for people's partitions"
                                         on a write (kv PUT/DELETE, blob
                                         PUT/DELETE, bus publish) by a user
                                         partition to a "shared": "read"
                                         resource; 403 when the caller
                                         reaches no partition of the scope
                                         (view-as; a credential without a
                                         person on a tile without a global
                                         instance; an unpartitioned tile
                                         when the scope has no global
                                         instance), and, from a user
                                         partition of another partitioned
                                         tile holding the grant, "<id> can't
                                         read <tile>: …" when the person
                                         can't read the tile, or "<id>
                                         hasn't let <caller> use their
                                         <tile> data (they allow it at
                                         /xbin/partitions)" while the workspace's
                                         partitionConsent policy is on and
                                         they haven't consented; 409 while
                                         the scope's tile is paused (a
                                         pending or invalid partition mode)
                                         or its mode record can't be read —
                                         the data isn't reached through
                                         today's keys meanwhile; 507 when
                                         the partition is over its own disk
                                         ceiling (by default the tile's
                                         per-namespace one): one person's
                                         data is write-blocked, not the
                                         tile's.
GET    /bus/subscriptions                own push subscriptions (admin: all),
                                         with counters since the daemon started.
                                         {subscriptions:[{name, resource, prefix,
                                         component, path, role, delivered,
                                         dropped, failed, lastError?, lastAt?}]}
                                         A tile credential lists its own
                                         deployment's; rows carry deployment
                                         (absent for main), dormant (absent
                                         while active) and dormantEvents;
                                         ?deployment=<name> (admin) lists that
                                         deployment's, echoed as deployment
PUT    /bus/subscriptions                reader on the bus resource (the
                                         SUBSCRIBER's grant, even when an admin
                                         registers it). body {name, resource,
                                         prefix?, path, role?, component?¹};
                                         idempotent by name; ≤64 per component;
                                         409 over the limit. Delivery: below.
                                         A tile credential subscribes its own
                                         deployment (one beyond main keeps its
                                         own subscriptions, ≤64 per
                                         deployment); while that deployment
                                         isn't the primary and its deliveries
                                         are off the subscription is dormant:
                                         200 {ok, dormant:true}.
                                         ?deployment=<name> (admin) subscribes
                                         that deployment (echoed); a tile
                                         credential naming another deployment
                                         → 403
DELETE /bus/subscriptions/<name>[?component=]  element: own; admin: any.
                                         ?deployment=<name> (admin): that
                                         deployment's
                                         On a partitioned tile its credentials
                                         acting in a person's partition list,
                                         subscribe and delete that partition's
                                         own (≤16, the 17th: 409; rows gain
                                         dormantDrops); on a partitioned
                                         scope's bus the partition must reach
                                         that scope — its person can read it
                                         and, with partitionConsent on,
                                         consented — else 403, and deliveries
                                         stop while it doesn't (the row is
                                         dormant); while the tile is paused
                                         (pending, invalid, switching) → 409;
                                         admins reach the global instance's
                                         only; ?partition= → 400 (the vault
                                         rows above)

GET    /cron/jobs                        own jobs (admin: all). {jobs}
                                         A tile credential lists its own
                                         deployment's; rows carry deployment
                                         (absent for main) and dormant (absent
                                         while active); ?deployment=<name>
                                         (admin) lists that deployment's,
                                         echoed as deployment
PUT    /cron/jobs                        writer on the cron resource.
                                         body {name, resource, schedule, path, role?, component?¹}
                                         A tile credential schedules for its
                                         own deployment (one beyond main keeps
                                         its own jobs); while that deployment
                                         isn't the primary and its deliveries
                                         are off the job is dormant: 200 {ok,
                                         dormant:true}. ?deployment=<name>
                                         (admin) schedules for that deployment
                                         (echoed); a tile credential naming
                                         another deployment → 403
DELETE /cron/jobs/<name>[?component=]    element: own; admin: any.
                                         ?deployment=<name> (admin): that
                                         deployment's
                                         On a partitioned tile its credentials
                                         acting in a person's partition list,
                                         schedule and delete that partition's
                                         own jobs (≤16, the 17th: 409; none
                                         more often than once a minute: 400;
                                         while the tile is paused — pending,
                                         invalid, switching — 409); admins
                                         reach the global instance's only;
                                         ?partition= → 400 (the vault rows
                                         above)
```

¹ `component` is owner-only; elements always schedule (and subscribe)
themselves.

## Bus push delivery


**Bus push delivery (D85).** Each event published on a subscribed bus whose
topic starts with the subscription's `prefix` is POSTed to the subscriber's
`path` through the proxy, as `X-XBin-From: xbin/bus` with the subscription's
`role` (default `writer`) — starting an idle backend like a cron tick. The
JSON body:

```
{"id":"<event id>","subscription":"<name>","resource":"res:<scope>/<name>",
 "topic":"<topic under the resource>","data":…,"ts":<unix ms>}
```

`id` is the event's, the same for every subscription it reaches. Delivery
is at-most-once: one FIFO per subscription (256 events; the newest is
dropped when full), one POST in flight, 2 min timeout, no retries; more
than 100 events in one second are dropped (a loop guard). `reader` is
re-checked at each delivery (a revoked grant counts as `failed`); a
disabled or offloaded subscriber's events are dropped; a subscriber that
no longer exists loses the subscription. Answer 2xx; ≥400 counts as
`failed`. Stored in `data/bus-subscriptions.json`, carried in the
component's backup; a new tile created at a removed tile's path starts
without that path's cron jobs and subscriptions. A tile deployment beyond
`main` keeps its cron jobs and subscriptions in files of its own (§Filesystem
contract); a tick or delivery reaches the deployment that registered it, and
a dormant one (§Tile deployments) doesn't tick or receive anything (its
events count as `dormantEvents`, not `dropped`). A subscription on the tile's
own bus receives only events published in its deployment's data; one on
another scope's bus receives that scope's primary's, re-checked against its
edge at every delivery.

A person's partition's cron jobs and subscriptions (docs/partitions.md) are
kept in files of its own, never in `data/cron-jobs.json` or
`data/bus-subscriptions.json`, and a backup of the tile carries the global
instance's only. Their ticks and deliveries carry the partition
(`X-XBin-Partition: user:<id>`) and reach it while its person exists (the
same incarnation), is enabled and can read the tile — asked at every tick
and delivery, so regaining access resumes them. A tick whose start the
runner defers is tried again with jitter until the next tick is due. A
subscription receives its own partition's events (a partitioned scope's own
bus, its person's namespace) — which start the partition only when its own
tile published them — and a shared bus's, an unpartitioned scope's or one
another tile published for the person only while the partition runs
(skipped ones count as `dormantDrops`); never the global instance's. On a
partitioned scope's bus it is registered, and each event delivered, only
while the partition reaches that scope as a call of it would: its person can
read the scope's tile and, with the `partitionConsent` policy on, consented
— a shared bus needs the read access alone (403 at registration; a delivery refused meanwhile counts as
`dormantEvents`, its reason in `lastError`). A failed tick or delivery logs
its status, never the partition's answer.
