# S1 — sealed components: findings and prototype

> Spike S1 of the workspace agent (issue #51, `plans/workspace-agent.md`
> §3, §8). Branch `wsagent/s1-sealed`, against master 83c03745, 2026-11-14.
> Line references are to the spike's commit (321aaeaf).
>
> **Status: implemented as D227** on the same branch (`plans/DECISIONS.md`;
> builder docs `docs/sealed.md`, `docs/protocol/sealed.md`). The owner's
> decisions on §11, and what changed from this document:
>
> | §11 | Decided |
> |---|---|
> | 1 name | `xbin/wsagent`; boot refuses a name that shadows a route or page, and a test walks the route table |
> | 2 word | "sealed component" in code and builder docs, "system component" for people |
> | 3 backend | prebuilt in production; only `--dev`/`--no-auth` compile; a missing build holds the component |
> | 4 squatter | the sealed scope takes its key; foreign data at the key holds the component and is never touched |
> | 5 listing | sealed rows only with `?sealed=1`; `/xbin/system` |
> | 6 admins | read access, enable/disable, limits, bindings of declared slots; nothing else |
> | 7 partitions | work for a sealed scope; unavailable without `--isolate`, except one global instance in development |
> | 8 backups | data only; a restore goes through the data stamp |
> | new | the table carries `Capabilities.Delegate` and the declared slots; a sealed component is never a target (the delegation spike's T3) |
>
> Differences from the design below: the stamp is claimed at boot and
> raised by the first admitted start (not "when the first instance is
> healthy"), and a restore never lowers it; write level is clamped as well
> as terminal level; the refusals are a table with a gate
> (`server/sealedclass.go`) rather than per-handler checks; the estimate
> of §7 held for the Go (about 2,600 lines without tests, 2,500 of tests) and the shell,
> admin console and app changes were left to the workspace agent's UI work,
> since the listing switch keeps sealed rows away from them.

## 1. Verdict

The facility is affordable. A sealed component fits the model xbind
already has for **a tile whose primary is pinned to a materialized
read-only tree** (tile deployments, D119): the registry composes the
component from code that is not the work tree, the server serves it
"beneath a root" in all three asset modes, and the runner binds that root
read-only into the sandbox. A sealed component is that, with the tree
rendered from the binary's embedded source instead of a checkpoint, and no
work tree at all.

The prototype is 198 added lines in 19 existing files, 377 lines in three
new files, and a 453-line test. It serves and runs a sealed
component, and hosts the agent template itself (frontend, registry row,
encrypted resources, backend built and started). My estimate for the
production facility is **about 2,300 lines of Go plus tests, and about 600
lines of JS/Swift**, in five PRs (§7). The fallback (the engine as a
library in a confined xbind worker) should stay shelved (§10).

Three things the plan did not know:

1. **`xbin/agent` is taken.** `/api/xbin/agent/providers` and
   `/api/xbin/agent/history*` are xbind's own routes
   (`internal/server/agentapi.go:27-46`), and a component's API lives at
   `/api/<path>/…`. The workspace agent needs another name (`xbin/wsagent`
   is what the prototype uses), or sealed components need a sub-namespace.
   Boot now refuses a table whose name shadows an API route.
2. **The component path is safe, the data key is not.** No workspace can
   hold a component at `xbin/…`, but a top-level directory named
   `xbin~agent` maps to the same resource data key as scope `xbin/agent`
   (`util.ScopeKey`, `/` → `~`). The prototype makes the sealed scope the
   key's holder; adopting a squatter's existing data still has to be
   refused (§3.2).
3. **"Sealed" already means the vault.** `sealed`/`unseal` is vault
   vocabulary across code, docs and UI (`test/sealed_backup_test.go`,
   `resenc.ErrSealed`). A "sealed component in a sealed workspace" reads
   badly. I kept the word in the prototype; a rename is cheap now (§11).

## 2. The design

| Aspect | Design | Prototype |
|---|---|---|
| Declaration | A compiled-in table, `sealed.Table()`: name, embedded source, authored path, template flag, partition flag. Later columns: slots with default bindings, capabilities, data-format number, prebuilt flag. Nothing in a workspace declares one; `Config.Sealed` replaces the table in tests only (no flag, no env). | `internal/sealed/sealed.go` |
| Path | `xbin/<name>`. `util.SealedPath` is structural: under `xbin/` a path is xbind's or nothing. | `internal/util/util.go` |
| Source | At boot the binary renders its embedded tree with `builtins.RenderTree` (the transforms an instance gets: `go.mod.tile` → `go.mod`, authored path → reserved path, template block stripped) and hashes it. | done |
| On disk | The rendered tree is materialized to `<ws>/.xbin/sealed/<name>/<digest24>/`, mode 0444, xbind's own data (`.xbin` is masked from every sandbox). Verified file by file at every boot and rewritten when it differs; other digests are removed. Never in the workspace tree, never in `go.work`, never a `deps/` target. | done, incl. tamper test |
| Registry | `registry.OpenSealed(root, set.Registry)`; `Rescan` adds the rows after the walk (`addSealed`), with `Component.Sealed = true`, `Dir` = the materialized tree, manifest and scope parsed from the rendered bytes by the same validation as a tile's. | `internal/registry/sealed.go` |
| Frontend | `/c/xbin/<name>/…`, through the existing pinned tail (`servePinnedStatic` → `serveCode`, `openPinned` beneath the tree) in legacy, tokens and origins modes, with the one sanctioned transform. `Server.codeRoot` answers `(c.Dir, pinned)` for a sealed component. | done (legacy mode tested) |
| API | `/api/xbin/<name>/…` → the component proxy, decided in `handleAPI` before xbind's own mux; `Server.SealedAPIClash` fails the boot when a name equals the first segment of a registered route. | done |
| Backend | The normal runner: same states, limits, logs, sandbox spec. Prebuilt binary from the bundle when present (`bin/sealed/<name>-<digest24>`, the digest of the embedded source, so a binary built from other source is never run); otherwise built from the materialized source by the existing per-tile build, with **no workspace module and no root `go.work` line in its build workspace**. | done; prebuilt lookup written, not exercised |
| Surfaces removed | Terminal level does not exist on a sealed path, for admins and the owner token too (`auth.CanTerminalTile`, `CanTerminalTileVia`): no terminal, no agent session, no `term-net`. Code API, git, PRs, git import and chrome approval already refuse the path through `util.ComponentPathOK` (code and git tested; PRs, git import and chrome by reading the code). Clone-from and offload are refused explicitly. Deployments are not closed yet (Q3). Watcher events under a workspace `xbin/` directory are dropped. | done |
| Surfaces kept | `/components` row with `sealed: true`; read/write levels, grants, bindings, lifecycle disable/hide, limits, cron, bus, notify, status; logs for admins (`obs.canReadLogs`). | done |

## 3. No workspace can hold, or create, the reserved path

### 3.1 The component path

- `xbin` is in `util.ReservedTop` (`internal/util/util.go:67`). It has been
  since the rename commit de19a580 (2026-07-05); before it, `buxon` was
  reserved from the initial commit. v0.1.0 (2026-08-02) contains de19a580,
  so no released xbind ever scanned a top-level `xbin/`.
- `Registry.Rescan` skips reserved top-level directories whole
  (`registry.go`, the `util.ReservedTop[...]` → `SkipDir` branch).
- Every creation path refuses it through `util.ComponentPathOK`
  (`util.go:97-105`): create, clone target, builtin import, template
  instance, git import (`broker/clone.go:68`, `gitimport.go:148`,
  `builtins.go:292`, `scaffold.go:39`).
- `/c/` refuses it (`pathAllowed`, `static.go:414`); the prototype admits
  only paths the registry resolves to a sealed component.
- A directory `<ws>/xbin/agent` made by hand in a terminal is never
  scanned, served or run. The test plants one (a `node` backend with
  `chrome: true`) before boot: the sealed row, page, module and backend
  are the table's, and the directory is untouched.
- The watcher does not ignore top-level `xbin/` (`watch.go:67`), so a save
  there resolved to the sealed component and would have rebuilt it; the
  prototype drops those events (`changedComponents`), with a test.

### 3.2 The data key (not closed yet)

Resource data is keyed by `util.ScopeKey(scope)`, which is not injective
(`scopekeys.go:16-26`). A real top-level directory `xbin~wsagent` with a
`scope.json` holds key `xbin~wsagent`, the same as sealed scope
`xbin/wsagent`: shared encrypted volumes, quota and backups.

- The existing D118 rule gives the key to "the scope that held it before",
  and checks holders with an `Lstat` under the workspace, which a sealed
  scope never passes. Unchanged, a squatter would win after a restart.
- Prototype: a sealed scope always holds its key (`sealedHolder`,
  `scopekeys.go`), as the workspace scope holds `workspace`. The squatter
  stays a tile but its resources are refused, with the D118 manifest
  error. Tested with a squatter present before the first boot.
- `Registry.ScopeKeyClash` already refuses creating a tile at a clashing
  path through xbind once the sealed scope is registered.
- **Still open**: a squatter that held data before the upgrade leaves
  `data/resources*/xbin~wsagent` on disk, and the sealed component would
  mount it. Production rule: the first start of a sealed component writes
  a stamp (§6); a key directory that exists without the stamp holds the
  component (it refuses to start, names the directory, touches nothing).
  The squatter losing its resources on upgrade breaks a workspace in
  theory; a workspace with a scope directory literally named
  `xbin~<sealed name>` is not plausible, and the alternative is sharing
  encrypted volumes with it. Needs the owner's nod (§11).

## 4. Backend: prebuilt or built on first use

Measured on this machine (192 cores, 754 GiB, tmpfs), agent template
backend at 83c03745, `CGO_ENABLED=0`, cold build cache, warm module cache:

| | Value |
|---|---|
| Cold build, 2 CPUs (`taskset -c 0-1`) | 33.1 s |
| Cold build, 16 CPUs | 11.5 s |
| First request through xbind, sealed, unpinned (boot test) | 11.6 s |
| Binary | 60.3 MB; 44.0 MB with `-s -w` |
| Binary, zstd -6 (bundle cost per arch) | 29.8 MB; **16.1 MB** stripped |
| Go build cache it leaves | 367 MB |
| Module sources it needs (26 modules, extracted) | 414 MB, downloaded on first build |
| Current bundle (`xbin-v0.3.79-linux-amd64.tar.zst`) | 1,936 MB |
| xbind binary | 51.5 MB |

Not measured: a cold build on a small VPS, and the module download time.
The 2-CPU figure is on fast cores; expect one to several minutes on a
2-vCPU host, plus the download.

**Recommendation: prebuilt (a).** It adds 16 MB (0.8%) per architecture to
the bundle and removes, per workspace, about 840 MB of caches, a
multi-minute first start, and a dependency on the Go module proxy at the
moment a person first opens their agent. It also removes the build
sandbox's network access from the sealed path.

- `deploy/publish-release.sh`: after `make build`, build each table row's
  backend for the arch from the same rendered tree (a small
  `xbind sealed-build --out bin/sealed` subcommand, so the digest in the
  file name is computed by the code that will look for it), copy
  `bin/sealed/` into the stage, and fail the release when a row marked
  prebuilt has no binary. `deploy/install.sh` already unpacks `bin/` whole.
- `make vulncheck` already scans the agent template's backend
  (`AGENTS.md`); the sealed build is the same module.
- Fallback order at start: the bundle's binary for this digest; else,
  under `--dev` or when the row allows it, a source build; else an error
  naming the missing file. Production should not silently compile: a
  bundle without the binary is a broken release. Owner's call (§11).
- `make dev`: no `bin/sealed/`, so it compiles from the checkout, through
  the same per-tile build. Reading the source from disk under `--dev`
  (as `web/` and `docs/` are) is a small addition, not in the prototype.
- Source builds stay supported for one more reason: node/python sealed
  components have no binary, and run from the materialized tree as is.

## 5. Answers

### Q1. Which packages learn the kind

See §7 for the list with sizes. The principle: `Registry.Components()`
includes sealed components, so the runtime and data planes (runner, proxy,
broker policy, limits, cron, bus, partitions, bindings) need no
per-feature opt-in, and the places that turn a tile path into a workspace
directory are closed. Most of those are closed already by
`util.ComponentPathOK`, which refuses `xbin/…`.

### Q2. Data, scope, partitions, vault, bindings, cron, bus, notify, limits

- **Scope**: the component roots its own scope, `xbin/<name>`, from the
  embedded `scope.json`. Resource targets are `res:xbin/<name>/<res>`
  (the template's `res:apps/agent/db` is rewritten by `RenderTree`; the
  hosted-agent test checks no `apps/agent` remains in its grant requests).
- **Where data lives**: unchanged layouts, keyed by path-derived keys that
  all accept `xbin/<name>`:
  - `data/resources/xbin~<name>`, `data/resources-enc/xbin~<name>/<res>`,
    kv buckets `res:xbin/<name>/<res>` in `data/kv.db`
    (`broker/deploydata.go:436-438`);
  - partitions: `data/resources-enc/.partitions/<escS>/<dep>/<pkey>`
    (`deploydata.go:457-473`), vault `data/vault/.partitions/<TileKey>/…`
    (`partitionvault.go:52-63`), mode `data/partitions/<TileKey>/mode.json`;
  - tile vault `data/vault/<CompKey>.json`; logs `.xbin/log/<CompKey>.log`;
    build `.xbin/build/<CompKey>`; cgroup leaf `tile-<CompKey>`.
  The hosted agent test shows the broker provisioning
  `res:xbin/wsagent/db` as an encrypted volume and holding the start until
  the vault and gocryptfs are there, exactly as for a tile.
- **Partitions**: the mode request is the rendered manifest's `partition`
  (the template strip writes it when the row says partitioned), so
  `settlePartitions` and the mode store work unchanged; the recorded mode
  follows the request while the component holds no data. Two blockers:
  - `partitionKeys`, `partitionNS` and the non-main deployment keys
    reject `!util.ComponentPathOK(scope)` (`deploydata.go:445/465/486`):
    a sealed scope gets no partition namespace today. Fix: admit a
    registered sealed scope in the two partition checks only.
  - A partitioned tile may not hold `xbin`, `xbin:*` or `cap:sandboxes`
    grants (`registry/partition.go:328-334`). Delegation must therefore be
    its own primitive (as §4 of the plan already says), never an `xbin:`
    grant.
  Partitions need `--isolate`. Without it the row runs one unpartitioned
  instance, which is wrong for a per-person agent: the workspace agent
  should be unavailable without isolation (single-owner `--no-auth` dev
  excepted). A row's partition mode must never change after release (a
  switch with data present deletes it); a test should pin it.
- **Vault**: nothing new; sealed data needs the vault unsealed like any
  encrypted resource, and the "holds data" check waits on unseal.
- **Bindings (`llm`)**: stored in the workspace `xbin.json` under
  `bindings["xbin/<name>"]`; `validateBinding` needs only
  `Reg.Component` (`netfn.go:884`), so `bx bind` and the Interfaces tab
  work today. New: a table default ("`llm` → the xbind connector")
  applied at read when the workspace holds no binding for the slot, never
  written to the file.
- **Grants**: same-scope resource grants are automatic as for any tile.
  Cross-scope requests (`cap:open-links`) land pending today; table
  capabilities should be granted by the table (computed, not stored).
- **Cron, bus, notify, limits**: keyed by path string in
  `data/cron-jobs.json`, `data/bus-subscriptions.json`,
  `data/tile-limits.json`; none refuses or mangles `xbin/<name>`. The
  reserved identities `xbin/cron`, `xbin/bus`, `xbin/mail` are matched
  exactly (`broker.go:35`, `bussubs.go:47`, `partitionroute.go:44`), so
  those three names are refused as sealed names (`sealed.reservedNames`).

### Q3. What assumes "a component is a directory under the root"

About 70 `Components()` iteration sites in 45 files and about 65 `c.Dir`
uses. With `Dir` pointing at a real, read-only tree most keep working.
What breaks or misbehaves:

| Site | Effect | State |
|---|---|---|
| `deps.Reconcile` (`deps.go:52/92`) | `MkdirAll(c.Dir/deps)` into the sealed tree | fixed |
| `deps.goModules` (`deps.go:160`) | sealed tree listed in the workspace `go.work` | fixed |
| `runner.buildWork` (`gowork.go`) | workspace modules and root `go.work` replaces reach the build | fixed |
| `runner.goBuildCmd` | the tree is under the masked `.xbin` in the build sandbox | bind added, untested under isolation |
| `boot.changedComponents` | workspace saves under `xbin/` rebuild it | fixed |
| `scopeKeys.resolve` (`Lstat` holders) | a squatter takes the data key | fixed (§3.2 remainder open) |
| `broker/clone.go:107` | copies the sealed tree into the workspace | refused |
| `broker/lifecycle.go` offload → `backupTile` (`backup.go:125/516`) | archives and removes a source tree that isn't there | refused |
| `broker/backup.go`, `backup_cron.go` | tile backup = source + data; sealed needs data only | open |
| `broker/deploydata.go:445/465/486` | no partition namespace for a sealed scope | open |
| `broker/templaterepo.go:203`, `tiles.go:158` | template update/merge on `c.Dir` | open: refuse sealed |
| `deployments` (`branch.go`, `ops.go:267`, `plane.go:664`) | git on `c.Dir`; `Plane.CodeRoot` returns `c.Dir` unpinned | open: refuse in `singleDeployment` (`authz.go:346`); the server already bypasses the plane |
| `tilesbx` (`keys.go:149`) | tile sandboxes bind code roots | open: a sealed component declares none in v1 |
| `server/tileassetsapi.go:40` | scans `c.Dir` | works (reads the tree) |
| `server/api.go:279` (`API.md`) | reads `c.Dir/API.md` | works (tested) |
| `cmd/bx/doctor.go:59` | stats `<ws>/<path>/API.md`, warns | open: skip sealed |
| `registry/qualifier.go:139`, `deployview.go` | `onDisk` checks under the root | open: no deployments on sealed |

### Q4. `/components` and unknown-kind compat

The prototype adds `sealed: true` to the row and nothing else. Clients:

- **Old shells** (scaffold copies): the sidebar lists every component with
  an index that is not a template, offloaded or hidden
  (`shell/bx-side.js:77-81`), and the tile menu always offers terminal,
  logs, source and proposals (`menus.js:209-212`). An old shell would show
  `xbin/wsagent` as a tile whose terminal answers 403 and whose source
  answers 400. Nothing crashes (`r.json()`, no strict decoding).
- **Old admin consoles**: rows come from `/auth-overview`; clicking a path
  opens `/code/tree` (400), each row has disable/hide (allowed). The clone
  list in the manager is unfiltered (`tiles/manager/index.html:304`);
  cloning is refused server-side.
- **iOS**: hand-decoded `JSONValue` (`Catalog.swift:132`), tolerant.
  `xbin://ws/xbin/<x>` deep links mean an xbind page (`DeepLink.swift:125`),
  and `web/xb/tile-resource.js:68` returns `null` for a first segment
  `xbin`: the native runtime needs changes before a sealed tile's
  `native.js` works.
- **`bx`**: plain `json.Unmarshal`; `bx ls` lists it; term, code, pr,
  clone are refused by the server.

**Recommendation**: `GET /components` omits sealed rows unless the caller
asks (`?sealed=1`), so no old shell, manager or app build ever shows a
tile it can't handle. `/auth-overview` (admin only) and
`/components/<path>` include them with `sealed: true`, so an old admin
console still shows status and access. Add an xbind-served
`/xbin/system` page (status, logs, who has access) as the guaranteed
admin surface for old scaffolds. This is additive under `docs/compat.md`.

### Q5. Memory and idle stop

- One idle agent backend, hosted sealed and unpartitioned, right after its
  first request: **53.5 MiB resident** (28.5 MiB anonymous), 55 threads on
  this 192-core host. Consistent with the partitions measurement
  (55.8 MiB mean, 61.2 max per person's partition with its two gocryptfs
  processes; `plans/partitions/records/I2.md`). 96 MiB is the admission
  budget `E`, not the measured size.
- Nothing about sealing changes the cost: a person's partition starts on
  first use, stops 10 minutes after its last use, its volumes unmount an
  hour later, and the per-tile and per-workspace caps apply
  (`docs/partitions.md` "Started on first use, stopped when idle").
- What the workspace agent adds is a second partitioned agent per active
  person on workspaces that also use AgTT, under the same workspace cap
  (`clamp(MemTotal/2 ÷ E, 8, 128)`): on an 8 GiB host about 42 running
  partitions in total. A long-lived agent that wakes on session status
  (bus deliveries) restarts its partition each time; deliveries are
  background starts, at most 4 at once. Not measured: resident size under
  a long conversation, and the wake-up rate in real use.
- With a prebuilt binary a cold partition start is a process start plus
  migrations, not a build.

### Q6. Risks and the fallback

See §9 and §10.

## 6. Data across versions, and the upgrade

### The version stamp

The agent template has no schema number today: `migrate.go` is idempotent
`CREATE TABLE IF NOT EXISTS` and guarded conversions. The facility needs
one, owned by xbind so the refusal happens before anything starts:

- Each table row carries `Data int`, the component's data-format number.
  It is raised only by a release whose migrations make a store unreadable
  to the previous release's code.
- xbind keeps `data/sealed/<name>.json` (xbind's own, masked from
  sandboxes, in backups): `{"data": 7, "release": "v0.3.82",
  "digest": "…", "at": "…"}`. It is a high-water mark for the whole
  component, all partitions and the global instance.
- Before any instance of the component starts (the runner's `ShouldRun` /
  `HoldReason` hooks, which already hold a tile with a reason):
  - no stamp, and no data directory at the component's keys: first use;
    the stamp is written when the first instance becomes healthy;
  - no stamp, but data exists at its keys: held (§3.2);
  - `stamp.data == row.Data`: start;
  - `stamp.data < row.Data`: start; the backend migrates its own store
    forward at start, as it does today (each partition when it next
    starts); the stamp is raised when the first instance is healthy;
  - `stamp.data > row.Data`: **held**. Every call answers 409 with
    "`xbin/wsagent` data was written by xbin v0.3.82 (data format 7); this
    xbin reads up to format 6. Install v0.3.82 or later. Nothing was
    changed." Nothing is mounted, migrated or deleted.
- The backend learns the number through `XBIN_SEALED_DATA` and also writes
  it into its store (`PRAGMA user_version`), refusing a newer one itself:
  the second line of defence for a store restored from a backup.
- A downgrade to an xbind older than the facility: the component does not
  exist there. Its data directories, grants, bindings and lifecycle rows
  in `xbin.json` stay unused. I did not verify that every old release
  leaves unknown keys alone (grant pruning, orphan-volume cleanup): that
  needs a `test/downgrade_test.go` case against `XBIN_DOWNGRADE_BIN`
  before release.

### What happens on an upgrade, step by step

1. The service restarts onto the new binary. Every backend, sealed or
   not, gets SIGTERM with the usual drain; tile agent sessions end as they
   do today. There is no blue/green across an xbind restart for any tile.
2. Boot, registry step: the new binary renders its embedded trees. A
   changed tree has a new digest; it is written to
   `.xbin/sealed/<name>/<new>/` and the old directory is removed. An
   unchanged tree is verified in place.
3. The registry registers the component at the same path. Everything
   keyed by path is as before: grants, bindings, lifecycle, partition
   mode, cron jobs, bus subscriptions, limits, data.
4. The stamp check runs (above).
5. Nothing starts at boot except an `alwaysOn` global instance. Each
   person's partition starts on their next use, a cron tick or a bus
   delivery.
6. A start takes the bundle's binary for the new digest (or, in dev,
   builds the new source once; the artifact is shared by all partitions).
   The backend migrates its store forward and becomes healthy; xbind
   raises the stamp.
7. Frontends: the page and modules are the new tree's on the next load
   (`Cache-Control: no-store`). Open frames reconnect their event stream;
   I did not check whether a frame reloads after an xbind restart, so a
   stale page talking to a new backend for the life of a tab is possible
   and should be closed with a version check in the frame.

The prototype test covers steps 2, 3, 5 and 6 for a source build: same
workspace, a binary carrying other code, new page, new module, new
backend, old tree gone.

## 7. Change list for production

Sizes are added or changed non-test lines, my estimate. "P" marks what
the prototype already has.

| Package / file | Change | Size |
|---|---|---|
| `internal/sealed` (new) | table, render, digest, materialize (P); table columns (slots, capabilities, data number, prebuilt); stamp store; dev source from disk; `xbind sealed-build` | 450 (P: 220) |
| `internal/util` | `SealedPath` (P); `TilePathOK` for the two partition checks | 30 (P: 11) |
| `internal/registry` | `SealedCode`, `OpenSealed`, `addSealed`, `Component.Sealed`, scope-key holder (P); table partition immutable | 180 (P: 140) |
| `internal/watch`, `boot/serve.go` | drop events at sealed paths (P) | 10 (P: 6) |
| `internal/deps` | skip sealed in `Reconcile`, `goModules` (P) | 6 (P) |
| `internal/runner` | prebuilt hook, sealed build workspace, sandbox bind (P); stamp hold through `ShouldRun`/`HoldReason`; `XBIN_SEALED_DATA` env; no tile sandboxes | 120 (P: 25) |
| `internal/proxy` | none for routing (P: zero lines); 409 body for a held stamp | 20 |
| `internal/server` | `/c/` and `/api/` dispatch, `codeRoot`, clash check, row field (P); `?sealed=1` filter; `/xbin/system` page; frame version check | 350 (P: 70) |
| `internal/auth`, `internal/obs` | terminal level clamp, admin logs (P) | 20 (P) |
| `internal/broker` | clone, offload refusals (P); partition keys admit sealed; data-only backup and restore; template update/merge refusal; table default bindings and table capabilities; `/auth-overview` field; access `PUT` refuses terminal level on sealed | 450 (P: 12) |
| `internal/deployments` | refuse sealed in `singleDeployment` | 15 |
| `internal/builtins` | none (`RenderTree` reused) | 0 |
| `internal/boot` | wiring, prebuilt lookup, `Config.Sealed` (P); stamp wiring | 80 (P: 50) |
| `cmd/bx` | `ls` marks sealed; `doctor` skips; clear errors for term/code/pr on sealed | 60 |
| `deploy/publish-release.sh`, `Makefile` | build and ship `bin/sealed/`, fail on a missing prebuilt | 50 |
| `internal/server/openapi.go`, `docs/` | `sealed` field, `?sealed=1`, `/xbin/system`, compat note, D-entry, changelog | docs |
| Tests | registry, scope key, authz matrix per surface, isolated integration (`test/`), downgrade both directions, release bundle check | 900 |
| `web/` (`bx-frame`, `frame-info`, `bx-grants`, `xb/tile-resource.js`) | sealed tiles: no edit affordances, native runtime resolves `xbin/` tiles | 250 JS |
| `workspace-template` shell and admin | hide edit/terminal/source/clone for sealed rows; show status, logs, access | 250 JS |
| `native/` | deep-link and tile-resource handling of `xbin/<sealed>` | 100 Swift |

Total: about 2,300 lines of Go, 600 of JS/Swift, 900 of tests. Suggested
PRs: (1) facility core = this prototype hardened, table empty; (2) data
plane: partitions, stamp, backup; (3) prebuilt in the release bundle;
(4) API filter, `/xbin/system`, `bx`; (5) shell, admin, native.

## 8. What the prototype proves, and what it doesn't

Tests (`internal/boot/sealed_test.go`, in-process boot, `--no-auth`, no
isolation; all pass, 5.7 s):

- `TestSealedComponentServedAndRunFromEmbeddedSource`: a Go component
  declared only by the table is listed with `sealed: true`; its page is
  served with the injection and its module byte-exact from the embedded
  source; its backend is built from the materialized source and answers at
  `/api/xbin/counter/hello` with identity `xbin/counter`, running inside
  `.xbin/sealed/`; `/api/xbin/status` is still xbind's; code tree, code
  file, git log answer 400; a terminal answers 403 for the owner; clone
  and offload answer 400; admin logs answer 200; the workspace `xbin.json`,
  `go.work` and a pre-existing `<ws>/xbin/counter/` directory are
  untouched and never served; a pre-existing `xbin~counter` scope is
  refused the data key; a tampered materialized file is rewritten at the
  next boot; a boot with other embedded code serves and runs the new code
  and removes the old tree.
- `TestSealedIgnoresWorkspaceSaves`: watcher events under `xbin/` reach no
  sealed component.
- `TestSealedNameShadowingAPIRefusesBoot`: a row named `agent` fails the
  boot.
- `TestSealedAgentTemplateHosted`: the agent template from
  `BuiltinTemplatesFS()` is registered at `xbin/wsagent` with no manifest
  error, its grant requests rewritten, its native entry and `API.md`
  resolved, its page and modules served. With
  `XBIN_SEALED_AGENT_BACKEND=1` and `XBIN_GOCRYPTFS` set (run once, by
  hand): its encrypted sqlite is provisioned, the backend builds in
  11.6 s, starts and answers.

Also run, all passing: `go test` of `internal/{registry,auth,deps,obs,
util,server,runner,broker,boot,confine,apicheck,term,proxy,builtins}` and
the root assets test. `TestNoDirectExec` passes (no new `exec.Command`).
Not run: `make check` as a whole, `make integration`, the UI harness.

Not proven:

- **Isolation.** No test ran under `--isolate`: the sandboxed build (the
  extra read-only bind of the tree), the sandboxed backend, and the tokens
  and origins asset modes are untested for a sealed component. The code
  paths are the pinned-checkpoint ones, which have their own tests.
- **Partitions.** Not exercised (they need isolation), and blocked by the
  `ComponentPathOK` checks in `deploydata.go` until those admit a sealed
  scope.
- **Prebuilt.** The lookup is written; no bundle was built with a
  `bin/sealed/` directory.
- **The stamp, downgrade, data-only backup, default bindings, the
  `/components` filter**: designed here, not built.
- `make check` and `make integration` were not run (a spike; `gofmt`,
  `go build ./...` and `go vet` of the touched packages are clean).

## 9. Risks

- **Namespace crowding under `xbin/`**: API routes, pages (`/xbin/…`),
  identities (`xbin/cron`) and now components share one prefix. The boot
  check covers API routes; pages and deep links are a naming convention
  only. A sub-namespace would end it (§11).
- **Authorization is spread out.** There is no single gate: terminal
  level, `ComponentPathOK`, `Broker.IsAdmin`, and the deployments
  `Plane.judge` each guard some surfaces. The prototype closes the ones
  found; production needs a table test that walks every registered route
  with a sealed path and asserts the expected refusal, so a new route
  can't forget.
- **`PUT /access` can still record terminal level on a sealed path**
  (`orgsapi.go:594-612`); it grants nothing after the clamp, but the UI
  would show it. Refuse it at write.
- **The materialized tree is trusted because `.xbin` is.** A host user
  with xbind's uid can change it between boots; the boot verification
  rewrites it, and that user already holds xbind's secrets. Serving
  straight from memory would remove the copy for the frontend; it needs
  the three static openers generalized from `*os.File` (about 150 lines)
  and buys little while backends need files on disk anyway.
- **Template drift**: workspace mode lives in the template's source; a
  template change can break the sealed agent. Its tests run in the
  template's suite; the release should also start the prebuilt binary
  once (a smoke test in `publish-release.sh`).
- **Release size and time**: +16 MB per arch, plus about 35 s of build per
  arch on the release machine (cross-compiled, pure Go).
- **Go version skew in dev**: a source build uses the host's Go; the
  template needs go 1.26.0. Prebuilt removes this in production.
- **Old scaffolds showing the tile** until the `/components` filter
  lands; do not put a row in the table before PR 4.

## 10. The fallback

The engine as a library in a confined xbind worker would save the
registry, serving and authorization work (about 1,400 of the 2,300
lines), and would cost:

- 91k lines of template Go (and goja, wazero, pdfium, excelize, sqlite)
  linked into or shipped beside xbind and run with a hand-built sandbox,
  instead of the tile sandbox that already confines exactly this code;
- a second implementation of per-person data, encryption and vault
  (partitions give all three), of limits and OOM handling, logs, cron and
  bus deliveries, idle stop, and the frontend's identity (frame tokens);
- nothing for the hosted-instance manager and the inference gateway,
  which the owner wants on the same facility.

The facility is the cheaper of the two by a wide margin, and the numbers
above (under 600 lines to get a component served and run, 53.5 MiB per idle
instance, 16 MB per bundle) do not argue for revisiting it.

## 11. Decisions needed from the owner

1. **The name.** `xbin/agent` collides with `/api/xbin/agent/*`. Options:
   `xbin/wsagent` (matches the code name; my default), or a sub-namespace
   for all sealed components (`xbin/app/<name>`), which also ends the
   crowding risk at the cost of longer URLs.
2. **The word.** Keep "sealed component", or rename in code and UI
   ("system component", "built-in") before it spreads, since "sealed" is
   the vault's word.
3. **Prebuilt only in production?** My default: yes; a release bundle
   without the binary fails to start the component with a clear error,
   and only `--dev` compiles.
4. **The data-key squatter rule** (§3.2): the sealed scope takes the key,
   a same-key workspace scope loses its resources with a manifest error,
   and pre-existing data at the key holds the component instead of being
   adopted.
5. **Visibility default** (Q4): omit sealed rows from `GET /components`
   unless asked, plus an xbind-served `/xbin/system` page. The alternative
   is to list them always and accept old shells showing a tile with dead
   menu items.
6. **Without `--isolate`**: the workspace agent is unavailable (my
   default), or runs as one shared unpartitioned instance.
7. **What admins may change on a sealed component**: my default is
   access levels (read, write), disable/hide, limits, and bindings of the
   slots the table declares; no extra grants beyond the table's
   capabilities.
8. **Backups**: sealed components' data joins the existing tile backup
   schedule as data-only archives (my default), or stays out until a
   later PR.

## Decision record

`plans/DECISIONS.md` D227 as it stood on 2026-11-15, verbatim: the rules
with their reasoning. (The rounds its last bullets summarise are in full
under §Review rounds below.) The log keeps the rules in brief.

- **D227 — Sealed components: code that is always the running release's
  (2026-11-14; issue #51).** The owner, on where the workspace agent runs:
  a sealed system component, as a general facility ("will be useful later
  when we begin a hosted xbin offering": a component managing the hosted
  instance, a platform inference gateway), with "a very clean auto-update
  story (eg code always = builtin template code)". Spike and findings:
  `plans/wsagent/s1-sealed.md`. The facility ships with an **empty table**;
  `xbin/wsagent` is registered by the workspace agent's own work.
  - **A compiled-in table declares them** (`internal/sealed`): name (path
    `xbin/<name>`), embedded source, partition mode, the interface slots an
    admin may bind, the grants and capabilities the release gives it
    (`Capabilities.Delegate`, read by the delegation primitive through
    `sealed.Lookup`), its data format. Nothing in a workspace declares one,
    and what a workspace `xbin.json` says about one (a grant from or on it,
    a binding of an undeclared slot, a binding naming it as provider) is
    not read; the file is never rewritten for it.
  - **No workspace can hold the path.** `xbin` has been a reserved
    top-level name since before v0.1.0: the scan skips it, every creation
    path refuses it, `/c/` refuses it. A name may not shadow an xbind route
    or page: boot refuses, and a test walks the route table. That is why
    the workspace agent is `xbin/wsagent`: `/api/xbin/agent/…` is xbind's.
  - **The code is the binary's.** At boot the embedded tree is rendered as
    a template instance is (`builtins.RenderTree`), hashed, and
    materialized read-only under `.xbin/sealed/<name>/<digest>`, verified
    file by file at every boot; other digests are removed. It is a tile
    whose primary is permanently pinned to that tree: the frontend goes
    through the pinned-checkpoint serving in all three asset modes, with
    the one HTML transform. Serving from memory was weighed and left: the
    backend needs files on disk anyway, and `.xbin` is as trusted as the
    binary beside it.
  - **Prebuilt in production** (owner). `make build` and
    `deploy/publish-release.sh` produce `bin/sealed/<name>-<digest>` per
    architecture (`xbind sealed-build`); the digest in the name is the
    rendered source's, so a build of other source is never run. Only
    `--dev`/`--no-auth` compile from the tree (against no workspace module
    and no line of the root `go.work`). Measured for the agent template:
    33 s cold on two cores, 60 MB (16 MB stripped and compressed in a
    1.9 GB bundle), against 780 MB of caches and a module download per
    workspace.
  - **Holds, not fallbacks.** A component that can't run is held with a
    reason its API, `/xbin/system` and `bx system` show: `no-build`,
    `foreign-data`, `newer-data`, `stamp`, `needs-sandboxes`.
  - **The data key and the stamp.** `util.ScopeKey` maps `xbin~x` and
    `xbin/x` to one key. The sealed scope always holds it (a same-key
    workspace scope loses its resources, D118's refusal); and
    `data/sealed/<name>.json` says the data at the key is the component's
    and in which format. Claimed at the first boot while nothing sits at
    the key — data found there unclaimed holds the component and is never
    read, moved or deleted (owner). Raised by the first admitted start of
    a release with a higher format; a stamp above the running release's
    format holds it (a downgrade). One mark for the whole component:
    partitions migrate as they start. A restore is refused the same way
    and never lowers it.
  - **Partitions** work for a sealed scope (the partition key checks admit
    one); the table's mode is the only one. Without `--isolate` a
    partitioned component is held, except that a development xbind runs
    one global instance (owner).
  - **Never a target** (the delegation spike's threat T3): its roles,
    provided interfaces and public endpoints are dropped, so no grant,
    binding or route reaches it or a person's partition of it; its
    resources, secrets and registrations pass only its own credentials.
  - **What an admin may change** (owner): who has read access (the levels
    above read don't exist on it, for admins too), enabled/disabled, its
    limits, the bindings of its declared slots. Everything else is the
    table's. Each of the 300-odd `/api/xbin` routes has a row saying what a
    sealed path gets (`server/sealedclass.go`, a gate before the handler;
    guards in `internal/apicheck` and `internal/boot`), as deployments and
    partitions have theirs.
  - **Compat.** Listings leave sealed rows out unless asked (`?sealed=1`),
    so a shell, an admin console or an app that predates them never shows
    a tile with a dead terminal; `/xbin/system` is the surface that is
    always there. Backups are data only.
  - **Words.** "Sealed component" in code, plans and builder docs (the
    owner's term); people read "system component". No user-facing sentence
    puts it beside the vault's sealed state, and `docs/sealed.md` says how
    the two differ.
  - **Second round (2026-11-15; the owner's answers;
    `plans/wsagent/s1-sealed.md` §Review rounds has the reasoning).** An
    access entry that names a sealed path above read is refused where
    entries are written; a pattern is clamped. People's-partition
    operations and data-only backups stay an admin's and read no
    person's data. A row may bind a slot to a builtin provider by
    default (`Defaults`); an admin's binding or unbinding wins. An open
    page across a release: the page carries its code's id and its first
    stale call is refused (`409`, `X-XBin-Reload`), which then sends the
    `reload` event — not a signal at boot, which has nobody to reach.
  - **Review rounds three to six (2026-11-15)** — two independent
    security reviews, then meeting delegation (D228) and agent sessions,
    notices and previews. The full record, with what was rejected and
    why, is `plans/wsagent/s1-sealed.md` §Review rounds; the rules that
    came out of them:
    - *The route gate is an early answer, not the boundary.* A body can
      read differently to the gate and to a handler, so every refusal is
      made where the work is done, on the value acted on — and in
      `registry.MutateWorkspace`, the one writer of the workspace file.
      `TestSealedBoundaryWithoutTheGate` drives every refused, self and
      handled row with the gate off; a route without a row refuses a
      sealed path at runtime.
    - *One spelling.* The sealed test asks the canonical form
      (`util.SealedPath` over `util.CanonPath`); principals carry
      canonical components; the API's edge refuses an odd spelling of a
      sealed path. Tiles' forgiving spellings are left alone (compat).
    - *A sealed component's frame token is a viewer's*: its page runs in
      any reader's browser, so that credential reaches its own backend,
      files, renewal, `whoami` and the events socket — plus a holder's
      delegation API and the exact GET paths its row names
      (`FrameReads`). Only the instance credential acts as the component.
    - *Providers are the release's to name* (`Spec.Providers`), enforced
      when a binding is set, written and read.
    - *Restore is a write path*: only from a sealed archive that names
      it, under a backup key this workspace created (not an imported
      bundle); never while held; no member is read out of its backup.
      Disaster recovery is the workspace's own directory restored.
    - *What runs is verified at every start* (`SealedVerify` in the
      spawn, non-latching): the tree against the release's bytes, the
      prebuilt backend against `bin/sealed/sealed.json`; pages are
      served from memory. Names are at most 18 characters and `xbin~*`
      is reserved, so no tile shares its data key.
    - *The reload signal* goes only to the person whose frame was stale.
    - *Delegation.* Holders, their notice path and which row is the
      agent xbin's chrome frames are the table's (`Capabilities.Delegate`,
      `Notify`, `Capabilities.Agent`: one row at most; boot installs all three).
      A person's agent does nothing in a sealed component, holder or
      not; the two route tables class each other's routes, each pinned
      by a golden. No preview shows one. Under `--no-auth` a holder's
      single instance is the implicit owner's delegate, never the root
      token. Left as found: the delegation package says "sealed system
      component" in a few refusals.
    - *Found on master too, fixed in commits of their own:* a restore
      registered cron jobs naming other components; `res:/…` targets
      looped forever; a terminal opened at any path its caller had
      terminal level on (`cwd=.xbin`, `data`, `homes`), bound read-write
      — it now opens only at, or inside, a registered tile; in-process
      shutdown left backends running.
  - **Not done here.** No entry point in the shell, the admin console or
    the app (the workspace agent's UI work adds them). The real-binary
    downgrade suite has nothing sealed to downgrade until a row exists.

## Review rounds

The record `plans/DECISIONS.md` D227 points at: what each round after the
owner's answers found, what was chosen and what was rejected. Moved here
verbatim from the decision entry (it had outgrown the file's size guard);
the entry keeps the rules.

- **Second round (2026-11-15; the owner's answers).**
  - *Explicit entries.* An access entry that names a sealed path above
    read is refused wherever entries are written (the users store: one
    entry, a user's map, the defaults, a request, an owner); only a
    pattern that reaches it is clamped to read.
  - *What an admin sees.* People's-partition operations and data-only
    backups stay an admin's, under the rules of any partitioned tile, and
    none of them reads a person's data: resources, secrets and
    registrations pass only the component, a partition restores into the
    same person's partition, a person's log needs their share, and a
    single file is never read out of a sealed component's backup
    (`POST /restore {file}` is refused for one). `docs/sealed.md` lists
    both sides, and says what an admin's control of accounts still means.
  - *Default bindings.* A row may bind a declared slot to a builtin
    provider (`Defaults`: the workspace agent's `llm` → `xbin:llm`). The
    registry applies it when the manifest is read, while the file has no
    entry for the slot and the provider is available (the connector is
    configured); nothing is written. An admin's binding wins, and so does
    their unbinding it, kept as an empty entry — otherwise an unbind
    would be undone by the next read. A backend that started before the
    default applied restarts onto it (its bindings are its environment).
  - *An open page across a release.* Chosen: a version check on the
    page's next request, which then sends the existing `reload` event —
    not a signal at boot. A boot-time signal has nobody to reach (the
    events sockets died with the old xbind, and a frame that reconnects
    late, or a page in a background tab or the app, misses it), and it
    needs xbind to remember the previous release's id; the check needs
    no state, because the page carries its code's id (`xbin-code` meta,
    `X-XBin-Code` / `xbin-code=` on its calls to its own API) and the
    first stale call is refused (`409`, `X-XBin-Reload`) before it
    reaches the new backend. That refusal is what rules out the stale
    mix; the `reload` event it triggers is only how embedded frames get
    to the new page (`bx-frame` already reloads on it, re-minting what a
    sandboxed frame needs), and a top-level page reloads itself. Callers
    that name no code are not pages and pass. Old clients (a cached
    `xbin-client.js`) send no id and behave as before this change.
- **Third round (2026-11-15; an independent security review of
  58f54aee).** The boundary did not hold: the route gate was the only
  refusal on many routes, and it read a request's body beside the
  handler.
  - *The gate is not the boundary.* `{"component":"xbin/x",
    "component":null}` read as nothing in the gate's map decode and as
    `xbin/x` in a handler's struct decode. No amount of re-parsing fixes
    that class, so the rule moved to where the work is done, on the value
    acted on: `allowRes`/`allowAt` and `vaultAccess` (no admin shortcut
    onto a sealed component's resources and secrets), the cron and bus
    registration handlers, `grantMutation`, the owner transfer, the
    partition mode/review/consent handlers, `guardNewComponentTree`
    (every creation path), source and PR resolution, interface instances
    and ingress hosts, chrome, tile reports, sandboxes, the deployments
    plane's `judge`, resolvers and record writer — and
    `registry.MutateWorkspace`, the one writer of the workspace file,
    which undoes and refuses a change that adds a row about a sealed
    component the file may not hold. The gate stays as the early answer
    with the reason, refuses a body that gives one of its names twice
    and reads a bounded prefix. `TestSealedBoundaryWithoutTheGate` runs
    every refused and self row with the gate off.
  - *A frame token of a sealed component is a viewer's.* For a tile the
    frame token carries the tile's authority, because page and backend
    are one author's code. A sealed component's page runs in any
    reader's browser, so that token — any reader could mint one — passed
    the "its own alone" rows and reached its kv, blob, bus, cron and
    subscriptions. Chosen: a rule on the credential, where a credential
    becomes a principal (`authed`, `authedStatic`, the tile-origin path):
    a sealed component's frame principal reaches its own backend, its
    own files, its renewal, `whoami` and the events socket. Rejected:
    checking `Via` in each handler (the same per-handler trust the gate
    had), and minting no frame token (the page needs one to reach its
    backend and to be attributed). Consequence for the workspace agent:
    its frontend takes live updates from its backend, never `xbin.bus`.
    Only the instance credential acts as the component (`sealedActor`).
  - *Providers are the release's to name* (the coordinator's decision). A
    slot's provider sees what the component sends it; a model provider
    reads every person's conversation and steers what the component does
    with their access. So `Spec.Providers` lists, per slot, what an admin
    may bind beside the default, enforced when a binding is set, when the
    file is written and when it is read (a hand-edited row to another
    provider reads as no binding, and no default takes its place). The
    workspace agent's model slot allows `xbin:llm` alone.
  - *Restore is a write path.* A sealed component restores only from a
    sealed archive — encrypted under a backup key the workspace's vault
    holds and authenticated end to end — whose manifest names it; a
    plain tar is the archiver tile's word. It is archived only sealed
    (nothing in plaintext-vault mode, as a person's partition), a held
    component takes no restore, an archive carries no deployment record
    for it, and no member is read out of its backup (refused in
    `extractMember` itself). Two bugs this found exist on master too and
    are fixed in commits of their own: a restore registered cron jobs
    naming other components, and `res:/…` targets looped forever.
  - *What runs is verified at every start.* The materialized tree is
    compared with the release's bytes before each start and rewritten;
    pages are never served from it but from memory (an anonymous file
    per open, so the handlers keep serving an `*os.File`). The prebuilt
    backend is the file `bin/sealed/sealed.json` lists, with that
    SHA-256, checked at boot and before each start. Not done as asked:
    the SDK tree and go version are recorded in the manifest and
    compared by `sealed-build --check`, not folded into the source
    digest — the SDK is a separate module xbind neither embeds nor can
    recompute at runtime, and the file name must be one xbind can
    predict. The manifest sits beside the binaries: it does not stop
    someone who can write the install prefix.
  - *Smaller ones.* The workspace `importMap` no longer reaches sealed
    documents. Level readers that looked at the stored map go through
    the cap (`CanTerminal`, `whoami`), and `CanWriteTile` is false on a
    sealed path. A sealed name is at most 19 characters so `CompKey`
    never cuts its readable part (a tile path could otherwise be ground
    into the same 32-bit-hash key). In origins mode the cookie of a
    sealed component's own origin is the same viewer (found by the test
    written for it). `XBIN_E2E_REQUIRE=1` makes a skipped isolated test
    a failure.
- **Fourth round (2026-11-15; a second independent review, of
  182bc1af).** Two shapes recurred, so the classes were fixed.
  - *A raw path string tested instead of a canonical component.*
    `/xbin/x` was "no sealed path" to the exact-prefix test and the
    sealed component to the code that trims, joins or resolves — a
    forged restore, a cron row firing at `/api//xbin/x/…`, a frame token
    the viewer rule didn't recognise. Chosen: the sealed test itself
    asks the canonical form (`util.SealedPath` over `util.CanonPath`), so
    every decision point sees every spelling without knowing about
    spellings; principals carry canonical components only
    (`auth.canonicalComponent`, `mintFrame`, a registered component for
    `GET /frame-token`); what stores a component's name takes it as
    xbind stores one (`broker.componentNamed`); and the API's edge
    refuses an odd spelling of a sealed path (path, query, the fields a
    route names a tile in, map keys). Rejected: canonicalising every
    parameter of every route at the edge — tiles' forgiving spellings
    are in the wild (compat), and a rewrite of bodies is the re-parsing
    the first review ruled out as a boundary. Case, percent-escapes and
    look-alike letters are not folded anywhere in xbind, so they name
    other (nonexistent) paths; the tests pin that.
  - *A row class the gate-off test never drove.* It now drives the
    handled rows as every principal kind too, and a route without a row
    refuses a sealed path at runtime (the CI guard stays the first line).
  - *Keys.* An imported key bundle can hold any key under any subject,
    so a sealed target restores only under a backup key this workspace
    created for that archive (`sealedKeyNative`). The cost is stated in
    the docs: disaster recovery of a sealed component onto a new host is
    the workspace's own directory restored, not archiver + key bundle.
  - *Terminals (on master too, its own commit).* A session's directory
    was any path the caller had terminal level on, bound read-write over
    the masks: measured under `--isolate`, an admin's shell with
    `cwd=.xbin` could not read the owner token but replaced it, with
    `cwd=data` rewrote `users.json` and moved the vault, with
    `cwd=homes` wrote into anyone's home. A session now opens only at a
    component-shaped path that is, or lies in, a registered tile.
  - *Verification moved to the spawn.* It ran per proxied request under
    one mutex and latched a hold on any failure. It now runs in
    `startFor` (`SealedVerify`), fails that start alone, and is retried
    by the next; boot still holds a build that is missing, unlisted or
    definitely another file.
  - *The reload signal* counts only from the component's own frame
    principal and goes to that person's sockets (an event whose data
    names its audience), so nobody reloads anyone else's frames.
  - *Smaller ones.* A same-key tile (`xbin~<name>`) declares nothing in
    any deployment and such names are reserved at creation; names are at
    most 18 characters (at 19 the cut form of longer tile paths shares
    the readable key part); admins' event sockets get no sealed bus
    events; personal binds take only the release's providers; a
    partition restore under a data hold restores nothing; one shared
    instance only under `--no-auth`; view-as opens nothing of a sealed
    component; `sealed-build --check` compares OS, architecture and go
    version.
- **Fifth round (2026-11-15; meeting delegation, D228).** The two
  tables classify each other's routes: `PUT /agent-levels` and `POST
  /delegate/approvals` name a tile and refuse a sealed one, `GET
  /agent-levels` never lists one, every other delegation route is
  no-tile (it names calls by method and URL, judged when they run — and
  `broker.DelegateTile`, `WorkspaceAgentGate` and `AgentSessionAllows`
  refuse every sealed component, holder or not: a person's agent acts
  on tiles); `GET /sealed` is `never` for a delegate. The sealed table
  is pinned by a golden of its own. Holders are the table's
  `Capabilities.Delegate`, installed where the table is opened. A
  holder's page keeps the delegation API beside a viewer's list — the
  page is where its person approves, and those handlers take it only as
  that person's signed-in frame — and a row may name further reads
  (`FrameReads`, GET only, validated). Under `--no-auth` a holder's
  single instance is the implicit owner's delegate, as a person, never
  as the root token. Rows gain `Env` and `Notify`. Left as found: the
  delegation package says "sealed system component" in a few refusals,
  where this decision's rule is "system component" for people (fixed
  2026-11-16: those refusals say "system component").
- **Sixth round (2026-11-15; agent sessions, notices, previews).**
  `auth.SetDelegateNotify` is installed beside the holder lookup, from
  the same row: a holder's `Notify`, "" for every other component. The
  holder page's one extra read, `GET /agents/connection?session=`, is
  the fixture holder's `FrameReads` entry — the allow-list matches the
  path exactly and GET/HEAD only, and the handler answers only that
  holder's own frame for its driver. The handle routes, the map, docs
  search and the LLM door's keys are no-tile (a handle's scope narrows
  calls that are still judged when they run). `POST /preview` is
  refused for a sealed tile, in the handler's path before availability
  is asked, and a render's scope never has a sealed path — a screen is
  drawn without it — because a render holds the person's session and a
  system component's page is each person's own to open.
