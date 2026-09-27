# Backend identity and everything keyed by a tile's path

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: explore agent aace5e42.

I've mapped all six areas. Three things change the premise of your plan:

- **The `/c/<path>/` plane is not in `internal/proxy`.** It is static serving in `/home/magik6k/buxon/internal/server/static.go:70-180`. Backend traffic goes through `/api/<path>/…`, handled by `/home/magik6k/buxon/internal/proxy/proxy.go:128-235`.
- **kv, sqlite, filesystem, blob, bus and cron are keyed by scope, not by tile.** A resource is `res:<scope>/<name>`. A tile only has private resources if its directory holds a `scope.json` (templates and webhooks do this). The vault is the only data store keyed by the tile path.
- **There is no tile delete, rename or move API.** Removing a directory leaves every path-keyed record behind.

There are two key encodings throughout:
- `util.CompKey(path)`: the path with `/`→`~`, cut to 24 characters, plus `-` and 8 hex digits of its sha256. It is one-way. `/home/magik6k/buxon/internal/util/util.go:137-144`.
- `util.ScopeKey(scope)`: `apps~cal`, or `workspace` for the root. `util.go:127-132`.

### 1. Identity of a running backend

**The credential**
- **Minting:** `Runner.start` creates `util.RandomToken(24)`: 48 hex characters, random, held only in memory (`/home/magik6k/buxon/internal/runner/runner.go:415`).
- **Environment given to the backend** (`runner.go:408-422`):
  - `XBIN_SOCKET=<rundir>/<CompKey>/g<gen>.sock`
  - `XBIN_COMPONENT=<path>`
  - `XBIN_GATEWAY=<rundir>/gateway.sock`
  - `XBIN_TOKEN`
  - plus `Broker.EnvFor` output (`/home/magik6k/buxon/internal/broker/resources.go:71-144`).
  - The daemon's own environment is filtered by an allow-list (`/home/magik6k/buxon/internal/runner/hostenv.go:21-46`).
- **Registration:** after `cmd.Start`, `Auth.RegisterInstance(token, c.Path)` (`runner.go:479`) adds it to `Auth.instances`, a token→path map (`/home/magik6k/buxon/internal/auth/auth.go:219, 445-449`).
- **Revocation:** only when the process exits (`runner.go:591-610`), not at the swap. During the 30 s blue/green drain (`runner.go:44, 322-324`) two live tokens map to the same path.
- **How the backend presents it:** `Authorization: Bearer` over the gateway unix socket, via the SDK's `Client()`, `gatewayTransport` and `GatewayDial` (`/home/magik6k/buxon/sdk/xbin.go:205-249`). The gateway runs the same handler as the console (`/home/magik6k/buxon/internal/boot/serve.go:34-54`).
- **Resolution:** bearer tokens are tried in order owner → instance → terminal → app session (`auth.go:584-597`). An instance token becomes `Principal{Component: path, Via: "instance"}` (`auth.go:589-591`; also in no-auth mode, `569-573`).

**The principal model** (`auth.go:53-84`)
- Fields: `Owner`, `UserID`/`User`/`Access`, `Component`, `Via`, `DeviceID`, `Gen`, `Role` (synthetic principals only), `Impersonator`.
- `Via` values: cookie, bearer, instance, frame, cron, terminal, session, device, app, plus `dev` (`auth.go:581`) and `bus` (`bussubs.go:354`).
- `From()` returns the component path, `owner`, or `user:<id>` (`auth.go:196-207`).

**Principal kinds**
- **Root token:** stored in `.xbin/token` (`auth.go:257-287`).
- **Human sessions** (session, device, app): `internal/auth/sessions.go`, `devices.go`.
- **Frame token** (tile frontend):
  - Wire format: `b64url(component)|b64url(user)|exp|gen|hmac`, HMAC-SHA256 keyed by `.xbin/secret`.
  - `gen` is one of `s.<handle>`, `u.<epoch>.<n>`, `o.<hash>` (`/home/magik6k/buxon/internal/auth/frametoken.go:18-43`).
  - Code: mint `226-232`, verify `252-294`, `framePrincipal` `304-320`. Lifetime is 15 minutes (`static.go:24`).
  - In `--tile-assets=origins` mode (D95) the tile-origin cookie resolves to the same frame principal via `TilePrincipal` (`/home/magik6k/buxon/internal/auth/assettoken.go:215-225`).
- **Terminal:** `MintTerminal(component, userID)` creates a random 24-byte token (`auth.go:240-245, 471-507`), called at `/home/magik6k/buxon/internal/term/term.go:334`.
- **Synthetic callers:**
  - `CronPrincipal="xbin/cron"` (`/home/magik6k/buxon/internal/broker/broker.go:34`, built at `cron.go:180`)
  - `BusPrincipal="xbin/bus"` (`bussubs.go:44, 354`)
  - `IngressFrom="ingress"` (`auth.go:43-50`)
- **What `whoami` reports:** kind is user, root or element. For an element it adds `driverView`, the human behind it (`/home/magik6k/buxon/internal/broker/whoami.go:36-68, 85-113`).

**Headers** (`Proxy.identify`, `proxy.go:275-305`; constants `29-45`)
- **Stripped:**
  - every inbound `X-Xbin-*`
  - xbind's session and tile cookies
  - any `Authorization: Bearer` (`276-284`)
  - `?frame=` from the forwarded query (`199-203`)
- **Injected:**
  - `X-XBin-From`
  - `X-XBin-Role`
  - `X-XBin-User`
  - `X-XBin-User-Level`: the user's level on the callee (`/home/magik6k/buxon/internal/boot/boot.go:485-491`)
  - `X-XBin-Viewed-By`
- **Public ingress:** the same stripping, then `X-XBin-From: ingress` and `X-XBin-Ingress-Host` (`/home/magik6k/buxon/internal/proxy/ingress.go:24, 42-54, 68-70`).
- **CGI backends** get `XBIN_FROM`/`XBIN_ROLE` in the environment instead (`proxy.go:249-258`).
- **Other `X-XBin-*` headers** (not identity):
  - `-Frame-Token` on requests (`auth.go:41`, or `?frame=` for WebSockets at `559`)
  - `-Lifecycle` on the 409 for a non-enabled tile (`proxy.go:149`)
  - `-Client` from the native app (`native.go:26`)
  - `-Session` on the `/ws/term` upgrade response (`term.go:239-241`)
  - The SDK reads the identity headers through `Caller()` (`sdk/xbin.go:84-131`).

**Attribution (D29)**
- `X-XBin-User` is set only for session, frame and terminal principals (`proxy.go:290-304`).
- Calls from a backend's instance token, cron and bus carry no user. Nothing forwards the driving human through a backend→backend hop.
- `Broker.Policy` (`broker.go:477-490`) decides the role:
  - an admin gets admin
  - a tile calling itself gets admin
  - cron and bus deliveries get the role chosen when they were registered
  - everything else gets `grantedRole(p.Component, target)`
- Limits on instance principals:
  - they can read only their own tile (`auth.go:133-138`)
  - they never get a frame token (`backendPrincipal` `static.go:549-557`; `/home/magik6k/buxon/internal/server/api.go:241-252`)
  - they are the only principals that can read vault values (D30, `vault.go:183-194`)

### 2. Per-tile state and where it lives

**Scope-keyed resources** (all under `/home/magik6k/buxon/internal/broker`)
- **Scope resolution:**
  - A scope is the nearest ancestor directory holding `scope.json` (`/home/magik6k/buxon/internal/registry/registry.go:447-454, 488-508`).
  - `parseRes` matches the longest declared scope prefix; `res:workspace/<n>` addresses root-level resources (`broker.go:321-366`).
  - Same-scope `uses` entries are granted automatically (`broker.go:443-460`).
- **kv:**
  - one bbolt file, `data/kv.db` (`resources.go:157-169`)
  - bucket name `res:<scope>/<name>` (`171-202`)
  - values encrypted with the vault key under label `kv:<bucket>` (`resenc_wire.go:189-229`)
- **filesystem / sqlite / blob** (gocryptfs):
  - ciphertext in `data/resources-enc/<ScopeKey>/<name>/`
  - decrypted mount at `.xbin/resenc/<ScopeKey>/<name>/`
  - password derived with label `fs:<ScopeKey>/<name>` (`/home/magik6k/buxon/internal/resenc/resenc.go:83-118`; `resLabel` `resenc_wire.go:43`)
  - `XBIN_RES_<N>` is the mount directory, or `<dir>/<name>.sqlite`; it is given only to components in the same scope (`resources.go:83-95`)
  - bound read-write into the sandbox (`/home/magik6k/buxon/internal/runner/binds.go:17-54`)
  - a backend is held from spawning while its resources are sealed (`EncryptionHold`, `resenc_wire.go:102-129`)
- **bus:** in-memory topics `res:<scope>/<name>/<topic>` (`resources.go:408-437`). WebSocket delivery is filtered by the subscriber's grants (`439-458`; `server.go:592-612`).
- **cron:**
  - jobs stored in `data/cron-jobs.json`, keyed by component plus job name
  - the job's component is always forced to the caller (`cron.go:24-38, 76-78, 108, 227-255`)
- **Quotas and uids:** disk quotas per ScopeKey (`broker.go:177-198`); tier-2 uids per scope in `.xbin/uids.json` (`uids.go:36-84`).

**Keyed by the tile path**
- **Vault (D30):** `data/vault/<CompKey(path)>.json`, sealed (`vault.go:22-111`); access checks at `149-194`. The SDK builds the vault URL from `Self()`, i.e. `XBIN_COMPONENT` (`sdk/xbin.go:263-300`).
- **Bus push subscriptions (D85):**
  - stored in `data/bus-subscriptions.json`, keyed by component plus name; the component is forced to the caller
  - each delivery is a POST to `/api/<comp><path>` as `xbin/bus` (`bussubs.go:26-62, 97-235, 330-362, 391-435`)
- **Backups:**
  - archive key `CompKey(path)`
  - archiver is `bindings[comp]["@archive"]`, falling back to `bindings["*"]["@archive"]`
  - scope data is included only if the tile roots its scope (`backup.go:31-108`)
  - schedules in `data/backup-schedule.json` (`backup_cron.go:15-83`)
- **Runtime artifacts, all named by `CompKey`:**
  - log `.xbin/log/<key>.log` (`runner.go:464-466`)
  - build output `.xbin/build/<key>/bin` (`runner.go:366`)
  - Go caches `.xbin/cache/tile/<key>/` (`build.go:43-45`)
  - `setup` layer `.xbin/env/<key>/<hash>/` (`env.go:28-46`)
  - sockets `<tmpfs>/…/run/<key>/g<gen>.sock` (`rundir.go:17-48`)
  - cgroup leaf `comp-<key>` (`cgroup_linux.go:89`)
  - terminal layer `.xbin/term/<key>/`, including a VM's `vm/disk.img` (`term.go:481-487`; `vm/disk.go:16-21`)
  - terminator socket `igw-<key>.sock` (`/home/magik6k/buxon/internal/ingress/forwards.go:36-40`)
- **No other per-tile data directory exists.** The backend's root overlay is tmpfs, so anything written outside resources is lost (`runner.go:622-636`; `sandbox/sandbox.go:136-142`).
- **Homes are per user**, `homes/<user>`, not per tile (`term/homes.go:25, 48`).
- **Agent history:** `data/agent-history/<user>/<CompKey(tile)>/<id>.json`, newest 20 kept (`term/history.go:1-92`).
- **Prefs:** `data/prefs/<CompKey(user)>/<CompKey(caller component)>.json` (`obs/prefs.go:15-48`).
- **PRs:** `data/prs/<CompKey(target)>/` (`prs.go:86-87`).
- **Status reports:** in memory, per tile (`obs/status.go:13-116`).
- **Push (D94):**
  - stored in `data/push/push.json` (`boot/push.go:18`); `mutedTiles` holds tile paths (`push/store.go:71`)
  - `/notify` takes the tile from the caller's principal (`push/api.go:282-300`)
  - rate budget is per tile, or per tile and person (`356-361`)
  - links point at `c/<tile>/…` (`368`)
- **Webhooks:** a builtin tile, not a platform store (`/home/magik6k/buxon/builtin-tiles/webhooks/xbin.json`):
  - its own resource `res:apps/webhooks/state`
  - public `/hook/*` via `exposes`
  - a multi `agents` slot bound to an agent's `inbox`
- **Triggers (D87):** internal to the agent template:
  - `triggers` and `trigger_events` tables in the agent's sqlite (`builtin-templates/agent/_backend/triggers.go:29-41`)
  - bus triggers are bus subscriptions named `trig-<id>`, re-registered on every start (`triggers.go:441-470`; `main.go:67-68`)

### 3. How inbound traffic reaches a tile

- **Route table:** `server.go:129-174`. `handleAPI` sends `/api/xbin/*` to the core mux and everything else to the proxy (`server.go:561-590`).
- **The proxy's steps** (`proxy.go:128-235`):
  1. `Reg.Resolve` finds the longest registered path prefix (`registry.go:517-536`).
  2. It returns 409 if the tile isn't enabled.
  3. It checks `Policy`.
  4. It sets identity headers in `identify`.
  5. It calls `Runner.Ensure(comp)`.
  6. It calls `Track`.
  7. It forwards over one pooled unix-socket transport per generation socket (`86-126`).
- **The runner side:**
  - `states` is a map keyed by path (`runner.go:154-182`).
  - `Ensure` returns `s.cur.sock` or runs a single build shared by all waiting callers (`184-237`).
  - Blue/green swap is in `buildAndStart` (`283-355`).
  - VM backends with file resources stop the old generation first (`vm.go:120-129`).
- **Every inbound path goes through `Runner.Ensure(comp)` or `/api/<comp>`:**
  - frontends
  - consumer backends calling a provider via `XBIN_IFACE_*_URL`
  - cron (`cron.go:198-208`)
  - bus deliveries (`bussubs.go:125-135`)
  - archiver calls (`backup.go:375-388`)
  - public HTTP ingress (`proxy/ingress.go:56-58`)
  - streams and hairpin connections (`runner/ingress.go:28-134`)
  - net-provider warm-up (`netmux.go:51-62`)
  - always-on tiles (`alwayson.go:43-60`)
  - grant-change restarts (`boot.go:497-501`)
- **Frame tokens (ND2/ND8/D95):**
  - `/c/` access checks: `static.go:78-115`
  - owning tile: `owningComponent` `327-338`
  - `headInjection` adds the metas `xbin-component`, `xbin-frame-token`, `xbin-interfaces`, `xbin-sandbox` (`static.go:423-466`)
  - a token is minted only for a human or the tile itself (`mayMintFrameToken` `468-503`)
  - renewal via `api.go:232-252`; the client sends `X-XBin-Frame-Token` (`web/xbin-client.js:50-98`)
  - the session cookie is dropped for requests coming from inside a tile (`TileContext`, `auth.go:654-698`)
  - in origins mode each tile gets host `t-`+HMAC(secret, path) (`assettoken.go:178-188`, `tileorigin.go:17-140`)
- **Native tiles (D91):**
  - `/c/<tile>/?native=1` is served by `serveNativeRoute` and `nativeRuntimeDoc` (`native.go:44-128`)
  - they reuse `headInjection`, so the identity and frame token are the same
  - authorized exactly like `index.html` (`static.go:116-121`)
- **Exposed endpoints (D79):**
  - Declared under `exposes` in the manifest (`registry.go:94-98, 119-180`).
  - Each route is an entry in `bindings[comp][slot]`, a `BindRef{Ref: "runtime" or terminator path, Host | Zone | Listen}` (`registry.go:238-345`), added or removed one at a time via `POST/DELETE /bindings` with `add` (`netfn.go:719-851`; `exposefn.go:22-175`).
  - Zone hostnames are registered by the tile itself into `ingressHosts[comp]` (`ingressfn.go:486-586`).
  - HTTP path: `IngressLookup(source, host)` (`ingressfn.go:98-146`) → `ForwardIngress` (`proxy/ingress.go:31-95`).
  - Streams: `IngressStreamSpecs` (`240-259`) → `DialInto`.
  - Only enabled, non-template tiles publish anything (`liveForIngress` `93-96`).

### 4. Bindings and grants

- **Where slots are declared:** in the manifest, under `interfaces`, `provides` (optionally `instances:true`) and `exposes` (`registry.go:89-133`).
- **Where state is stored** (the root `xbin.json`, `registry.go:205-236`):
  - `grants[]{from, target, role, approvedBy, approvedAt}`
  - `bindings[comp][slot]`
  - `ifaceInstances[provider][id]`
  - `ingressHosts[comp]`
  - `lifecycle[comp]` and `lifecycleAt[comp]`
  - `@`-prefixed pseudo-slots such as `@archive` (`netfn.go:865-873`)
  - all writes go through `MutateWorkspace` (`584-595`)
- **Resolution at call time:** `grantedRole` (`broker.go:412-452`) checks, in order:
  1. the policy ceiling (`policy.go:22-87`)
  2. explicit grant rows
  3. `httpBindingRole`: an http binding is itself the grant, at the provider's declared role (`netfn.go:283-311`)
  4. same-scope auto-grant
- **How a provider call reaches the provider:**
  - `HTTPSlots` turns each ref `provider[#inst]` into `/api/<provider>` plus the instance's path prefix (`netfn.go:313-381`).
  - This is injected as `XBIN_IFACE_<SLOT>_URL` / `_INSTANCE`, or as a JSON list for multi slots (`resources.go:99-123`).
  - The consumer calls through the gateway and lands on the provider's `Runner.Ensure` with `X-XBin-From: <consumer path>`.
  - A provider cannot tell two deployments of the same caller apart today.
- **Grant target forms:**
  - a component path
  - `res:<scope>/<name>` (`broker.go:321-366`)
  - `xbin` / `xbin:*` (`broker.go:462-475`; creating tiles needs `xbin:writer`, `policy.go:109-114`)
  - `code` / `code:<comp>` (`code.go:133-145`)
  - `gpu:<n>`, `cap:net-admin`, `cap:containers`, `cap:open-links` (`gpu.go:10-96`)
  - new `net:*` grants are refused (`broker.go:684-690`)
  - grants that are baked in at spawn restart the tile (`broker.go:643-666`)
- **Allowance targets derived from bindings** (`delegated.go:134-222`):
  - `ingress:host|zone|listen:…`
  - `net:…`
  - `net:provider:<tile>`
  - `iface:<svc>@<provider>[#<inst>]`
- **Policy ceilings:** pattern rows matched against the tile path (`users/orgs.go:43-47, 1211-1264`).
- **The net slot and egress:**
  - Possible bindings: `internet`, `internet:<specs>`, `lan:<cidr>`, `host`, `org`, `personal`, `set:<name>`, `none`, or a provider tile (`netfn.go:912-959`).
  - `netBinding(comp)` is the one resolver (`36-114`); `EgressFor` turns it into a relay policy (`egress.go:57-98`).
  - Provider splicing uses /30 link addresses assigned by sorted client index (`netfn.go:212-251`); the runner keeps a provider→client map of link file descriptors (`netmux.go:11-49`).
- **Network namespaces are per generation, not per tile or scope.** Every launch creates fresh user, mount, pid, ipc, uts and net namespaces (`launch_linux.go:122-125`). The "reuse a per-scope namespace" idea from ISO-4 was never implemented. The relay hangs off each `instance` (`runner.go:493-589`).

### 5. Ownership and lifecycle keyed by path

- **Owner entries (D24):**
  - `owners` in `data/users.json`, values `user:<id>` or `org:<id>` (`users/orgs.go:150-247`)
  - set by `assignOwner` (`create.go:180-198`), called from create `:64`, clone `:151`, git import `:224`, builtin import `:110` and template-new `:171`
  - boot backfill does not set an owner (`boot.go:377`)
  - deleting a user drops their owner rows (`users.go:606-652`)
  - only `bx doctor` flags owner rows whose tile is gone (`cmd/bx/doctor.go:80-95`)
- **Other path-keyed rows in the users store:**
  - `User.Tiles` (`users.go:68-71`)
  - `Org.Tiles` (`orgs.go:98-100`)
  - `defaultTiles`
  - policy rows
  - access requests `{user, tile}`, plus the dismiss cooldown keyed `user\x00tile` (`requests.go:18-24, 126`)
- **Hidden (D42):** `StateHidden` is stored in `lifecycle[comp]` (`registry.go:347-370`; set in `lifecycle.go:56-63`).
  - Server-side it just means "not enabled": no spawn (`boot.go:504-508`), proxy 409, cron skipped (`cron.go:167-173`), bus deliveries dropped (`bussubs.go:342`), nothing published.
  - Hiding from lists happens only in the UIs.
- **Transfer (D39):**
  - Flow: `POST /owner` (`orgsapi.go:459-499`) → `SetOwner` → `executeTransferEffects` (`transfer.go:239-281`), which unbinds slots the new ceiling kills and restarts the affected backends.
  - It rekeys nothing else: the path stays the key everywhere.
- **Rename/move:** not supported. Moving the directory orphans every row above plus the vault and scope data.
- **Delete:** no API, and cleanup only partly handles leftovers:
  - **What `pathLeftovers` checks** (`policy.go:211-258`):
    - grants naming the path, including `code:` targets
    - bindings where the path is consumer or provider
    - `ifaceInstances`, `ingressHosts`
    - the vault file
    - `Store.PathLeftovers` (`/home/magik6k/buxon/internal/users/leftovers.go:9-44`): another owner, other users' exact entries, org shares, an exact `defaultTiles` entry
  - It only runs through `newTilePathOK`, for non-admin creators (`policy.go:142-174`). An owner re-creating their own tile is exempt (`219-221`).
  - **Cleared on re-create:** only cron jobs and bus subscriptions (`cron.go:146-164`, `bussubs.go:218-235`).
  - **Neither checked nor cleared:**
    - `lifecycle[path]`: a new tile inherits "disabled" or "hidden"
    - the scope's kv buckets and encrypted data when the tile roots its scope
    - backup schedule, PRs, prefs, agent history, logs and layers
    - `mutedTiles`, access requests
  - **Likely bug:** offload (`backup.go:449-512`) removes `data/resources/<scope>` but not `data/resources-enc/<scope>`, so encrypted file-resource bytes are never freed.

### 6. What already resembles instances or variants

- **Runner `instance`:** one backend generation (`runner.go:55-67, 294`). Two coexist during the drain.
  - The agent template's D81 engine lock exists for exactly this reason: two processes share one sqlite during a swap. It is a `flock` on `<db>.engine` plus an `engine_epoch` fence (`builtin-templates/agent/_backend/owner.go:1-12`, `main.go:64`).
- **Provider instances (IFACE-7):**
  - A provider declares `provides{instances:true}` and registers `ifaceInstances[provider][id] = prefix` via `PUT /iface-instances` (`netfn.go:1032-1131`).
  - Bindings address them as `<provider>#<id>`. It is one process with one RBAC identity for all instances (`plans/interfaces.md:140-208`).
  - Allowances can pin one: `iface:<svc>@<tile>#<inst>` — D32 even says "the dev instance only, for this org" (`plans/DECISIONS.md:754-763`).
  - This is the closest existing concept to a variant.
- **Template instances (D50):**
  - Separate tiles at new paths (`templates.go:68-175`).
  - `RenderTree` rewrites the template's own path in `xbin.json` and text files, so `res:apps/agent/*` becomes `res:<new>/*` (`builtins/builtins.go:283-343`).
  - Each instance roots its own scope and shares git history with the template (`templaterepo.go:106-141`).
- **Clone:** a new tile with the directory and `.git`; vault and resource data are never copied (`clone.go:17-26`).
- **D81 per-run actors:** goroutines inside one backend, keyed by run id in that tile's own sqlite. Not separate identities.
- **Reserved names:**
  - `tiles/`, `root`, `shell` for non-admin creates, and `:` in any path segment (`policy.go:132-166`)
  - `ReservedTop` (`.xbin`, vendor, data, home, homes, xbin, ingress, runtime, owner), reserved because a path becomes `X-XBin-From` (`util.go:59-70`)
  - `#` and `@` pass `ComponentPathOK` (`util.go:95-114`) but already mean something in binding refs and pseudo-slots, so they're unsafe as a deployment suffix.

### Where a main/dev split collides with the current code

1. **Caller identity is the path everywhere.** A dev process registered under the same path would overwrite main's self-registrations, and all of them deliver to `/api/<path>`, i.e. to main. Affected:
   - cron jobs and bus subscriptions (the agent re-registers `trig-<id>` subscriptions and schedules on every start)
   - `ifaceInstances` and `ingressHosts`
   - status reports, notify budgets, prefs
   - the vault access check
2. **Runner state and every `CompKey` artifact are keyed by path.** `runner.states` (`runner.go:155`) and the sockets, logs, build output, caches and cgroup leaf all need a deployment dimension.
3. **Resource keys need a deployment dimension:**
   - `ScopeKey` and `resLabel`
   - the kv bucket and its `kv:<bucket>` key label
   - `vaultPath`
   - the backup key
4. **One natural place to pin inbound traffic to main:** `Runner.Ensure(comp)` is the single funnel for everything in section 3.
5. **Source and watcher:** there is one source directory (`c.Dir`, bound read-only, `runner.go:630`) and one watcher that restarts the backend on change (`boot/serve.go:161-185`).
