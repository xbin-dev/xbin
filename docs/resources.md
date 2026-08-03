# Resources: filesystem, kv, blob, bus, cron, sqlite

Resources are broker-provisioned shared state and infrastructure, addressed
in the same grant grammar as element APIs. Declare them at the scope (or
workspace) level; request access in `uses`; same-scope is auto-granted,
cross-scope needs owner approval ([auth.md](/docs/auth.md)).

```jsonc
// apps/thing/scope.json — this subtree is an app with these resources
{ "resources": {
    "store":  { "type": "filesystem" },
    "events": { "type": "kv" },
    "files":  { "type": "blob" },
    "bus":    { "type": "bus" },
    "cron":   { "type": "cron" } } }

// workspace-level (rare): declare in the workspace xbin.json "resources";
// address as res:workspace/<name>.
```

```jsonc
// a component's xbin.json
{ "uses": [
    { "target": "res:apps/thing/events", "role": "writer" },
    { "target": "res:apps/thing/bus",    "role": "reader" } ] }
```

Delivery: each granted resource appears in the backend env as
`XBIN_RES_<NAME>` (name uppercased; e.g. `events` → `XBIN_RES_EVENTS`).
For brokered types (kv/blob/bus/cron) the value is the canonical id you pass to
the APIs; for **filesystem** it's a directory path, and for **sqlite** a file
path (both a real rw path bound into your sandbox — the *decrypted* mount when
encryption is on, see §Encryption). The on-disk bytes are ciphertext under
`data/resources-enc/<scope>/` (or plaintext `data/resources/<scope>/` when the
vault is off) — gitignored, captured by backups.

Roles: `reader` / `writer` as usual (`subscriber`/`publisher` accepted for
bus).

## Encryption at rest

**Resource state is always encrypted at rest under vault-derived keys** — there
is no plaintext resource path. `filesystem`, `sqlite`, and `blob` are each a
per-resource gocryptfs mount (xbind mounts the *decrypted* view into your
sandbox); `kv` values are envelope-encrypted per bucket. It's **transparent to
your code** — you always read and write plaintext; only the on-disk bytes
(`data/resources-enc/…`, `data/kv.db`) are ciphertext, so a stolen disk or backup
snapshot yields nothing without the key. The key comes from the vault barrier —
a passphrase / manual unseal in production, or a built-in dev key under a bare
`make dev` ([auth.md](/docs/auth.md)). Consequences:

- **If encryption can't run, the resource is unavailable — never plaintext.** A
  component that uses a `filesystem`/`sqlite`/`blob`/`kv` resource is **held**
  (won't spawn) while the vault is sealed or gocryptfs is missing, and
  `kv`/`blob` API calls return `503`. Everything resumes on unseal.
- **Backups are plaintext.** `bx backup` and the archive interface stream
  *decrypted* data — encrypting the archive is the archiver tile's job
  (`plans/vault-data.md`).

Only an explicit `--insecure-vault` (or `--no-auth`) stores resource data
plaintext, for throwaway/inspection setups.

## kv — small structured state

Namespaced key-value store (single workspace bbolt db under the hood).
Values up to 1 MiB. Keys are strings; use `/`-separated prefixes and the
prefix-list to model collections.

```go
kv := xbin.KV(xbin.Resource("events"))
kv.PutJSON("2026-07-02/standup", ev)
var ev Event; err := kv.GetJSON("2026-07-02/standup", &ev)
keys, _ := kv.List("2026-07-02/")
kv.Delete("2026-07-02/standup")
```

HTTP (any language, via the gateway with your instance token):

```
GET    /api/xbin/kv/res:apps/thing/events/?prefix=2026-07-02/   → {"keys":[…]}
GET    /api/xbin/kv/res:apps/thing/events/<key>                 → raw bytes
PUT    /api/xbin/kv/res:apps/thing/events/<key>   (body = value)
DELETE /api/xbin/kv/res:apps/thing/events/<key>
```

## blob — files with a home

A directory served/written through the broker. For attachments, uploads,
generated artifacts. 256 MiB per write.

```
GET    /api/xbin/blob/res:apps/thing/files/          → {"entries":[…]} (dirs end with /)
GET    /api/xbin/blob/res:apps/thing/files/a/b.png   → bytes
PUT    /api/xbin/blob/res:apps/thing/files/a/b.png   (body = content)
DELETE /api/xbin/blob/res:apps/thing/files/a/b.png
```

## bus — cross-app reactivity

In-memory pub/sub topics. **At-most-once**: subscribers that are offline
miss messages — the bus is for "something changed, go look", not for
durable queues. Keep the truth in kv/sqlite and treat bus messages as cache
invalidation.

Publish (backend):

```go
xbin.Publish(xbin.Resource("bus"), "events/created", ev)
```

Subscribe (frontend — this is how an email widget updates live when the
calendar changes):

```js
xbin.bus.on('res:apps/thing/bus/events/', (topic, data) => refresh());
```

Backends don't hold subscriptions (they'd die at idle-reap anyway): if a
backend must *react* to bus traffic, schedule a cron sweep or let the
frontend drive. Publishing over HTTP:
`POST /api/xbin/bus/publish {"resource":"res:…","topic":"…","data":…}`.

Document your topics in your `API.md` — they're part of your contract.

## cron — scheduled work

Elements register jobs that call **their own endpoints** on a schedule
(cron can't be aimed at other elements; the owner can register anything).
Invocations arrive as `POST <path>` with `X-XBin-From: xbin/cron` and the
role you chose at registration (a job is self-targeted — a tile is already
admin of itself — so the role isn't separately vetted). Jobs persist across
restarts; a tick on an idle backend wakes it (lazy start), so cron + idle
reaping compose correctly.

```
PUT /api/xbin/cron/jobs
{"name":"sweep","resource":"res:apps/thing/cron",
 "schedule":"*/5 * * * *","path":"/sweep","role":"writer"}
```

Schedules: standard 5-field cron or `@every 30s` / `@hourly`. List:
`bx cron ls` or `GET /api/xbin/cron/jobs`. Delete:
`DELETE /api/xbin/cron/jobs/<name>`. Failures are logged (xbind log +
component log); there are no retries — make handlers idempotent and let the
next tick catch up.

## filesystem — a persistent read-write directory

The primitive for "my backend needs a real writable directory": a db, a cache,
generated files, a git checkout, whatever. xbind binds it read-write into your
sandbox and backs it up.

```jsonc
{ "resources": { "store": { "type": "filesystem" } } }
// component: { "uses": [{ "target": "res:apps/thing/store", "role": "writer" }] }
```

Same-scope components get `XBIN_RES_STORE` = a **directory** path under
`data/resources/`. **Write only inside it** — anywhere else is the backend's
throwaway overlay (lost on restart, not backed up).

```go
dir := xbin.Resource("store")             // == $XBIN_RES_STORE, a directory
os.WriteFile(filepath.Join(dir, "notes.txt"), data, 0o644)
db, _ := sql.Open("sqlite", filepath.Join(dir, "app.db")+"?_journal_mode=WAL")
```

### Container stores (cap:containers scopes)

A container layer store is the one workload a normal encrypted mount can't
host: podman writes `0555` layer directories and then creates inside them,
chowns files to arbitrary sub-uids, and expects whiteouts and file
capabilities to round-trip — all things an unprivileged FUSE daemon doing the
real I/O must refuse. So **filesystem resources of a scope holding
`cap:containers` mount in gocryptfs *single-tenant mode*** (an xbin patch,
`hack/gocryptfs-patches/`), automatically:

- **Ownership, mode, and special files are virtualized**: chown/chmod/mknod
  always succeed; uid/gid/mode/rdev live in an *encrypted* xattr on the cipher
  file, so identity metadata is as opaque at rest as contents. Device nodes,
  FIFOs and whiteouts are stored as empty cipher files with their virtual
  type. `security.capability` round-trips (encrypted) so file caps in image
  layers survive.
- **No permission checks inside the mount.** The mount serves exactly this
  scope's sandboxes — which already share the resource read-write — so
  reaching it *is* the access decision. Nothing outside the sandbox can see
  the decrypted view (the mountpoint sits under xbind's 0700 runtime dir).
- **Same on-disk format.** Granting or revoking `cap:containers` just
  remounts the store in the other mode; existing files keep working (files
  written pre-grant appear with their real attributes).

Requirements (checked by `bx doctor`): the xbin-built gocryptfs (`make build`
applies the patchset — a stock binary refuses with a pointed error, never a
silently broken store) and `user_allow_other` in `/etc/fuse.conf` (the system
installer enables it; user-mode installs need root to add it once).

Known limits: symlink ownership is not preserved (Linux forbids user xattrs
on symlinks — extraction still succeeds; `podman diff`/commit see them
daemon-owned), and creating *real* device nodes stays kernel-refused for
rootless podman on any filesystem — extraction skips them, same as on a plain
directory.

## sqlite — a filesystem resource pointed at a db file

A convenience over `filesystem` for the common "I just want one sqlite db" case:

```jsonc
{ "resources": { "db": { "type": "sqlite" } } }
```

Same rw-directory mechanism, but `XBIN_RES_DB` is the `.sqlite` **file** path —
just open it (with `modernc.org/sqlite` for CGO-free builds). Use WAL if multiple
same-scope components share it. Prefer `filesystem` when you need a general
directory rather than a single db.

```go
db, _ := sql.Open("sqlite", xbin.Resource("db")+"?_journal_mode=WAL&_busy_timeout=5000")
```

**Cross-scope direct filesystem/sqlite is deliberately not a thing.** The path is
only handed to same-scope components; other apps go through your service API.
Sharing files across app boundaries welds schemas together — the whole point of
the roles/API model is to avoid that.

## Choosing

| Need | Use |
|------|-----|
| A writable directory (files, caches, a git checkout, anything) | `filesystem` |
| App state, queries, transactions (one db) | `sqlite` |
| Settings, small documents, indexes by prefix | `kv` |
| Files | `blob` |
| "Something changed" notifications | `bus` |
| Periodic work | `cron` |
| Another app's data | **their API** with a `reader` grant |
| Credentials for external services | the vault ([auth.md](/docs/auth.md)) — not a resource, private per element |
