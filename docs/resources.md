# Resources: kv, blob, bus, cron, sqlite

Resources are broker-provisioned shared state and infrastructure, addressed
in the same grant grammar as element APIs. Declare them at the scope (or
workspace) level; request access in `uses`; same-scope is auto-granted,
cross-scope needs owner approval ([auth.md](/docs/auth.md)).

```jsonc
// apps/thing/scope.json — this subtree is an app with these resources
{ "resources": {
    "db":     { "type": "sqlite" },
    "events": { "type": "kv" },
    "files":  { "type": "blob" },
    "bus":    { "type": "bus" },
    "cron":   { "type": "cron" } } }

// workspace-level (rare): declare in the workspace buxon.json "resources";
// address as res:workspace/<name>.
```

```jsonc
// a component's buxon.json
{ "uses": [
    { "target": "res:apps/thing/events", "role": "writer" },
    { "target": "res:apps/thing/bus",    "role": "reader" } ] }
```

Delivery: each granted resource appears in the backend env as
`BUXON_RES_<NAME>` (name uppercased; e.g. `events` → `BUXON_RES_EVENTS`).
For brokered types the value is the canonical id you pass to the APIs; for
sqlite it's a file path. State lives under `data/resources/<scope>/` —
gitignored, captured by `bx backup`.

Roles: `reader` / `writer` as usual (`subscriber`/`publisher` accepted for
bus).

## kv — small structured state

Namespaced key-value store (single workspace bbolt db under the hood).
Values up to 1 MiB. Keys are strings; use `/`-separated prefixes and the
prefix-list to model collections.

```go
kv := buxon.KV(buxon.Resource("events"))
kv.PutJSON("2026-07-02/standup", ev)
var ev Event; err := kv.GetJSON("2026-07-02/standup", &ev)
keys, _ := kv.List("2026-07-02/")
kv.Delete("2026-07-02/standup")
```

HTTP (any language, via the gateway with your instance token):

```
GET    /api/buxon/kv/res:apps/thing/events/?prefix=2026-07-02/   → {"keys":[…]}
GET    /api/buxon/kv/res:apps/thing/events/<key>                 → raw bytes
PUT    /api/buxon/kv/res:apps/thing/events/<key>   (body = value)
DELETE /api/buxon/kv/res:apps/thing/events/<key>
```

## blob — files with a home

A directory served/written through the broker. For attachments, uploads,
generated artifacts. 256 MiB per write.

```
GET    /api/buxon/blob/res:apps/thing/files/          → {"entries":[…]} (dirs end with /)
GET    /api/buxon/blob/res:apps/thing/files/a/b.png   → bytes
PUT    /api/buxon/blob/res:apps/thing/files/a/b.png   (body = content)
DELETE /api/buxon/blob/res:apps/thing/files/a/b.png
```

## bus — cross-app reactivity

In-memory pub/sub topics. **At-most-once**: subscribers that are offline
miss messages — the bus is for "something changed, go look", not for
durable queues. Keep the truth in kv/sqlite and treat bus messages as cache
invalidation.

Publish (backend):

```go
buxon.Publish(buxon.Resource("bus"), "events/created", ev)
```

Subscribe (frontend — this is how an email widget updates live when the
calendar changes):

```js
buxon.bus.on('res:apps/thing/bus/events/', (topic, data) => refresh());
```

Backends don't hold subscriptions (they'd die at idle-reap anyway): if a
backend must *react* to bus traffic, schedule a cron sweep or let the
frontend drive. Publishing over HTTP:
`POST /api/buxon/bus/publish {"resource":"res:…","topic":"…","data":…}`.

Document your topics in your `API.md` — they're part of your contract.

## cron — scheduled work

Elements register jobs that call **their own endpoints** on a schedule
(cron can't be aimed at other elements; the owner can register anything).
Invocations arrive as `POST <path>` with `X-Buxon-From: buxon/cron` and the
role you chose (bounded by your own declared roles). Jobs persist across
restarts; a tick on an idle backend wakes it (lazy start), so cron + idle
reaping compose correctly.

```
PUT /api/buxon/cron/jobs
{"name":"sweep","resource":"res:apps/thing/cron",
 "schedule":"*/5 * * * *","path":"/sweep","role":"writer"}
```

Schedules: standard 5-field cron or `@every 30s` / `@hourly`. List:
`bx cron ls` or `GET /api/buxon/cron/jobs`. Delete:
`DELETE /api/buxon/cron/jobs/<name>`. Failures are logged (buxond log +
component log); there are no retries — make handlers idempotent and let the
next tick catch up.

## sqlite — a real database, zero ops

```jsonc
{ "resources": { "db": { "type": "sqlite" } } }
// component: { "uses": [{ "target": "res:apps/thing/db", "role": "writer" }] }
```

Same-scope components get `BUXON_RES_DB` = a file path under
`data/resources/`. **Open exactly that path — don't invent your own** (`./db`,
`/tmp/…`, a path under your component dir). buxond binds the resource's directory
read-write, so a fresh db and its `-wal`/`-shm` sidecars persist there; any other
path lands in the backend's throwaway overlay — lost on restart and not captured
by backups.

```go
import (
	"database/sql"
	_ "modernc.org/sqlite" // CGO-free
	buxon "github.com/magik6k/buxon/sdk"
)

// buxon.Resource("db") == $BUXON_RES_DB, the file path. Just open it.
db, err := sql.Open("sqlite", buxon.Resource("db")+"?_journal_mode=WAL&_busy_timeout=5000")
```

Use WAL mode if multiple components in the scope share the db.

**Cross-scope sqlite is deliberately not a thing.** The file path is only
handed to same-scope components; other apps go through your service API.
Sharing a database file across app boundaries welds schemas together — the
whole point of the roles/API model is to avoid that. (A brokered read-only
query API may come later if a real need shows up.)

## Choosing

| Need | Use |
|------|-----|
| App state, queries, transactions | `sqlite` |
| Settings, small documents, indexes by prefix | `kv` |
| Files | `blob` |
| "Something changed" notifications | `bus` |
| Periodic work | `cron` |
| Another app's data | **their API** with a `reader` grant |
| Credentials for external services | the vault ([auth.md](/docs/auth.md)) — not a resource, private per element |
