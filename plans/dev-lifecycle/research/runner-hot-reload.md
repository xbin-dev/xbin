# Runner, watcher, build and reload pipeline

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: explore agent a64c1f33.

I mapped the whole path from files on disk to running code. The manifest has no literal `backend`/`frontend`/`slots` keys; §1 shows where those concepts actually live.

The three facts that matter most for your design:
- **Every runtime identity is the component path.** Principal, runner state, build output, log, sockets, cgroup leaf, vault, grants and lifecycle are all keyed by the path or a hash of it. A second deployment of the same code collides everywhere unless it gets its own key.
- **Every backend start rebuilds from the live working tree.** There is no "last good build" to fall back to.
- **Nothing keeps a pinnable build today.** Frontends and node/python backends run straight from the live files, and Go has one output file per tile that each build overwrites.

Repo root: `/home/magik6k/buxon`.

### 1. Tile on disk

**Where a tile lives.**
- The workspace root is set by `--workspace` / `XBIN_WORKSPACE`, default `/workspace` (`/home/magik6k/buxon/internal/boot/config.go:36`). Installed systems use `/opt/xbin/workspace` (`/home/magik6k/buxon/deploy/xbin.service:48`).
- Component path `apps/foo` is the directory `<ws>/apps/foo/`. The path is the identity; there is no registry file.
- Derived state on disk uses `util.CompKey(path)`: the first 24 chars of the path with `/`→`~`, then `-` and 8 hex chars of sha256 (`/home/magik6k/buxon/internal/util/util.go:137-144`).

**The manifest** is `xbin.json`, parsed as JSONC (D5). The struct is `registry.Manifest` in `/home/magik6k/buxon/internal/registry/registry.go:52-99`.

| Your term | What exists (registry.go line) |
|---|---|
| backend | `runtime` (static, go, node, python, cgi) :53; `entry` :54; `setup` :71; `alwaysOn` :77; `vm` :83 (a bool or `{memory, vcpus}`, `VMOpt` in `registry/vm.go:12-47`) |
| frontend | no key. The presence of `index.html` sets `Component.HasIndex` :377; `inject` :58 opts out of HTML injection (D4); `chrome` :65 |
| native.js | `native` :87 (`NativeOpt`: a path or `false`, never a manifest error; `registry/native.go:22-43`). The convention file is `native.js` (native.go:15), resolved at scan time by `resolveNative` (native.go:140-162) |
| slots | `interfaces` :92, `provides` :93, `exposes` :98, all keyed by slot name. The owner's bindings live in the root manifest as `bindings[comp][slot]` :217 |

Also present: `deps`, `uses`, `expose` (roles), and `template` (a template tile never runs; `HasBackend()` :393-402). The root `xbin.json` is `WorkspaceManifest` (:207-236). It is machine-managed and holds the `lifecycle` map (:231) and `lifecycleAt` (:235).

**Discovery** is `Registry.Rescan` (registry.go:423-497):
- It walks the tree and skips `IgnoredDirs` (`.git`, `.xbin`, `node_modules`, `deps`, `__pycache__`), any dot-directory, and reserved top-level names (`util.go:67-76`).
- Any directory with `xbin.json` or `index.html` becomes a component (:471). Nested components are allowed.
- `scope.json` marks a scope.
- `Resolve` (:519-536) maps a path to the longest registered component prefix; the watcher, proxy and static server all use it.
- Rescan runs at boot (`stepRegistry`, `boot/boot.go:233`), on every watcher batch (`boot/serve.go:163`), and after broker structure changes.

### 2. Hot reload

#### 2a. The watcher (`/home/magik6k/buxon/internal/watch/watch.go`)
- One recursive fsnotify watcher covers the whole workspace (`New` :76). New directories are added when created (:145).
- Debounce: `watchDebounce = 300ms` (`boot/boot.go:40`). The timer resets on every event (:151-158), then `flush` sends one batch of paths.
- The channel holds 16 batches. If the consumer falls behind, a batch is dropped with a warning (:173-177).
- Ignored files (`ignoreFile` :46-59): `.swp`, `~`, `.#*`, `4913`, `.goutputstream*`, `.<x>.tmp`.
- Ignored directories (`ignoreDir` :61-74): `data/`, `home/`, `homes/`, `.xbin/`, `vendor/`, and any `.git`, `node_modules`, `deps`, `__pycache__` or dot-directory segment.

The consumer is `watchLoop` (`boot/serve.go:161-185`). For each batch it:
1. Rescans the registry, provisions resources, refreshes pending grants, reconciles ingress, and regenerates `deps/` links and the root `go.work`.
2. Calls `changedComponents` (:192-205) to map paths to components.
3. Publishes a `reload` event for each, and calls `run.Changed(c)` unless the change is native-entry-only.
4. Calls `run.WakeAlwaysOn()`.

**Any file edit in a tile restarts its backend**, including `index.html` and CSS; only the native entry is exempt (`boot/watch_native_test.go:57-64`). Editing a dependency tile only reloads that tile, not the tiles that use it, even though Go builds pull it in through `go.work`.

#### 2b. The build step (`Runner.build`, `/home/magik6k/buxon/internal/runner/runner.go:359-405`)

**Go**
- Output is `.xbin/build/<CompKey>/bin` (:366). There is one output file per tile, overwritten by every build.
- With `--isolate`, `buildConfined` (`runner/build.go:38-97`) runs `go build -buildvcs=false -o out entry` through `confine.Run`:
  - The tile directory and the workspace are read-only. `.xbin` is masked, with the tile's own output and cache dirs mounted back in; `data/` and `homes/` are masked.
  - `CGO_ENABLED=0`. The host module cache is offered as a read-only `file://` GOPROXY.
  - Network is public-only (`confine.NetInternet`), or the host network with `XBIN_BUILD_NET=host`. Timeout 20 minutes, output capped at 1 MiB.
  - Caches are per tile: `.xbin/cache/tile/<CompKey>/{go-build,mod}` (:44-45).
- Without isolation it is a direct `exec.Command("go","build",...)` marked `// exec-ok` (:379-386), with a shared `GOCACHE=.xbin/cache/go-build`.
- Builds resolve through the generated root `go.work` (`deps/deps.go:148-193`), so a build is not self-contained in the tile directory.

**Other runtimes**
- node/python have no build: the "binary" is the entry file inside the live tile directory (:388-401).
- cgi has no build and no supervision (see the security note at the end).
- `setup` builds an environment layer (`ensureEnvLayer`, `runner/env.go:52-131`, isolation only) at `.xbin/env/<CompKey>/<envHash>/`. `envHash` is sha256 of the setup script plus a rootfs identity, first 16 hex chars (:39-46); only the current one is kept (:134-145).

**Other derived paths**
- Log: `.xbin/log/<CompKey>.log`, appended with `--- gen N start ---` markers, no rotation (:464-471).
- Sockets: `<tmpfs>/xbin-<hash>/run/<CompKey>/g<gen>.sock`, symlinked from `.xbin/run` (`runner/rundir.go:24-48`).

#### 2c. The runner (`runner.go`)

**Constants** (:42-48): health timeout 5s, drain 30s (D8), idle reap 30m, crash window 10s, crash limit 3.

**How an instance is identified**
- Runner state is a map keyed by component path (:155, `state()` :173); a new entry starts dirty.
- Each generation has a `gen` counter (in memory; it starts again from 1 after an xbind restart), a socket `g<gen>.sock`, and a fresh `XBIN_TOKEN` (`RandomToken(24)` :415).
- The token maps only to the component path (`Auth.RegisterInstance`, `/home/magik6k/buxon/internal/auth/auth.go:445-455`). There is no separate instance id.
- The backend also gets `XBIN_SOCKET`, `XBIN_COMPONENT` and `XBIN_GATEWAY`, plus `Broker.EnvFor` (`broker/resources.go:71`) for resource and interface variables.

**`Ensure`** (:187-237) is single-flight. The lifecycle gate `ShouldRun` is checked first (:194). The fast path is:
```go
if !s.dirty && s.cur != nil { return s.cur.sock }
```
Otherwise one caller builds and the rest wait on `buildDone`. The loop means a change during a build causes exactly one more build afterwards; the in-flight build is not cancelled, and its (already stale) generation is swapped in first.

**`Changed`** (:262-281) marks the tile dirty and clears crash history. It only starts a background rebuild if a process exists:
```go
hadProcess := s.cur != nil || s.building || s.lastErr != nil
```
A tile that has never started just stays dirty until its first request.

**Blue/green, `buildAndStart`** (:284-355)
1. Publish `build-start`, then build.
2. `stopFirst` stops the old generation first for VM tiles with file resources (`runner/vm.go:124-129`).
3. `start`, then `waitHealthy`. The health check only dials the unix socket; there is no HTTP check (:822-833).
4. Swap `s.cur`, then stop the old generation in the background: SIGTERM, kill after 30s (:717-729).
5. Start the crash watch and publish `build-ok`.

- A failed build publishes `build-error` and leaves `cur` alone, so the old generation keeps serving.
- While the tile is dirty, new requests wait for the new generation; only in-flight requests and streams stay on the old one.
- The old generation's token is revoked when its process exits (:591-610, `RevokeInstance` :608), so it can live up to 30s past the swap. `docs/elements.md:539` says it "dies at swap".

**Crashes, reaping, alwaysOn, lazy start**
- An unreplaced exit marks the tile dirty so the next request rebuilds. Three exits within 30s set a sticky "crash-looping" error, publish `build-error`, and keep it down until the next save (:328-350).
- The reaper (:798-820) stops tiles with no active connections and no request for 30 minutes, unless alwaysOn. `Track` (:243-258) counts SSE/WebSocket streams as active.
- alwaysOn (D84, `runner/alwayson.go`):
  - `WakeAlwaysOn` (:45-61) runs at boot (`boot.go:705`), after every watcher batch, on vault unseal and on lifecycle enable (`brk.WakeBackends`, `boot.go:503`).
  - `aoStart` (:63-84) runs at most 2 at once.
  - `afterExit` (:100-137) schedules a restart after `nextBackoff` (:88-96): 1s doubling to 5 min, reset after 10 healthy minutes.
- Every caller of `Ensure` is a lazy start:
  - the `/api/<path>` proxy (`proxy/proxy.go:182`) and public ingress (`proxy/ingress.go:58`);
  - stream dials (`runner/ingress.go:37`) and network-provider bring-up (`runner/netmux.go:62`);
  - cron and bus deliveries, which go through the proxy;
  - `Changed` and `aoStart`.

**Every start rebuilds from the live tree.** A tile that was reaped, crashed, stopped, re-enabled, or survived an xbind restart builds from whatever is on disk now. Other things that trigger a rebuild through `Changed`:
- grant, binding, netset and ownership-transfer changes (`OnGrantChange`, `boot.go:497-501`; `broker.go:643-658`);
- a network-provider tile restarting, which nudges its client tiles (`runner.go:572-588`).

#### 2d. VM vs namespace sandbox (D89/D90)
- `start` (:431-462): with isolation on and a go/node/python runtime, `sandboxCmd` (:627-715) builds a namespace sandbox spec. The tile directory is read-only, the run dir and resource dirs are read-write, and the capabilities are dropped.
- Then `if r.wantsVM(c) { vmApply(...) }` (:709-713) converts that spec into a VM, and `sandbox.Launch` starts it.
- `wantsVM` is `r.Isolate && Manifest.VM.Enabled() && sandboxable(runtime)` (`runner/vm.go:40-42`).
- `vmApply` (:56-97) refuses to start when:
  - there is no VM manager;
  - the admin's `backends` switch is off (`.xbin/vm/policy.json`, `vm/policy.go:18-26,70`);
  - VMs are unavailable on this host;
  - `setup` is set.
- `vm.Manager.Apply` (`vm/manager.go:163-175`) also refuses host networking, provider splices, lan-ingress links and environment layers.
- Firecracker vs emulation is decided in `decide` (manager.go:80-98): usable KVM means Firecracker; otherwise QEMU emulation if its files ship. `XBIN_VM_ACCEL=kvm|emulate` forces one.
- The health timeout is 60s, tripled when emulated (`healthFor` :44-52). `run.VM` is only set under `--isolate` (`boot/vm.go:17-36`).
- **Docs disagree with code:** without `--isolate`, a `"vm": true` tile silently runs as an ordinary host process. `docs/elements.md:56-61` says it fails with a reason instead.

### 3. Frontend reload

**Serving**
- The `/c/` route (`server/server.go:155`) goes to `handleComponentStatic` (`server/static.go:70-180`). It reads files from the working tree on every request (`openLegacy` :193-219; strict mode `serveStrictStatic`, `tileassets.go:169`, is also live).
- Responses are `Cache-Control: no-store`. HTML gets the one sanctioned injection (`headInjection` :431-466).
- There is no snapshot or version of a frontend.
- Static serving does not check lifecycle. A disabled or hidden tile's page still loads; only its API calls get 409 plus an `X-XBin-Lifecycle` header (`proxy.go:148-158`).
- Nothing in `web/` or the workspace template reads that header, so the "placeholder" described in `docs/overview/14-lifecycle.md:39-41` is whatever the tile does with a 409.

**Events**
- The hub `Event` struct is in `events/events.go:11-17`, served on `/ws/events` (`server.go:169`, filter at :592-612). The wire format is in `docs/protocol.md:2191-2209`.
- `reload` comes from the watcher (`serve.go:178`) and from `POST /lifecycle` (`broker/lifecycle.go:114`).
- `build-start`, `build-error` and `build-ok` come from the runner (runner.go:285/289/309/315/345/352; env.go:72).

**In the browser**
- Each page has one shared socket (`web/events-socket.js`). `isReloadTarget` picks the most specific mounted frame by longest path prefix.
- `BxFrame._event` (`web/bx-frame.js:366-396`) calls `_reload` (:405-417), which re-navigates the frame and re-mints its token when needed.
- `build-error` shows an overlay (:825-826); `build-ok` or the next reload clears it.
- `reload` fires as soon as the file changes, before the rebuild. The reloaded page's API calls wait in `Ensure` for the new generation, or reach the old one if the build fails.

**Native UIs (D91)**
- The app loads `/c/<tile>/?native=1`, a document generated per request (`server/native.go:54-128`). It imports the entry from the live tile directory.
- The shell and `bx-frame` never run `native.js`.
- Saving the entry sends `reload` without restarting the backend (`registry/native.go:108-124`). Two exceptions: an undeclared `native.js` under node, and a Go tile built from its root.
- The app reloads on `reload` (`native/ios/App/Model/WorkspaceEvents.swift:243-245`, `native/ios/App/Tiles/NativeTile.swift:374`). It parses build events (XbinCore `Events.swift:56`) but nothing in the app uses them, so the app shows no build errors.

### 4. Controls that exist today

**Lifecycle**
- `POST /api/xbin/lifecycle {component, state}` is handled by `Broker.apiLifecycleSet` (`broker/lifecycle.go:22-116`). States are `enabled | disabled | hidden | offloaded | offloaded-full`. Allowed for workspace admins, the tile's owner, or the owning org's admins (:28).
- Any non-enabled state calls `Runner.Stop` (`runner.go:734-748`, 5s deadline). Enabling only starts alwaysOn tiles immediately; others start on their first request.
- `hidden` (D42) is disabled plus filtered out of sidebars on the client (`workspace-template/shell/menus.js:28-29`, `bx-side.js:60`). It is refused while offloaded (:56-63).
- Enforcement is `ShouldRun = enabled && !EncryptionHold` (`boot.go:506-508`). It gates `Ensure`, `Changed` and alwaysOn. The proxy returns 409, ingress drops the tile, and cron (`cron.go:171`) and bus deliveries (`bussubs.go:342`) pause.
- The watcher still sends `reload` for disabled tiles, so their frames keep reloading.

**What does not exist:**
- no per-tile pause of rebuilds or hot reload;
- no way to stop a backend while keeping the tile enabled;
- no restart endpoint, and no `bx restart`, `bx stop` or `bx build`.

Today you restart a backend by saving a file, changing a grant, or disabling then enabling and sending a request.

**Status APIs**
- `GET /api/xbin/tile-status?component=` (admin or the tile itself; `boot/api.go:87-117`) returns `{component, backend, disk, alerts, net}`.
  - `backend` is a `runner.Backend` (`runner/inspect.go:26-53`). Fields: path, runtime, state (idle/building/healthy/failed), isolated, pid, gen, restarts, activeConns, uptimeSec, lastReqSec, error, rssKb, threads, fds, cpuSec, namespaces, egress, activity, cgroup, net fields.
  - A failed build with the old generation still up shows as `healthy` with `error` set.
- `GET /backends` (api.go:22 → `Runner.Status`, runner.go:773-796) returns `{path: {state, gen, error}}`.
- `GET /runtime` (api.go:31, admin only).
- `GET /components` includes `state`, omitted when enabled (`server/api.go:136,186`).
- `GET /logs?component=&tail=&follow=1` (`obs/logs.go:51-153`).
- `/tile-report` is the tile's self-reported status, cleared on every `build-start` (`obs/status.go:129-143`).

**bx** (`/home/magik6k/buxon/cmd/bx/main.go`)
- `status [<c>] [--all]` (:349-377).
- `logs [-f] <c>` (:494-530) reads `.xbin/log/<CompKey>.log` directly, not through the API.
- `enable`, `disable`, `hide`, `unhide` (:84-91).
- `offload`, `backup`, `backups`, `restore`, `backup-schedule` (:92-101).
- `doctor` has no backend or build checks.

**Where build errors show up**
- the bx-frame overlay;
- a 502 `{"error":"backend build failed","detail":…}` from the proxy, only when no generation is live (`proxy.go:183-190`);
- the admin runtime tab (`workspace-template/tiles/admin/tabs/runtime.js:261`);
- `bx status`.

Compiler output is not written to the log file; only process output and setup output are. `plans/dev-flow.md:92-93` says compiler errors appear in the log tail, which isn't true.

### 5. Snapshots and versions

**Nothing today can be pinned or promoted.**
- Go has one output file per tile, overwritten each build.
- node/python and all frontends run from the live files.
- Go backends also run with their working directory set to the live tile directory (mounted read-only).
- Go's build cache is content-addressed internally, which makes rebuilding old source fast, but it isn't exposed as artifacts.
- Builds use the working tree (`-buildvcs=false`), and nothing records which commit a generation was built from.

**The only content-hash-keyed things**
- The `setup` environment layer (env.go:30-46).
- Builtin provenance: `.xbin/builtins.json` stores a `UnitState` (source, install path, version, rollup hash, per-file sha256, adopted, pinned; `builtins/updates.go:36-44`, rollup :245-256) plus a base snapshot in `.xbin/builtins/<id>/` (:215). "Pinned" there only mutes update offers (:803).
- VM rootfs images (`vm/image.go:20-47`).

**Places that hold versions**
- Every component is its own git repo (`broker/code.go:96-119`). xbind makes one initial commit and never commits again (D2/CM-2).
- Backups are versions stored by the archiver tile; restoring overwrites the live tree wholesale.
- Template repos in `.xbin/template-repos/<name>` get one commit per embedded version (`broker/templaterepo.go:56-95`).

**Builtin updates**
`POST /builtins/update {id, mode}` is handled by `apiBuiltinsUpdate` (`broker/tiles.go:147-206`); detection is `Updater.Updates` and `compare` (updates.go:365-489).
- `replace` (`ApplyReplace` :504) and `merge` (`ApplyMerge` :538, conflict markers via git merge-file) write straight into the live tile. The watcher then reloads and rebuilds it immediately, with no staging step.
- `pr` (D49):
  - `ProposeBuiltinPR` (`broker/prs.go:335-386`) calls `Propose` (updates.go:625-700), which renders the update as a git patch in a temporary repo (`patchSeries` :733).
  - It is filed in `data/prs/<CompKey>/` with `Kind:"builtin-update"` and a content hash. It is idempotent, and older open proposals are withdrawn as superseded.
  - Nothing is written to the tile. The tile's own terminal applies it with `git am --3way` in a clone, then pulls it into the live directory.
  - Closing the PR as merged calls `RecordApplied` (:705), which checks the hash still matches.
- `pin` and `unpin` are also accepted. At boot, `BackfillEssentials` (:85) installs missing essential scaffold tiles.

**Templates (D50)**
- `POST /templates/new` (`broker/templates.go:68`) copies the template and seeds its git history from the template repo (`SeedInstanceRepo`, templaterepo.go:106-138). It also adds a `template` remote.
- `GET /templates/updates` flags instances that are behind (:167-207). Nothing merges automatically.

**D58** is about the shipped trees rather than updates:
- `assets_test.go` guards the embedded trees.
- The copier skips a compiled binary named after its own directory, like `backend/backend` (`builtins/builtins.go:215-218`).
- `docs/compat.md` is the upgrade contract you'll design against: API additive only (rule 2), CLI a superset (6), manifest keys additive with unknown keys ignored (7).

**Closest things to "multiple deployments" today**
- `POST /clone` (`broker/clone.go:28`) copies a tile to a new path, which is a new identity: git history copied, path references rewritten, no data or vault.
- Template instances.
- A provider declaring `instances: true` is one backend serving many routes, not several deployments.

### Design pitfalls
1. **Everything is keyed by component path:** the principal and token map, runner state, build output, log, socket dir, cgroup leaf, vault (`data/vault/<CompKey>.json`), environment layer, lifecycle, grants, bindings and PRs. A second deployment needs its own key for all of them.
2. **Pausing hot reload needs more than skipping `run.Changed` in the watcher.** Every restart path rebuilds from the live tree: reaping, crash restarts, alwaysOn backoff, grant changes, re-enabling, and xbind restarts. The watcher's `reload` events would also keep reloading frames. Because frontends and node/python read live files, a real pause needs a snapshot of the tile's source, not just of the binary.
3. **Per-tile suppression would go in `changedComponents` / `watchLoop`** (`serve.go:175-182`), since the watcher is global.
4. **The implementation plan has drifted from the code.** `plans/implementation.md:168-183` describes a `next` build slot, a global build semaphore, a `/healthz` check, rotated logs streamed to the event hub, and cancelling in-flight builds. None of these exist: there is one `bin` output, no semaphore except alwaysOn's limit of 2, a socket-dial health check, plain append-only logs, and queued (not cancelled) builds.

### Security finding (outside the task, please verify)
`runtime: "cgi"` tiles run `backend/handler` through the standard library's `net/http/cgi.Handler` inside the proxy (`proxy.go:175-178, 239-261`). That runs the tile-written handler as the xbind user on the host, with no sandbox, even under `--isolate`.

The D78 guard test (`/home/magik6k/buxon/internal/confine/guard_test.go:14`) only matches direct exec calls, so it never sees this. Anyone who can write to a tile, such as a tile terminal or coding agent, appears able to run code as xbind by switching the tile to cgi. No test covers cgi.
