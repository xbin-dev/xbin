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
