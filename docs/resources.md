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

**Names** are letters, digits, `.`, `_` and `-`, start with a letter or
digit, and are at most 64 characters (D118). A name becomes a directory, a
key-derivation label and a kv bucket suffix, so anything else (`a/b`, `..`,
spaces) is refused. The resource isn't provisioned, and the scope's tiles
show a manifest error naming it (`bx doctor`, `manifestError`). A
workspace-level one is left out and logged.

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

That `<scope>` is the scope's **data key**: its path with `/` written as `~`
(`apps/thing` → `apps~thing`; workspace-level resources use `workspace`).
Two paths can map to one key (`apps/thing` and a directory literally named
`apps~thing`, or a scope at a top-level `workspace/`), so each key has one
holder: the scope that had it first (D118). A scope whose key another scope
holds keeps being a scope, but none of its resources are provisioned, and
its tiles show a manifest error naming the holder (`bx doctor`,
`manifestError`). Rename its directory. Creating a tile at such a path is
refused.

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
  (won't spawn) while the vault is sealed, gocryptfs is missing or the
  resource's mount failed, and `kv`/`blob` API calls return `503`. A call
  to a held component's backend answers `502` naming the resource and the
  cause ("component apps/x is held: it uses the encrypted resource
  res:apps/x/db, and …"). Everything resumes on unseal.
- **On Ubuntu, AppArmor must let `fusermount3` mount under the workspace.**
  gocryptfs mounts through `fusermount3`, whose AppArmor profile allows FUSE
  mount points only under home dirs, `/mnt`, `/media` and `/tmp`. With the
  workspace elsewhere (the installer's `/opt/xbin/workspace`) every mount
  fails, those components stay held, and xbind logs `resource encryption:
  mount failed … fusermount3: mount failed: Permission denied` followed by
  the fix. The system installer adds the rule for you (D110,
  [operations](/docs/overview/15-operations.md)); by hand, as root, for a
  workspace at `/srv/ws`:

  ```sh
  sudo tee -a /etc/apparmor.d/local/fusermount3 >/dev/null <<'EOF'
  # BEGIN xbin (install.sh) — encrypted resources (gocryptfs) under the workspace
  mount fstype=@{fuse_types} options=(nosuid,nodev) options in (ro,rw,noatime,dirsync,nodiratime,noexec,sync) -> "/srv/ws/.xbin/resenc/**/",
  umount "/srv/ws/.xbin/resenc/**/",
  # END xbin
  EOF
  sudo apparmor_parser -r /etc/apparmor.d/fusermount3
  ```

  then restart xbind (`sudo systemctl restart xbin`) so it mounts them now
  rather than at its next reprovision.
- **Backups are sealed.** `bx backup` reads the *decrypted* data and xbind
  seals every archive itself under a backup key the vault's data key wraps
  (AES-256-GCM), so the archiver tile only ever holds ciphertext; a plain
  archive made before sealing still restores. Restoring on a new machine
  needs the exported key bundle (`bx backup keys export`) and the vault
  passphrase; `bx backup erase` crypto-erases a tile's backups
  ([14-lifecycle.md](/docs/overview/14-lifecycle.md) §Sealed archives). No
  backup runs while the vault is sealed or not set up yet.

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

Publishing over HTTP:
`POST /api/xbin/bus/publish {"resource":"res:…","topic":"…","data":…}`.

A backend that must *react* to bus traffic registers a **push
subscription** (D85) instead of holding a socket (idle reaping would sever
it): xbind POSTs each matching event to one of its own endpoints, starting
an idle backend the way a cron tick does.

```
PUT /api/xbin/bus/subscriptions
{"name":"deploys","resource":"res:apps/ci/bus","prefix":"deploy/",
 "path":"/on-deploy","role":"writer"}
```

- The subscriber needs `reader` on the bus (declare it in `uses`); it is
  checked at registration and again at every delivery. Like cron, a
  component subscribes only itself (the owner may name one with
  `component`); `role` (default `writer`) is what the delivery carries.
- Each delivery is `POST <path>` with `X-XBin-From: xbin/bus` and
  `{"id","subscription","resource","topic","data","ts"}`; `id` is the
  event's, shared by every subscription it reaches.
- **At-most-once**, like the bus: one queue per subscription (256; the
  newest dropped when full), one POST in flight, 2 min timeout, no retries,
  and at most 100 events a second (a loop guard for a handler that
  publishes to the bus it consumes). Treat it as "go look", keep truth in
  kv/sqlite.
- Idempotent by name (subscribe at every start); ≤64 per component.
  `GET /api/xbin/bus/subscriptions` lists yours with
  delivered/dropped/failed counters and the last error;
  `DELETE /api/xbin/bus/subscriptions/<name>` removes one. SDK:
  `xbin.Subscribe` / `xbin.Unsubscribe` / `xbin.BusEvent`.
- They persist, ride along in the component's backup, and pause while the
  component is disabled; a subscriber that no longer exists loses them.
- In a tile deployment that isn't the primary they deliver to that
  deployment (§Tile deployments below), unless a tile manager switched its
  deliveries off: then the subscription answers 200 with `"dormant": true`
  and receives nothing. A subscription on the tile's own bus receives only
  events published in its deployment's data; one on another scope's bus
  receives that scope's primary's, under the edge policy.
- In a partitioned tile ([partitions.md](/docs/partitions.md)) each
  person's partition subscribes for itself (at most 16): it receives its
  own partition's events on its scope's bus — which start it only when its
  own tile published them — and a shared bus's, or one another tile
  published for the person, only while it runs (counted as `dormantDrops`
  otherwise) — never the global instance's. Another partitioned tile's bus
  needs the person to be able to read that tile (and, with the
  `partitionConsent` policy on, their consent, unless the bus is shared):
  403 otherwise.

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
next tick catch up. In a tile deployment that isn't the primary a job ticks
that deployment (§Tile deployments below); while a tile manager has its
deliveries off it is dormant — registered (`"dormant": true`), never
ticking. In a partitioned tile ([partitions.md](/docs/partitions.md)) each
person's partition schedules its own jobs — at most 16, none more often than
once a minute — which tick that partition while its person can use the
tile; a tick whose start is deferred (too many people's instances starting)
is tried again before the next one is due.

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
db, _ := sql.Open("sqlite", filepath.Join(dir, "app.db")+"?_pragma=journal_mode(WAL)")
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
db, _ := sql.Open("sqlite", xbin.Resource("db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
```

`modernc.org/sqlite` reads only `_pragma=name(value)` (repeatable) and
`_txlock=` from the DSN; mattn-style keys like `_busy_timeout=5000` are
silently ignored, so a "busy" write fails at once instead of waiting.

**Cross-scope direct filesystem/sqlite is deliberately not a thing.** The path is
only handed to same-scope components; other apps go through your service API.
Sharing files across app boundaries welds schemas together — the whole point of
the roles/API model is to avoid that.

## Tile deployments: data per deployment

A tile can run several deployments of its code
([tile-deployments.md](/docs/tile-deployments.md)). The primary keeps the
data this page describes; each other deployment (`dev`, …) has data of its
own, so testing against it never touches what everyone else sees.

- **Its own data in the tile's scope.** Each deployment beyond `main` keeps
  its own kv, blob, bus and filesystem and sqlite volumes in its tile's scope.
  The scope's tiles that each have a deployment of the same name share that
  data, as the scope's tiles share the primary's. Resource ids and
  `XBIN_RES_*` values are the same in every deployment — a file resource's
  path is bound to the deployment's own volume — so code that stores absolute
  paths keeps working, and nothing in your code changes. kv, blob and bus
  calls resolve in the caller's deployment's data.
- **Declared by its own code.** A deployment's resources are what its own
  code's `scope.json` declares: a pinned deployment's checkpoint, the live
  reload target's work tree. A resource the primary's code doesn't declare
  exists only in the deployments that do. Beyond `main`, a deployment's data
  holds at most 64 resources, and its volumes mount on first use (a start, a
  blob request).
- **Other scopes read-only.** Other scopes' data, and the workspace's, are
  read-only to non-primary deployments: a write answers 403 `<tile>+<name>
  may not write <res>: non-primary deployments reach other scopes read-only
  (edge policy "read")`.
- **Registrations are the deployment's own.** A deployment beyond `main`
  registers cron jobs and bus push subscriptions into its own store, never
  `main`'s, and they fire for it: a tick or delivery always reaches the
  deployment that registered it, never the primary. A tile manager can
  switch a non-primary deployment's deliveries off (`main` beside another
  primary included; on by default): its registrations are then dormant
  (registering still answers 200, with `"dormant": true`) — a dormant job
  doesn't tick and a dormant subscription receives nothing — until they are
  switched on again or it becomes the primary. A tile's credential lists its own
  deployment's jobs and subscriptions: rows carry `deployment` (absent for
  `main`) and `dormant` (absent while active), and bus rows count
  `dormantEvents`. Admins pass `?deployment=<name>` to list, register or
  delete another deployment's, and the answer echoes it. A registration on
  another scope's resource is refused while its edge is `block`, and stored
  under `read` (a subscription then reads that scope's primary's bus, its
  edge re-checked at every delivery). Each deployment may hold 64 push
  subscriptions. `bx
  deployment run-now` delivers one job of a non-primary deployment once, as
  `xbin/cron` with the job's role, whatever its deliveries switch says (one
  run of a job at a time).
- **Where it comes from.** A new deployment's data starts empty, unless a
  tile manager seeds it from the primary's: kv consistent at one moment,
  each SQLite database at one point in time, files and blobs file by file,
  or everything at one point in time with the primary stopped (required for
  a container store and for a VM primary with file resources). A seed copies
  the resources both deployments' code declare with the same type; those
  only the new code declares start empty, the rest are skipped and listed.
  Special files (FIFOs, sockets) aren't copied, and neither are the vault,
  cron jobs, bus subscriptions or registrations. An admin can back a
  deployment's data up, now or on a schedule of its own, and restore an
  archive into it, replacing what it held (`/api/xbin/deployments/backup`,
  `restore`: [protocol.md](/docs/protocol.md) §Tile deployments).
- **Reset** empties it: terminal level on every tile of the scope that has
  that deployment (the refusal names the tile that blocks), `main`'s only by
  a tile manager while `main` isn't the primary, never data some tile serves
  as its primary, never `res:workspace/*`.
- **While a seed, reset or restore runs**, that data's kv, blob and bus
  requests answer 503 with `Retry-After`, the deployments using it don't
  start, and another such act answers 409. One cut short leaves the data
  partial, and its deployments refuse to start (`<act> of <scope> for <name>
  failed at <step>: reset or seed again`) until a reset or a new seed. The
  state shows as `original` (`main`, untouched), `empty`, `seeded`,
  `restored` or `partial`.
- **Removal.** Removing a deployment deletes its data only with the scope's
  last tile that has that deployment. Data no tile claims any more (a
  `scope.json` that went away, a deployment record gone) is kept 14 days,
  listed to admins, then deleted; `main`'s data is never collected.
- **Disk.** Each deployment's data beyond `main` is a quota bucket of its
  own, at the scope quota or a lower per-deployment `diskGiB` limit (the
  lowest among the tiles sharing it), which a tile manager sets on the
  scope's root tile. When the workspace disk is low, non-primary data is
  write-blocked first (507).
- **On disk** it sits under `data/resources-enc/.deployments/<scope>/<name>/`
  (its `kv.db`, and `fs/<resource>/` per volume), apart from the primary's;
  a scope whose data key is longer than 200 bytes can't have deployments
  beyond `main`.

## Partitioned tiles: `shared`

A tile whose `xbin.json` asks for `"partition"` ([elements.md](/docs/elements.md)
§Manifest) keeps its scope's data per partition. A resource of that scope
opts out with `"shared"`, keeping one copy:

```jsonc
{ "resources": {
    "db":   { "type": "sqlite" },                     // per partition (the default)
    "conf": { "type": "kv",     "shared": "read" },   // the global instance writes, people's partitions read
    "team": { "type": "sqlite", "shared": true } } }  // every partition reads and writes one copy
```

`shared` is ignored in a scope no tile partitions, so a template can ship it
ready, and on a `cron` resource. In a partitioned scope any other value, and
`"read"` on a `sqlite` resource (its readers write its journal), make the
tile's partition request invalid: its backend doesn't run, and the manifest
error names the resource.

What each partition sees:

- **The same names and paths.** `XBIN_RES_<NAME>` and `res:` ids are the
  same in every partition, so the code doesn't change: xbind binds each
  partition's own volume at the canonical path, and a shared one's single
  copy there. A `"read"` directory is mounted read-only in people's
  partitions; a `"read"` kv, blob or bus answers their writes 403
  `res:<scope>/<name> is read-only for people's partitions`.
- **Its own disk ceiling.** A person's partition is measured on its own
  (by default at the tile's per-namespace quota): when it is full, that
  person's kv and blob writes answer 507 — nobody else's.
- **On disk** a person's partition sits under
  `data/resources-enc/.partitions/<scope>/<deployment>/<partition id>/`
  (its `kv.db`, `fs/<resource>/` per volume and `ns.json`, which names
  the person), beside the tile's own data, which keeps today's keys and is
  the global instance's. Its volumes mount on first use and unmount once
  nobody has used them for an hour while none of that person's instances
  runs (and, like every volume, when the vault is sealed).
- **Other tiles' access** is today's grants: an unpartitioned tile, or the
  global instance of a partitioned one, reaches the tile's own (global's)
  data; a person's partition of another partitioned tile reaches that same
  person's partition here, and only when they can read this tile (and, if
  the workspace's `partitionConsent` policy is on, have allowed it).

A partition's data is deleted 30 days after its tile is removed (unless the
tile comes back) or its person's id is given to someone new, and at once
when a tile manager switches the tile's partition mode
([partitions.md](/docs/partitions.md)). Deleting a deleted person's
partitions 30 days later is not available yet: until then their data stays
on disk.

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
