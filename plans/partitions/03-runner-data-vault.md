# 03 — Runner, data namespaces, vault, registrations, identity records

Everything here mirrors the tile-deployments work (D119/D127), which already
runs several backend lines of one tile with their own data and vault. The
partition is a **second key beside the deployment**, never a deployment name
(user ids `^[a-z0-9][a-z0-9._-]{0,31}$` vs deployment names
`^[a-z][a-z0-9-]{0,23}$`; `global` is a legal deployment name — PD-01). In
v1 user partitions exist only on the tile's primary deployment (PD-17); keys
spell the deployment anyway.

## A. Runner

### Current behaviour

- State keyed `stateKey(tile, dep)` (`internal/runner/deployments.go:38`);
  `stateOf/existingStateOf/allStates` (`:47-83`).
- `EnsureDeployment` (`internal/runner/deploy.go:246`) → `ensurePrimary` or
  `ensureOther` (`deployments.go:126`), which refuses without `--isolate`
  (`:151`: "nothing binds its own data at its paths").
- `spawnSetup` (`deployments.go:226`): run dir `sockDir` (`deploy.go:202`),
  log `deploymentLog` (`internal/runner/states.go:230`), fresh token, env
  `XBIN_SOCKET/COMPONENT/GATEWAY/TOKEN[/DEPLOYMENT]` + `envFor` (the broker's
  `DeploymentEnv`) with a resource remap. Network wiring is the primary's
  alone when `c.Deployment == ""` (`internal/runner/sandboxcmd.go:132`):
  net-provider TUN rosters, lan-ingress legs, host network, a provider splice,
  the ingress relay (`:132-163`); non-primary deployments get relay egress
  only (D127o, `:146`).
- `admit` (`deployments.go:292`): caps 3/24/12 on non-primary deployments
  (`:28-32`), counting live states.
- Builds: `resolveGenFor` (`internal/runner/inspect.go:279`) — work tree:
  `buildGen(v)` each time (Go output at a fixed
  `.xbin/build/<CompKey>/bin`, `internal/runner/build.go:29`); checkpoints:
  one artifact per (tile, checkpoint), shared.
- Idle reap 30 min (`internal/runner/runner.go:45`); a live connection keeps a
  state active (`track`, `internal/runner/runner.go:281-297`; `idle`, `states.go:179-182`); alwaysOn per
  deployment (`internal/runner/alwayson.go`); `ChangedTile` restarts every
  live deployment (`deploy.go:307`); `Reassign` (`deployments.go:341`).

### The change

1. **State key.** `state` gains `part util.Partition` and `pkey string`.

   ```go
   func stateKey(tile, dep string) string                 // unchanged (global, and every unpartitioned tile)
   func partStateKey(tile, dep, pkey string) string {     // user partitions
       return stateKey(tile, dep) + "\x00p\x00" + pkey     // NUL never in a path; "p" separates from dep keys
   }
   ```
   The global partition *is* `stateKey(tile, dep)` (PD-04): no state moves
   when a tile turns partitions on.

2. **`EnsurePartition(ctx, c, dep, part, class) (sock string, err error)`**
   and `TrackPartition(tile, dep, part, passive)`:
   - `part == ""` or `global` → `EnsureDeployment` (today's path; `global`
     additionally requires the spec to declare it, else `ErrNoPartition` 404).
   - `user:*` → `ensurePartition`: refuses a non-primary `dep` (PD-17);
     refuses without `r.Isolate` ("a person's partition runs only in a sandbox
     (--isolate)": PD-19, the reason of `deployments.go:151`); asks the new
     hook `ShouldRunPartition(tile, dep, part)` (effective state
     `Partitioned`, lifecycle, encryption hold of the partition's namespace,
     the person live with the same uid — PD-20, PD-44); admission (§5); then
     `ensureState` on `partState(c.Path, dep, pkey)`.
   - Nothing creates a state for a partition that fails these checks.

3. **One build, many instances.** `resolveGenFor` gains a single-flight keyed
   `(tile, dep, code identity)`: the first partition to start after a change
   builds; the rest wait for and reuse that `bin` (for the work tree: until
   the next `Changed`, tracked by a build sequence number; checkpoints are
   already shared). Without this, N partition states would race `go build`
   into the same output path.

4. **`spawnSetup` for a user partition:**
   - run dir `sockDir(tile, dep, pkey)` = `"p-" + hex(SHA-256(tile‖0‖dep‖0‖
     pkey))[:16]` (fits the 108-byte socket limit; no CompKey or `d-` name
     collides);
   - log `.xbin/partition/<TileKey>/<dep>/<pkey>/backend.log` (a new
     `partitionLog`, beside `deploymentLog`);
   - env: today's, plus `XBIN_PARTITION=<key>` right after `XBIN_COMPONENT`
     (and after `XBIN_DEPLOYMENT` when set); the global instance of a
     partitioned tile gets `XBIN_PARTITION=global` (an unpartitioned tile gets
     nothing new — byte-identical env);
   - token registered through `RegisterInstancePartition` (02 §2);
   - env + remap from the broker's `PartitionEnv(c, dep, part)` (§B.4);
   - **network (S5): the non-primary rule.** `sandboxcmd.go:132` becomes
     `primary := c.Deployment == "" && !c.UserPartition()`, so a user
     partition never gets a net-provider roster, lan-ingress legs, the host
     network, a provider splice address or the ingress relay — only relay
     egress under the tile's policy (D127o's branch, `:146`). Alice's
     partition can't reach bob's loopback listeners or abstract sockets
     through a shared host namespace, and no splice return path is shared.
     (`cap:net-admin`/`cap:containers` can't be held by a partitioned tile at
     all, 01 §3.)

5. **Admission (PD-18, C4, C5, S20).**
   - **Caps** (user partitions only; global is exempt like a primary): a
     running cap per tile and per workspace, **admin-settable** through
     `POST /api/xbin/partitions/limits` (06 §6), with defaults derived from
     host memory — per tile `clamp(MemTotal/4 ÷ E, 4, 32)`, workspace
     `clamp(MemTotal/2 ÷ E, 8, 128)`, where `E` is the per-instance estimate
     (placeholder 160 MiB incl. two gocryptfs processes, replaced by the QA-box
     measurement of an agent partition's RSS before release, I2). **I2
     measured it (records/I2.md): E = 96 MiB** — an agent partition holds
     ~56 MiB resident (61 at most) with its two gocryptfs processes (~26 MiB
     of its own, PSS plus kernel); E keeps ~70% headroom for heavier use
     I2 didn't measure (64 MiB is what the measurement alone supports: a
     recommendation for the owner). A 4 GiB macOS VM
     (`deploy/install-macos.sh`) gets 10/21 (6/12 with the placeholder).
   - **Interactive vs background.** `class` comes from Route (02 §7): rule-1
     deliveries (cron, bus, mail) are background. A partition is *in use*
     while it has a non-passive tracked connection, a hold (the agent's
     `/engine/hold`), or an interactive request in the last 2 min.
   - **Eviction.** Past a cap, an interactive start stops the
     least-recently-used partition instance that isn't in use (passive SSE
     streams don't count — their frames reconnect only while visible, 08
     §11), of the same tile, then of the workspace; a background start may
     evict only a partition that is neither in use nor holding any passive
     stream. If none, the start is refused: interactive → `sbx.Refuse` 503
     `too many people's instances of <tile> are running; try again shortly`;
     background → deferred (§D: cron retries, mail re-rings later).
   - **Background cold starts** are further limited to 4 concurrent
     workspace-wide, and mail doorbell starts to 6 per minute per tile
     (04 §3).
   - D127q's caps are unchanged (user partitions never exist on non-primary
     deployments in v1, so `admit`'s count of states stays a count of
     deployments).
   - **Coverage for the partitioned-by-default agent (PD-35, decided).**
     Every new agent instance runs one process per active person, plus its
     global instance. For K instances used by N people, the load is bounded
     by these mechanisms, and none depends on people's discipline:
     - interactive starts ≤ the per-tile and workspace caps above, with LRU
       eviction of partitions not in use;
     - hidden tabs close their streams (08 §11), so a background tab doesn't
       pin a partition;
     - an idle partition stops after 10 min (§6), with the agent's
       `/engine/hold` as the only keep-alive while a run is busy;
     - user-mode resume never registers `@every 1m` for waiting runs
       (08 §9), and schedules and mail start partitions only as background
       starts (≤ 4 concurrent, mail ≤ 6/min per tile);
     - nothing starts at boot (§6);
     - global instances are one per instance, as today.

     I2 measures `E` on the QA box with the **default** agent (20 people,
     two instances) before release, and the caps' defaults are set from
     that. Workspaces without `--isolate` get unpartitioned instances (01
     §2.7), so they never pay this.

6. **Reaping.** User partitions reap after `partitionIdleReap = 10 min` of no
   non-passive use (PD-18; the agent's `/engine/hold` keeps a busy one alive,
   as today). `alwaysOn` applies to the global instance only; user partitions
   are never started at boot.

7. **Code changes.** `ChangedTile`/`ChangedDeployment` restart every *live*
   partition state of the primary, staggered (at most 4 blue/green swaps at
   once per tile) after the one shared build; states without a generation
   build (reuse) on their next request. `Reassign` restarts live partition
   states.

8. **Stopping.** `StopPartition(tile, dep, part)`,
   `StopPartitionsOf(userID)` (user delete/disable/access loss, 06 §9),
   `StopPartitions(tile)` (mode change, 01 §6). Stopping revokes the
   instance token first (02 §2) and never deletes data.

9. **Sandbox registry** (`internal/sbx/sbx.go`): `Entry` gains
   `Partition string` (metadata: admins see "apps/agent · alice's
   partition", never its content).

10. **`PartitionsChanged(c, old, new)`** for the transitions (01 §6).

## B. Data namespaces

### Current behaviour

- The one key function: `scopeKeys(scope, dep)`
  (`internal/broker/deploydata.go:380`) and `resKeys(rt, dep)` (`:425`);
  main's keys are today's, others live at `.deployments/<escS>/<dep>`
  (`escS` injective, `:350`); no scope key starts with ".", so no older
  binary reaches that level. `TestNoAdHocResourceKeys` keeps keys in one
  place.
- `reachRes`/`resNamespace` (`internal/broker/resources.go:626/648`):
  own-scope resources in the caller's deployment's namespace, other scopes in
  the scope primary's.
- `DeploymentEnv` (`internal/broker/resenc_wire.go:342`): same env values in
  every deployment; a remap binds the deployment's volume at the canonical
  path. `resourceBindsFor` (`internal/runner/binds.go:78`) fails closed and
  refuses any source in main's data (`mainData`, `:124`).
- File-backed resources beyond main are gocryptfs volumes mounted on first
  use — a start, or a blob request (`ensureVolume`,
  `resenc_wire.go:115-130`); they stay mounted until seal. `resenc.Ensure`
  holds the manager-wide `m.mu` across `gocryptfs -init` and mount
  (`internal/resenc/resenc.go:158-230`), with the default scrypt cost; the
  password is a 32-byte HKDF subkey of the vault DEK (`resenc.go:9-12`).
  kv values are sealed by the broker's barrier; `maxNSResources = 64` per
  namespace (`resources.go:613`).

### The change

1. **Keys.** `scopeKeys(scope, dep)` becomes `nsKeysFor(scope, dep, pkey)`;
   `pkey == ""` returns exactly today's answer (main and `.deployments`).
   A user partition's namespace:

   ```
   NS      = ".partitions/" + escS(scope) + "/" + dep + "/" + pkey   // dep spelled even for main
   DirKey  = NS + "/fs"          Quota = NS
   Enc     = "data/resources-enc/" + NS    KVFile = Enc + "/kv.db"
   KVLabel = "kv:" + NS + "/" + bucket     Bucket = rt.String()     // resource ids never change
   ```
   `resKeys(rt, dep, pkey)`; `TestNoAdHocResourceKeys` extends to the new
   level. The workspace scope is never partitioned (like never split,
   `deploydata.go:389`). Each partition namespace's `ns.json` records
   `{user, uid, tile, dep, created}` (§E: rebuildable identity; archives are
   self-contained).

2. **Which namespace a resource is in** (the one decision, in `reachRes`/
   `resNamespace` and `PartitionEnv`):

   | Resource in a partitioned scope | reached by | namespace | access |
   |---|---|---|---|
   | `"shared": true` | any partition, global | today's `(scope, dep)` | grant's role |
   | `"shared": "read"` | global | today's | grant's role |
   | `"shared": "read"` | a user partition | today's | **read-only**: fs bound RO; kv/blob writes and bus publishes refused 403 `<res> is read-only for people's partitions` (S8) |
   | not shared | a user partition | `(scope, dep, pkey)` | grant's role |
   | not shared | global | today's `(scope, dep)` (PD-04, PD-05) | grant's role |
   | any, from **another scope** | caller acting in `user:<id>` (a partitioned tile's principal) | that person's namespace of the target scope if the target scope is partitioned — **only if the person can read the target tile, and, when the consent policy is on, has consented to (caller tile → target tile)** (PD-13, 05 §2); else 403 / not bound | grant's role |
   | any, from another scope | everyone else | today's (global's) | grant's role |

   `resNamespace` returns `(dep, pkey, own, readOnly)`; `reach` gains `pkey`;
   `allowAt`/`readClamp` (`resources.go:706`) are unchanged (authority is the
   tile's). Every cross-scope bind or call into a person's namespace is
   counted in the caller partition's egress ledger (06 §6).

3. **`mainData`** (`binds.go:124`) also treats the `.partitions` level as
   "not main" (like `.deployments`), so partition volumes pass the guard.

4. **`PartitionEnv(c, dep, part)`** (broker; installed as the runner's
   env hook for partitions): `EnvFor(c)` exactly (same canonical paths and
   ids, so code is identical in every partition), and a remap:
   - partitioned file resources → `ResBind{Src: resMount(k)}` for the
     partition's keys (volume mounted on first use; a volume that can't mount
     has no entry → the start fails closed, as today);
   - shared file resources → `ResBind{Src: canon, Shared: true, ReadOnly:
     mode == "read"}` — new fields. `resourceBindsFor` accepts a source in
     main's data **only** for a `Shared` entry whose `Src == Dst` (the
     canonical path) **and** whose resource the runner re-derives as shared,
     with the same mode, from the registry (`Registry.PartitionedScope` +
     `Resource.Shared`), never from the env or the broker's word alone
     (06-security C7 style); a `read` entry is bound read-only;
   - cross-scope partitioned resources → bound only with the person's read
     access, and their consent when the consent policy is on (§2, 05 §2);
   - **interfaces:** `XBIN_IFACE_<SLOT>` for a multi http slot gets the
     person's **personal binds** appended (`{provider, url, service,
     personal: true}`, 05 §3); global binds come from `EnvFor` as today
     (`internal/broker/resources.go:135-159`). A personal-bind change
     restarts only that person's partition.

5. **Volumes (C6).**
   - `resenc.Manager` takes a lock per `(dirKey, name)` instead of `m.mu`
     around `run()`, so N people's cold starts don't queue behind one mutex.
   - New partition volumes are initialized with `-scryptn 10`: the password is
     already a 256-bit HKDF subkey, so scrypt stretching buys nothing and costs
     ~0.3 s and 64 MiB per init and mount. Existing volumes keep their config.
   - **Refcounted idle unmount inside resenc:** every user of a partition
     volume (the runner's instance, the broker's blob/kv handlers, backups,
     resets) takes a reference; a volume with no reference for 60 min is
     unmounted, and a partition namespace's bbolt `kv.db` handle is closed on
     the same timer. Global and shared volumes stay as today. This bounds the
     gocryptfs process count to the partitions used in the last hour instead
     of N × M forever.

6. **Encryption hold.** `EncryptionHoldReason` (`resenc_wire.go:163`) and
   `DeploymentEncryptionHold` (`:174`) gain a partition variant used by
   `ShouldRunPartition`.

7. **Quotas** (`internal/broker/diskmon.go`, `resusage.go`): each partition
   namespace is measured like a deployment namespace (key `Quota = NS`) and
   has its own **hard ceiling**, by default the tile's per-namespace ceiling,
   lowerable per tile in `POST /partitions/limits` (S20). There is no
   tile-wide partitions total (C21): the low-disk "biggest offenders" logic
   treats each partition namespace as its own candidate, so one heavy person
   is blocked, not everyone. Directly mounted sqlite/filesystem can't be
   write-blocked at the API (`diskmon.go:29-35`): as today they raise alerts
   (metadata only: tile and partition key, never contents) and stopping them
   is the admin's act (`partitions/stop`).

8. **Namespaces, orphans, sweeps (PD-26, C1, C2).** `nsVolume`
   (`resenc_wire.go:268`), `eachNamespace`/`SweepNamespaces`
   (`internal/broker/deployns.go:683/707`), `OrphanedNamespaces` learn the
   `.partitions` level. A partition namespace is **orphaned** only on a
   recorded event:
   - `user-deleted`: the users-store delete hook fired for its uid, or its
     `ns.json` predates the current holder of its user id (the id was deleted
     and recreated, possibly by an older xbind — §E);
   - `tile-removed`: the tile was removed (today's removal flow).
   Only these two are swept automatically after the retention (default 30
   days). The sweep also erases the partition's backup subkey (11 §3).
   **Never** orphaned:
   - a tile whose switch is pending, or whose request is invalid or was
     declined: kept and not running until a manager decides;
   - a missing index or record;
   - an unknown user record: kept, and reported by `bx doctor`.

   A confirmed **mode switch** isn't a sweep. It deletes the tile's
   namespaces at once through the wipe executor (01 §2.6), and each plane
   registers a wipe hook with it: namespaces here, the vault (§C),
   registrations and records (§D, §E), mail (04), layers and history (06),
   consents, ledgers and personal binds (05, 06), and subkeys (11).

9. **Seed/reset/restore.** No seeding between partitions (a partition never
   starts from another's data). `POST /api/xbin/partitions/reset` (06 §6)
   wipes one partition's namespace (+ vault + registrations + mail), holding
   its gate like `holdNS` (`deployns.go:139`) and stopping its instance first.
   It also erases the partition's backup subkey (11 §3): a person resetting
   their data wants it gone from backups too.

## C. Vault

### Current behaviour

`vaultPathIn(comp, dep)` (`internal/broker/deployvault.go:64`) → main's
file or `data/vault/.deployments/<key>/<name>.json`; `vaultDeployment`
(`:152`) picks the deployment for the caller (a tile principal: its own;
admins: `?deployment=`); `vaultAdmin` (`:201`) for cross-vault access.

### The change

- `vaultPathIn(comp, dep, pkey)`: `pkey == ""` today's; a user partition:
  `data/vault/.partitions/<TileKey>/<dep>/<pkey>.json` (0600, xbind-owned,
  barrier-sealed values, never bound into a sandbox — the vault stays
  broker-only).
- `vaultDeployment` → `vaultTarget(w, r, p, tile) (dep, pkey, named, ok)`:
  the tile's own principals reach **their** partition's vault (a terminal's
  `bx vault set` → the person's). `?partition=` on a **partitioned** tile is
  refused for everyone (400 `a partition's vault is reached only from inside
  it`); on other tiles the parameter is ignored as today (C22). An admin
  reaches only the global (today's) vault of a partitioned tile (PD-07).
- Admin vault listings (`GET /vaults`) show a partitioned tile's global keys
  and, for partitions, a **count** only — never partitions' key names (S19).
- Placeholders, `VaultCopy` and `DeploymentVault` summaries don't cross
  partitions. Purge/reset deletes the partition's file.

## D. Registrations (cron, bus subscriptions, interface instances, ingress hosts)

### Current behaviour

Main's rows in `data/cron-jobs.json` / `data/bus-subscriptions.json`; a
non-main deployment's in `data/deployments/<TileKey>/<name>/{cron,bus-
subscriptions,iface-instances,ingress-hosts,backup-schedule,sandboxes}.json`
through plane hooks (`deploydata.go:72-82`; `internal/broker/dormant.go`,
`regDeployment :181`, `firing :213`, `fireDep :419`; `bussubs.go`
`publishIn :293`, `deliver :420`). Files are the truth; an older xbind never
reads them (12-compat PO-9). Cron fires once, without retry — a refused
dispatch drops the tick (`internal/broker/cron.go:180-205`); no per-component
job cap; bus: 64 subscriptions per component (`bussubs.go:50-53`).

### The change

- **Where:** a user partition's rows live in
  `data/partitions/<TileKey>/<dep>/<pkey>/{cron,bus-subscriptions,iface-
  instances,ingress-hosts}.json`, schema 1, same row shapes as the deployment
  files (`depCronRow`, `depBusRow`). Global's stay in today's stores. An
  older xbind never reads the new files, so it never fires a person's job as
  the tile's.
- **Who registers:** `regDeployment` → `regTarget(r, p, tile) (dep, pkey,
  status, err)`: a partition principal registers only in its own partition;
  `?partition=` on a partitioned tile → 400; admins list/delete only global's
  rows and see per-partition *counts* in `GET /partitions`.
- **Caps for user partitions (C5):** 16 cron jobs, minimum interval 1 min,
  16 bus subscriptions per partition (global keeps today's limits).
- **In memory:** keys `depKey(tile, dep, name)` gain the pkey
  (`tile\0dep\0p\0pkey\0name`); `busSubState` gains `ownerPart`. Anything that
  enumerates by component (backups' `cronJobsFor`/`bus.forComponent`,
  `internal/broker/backup.go:92-100`) filters `ownerPart == ""` (S15).
- **Firing:** `firing(tile, dep)` → `firing(tile, dep, part)`: today's
  deliveries switch **and** the partition's person exists (same uid), is
  enabled and can read the tile (PD-20). Asked at every tick, publish and
  delivery (never cached) — a person regaining access resumes without
  re-registering.
- **Cron from a user partition** is a background start (§A.5). When admission
  defers it, the tick is retried with jitter within its period (at most
  until the next tick), instead of being dropped as `cron.go:180-205` does
  today; a tick still undeliverable is counted (`missedTicks` in
  `GET /partitions`).
- **Bus deliveries to user partitions:** events in the partition's own
  namespace reach its subscriptions as today (the publisher is that
  partition, so it runs). Deliveries from a **shared** bus or another tile's
  bus reach a user partition's push subscription **only while that partition
  is running** — never cold-starting it; skipped deliveries are counted
  (`dormantDrops`). Only partition mail may cold-start a partition in the
  background (04 §3).
- **Interface instances and ingress hosts** registered by a user partition
  are stored and answered with success but never route (dormant, PD-21): the
  public surface and `#instance` bindings belong to the global instance.
- **Boot:** `loadDeps` also walks `data/partitions/`.

## E. Identity records (xbind-owned) — PD-43, C2, S17

- **The uid lives in the users store.** `users.User` gains `UID string
  json:"uid,omitempty"`: minted (128-bit random) at creation for new users,
  and lazily at a person's first partition for existing users, written by
  xbind into the users store (no boot migration, rule 9). Deleting the user
  deletes the uid with the record — including a delete by an older xbind, so
  a recreated id always gets a new uid.
- **Adoption instead of re-minting.** A user record without a `uid` (an
  older xbind rewrote the store; a restored users file) adopts the uid that
  that user id's live partition records (`partition.json`/`ns.json`) carry
  **when those records were created at or after the user record's
  `Created`** (`internal/users/users.go:106`; kept across upserts, `:540`).
  Records created before it belonged to a previous holder of the id and are
  orphaned (`user-deleted`). xbind never mints a new uid for an id while a
  live record created after the user's `Created` carries one.
- **Per partition:** `data/partitions/<TileKey>/<dep>/<pkey>/partition.json`
  — `{schema: 1, tile, dep, user, uid, created, lastStarted, state:
  "active|dormant|orphaned", reason, lastExit, restarts, crashLoop}`,
  mirrored (`user, uid, tile, dep, created`) into each namespace's `ns.json`.
  `GET /partitions` lists these; the sweep reads them. If `partition.json` is
  missing, it is rebuilt from the namespaces' `ns.json` at boot; a missing
  record never makes data orphaned (§B.8).
- **Mode:** `data/partitions/<TileKey>/mode.json` (01 §2), with the
  recorded mode, any open request or decline, and the history.
- **Personal binds:** `data/partitions/binds/<uid>.json` (05 §3).
- All under `data/`, masked from every sandbox (D118: trust never comes from a
  file a sandbox can write).

## Wire (for docs/protocol.md)

- Env `XBIN_PARTITION` (backend contract section, protocol.md §Backend
  contract).
- `?partition=` refused (400) on vault, cron, bus-subscription, logs routes
  **of partitioned tiles** (ignored elsewhere, as today).
- 503 admission text; 404 `ErrNoPartition` for `global` on a tile without
  one; `POST /api/xbin/partitions/limits` (06 §6).
- 403 on writes to a `"shared": "read"` resource from a user partition.
- Disk: 507 past a partition's own ceiling (kv/blob).
- `users` rows gain nothing on the wire (`uid` is internal; `GET /users`
  doesn't list it).

## Tests

- runner: `TestPartStateKey` (distinct from every `stateKey`),
  `TestEnsurePartitionNeedsIsolate`, `TestEnsurePartitionPrimaryOnly`,
  `TestPartitionBuildOnce` (N concurrent ensures → one build; fake engine),
  `TestPartitionAdmission` (interactive evicts only not-in-use; background
  never evicts in-use or streaming partitions; passive streams don't count as
  in use; caps from `MemTotal`; admin override), `TestPartitionReap10m`,
  `TestAlwaysOnGlobalOnly`, env golden for global vs user vs unpartitioned
  (`deploy_test.go` style, XBIN_PARTITION placement),
  `TestUserPartitionNetworkIsNonPrimary` (spec for a user partition of a
  host-net / splice / net-provider fixture has no HostNet, splice, roster,
  links or ingress relay).
- isolated (`test/isolated`): two partitions of a host-net fixture can't
  connect to each other's `127.0.0.1` listener; two people's agent
  partitions can't see each other's `XBIN_RES_*` files (bind check inside
  the sandbox); a `"shared": "read"` filesystem is read-only inside a user
  partition.
- runner binds: `TestSharedBindNeedsRegistryShared` (a `Shared` remap for a
  resource the registry doesn't mark shared is refused; `Src != Dst` refused;
  a `read` resource bound RO even if the remap says RW), `.partitions` passes
  `mainData`.
- resenc: per-key locks (two volumes init concurrently), `-scryptn 10` on new
  partition volumes only, refcounted idle unmount (a blob request in flight
  holds the mount; unmount 60 min after the last reference).
- broker keys: `TestNsKeysPartition` (injective across scope/dep/pkey,
  never under `.deployments`, main's unchanged byte for byte),
  `TestNoAdHocResourceKeys` extension.
- broker reach: the §B.2 table (own/other scope, shared true/read, global,
  cross-scope with the consent policy off (read suffices) and on (with and
  without consent)).
- sweeps: `TestPartitionOrphanRules`:
  - pending, declined and invalid never orphan;
  - the delete hook and a recreated id (a record older than `Created`)
    orphan;
  - a missing `partition.json` is rebuilt from `ns.json`;
  - a lost users-store uid is re-adopted, never re-minted;
  - the sweep erases the `part:` subkey;
  - `TestWipeHooks`: after a switch, no store of this file holds a trace of
    the tile's partitions.
- vault: a terminal of alice writes alice's file; admin `?partition=` 400 on a
  partitioned tile, ignored on others; `GET /vaults` shows partition counts,
  no names.
- registrations: a partition's cron fires into it; retried with jitter when
  admission defers it; stops while the person is disabled; never appears in
  `data/cron-jobs.json`; per-partition caps; shared-bus deliveries skip a
  stopped partition (counted); iface-instances from a partition answer 200
  and route nothing; a backup manifest lists only global's rows.
