# Component lifecycle + backup / archive

Two related capabilities:

1. **Lifecycle** — a component is no longer always-on. It has a state the owner
   controls: `enabled` (today's behaviour), `disabled` (stopped, data kept), and
   `offloaded` (data archived off-box to free disk, restorable on demand).
2. **Backup / archive** — a pluggable, per-component archive mechanism: buxond
   streams a component's state as a **tar** to an **archiver tile** (an interface
   provider; the builtin one targets S3). The same mechanism powers manual
   backups, scheduled backups (cron), and `offloaded`. Reads expose **restore a
   version** and **restore a single file**.

Grounding / prior art: `plans/deployment.md §Backup & restore` already says the
workspace `data/` dir is the runtime-state backup unit, **source lives in git**,
sqlite is checkpointed, and **vault is excluded by default**. This extends that
from a monolithic `bx backup` to *per-component* archiving through a *pluggable*
provider, and adds the lifecycle states on top.

## Lifecycle

State lives in the workspace manifest (machine-managed, like grants/bindings):
`WorkspaceManifest.Lifecycle[component] = "disabled" | "offloaded" | "offloaded-full"`.
Absent = **enabled** (so nothing changes for existing workspaces).

| state | backend runs | served | resource data | source + term-env | frees |
|-------|:---:|:---:|:---:|:---:|-------|
| `enabled` (default) | ✓ | ✓ | local | local | — |
| `disabled` | ✗ | placeholder | local | local | compute |
| `offloaded` | ✗ | placeholder | **archived + removed** | local | disk (data) |
| `offloaded-full` | ✗ | placeholder | **archived + removed** | **archived + removed** | disk (all) |

- **Two offload depths** (owner's decision): `offloaded` frees the big resource
  data but keeps the (small) source + terminal dev layer, so the tile still lists
  and renders an "offloaded — restore to use" placeholder. `offloaded-full` also
  archives + removes the source subtree and the terminal env layer, leaving only
  a manifest stub — maximum reclamation, restored source-and-all.
- **Gating**: `runner.Ensure` refuses to build/spawn a non-`enabled` component;
  the proxy serves a lifecycle placeholder (HTTP 409-ish JSON / a small HTML card
  for frame loads) instead of 502; rescan still *lists* the component (and a
  stub for `offloaded-full`) so the owner can re-enable/restore.
- **Transitions** (all owner/admin only):
  - enable ⇄ disable: just flip state + stop/allow spawn. No data movement.
  - enabled/disabled → offloaded[-full]: **run a backup**, then remove the
    in-scope local bytes, then set state. (Never remove before the archive PUT
    is confirmed.)
  - offloaded[-full] → enabled: **restore** the latest version (data, +source for
    -full), rebuild the env layer from `setup`, then spawn.

## Backup / archive

### Scope — what goes in the tar (owner's decision: *data + source*)

Per component `<c>` rooted at scope `<s>`:

- **include**
  - source subtree `src/…` — the component's files (code, `buxon.json`, …).
  - resource data — the scope's `data/resources/<scope-key>/` (kv `kv.db`,
    `*.sqlite` **checkpointed** via `VACUUM INTO` for a consistent snapshot,
    blob dirs).
  - terminal dev layer `.buxon/term/<comp-key>/` — hand-installed, *not*
    reproducible, so it travels with source (owner's call).
- **exclude**
  - component env layer `.buxon/env/…` — reproducible from `setup`, rebuilt on
    restore (never archived).
  - logs, runtime sockets (`.buxon/log`, `.buxon/run`), in-memory bus.
  - **vault** (`data/vault/<comp-key>.json`) — excluded by default (consistent
    with the existing decision); `--with-vault` opts in and archives only the
    already-encrypted envelope (dead weight without the same barrier key).

The tar carries a small `backup.json` header (component, scope, buxon version,
timestamp, what's included, sqlite checkpoint list) so restore is unambiguous.

### The archive interface — `kind:"archive"`

A new interface family. The **archiver tile** `provides: {store: {kind:"archive"}}`
and implements an HTTP contract on its own component API; **buxond is the client**
(it owns the data + drives it on the owner's behalf — the component being backed
up declares nothing):

```
PUT    /archive/<key>                     body: tar stream  → {version, size}
GET    /archive/<key>/versions            → [{version, time, size, note}]
GET    /archive/<key>/versions/<v>        → tar stream            (restore version)
GET    /archive/<key>/versions/<v>/file?path=…  → one file        (restore file)
DELETE /archive/<key>/versions/<v>        → prune                 (retention)
```

`<key>` = the component key. The archiver may hash/dedupe/compress or store the
tar as-is — buxond doesn't care. `restore file` lets the archiver stream a single
member (it can extract from its stored tar, or from a dedup/CDC store).

**Binding**: owner-driven, no component declaration. A **workspace default
archiver** plus an optional **per-component override**, stored in the bindings
table under a reserved slot (`@archive`): `bindings["*"]["@archive"]` = default,
`bindings["<comp>"]["@archive"]` = override. Unbound = no backup (loud error on a
backup attempt). This mirrors net/http binding UX — the archiver is a privileged
tap (sees a component's whole state), so binding is the explicit owner decision.

### S3 archiver tile (builtin)

`builtin-tiles/s3-archiver`: provides `{kind:"archive"}`; config (endpoint,
region, bucket, prefix) on its own frontend; **credentials in its vault**. Stores
`<prefix>/<key>/<version>.tar` (version = RFC3339 + short hash); `versions` = a
prefix listing; `restore file` streams the object and extracts the member on the
fly. Start with plain PUT/GET (no dedupe); CDC/dedupe is a later drop-in that
doesn't change the interface. Uses the standard S3 REST API (SigV4) so it works
with AWS, MinIO, R2, B2, etc.

### Restore

- **version**: fetch the tar → stop the component → replace its in-scope data
  (+source for `-full`) atomically (stage to a temp dir, checkpoint-swap) →
  rebuild env → enable. Guarded by a version-compat check (buxon version in the
  header, like migrations).
- **file**: fetch one member from a version — download it, or write it into place
  (recover a clobbered sqlite / config without a full rollback). Admin-only.

### Cron

Backups reuse the existing cron engine (`internal/broker/cron.go`, robfig/cron)
but as **owner-scheduled jobs invoking a buxond backup action** rather than an
element endpoint. A `data/backups.json` holds `{component, schedule, retention}`;
on tick buxond runs the backup and prunes to `retention` versions. `bx backup`
gains the per-component/scheduled forms; the monolithic workspace `bx backup`
(deployment.md) stays for whole-workspace cold copies.

## Security

- An archiver is a **privileged tap** (sees a component's whole tar) — owner
  binding is the loud, explicit authorization, exactly like a `net` provider.
- Vault excluded by default; `--with-vault` archives only the encrypted envelope.
- Restore/restore-file and all lifecycle transitions are **admin-only**;
  buxond strips inbound `X-Buxon-*`; the archiver never gets element principals.
- Restore validates the backup header's buxon version (≤ one minor newer, like
  the migration guard) before overwriting live data.

## UX

- **Admin tile**: a per-component lifecycle control (enable / disable / offload /
  offload-full / restore) + a backup panel (back up now, version list with
  size/time, restore-version, restore-file, retention/schedule) + the archiver
  binding (default + overrides), rendered in the interfaces area (`archive`
  family).
- **bx**: `bx enable|disable <c>`, `bx offload <c> [--full]`, `bx restore <c>
  [--version v] [--file path]`, `bx backup <c>`, `bx backups <c>` (list),
  `bx archiver set [--component c] <provider>`.

## Decisions (LC-*)

- **LC-1** — lifecycle state (`disabled`/`offloaded`/`offloaded-full`) lives in
  `WorkspaceManifest.Lifecycle`; absent = enabled; owner-only; gated at
  `runner.Ensure` + proxy; component still listed (stub for `-full`).
- **LC-2** — backup scope is **data + source + terminal-env**; env layer
  excluded (rebuilt), vault excluded by default. sqlite checkpointed.
- **LC-3** — `archive` is an interface **kind**; the archiver *provides* an
  HTTP tar-in / versions-and-file-out contract; **buxond is the client**; owner
  binds an archiver (workspace default + per-component override), no component
  declaration.
- **LC-4** — offload/restore/scheduled-backup/manual-backup all use the one
  archive path; two offload depths (data, or data+source+term-env).
- **LC-5** — backups schedule on the existing cron engine as owner jobs;
  retention prunes versions.

## Phasing

1. **Lifecycle states + enable/disable** *(done)* — state model, gating, API,
   `bx`, admin toggle.
2. **Backup core** *(done)* — `internal/backup` self-describing tar (Manifest +
   Writer/Reader), broker build/restore, `@archive` bindings, `restore`
   (version/file). sqlite snapshot copies the file + WAL sidecars (no checkpoint
   driver yet — consistent for offload since the component is stopped first;
   VACUUM-INTO hardening is a later drop-in).
3. **S3 archiver tile** *(done)* — `builtin-tiles/s3-archiver`, dependency-free
   SigV4 (unit-tested against AWS's vector), path-style, config UI + vault creds.
4. **Offload / restore** *(done)* — both depths, wired into `POST /lifecycle`.
5. **Cron scheduling + retention** *(done)* — owner-scheduled backups on the cron
   engine + version pruning. Still to do: the admin backup *panel* (list versions
   / restore / schedule in the UI — API is all there), and the `archive` family
   entry in the interfaces UX.

## Touchpoints

`internal/registry` (Lifecycle on the workspace; stub for offloaded-full) ·
`internal/runner` (Ensure gating; env rebuild on restore) · `internal/proxy`
(lifecycle placeholder) · `internal/broker` (backup/restore orchestration, tar
builder, archive bindings, cron backup jobs) · `internal/server` (lifecycle +
backup API) · `cmd/bx` · `web/`+admin (`archive` family UX) ·
`builtin-tiles/s3-archiver`.
