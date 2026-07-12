# Tile lifecycle: backup, sharing & updates

A tile is not always-on and not forever-local: it has an owner-controlled
runtime **state** (running, stopped, or archived off-box), a **backup unit**
(one self-describing tar), and a **provenance story** (where its code came
from, and how newer upstream code reaches it without trampling your edits).
This chapter covers all three, because they compose into one system: the same
archive path powers manual backups, scheduled backups, offload, and restore
(LC-4); the same create authority gates every way code enters the workspace;
and per-component git repos are the substrate that makes all of it diffable
and reversible.

**Related:** [03-components.md](03-components.md) (what a component is) ·
[06-authorization.md](06-authorization.md) (the create/read clamps imports
run through) · [10-resources.md](10-resources.md) (resource data & the vault
barrier this interacts with) · [11-interfaces.md](11-interfaces.md) (the
binding model `@archive` reuses) · [09-terminals.md](09-terminals.md) (the
terminal dev layer that travels in backups) — reference:
[/docs/protocol.md](/docs/protocol.md), plans/lifecycle.md,
plans/tile-sharing.md, plans/builtin-updates.md.

## Runtime lifecycle states (LC-1)

Every component has a lifecycle state stored in the workspace manifest
(`WorkspaceManifest.Lifecycle`, machine-managed like grants and bindings).
**Absent means `enabled`** — existing workspaces needed no migration, and an
enabled component carries no entry at all.

| state | backend runs | resource data | source + term layer | frees |
|---|:---:|---|---|---|
| `enabled` (default) | ✓ | local | local | — |
| `disabled` | ✗ | local | local | compute |
| `offloaded` | ✗ | **archived + removed** | local | disk (data) |
| `offloaded-full` | ✗ | **archived + removed** | **archived + removed** (a manifest stub stays) | disk (nearly all) |

The state is enforced at every path that could start a backend, not just the
friendly one:

- **The proxy** answers requests to a non-enabled component with **409** and
  an `X-XBin-Lifecycle` header naming the state, so frames can render an
  "enable/restore to use" placeholder instead of a 502.
- **`runner.Ensure` refuses to spawn** a non-enabled component. This is the
  authoritative gate: the watcher's rebuild-on-save, a grant-change respawn,
  an inbound ingress connection — no path can resurrect a disabled backend,
  because they all go through `Ensure`.
- **Disabling stops a running backend now** (not at next idle-reap) — the
  point of disabling is to free compute immediately.
- **Ingress drops non-enabled tiles**: published HTTP routes stop resolving
  and bound host-port stream listeners are reconciled away
  ([13-ingress.md](13-ingress.md)), so an offloaded tile is not publicly
  reachable either.
- The grants panel skips offloaded components' `uses` (they make no live
  requests), and a component held by encryption (below) is treated like
  disabled at spawn.

Transitions are **admin-only** (`POST /api/xbin/lifecycle {component,
state}`, `bx enable|disable|offload|restore`). Enable⇄disable is a pure
state flip; the heavy transitions run their work *before* the state flips,
so a failure leaves the component untouched.

### The offload safety gate — `lifecycleAt`

Alongside the state, `WorkspaceManifest.LifecycleAt` records **when the
state last changed** (RFC3339). Its job is consistency: the admin tile
deliberately makes offload a two-step flow — **disable first** (stops the
backend, so the database is quiescent), **then back up**, and only a backup
whose timestamp is **newer than the disable timestamp** un-gates the offload
buttons. You can never free local data on the strength of a snapshot taken
while the backend was still writing. (The API itself will offload on demand
— `offload()` stops the backend and takes a fresh archive inline; the UI
gate is the guided, verifiable version of the same discipline.)

## The backup unit (LC-2)

A backup is **one tar per component**, built by xbind and streamed out —
never buffered to workspace disk. Its first entry is `backup.json`, a
manifest that fully describes the component (path, scope, what's included,
xbind version, creation time, its cron jobs), so restore needs **no local
metadata** — the archive alone is enough, which is exactly what disaster
recovery wants.

| in the tar | why |
|---|---|
| `source/…` — the component's files, **including `.git`** | history and remotes travel with the code; a restore is a full, re-pullable clone (skips `node_modules`, `.xbin` — reproducible/runtime) |
| `data/…` — the scope's resources, **only when the component roots its scope** | kv as `data/kv.json` (values base64), sqlite/filesystem/blob as whole directory trees |
| `term/…` — the terminal dev layer | hand-installed (`apt` in the shell), *not* reproducible, so it travels ([09-terminals.md](09-terminals.md)) |
| cron jobs (in the manifest) | re-registered on restore |

| deliberately excluded | why |
|---|---|
| the `setup` env layer | reproducible — rebuilt from the manifest's `setup` script on first spawn after restore |
| logs, run sockets, in-memory bus | runtime noise |
| **the vault** | secrets don't travel by default (docs/auth.md); the tar format reserves a `withVault` manifest flag, but no backup path writes vault content today — treat vault export as a separate, deliberate act |

A component that *doesn't* root a scope backs up source + terminal layer
only; its data belongs to the scope root's backup (the manifest records
which ancestor scope that is).

Two honesty notes. **Backups are plaintext tars**: xbind reads resources
through the decrypted view and re-encrypts on restore, so backing up (and
restoring) **requires the vault to be unsealed** when encrypted resources
are involved — encryption-at-rest is the *archiver's* responsibility, not
the tar's (plans/vault-data.md). And **sqlite is copied as files** (the db
plus its `-wal`/`-shm` sidecars), not `VACUUM INTO`-checkpointed yet: the
copy is guaranteed-consistent when the backend is stopped — which is what
the disable-first offload gate above ensures — while a backup of a live,
mid-write component is best-effort (plans/lifecycle.md lists the checkpoint
driver as a drop-in hardening).

## The archiver is an interface (LC-3)

Where do the bytes go? Not into xbind. `archive` is an **interface kind**
([11-interfaces.md](11-interfaces.md)): an archiver **tile** declares
`provides: {store: {kind: "archive"}}` and implements a small HTTP contract
on its ordinary component API; **xbind is the client**, calling it
internally *as the owner* through the same proxy every element call uses
(the archiver spawns lazily like any backend, and never sees element
principals).

```
PUT    /archive/<key>                        tar stream in → {version, size}
GET    /archive/<key>/versions               → {versions: [{version, time, size}]}  (newest first)
GET    /archive/<key>/versions/<v>           tar stream out ("latest" accepted)
GET    /archive/<key>/versions/<v>/file?path=…   one member's bytes
DELETE /archive/<key>/versions/<v>           prune (retention)
```

`<key>` is the component's stable hashed key. What the archiver does behind
the contract — S3 objects, dedupe, compression — is its business.

**Binding is the authorization.** The component being backed up declares
nothing; the *owner* binds an archiver under the reserved pseudo-slot
`@archive` — a workspace default plus optional per-component overrides:

```
bx bind '*' @archive=apps/s3-archiver        # workspace default
bx bind apps/crm @archive=apps/other-arch    # per-component override
```

Unbound means no backups, loudly, on the first attempt. That's deliberate:
an archiver is a **privileged tap** — it receives a component's entire
plaintext state — so pointing one at your data is an explicit owner
decision, exactly like binding a `net` provider.

The worked example is the **s3-archiver builtin**: SigV4-signed, path-style,
dependency-free S3 client (works against AWS, MinIO, R2, B2), storing
`<prefix>/<key>/<version>.tar`. Its endpoint/region/bucket/prefix are
configured on its own page; **credentials live in its vault**, never in a
resource; and it declares a `net` interface the owner must bind
(`net=internet` for a public bucket, a `lan:` or provider tile for a LAN
MinIO) — under isolation it has zero egress until then, and its settings
page probes the bucket on every save so a missing binding surfaces
immediately.

## Restore

`POST /api/xbin/restore {component, version?, file?}` (`bx restore`) is
fully **archive-driven**: the tar's manifest says where everything goes.

- **Whole version** (version defaults to `latest`): stop the backend, unpack
  — source and terminal layer into place, file resources through freshly
  mounted encrypted views (re-encrypted under the *current* vault), kv
  re-encoded key by key, cron jobs re-registered — then mark the component
  enabled, rescan, and reprovision. Tar entry names are traversal-proofed
  (a hostile `../../` clamps inside the target), and a restore overwrites
  wholesale.
- **Single file** (`file` set): the archiver streams one member back and
  xbind hands you the bytes — a download for recovering a clobbered config
  or database *without* rolling the whole component back. It does not write
  into the live tree; putting the file back where you want it is your move.

Compatibility is guarded by the tar's schema number: an archive written by a
*newer* format refuses to restore ("upgrade to restore") rather than
half-applying.

## Offload — the same path, plus deletion

Offload composes what's above: **stop → back up → verify → remove**. Nothing
is deleted until the archiver has confirmed the PUT (`archive before offload
failed (nothing removed)` is a real error string, and the invariant it
states is the design). `offloaded` removes the scope's resource data (files
and kv buckets); `offloaded-full` also removes the terminal layer and the
source subtree — keeping just `xbin.json`/`scope.json` so the tile stays
listed, renders its "offloaded — restore to use" placeholder, and remains
restorable from the admin tile. Re-enabling an offloaded component *is* a
restore of the latest version (LC-4: one archive path for everything).

## Scheduled backups (LC-5)

Schedules are **owner jobs on the existing cron engine**, persisted in
`data/backup-schedule.json` — `{component, schedule, retention}` with
five-field cron or `@every 24h` syntax (`bx backup-schedule apps/crm --every
24h --keep 7`). Each tick runs the standard backup, then prunes the
archiver's version list down to the retention count (the list is
newest-first; everything past `keep` is deleted through the same contract).
Retention `0` keeps everything.

## Encryption interplay

Two rules connect this system to the vault barrier
([10-resources.md](10-resources.md)):

- **A tile whose encrypted state is inaccessible does not spawn.**
  `EncryptionHold` is composed into the runner's spawn gate alongside the
  lifecycle state: a sealed vault (kv values undecodable) or an unmounted
  encrypted file resource holds the backend rather than letting it run
  against missing or ciphertext data.
- **Backups and restores refuse while sealed** — the tar is plaintext by
  contract, so both directions need the decrypted view. Unseal first; the
  error says exactly that.

## Getting code in

Every road into the workspace converges on the same authority checks
([06-authorization.md](06-authorization.md)): the caller must be allowed to
**create at the target path** (admin, a user whose create patterns cover it,
or a tile holding `xbin:writer` — clamped to the attributed human's own
rights), the path must survive the reserved-segment rules (org `o/`
positions, reserved names), and the target must not nest with an existing
component. Whatever a new tile's `uses` demands lands as **pending grants**
for the owner to approve — imported code never arrives pre-authorized.

| road | mechanics |
|---|---|
| **Builtin tiles** (`bx tile import`, Tile Manager) | A curated catalog embedded in the xbind binary (`tile.json` metadata: title, default path, version, changelog) — trusted like the binary itself, but *not* auto-installed (plans/tile-sharing.md rung 1). Import copies the files; installing under a non-default path rewrites the tile's *own* authored path in its text files (self-references, its scope's `res:` ids) and sets a unique Go module path — a Go backend ships as `go.mod.tile` (restored to `go.mod` on import) because `go:embed` skips nested modules. Cross-tile references stay intact. Never overwrites an existing component. |
| **Git import** (`POST /git/import`, Tile Manager "from git") | Any https/ssh/scp-style remote (local paths, `file://`, and git's `ext::` transports are rejected; URLs are option-injection-guarded). The UI first inspects the remote (default branch + version-sorted tags) so you can pick a ref. The clone keeps its `origin`, so updating later is `git pull`. A repo that isn't a component (no `xbin.json`/`index.html`) — or whose `uses` reference resources/components that don't exist — is **removed again and rejected**, not half-installed. |
| **Clone** (`POST /clone`, `bx`/manager) | Fork an existing component: copies the directory *including `.git`* (the fork stays related to its source history), rewrites whole-word occurrences of the old path (so `apps/x` never corrupts `apps/x2`), and commits the rewrite. Requires **read on the source** — attributed through element principals, so a manager-style tile can't be driven into source exfiltration. Vault secrets and resource *data* are deliberately not copied: a fork is a new app. |
| **Templates** (`POST /templates/new`, `bx template new`) | A template is a component carrying a `template` block in its manifest — a blueprint that never runs. Instantiation copies it (builtin or workspace template) and **strips the block**, producing a normal, independent component. Builtin templates are additionally materialized as read-only git repos under `.xbin/template-repos/` and served over dumb HTTP; each instance gets that repo as a **`template` remote**, so a builder pulls upstream fixes with `git fetch template && git merge` — the fork-upstream model, with the builder in control (plans/agent-v2.md). |

## Keeping code fresh

Builtins are copied once and then **owned by you** — so updates can't be
blind overwrites. The updater (plans/builtin-updates.md) records
**provenance at install time**: an origin marker (`.xbin/builtins.json`)
plus a full **base snapshot** of the installed form (`.xbin/builtins/<id>/`)
for every imported tile and every scaffold component seeded at `xbind init`.
Content hashes decide whether an update exists at all (BU-1); when a newer
embed ships in an upgraded xbind, each file is classified by a three-way
comparison — base / yours / theirs:

| status | meaning | on apply |
|---|---|---|
| `clean` | upstream changed, you didn't | fast-forward |
| `user` | you changed, upstream didn't | kept |
| `new` / `removed` | added/removed upstream | added / removed (if unmodified) |
| `conflict` | both changed | **merge** leaves `git merge-file --diff3` conflict markers to resolve |

Apply modes: **replace** (take upstream wholesale, discarding local edits)
or **merge** (per-file three-way); **pin** mutes offers for a unit. A tile
that exists at a builtin's path *without* recorded provenance is "adopted" —
no trustworthy base, so any divergence is a conflict and merge is refused in
favor of replace-or-hand-diff. Template *instances* are deliberately never
tracked here: they're forks meant to diverge, served by the `template`
remote instead.

Underneath all of this sits the versioning substrate: **each component is
its own git repo** (created/migrated idempotently at startup and after
structural changes), with the workspace root a repo as well for layout-level
files (D2: auto-init, `.xbin/`/`data/` ignored, xbind never auto-commits —
committing is the agent's/human's act, CM-2). Per-component repos are what
make a component *installable, forkable, and updatable as a unit* — import
is a clone, a fork keeps history, backups carry `.git`, and template updates
are ordinary merges.

Finally, the platform's own upgrade contract is documentation, enforced as
process: every builder-visible xbind change lands a
[/docs/changelog.md](/docs/changelog.md) entry in the same commit, and
breaking changes add a migration note under `/docs/changes/` — the first
thing to read (or have an agent read) after upgrading the daemon.

## Surface summary

| action | API | CLI |
|---|---|---|
| set state | `POST /api/xbin/lifecycle {component, state}` | `bx enable\|disable <c>`, `bx offload <c> [--full]` |
| back up now | `POST /api/xbin/backup {component}` | `bx backup <c>` |
| list versions | `GET /api/xbin/backups?component=` | `bx backups <c>` |
| restore | `POST /api/xbin/restore {component, version?, file?}` | `bx restore <c> [--version v] [--file path]` |
| schedules | `GET/POST /api/xbin/backup-schedule`, `DELETE ?component=` | `bx backup-schedule <c> --every 24h [--keep N]` |
| archiver binding | `POST /api/xbin/bindings` (`@archive`) | `bx bind '*' @archive=<tile>` |
| catalog / import | `GET /api/xbin/builtins`, `POST /builtins/import` | `bx tile ls`, `bx tile import <name> [as <path>]` |
| updates | `GET /builtins/updates`, `POST /builtins/update {id, mode}` | `bx builtin updates`, `bx builtin update <id> [--replace\|--merge]` |
| templates | `GET /templates`, `POST /templates/new` | `bx template ls`, `bx template new <source> [as <path>]` |
| fork / git | `POST /clone`, `GET /git/remote-info`, `POST /git/import` | Tile Manager UI |

Everything above is admin-gated except catalog/template *listing* (any
authenticated principal) and the template git repos (readable by any
principal — they're embedded, identical-everywhere sources with no
secrets). All of it is driveable from the admin tile's **backup** tab and
the Tile Manager.
