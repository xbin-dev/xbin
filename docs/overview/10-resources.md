# Resources, vault & data

Components in xbin do not share raw files, connection strings, or secret
paths with each other. Durable state is **declared** (in `scope.json` or the
workspace manifest), **granted** (in the same RBAC grammar as element APIs),
and **delivered** by the broker — as an env-injected path for same-scope
file-backed state, or over the authenticated HTTP API for everything else.
That one discipline is what makes tiles portable (backup/offload know exactly
what a tile's state is), governable (every access crosses a grant check), and
encryptable at rest (the broker owns every byte's path to disk).

**Related:** [08-sandbox.md](08-sandbox.md) (how paths are bound in) ·
[06-authorization.md](06-authorization.md) (the grant grammar) ·
[14-lifecycle.md](14-lifecycle.md) (what backup captures) ·
reference: [/docs/resources.md](/docs/resources.md),
[/docs/auth.md](/docs/auth.md) · design records in the xbin repo: `auth` §4–5,
`vault-data`.

## Why brokered state

A backend's own filesystem is a throwaway overlay — anything written outside
a resource is gone at the next restart and invisible to backups
([08-sandbox.md](08-sandbox.md)). That is deliberate. The only durable state
a component has is what it *declared*, which buys three properties at once:

- **Portability.** "This tile's data" is a well-defined set: its scope's
  declared resources. Backup, offload, restore, and clone operate on that
  set mechanically ([14-lifecycle.md](14-lifecycle.md), LC-2).
- **Enforcement.** Every access — an env path materialized at spawn, or an
  API call — passes the same `grantedRole` check as calling another
  element's API. Cross-scope data access is an owner decision, not an
  ambient filesystem fact.
- **Encryption hooks.** Because the broker mediates every path to disk,
  resource data is encrypted at rest *unconditionally* (VD-1) — there is no
  plaintext resource path to forget about.

The corollary rule: **cross-scope `filesystem`/`sqlite` sharing does not
exist.** A raw path is only ever handed to same-scope components; another
app gets your *API* with a `reader` grant, never your files. Shared files
weld schemas together — the roles/API model exists to prevent exactly that.

## Declaring and requesting

Resources are declared where the state conceptually lives — a scope (an app)
or, rarely, the workspace:

```jsonc
// apps/thing/scope.json — this subtree is an app with these resources
{ "resources": {
    "store":  { "type": "filesystem" },
    "db":     { "type": "sqlite" },
    "events": { "type": "kv" },
    "files":  { "type": "blob" },
    "bus":    { "type": "bus" },
    "cron":   { "type": "cron" } } }

// workspace xbin.json "resources" → addressed as res:workspace/<name>
```

Components request access in `uses`, exactly like requesting another
element's API:

```jsonc
{ "uses": [
    { "target": "res:apps/thing/events", "role": "writer" },
    { "target": "res:apps/thing/bus",    "role": "reader" } ] }
```

**Identity & resolution.** A target `res:apps/thing/events` is resolved by
longest *declared-scope* prefix (`parseRes`): the deepest scope that is a
prefix of the path wins, the remainder is the resource name. Grants follow
the standard sources ([06-authorization.md](06-authorization.md)): a
same-scope `uses` declaration **is** the grant (ND5 — a scope is one trust
unit); cross-scope requests land in the pending queue for owner approval;
the org/workspace policy ceiling caps all of it (`res:` targets are subject
to `mayCall` rows, with same-scope always exempt). Resource APIs are a
runtime-plane surface: they serve element principals and admins — an
ordinary user session has no business on them and is refused.

## The types

| Type | What it is | Delivered as | Roles |
|---|---|---|---|
| `filesystem` | a persistent rw directory | env: directory path, bound rw into the sandbox (same-scope only) | reader/writer |
| `sqlite` | `filesystem` pointed at one db file | env: `.sqlite` file path (same-scope only) | reader/writer |
| `kv` | namespaced key→value store (shared bbolt db) | API `/api/xbin/kv/…` (1 MiB/value) | reader/writer |
| `blob` | a file tree behind the broker | API `/api/xbin/blob/…` (256 MiB/write) | reader/writer |
| `bus` | in-memory pub/sub topics | API publish + `/ws/events` delivery | reader/writer (aliases `subscriber`/`publisher`) |
| `cron` | scheduled calls to your own endpoints | API `PUT /api/xbin/cron/jobs` | writer |

Where the bytes actually live (`<scope-key>` is the scope path with `/`→`~`,
`workspace` for workspace-level; `<comp-key>` is a hashed component key):

| State | On disk |
|---|---|
| file-backed ciphertext (`filesystem`/`sqlite`/`blob`) | `data/resources-enc/<scope-key>/<name>/` |
| their decrypted views (runtime only) | `.xbin/resenc/<scope-key>/<name>/` (gocryptfs mounts) |
| kv (values encrypted per bucket) | `data/kv.db` |
| cron job registry | `data/cron-jobs.json` |
| per-element vaults | `data/vault/<comp-key>.json` |
| barrier keyfile (wrapped key only) | `data/vault/.barrier.json` |
| per-user UI prefs | `data/prefs/<user-key>/<comp-key>.json` |
| a tile deployment beyond `main`: its kv and file-backed data | `data/resources-enc/.deployments/<scope-key>/<name>/` (`kv.db`, `fs/<res>/`) |
| its decrypted views | `.xbin/resenc/.deployments/<scope-key>/<name>/fs/<res>/` |
| its cron jobs, bus subscriptions and other registrations | `data/deployments/<tile-key>/<name>/` |
| its vault | `data/vault/.deployments/<tile-key>/<name>.json` |
| its per-user prefs | `data/prefs/<user-key>/.deployments/<tile-key>/<name>.json` |

Everything under `data/` is xbind-owned, gitignored, and masked out of
terminals ([09-terminals.md](09-terminals.md)). `main`'s rows are the ones
above them, forever: giving a tile deployments migrates nothing (below).

## Delivery: env paths vs the API

At spawn, every granted resource yields an env var `XBIN_RES_<NAME>`
(uppercased). For same-scope `filesystem`/`sqlite` the value is a real
path — the *decrypted* gocryptfs mount — and the runner bind-mounts that
directory read-write into the sandbox (the one durable mount a backend
gets; [08-sandbox.md](08-sandbox.md)). For everything else the value is the
canonical `res:…` id you pass to the brokered APIs over the gateway socket.
Cross-scope file-backed grants inject nothing — by design.

Because env and binds are captured at spawn, **approving a `res:` grant
restarts the requesting backend** so the materialized access appears
immediately (same rule as `gpu:` and `cap:net-admin` grants).

SDK shape (any language works over plain HTTP with the instance token):

```go
dir := xbin.Resource("store")              // $XBIN_RES_STORE — a directory
kv  := xbin.KV(xbin.Resource("events"))    // brokered kv client
kv.PutJSON("2026-07-02/standup", ev)
```

## bus — reactivity, not a queue

`POST /api/xbin/bus/publish {resource, topic, data}` (writer/`publisher`
role) publishes onto the workspace events hub; the full topic is
`res:<scope>/<name>/<your-topic>`. Delivery is **in-memory, at-most-once**:
offline subscribers miss messages. Keep truth in kv/sqlite and treat bus
traffic as "something changed, go look".

Delivery is authorized per subscriber on the `/ws/events` stream: admins see
all bus events; an element principal (a tile frame or backend connection)
receives a bus event only if it holds `reader` on that bus resource
(`busFilter`); other principals get none. Backends don't hold that socket
(idle-reap would sever it); they register a **push subscription** instead
(D85): `PUT /api/xbin/bus/subscriptions {name, resource, prefix?, path,
role?}`, and xbind POSTs each matching event to that path as
`From: xbin/bus`, lazily starting the backend like a cron tick. The
subscriber needs `reader` on the bus — at registration and again at every
delivery. Delivery stays at-most-once: a bounded queue per subscription,
one POST in flight, no retries, and a 100/s loop guard. A frontend
`xbin.bus.on('res:apps/thing/bus/events/', …)` still drives live UI
refreshes. Bus topics are part of your contract: document them in
`API.md`.

## cron — scheduled self-calls

An element registers jobs that call **its own endpoints**; cron can never be
aimed at a third element (the API forces `component` to the caller;
admins may register jobs for any component):

```
PUT /api/xbin/cron/jobs
{"name":"sweep","resource":"res:apps/thing/cron",
 "schedule":"*/5 * * * *","path":"/sweep","role":"writer"}
```

Schedules are standard 5-field cron or `@every 30s`-style descriptors.
Ticks arrive as `POST <path>` carrying the reserved principal
`X-XBin-From: xbin/cron` with the role chosen at registration (default
`writer`) — since the target is always the registering element itself, the
role means whatever *your own* handlers make of it, so there is no
escalation surface. A tick on an idle backend wakes it (lazy spawn), so cron
composes with idle-reaping; a disabled or offloaded component's jobs pause
without being unregistered ([14-lifecycle.md](14-lifecycle.md)). Failures
are logged, never retried — make handlers idempotent and let the next tick
catch up.

## The vault — per-element secrets

Every element has a private key→value vault for third-party credentials
(docs/auth.md §Vault), so secrets stay out of source trees, env files, and
other elements' reach:

- **Value reads are self-only — absolutely.** `GET
  /api/xbin/vault/<comp>/<key>` succeeds only when the caller *is* that
  component. Since the 2026-07-08 lockdown
  ([/docs/changes/2026-07-08-vault-read-lockdown.md](/docs/changes/2026-07-08-vault-read-lockdown.md)),
  not even the owner or an `xbin:admin` tile can read another element's
  secret **value** through the API — admins can *list keys*, *set/rotate*,
  and *delete* (the password-manager function) without ever seeing values,
  so a compromised admin surface can't exfiltrate the workspace's secrets.
- **No cross-element sharing.** Two elements needing the same secret each
  store it, or the owning element exposes a role-guarded API that *uses*
  the secret without returning it.
- **Delivery is pull, not env.** Code fetches at runtime
  (`xbin.Secret("imap-pass")`, or the HTTP API); secrets are never injected
  into the environment, which leaks into `/proc`, logs, and children.
- Storage is one xbind-owned 0600 JSON file per component. With the barrier
  unsealed it is an encrypted envelope (`{"enc":1,"data":…}`, the whole map
  including key names); legacy plaintext files are re-encrypted on the
  barrier's first init. With **no** barrier configured, writes are
  **refused** unless the daemon explicitly runs insecure — production can
  never persist secrets in the clear by accident.

## The encryption barrier

`internal/vault` is the root of every at-rest key (docs/resources.md §Encryption at rest). It
mirrors the property that makes HashiCorp Vault's model meaningful: **the
master key never lives in the at-rest data.**

```
passphrase --Argon2id(salt)--> KEK   (memory only)
KEK  --AES-256-GCM wraps-->    DEK   (random master key; mlocked in memory)
DEK  --HKDF("xbin/<label>")--> per-use subkeys ("kv:<bucket>", "fs:<scope>/<res>")
```

Only the *wrapped* DEK and KDF parameters are on disk
(`data/vault/.barrier.json`); a stolen disk, backup, or snapshot yields
ciphertext. Sealing zeroes the DEK from memory; unsealing re-derives it from
the passphrase. `Rekey` re-wraps the DEK under a new passphrase — O(1), no
data re-encryption — and demands the *current* passphrase, so an admin
session alone can't silently rotate the credential. Boot behavior (first
match wins):

| Boot condition | Vault state |
|---|---|
| `XBIN_VAULT_PASSPHRASE` set | auto-init/unseal — hands-off production |
| `--insecure-vault` or `--no-auth` | plaintext at rest allowed (explicit opt-out) |
| `--dev` | auto-unseal with a fixed **dev-only** in-source key, so `make dev` exercises encryption |
| barrier exists, none of the above | starts **sealed** — an admin unseals after login |
| nothing configured | **locked** — secret writes refused until an admin unseals (which creates the barrier); the passphrase never touches env |

While **sealed**: encrypted vault reads/writes and kv reads/writes return
503; sealing also stops every component that depends on file-backed
resources and unmounts their decrypted views, leaving only ciphertext on
disk. Honest limit: Go gives no guaranteed memory zeroing — the barrier
defends data *at rest*, not a root-compromised live host while unsealed.

## Encryption at rest for resource data

Resource state is **always encrypted — there is no plaintext resource path**
(VD-1). The mechanism splits by delivery mode (docs/resources.md §Encryption at rest):

- **Broker-mediated** (`kv`, `blob` API bodies): encrypted by the broker —
  kv values are envelope-encrypted per bucket under a derived subkey
  (self-describing 1-byte tag, so dev-plaintext and encrypted values can
  coexist); blob trees ride the file mechanism below.
- **Directly bind-mounted** (`filesystem`, `sqlite`, blob storage): each
  resource is a **per-resource gocryptfs mount** — ciphertext at
  `data/resources-enc/…`, decrypted view mounted at `.xbin/resenc/…` and
  bound into the sandbox. The gocryptfs password is an HKDF subkey derived
  in memory from the DEK; **the key never enters the sandbox** (VD-2) — the
  backend just sees a normal rw directory.

If encryption *cannot* run — vault sealed, no barrier, or no gocryptfs
binary — the resource is **unavailable, never silently plaintext**: kv/blob
calls return 503, and a component that `uses` an affected resource is
**held** (`EncryptionHold` composes into the runner's spawn gate alongside
lifecycle state, so no spawn path can start it). Everything resumes on
unseal; stale mounts from a crashed daemon are lazily recovered at boot.
Per-label subkeys mean a leaked per-resource key never crosses resources.
Note the deliberate trade (VD-4): **backups stream decrypted data** — archive
encryption is the archiver tile's job ([14-lifecycle.md](14-lifecycle.md)).

## Disk governance

A clumsy tile must not fill the shared data partition
([/docs/isolation.md](/docs/isolation.md) §disk). A background monitor
rescans each scope's resource footprint and the partition every ~45s:

- **Per-scope quota** (`XBIN_LIMIT_DISK`, default 50 GiB): over it, the
  scope's brokered *writes* (kv/blob) are refused with `507`.
- **Low-disk reserve** (10% of the partition): under pressure, the biggest
  consumers over a fair share are write-blocked to hold the reserve.
- Directly-mounted resources (`filesystem`/`sqlite`) can't be blocked at an
  API they don't pass through — they count toward usage and raise alerts;
  intervention is the admin's call.
- **Alerts** (over-quota, low-disk, active blocking, plus cgroup OOM/pids
  events) surface via `GET /api/xbin/alerts` — system-wide ones to
  everyone, tile-scoped ones to admins and that tile's users — rendered as
  shell banners and in the admin console. `GET /api/xbin/tile-status` (and
  `bx status`) reports one tile's usage/quota/blocked state; the admin
  runtime view lists every resource with its footprint.

## Tile deployments: data per deployment

A tile deployment ([/docs/tile-deployments.md](/docs/tile-deployments.md))
has its own data, keyed by **(scope, deployment name)**:

- **`main` keeps today's keys and paths**, whichever deployment is the
  primary. Every other deployment gets a namespace no existing key can
  produce (the `.deployments` rows above), which starts **empty**. Resource
  ids and `XBIN_RES_*` values are the same in every deployment: the broker
  picks the namespace from the caller's credential, and the runner binds the
  deployment's own volumes at the paths `main`'s use.
- **Each deployment provisions what its own code declares** — its
  checkpoint's `scope.json`, or the work tree's for the live reload target —
  at most 64 resources beyond `main`; volumes beyond `main` mount on first
  use. A resource the primary's code doesn't declare exists only in the
  deployments that do.
- **Scopes share namespaces.** Same-named deployments of a scope's tiles
  share its namespace for that name, as `main`s share today's; a tile
  without that deployment uses the primary's. Other scopes' data and
  `res:workspace/*` are read-only to non-primary deployments (`<tile>+<name>
  may not write <res>: non-primary deployments reach other scopes read-only
  (edge policy "read")`).
- **Seed, reset, restore** replace or empty a non-primary namespace: seeding
  from the primary is a tile manager's act, resetting is terminal level
  (`main`'s, while it isn't the primary, a tile manager's), and each needs
  its level on every tile of the scope that has the deployment. While one
  runs the namespace's kv, blob and bus requests answer `503` with
  `Retry-After`; one cut short leaves it partial, and its deployments refuse
  to start until a reset or a new seed. A namespace is deleted with the
  scope's last tile that has that deployment; one no tile claims any more is
  kept 14 days, listed to admins, then deleted.
- **Registrations** of a non-primary deployment — cron jobs, bus push
  subscriptions, interface instances, ingress hosts — go to its own files,
  never `main`'s stores, and are dormant: registering answers success (with
  `dormant: true`), and nothing fires until a tile manager turns its
  deliveries on (cron and bus only) or it becomes the primary. A tick or a
  delivery reaches the deployment that registered it; a subscription on the
  tile's own bus hears only its namespace's events. Bus events published in a
  namespace beyond `main` carry `deployment`, and reach admins and that
  namespace's own principals only.
- **The vault is per deployment**: a new one holds the primary's key names
  as placeholders, values are copied in by a tile manager (vault copy), and
  a backend reads only its own deployment's values.
- **Disk.** Each namespace beyond `main` is its own quota bucket, at the
  scope's quota or a lower per-deployment limit, and non-primary data is
  write-blocked first under low disk.

## Prefs — tiny, non-secret UI state

`/api/xbin/prefs` is a small per-**(user × component)** JSON store (the
shell keeps its screen layout there; any tile can keep per-user UI state the
same way). The bucket is keyed by the *verified* principal on both axes, so
there is nothing to spoof and no cross-user or cross-tile access. A tile
deployment beyond `main` keeps a bucket of its own. Not for
secrets (that's the vault) and not a resource — no declaration or grant
needed.

## What resources do *not* cover

Component **source** is git, not a resource ([02-workspace.md](02-workspace.md));
**terminal layers** and **env layers** are rebuildable sandbox state
([09-terminals.md](09-terminals.md), [08-sandbox.md](08-sandbox.md));
**vault contents are excluded from backups by default**. What a backup
actually captures — declared resource data + source + terminal env (LC-2) —
and how offload/restore use it, is [14-lifecycle.md](14-lifecycle.md)'s
story. The one-line rule for builders: if state matters, declare it; if
it's a credential, vault it; everything else is disposable.
