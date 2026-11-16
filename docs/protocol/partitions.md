# Protocol: partitioned tiles

Part of the [protocol reference](/docs/protocol.md), which holds the
conventions every route shares (authentication, tokens, headers) and the
index of these topic pages. This page:
a partitioned tile's mode, binds, consents, the ledger,
the partition list and its operations, and partition mail. The builder
page is [partitions.md](/docs/partitions.md).

Rows are paths below `/api/xbin/`; the words before the first full stop
of a row say who may call it.

## Partitions

```
POST   /partitions/mode            a tile manager (the tile's user-owner, an
                                   admin of its owning org, or a workspace
                                   admin) acting as a person: their own
                                   session, app, device or the root token, or
                                   the admin tile's frame under their login;
                                   every other tile principal (instance,
                                   frame, terminal, agent session) 403.
                                   body {tile, act: "keep"|"switch", from, to,
                                   confirm?, yes?, dryRun?} — decide a
                                   partition mode switch request R → Q
                                   (docs/partitions.md §The mode).
                                   from/to are {user, global} (null:
                                   unpartitioned) and must still be the
                                   request's R and Q, else 409 with the
                                   current partition {state, from, to?,
                                   declined?}; 409 too for an invalid request
                                   or an unreadable mode record. keep answers
                                   an open request: R runs again at once,
                                   nothing is deleted — {ok, tile, act, mode,
                                   declined}. switch answers an open or a
                                   declined request; confirm must be the tile
                                   path (400); user partitions need xbind's
                                   --isolate (409); an offloaded tile is 409;
                                   when to has user partitions and the tile
                                   binds sandbox managers whose GET
                                   /sbx/hello caps lack "partitions", 409
                                   {managers} unless yes. It stops every
                                   instance, deletes the tile's data — from
                                   or to unpartitioned: every namespace
                                   (main's and every deployment's), vault file
                                   and registration (cron, bus, interface
                                   instances, ingress hosts; people's
                                   personal binds on the tile, counted with
                                   the registrations), and erases its
                                   ns: backup keys (tile: stays); removing
                                   "global": global's namespace, vault and
                                   registrations and global's ns: key only;
                                   adding "global": nothing — then records R
                                   := Q and tells each person whose partition
                                   went (push kind tile.partition-deleted).
                                   → {ok, tile, act, from, to, deletes,
                                   wiped: {namespaces, partitions, vaultKeys,
                                   registrations, bytes, subkeys,
                                   keyFilesLeft?}, keeps: [text], people?,
                                   managers?, archiver?, eraseError?}
                                   (eraseError, keyFilesLeft: keys erased
                                   whose files aren't removed yet — refused
                                   everywhere all the same). A wipe that
                                   fails part-way, or an erase of the backup
                                   keys that fails, is 500 with wiped:
                                   nothing is recorded, the request stays
                                   open, a retry finishes it. A tile at the
                                   path "workspace" never counts or deletes
                                   the workspace-level resources. dryRun
                                   counts the same, deletes nothing and needs
                                   no confirm. Audited. A request opening
                                   pushes to the tile's managers (kind
                                   tile.partition-switch; at most one per
                                   tile every 15 minutes), and every mode
                                   change reloads the tile's frames (event
                                   reload).
GET    /partitions/binds           a person (their own personal binds), or
                                   admin — an admin person, the root token or
                                   a tile holding xbin:admin; its frame
                                   under a person's login (the admin
                                   console) only when that person is an
                                   admin — every living person's; every
                                   other tile principal 403.
                                   → {binds: [{id, user, requester, slot,
                                   provider, at, live, why?}]} — live: the
                                   bind holds now; why: why not (the provider
                                   changed owner, the requester no longer
                                   partitions, …). A deleted person's rows,
                                   or an earlier incarnation's under the same
                                   id, are never listed (docs/partitions.md
                                   §Bind types).
POST   /partitions/binds           a person's own act: their session, app or
                                   device (never a tile principal, view-as or
                                   the root token — 403), and never an
                                   admin's (403: an admin's bind is always a
                                   global bind, POST /bindings). body
                                   {requester, slot, provider} — wire
                                   provider into the caller's OWN partition
                                   of requester. 403 unless the caller owns
                                   provider personally (user:<id>) and can
                                   read requester, or when the policy
                                   ceiling denies the edge; 404 an unknown
                                   tile, answered only past those checks (a
                                   tile you own, a path you may read); 409
                                   when requester isn't partitioned or is
                                   paused, slot isn't a multi:true http slot,
                                   provider is partitioned, doesn't provide
                                   the slot's service or exposes instances,
                                   or is already bound on the slot for
                                   everyone. Only that person's partition
                                   instance gets the row in
                                   XBIN_IFACE_<SLOT> (restarted: its env is
                                   captured at spawn) and only their frames'
                                   xbin-interfaces meta lists it, each with
                                   personal: true; it lets a call through
                                   only from requester acting in their
                                   partition, while they still own provider
                                   (every other caller: today's 403). Adding
                                   the same bind again answers the existing
                                   one. → {ok, bind: {id, user, requester,
                                   slot, provider, at, live}}; publishes
                                   grants for requester to that person's
                                   sockets and frames only.
DELETE /partitions/binds           the bind's person, or admin (as for GET).
                                   body {id} or {requester, slot, provider[,
                                   user]} (user: whose, default the caller's
                                   own; an admin's {id} matches anyone's) →
                                   {ok, removed: [{id, user, requester, slot,
                                   provider, at}]}; 404 when nothing matches
                                   — a deleted person's rows, or an earlier
                                   incarnation's, never do. Restarts that
                                   person's partition instance of requester.
                                   A personal bind also goes when its
                                   provider changes owner, its person is
                                   deleted, requester switches between user
                                   partitions and unpartitioned, or a tile is
                                   created at its requester's or provider's
                                   path (a removed tile's binds never reach
                                   the new one).

GET    /partitions/consents        a person's own session, app or device
                                   (PersonOnly: tile code — frames,
                                   instances, terminals, agent sessions —,
                                   view-as and the root token 403). →
                                   {policy: {partitionConsent}, consents:
                                   [{from, to, at, via}], asked: [{from, to,
                                   at}]}: the person's consents to
                                   cross-tile partition edges (kept while
                                   the policy is off; they apply again when
                                   it returns) and the edges they were asked
                                   about in the last day and haven't allowed
                                   since (an allow answers the ask: taking
                                   the consent back doesn't list it again;
                                   a tile deleted or moved, or switched to
                                   or from unpartitioned, takes the asks
                                   naming it)
                                   (docs/partitions.md §Calls between
                                   partitioned tiles)
POST   /partitions/consents        PersonOnly, as above. {from, to}: let
                                   partitioned tile from use the person's
                                   data in partitioned tile to. Only while
                                   the workspace setting partitionConsent is
                                   on (else 409); both tiles partitioned
                                   (409), existing (404) and readable by the
                                   person (403); a path holding "→" 400.
                                   Kept in
                                   data/partitions/consents/<uid>.json (a
                                   person recreated under the same id
                                   inherits none; a tile deleted, moved or
                                   switching mode takes every consent
                                   naming it). A record this xbind can't
                                   read is kept as it is: 409 (/alerts kind
                                   partition-consents). → the GET view;
                                   audited; publishes `partitions` op
                                   consent
DELETE /partitions/consents        PersonOnly, as above; either setting.
                                   {from, to} (body, or ?from=&to=): take a
                                   consent back. With the policy on, the
                                   next call and data reach are refused, and
                                   from's backend instance of the person
                                   stops (it starts again on the next
                                   request; a proxied stream a page,
                                   terminal or agent session of from opened
                                   before lasts until it closes). → the GET
                                   view plus revoked: whether there was a
                                   consent to take back; audited; 409 as
                                   for POST
GET    /partitions/ledger          PersonOnly, as above. ?tile= ?days=1-90
                                   (30). The egress ledger (counts per day,
                                   never contents): kind edge — an allowed
                                   call or data reach into the same person's
                                   partition of another partitioned tile, a
                                   bus subscription there once when made
                                   and once per delivery —,
                                   provider — a call to a tile that isn't
                                   partitioned (a global or a personal
                                   bind), or to a partitioned tile's
                                   deployment beyond its primary, target
                                   <tile>+<name> —, bus and trigger (a
                                   shared resource's reach isn't counted).
                                   → {days, rows:
                                   [{tile, day, kind, target, count}] (the
                                   person's own), totals?: [{kind, target,
                                   count, people}] (with tile: the tile's
                                   writers, managers and admins; for all but
                                   admins every personal tile's target is
                                   "(a personal tile)"), people?:
                                   [{user, tile, kind, target, count}]
                                   (admins)}. Kept 90 days in
                                   data/partitions/<tile-key>/<dep>/<pkey>/
                                   ledger.json; both consent settings
GET    /partitions/edges           admin. ?days=1-90 (30). → {days, policy:
                                   {partitionConsent}, edges: [{from, to,
                                   granted, people, calls, consented}]}: the
                                   edges from a partitioned tile into
                                   another's people's data — granted now, or
                                   counted in people's ledgers in the window
                                   — with how many people used and allowed
                                   each: what turning partitionConsent on
                                   starts asking about (the admin tile's
                                   Policies tab shows it first)
GET    /partitions                 anyone; what it answers depends on who
                                   asks (docs/partitions.md §Operating
                                   people's partitions). → {features:
                                   ["partitions/1", "mode-switch/1",
                                   "consents/1", "personal-binds/1",
                                   "global-address/1", "partition-ops/1",
                                   "log-share/1", "credential-confirm/1",
                                   "partition-mail/1", "partitions-page/1"
                                   (GET /xbin/partitions is served), …]
                                   (what this xbind
                                   serves; a 404 is an
                                   xbind without partitions), policies?:
                                   {partitionConsent,
                                   credentialResetConfirm} (people and
                                   admins, never tile code), …}.
                                   ?tile=<t> (one the caller can read, else
                                   404): tile, state (partitioned |
                                   unpartitioned | pending | invalid), spec
                                   {user, global}, request {spec, since,
                                   declined} | null, error?, limits
                                   {maxRunning, partitionBytes},
                                   reviewedOnly {on, by?, at?,
                                   unprotected?} (POST
                                   /partitions/reviewed); partitions: the
                                   caller's own row {user, partition,
                                   partitionId, state (active | dormant |
                                   orphaned), why?, running, instance?:
                                   {tile, deployment, partition, state,
                                   gen, uptimeSec, rssKb, restarts,
                                   error? (their own row only),
                                   errorClass?}, lastStarted?,
                                   created, lastExit?, restarts?,
                                   crashLoop?, bytes, registrations:
                                   {cronJobs, busSubscriptions,
                                   ifaceInstances, ingressHosts,
                                   missedTicks, dormantDrops, vaultKeys},
                                   logShare?: {until}, ledger?: [{kind,
                                   target, count}] (30 days), mail?:
                                   {pending, bytes, expired,
                                   undeliverable?} (their inbox's counts,
                                   once it has held an item)} — for admins
                                   every person's metadata row (never
                                   content, vault key names, log lines or
                                   mail: its counts only; logShare, no ledger;
                                   instance.errorClass, never the error's
                                   text), orphaned ones included; bytes are
                                   measured at most once a minute; totals
                                   {people, running, bytes, cron, bus} (the
                                   tile's writers, managers, admins); trust
                                   {writers, admins, liveReload, protected,
                                   lastCodeChange? {at, by, how, result}
                                   (the primary's last code move, for a
                                   tile with deployments), reviewedOnly,
                                   providers: [{tile, writers, liveReload,
                                   protected, lastCodeChange?}], warnings}
                                   (its people);
                                   consents (the person's, policy on);
                                   binds (GET /partitions/binds' rows whose
                                   requester is the tile, in its order: the
                                   person's own, every live one for admins;
                                   removed with DELETE /partitions/binds);
                                   orphans (admins);
                                   notices (the person's); for admins also
                                   history (the tile's mode history, newest
                                   first, at most 50: [{op (auto | request
                                   | switch | keep | withdrawn |
                                   backup-erase | partition-restore), from,
                                   to, by?, at, wiped?, reason?,
                                   partition?}]), lastWipe? {from, to, at}
                                   (the last switch that deleted data) and
                                   globalMail? {pending, bytes, expired,
                                   undeliverable?} (the global instance's
                                   inbox: counts only, once it held an
                                   item); for a tile manager who isn't an
                                   admin, in their own session, logShares?
                                   [{user, until}] (who shares their
                                   partition's log now, the manager's own
                                   left out: the logs panel offers each).
                                   A tile's own
                                   credentials (its frames, backend,
                                   terminals) get the tile-level fields and
                                   features only — except the admin tile's
                                   frame under an admin's login (the admin
                                   console driven by an admin), which reads
                                   as an admin, with and without tile;
                                   driven by anyone else it is tile code
                                   here. Without tile: tiles:
                                   [{tile, state, spec, request, error?,
                                   mine?: {partition, state, running,
                                   bytes}, totals? (admins), trust?
                                   (admins: the warnings), globalBinds?,
                                   reviewedOnly?, capsHit? {at, kind
                                   (evicted | refused | deferred), count}
                                   (the last 24 hours), and with
                                   ?untracked=1 untracked? (files the
                                   tile's own repository doesn't track, at
                                   most 20), untrackedCount?,
                                   untrackedError? (admins)}] — the tiles
                                   that are or ask
                                   to be partitioned, that the caller can
                                   read — and for admins isolated, orphans
                                   [{tile, deployment, partition, user,
                                   reason, since}] and now; for a person
                                   credentials [{id, kind (invite |
                                   password | email | sso-provider), by,
                                   at, until, email?, issuer?}]
                                   (credentials an admin made for
                                   them, waiting for their answer) and
                                   notices [{id, at, kind, tile?, text,
                                   hold?}]. Cache-Control: no-store
POST   /partitions/stop            a person's act: their own session, app or
                                   device, the root token, or the admin
                                   tile's frame driven by one (anything
                                   else 403). {tile, partition: "user:<id>"}:
                                   stop that partition's instance — the
                                   person their own, a tile manager or an
                                   admin anyone's. Its token is revoked
                                   first; its data stays and the next
                                   request starts it again. In order: a
                                   partition that isn't user:<id> 400; a
                                   tile the caller can't read 404 as a
                                   missing one (unless they name their own
                                   partition of it); someone else's
                                   partition 403, before anything of it is
                                   said; a tile that isn't partitioned 409;
                                   a person without a partition of the
                                   tile 404. → {ok, tile, partition};
                                   audited
POST   /partitions/reset           as for stop: the person their own, an
                                   admin anyone's. {tile, partition,
                                   confirm: "<tile> <partition>"} (else
                                   409 {error, confirm}). Stops the
                                   instance, ends the person's terminal and
                                   agent sessions on the tile (a new one
                                   answers 409 until the reset is done),
                                   deletes — in every deployment it has
                                   data in — the partition's namespaces
                                   (only when the tile roots its scope: a
                                   partitioned tile in another's scope
                                   uses none), vault, registrations,
                                   records, ledger, log share, backend log,
                                   terminal layers and agent-session
                                   history, and erases the tile's own
                                   backup keys of it
                                   (part:<tile>/<deployment>/<partition
                                   id>; its archives become unreadable —
                                   never the scope root's). The refusals
                                   in stop's order. 409 while the tile is
                                   paused. → {ok, tile, partition, deleted:
                                   {namespaces, layers, histories, subkeys,
                                   bytes}}; audited; an admin's reset tells
                                   the person (push tile.partition-reset
                                   and a notice)
POST   /partitions/purge           admin (as for stop). {tile?,
                                   partition?} (partition: the partition id
                                   u-<32 hex>, or user:<id> of the person it
                                   was): delete orphaned partitions — their
                                   person deleted (or the id someone else's
                                   now), their tile removed — now instead
                                   of 30 days after, as reset deletes one,
                                   subkey erased. A live person's
                                   partition 409 (reset it), none 404; a
                                   paused tile's orphans are skipped with
                                   an error. A removed tile left with
                                   nothing loses its mode record. → {ok,
                                   purged: [{tile, deployment, partition,
                                   user, reason, since, deleted, error?}]};
                                   audited
POST   /partitions/share-log       PersonOnly (as for /partitions/consents).
                                   {tile, days?: 1-14 (7)}: share the
                                   person's partition's backend log of the
                                   tile with its managers and admins until
                                   then (GET /logs?user=). Kept in the
                                   partition's directory (a reset, a purge
                                   or a switch takes it). 404 without a
                                   partition of the tile. → {ok, tile,
                                   shared, until}; audited
DELETE /partitions/share-log       PersonOnly. {tile}: end the share now. →
                                   {ok, tile, shared: false}
POST   /partitions/credential-confirm
                                   PersonOnly. {id, allow: bool}: the
                                   person allows (it takes effect) or
                                   refuses (a link stops working; a
                                   password or email is dropped; a
                                   provider change unbinds their SSO
                                   email) a credential an admin made for
                                   them while the workspace policy
                                   credentialResetConfirm was on (GET
                                   /partitions' credentials). Unanswered,
                                   it takes effect 24 hours after they were
                                   told; a refusal still revokes one that
                                   is unused. 409 {error, decision:
                                   "already-effective"}: a link no longer
                                   pending (redeemed, replaced or expired)
                                   — change the password and sign out
                                   everywhere. 404 for one that isn't
                                   waiting. → {ok, id, kind, decision:
                                   allowed|refused}; audited; a notice
POST   /partitions/reviewed        admin (as for stop). {tile, on: bool}:
                                   the tile's "reviewed code only" switch
                                   (docs/partitions.md §Reviewed code
                                   only). on needs the tile partitioned
                                   (409) and the primary of the tile and of
                                   every non-partitioned provider bound to
                                   it protected (409 {error,
                                   unprotected}); while on, unprotecting
                                   any of them (POST /deployments/protect
                                   {on: false}) and binding into the tile
                                   a provider that isn't partitioned and
                                   whose primary isn't protected answer
                                   409. off always succeeds. → {ok, tile,
                                   reviewedOnly: {on, by?, at?,
                                   unprotected?}}; audited

POST   /partitions/mail            a partitioned tile's global instance (its
                                   instance token, on the primary) or a
                                   person's partition of it (its backend,
                                   that person's frames, terminals and agent
                                   sessions); everyone else 403 — people
                                   outside the tile's credentials (admins
                                   included), the root token, frames and
                                   terminals acting in no person's
                                   partition (they read global's inbox,
                                   below, but send nothing), view-as, other
                                   tiles, a non-primary deployment and every
                                   delivery principal. {to, topic, data?, ttl?,
                                   source?} → {ok, id}. The global instance
                                   mails "user:<id>" — a person who exists,
                                   is enabled and can read the tile, else
                                   404 "no such person here: user:<id>",
                                   whatever the reason — or "global"; a
                                   person's partition mails "global" only
                                   (403 otherwise, and on a tile without a
                                   global instance). from is stamped by
                                   xbind — global or user:<id> — never read
                                   from the body (a from field is ignored).
                                   Limits: topic ≤ 256 bytes, topic + data
                                   ≤ 1 MiB (413); an inbox holds ≤ 1000
                                   items and ≤ 64 MiB (507 to the sender);
                                   in the global instance's inbox each
                                   sender (global, each person) has ≤ 100
                                   items and ≤ 8 MiB waiting (507 "your
                                   share … is full" to that sender only).
                                   ttl: seconds, 1–2592000 (default 7 days;
                                   400 outside). source (the global instance
                                   to a person only): a private trigger's
                                   source, counted as kind trigger in that
                                   person's egress ledger — never the
                                   content. 409 while the tile is paused (a
                                   mode switch pending or deleting); 503
                                   while the vault is sealed. Stored in
                                   data/partitions/<tile-key>/<dep>/mail.db,
                                   sealed with the vault barrier; not backed
                                   up; not in the audit stream (data plane).
                                   Rings the addressee's doorbell (below)
GET    /partitions/mail            the callers of POST (403 and 409
                                   alike), and — the global instance's
                                   inbox only — the tile's frames, terminals
                                   and agent sessions acting as global (the
                                   owner token's frames, root terminals; on
                                   the primary; never view-as), reading the
                                   caller's OWN inbox only — its person's
                                   partition's, or the global instance's; no
                                   parameter names another, and admins see
                                   counts only (GET /partitions).
                                   ?after=<id> ?limit=1-1000
                                   (100) → {items: [{id, from, topic, data,
                                   at, expires}], more}: oldest first. A
                                   page stops at limit or past ~8 MiB of
                                   data (at least one item): only more
                                   false ends the inbox. Expired items are
                                   dropped (counted as expired); an item
                                   that can't be opened (sealed under
                                   another key, damaged) is dropped
                                   (counted as undeliverable) and never
                                   stops the page. 503 while the vault is
                                   sealed and the page would hold items (an
                                   empty inbox answers 200)
POST   /partitions/mail/ack        the same callers as GET. {ids} (≤ 1000)
                                   → {ok}: removes those items of the
                                   caller's own inbox; ids acked or expired
                                   already are nothing to do. It never
                                   opens an item: it works while the vault
                                   is sealed. Delivery is at-least-once
                                   until acked or expired: dedupe by id
```

## Partition mail doorbell

**Partition mail doorbell** (docs/partitions.md §Partition mail). While an
inbox of a partitioned tile holds unacked items (`POST /partitions/mail`),
xbind POSTs to the tile's `partitionMail` path — declared beside `"global"`
in `xbin.json` — on the addressee's instance, through the proxy, as
`X-XBin-From: xbin/mail`, `X-XBin-Role: writer` and `X-XBin-Partition: <the
addressee>` (`global`, or `user:<id>` with its `X-XBin-Partition-Id`):

```
{"partition":"user:<id>" | "global","pending":<unacked items>}
```

The handler reads with `GET /partitions/mail` and acks; the answer's status
only is logged. A new item rings at once and starts the steps over; the
addressee's start (whatever starts it) rings again unless a ring was in
flight at it or came after it (the doorbell's own ring often started it);
and while items remain it rings after 1 min, 5 min, 30 min, 2 h, then every
6 h — a start doesn't shorten the steps. A person's stopped partition is started for it
— a background start that counts against the tile's mail starts (6 a
minute) — only when it has run before, and only while its person exists (the
same incarnation), is enabled and can read the tile; otherwise its items wait
for its next start. Nothing rings while the tile is paused, disabled or
declares no `partitionMail` (its code polls `GET`). A restart of xbind loses
no mail: boot rings every inbox holding items. The feature word is
`partition-mail/1`.
