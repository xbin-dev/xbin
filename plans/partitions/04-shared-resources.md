# 04 — Shared resources and partition mail

The owner's model: the global instance "communicates with the user
partitions over some shared resource (bus or db)". This file specifies that
shared plane, and the one new fabric primitive it needs so the global
instance can hand something to **one** person's partition without any other
partition, any admin, or global's own storage keeping it: **partition mail**,
an xbind-owned, durable, per-addressee drop box (PD-15). Revision 2 replaces
the earlier "addressed bus event" design: that one let admins and the root
token forge global's doorbell, kept handoff payloads in global's storage,
and delivered to frames and via a 202 that broke `== 200` checks (S4, C21).

## Current behaviour

- `scope.json` resources: `filesystem|sqlite|kv|blob|bus|cron`
  (`internal/registry/registry.go:253`); provisioning `Provision`
  (`internal/broker/resources.go:50`: file-backed ones are gocryptfs volumes,
  kv in bbolt with barrier-sealed values, bus in memory).
- Several tiles of one scope already mount the same sqlite/filesystem
  read-write; the agent's engine lock is an `flock` on `<db>.engine` shared
  across blue/green generations in different sandboxes
  (`builtin-templates/agent/_backend/owner.go:35`, D81) — the multi-writer
  precedent.
- Bus: `POST /api/xbin/bus/publish {resource, topic, data}`
  (`resources.go:527`) → the hub (`/ws/events`, filtered by `busFilter`
  `:575`) and push subscriptions (`bussubs.go:293` `publishIn`; delivery
  `:420`, at-most-once, 256 queue, 100/s, 2 min timeout;
  `bussubs.go:50-53`). The SDK's `Publish` treats anything but 200 as an
  error (`sdk/xbin.go:342`).
- Admins pass `allowRes` for every resource (`internal/broker/broker.go:533-535`),
  and a person reaches a scope's primary (= global's) namespace
  (`resources.go:648-656`).

## The change

### 1. Declaring a shared resource

```jsonc
// apps/docs/scope.json (the owner's document-manager example)
"resources": {
  "docs":    { "type": "sqlite" },                 // one per partition (global's = today's)
  "public":  { "type": "kv",  "shared": "read" },  // global writes, partitions read
  "board":   { "type": "sqlite", "shared": true }, // every partition + global read-write
  "beat":    { "type": "cron" }                    // jobs always belong to their registrant
}
```

- `shared` is meaningful only in a partitioned scope (01 §4); elsewhere it is
  ignored without a warning (templates ship it ready for the opt-in, 08 §1).
- **A shared resource lives at today's keys** (main's, or the deployment's
  namespace for a non-primary deployment). An older xbind — which ignores
  both `partition` and `shared` — sees shared data exactly where it was.
  Shared data is the tile's data, so a confirmed mode switch deletes it
  with everything else (01 §2.6).
- **`"shared": true`**: every tile principal of the scope, in any partition,
  reaches it read-write the same way as today — sqlite/filesystem through the
  same canonical `XBIN_RES_<NAME>` path (`ResBind{Shared: true}`, 03 §B.4),
  kv/blob/bus through the same `res:` id.
- **`"shared": "read"`** (S8): the global instance reads and writes; user
  partitions read only — filesystem bound read-only, kv/blob writes and bus
  publishes refused (403). Use it for anything a partition *acts on*:
  routing tables, configuration, a registry of who owns what. Not allowed for
  `sqlite` (01 §3: a read-only mount can't open a database another process
  writes through a rollback journal) — use kv, or `true`.
- Access control is otherwise today's (grants on the tile). Nothing about
  the shared plane is private between partitions: **anything a partition
  writes to a `true` resource is readable — and changeable — by every
  partition's code and by the global instance.** A tile must never let a row
  in a `true` resource decide where a person's private data or resources go;
  that belongs in a `read` resource written by global, or in the partition's
  own data (docs/partitions.md, with the agent as the worked example, 08).

### 2. Namespaces for brokered types

`busNamespace`, `reachRes` and the kv/blob handlers pick the namespace per
03 §B.2: shared → today's; partitioned → the caller's partition's (`global`
→ today's). Events on a partitioned bus are stamped `Event.Partition = <key>`
(02 §9) and reach only that partition's subscriptions and frames. Events on a
shared bus carry no partition and reach every reader (as today) — push
subscriptions of user partitions only while those partitions run (03 §D).

### 3. Partition mail

A tile that declares `global` gets one mail drop box per addressee:
`global`, and each person's partition. xbind owns it, stores it durably,
stamps the sender, and lets only the addressee read it.

```
POST /api/xbin/partitions/mail   {to: "user:alice" | "global", topic, data, ttl?}
  → 200 {"ok": true, "id": "<mail id>"}
GET  /api/xbin/partitions/mail[?after=<id>&limit=<n>]
  → 200 {"items": [{"id","from","topic","data","at","expires"}], "more": false}
POST /api/xbin/partitions/mail/ack   {ids: [...]}
  → 200 {"ok": true}
```

**Who may send (PD-15, S4):**

| Sender | May mail |
|---|---|
| the tile's **global instance** (its instance token, partition `global`) | any `user:<id>` of a live person who can read the tile (404 `no such person here` otherwise — never reveals more), or `global` |
| a user partition's principals (instance, frame, terminal, agent session) | `global` only; 403 otherwise — no person-to-person channel through xbind |
| everyone else — persons (admins included) outside the tile's own principals, the root token, owner-token frames, global's frames, other tiles, cron/bus/mail delivery principals | nothing: 403 |

**`from` is stamped by xbind**, never taken from the body: `global` for the
global instance, `user:<id>` for a person's partition. A handler can trust it.

**Storage.** `data/partitions/<TileKey>/<dep>/mail.db` (bbolt, xbind-owned,
masked from every sandbox), one bucket per addressee (`global`, `<pkey>`),
values sealed with the vault barrier like kv values. Limits: an item ≤ 1 MiB;
an inbox ≤ 1000 items and 64 MiB (a full inbox answers 507 to the sender);
`ttl` default 7 days, max 30. Only the addressee's principals read
(`GET`) and ack; nobody else — no admin route, no listing — ever reads an
inbox; admins see counts (`GET /partitions`, metadata). Reset/purge of a
partition and the user-delete hook remove its bucket; a mode switch removes
the tile's whole mail store (01 §2.6). Mail isn't backed up (transient by
design; documented).

**Doorbell.** When an inbox has unacked items, xbind POSTs
`{"partition": "<addressee>", "pending": <n>}` to the tile's declared
`partitionMail` path (01 §1) on the addressee's instance, as principal
`xbin/mail` (role `writer`, `Partition` = addressee, `Background`), and the
handler pulls with `GET` and acks. Delivery rules:
- to a **running** instance: at once, then re-rung with backoff (1 min, 5 min,
  30 min, 2 h, then every 6 h) while unacked items remain;
- **starting** a stopped user partition is allowed only when (a) that
  partition has run before (its `partition.json` has `lastStarted`) — mail
  never creates a person's first instance (S1, S20), (b) the person is live
  (PD-20), and (c) background admission and the mail start rate (6/min per
  tile, 03 §A.5) allow; otherwise the items wait and the doorbell rings at
  the partition's next start, whatever starts it;
- a tile without `partitionMail` gets no doorbell: its code polls `GET`
  (e.g. at start and on a timer).
Semantics: **at-least-once until acked or expired**; handlers dedupe by `id`.
Expired items are dropped and counted.

**Why a drop box and not the bus.** It carries a person's private items
(a DM, a webhook payload meant for them) from global to one partition without
global keeping a copy, without a forgeable `from`, without reaching frames or
admins' sockets, and survives the partition being stopped.

### 4. Patterns (docs/partitions.md)

- **Global view of opted-in items** (the owner's document-manager example):
  a person's partition marks a document "global" by mailing global
  `publish {docId, title, body}`; global (the only writer of the `"read"`
  resource `public`) stores it keyed by (`from`, docId) and serves
  non-partitioned callers from `public` only. Unmarking mails `unpublish`.
  Global trusts `from`, never a person field in `data`. The partition's
  private data never leaves it.
- **Global → one person** (a webhook or chat message for alice): global mails
  `user:alice` the item (≤ 1 MiB inline; larger content staged by global and
  fetched by alice's partition through F5 — attributed to alice, 05 §6 — then
  deleted by global on ack, documented as passing through global's storage).
  Global keeps only routing metadata (id → destination), never the content.
- **Person → global** (a reply to send to a chat): mail `global`
  `{inReplyTo: <handoff id>, text}`; global resolves the destination from its
  **own** record of that handoff and checks that `from` is the person it was
  handed to (08 §5) — a partition can't pick an arbitrary destination.
- **Coordination across instances**: `flock` on a file in a shared
  filesystem/sqlite directory (as D81 does), or sqlite transactions with
  `busy_timeout` on a `true` resource.

### 5. What is *not* offered

- No partition-to-partition channel; no global read of a partition's data; no
  global call into a partition; no "broadcast to all partitions" mail (a
  shared bus event is that, delivered to running partitions and readers'
  frames).
- No addressed bus events, no `mailboxRole`, no delivery of mail to frames
  (C21): a frame reads its partition's state through its own backend.
- No per-partition ACL inside a shared resource (xbind doesn't parse sqlite
  or kv values) — hence `"shared": "read"`.

## Wire (for docs/protocol.md, docs/resources.md, docs/elements.md)

- `scope.json` resource key `shared: true | "read"` (any type but cron;
  `"read"` not for sqlite).
- `xbin.json` `partitionMail` (01 §1).
- `POST /api/xbin/partitions/mail`, `GET /api/xbin/partitions/mail`,
  `POST /api/xbin/partitions/mail/ack` (shapes, limits, 403/404/507 above);
  the doorbell body; principal `xbin/mail`; feature `partition-mail/1`
  (06 §6).
- `/ws/events` `bus` events of partitioned namespaces carry `partition`.
- `POST /bus/publish` is **unchanged** (no new field, still 200).

## Tests

- broker: shared sqlite path identical in every partition's env and bound
  from today's volume; a partitioned one bound from the partition's volume;
  a `"read"` kv refuses a user partition's `PUT` (403) and accepts global's.
- broker: `TestMailRules` (the sender table: global instance → user ok;
  admin person, root token, owner-token frame, global's frame, another tile,
  a cron/bus/mail principal → 403; user partition → global ok, → another
  person 403; unknown/unreadable person → 404; `from` stamped whatever the
  body says).
- broker: an inbox is readable only by its addressee's principals; admins get
  403 on `GET` and see counts in `GET /partitions`; ack deletes; ttl expiry;
  limits (1 MiB, 1000 items, 64 MiB → 507); reset/user-delete remove the
  bucket.
- broker: doorbell rings a running instance; re-rings with backoff; starts a
  stopped partition that has run before (rate-limited); **never starts a
  never-run partition**; skips a disabled person; rings at the next start.
- integration: two partitions + global of a fixture tile
  (`test/partitions_test.go`) exchange mail both ways; a restart of xbind
  between send and ack loses nothing; an unaddressed publish answers 200 as
  today.
