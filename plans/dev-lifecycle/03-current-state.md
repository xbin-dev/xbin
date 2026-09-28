# 03 — How it works today (the baseline)

> Status: historical (the input to D119 and D127) — the as-is baseline the dev lifecycle changes, every fact re-verified with file:line against master at `5012a92` (part of [plans/dev-lifecycle](README.md))

This document describes what xbin does today in every area the tile dev
lifecycle touches. It proposes nothing. The design is
[05-model.md](05-model.md), and the documents built on it say what changes.

Vocabulary follows [01-glossary.md](01-glossary.md):
- Today's mechanisms keep their existing names: backend generation,
  blue/green swap, env layer, instance token, interface instances.
- The glossary's new nouns (deployment, primary, checkpoint, pinned) appear
  only where a sentence points at what the model changes.

**Baseline.** The baseline is master: D112 is landed, and D113 is designed
([plans/tile-sandboxes.md](../tile-sandboxes.md)).
- The `internal/sbx` registry, VM reservations booked to a tile and
  `GET /api/xbin/sandboxes` are described here as today (§3.3).
- D113's tile-managed sandboxes are not built. Where they matter they are
  marked *designed*.
- cgi no longer exists: it is removed by its own change, which lands first.
- Work on other branches (the sandbox-managers contract) is not part of
  today and is described only where the design meets it (§8).

**References.** Every `path:line` below was checked against master at
`5012a92`. Line numbers drift, so re-check them before editing code.
- The maps in [research/](research/README.md) were taken on master before
  D112 landed. Their `internal/runner/runner.go` references are 3 to 6 lines
  low (the file gained the registry wiring), and their
  `internal/boot/boot.go` references 5 to 15 lines low.
- Where the research and the code disagree, the code wins.

**In one paragraph.** Every tile is one principal, one runner state, one
work tree and one set of path-keyed records. A save rebuilds and swaps its
backend and reloads every open frame. Every restart, whatever its cause,
rebuilds from whatever the work tree holds at that moment. Nothing records or
keeps the code a backend generation runs, and nothing serves a frontend from
anywhere but the work tree. In the model's terms every tile is in the zero
state (P5): one implicit `main`, primary, with live reload attached.

## 1. The save→serve pipeline

Evidence: [research/runner-hot-reload.md](research/runner-hot-reload.md) §2–3,
[research/ui-surfaces.md](research/ui-surfaces.md) §2.

```text
write under <ws>/<tile>/
 └─▶ one recursive fsnotify watcher ─▶ 300 ms debounce ─▶ one batch of paths
      └─▶ watchLoop: Rescan · Provision · pending grants · ingress · deps/ + go.work
           └─▶ per changed component: emit `reload`, then run.Changed(c)
                └─▶ Ensure (single-flight) ─▶ buildAndStart:
                     build-start ─▶ build ─▶ start ─▶ health ─▶ swap ─▶ drain old ─▶ build-ok
                                        └──── any failure ─▶ build-error; the old generation keeps serving
 browsers: bx-frame re-navigates on `reload` (emitted before the rebuild); overlay on build-error
```

| # | Step | What happens | Code |
|---|---|---|---|
| 1 | Watch | One recursive fsnotify watcher over the workspace root (`--workspace`, default `/workspace`). Directories created later are added when they appear. | `internal/watch/watch.go:76-112`, `:145-149`; `internal/boot/config.go:36` |
| 2 | Filter | Ignored files: editor droppings, and xbind's own `.<name>.<random>.tmp` atomic-write temporaries. Ignored directories: a reserved top segment (`data`, `home`, `homes`, `.xbin`, `vendor`, …) and any `.git`, `node_modules`, `deps`, `__pycache__` or dot-directory segment. So a commit alone triggers nothing; a checkout does, because it rewrites files. | `watch.go:46-74`; `internal/util/util.go:67-76` |
| 3 | Debounce | 300 ms, reset by every event; the flush sends one batch. The queue holds 16 batches; a batch that doesn't fit is dropped with a warning. | `internal/boot/boot.go:40-41`; `watch.go:84`, `:151-178` |
| 4 | Rescan and side effects | Per batch: `Registry.Rescan`, `Provision` (resources), `RefreshPending` (new `uses` requests to approvers), ingress reconcile, `deps/` links and the root `go.work`. | `internal/boot/serve.go:161-174` |
| 5 | Map to components | Each path resolves to its longest registered component. Every hit reloads. Every hit restarts its backend, except a change to the native UI entry alone. | `serve.go:175`, `:192-205`; `internal/registry/registry.go:519-536`; `internal/registry/native.go:108-124` |
| 6 | `reload` event | `{type:"reload", component}`, emitted **before** the rebuild starts, with no lifecycle check. | `serve.go:176-178` |
| 7 | `run.Changed(c)` | Marks the state dirty and clears its crash history. Starts a background `Ensure` only if a process exists, is building or last failed, and only if the tile may run. A tile that never started stays dirty until its first request. | `serve.go:179-181`; `internal/runner/runner.go:265-284` |
| 8 | alwaysOn wake | After every batch, `WakeAlwaysOn` starts alwaysOn backends that are down (a new tile, the flag just added). | `serve.go:183`; `internal/runner/alwayson.go:45-61` |
| 9 | `Ensure` | Lifecycle gate, then the fast path `!dirty && cur != nil` returns the current socket. Otherwise one caller builds and the rest wait on `buildDone`. The loop re-checks after each build, so a change during a build causes exactly one more build. The in-flight build is never cancelled, and its already stale generation is swapped in first. | `runner.go:190-240` |
| 10 | `build-start` | Emitted per transition. The env layer emits a second one, with a setup message, when it rebuilds. The status plane clears the tile's reported status on every `build-start`. | `runner.go:288`; `internal/runner/env.go:72`; `internal/obs/status.go:129-142` |
| 11 | Build | Go: one output file `.xbin/build/<CompKey>/bin`, overwritten by every build; confined under `--isolate` (§7.3), a direct `go build` without it. node/python: no build; the "binary" is the entry file inside the tile directory. | `runner.go:365-411`; `internal/runner/build.go:38-97` |
| 12 | Start | Next generation number, socket `g<gen>.sock`, a fresh `XBIN_TOKEN`, env (§5.3). Then the namespace sandbox (or a VM), or without isolation a host process in the tile directory. Then the log file, the registry entry, the token registration and the cgroup leaf. | `runner.go:296-310`, `:413-499` |
| 13 | Health | Dials the unix socket until it answers, the process exits, or the timeout passes: 5 s, or 60 s for a VM backend, tripled when emulated. There is no HTTP check. | `internal/runner/health.go:12-27`; `runner.go:44`, `:315-323`; `internal/runner/vm.go:26`, `:47-55` |
| 14 | Swap | `s.cur = inst`. VM backends with file resources stop the old generation *before* starting the new one instead (no two guests on one sqlite). | `runner.go:300-308`, `:325-327`; `vm.go:124-133` |
| 15 | Drain | The old generation gets SIGTERM in the background and is killed after 30 s (D8). Its token is revoked only when its process exits. | `runner.go:45`, `:328-330`, `:600-621`, `:728-740` |
| 16 | Crash watch, `build-ok` | A goroutine per generation watches for an unreplaced exit (§3). Then `build-ok`. | `runner.go:334-358` |
| 17 | Browser | `/ws/events` fans the event out. bx-frame re-navigates its iframe on `reload` if it is the most specific mounted frame. `build-error` paints the overlay only on the exact `src`; `build-ok` clears it. | `internal/server/server.go:169`, `:592-612`; `web/events-socket.js:26-49`; `web/bx-frame.js:366-379`, `:405-417`, `:825-826` |

Properties this feature depends on:

- **A failed build keeps the old generation.** `build-error` leaves `cur`
  alone, and the fast path then returns the old socket (`runner.go:205-209`,
  `:291-294`). Callers see a 502 carrying the compiler output only when no
  generation is running: the first start, or after a crash or an idle reap
  (`internal/proxy/proxy.go:182-190`).
- **Requests wait during a build.** While the state is dirty or building, new
  requests block in `Ensure` for the new generation. In-flight requests and
  streams stay on the old one until the drain kills it (`runner.go:215-224`,
  `:728-740`).
- **The frame reloads early.** `reload` precedes the rebuild, so the reloaded
  page's API calls wait in `Ensure`, or reach the old generation if the build
  fails (`serve.go:178-180`).
- **Two generations overlap for up to 30 s.** Both hold valid tokens that map
  to the same path (`runner.go:328-330`, `:619`;
  `internal/auth/auth.go:445-455`). D81's engine lock is built for exactly
  this bound (§6.2).
- **Any file edit restarts the backend,** including `index.html` and CSS
  (`serve.go:199-202`). A dependency tile's edit reloads only that tile, even
  though Go builds pull dependencies in through `go.work`
  (`serve.go:192-205`; `internal/deps/deps.go:120`).
- **Compiler output is not logged.** It travels only in the `build-error`
  text and the 502 detail (`runner.go:390-392`; `build.go:90-93`). The tile
  log gets process output and env-layer setup output (`runner.go:471-478`;
  `env.go:88-104`).
- **Nothing records what a generation runs.** Builds pass `-buildvcs=false`
  and no hash of the tree is kept (`build.go:86`). The generation counter is
  in memory and starts again at 1 after an xbind restart (`runner.go:73`,
  `:297`).

## 2. The frontend

Evidence: [research/serving-fabric.md](research/serving-fabric.md) §A,
[research/identity-resources.md](research/identity-resources.md) §3.

### 2.1 Served from the work tree on every request

- **Route.** `/c/` is `withAssetTokens(authedStatic(handleComponentStatic))`
  (`server.go:155`).
- **`handleComponentStatic`** (`internal/server/static.go:70-180`), in order:
  1. `SafeJoin` rejects `..`. `pathAllowed` refuses a reserved first segment
     and any `.git` or `.xbin` segment (`static.go:72-76`, `:347-361`).
  2. The owner is the longest registered prefix (`:330-338`). A non-chrome
     owner needs `CanReadTile`, a `code` grant, or the credential-less
     subresource rule (`:105-115`).
  3. `?native=1` serves the generated native runtime document (`:116-121`).
  4. Strict modes (`--tile-assets=tokens|origins`, D95) go to
     `serveStrictStatic` (`:122-125`; `internal/server/tileassets.go:133`,
     `:169`).
  5. Legacy mode: `openLegacy` tries the `--dev` overlay first (never for
     manifests), then `fsutil.OpenResolved` on the workspace root
     (`static.go:193-219`).
- **Headers.** Every response carries `Cache-Control: no-store` and
  `X-Content-Type-Options: nosniff` (`static.go:153-154`). There is no cache,
  no frontend artifact and no record of which bytes were served.
- **Only the bytes come from the file.** `inject`, chrome-ness, the native
  entry and whether the tile exists at all come from the registry, which is
  the work tree as of the last rescan (`static.go:156-158`, `:313-325`;
  `registry.go:456-474`). The registry reads each `xbin.json` and
  `scope.json` with `os.ReadFile`, which follows a symlink
  (`registry.go:447`, `:458`).
- **No lifecycle gate.** A disabled or hidden tile's page still loads: nothing
  in `static.go`, `tileassets.go` or `native.go` reads the lifecycle state.
  Only its API calls get a 409 (`proxy.go:148-158`).
- **`+` is a legal path character today.** `ComponentPathOK` and the D82
  create rules refuse only reserved names, dot segments and `:`
  (`util.go:97-114`; `internal/broker/policy.go:156-174`). A create request
  is accepted or refused; there is no warning channel (`newTilePathOK`
  returns a verdict and a reason). The model uses `+` as the deployment
  qualifier and refuses it in every new tile name ([05-model.md](05-model.md)
  §7; P17, decided 2026-09-28).
- **`deps/` links are followed on disk.** Legacy mode opens through
  `fsutil.OpenResolved`, which follows symlinks anywhere and accepts the
  result only if the fully resolved path stays inside the workspace root and
  passes `pathAllowed`. A `deps/<name>` link into another tile therefore
  serves that tile's work tree (`static.go:193-219`;
  `internal/fsutil/beneath.go:47-55`).

### 2.2 The one injection (D4)

`headInjection` builds the block inserted right after `<head…>` in every HTML
document of a tile (`static.go:367-395`, `:431-466`):

| Piece | Value | Code |
|---|---|---|
| strict-mode head | tokens mode: `<base>` plus an import-map remap; origins mode: a mode meta; empty in legacy mode | `static.go:434-441`; `tileassets.go:338` |
| import map | workspace map merged with the scope's | `static.go:432`, `:442` |
| `xbin-component` | the tile path | `static.go:461` |
| `xbin-frame-token` | a token, or empty (§2.3) | `static.go:435-436`, `:462` |
| `xbin-interfaces` | the tile's resolved http slots | `static.go:444-448` |
| `xbin-sandbox` | the CSP sandbox tokens (non-chrome only) | `static.go:453-457` |
| `xbin-ws-origin` | app WebViews only | `static.go:465` |
| client | `<script type="module" src="/vendor/xbin-client.js">` | `static.go:464` |

- `inject:false` serves HTML byte-exact, with the CSP sandbox header and no
  frame token (`static.go:157-175`).
- The native runtime document uses the same `headInjection`
  (`internal/server/native.go:90`).
- Legacy output is pinned byte-for-byte by
  `TestLegacyInjectionUnchanged` (`internal/server/tileassets_test.go:187`).
- `xbin.self` is the `xbin-component` meta (`web/xbin-client.js:50`). Tiles
  address their own backend as `/api/${xbin.self}/…`.

### 2.3 Frame tokens

| Aspect | Today | Code |
|---|---|---|
| Wire | `b64url(component)\|b64url(user)\|exp\|gen\|hmac`, HMAC-SHA256 keyed by `.xbin/secret` | `internal/auth/frametoken.go:39-43`, `:226-238` |
| `gen` | `s.<handle>` (a login session), `u.<epoch>.<n>` (the user's generation), `o.<hash>` (the owner token) (D93) | `frametoken.go:22-38` |
| Lifetime | 15 min; the client renews every 10 min | `static.go:24`; `xbin-client.js:83-93` |
| Who gets one | never a backend principal; the principal must read the tile; and it is a human, the tile's own frame or terminal principal, or a same-tree navigation in which every writer of the navigating page can write the target | `static.go:494-503`, `:555-557` |
| Renewal | `GET /api/xbin/frame-token?component=` with the same rule; carries over the caller's `gen` | `internal/server/api.go:241-252`; `frametoken.go:218-224` |
| Verify | 5 fields, or the legacy 4 fields only if they expire within an hour of boot; any other shape is rejected | `frametoken.go:73-77`, `:252-276` |
| Principal | `{Component, UserID, Via:"frame", Gen, Impersonator}` plus the user's `Access` | `frametoken.go:304-320` |
| Tile origins (D95) | one host label per tile, keyed by the path; the tile cookie resolves to the same frame principal | `internal/auth/assettoken.go:182`, `:215` |
| Bootstrap | bx-frame's credentialless frames get a token minted from the shell's cookie context, for `/c/<src>/` | `web/frame-info.js:72-100` |

### 2.4 How open frames and clients react

- **bx-frame** acts only when `e.component === src` or starts with `src + '/'`.
  On `reload` it reloads only if it is the most specific mounted frame
  covering the component, and it re-navigates (a credentialless frame
  re-mints its bootstrap token) (`bx-frame.js:366-373`, `:405-417`;
  `events-socket.js:40-49`).
- **The shell** refetches `/api/xbin/components` on every `reload` and
  `grants` event, whatever the component
  (`workspace-template/shell/bx-shell.js:195`).
- **The shell's status handling** toasts every transient `status` event and
  tints the browser tab from the worst status across *all* keys it holds, so
  it doesn't filter status events by known tiles (`bx-shell.js:1257-1266`,
  `:1278-1289`; `workspace-template/shell/shell-kit.js:83-91`).
- **The admin tile and llm-gw** refresh on any `reload` or `build-ok`
  (`workspace-template/tiles/admin/admin.js:157-159`;
  `builtin-tiles/llm-gw/llm-gw.js:100-102`). The code panel matches by
  prefix (`web/bx-code.js:267-272`).
- **The native app** reloads the most specific open tile covering the
  component (`native/ios/App/Model/WorkspaceEvents.swift:239-245`;
  `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:160-166`).
  It parses build events (`Events.swift:56-58`), but only its tests read them
  (`native/ios/Packages/XbinCore/Tests/XbinCoreTests/ClientEventsTests.swift:10-14`),
  so the app shows no build errors.

Because of these matches, and because `reload`, `build-*` and `status` reach
every subscriber (§9.2), the model keeps non-primary activity off every old
event type and delivers its new `deployments` event by audience
([05-model.md](05-model.md) §8).

## 3. Every path that (re)starts a backend

Evidence: [research/runner-hot-reload.md](research/runner-hot-reload.md) §2c,
[research/inbound-edges.md](research/inbound-edges.md) §0 and E6.

### 3.1 The paths

| Path | Trigger | How it reaches `Ensure` | Code |
|---|---|---|---|
| Lazy start | the first request, stream or dial to a stopped backend | the `/api/<tile>/` proxy (cron, bus and archiver dispatch ride it too), public ingress, L4 `DialInto`, a net client's `ensureProvider` | `proxy.go:180-182`; `internal/proxy/ingress.go:56-58`; `internal/runner/ingress.go:32-37`; `internal/runner/netmux.go:55-63` |
| Save | the watcher (§1) | `Changed`, then a goroutine `Ensure`, only if a process exists, is building or failed | `serve.go:179-181`; `runner.go:265-284` |
| Idle reap | every minute: no active connection, no request for 30 min, not alwaysOn | stops `cur` and marks the state dirty; the next request is a lazy start | `runner.go:46`, `:809-831` |
| Crash | an exit that no swap replaced | marks the state dirty (the next request rebuilds) and calls `afterExit`, which restarts only alwaysOn tiles. 3 exits within 30 s set a sticky error and a `build-error` until the next save. | `runner.go:47-48`, `:334-356`; `alwayson.go:100-103` |
| alwaysOn | boot (the last step), every watcher batch, vault unseal or init, lifecycle enable; after an exit, a backoff of 1 s doubling to 5 min (reset after 10 healthy min); at most 2 starts at once | `WakeAlwaysOn` or `afterExit`, then `aoStart`, then `Ensure`; skipped while a failure is sticky | `boot.go:719-722`; `serve.go:183`; `internal/broker/broker.go:214-236`; `internal/broker/lifecycle.go:103-104`; `alwayson.go:23-28`, `:45-137` |
| Grant change | approve or revoke of `res:`, `gpu:`, `cap:net-admin`, `cap:containers` | `OnGrantChange(from)`, then `Changed` | `broker.go:648-666`; `boot.go:508-512` |
| Binding change | `POST`/`DELETE /bindings` | `OnGrantChange` for the tile and for its old and new providers (not for terminators, on exposes) | `internal/broker/netfn.go:824-846` |
| Interface instances | a provider's `PUT /iface-instances` | `OnGrantChange` for every consumer bound to it | `netfn.go:1115-1129` |
| Transfer (D39) | `POST /owner` | `OnGrantChange` for the tile and for net providers whose roster changed | `internal/broker/transfer.go:267-277` |
| Provider nudge | a net-provider generation starts with clients | `Changed` on every client, so each re-splices | `runner.go:581-597` |
| Re-enable | lifecycle set to `enabled` | alwaysOn tiles start at once; the rest on the next request (`Ensure` is gated) | `lifecycle.go:99-105`; `runner.go:194-199` |
| xbind restart | boot | runner state is in memory: a tile's first `state()` is a fresh dirty entry, the generation counter starts at 1 and the run dir is wiped; alwaysOn tiles start at boot, the rest lazily | `runner.go:164-185`, `:297`; `internal/runner/rundir.go:24-31` |

### 3.2 What every start builds from

Every path ends in `buildAndStart`, then `build(c)`, where `c` is the registry
entry at that moment (`runner.go:287-290`). Callers look it up afresh
(`serve.go:195`; `boot.go:509`; `runner.go:592`; `alwayson.go:125`;
`proxy.go:130`). So each start builds:

- **from the work tree as it is now.**
  - Go compiles `c.Dir` against the generated root `go.work` into the single
    `bin` (`build.go:85-89`; `runner.go:372`, `:385-392`).
  - node and python run the entry file in `c.Dir`, in place
    (`runner.go:394-407`). The tile directory is bound read-only at its own
    path and is the working directory (`runner.go:641`, `:682`; without
    isolation `cmd.Dir = c.Dir`, `:462`).
- **with the manifest as it is now.** `runtime`, `entry`, `setup`,
  `alwaysOn`, `vm`, `uses` (which become `XBIN_RES_*`) and `interfaces`
  (which become `XBIN_IFACE_*`) come from the registry's one manifest per
  path (`registry.go:52-99`, `:456-474`;
  `internal/broker/resources.go:71-144`). So does the inbound surface:
  `template`, `exposes`, `expose.roles`, `provides` and `chrome`. A save
  changes what the tile exposes and provides, and how it is framed, at the
  next rescan.
- **with the resources the work tree declares.** `Provision` runs at boot
  and after every rescan over the root manifest's and each `scope.json`'s
  `resources` (§1 step 4). It creates what is declared and deletes nothing
  that stops being declared (`resources.go:36-66`).
- **with the env layer of the current `setup` hash.** The layer is rebuilt
  when the script or the rootfs changes, and only the newest hash is kept
  (`env.go:30-46`, `:52-131`, `:133-145`).

No path reuses an earlier artifact. A dirty state always runs `build` (Go's
content-addressed build cache only makes it fast), and there is no "last good
build".

What stops a backend. Every stop takes a path and stops its one `cur`:

| Stop | Code |
|---|---|
| lifecycle disable, hide or offload | `lifecycle.go:99-102`, then `StopBackendSafe` (`internal/broker/backup.go:443-447`), then `Runner.Stop` with a 5 s deadline (`runner.go:745-759`) |
| restore, offload | `backup.go:439`, `:455` |
| vault seal | stops every tile that uses a file resource, then unmounts (`internal/broker/resenc_wire.go:150-162`) |
| xbind shutdown | `run.StopAll()` (`serve.go:143`; `runner.go:762-781`) |

### 3.3 What a generation runs in: the launch spec and the sandbox layer

Evidence: [research/sandbox-visibility.md](research/sandbox-visibility.md).
This is the shared layer that [05-model.md](05-model.md) §12 changes one
layer above tile-managed sandboxes.

| Piece | Today | Code |
|---|---|---|
| Mode | `vm` with `--isolate`, a manifest `vm` and go/node/python; `namespace` with `--isolate`; otherwise `host` (a plain process as xbind; `SpawnUser` may drop to a scope uid) | `internal/runner/sbx.go:36-56`; `vm.go:43-45`; `runner.go:437-469` |
| Binds | the tile directory read-only at its own path; the run dir read-write; the gateway socket; same-scope file resources read-write at the path `XBIN_RES_*` names (`Src == Dst`); the Go binary read-only at `/run/backend`; granted GPUs | `runner.go:640-667`; `internal/runner/binds.go:28-54` |
| Root | an overlay of the rootfs, with the env layer as an extra lower. Backends set no `Upper`, so the init uses a private tmpfs and nothing outside resources persists. | `runner.go:672-686`; `internal/sandbox/sandbox.go:137-142` |
| Namespaces | fresh user, mount, pid, ipc and uts namespaces per generation, plus net unless host networking; the egress relay is per generation | `internal/sandbox/launch_linux.go:122-126`; `runner.go:506-553` |
| Bind order | depth-sorted and stable within a depth, so a later bind at the same path shadows an earlier one; `Src ≠ Dst` is supported | `sandbox.go:109-128`; `internal/sandbox/init_linux.go:391-423` |
| Mountpoints | `mountBind` creates each mountpoint with `os.MkdirAll` under the new root and mounts by path, so both follow a symlink that an earlier bind put there; the init never uses `openat2` | `init_linux.go:391-423` |
| VM | `vmApply` refuses with no VM manager, the admin switch off or VMs unavailable (marked as refusals for the registry), and errors when `setup` is set; it reserves memory and records the reservation under the generation's socket path | `vm.go:59-101`, `:113-122` |
| VM books | `vm.Manager.Reserve(owner, mem)` checks the global count and budget and books the VM to `owner`: the runner passes the tile path, terminals pass their tile; `UsedBy()` reports per tile | `internal/vm/policy.go:127-182`; `vm.go:80`; `internal/term/vm.go:85` |
| cgroup | one flat leaf `comp-<CompKey>` per tile, shared by blue/green; defaults 2 GiB memory, `max(512, 8×CPUs)` pids; a VM generation sets `memory.max` to twice its guest plus overhead; every generation's exit tries to remove the leaf (an rmdir that fails while the other generation lives) | `internal/cgroup/cgroup_linux.go:89-125`, `:214-219`; `boot.go:565-578`; `runner.go:489-493`, `:613-615`; `internal/runner/vm.go:103-111` |
| Registry entry | one `sbx.Entry` per running generation: ID `backend:<CompKey>:g<gen>`, `Tile`, `Mode`, `Gen`, `PID`, `Leaf = CompKey` (when cgroups are on), VM memory, vCPUs and accel. Added after start, removed in the wait goroutine. Start and health failures and shim or init exits go to the failure ring. | `internal/runner/sbx.go:58-99`; `runner.go:487`, `:602-603` |
| Registry package | in-memory entries plus a 64-entry failure ring, identical failures coalesced for 10 min; `Filter{Tile, User, Kind}`; kinds `backend \| terminal \| agent \| tile`, with `tile` and `Parent` reserved for D113; a nil registry is valid | `internal/sbx/sbx.go:22-121`, `:131-234` |
| `GET /sandboxes` | admin only: rows (entry, owner, current stats), VM disks, failures, and `health.isolation` and `health.vm` including `usedBy`. The scope function already notes "a tile listing its own" as the D113 case. | `internal/boot/sandboxes.go:31-50`, `:104-154` |
| Terminals, agents | listed by session id with `Tile` = the session's tile; VM and restricted sessions get their own leaf `term-<id>` | `internal/term/sbx.go:50-64`, `:73-83` |
| Tile-managed sandboxes (*designed*, D113) | siblings of kind `tile`; definitions in `data/sandboxes.json`, state in `.xbin/sbx/<CompKey>/<name>/`, a policy in `.xbin/sandboxes/policy.json`; admission through `Reserve(owner=tile)`; a nested per-tile cgroup deferred | `plans/tile-sandboxes.md:53-60`, `:217-229`, `:290` |

## 4. State keyed by tile path or scope

Evidence: [research/identity-resources.md](research/identity-resources.md)
§2 and §5, [research/data-plane.md](research/data-plane.md) §2–3.

### 4.1 Key functions

- **`CompKey(path)`:** the first 24 characters of the path with `/`→`~`, then
  `-` and 8 hex digits of its sha256. It is one-way and keeps socket paths
  under 108 bytes (`util.go:134-144`). The 8 hex digits are 32 bits, so a
  chosen path can collide with another; the model's new stores key by
  `<TileKey>`, a 128-bit hash, and every existing store keeps its key
  ([05-model.md](05-model.md) §3).
- **`ScopeKey(scope)`:** `/`→`~`, and `""`→`workspace`. It is not injective:
  `apps~cal` and `apps/cal` collide (`util.go:125-132`; side finding #7).
- **Resource ids:** `res:<scope>/<name>`, resolved by the longest declared
  scope prefix; `res:workspace/<name>` addresses the root manifest
  (`broker.go:323-366`). A scope is the nearest ancestor holding
  `scope.json` (`registry.go:447-454`, `:488-491`).

### 4.2 Principals, tokens and runner state

| Store | Key | Where | Code |
|---|---|---|---|
| Instance tokens | token → tile path | memory | `auth.go:219`, `:445-462`; `runner.go:421`, `:488`, `:619` |
| Terminal tokens | token → (tile, user) | memory | `auth.go:220`, `:240-245`, `:466-507`; `internal/term/term.go:338` |
| Frame tokens | stateless claims (tile, user, exp, gen); generation handles in `.xbin/frame-gens.json` | HMAC under `.xbin/secret` | `frametoken.go:226-238`; `internal/auth/framegens.go:42` |
| Tile origin | `TileHostID(tile)` | derived | `assettoken.go:182` |
| Runner state | `states[path]`: generation counter, `cur`, dirty, building, crashes, active, lastReq | memory | `runner.go:70-82`, `:158`, `:176-185` |
| alwaysOn | `backoff`, `upSince`, `pending` by path; the reaper exemption reads the manifest | memory | `alwayson.go:30-41`; `runner.go:819` |
| Stats | series by tile path | memory | `internal/runner/stats.go:71-77` |
| Net links | provider path → client path (`#slot` for lan-ingress) → fd | memory | `netmux.go:14-32` |
| VM reservations | per generation, by socket path; books by owner = tile path | memory | `internal/runner/vm.go:37-40`, `:93-99`; `internal/vm/policy.go:132-168` |
| Sandbox registry | `backend:<CompKey>:g<gen>`, `Tile` = path, `Leaf` = CompKey | memory | `internal/runner/sbx.go:63-67` |

### 4.3 Runtime artifacts

| Artifact | Key | Where | Code |
|---|---|---|---|
| Go build output | CompKey | `.xbin/build/<CompKey>/bin`, one file | `runner.go:372` |
| Go caches | CompKey (isolated); shared without isolation | `.xbin/cache/tile/<CompKey>/{go-build,mod}`; `.xbin/cache/go-build` | `build.go:43-45`; `runner.go:388` |
| Log | CompKey | `.xbin/log/<CompKey>.log`, append-only, `--- gen N start ---` markers, every generation plus env setup | `runner.go:471-478`; `env.go:88-90` |
| Sockets | CompKey + generation | `<rundir>/<CompKey>/g<gen>.sock`; the run dir is a tmpfs `xbin-<hash>/run`, symlinked from `.xbin/run`; `gateway.sock` is shared | `runner.go:414-419`; `rundir.go:24-48`; `serve.go:36` |
| cgroup leaf | CompKey | `comp-<CompKey>` | `cgroup_linux.go:89`; `runner.go:489-493` |
| Env layer | CompKey + 16-hex hash of the `setup` script and the rootfs (its path and `os-release` mtime) | `.xbin/env/<CompKey>/<hash>/{upper,work,.ok}`; only the newest hash kept | `env.go:30-46`, `:133-145` |
| Terminal layer | CompKey | `.xbin/term/<CompKey>/`; one running session may mount it | `term.go:479-485`, `:487-498`, `:612` |
| Terminator door | CompKey of the terminator | `<rundir>/igw-<CompKey>.sock` | `internal/ingress/forwards.go:39` |
| Tile-managed sandboxes (*designed*) | tile + name | `data/sandboxes.json`; `.xbin/sbx/<CompKey>/<name>/` | `plans/tile-sandboxes.md:53-60` |

### 4.4 Data plane

| Store | Key | Where | Code |
|---|---|---|---|
| Vault (D30) | CompKey of the tile | `data/vault/<CompKey>.json`, the whole map sealed with the vault key | `internal/broker/vault.go:22-47` |
| kv | bucket = resource id `res:<scope>/<name>` | one bbolt file `data/kv.db`; values AES-GCM under the label `kv:<bucket>` | `resources.go:159-202`; `resenc_wire.go:189-229` |
| File resources (filesystem, sqlite, blob) | (ScopeKey, name) | ciphertext `data/resources-enc/<ScopeKey>/<name>`; mount `.xbin/resenc/<ScopeKey>/<name>`; password label `fs:<ScopeKey>/<name>` | `internal/resenc/resenc.go:83-118`; `resenc_wire.go:43`, `:134-145` |
| Resource declarations | the `resources` map of the root manifest and of each `scope.json`, keyed by name | the work tree, parsed at every rescan; no check on the name's characters: it is joined into the ciphertext and mount paths, and `resenc.Ensure` runs `os.MkdirAll` on both before `gocryptfs -init` | `registry.go:188-191`, `:447-454`; `resenc_wire.go:175-187`; `resenc.go:84-91`, `:132-160` |
| `XBIN_RES_*` paths | same scope only | the mount path, or `<mount>/<name>.sqlite` | `resources.go:81-97`; `resenc_wire.go:94-100` |
| AppArmor | mounts only under `<ws>/.xbin/resenc/**` on installed systems | installer rule | `deploy/install.sh:812-813`, `:909` |
| cron resource dir | ScopeKey | `data/resources/<ScopeKey>` (empty) | `resources.go:38-45` |
| Bus | topic `res:<scope>/<name>/<topic>` | memory | `resources.go:410-437` |
| Disk quota | ScopeKey | plaintext plus ciphertext trees | `broker.go:185-198` |
| Tier-2 uids | scope | `.xbin/uids.json` | `internal/broker/uids.go:38` |
| Encryption hold | the tile's declared resources | gates spawning while a mount is down or the vault is sealed | `resenc_wire.go:106-129`; `boot.go:517-519` |

### 4.5 Registrations

| Store | Key | Where | Code |
|---|---|---|---|
| Cron jobs | `component\x00name`; the component is forced to the caller | `data/cron-jobs.json` | `internal/broker/cron.go:31-38`, `:76-78`, `:108`, `:233-236` |
| Bus subscriptions (D85) | `component\x00name`; at most 64 per component; forced to the caller | `data/bus-subscriptions.json` | `internal/broker/bussubs.go:50`, `:55-62`, `:158-162`, `:398-401` |
| Interface instances (IFACE-7) | `ifaceInstances[provider][id]`, replaced wholesale per call | root `xbin.json` | `registry.go:218-223`; `netfn.go:1102-1111` |
| Ingress hosts (ING-2) | `ingressHosts[comp]`, replaced wholesale per call | root `xbin.json` | `registry.go:224-228`; `internal/broker/ingressfn.go:564-574` |

### 4.6 Observability and people-facing state

| Store | Key | Where | Code |
|---|---|---|---|
| Status | component; cleared on any `build-start` for it | memory | `internal/obs/obs.go:25`; `status.go:107-113`, `:129-142` |
| Notify budget (D94) | the tile, or tile + person for frontends and terminals | memory limiter | `internal/push/api.go:356-363`; `internal/push/push.go:131` |
| Muted tiles | tile paths | `data/push/push.json` | `internal/push/store.go:71`, `:100-112` |
| Notify link | `c/<tile>/<link>` | in the push | `internal/push/api.go:368` |
| Prefs | (user, component) | `data/prefs/<CompKey(user)>/<CompKey(component)>.json` | `internal/obs/prefs.go:33-48` |
| Agent history (D75) | (user, CompKey(tile)); newest 20 kept | `data/agent-history/<user>/<CompKey>/<id>.json` | `internal/term/history.go:27`, `:49-55` |
| Code PRs (D48) | CompKey of the target | `data/prs/<CompKey>/<n>/` | `internal/broker/prs.go:86-87` |
| Backups | archive key = CompKey; schedule by component | the archiver tile; `data/backup-schedule.json` | `backup.go:45`; `internal/broker/backup_cron.go:26` |
| Builtin provenance (BU-2) | unit id and install path | `.xbin/builtins.json`, `.xbin/builtins/<id>/` | `internal/builtins/updates.go:31` |

### 4.7 Governance

| Store | Key | Where | Code |
|---|---|---|---|
| Lifecycle | `lifecycle[comp]`, `lifecycleAt[comp]`; absent = enabled | root `xbin.json` | `registry.go:229-235`, `:347-365`; `lifecycle.go:80-95` |
| Grants | rows `{from, target, role, approvedBy, approvedAt}` | root `xbin.json` | `registry.go:210` |
| Bindings | `bindings[comp][slot]`, including `@archive` and the `"*"` default | root `xbin.json` | `registry.go:217`; `backup.go:31-41` |
| Owners (D24) | `owners[path]` = `user:<id>` or `org:<id>` | `data/users.json` | `internal/users/orgs.go:169-172`, `:187-220` |
| Access entries | `User.Tiles`, `Org.Tiles`, `defaultTiles`, access requests `{user, tile}` | `data/users.json` | `internal/users/users.go:71`; `orgs.go:100`; `internal/users/requests.go:16-24` |
| Policy ceilings (D20) | rows matched against the path, plus owner-derived rows | users store | `orgs.go:1211-1220`; `internal/broker/policy.go:32-87` |

The root `xbin.json` is re-marshalled through the `WorkspaceManifest` struct
on every mutation, so a binary that doesn't know a top-level key drops it
(`registry.go:586-595`).

### 4.8 What removes path-keyed state

Nothing removes path-keyed state when a tile's directory disappears; there is
no delete, rename or move API.
- **Cleared on re-create:** only cron jobs and bus subscriptions. Creating a
  tile calls `forget(path)` on both (`internal/broker/create.go:188-190`;
  `cron.go:148-164`; `bussubs.go:220-235`).
- **Refused on re-create (D82):** a non-admin can't create at a path that
  still carries grant rows, bindings, interface instances, ingress hosts, a
  vault file or users-store entries (`internal/broker/policy.go:211-258`).
  The path's current owner is exempt (`:219-221`).
- **Neither:** `lifecycle[path]` (side finding #6), scope data, backup
  schedules, PRs, prefs, agent history, logs and layers.

## 5. Inbound edges

Evidence: [research/inbound-edges.md](research/inbound-edges.md) §1–2,
[research/serving-fabric.md](research/serving-fabric.md) §B.

### 5.1 Every edge

| Edge | Names its target by | Funnel | Code |
|---|---|---|---|
| `/api/<tile>/…` on the console, the gateway socket and tile origins | the URL path, as the longest registered prefix; the principal comes from the credential | `Resolve`; template, backend and lifecycle gates; `Policy`; `identify`; then `Ensure` and `Track`; one pooled transport per `g<N>.sock` | `server.go:163`, `:563-590`; `serve.go:34-54`; `proxy.go:128-235` |
| HTTP interface binding (backend) | `XBIN_IFACE_<SLOT>_URL = http://xbin/api/<provider>[<instance prefix>]`, set at spawn | over the gateway into row 1 on the provider; the binding is the call grant | `resources.go:99-123`; `netfn.go:286-311`, `:341` |
| HTTP interface binding (frontend) | the `xbin-interfaces` meta | a frame-token call into row 1 on the provider | `static.go:444-448` |
| Grant-based call | `/api/<target>` | row 1, with `grantedRole` deciding the role | `broker.go:478-490` |
| Cron tick | the stored `(component, path)` | a synthetic `POST /api/<comp><path>` as `xbin/cron`, through the proxy | `cron.go:166-208` |
| Bus push delivery | the stored `(component, path)` | a synthetic POST as `xbin/bus` through the proxy; the reader grant is re-checked per delivery | `bussubs.go:125-135`, `:337-362` |
| Archiver call | `bindings[comp]["@archive"]`, else `bindings["*"]["@archive"]` | `PUT /api/<archiver>/archive/<CompKey>` as the owner, through the proxy | `backup.go:35-45`, `:379-388`; `boot.go:521` |
| alwaysOn | every registry component with the flag | `WakeAlwaysOn`, then `aoStart`, then `Ensure` | `alwayson.go:45-84` |
| Public HTTP ingress (runtime listener) | the Host header, then `Route{Component}` from bindings plus `ingressHosts` | `IngressLookup`, then `ForwardIngress`: lifecycle 503, strip `X-XBin-*` and cookies, `From: ingress`, then `Ensure` and `Track` | `serve.go:66-72`; `ingressfn.go:104-146`; `proxy/ingress.go:31-95` |
| Terminator forward door | one socket `igw-<CompKey(terminator)>.sock` per terminator | the same `ForwardIngress` | `forwards.go:39`; `boot.go:531-541` |
| L4 streams and hairpin | one listener per (component, slot, proto, listen, port); the hairpin VIP | `run.DialInto`: `Ensure`, `Track`, then `dialCurrent`, which reads `s.cur` | `internal/ingress/streams.go:102`, `:185`, `:221`; `boot.go:531`, `:544`; `runner/ingress.go:32-87` |
| Stream interface (tile to tile) | a relay forward `stream:<provider>:<port>`, computed at the consumer's spawn | `hostDial`, then `DialInto(provider)` | `resources.go:131-135`; `ingressfn.go:313-327`; `runner/ingress.go:108-127` |
| Net-provider splice, lan-ingress | `NetTarget(c)` and `NetLinksFor(c)` at the client's spawn; the net-link key (provider, client path) | `ensureProvider` runs `Ensure` on the provider; the client reads `netmux.get` directly | `runner.go:511-524`, `:564-580`; `netmux.go:14-63` |
| Grant, binding, interface-instance and transfer restarts | the path the change names | `OnGrantChange`, then `Changed`, then `Ensure` | `boot.go:508-512` |
| Provider nudge | the roster's names | `Changed` | `runner.go:581-597` |
| Watcher | changed paths mapped to components | `Changed` | `serve.go:175-181` |
| Terminals and agent sessions | `/ws/term?cwd=<tile>`, `POST /term/sessions {cwd}`; the token is the tile's element principal | their calls to `$XBIN_URL/api/<tile>` are row 1, as self | `term.go:224-235`, `:338`; `internal/server/agentapi.go:28`; `auth.go:466-507` |
| Frontend documents | `/c/<tile>/…`: bx-frame `src`, `xbin.url`, links, the native app, push taps | the static plane (§2); **no runner** | `static.go:70-180`; `frame-info.js:85` |
| Notify link | `c/<tile>/<link>` inside a push | the frontend row | `internal/push/api.go:368` |
| Bus publish | a resource, not a tile | `/ws/events` subscribers filtered by their reader grant, plus the bus push row | `resources.go:410-458`; `server.go:603-609` |

### 5.2 Path-keyed runner entry points besides `Ensure`

`Ensure` is the one call that starts a backend. It is not the only runner
entry point keyed by the tile path:
- `Track(path)` counts active connections for the reaper
  (`runner.go:246-261`).
- `dialCurrent` reads `s.cur` of the path's state directly
  (`runner/ingress.go:49-87`).
- The net links are keyed by provider and client path (`runner.go:519`,
  `:559`, `:575`).
- `Stop`, `StopAll`, `Status`, `Inspect`, the reaper and `isAlwaysOn`
  iterate or index the path-keyed states (`runner.go:745-831`;
  `internal/runner/inspect.go:62`; `alwayson.go:38-41`).
- The proxy pools one transport per generation socket path
  (`proxy.go:74`, `:86-126`).
- The static plane never touches the runner (§2).

### 5.3 What a backend is told and what a callee sees

Evidence: [research/builder-contract.md](research/builder-contract.md) §2.

| Variable | Value | Code |
|---|---|---|
| `XBIN_SOCKET` | `<rundir>/<CompKey>/g<gen>.sock` | `runner.go:414-424` |
| `XBIN_COMPONENT` | the tile path; the SDK's `Self()` | `runner.go:425`; `sdk/xbin.go:82` |
| `XBIN_GATEWAY` | `<rundir>/gateway.sock`, served by the console's own handler | `runner.go:426`; `serve.go:34-54` |
| `XBIN_TOKEN` | this generation's instance token, 48 hex characters | `runner.go:421`, `:427` |
| `XBIN_RES_<NAME>` | per granted resource: a directory or `.sqlite` path for same-scope filesystem and sqlite, otherwise the canonical `res:` id; nothing for cross-scope files | `resources.go:71-98` |
| `XBIN_IFACE_<SLOT>_URL`, `_INSTANCE`, `XBIN_IFACE_<SLOT>` (multi) | `http://xbin/api/<provider>[<prefix>]` per http binding | `resources.go:99-123` |
| `XBIN_IFACE_<SLOT>_ADDR`, `_IP`, `XBIN_INGRESS_FORWARD_URL`, `XBIN_LAN_INGRESS` | stream, lan-ingress and terminator wiring | `resources.go:124-142` |
| Daemon env | isolated: the rootfs `PATH`, locale, `TZ`, proxy variables; host mode: everything but `XBIN_*` | `internal/runner/hostenv.go:21-46` |

The SDK builds vault URLs from `Self()` (`sdk/xbin.go:265`, `:300`). A
terminal gets `XBIN_COMPONENT`, its session's `XBIN_TOKEN`, `XBIN_URL` and a
git rewrite (§7.5) (`term.go:737-795`). Agent sessions are created through
the same path and get the same env (`term.go:556-564`).

What the callee sees (`proxy.go:263-305`):
- **Stripped first:** every inbound `X-Xbin-*`, xbind's cookies and any
  bearer (`proxy.go:275-284`). `?frame=` is consumed and never forwarded
  (`proxy.go:199-203`).
- **`X-XBin-From`:** `p.From()`, which is the tile path, `owner`,
  `user:<id>`, `xbin/cron` or `xbin/bus` (`proxy.go:285`;
  `auth.go:196-207`). Public ingress sets `ingress` and
  `X-XBin-Ingress-Host` instead (`proxy/ingress.go:46-54`).
- **`X-XBin-Role`:** the role `Policy` decided (`proxy.go:286`).
- **`X-XBin-User`, `X-XBin-User-Level`:** only when a human is attributed
  (session, frame or terminal principal) (D29) (`proxy.go:290-300`;
  `boot.go:496-502`). Instance-token, cron and bus calls carry no user.
- **`X-XBin-Viewed-By`:** in a view-as session (D64) (`proxy.go:301-303`).
- The SDK exposes these as `CallerInfo` (`sdk/xbin.go:107`). Nothing tells a
  callee which of two backends of one caller is calling.

## 6. Single-backend assumptions

Evidence: [research/inbound-edges.md](research/inbound-edges.md) §3,
[research/data-plane.md](research/data-plane.md) §3.

What a second backend of the same tile path, running beside the first, would
collide with or fire twice. Today this happens only during the 30 s
blue/green drain (§1).

### 6.1 The xbind side

| # | Assumption | With a second backend of the same tile running | Code |
|---|---|---|---|
| 1 | one runner state per path, with one `cur` | it has nowhere to live: `Ensure`, `Track`, `Stop`, `Status` and `Inspect` see one; `/tile-status` takes the first matching row | `runner.go:70-82`, `:158`, `:176-185`, `:784-807`; `internal/boot/api.go:101-108` |
| 2 | one socket dir per tile, names `g<gen>.sock`, removed before start and on stop | two counters starting at 1 delete each other's live sockets; the proxy's janitor then evicts the pooled transport | `runner.go:414-419`, `:739`; `proxy.go:117-126` |
| 3 | one Go artifact, bound read-only into the running sandbox | the next build overwrites the file the other backend runs | `runner.go:372`, `:655` |
| 4 | one log file | output interleaves; `/logs` and `bx logs` read it unfiltered | `runner.go:471-478`; `internal/obs/logs.go:69`; `cmd/bx/main.go:511` |
| 5 | one cgroup leaf and cap; a VM leaf sized for two guests | the backends share one memory and pids cap; a third VM guest exceeds the leaf | `cgroup_linux.go:89`; `runner.go:489-493`, `:613-615`; `internal/runner/vm.go:103-111` |
| 6 | VM reservations keyed by socket path | the same socket path overwrites the first reservation, whose give-back is lost | `internal/runner/vm.go:37-40`, `:93-99`, `:113-122` |
| 7 | registry ID `backend:<CompKey>:g<gen>` | equal IDs: the second `Add` replaces the first entry, which vanishes from `/sandboxes` | `internal/runner/sbx.go:63`; `internal/sbx/sbx.go:131-151` |
| 8 | alwaysOn maps and stats by path | one backoff and one series for both | `alwayson.go:30-41`; `stats.go:71-77` |
| 9 | env-layer GC keeps one hash per tile | two different `setup` scripts delete each other's layer | `env.go:133-145` |
| 10 | instance token maps to the path; `X-XBin-From` is the path | the broker and every callee can't tell the backends apart | `auth.go:219`, `:445-462`, `:589-591`; `proxy.go:285` |
| 11 | self-calls: `XBIN_COMPONENT` is the path, `Policy` makes self admin, `Ensure(comp)` reaches the one state | the second backend's self-call lands on the first, as admin | `runner.go:425`; `sdk/xbin.go:82`; `broker.go:482-484` |
| 12 | vault per path; value reads need `Component == path` and `Via == "instance"` | both read and write the same secrets | `vault.go:45-47`, `:188` |
| 13 | cron key `component\x00name`; DELETE acts on the caller's own component | the second rewrites or deletes the first's jobs; ticks reach the one state | `cron.go:108-130`, `:200-208`, `:257-270` |
| 14 | bus-subscription key `component\x00name`, 64 per component | the same | `bussubs.go:158`, `:186-216`, `:437-449` |
| 15 | interface instances: the whole map replaced per PUT, every bound consumer restarted | each start of either rewrites the map and restarts every consumer | `netfn.go:1102-1129` |
| 16 | ingress hosts: the set replaced, ingress reconciled | one unpublishes the other's hostnames | `ingressfn.go:564-581` |
| 17 | status: one record per path, cleared by any `build-start` for it | one backend's build clears the other's status; reports overwrite each other | `obs.go:25`; `status.go:107-113`, `:129-142` |
| 18 | notify: the tile comes from the principal; one budget per tile | both push as the tile and spend one budget | `internal/push/api.go:287-300`, `:356-363` |
| 19 | `build-*`, `reload` and `status` events carry only `component` | frames, shells and the app can't tell the backends apart (§2.4) | `runner.go:288-358`; `serve.go:178`; `status.go:123` |
| 20 | one net link per (provider, client path); a provider start closes old fds and restarts its clients | two provider backends fight over the roster; two client backends splice one fd | `netmux.go:22-32`; `runner.go:554-597` |
| 21 | one manifest per path, read from the work tree | whatever runs gets the work tree's runtime, entry, uses, interfaces, alwaysOn, vm and setup | `registry.go:456-474` |
| 22 | backups carry cron and bus registrations by path, and restore re-adds them | a restore re-registers them for the path | `backup.go:60-108`, `:219` |

### 6.2 The tile side: the agent template (D81) and the messaging bridge (D86)

Shipped templates rely on the same assumptions. Each template instance is a
separate tile made from the template (D50), so a template-side change reaches
an owner only when they pull it.

| Pattern | Why it exists | With a second backend of the same tile running | Code |
|---|---|---|---|
| Engine lock: an exclusive `flock` on `<db>.engine` for the process lifetime, plus an `engine_epoch` fence bumped at takeover | the successor boots during the drain and waits in `flock`; built for a blue/green overlap of at most 30 s on one database | on one database, one backend blocks in `flock` indefinitely while its HTTP handlers keep writing inbox rows | `builtin-templates/agent/_backend/owner.go:1-14`, `:33-62`; `main.go:64-66`; `engine.go:114-128` |
| SIGTERM hand-off | stop driving at once so the successor takes over | — | `main.go:70-78` |
| Self-hold: `GET /api/<Self()>/engine/hold` | keeps its own backend from the idle reaper while runs have work | the request routes by path to the one state | `owner.go:152-184` |
| Resume job: on shutdown with pending work, cron `resume` calls `/tick` every minute; takeover deletes `resume` and `heartbeat` | recovery across stops | one backend's takeover deletes the other's recovery job; its shutdown aims `/tick` at the path | `owner.go:186-238` |
| Start-time re-registration of `sched-<id>` cron jobs and `trig-<id>` bus subscriptions (unregistered when disabled) | converge after edits and restarts | ids from two databases collide and overwrite or delete each other | `main.go:67-68`; `schedule.go:166`, `:202-216`; `triggers.go:441-470` |
| Adapters are identified by `X-XBin-From` (`adapterOf`) for hello, outbox, acks and push triggers | trust only the verified caller | two bridge backends are one adapter to the agent | `channels.go:190`, `:223`; `outbox.go:254`, `:335`; `triggers.go:394`, `:433` |
| The bridge is `alwaysOn` and keeps its platform secrets in the tile vault | hold the platform connection open | two backends open two connections on one token and consume one outbox | `builtin-templates/agent-messaging-bridge/xbin.json:20`; `_backend/platform.go:72-73`, `:93-100` |

The builder docs tell every tile to re-assert state at start:
- `Subscribe` "at every start is fine" (`sdk/xbin.go:350-355`;
  [/docs/resources.md](/docs/resources.md), line 166);
- report status again "on startup" (`workspace-template/AGENTS.md:535`).

Other shipped tiles with the same shape:
- webhooks forwards pushes with its own path as `From`
  (`builtin-tiles/webhooks/backend/main.go:176-183`);
- traefik polls `/ingress-routes` every 15 s
  (`builtin-tiles/traefik/backend/main.go:35`, `:87`).

## 7. Git today

Evidence: [research/git-confine.md](research/git-confine.md).

### 7.1 Repositories

- **The workspace root** gets `git init -q` and no commit, via `confine.Git`
  in `InitWorkspace` (`internal/boot/workspace.go:114-118`).
  - It runs from `stepWorkspace` (`boot.go:149-153`), before the `confine`
    step configures isolation (`boot.go:96-99`). On a fresh directory it
    therefore runs directly.
  - It ignores `.xbin/`, `data/`, `home/` and `homes/`
    (`workspace-template/gitignore:2-5`). Tile directories are not ignored
    (side finding #17).
- **Every tile is its own repository.**
  - `gitInitComponent` runs `init -q -b main`, `add -A` and commits
    "initial commit" as `xbin` (`internal/broker/code.go:96-108`).
  - `EnsureComponentRepos` does it for every component without a `.git`
    (`code.go:113-119`), at boot and after every broker structure change
    (`boot.go:411-418`). The watcher never calls it.
- **The tile's `.git` is inside the work tree.** It is bound read-write into
  the tile's terminals and agent sessions (`internal/term/binds.go:57-58`).
  It is untrusted (D77, D78).
- **The static plane refuses `.git` paths** (`static.go:355-359`).
- **Builds ignore git.** `-buildvcs=false` means nothing records a commit
  (`build.go:86`).

How a new tile's repository starts:

| Creation path | Repository | Code |
|---|---|---|
| `POST /create`, `bx new --owner` | fresh "initial commit" on `main` | `internal/broker/create.go:64-68` |
| a hand-made directory, `bx new` without `--owner` | none until the next `EnsureComponentRepos` (side finding #18) | `code.go:113-119` |
| builtin tile import | fresh repository; update provenance lives in `.xbin/builtins*` (BU-2), not in git | `internal/broker/tiles.go:81-99` |
| builtin template instance (D50) | seeded from the template repository, then a `template` remote | `internal/broker/templates.go:139-163`; `internal/broker/templaterepo.go:106-151` |
| workspace template instance | fresh repository, no remote | `templates.go:185-194` |
| `POST /clone` | the original tile's directory including `.git`, then a "fork from <path>" commit | `internal/broker/clone.go:101`, `:130-138` |
| git import | `git clone` keeps `origin`; optional checkout of a ref | `internal/broker/gitimport.go:170-181` |
| restore | `.git` comes back from the backup | `backup.go:110-119` |

### 7.2 Every git operation xbind runs

Every git run goes through `internal/confine`. xbind never pushes, never runs
`git am`, and never merges or checks out branches in a tile (the import ref
aside).

| Subsystem | Commands | Mounts | Code |
|---|---|---|---|
| Workspace init | `init -q` | the root, read-write | `workspace.go:114-118` |
| Tile repository init | `init -q -b main`, `add -A`, `commit` | the tile, read-write | `code.go:96-108` |
| Code panel reads | `remote get-url origin`; `log --numstat`; `log <ref>`; `symbolic-ref`, `rev-parse --verify`; `diff --no-ext-diff --no-textconv HEAD`; `show <rev>` | the tile, **read-write** (the helper binds read-write even for reads) | `code.go:64-80`, `:126`, `:264`, `:341`, `:369`, `:375`, `:403-412` |
| Clone | `add -A`, `commit` in the new tile | the new tile, read-write | `clone.go:132-138` |
| Git import | `ls-remote --symref` (no directory), `clone`, then `checkout <ref>` | the target, read-write; host network; `~/.ssh` read-only at `/root/.ssh` | `gitimport.go:62-68`, `:83`, `:170`, `:176` |
| Template repositories | materialize: `init`, `add`, `commit`, `update-server-info`; seed a template instance: `init`, `fetch <tpl> main`, `update-ref`, `reset --mixed`, `add`, `commit`; the `template` remote; the update check: `remote get-url` on every readable component, `rev-parse`, `rev-list`, `merge-base --is-ancestor` | each command's directory read-write (the template repository, the new tile, or each checked component); the template read-only for the fetch | `internal/broker/templaterepo.go:56-93`, `:106-151`, `:176-201` |
| Builtin updates | PR mode: a temporary repository in host tmp, two commits, `format-patch`; merge mode: `merge-file -p --diff3` | the temporary directory | `internal/builtins/updates.go:733-747`, `:835` |
| D77 agent-session diffs | a private bare git dir in host tmp, `--git-dir=<private> --work-tree=<tile>`: `add -A --ignore-errors`, `write-tree`, `diff-tree`; honours the tile's `.gitignore` plus default excludes | the private dir read-write, the tile read-only; 8 s timeout | `internal/term/agentdiff.go:40-49`, `:89-124`, `:270-284`; `agentdiff_full.go:103` |
| Other confined tools | `go build` (§1); `mkfs.erofs` for VM images | per job | `build.go:85-89`; `internal/vm/image.go:62-65` |

### 7.3 `internal/confine`

**API** (`internal/confine/confine.go`):
- `Configure(rootfs)` and `Isolated()` (`:47-60`). `Configure` runs in the
  boot step `confine`, before the registry and broker steps (`boot.go:96-102`,
  `:587`).
- `Net`: `NetNone` (the default, an empty network namespace), `NetInternet`
  (egress through a relay confine starts), `NetHost` (`:62-69`, `:189-204`).
- `Cmd{Argv, Dir, Binds, Env, Net, Stdin, ReadOnlyDir, Timeout, MaxOutput}`
  (`:71-84`).
  - `Dir` is always bound **at its own path**, read-write unless
    `ReadOnlyDir` (`:218-224`).
  - `Binds` takes arbitrary `sandbox.Bind` values, including `Src ≠ Dst`.
    The helpers `RO`, `RW`, `Mask` and `MaskOpen` bind at the same path
    (`:239-248`).
  - The default timeout is 2 min; the default output cap is 64 MiB per
    stream (`:123-132`).
- `Result{Stdout, Stderr}`, `*ExitError{Code, Stderr}`, and `ErrUnavailable`
  when the sandbox can't start (`:86-114`).

**A run** (`confine.go:119-216`):
- An overlay of the rootfs with a throwaway upper; entry `/usr/bin/env
  <argv>`; `PATH`, `HOME=/tmp` and `LANG` set; `Unprivileged` (no
  capabilities, the syscall block-list) (`:145-157`).
- Each run writes a spec file to the host's temp dir and re-execs xbind as
  `__sandbox-init` (`internal/sandbox/launch_linux.go:24`, `:105-127`).
- On timeout the sandbox's PID 1 is killed (`confine.go:180-183`).
- Isolation off: the tool runs directly, with xbind's env minus `GIT_*`
  (`:140-143`, `:229-237`). Isolation on and the sandbox fails:
  `ErrUnavailable`, never a host fallback (`:165-168`, `:173-176`).
- confine never sets `Spec.VM`: tool runs are always namespace sandboxes,
  whatever the VM policy says ([research/git-confine.md](research/git-confine.md) §3).

**Buffering.** Output is fully buffered and capped (`confine.go:133`,
`:206-207`, `:250-265`). Nothing streams out; only `Stdin` can stream in.

**Cost.** D78 measured about 45 ms per run with fuse-overlayfs and about
17 ms with a kernel overlay ([../DECISIONS.md](../DECISIONS.md) D78).
Per-request counts from the research: `/git/activity` makes up to about 6
runs; `/templates/updates` at least one per component; an agent tool call
2 diff runs, plus 3 when it reports a diff. A diff run slower than 8 s turns
diffs off for that session (`agentdiff.go:41`).

**Git hardening** (`internal/confine/git.go`):
- Flags: fsmonitor off, hooks off (`core.hooksPath=/dev/null`), untracked
  cache off, `safe.directory=*`, `protocol.ext.allow=never`, commit and tag
  signing off, `core.pager=cat` (`git.go:14-23`).
- Env vars: no system or global config, no prompts, no optional locks,
  `LC_ALL=C` (`git.go:28-34`).
- Wrappers `Git`, `GitRead`, `GitCmd`, `GitBytes` (`git.go:36-68`).
  `GitRead` has no caller outside tests (side finding #21).

**Enforcement.** `TestNoDirectExec` fails on any exec in `internal/` or
`cmd/xbind` outside `internal/confine`, `internal/sandbox` and
`internal/agent/host`, unless the line or one of the two above says
`exec-ok:` (`internal/confine/guard_test.go:13-66`).

### 7.4 Code PRs (D48) and builtin-update PRs (D49)

- **Code PRs are inert broker state** under `data/prs/<CompKey(target)>/`
  (`prs.go:23-30`, `:86-87`).
  - Anyone who can read the target may propose (read = suggest), and so may
    an element holding a `code[:target]` grant (`prs.go:95-104`).
  - Merged or rejected is decided by an admin, the target's own principal,
    or a user with write on the tile (`prs.go:106-111`).
  - xbind never applies a patch. The target's own plane runs `git am` in its
    writable mount, then closes the PR as merged, which is a declaration
    only.
- **Builtin updates in `pr` mode** render the update as a patch series in a
  temporary repository and file it as a PR (`prs.go:335`;
  `updates.go:733`).
- **`replace` and `merge` modes write the work tree directly**
  (`updates.go:504-532`, `:538`). The watcher then reloads and rebuilds the
  tile at once.

### 7.5 The template remote and the terminal's git rewrite

- **The only git server is dumb HTTP for builtin templates.**
  - Repositories live in `.xbin/template-repos/<name>/`, materialized at
    every boot (`boot.go:372`; `templaterepo.go:44-93`).
  - `update-server-info` makes them fetchable over dumb HTTP
    (`templaterepo.go:93`).
  - `serveTemplateRepo` serves the git dir with `http.ServeFile`: GET only,
    no git process, any authenticated principal, any file under the git dir
    that `SafeJoin` accepts (`templaterepo.go:209-227`; the route at
    `internal/broker/templates.go:30`).
- **Template instances get a remote.** `AddTemplateRemote` points a builtin
  template instance's `template` remote at
  `http://xbin/api/xbin/templates/<name>.git`. It writes the remote into the
  instance's `.git/config` with `git remote add` or `set-url`
  (`templaterepo.go:139-151`).
- **The session's git rewrite.** Terminals and agent sessions get
  `GIT_CONFIG_COUNT=2` (`term.go:787-793`):
  - `url.<XBIN_URL>/.insteadOf = http://xbin/`;
  - `http.<XBIN_URL>/.extraHeader = Authorization: Bearer <session token>`.
  - It exists only when the session has an API token and a reachable
    `XBIN_URL`. A session opened without API access (`api=0`, or
    `api: false` for an agent) is minted no token, so it can't fetch
    (`term.go:188-190`, `:334-338`; `internal/server/agentapi.go:99`).

### 7.6 What does not exist

- no smart HTTP (`upload-pack`, `receive-pack`, `http-backend`);
- no push endpoint, no per-tile bare repository, no `refs/xbin/*`;
- no record of the code a tile runs: the running code is whatever the work
  tree held at the last build.

The only bare repositories are D77's per-session private dirs in host tmp
(`agentdiff.go:95-103`). The design adds none for a tile without a record:
its work-tree diff answers 409 and captures nothing
([11-contract.md](11-contract.md) §1.11).

## 8. Authority facts this feature relies on

Evidence: [research/identity-resources.md](research/identity-resources.md)
§1 and §4, [research/serving-fabric.md](research/serving-fabric.md) §B.

| Fact | Detail | Code |
|---|---|---|
| `grantedRole` order | policy ceiling first (D20), then explicit grant rows (best role), then an http binding to the target (the binding is the grant, at the provider's declared role), then a same-scope `uses` entry | `broker.go:418-452`; `netfn.go:286-311` |
| Role order | `reader` < `writer` < `admin` by rank (bus `subscriber`/`publisher` alias the first two). A custom role satisfies another role only through the provider's `expose.implies` graph, so a custom role with no path to `reader` has no reader form. The sandbox-managers work (another branch, not on master) has the agent tile reach `sandbox-manager` providers through the custom role `consumer`; P23 blocks such edges for non-primary deployments. | `broker.go:371-410` |
| Write roles that mean spend | llm-gw's `openai` provide grants `writer`, which its completions need; `reader` only lists models. The agent template reaches it through its `llm` multi slot. | `builtin-tiles/llm-gw/xbin.json:20-23`, `:30`; `builtin-templates/agent/xbin.json:48` |
| Same-scope auto-grant (ND5) | both sides in the same **non-empty** scope; workspace-scope tiles never auto-grant; same-scope targets are exempt from the ceiling's call allow-list | `broker.go:454-460`; `internal/broker/policy.go:49-61` |
| Net slot | `netBinding` resolves the tile's `net` slot (declared in the work tree's `interfaces`) to a builtin (`internet`, `host`, `lan:<cidr>`, `org`, `personal`, a named set) or a provider-tile path, after the ceiling. `NetHostShare` is true for `host`, for `org` when the org ceiling says host, for `personal` when the owner's rules say host, and for a named set whose rules say host. | `internal/broker/netfn.go:35-47`, `:196-210` |
| Self-admin | `Policy` gives `admin` when `p.Component == target.Path`: the tile's instance-token, terminal and frame principals alike | `broker.go:478-484`; `proxy.go:52-60` |
| Cron and bus roles | deliveries carry the role chosen at registration; jobs and subscriptions are forced to the registering tile | `broker.go:485-487`; `cron.go:180`, `:233-236`; `bussubs.go:354`, `:398-401` |
| Admin | the root token or an admin user, or an element granted `xbin` at `admin` | `auth.go:90-94`; `broker.go:466-475` |
| Ceilings (D20) | pattern rows matched against the tile path plus owner-derived rows; asked at every `grantedRole` and at approval | `policy.go:32-87`; `orgs.go:1211-1220`; `broker.go:691-699` |
| Levels | `read` < `write` < `terminal`; `none` excludes; `noTerminal` accounts (D88) are capped at `write` | `internal/users/users.go:39-56`; `internal/users/personal.go:356-368` |
| Element reach | an element principal reaches its own tile always; beyond it, the attributed human's access; an unattributed instance token is self-only | `auth.go:117-160` |
| "Self" is a principal test | gates written as `p.Component == tile` admit the tile's frame principal, and a frame token is minted for **any** principal that can read the tile. Such gates: `/logs`, `/tile-status`, setting status, deciding a PR, self-admin on `/api/<tile>/`. The vault excludes frames, and value reads need an instance token. | `static.go:494-499`; `obs/logs.go:42-47`; `boot/api.go:97-100`; `status.go:83-87`; `prs.go:109-111`; `vault.go:167-170`, `:188` |
| Attribution (D29) | the human's id and level on the callee ride as headers; backends gate in-app on them | `proxy.go:287-304`; `boot.go:496-502` |
| Tile managers (D24/D33) | `mayManageTile`: `canManageUsers`, the tile's user-owner, or an admin of the owning org. `humanID` returns `""` for every element principal, so the owner and org-admin branches never pass on an instance, frame or terminal token. The `canManageUsers` branch does: it admits any principal of a tile holding `xbin` at `admin` or `xbin:users` at `writer` (the admin tile's frames, terminals and agents included). Lifecycle uses `IsAdmin \|\| mayManageTile`, and the broker's `IsAdmin` also admits an element granted `xbin` at `admin`. | `internal/broker/orgsapi.go:73-78`, `:425-443`; `internal/broker/usersapi.go:38-50`; `broker.go:462-475`; `lifecycle.go:28` |
| xbind's API routes | every `/api/xbin/*` route is mounted with `RegisterAPI`, which records the pattern for the route-drift test (`internal/apicheck`, against `openapi.go` and `docs/protocol.md`). There is no classification by principal kind: each handler applies its own gate (admin, tile level, or a self test). | `internal/server/server.go:112-123` |
| Terminals | opening one needs terminal level on the tile; the root terminal is refused; the shell's token is the tile's element principal | `term.go:219-232`; `auth.go:466-507` |
| Code reads (CM-3) | admin, the tile itself, or a `code[:tile]` grant | `code.go:133-164` |
| Grant approval | workspace admins; org admins within the allowance (D26); provider-side org admins (D33); personal owners (D88) | `broker.go:675-682` |
| View-as (D64) | `Principal.ReadOnly()`; the `authed` middleware refuses writes, terminals are refused outright, tile origins do the same; a frame token minted under view-as stays read-only | `auth.go:86-88`; `server.go:229-232`, `:284-287`; `internal/server/tileorigin.go:221`; `auth.go:644` |

## 9. Lifecycle controls and status surfaces

Evidence: [research/runner-hot-reload.md](research/runner-hot-reload.md) §4,
[research/ui-surfaces.md](research/ui-surfaces.md) §3.

### 9.1 Controls

| Control | Who | Effect | Code |
|---|---|---|---|
| `POST /api/xbin/lifecycle {component, state}` | tile managers (§8) | states `enabled \| disabled \| hidden \| offloaded \| offloaded-full`, others rejected. A non-enabled state stops the backend; `enabled` wakes alwaysOn tiles. Offload and restore move data first. Emits `reload`. | `lifecycle.go:22-116` |
| `ShouldRun` | — | `enabled && !EncryptionHold`; gates `Ensure`, `Changed` and alwaysOn | `boot.go:517-519`; `runner.go:197-199`, `:277`; `alwayson.go:50`, `:126` |
| Other gates | — | proxy 409 with `X-XBin-Lifecycle`; ingress 503; routes and streams unpublished; cron ticks skipped; bus deliveries dropped. The static plane is not gated. | `proxy.go:148-158`; `proxy/ingress.go:37-40`; `ingressfn.go:94-96`; `cron.go:171-173`; `bussubs.go:342-344` |
| `bx enable \| disable \| hide \| unhide \| offload \| backup \| restore` | as the API | calls `/lifecycle` and the backup routes | `cmd/bx/main.go:84-101` |
| `POST /api/xbin/restore {component, version?, file?}` | admin | decoded strictly: an unknown field is a 400, so an older xbind refuses a body with a new field | `internal/broker/backup.go:552-560`; `internal/server/api.go:67-76` |
| `bx` exit codes | — | 1 on any error, 2 on usage; the `bx agent` commands that follow a session (`run`, `send`, `attach`, `resume`) add 3 (a refused or failed turn) and 130 (cancelled or detached). Nothing uses 4, 5 or 6. | `cmd/bx/main.go:105-108`, `:175`; `cmd/bx/agent.go:388`, `:627-634`, `:646` |
| Terminal window API select | the session's user (terminal level) | two entries, `🔌 tile API` and `⛔ no API`, for terminals and agent sessions alike; a change restarts the session with `api=0\|1` (agents: `api: false`); `api=0` mints no token | `web/frame-titlebar.js:162-167`; `web/bx-frame.js:768`; `web/bx-terminal.js:602`; `term.go:188-190`, `:334-338`; `internal/server/agentapi.go:99`, `:108` |
| Shell tile menu | tile admins | Enable or Unhide; `⏸ Disable`; `⊘ Hide`; admin sections | `workspace-template/shell/menus.js:109-124` |
| Tile admin popover | tile admins | a lifecycle section with enable, disable and hide | `workspace-template/shell/bx-tile-admin.js:178-194`, `:521` |
| Vault seal | admin | stops file-resource users, unmounts | `resenc_wire.go:150-162` |

[/docs/bx.md](/docs/bx.md) (line 138) documents `bx enable | disable` as
"lifecycle: pause/resume a tile". That is why the glossary reserves the word
for lifecycle disable ([01-glossary.md](01-glossary.md)).

### 9.2 Status surfaces

| Surface | Shows | Who | Code |
|---|---|---|---|
| `GET /tile-status?component=` | `{component, backend, disk, alerts, net}`; `backend` is a `runner.Backend`: state, sandbox mode, VM, pid, generation, restarts, connections, uptime, error, process and cgroup stats. A failed build with the old generation up shows `healthy` with `error` set. | admin, or self | `boot/api.go:87-117`; `inspect.go:26-59` |
| `GET /backends` | `{path: {state, gen, error}}` | admin | `boot/api.go:21-27`; `runner.go:784-807` |
| `GET /runtime` | host info, every `Backend` row, resource usage, stats | admin | `boot/api.go:30-66` |
| `GET /sandboxes` | registry rows, disks, failures, isolation and VM health (§3.3) | admin | `internal/boot/sandboxes.go:34-50` |
| `GET /components` | per tile: `path`, `scope`, `runtime`, `hasIndex`, `template` (a blueprint), `state` (omitted when enabled), `roles` (the manifest's `expose.roles`), `uses`, `deps`, `manifestError`, `owner`, `chrome`, `sandbox`, `native`, `origin`. Every manifest-derived field is the registry's, so the work tree as of the last rescan. | each tile for those who can read it, plus chrome and `code` grants | `internal/server/api.go:130-160`, `:162-198` |
| `GET /logs?component=&tail=&follow=1` | the tile's one log file | admin, self, or terminal level | `obs/logs.go:42-80` |
| `GET` and `POST /tile-report` | the tile's self-reported status | reads filtered to readable tiles; writes by self or admin | `status.go:34-116` |
| Events | `reload`, `build-start`, `build-error`, `build-ok`, `status` | every subscriber (only `bus`, `pr`, `term` and `session` are filtered; side finding #2) | `internal/events/events.go:11-17`; `server.go:592-612` |
| `bx status`, `bx logs` | `/tile-status`; the log **file**, read directly | the caller's token | `cmd/bx/main.go:349`, `:494-530` |
| bx-frame overlay | `build failed — <src>` with the compiler text | viewers of the frame | `bx-frame.js:374-379`, `:825-826` |
| Admin console | the runtime and sandboxes tabs, lifecycle cells | admins | `workspace-template/tiles/admin/tabs/` |

### 9.3 What does not exist

- No per-tile switch on the watcher: it is global, and `changedComponents`
  has no per-tile suppression (`serve.go:175-182`).
- No way to stop a backend while keeping the tile enabled; no backend
  restart route; no `bx restart`, `stop` or `build` (`cmd/bx/main.go:31-101`).
  The only restart route belongs to agent sessions
  (`internal/server/agentapi.go:33`).
- No record of the code a generation runs, no build history, and one
  overwritten Go artifact per tile (`build.go:86`; `runner.go:372`).
- No frontend artifact: `/c/` reads the work tree on every request
  (`static.go:127`, `:193-219`).
- No deployment dimension in any key, token, principal, event, env variable
  or header (§4–§6). `auth.Principal` has no such field (`auth.go:53-84`).
- The lifecycle has five states, and the API rejects any other
  (`lifecycle.go:45-78`). Old shells treat every non-`enabled` state as
  disabled and offer Enable (`menus.js:111-116`).

## 10. Doc drift in this area

Items from [research/side-findings.md](research/side-findings.md) that
touch this feature, re-checked at `5012a92`:

| # | Drift or defect | Code |
|---|---|---|
| 2 | `reload`, `build-*`, `status` and the other unfiltered hub events, including compiler output and tile names, reach every subscriber; only `bus`, `pr`, `term` and `session` are filtered | `server.go:592-610` |
| 9 | `"vm": true` without `--isolate` runs as a plain host process; [/docs/elements.md](/docs/elements.md) (lines 56-61) says it fails with a reason | `vm.go:43-45`; `internal/boot/vm.go:17-26` |
| 10 | instance tokens die at process exit, not at the swap, as D8 and `/docs/elements.md` line 539 say; the runner's own header comment repeats "revoked at swap" | `runner.go:12-13`, `:600-621` |
| 11 | compiler output is not in the tile log; `plans/dev-flow.md:92-93` says it is | `runner.go:390-392`; `build.go:90-93` |
| 12 | `plans/implementation.md:167-184` describes a runner that doesn't exist: a `next` build slot, a global build semaphore, a `/healthz` check, rotated logs tee'd to the hub. Also: its crash breaker is "3 exits in 10 s" (code: 3 within 30 s), and idle reap is "configurable per component" (it is fixed). | `runner.go:43-48`, `:343-349` |
| 13 | a disabled or hidden tile's page still loads; [/docs/overview/14-lifecycle.md](/docs/overview/14-lifecycle.md) (lines 39-41) promises a placeholder | §2.1 |
| 14 | the template remote is readable by any authenticated principal with the tile-scoped token; `plans/templates.md:89,93` says admin-only | `templaterepo.go:209-227` |
| 17 | the workspace repository does not ignore tile subtrees, contrary to CM-1 as amended | `workspace-template/gitignore:2-5` |
| 18 | `bx new <path>` without `--owner` creates no repository until the next `EnsureComponentRepos` | `code.go:113-119` |
| 21 | `runGitIn` binds the tile read-write for read-only commands; `GitRead` is unused | `code.go:64-80`; `git.go:44-47` |
| 22 | `bx logs` reads `.xbin/log/<CompKey>.log` directly, and `.xbin` is masked in isolated tile terminals | `cmd/bx/main.go:511`; `internal/term/binds.go:49-53` |
| 23 | the runner and the watcher have no unit tests; the blue/green swap is covered end-to-end only | [research/delivery-infra.md](research/delivery-infra.md) |

Found while verifying (not in the side findings):
- **Resource names are not validated** (security; needs its own fix). A
  `resources` key in `scope.json` goes unchecked into
  `data/resources-enc/<ScopeKey>/<name>` and `.xbin/resenc/<ScopeKey>/<name>`,
  and `resenc.Ensure` creates both with `os.MkdirAll` as xbind before
  `gocryptfs -init`. A name with `..` segments escapes those directories.
  A tile that roots its scope keeps `scope.json` in its own directory, which
  its terminals and agent sessions can write (`resenc.go:84-91`,
  `:155-160`; `resenc_wire.go:175-187`; `internal/term/binds.go:57-58`). The
  installer's AppArmor block only widens where `fusermount3` may mount; it
  does not limit where xbind creates directories (`deploy/install.sh:811-821`).
- [/docs/elements.md](/docs/elements.md) (line 508) says Go builds use a
  "shared cache". Under `--isolate` the caches are per tile; they are shared
  only without isolation (`build.go:43-45`; `runner.go:388`).
- [/docs/protocol.md](/docs/protocol.md) (the "Filesystem contract" section)
  calls all of `.xbin/` "derived state — safe to delete when xbind is
  stopped". It also holds the terminal layers (`term.go:612`), the tier-2
  uid map (`uids.go:38`) and builtin provenance (`updates.go:31`).

## Divergences from the model

Where [05-model.md](05-model.md) or [01-glossary.md](01-glossary.md) rests
on a statement about today that the code contradicts or leaves incomplete.
Numbers are stable: a resolved item keeps its number and a one-line note.

1. Old shells react to `status` events of any component: resolved by
   ruling 1 (rule C2); non-primary status rides only `deployments`
   (05 §7–§8).
2. Some consumers refresh on any `reload` or `build-ok`: resolved by
   ruling 1 (rule C2); no non-primary activity rides `reload` or `build-*`.
3. `Ensure` is the single funnel only for starts: resolved by 05 §7; every
   path-keyed runner map gains the deployment, with new names beside
   `Ensure` (ruling 21).
4. The run dir is an unnamed key: resolved by 05 §7 (the run dir and its
   sockets gain the deployment) and ruling 5 (non-main registry IDs
   `backend+<name>:<CompKey>:g<gen>`).
5. confine can already bind over `Dir`: resolved by 05 §12 (`DirFrom`; the
   work tree is never mounted; a non-isolated run fails).
6. A per-tile cgroup parent changes other consumers: resolved by the owner's
   answer (separate cgroups per deployment) and 05 §12, which names the
   flat-leaf consumers and keeps the flat leaf for zero-state and main-only
   tiles.
7. "Self" gates would admit readers: resolved by 05 §7 (the current level on
   every request, never the bare self test) and rulings 2 and 13 (P26).
8. Two more restart paths must honour pinning: resolved by 05 §5, whose
   restart list now names interface-instance registration, ownership
   transfer and vault seal.
9. **Today's event filter is wider than 05 §8 says** (wording).
   - The claim: "Today every non-bus event reaches every subscriber."
   - What holds: `pr` goes only to readers of its tile, and `term` and
     `session` only to their owner and admins, besides `bus`. `reload`,
     `build-*`, `status` and `grants` reach everyone (`server.go:592-612`).
   - Recommended fix: 05 §8 says "`reload`, `build-*` and `status` reach
     every subscriber; only `bus`, `pr`, `term` and `session` are filtered",
     and `11-contract.md` adds `deployments` to that per-type switch.
10. **The one-release `+` warning has no D82 precedent** (the glossary says
    "the D82 way").
    - D82's create rules answer yes or no, with a reason; nothing warns
      (`internal/broker/policy.go:156-174`; D82 in
      [../DECISIONS.md](../DECISIONS.md)).
    - Recommended fix: the glossary drops "the D82 way", and
      `11-contract.md` names the channel (for example a warning field on the
      create answer, printed by `bx new` and the New tile dialog).
11. **Resource names need a check on every read, not only a checkpoint's**
    (the model is incomplete here).
    - The claim: 05 §6 and ruling 18 read a checkpoint's `scope.json` with
      `OpenBeneath` and validate its resource names.
    - What holds: the live reload target and every zero-state tile provision
      from the work tree's `scope.json`, whose names nothing validates
      today, and xbind creates directories from them (§4.4, §10).
    - The effect: non-main data namespaces put more path components beside
      today's, so an unchecked name could address another deployment's
      directories.
    - Recommended fix: `08-data.md` applies the same charset check to every
      `scope.json` read, the work tree's included, and `14-implementation.md`
      lists today's fix among its prerequisites.
12. **The fetch remote rides the session token, so "API off" sessions can't
    fetch** (the model is incomplete here).
    - The claim: 05 §3 injects `xbin-deploy` beside today's git rewrite,
      gated to the tile's own sessions (ruling 6).
    - What holds: the rewrite carries the session's bearer and exists only
      when the session has a token. A session without API access has none
      (§7.5).
    - The effect: when the primary is protected and live reload is paused,
      P24 falls to "API off", and then no session can `git fetch
      xbin-deploy`, the first step of flow H.
    - Recommended fix: `06-security.md` and `11-contract.md` either say so
      (the hotfix then starts once live reload is attached to a non-primary
      deployment), or give API-off sessions a credential scoped to the view
      repository alone.
13. **`/components`' `roles` is the manifest's `expose.roles`** (a
    consequence to state).
    - 05 §6 lists `expose.roles` as inbound surface, from the primary's code,
      while ruling 16 keeps `/components`' `roles` on the work tree. Today
      `roles` is exactly `Manifest.Expose.Roles` (`internal/server/api.go:189-191`).
    - The effect: with a pinned primary, the grant dialogs offer the work
      tree's roles, which the primary's code may not implement yet. That
      fits "authority requests track the tile's latest intent", but no
      document says it.
    - Recommended fix: `11-contract.md` states that `roles` may lead the
      primary, and `10-ux.md` says so where roles are offered.
14. **The `template` remote is not an env-injected precedent** (the glossary
    is inexact).
    - The claim: the deploy remote is injected "the `template` remote
      precedent, via `GIT_CONFIG_COUNT`".
    - What holds: `AddTemplateRemote` writes the `template` remote into the
      instance's `.git/config`; only the URL rewrite and the bearer ride
      `GIT_CONFIG_*` (`internal/broker/templaterepo.go:139-151`;
      `term.go:787-793`).
    - Recommended fix: the glossary says the deploy remote, like
      `xbin-deploy`, is injected the way the git rewrite is, and never
      written into `.git/config`, unlike the `template` remote.

## New proposals

None. This document describes today; the decisions it bears on are in
[05-model.md](05-model.md) and its companions.
