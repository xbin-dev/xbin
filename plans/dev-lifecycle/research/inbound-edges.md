# Inbound edges into a tile and single-backend assumptions

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `edges`.

## Report

## Inbound edges into a tile, and single-backend assumptions

All paths are relative to `/home/magik6k/buxon/`. Anything marked (inference) is my reading, not stated in code or docs.

### 0. Headline findings

1. **Three primitives, all keyed by the component path alone.** Every inbound edge ends in one of these:
   - **HTTP:** `proxy.Proxy.ServeHTTP` or `ForwardIngress` calls `runner.Ensure(ctx, c)` plus `runner.Track(path)` (internal/proxy/proxy.go:182,196; internal/proxy/ingress.go:58,65).
   - **L4:** `runner.DialInto(ctx, path, proto, port)` does `Ensure`, `Track` and `relay.DialIn` (internal/runner/ingress.go:32-87).
   - **L3:** `netMux` provider→client TUN fds (internal/runner/netmux.go:14-51), spliced by `relay.Splice` (runner.go:506-571).

   `Runner.states` is `map[string]*state` keyed by path (runner.go:155,173-182). Each state holds exactly one `cur *instance` (runner.go:69-81).
   - (inference) If the primary deployment keeps the bare-path key and non-primary deployments get new keys, every edge below still reaches the primary with no change at the edge.
2. **Identity has no deployment dimension.**
   - Instance tokens map token→path (internal/auth/auth.go:219,445-462; runner.go:479) and resolve to `Principal{Component: path, Via: "instance"}` (auth.go:589-591).
   - Terminal tokens are (component, user) (auth.go:240-245,471-507).
   - Frame tokens are `b64(component)|b64(user)|exp|gen|sig` (internal/auth/frametoken.go:226-232).

   So a second copy of a backend is indistinguishable from the first, both to the broker (resources, vault, cron, bus, iface-instances, ingress-hosts, tile-report, notify) and to every callee: `X-XBin-From` is the path (auth.go:196-207; docs/auth.md:16-26).
3. **Self-registrations are re-asserted at every start and replaced by the last writer.**
   - The docs tell tiles to re-register at start: docs/resources.md:166 ("subscribe at every start"), workspace-template/AGENTS.md:534-535 ("re-report it on startup"), and the agent does so (builtin-templates/agent/_backend/main.go:67-68).
   - Keys: cron and bus by (path, name); iface-instances, ingress-hosts and status by path.
   - A second copy therefore overwrites or deletes the first copy's registrations.
4. **The internal dispatchers are synthetic requests into the same proxy.** Cron (internal/broker/cron.go:200-208), bus deliveries (bussubs.go:127-135) and archiver calls (backup.go:379-388) all reach whatever `/api/<path>` reaches.
5. **(inference, matters for pause) No restart path reuses a built artifact.**
   - `Ensure` always rebuilds when there is no current instance (runner.go:199-236).
   - These restart paths all go through it: the reaper marks the tile dirty (runner.go:808-812), as does the crash watch (runner.go:347); grant and binding changes (boot.go:497-501; netfn.go:824-846); provider nudges (runner.go:583-585); alwaysOn restarts (alwayson.go:121-136).
   - node, python and cgi run straight from `c.Dir` (runner.go:388-401,630; proxy.go:244-251).
   - The static plane serves the working tree with `Cache-Control: no-store` (internal/server/static.go:153).
   - So any edge that wakes a "paused" or pinned deployment runs working-tree code unless these paths are pinned.

### 1. Shared machinery

- **One handler, three front doors.** The console listener, the gateway socket (backends: `XBIN_GATEWAY` plus the instance token; internal/boot/serve.go:34-54) and tile origins in origins mode (internal/server/tileorigin.go:40-42,83-96) all use it. `/api/xbin/*` goes to the core mux; anything else goes to `ComponentAPI`, which is the proxy (internal/server/server.go:163,563-590).
- **Target.** `Registry.Resolve` takes the longest component prefix of the URL path (internal/registry/registry.go:519-536). There is one `*Component` per path, parsed from the working tree's xbin.json (registry.go:372-385,456-474).
- **Authorization.** `broker.Policy` (internal/broker/broker.go:478-490):
  - admin gets admin;
  - `p.Component == target.Path` gets admin (self);
  - `xbin/cron` and `xbin/bus` get the role chosen at registration;
  - anyone else gets `grantedRole`: grant rows, then the http-binding role, then the same-scope auto-grant (broker.go:418-452).
- **Start.** `Ensure` is a single-flight build+start with a blue/green swap (runner.go:187-355). Health is a socket dial within 5 s, or 60 s for VM backends (runner.go:312,822-833; vm.go:44-52). `Track` holds a backend against the reaper per connection (runner.go:243-258; runner/ingress.go:89-100).
- **Two generations already overlap.** Blue/green runs two generations of one tile for up to `drainDeadline` = 30 s (runner.go:44,319-324). Only VM tiles with file resources stop the old one first (vm.go:120-129).
  - The instance token is revoked at process exit, not at swap (runner.go:591-610), although D8 says "die at swap". The old generation can keep calling the gateway during the drain.
  - D81's flock design depends on this bounded overlap. Deployments make the overlap unbounded.

### 2. Inbound edge inventory

Each edge covers: code, how the target is named, how the backend is found or started, what changes for primary-only, and whether a non-primary deployment would want its own copy.

#### E1. `/api/<tile>/` proxy (console, gateway, tile origin)
- **Code:** proxy.go:128-235, in this order:
  1. `Resolve` (130).
  2. Template → 404 (135-139); no backend → 404 (140-144).
  3. Lifecycle → 409 with `X-XBin-Lifecycle` (148-158).
  4. `Policy` (160-171).
  5. `identify` strips every `X-XBin-*` header, the xbind cookies and any Bearer, then injects From, Role, User, User-Level and Viewed-By (275-305).
  6. cgi → `serveCGI`, one exec per request from `comp.Dir` (175-178,239-261).
  7. Otherwise `Runner.Ensure` with a 10 min ctx (180-191), then `Track` (196-197).
  8. ReverseProxy over one pooled transport per `g<N>.sock` (86-126,228); `?frame=` is consumed and not forwarded (199-203).
- **Target:** the URL path. The principal comes from the credential.
- **Found/started:** `Ensure` returns `s.cur.sock` when clean (runner.go:202-205); otherwise it builds and starts. Lazy.
- **Primary-only change (inference):** `Ensure` and `Track` resolve path → the primary deployment's state. Nothing else in the proxy needs to know.
- **Non-primary copy wanted: yes.** Dev frontends need it, and so do coding agents testing from terminals (workspace-template/AGENTS.md:153: `curl … $XBIN_URL/api/apps/thing/hello`). Constraints on a selector:
  - A path grammar collides with legal directory names: `ComponentPathOK` allows `@` and `~` (internal/util/util.go:97-115).
  - `@` is already the allowance-grammar separator: `res:<glob>@<role>`, `iface:<svc>@<tile>#<inst>` (internal/users/orgs.go:597-601,630-641). `#` is taken by instance/expose refs.
  - A caller-set `X-XBin-*` header is stripped by `identify` (proxy.go:276-280), so a selector must be consumed before it, the way `?frame=` is.
  - A credential-bound deployment needs a new frame-token shape: the verifier accepts only 4 or 5 fields (frametoken.go:253-265).

#### E2. Cron jobs (principal `xbin/cron`)
- **Code:**
  - `cronRunner` (cron.go:40-66) persists to `data/cron-jobs.json` (76-106).
  - Key is component + NUL + name (108). `add` replaces an existing key (110-130).
  - PUT forces `j.Component = p.Component` for non-admins (234-236). DELETE acts on the caller's own component (257-270).
  - `fire` skips non-enabled tiles (166-186).
  - Dispatch is a synthetic `POST /api/<comp><path>` as `{Component: "xbin/cron", Via: "cron", Role}` (cron.go:200-208; boot.go:492). `Policy` grants the registered role (broker.go:485-487).
  - `forget(path)` runs when a tile is created at that path (cron.go:146-164; create.go:188-190).
- **Target:** (component, path) stored on the job.
- **Found/started:** as E1, lazily.
- **Primary-only change:**
  - Dispatch already reaches the primary.
  - Registrations from a non-primary copy currently land on the primary's key (same `p.Component`). They must be refused, no-op'd, or keyed by (path, deployment) and dispatched to that deployment.
  - Backup manifests carry jobs by path (backup.go:79-81,198-213) and restore re-adds them (backup.go:282-290).
- **Non-primary copy wanted: likely.** Testing scheduled work needs it; the agent template's `sched-<id>` and `resume` jobs are examples (§3 F).

#### E3. Backup schedules and archiver calls
- **Code:**
  - `backupSchedule{Component, Schedule, Retention}` in `data/backup-schedule.json`, on the same scheduler, keyed by component (backup_cron.go:19-72).
  - `runScheduledBackup` → `doBackup` → `writeBackup`. The tar holds source, the scope's resource data (only when the tile roots its scope), the term-env layer and the tile's cron/bus registrations (backup.go:60-108).
  - `archiveDo` sends `PUT /api/<provider>/archive/<CompKey(comp)>` as Owner through `ProxyHandler` (backup.go:379-420; boot.go:510).
  - The provider is `bindings[comp]["@archive"]`, else `bindings["*"]["@archive"]` (backup.go:35-41).
  - Restore and offload call `StopBackendSafe(comp)` (backup.go:439,443-447,455) and delete scope data (backup.go:473-489).
- **Target:** the archiver tile's path, taken from the binding. This is an inbound edge into the archiver; the tile being backed up is read from disk, not called.
- **Found/started:** as E1 on the archiver.
- **Primary-only change:** nothing on the archiver side. On the backed-up side: choose which deployment's data goes into the tar (primary by default, inference), and make stop apply to every deployment.
- **Non-primary copy wanted: rarely.** It would need a distinct archive key, because `backupKey` is `CompKey(path)` (backup.go:45).

#### E4. Bus push subscriptions (D85, principal `xbin/bus`)
- **Code:**
  - `busSub{Name, Resource, Prefix, Component, Path, Role}` persisted to `data/bus-subscriptions.json` (bussubs.go:55-62,160-182).
  - Key is component + NUL + name (158). At most 64 per component (50,194-202).
  - PUT forces `s.Component = p.Component` for non-admins, and the subscriber must hold `reader` (391-435).
  - `publish` fans out (271-299) to one drain per subscription (303-335). `deliver` prunes subscriptions whose component is gone, drops for disabled components, re-checks `reader`, and POSTs as `xbin/bus` with a 2 min timeout (337-362) through `DispatchBodyViaProxy` (125-135; boot.go:493).
  - The SDK documents `Subscribe` as idempotent and safe to call at every start (sdk/xbin.go:350-361; docs/resources.md:166).
- **Target:** (component, path). The source is a bus resource plus a topic prefix.
- **Found/started:** as E1, lazily.
- **Primary-only change:** as E2. A non-primary `Subscribe` overwrites the primary's subscription of the same name, and `Unsubscribe` deletes it.
- **Non-primary copy wanted: yes, if buses become per deployment.** Dev events on a dev bus should be delivered to dev.

#### E5. Agent event triggers (D87) and the webhooks tile
- **Bus source:** the agent registers one subscription per enabled trigger, `trig-<id>` → `/trigger/bus/<id>`, and re-asserts them at start (builtin-templates/agent/_backend/triggers.go:441-470; main.go:68). Delivery is E4.
- **Push source:**
  - A tile bound to the agent's `inbox` provide (service agent-inbox, custom role `channel`) POSTs `/adapter/event` to its `XBIN_IFACE_*` URL. Webhooks does this for every bound agent (builtin-tiles/webhooks/backend/main.go:176-200,451-453). The call goes over the gateway and is E1 on the agent.
  - The agent matches `source='push' AND source_ref = X-XBin-From` (triggers.go:396; channels.go:223).
- **Webhooks inbound:** public `POST /hook/{id}` arrives through ingress (E7) as `From: ingress`, or from the tile's admin (main.go:134-140,434; xbin.json publishes only `/hook/*`).
- **Primary-only change:** none for routing (the binding goes to the agent's primary). But a non-primary webhooks or bridge copy pushes with the same From, so it fires the primary agent's production triggers.
- **Non-primary copy wanted: yes**, to test triggers against a dev agent. That needs the parallel fabric or an explicit dev binding.

#### E6. alwaysOn (D84), lazy start, idle reaper
- **Boot:** `stepAlwaysOn` is the last boot step (boot.go:111,705-708). `WakeAlwaysOn` walks `Reg.Components()` and skips tiles that can't run, are running, are building or have failed (alwayson.go:45-61). `aoStart` runs at most 2 at a time and calls `Ensure` (alwayson.go:63-84).
- **Other wake points:** vault unseal (broker.go:214-236), lifecycle enable (lifecycle.go:101-105), every rescan (serve.go:183).
- **Crash restart:** the crash-watch goroutine (runner.go:328-350).
  - 3 exits within 30 s set a sticky `lastErr` until the next change.
  - Otherwise it sets `dirty = true` and calls `afterExit`: per-path backoff from 1 s doubling to 5 min, reset after 10 healthy minutes (alwayson.go:86-137).
- **Lazy start:** any E1 or E8 edge.
- **Reaper:** runs every minute; stops a backend when `active == 0`, idle > 30 min and `!isAlwaysOn(path)`, and marks it dirty (runner.go:45,798-820).
- **Keys:** `ao.backoff`, `ao.upSince` and `ao.pending` by path (alwayson.go:30-35). `isAlwaysOn` reads the one manifest (alwayson.go:38-41).
- **Primary-only change (inference):** boot, wake and restart apply to the primary only. The reaper's alwaysOn exemption must not cover non-primary states.
- **Non-primary copy wanted: only when explicitly opted in.** alwaysOn tiles are exactly the ones holding an exclusive external connection (D84; builtin-templates/agent-messaging-bridge/xbin.json).

#### E7. Ingress HTTP (ING-1..6, D79)
- **Runtime listener:**
  - `--ingress-listen` → `HTTPHandler{Source: "runtime"}` (serve.go:60-100).
  - `IngressLookup(source, host)` tries the exact host first, then a zone plus `ws.IngressHosts[comp]`; only live tiles match (ingressfn.go:93-146).
  - `ForwardIngress` returns 503 on the lifecycle gate, strips `X-XBin-*` and the session cookies, sets `From: ingress` and `X-XBin-Ingress-Host`, then calls `Ensure` and `Track` (proxy/ingress.go:31-95).
- **Terminator tiles (traefik):**
  - One forward socket per terminator path, `<RunDir>/igw-<CompKey(source)>.sock` (internal/ingress/forwards.go:37-66; boot.go:521-537).
  - The terminator reaches it from its netns via the relay HostFwd `10.0.2.2:8642` → `hostDial unix:` (ingressfn.go:34-36,313-317; runner.go:532-538; runner/ingress.go:108-114). Env `XBIN_INGRESS_FORWARD_URL` (resources.go:128-130).
  - The socket's handler only serves routes whose binding names that terminator (internal/ingress/ingress.go:129-158).
- **`GET /ingress-routes`:** a terminator reads the routes with `rt.Source == p.Component`; admins read all (ingressfn.go:613-639). Traefik polls it every 15 s (builtin-tiles/traefik/backend/main.go:35,87,271).
- **`PUT /ingress-hosts`:** self-scoped (`comp = p.Component`), bounded to the tile's delegated zones and conflict-checked. It replaces `ws.IngressHosts[comp]` in the root xbin.json, publishes `grants` and reconciles (ingressfn.go:491-607).
- **Target:** Host → `Route.Component` (bindings plus IngressHosts). Terminators are identified by their socket; registrations by the principal's path.
- **Primary-only change:** none for routing, since a Route carries only the Component. But the forward door, the route read and a zone tile's `/ingress-hosts` writes must be primary-only, because every copy shares the Component.
- **Non-primary copy wanted: maybe**, for a preview hostname. That needs a route that names a deployment. `BindRef` can take an extra field additively (registry.go:238-278). Exclusivity per hostname, zone and host port (ING-5, D79) stays as is.

#### E8. L4 streams and hairpin
- **Streams:**
  - `IngressStreamSpecs` lists live tiles with a backend whose expose is bound to `runtime` (ingressfn.go:242-259).
  - `Streams.Reconcile` keeps one listener per component+slot+proto+listen+port (internal/ingress/streams.go:24-35,104-126).
  - On accept, `Dial` = `run.DialInto` (boot.go:520; streams.go:176-195), which does `Ensure`, `Track` and `relay.DialIn`, retrying 4 times (runner/ingress.go:21-87).
- **Hairpin (ING-6):** the relay's DNS answers published names with VIP 10.0.2.4 (internal/sandbox/relay/types.go:16; runner.go:521-531). `HairpinDial(port)` then calls `DialStream(spec.Component)` or dials the builtin listener (ingressfn.go:458-470).
- **Primary-only change:** `DialInto(path)` resolves to the primary.
- **Non-primary copy wanted: rarely.** A dev port is another host port and would need the route-level deployment from E7.

#### E9. Stream interfaces (tile→tile, `XBIN_IFACE_<slot>_ADDR`)
- **Code:**
  - The consumer's env gets `10.0.2.2:<20000+i>` (resources.go:131-135).
  - Its relay HostFwd map comes from `IngressFwdFor`: `stream:<prov>:<port>` (ingressfn.go:288-327). `hostDial` turns that into `DialInto(prov)` (runner/ingress.go:115-127).
  - The provider gets inbound plumbing because `IngressNetFor` sees the sibling's binding (ingressfn.go:333-356).
- **Primary-only change:** none. `DialInto` reaches the provider's primary.
- **Non-primary copy wanted: fabric decision.** The forward map is computed per spawned instance, so a dev consumer could be pointed at the provider's dev deployment.

#### E10. lan-ingress and net-provider splices (L3)
- **Provider side:**
  - `NetProviderRoster` lists bound net clients plus lan-ingress clients (`<client>#<slot>`). Link addresses come from the sorted client index (netfn.go:182-233; ingressfn.go:363-406).
  - At provider start, one TUN per client is registered (runner.go:545-552). `netMux.register` closes any previous fd for that key (netmux.go:22-32).
  - Every client is then restarted via `r.Changed` (runner.go:572-588).
  - `netMux.clear` has no caller (netmux.go:44-51).
- **Client side:** `NetClientTarget` / `NetLinksFor` (netfn.go:239-251; ingressfn.go:420-442) → `ensureProvider`, which is `Ensure` on the provider (netmux.go:55-63) → `netmux.get(provider, client[#slot])` → `relay.Splice(fd, pfd)` (runner.go:506-515,559-571). The splice reads the provider fd directly (internal/sandbox/relay/splice_linux.go:15-39).
- **Primary-only change:** only a provider's primary may register the roster, and only a client's primary may splice.
- **Non-primary copy wanted: (inference) costly.** Links are numbered by position, so adding a roster entry renumbers links and restarts the provider.

#### E11. HTTP interface bindings and grant-based calls
- **Backend:** `EnvFor` injects, at spawn, `XBIN_IFACE_<SLOT>_URL=http://xbin/api/<prov>[<instance path>]` or the multi-slot JSON (resources.go:99-123), from `HTTPSlots` (netfn.go:336-381).
  - The call goes through `xbin.Client()` over the gateway with the instance token (sdk/xbin.go:205-249) and becomes E1.
  - The Policy role comes from `httpBindingRole` (netfn.go:283-311) or from grant rows and `uses` (broker.go:418-452).
- **Frontend:** the `xbin-interfaces` meta (static.go:447; netfn.go:383-408) → frame-token fetch to `/api/<prov>`.
- **Found/started:** as E1 on the provider, lazily. Binding and instance changes restart the consumer and the provider (netfn.go:824-846,1115-1129; boot.go:497-501).
- **Primary-only change:** none.
- **Non-primary copy wanted:** this is the parallel-fabric question. It requires the caller's principal to carry its deployment, and the proxy to pick the target's deployment from it.

#### E12. `/notify` and push (D94)
- **Code:**
  - `POST /api/xbin/notify` (boot/push.go:155) → `notifier` (push/api.go:287-300): an instance principal may notify any reader of the tile; a frame or terminal principal only its own user.
  - `APINotify` checks mute, a per-tile budget (356-363) and `CanRead(user, tile)`, and sets the link `c/<tile>/<link>` (368).
  - SDK `NotifyUser` (sdk/notify.go:52-80); the agent's "needs you" pushes use it (agent main.go:59).
- **Direction:** outbound to people. The link is an inbound frontend edge that opens the primary.
- **Primary-only change:** none for links. A non-primary copy calling it sends real pushes and spends the tile's budget.
- **Non-primary copy wanted:** at most "notify only the developer" (inference).

#### E13. The bus itself
- **Code:** `POST /api/xbin/bus/publish` (resources.go:410-437) → `Hub.Publish{Type: bus, Topic: res:<scope>/<name>/<topic>}`. It reaches `/ws/events` subscribers filtered by `busFilter` on `p.Component`'s `reader` grant (resources.go:441-458; server.go:592-612), plus the E4 push subscriptions. The hub drops slow subscribers (internal/events/events.go:57-72).
- **Target:** the resource, not a tile. Consumers include other tiles' frontends and backends.
- **Primary-only change:** none for routing. But a non-primary publisher reaches every production consumer unless the bus namespace is per deployment. For example, the agent publishes run events on `res:apps/agent/events` (builtin-templates/agent/scope.json).

#### E14. Terminals, agent sessions, `bx`
- **Terminals:** `/ws/term?cwd=<tile>` (server.go:165-168) checks `CanTerminalTile(rel)`; `prepare` mints `MintTerminal(rel, user)` (internal/term/term.go:180-246,290-342). The shell gets `XBIN_COMPONENT=rel` and `XBIN_TOKEN` (term.go:462,474-477).
- **Agent sessions:** `POST /api/xbin/term/sessions {cwd}` uses the same token model (internal/server/agentapi.go:28,66; internal/term/agent.go:129-171; D74).
  - Sessions are keyed by id and carry `Cwd` (term.go:159; sessions.go:88).
  - Only one live session may mount a tile's persistent layer (`envHeld` by CompKey; term.go:160,481-499).
  - History lives at `data/agent-history/<user>/<tile>/` (D75).
- **Calls from a terminal or agent:** `$XBIN_URL/api/<tile>` runs as `Principal{Component: tile, Via: "terminal"}` (auth.go:498-507). That is E1 on the primary, as self-admin.
- **bx:**
  - `bx status` defaults to `$XBIN_COMPONENT` and calls `/tile-status` (cmd/bx/main.go:349-376). The server takes the first `Inspect()` row whose Path matches (boot/api.go:87-118, 101-107).
  - `bx logs` reads `.xbin/log/<CompKey>.log` directly (cmd/bx/main.go:494-530).
  - `bx agent` targets `$XBIN_COMPONENT` (cmd/bx/agent.go:179,244). `bx cron ls` (main.go:1122-1136).
  - There is no generic "call a tile" command; curl is the documented way (docs/bx.md:4-6).
- **Primary-only change:** none.
- **Non-primary copy wanted: yes (inference).** The terminal is the natural dev client. A per-session target deployment (a token claim or env) would make `curl …/api/<self>` and `bx status/logs` address dev.

#### E15. Frontend edges other than a human browsing
- **Edges:**
  - other tiles embedding or navigating (`<bx-frame src>`, `xbin.url`);
  - push taps (`c/<tile>/…`);
  - the native runtime document `/c/<tile>/?native=1` (internal/server/native.go:50-97; docs/compat.md native table);
  - tile origins `t-<HMAC(path)>` (internal/auth/assettoken.go:178-188; tileorigin.go:17-20);
  - the frame token minted for `compPath`, plus the `xbin-component` meta (static.go:435-436,461-462).
- **Served from:** the working tree, `no-store` (static.go:69,153,193-219).
- **Primary-only change:** if hot reload is attached to a non-primary deployment, or paused, the primary's documents must come from its own code, not the working tree.
- **Non-primary copy wanted: yes.** A dev frontend needs its own address and credential (an origin label and a frame-token claim).

#### E16. Lifecycle gates (where starting is refused)
- **Where:**
  - proxy 409 (proxy.go:148-158); `ForwardIngress` 503 (proxy/ingress.go:37-40);
  - `liveForIngress` — routes, streams and forward doors disappear (ingressfn.go:93-96);
  - `Ensure` checks `ShouldRun` = enabled ∧ ¬EncryptionHold (runner.go:191-196; boot.go:504-508; resenc_wire.go:102-129);
  - `Changed` skips (runner.go:272-275); WakeAlwaysOn and afterExit skip (alwayson.go:50-52,126);
  - cron `fire` skips (cron.go:167-173); bus `deliver` drops (bussubs.go:342-344);
  - templates never run (proxy.go:135-139; registry.go:391-402); `Pending` hides offloaded tiles (broker.go:558-562);
  - disable/offload stop the backend (lifecycle.go:101-105; backup.go:439,455); sealing stops tiles that use file resources (resenc_wire.go:151-161).
- **State:** `ws.Lifecycle[path]` (registry.go:229-235,357-365).
- **Primary-only change:** the gates stay per tile, but `Stop(path)` (runner.go:734-748), `StopBackendSafe` and `SealResources` must stop every deployment.
- **Non-primary copy wanted:** a per-deployment stop would be a new state outside `ws.Lifecycle` (inference).

### 3. Single-backend assumptions (break or double-fire with two live copies)

**A. Process and run keys**
1. `states` is keyed by path with one `cur` each (runner.go:69-81,155). `Status()` is a map by path (773-796) and `Inspect()` yields one row per state; both feed `/backends` and `/tile-status` (boot/api.go:22-28,87-118).
2. **Sockets collide.** Sockets live at `RunDir/CompKey(path)/g<gen>.sock` and `os.Remove(sock)` runs before start and on stop (runner.go:408-413,728). Two states in one dir with separate gen counters would delete each other's live socket. `sweepTransports` then evicts the pooled transport (proxy.go:117-126).
3. **One build artifact.** `.xbin/build/<CompKey>/bin` is shared (runner.go:366-377), and under isolation it is bind-mounted read-only into the running sandbox (runner.go:642-644).
4. **One log.** `.xbin/log/<CompKey>.log` is opened append-only by every generation (runner.go:464-471). It is read by obs/logs.go:69 and cmd/bx/main.go:511; the env-layer build log goes to the same file (runner/env.go:89).
5. **One cgroup leaf.** `comp-<CompKey>` carries one memory/pids cap, and any generation's exit tries to remove it (runner.go:480-484,602-604; internal/cgroup/cgroup_linux.go:89,215-219). The VM leaf is sized for exactly two guests (vm.go:99-107).
6. alwaysOn maps are keyed by path (alwayson.go:30-35), and the reaper exemption is read from the manifest (runner.go:808).
7. Stats are keyed by path (runner/stats.go:122-199).

**B. Identity**
8. **Same principal.** The instance principal is the path (auth.go:219,445-462,589-591). A second copy gets the same grants, resources, vault (broker/vault.go:151-176; value reads are backend-only, 188) and `X-XBin-From`.
9. **Self-calls cross copies.** `XBIN_COMPONENT` / `xbin.Self()` is the path (runner.go:419; sdk/xbin.go:81-82), and `Policy` makes self-calls admin (broker.go:482-484). A non-primary copy calling `/api/<self>` lands on the primary as admin.
   - The agent's keep-alive `GET /api/<Self()>/engine/hold` (owner.go:154-184) therefore holds the primary, not itself.
10. **Callee state keyed by From.** Agent channels, the outbox and push triggers (outbox.go:248-305; triggers.go:396). docs/agent-inbox.md: hello is "idempotent per (adapter, account.id)".

**C. Self-registrations (last writer wins)**
11. **Cron:** key (cron.go:108); DELETE acts on the caller's own component (257-270).
12. **Bus:** key (bussubs.go:158); the 64-per-component cap is shared (50,194-202).
13. **Iface instances:** each PUT replaces the path's whole map in the root xbin.json and restarts every bound consumer (netfn.go:1102-1129; registry.go:218-223). No shipped tile uses it; third-party tiles may.
14. **Ingress hosts:** replace-all by path, then reconcile (ingressfn.go:564-581). No shipped tile uses it.
15. **Status:** in memory by path (internal/obs/obs.go:25; status.go:61-116) and cleared on any `build-start` for the path (status.go:129-148). The bridge reports its phase this way (agent-messaging-bridge/_backend/main.go:94-116).
16. **Backups:** manifests carry cron/bus registrations by path, and restore re-adds them (backup.go:79-86,282-302).

**D. xbind events keyed by path**
17. `build-start`, `build-error`, `build-ok` (runner.go:285-352) and `reload` (serve.go:178; lifecycle.go:114) carry only the component.
   - bx-frame reloads or shows its overlay when `e.component === src` (web/bx-frame.js:366-381).
   - The shell reloads on `reload` (workspace-template/shell/bx-shell.js:195); the xbin app follows /ws/events (D99).
   - A non-primary build error would appear on every user's primary frame.

**E. L3 plumbing**
18. The netmux holds one fd per (provider, client), and a provider start nudges its clients (netmux.go:22-32; runner.go:545-588).
   - Two splicers reading one provider fd split packets between them, and both copies use the same link address (splice_linux.go:15-39; netfn.go:213-216,239-251).
   - Blue/green already makes this briefly racy.

**F. Templates and tiles**
19. **Agent engine (D81), shared db.** The engine takes a flock on `<db>.engine` (main.go:64; owner.go:33-62) and fences writes with an epoch (engine.go:10-17,118-132,276-290). It is built for a blue/green overlap of at most 30 s on a shared db. With a shared db, a second copy blocks in flock indefinitely, while its HTTP handlers still write inbox rows that the primary drives (engine.go:11-14).
20. **Agent engine, separate dbs.** Two engines run, and each re-registers at every start (main.go:67-68): `reRegisterSchedules` PUTs `sched-<id>` (schedule.go:166-216) and `reRegisterTriggers` PUTs `trig-<id>` (triggers.go:441-470).
   - Ids from different dbs collide, so a dev copy rewrites the primary's jobs and subscriptions (schedule, target path, resource, prefix), or deletes them when its own entry is disabled.
   - Takeover's `clearWakeJobs` deletes the primary's `resume` job (owner.go:232-238).
   - A dev shutdown's `registerResumeJob` makes `/tick` fire at the primary (owner.go:220-228).
21. **Messaging bridge (D86).**
   - It is alwaysOn (xbin.json) and reads the platform token from the tile vault, so each copy opens its own platform connection on the same token (platform.go:29-60).
   - hello is keyed by (From, account), and outbox rows are keyed by From (outbox.go:248-305). Both copies post the same replies until one acks, because each keeps its own sent-record (docs/agent-inbox.md; D86).
22. **Webhooks:** pushes carry From = the webhooks path (main.go:176-200), so a non-primary copy fires production agent triggers.
23. **Traefik:** polls the routes bound through its path every 15 s and renders ACME resolvers for them (traefik main.go:35,87,129-139,271). (inference) A second copy would request certificates for production hostnames whose challenges only reach the primary.
24. **Devbox:** podman state on `res:apps/devbox/storage` with `cap:containers`, plus an ssh stream expose (builtin-tiles/devbox/xbin.json). (inference) Two copies on one storage are unsafe.

**G. Pause and pinning** — see §0.5.

**H. One manifest per path**
25. The registry keeps one manifest per path (registry.go:456-474). Everything read at spawn comes from the working tree's xbin.json: runtime, entry, `uses`→env, interfaces, exposes, alwaysOn, vm, setup. (inference, overlaps the git brief) A deployment pinned to other code still gets the working tree's wiring.

### 4. Compat notes
- **HTTP is additive (compat.md rule 2).** `/backends` (a map by path), `/tile-status`, `/cron/jobs`, `/bus/subscriptions`, `/ingress`, `/runtime`, `/tile-report` and `/components` can only gain fields; their keys stay paths.
- **Manifest and SDK are additive (rules 7-8).** `xbin.Self()` keeps returning the path. New env is additive. For opted-in tiles, turning a non-primary `Subscribe`/cron PUT into an error would break "succeeds today keeps succeeding"; an accepted no-op or separate keying keeps it (inference).
- **Boot (rules 1, 9).** The first boot of an aged workspace must not change the root xbin.json, so no deployment state may be written at boot.
- **`X-XBin-From` is documented** (docs/auth.md:16-26) and matched by callees such as the agent. Giving non-primary callers a new form would affect every callee, but only for opted-in tiles.
- **Internals are free.** `.xbin/` contents and the on-disk shape of `data/` are explicitly not promised (compat.md "What this does not promise"), so per-deployment run, log and build keys can change freely. docs/protocol.md:2320 and docs/elements.md:524 do name `.xbin/log/<compkey>.log`, so the primary should keep that file.
- **(inference) Zero change for tiles that don't opt in holds if:**
  - a tile with only one deployment keeps every key exactly as today; and
  - all new behaviour is gated on the tile having more than one deployment.

## Surfaces this feature touches

### Component API proxy (/api/<tile>/ on console, gateway, tile origins)

- **Paths:** `/home/magik6k/buxon/internal/proxy/proxy.go:128-305`, `/home/magik6k/buxon/internal/registry/registry.go:519-536`, `/home/magik6k/buxon/internal/server/server.go:163,563-590`, `/home/magik6k/buxon/internal/boot/serve.go:34-54`, `/home/magik6k/buxon/internal/server/tileorigin.go:40-42`
- **Today:** Finds the tile by longest path prefix. Gates on template, backend and lifecycle, then applies broker Policy. Strips inbound X-XBin-*, cookies and Bearer and injects the verified From/Role/User. Then calls Runner.Ensure(ctx,c) + Track(path) and reverse-proxies to the single current g<N>.sock over a pooled transport. cgi runs one exec per request from comp.Dir. Keyed by component path.
- **Change needed:** Ensure and Track must resolve path to the primary deployment's state. Add an optional explicit selector for non-primary targets: either consumed before identify() the way ?frame= is (proxy.go:199-203), or carried by the credential. cgi needs a per-deployment code dir.
- **Compat:** Zero change for tiles with one deployment if the primary keeps the bare-path state key. A selector must not be a legal path grammar: util.go:97-115 allows @ and ~, @ is the allowance separator (orgs.go:597-641), and # is taken by instance/expose refs.

### Runner per-process state and artifacts

- **Paths:** `/home/magik6k/buxon/internal/runner/runner.go:55-182,187-355,359-405,407-612,717-820`, `/home/magik6k/buxon/internal/runner/rundir.go`, `/home/magik6k/buxon/internal/runner/vm.go:99-129`, `/home/magik6k/buxon/internal/runner/inspect.go`, `/home/magik6k/buxon/internal/runner/stats.go:122-199`, `/home/magik6k/buxon/internal/cgroup/cgroup_linux.go:89,215-219`
- **Today:** states map keyed by path, one cur instance each. Socket at RunDir/CompKey/g<gen>.sock, removed before start and on stop. Build output at .xbin/build/<CompKey>/bin, log at .xbin/log/<CompKey>.log, cgroup leaf comp-<CompKey>. The VM leaf is sized for two generations. Stats and Inspect are per path.
- **Change needed:** Key every per-process artifact by (path, deployment), with the primary keeping the legacy keys. Stop, StopAll, Status, Inspect and the reaper must iterate deployments. Decide whether deployments share one cgroup cap or get their own.
- **Compat:** .xbin contents are not promised (docs/compat.md). Status/Inspect output feeds /backends and /tile-status, whose shapes may only grow.

### alwaysOn, lazy start and idle reaper

- **Paths:** `/home/magik6k/buxon/internal/runner/alwayson.go:30-137`, `/home/magik6k/buxon/internal/runner/runner.go:45,328-350,798-820`, `/home/magik6k/buxon/internal/boot/boot.go:111,705-708`, `/home/magik6k/buxon/internal/boot/serve.go:183`, `/home/magik6k/buxon/internal/broker/broker.go:214-236`, `/home/magik6k/buxon/internal/broker/lifecycle.go:101-105`
- **Today:** WakeAlwaysOn runs at boot, unseal, enable and every rescan, for every alwaysOn component. Per-path backoff maps (1 s doubling to 5 min). The crash-loop breaker trips at 3 exits in 30 s. The reaper stops non-alwaysOn backends after 30 min idle and marks them dirty.
- **Change needed:** Boot, wake, restart and backoff apply to the primary only. Non-primary deployments are lazy and reapable unless opted in; the reaper checks a per-deployment alwaysOn instead of the manifest flag.
- **Compat:** Existing alwaysOn tiles are unchanged, because their only deployment is the primary.

### Implicit restart and rebuild paths (pause semantics)

- **Paths:** `/home/magik6k/buxon/internal/runner/runner.go:199-236,262-281,328-350,388-401,572-588,630,808-812`, `/home/magik6k/buxon/internal/boot/boot.go:494-503`, `/home/magik6k/buxon/internal/broker/broker.go:648-666`, `/home/magik6k/buxon/internal/broker/netfn.go:824-846,1115-1129`, `/home/magik6k/buxon/internal/proxy/proxy.go:239-261`
- **Today:** Every restart goes through Ensure, which rebuilds from the working tree: crash, reap, grant/binding/instance change, provider nudge, alwaysOn restart. node, python and cgi execute from c.Dir at runtime.
- **Change needed:** Restarts of a paused or pinned deployment must use its pinned code or artifact. Grant, binding and instance restarts must fan out to every deployment, since grants are per tile.
- **Compat:** Default hot-reload behaviour is unchanged when nothing is paused and there is one deployment.

### Auth principals and tokens

- **Paths:** `/home/magik6k/buxon/internal/auth/auth.go:54-84,196-207,219,240-245,445-507,543-648`, `/home/magik6k/buxon/internal/auth/frametoken.go:209-296`, `/home/magik6k/buxon/internal/auth/assettoken.go:178-188`
- **Today:** Instance token maps to the path; terminal token to (path, user); frame token to (component, user, gen). Principal has no deployment field. X-XBin-From is the path.
- **Change needed:** Add a deployment dimension so the broker can route resources and self-calls and callees can tell copies apart: a Principal field, or distinct tokens. Frame and terminal tokens need a claim for dev frontends and terminals.
- **Compat:** The frame token parser accepts only 4 or 5 parts (frametoken.go:253-265), so a new claim needs a new shape. Principal.From() must stay the bare path for primaries (docs/auth.md:16-26).

### Backend env and SDK self-identity

- **Paths:** `/home/magik6k/buxon/internal/runner/runner.go:415-425`, `/home/magik6k/buxon/internal/broker/resources.go:71-144`, `/home/magik6k/buxon/sdk/xbin.go:81-82,189-317`, `/home/magik6k/buxon/builtin-templates/agent/_backend/owner.go:154-184,220-238`
- **Today:** XBIN_COMPONENT is the path. XBIN_TOKEN is per generation. XBIN_RES_* and XBIN_IFACE_* are computed per path at spawn. Tiles build self URLs, vault paths and resource ids from Self().
- **Change needed:** Non-primary copies need their own token and per-deployment resource env. A copy's self-calls to /api/<Self()> must route to that copy: either Self() stays the path and the gateway routes by principal, or another mechanism. Add an additive env such as the deployment name.
- **Compat:** SDK rule 8: Self() keeps returning the path, and existing env names keep their meaning.

### Cron (xbin/cron)

- **Paths:** `/home/magik6k/buxon/internal/broker/cron.go:24-270`, `/home/magik6k/buxon/internal/boot/boot.go:492`, `/home/magik6k/buxon/internal/broker/create.go:188-190`, `/home/magik6k/buxon/internal/broker/backup.go:79-81,198-213,282-290`
- **Today:** data/cron-jobs.json keyed by (component, name). Non-admins can only register for their own component. Dispatch is a synthetic POST /api/<comp><path> as xbin/cron through the proxy, with lazy start. Firing is skipped when the tile is not enabled.
- **Change needed:** Dispatch to the primary stays as is. PUT and DELETE from non-primary principals must not touch the primary's keys: refuse, no-op, or key by (component, deployment) and dispatch to that deployment. forget(path) must cover deployment keys too.
- **Compat:** Persisted rows and API rows gain an optional field only.

### Backup schedules and archiver dispatch

- **Paths:** `/home/magik6k/buxon/internal/broker/backup_cron.go:19-160`, `/home/magik6k/buxon/internal/broker/backup.go:25-120,198-302,379-489`, `/home/magik6k/buxon/internal/broker/lifecycle.go:22-117`
- **Today:** Per-component schedule on the same scheduler. The tar holds source, scope data, term-env and cron/bus registrations, and is PUT to /api/<archiver>/archive/<CompKey> as Owner through the proxy. Restore and offload stop the backend by path.
- **Change needed:** Choose which deployment's data is backed up (primary by default). Restore, offload and disable must stop every deployment. Optionally give dev data its own archive key.
- **Compat:** The backup manifest may only grow, so old archives still restore.

### Bus push subscriptions (xbin/bus)

- **Paths:** `/home/magik6k/buxon/internal/broker/bussubs.go:26-449`, `/home/magik6k/buxon/sdk/xbin.go:338-384`, `/home/magik6k/buxon/docs/resources.md:140-172`
- **Today:** data/bus-subscriptions.json keyed by (component, name), at most 64 per component, only for the caller's own component. Delivery POSTs /api/<comp><path> as xbin/bus through the proxy; the reader grant is re-checked per delivery.
- **Change needed:** Same treatment as cron. If buses become per deployment, a subscription's resource resolves in the subscriber deployment's namespace.
- **Compat:** Subscribe is documented as idempotent at every start, so it must keep succeeding for opted-in tiles.

### Bus publish, events hub and frontend bus

- **Paths:** `/home/magik6k/buxon/internal/broker/resources.go:408-458`, `/home/magik6k/buxon/internal/events/events.go`, `/home/magik6k/buxon/internal/server/server.go:592-612`, `/home/magik6k/buxon/web/xbin-client.js`
- **Today:** Publish checks writer on the res: target, then fans out to /ws/events subscribers (frontends and backends, filtered by reader grant on p.Component) and to push subscriptions.
- **Change needed:** If resources are per deployment, a non-primary publisher publishes into its deployment's namespace, and non-primary frontends subscribe there.
- **Compat:** Topics stay res:<scope>/<name>/<topic> for primaries.

### Agent template engine, schedules and triggers (D81, D87)

- **Paths:** `/home/magik6k/buxon/builtin-templates/agent/_backend/main.go:49-88`, `/home/magik6k/buxon/builtin-templates/agent/_backend/owner.go:1-238`, `/home/magik6k/buxon/builtin-templates/agent/_backend/engine.go:1-30,83,118-132,276-290`, `/home/magik6k/buxon/builtin-templates/agent/_backend/schedule.go:164-216`, `/home/magik6k/buxon/builtin-templates/agent/_backend/triggers.go:380-470`, `/home/magik6k/buxon/builtin-templates/agent/_backend/outbox.go:225-305`, `/home/magik6k/buxon/builtin-templates/agent/_backend/channels.go:223`
- **Today:** flock on <db>.engine plus epoch fencing. Self-hold via /api/<Self()>/engine/hold. A resume cron job is left on shutdown and cleared at takeover. sched-<id> cron jobs and trig-<id> bus subscriptions are re-asserted at every start. Adapters and push sources are identified by X-XBin-From.
- **Change needed:** Every registration must be per deployment, or suppressed for non-primary copies. The self-hold must reach its own deployment. resume and clearWakeJobs must not cross deployments.
- **Compat:** Template instances are forked copies (D50/D72), so a template-side fix only reaches owners who pull it. Isolation on the xbind side that needs no tile change is the zero-change route (inference).

### Agent messaging bridge template (D86)

- **Paths:** `/home/magik6k/buxon/builtin-templates/agent-messaging-bridge/xbin.json`, `/home/magik6k/buxon/builtin-templates/agent-messaging-bridge/_backend/main.go:94-116`, `/home/magik6k/buxon/builtin-templates/agent-messaging-bridge/_backend/platform.go:28-80`, `/home/magik6k/buxon/builtin-templates/agent-messaging-bridge/_backend/agent.go:18,66-160`, `/home/magik6k/buxon/docs/agent-inbox.md`
- **Today:** alwaysOn. Platform credentials come from the tile vault, one connection per token. It says hello at every start, pulls the agent outbox over SSE keyed by its From, and reports its phase via xbin.Status.
- **Change needed:** A non-primary copy must not be alwaysOn by default and must not share the platform token. Its calls to the agent need a distinct identity or a dev agent.
- **Compat:** No change for existing bridges while non-primary copies are opt-in and not alwaysOn.

### Webhooks builtin tile (D87)

- **Paths:** `/home/magik6k/buxon/builtin-tiles/webhooks/xbin.json`, `/home/magik6k/buxon/builtin-tiles/webhooks/backend/main.go:134-200,434-465`
- **Today:** Public /hook/* arrives through ingress at the primary as From ingress. Deliveries are forwarded to the bound agents' /adapter/event, carrying the webhooks path as From.
- **Change needed:** A non-primary copy gets no ingress (primary only) and must not push to production agents under the same From. This depends on the fabric decision.
- **Compat:** Unchanged by default.

### Ingress HTTP routing and terminator forward doors

- **Paths:** `/home/magik6k/buxon/internal/broker/ingressfn.go:23-146,261-273,313-317,609-639`, `/home/magik6k/buxon/internal/proxy/ingress.go:26-95`, `/home/magik6k/buxon/internal/ingress/ingress.go:124-158`, `/home/magik6k/buxon/internal/ingress/forwards.go:14-110`, `/home/magik6k/buxon/internal/boot/boot.go:518-549`, `/home/magik6k/buxon/internal/boot/serve.go:56-100`, `/home/magik6k/buxon/builtin-tiles/traefik/backend/main.go:35,87,129-139,175,271`
- **Today:** Host resolves to Route{Component} from bindings plus IngressHosts; ForwardIngress then calls Ensure/Track (primary). One forward socket igw-<CompKey>.sock per terminator path, reached via relay HostFwd 10.0.2.2:8642. Terminators poll /ingress-routes scoped by Source == p.Component.
- **Change needed:** Nothing for routing to the primary. For terminators, the forward door, route reads and ACME must be primary-only. An optional deployment field on BindRef would enable preview hostnames.
- **Compat:** BindRef marshals bare refs as strings (registry.go:254-278), so an extra optional field keeps old xbin.json files parsing.

### Ingress host self-registration

- **Paths:** `/home/magik6k/buxon/internal/broker/ingressfn.go:486-607`, `/home/magik6k/buxon/internal/registry/registry.go:224-228`
- **Today:** PUT /ingress-hosts replaces IngressHosts[path] in the root xbin.json for the caller's path, conflict-checks, and reconciles listeners.
- **Change needed:** Refuse or no-op it for non-primary principals, unless dev routes exist, in which case keep separate registrations.
- **Compat:** No shipped tile calls it; third-party zone tiles are unchanged by default.

### L4 streams and hairpin

- **Paths:** `/home/magik6k/buxon/internal/ingress/streams.go:14-280`, `/home/magik6k/buxon/internal/runner/ingress.go:21-137`, `/home/magik6k/buxon/internal/broker/ingressfn.go:240-259,444-482`, `/home/magik6k/buxon/internal/sandbox/relay/types.go:16`
- **Today:** One host listener per (component, slot, proto, listen, port). Accepted connections go to DialInto(path): Ensure, Track, then relay.DialIn. Hairpin VIP flows take the same route.
- **Change needed:** DialInto resolves the primary. Dev ports would need the route-level deployment field.
- **Compat:** Unchanged.

### Stream interfaces (tile to tile)

- **Paths:** `/home/magik6k/buxon/internal/broker/ingressfn.go:275-356`, `/home/magik6k/buxon/internal/broker/resources.go:131-135`, `/home/magik6k/buxon/internal/runner/ingress.go:102-134`
- **Today:** The consumer dials gateway port 10.0.2.2:20000+i, which maps to stream:<prov>:<port> and then DialInto(prov), reaching the provider's primary. The map is computed per consumer at spawn.
- **Change needed:** None for the primary. A parallel fabric would pick the provider deployment from the consumer's deployment at spawn.
- **Compat:** Unchanged.

### Net providers and lan-ingress (L3 netmux)

- **Paths:** `/home/magik6k/buxon/internal/runner/netmux.go:1-63`, `/home/magik6k/buxon/internal/runner/runner.go:493-589`, `/home/magik6k/buxon/internal/broker/netfn.go:170-251`, `/home/magik6k/buxon/internal/broker/ingressfn.go:358-442`, `/home/magik6k/buxon/internal/sandbox/relay/splice_linux.go:15-49`
- **Today:** A provider registers one TUN per bound client (closing old fds) and nudges every client to restart. Clients splice to the provider fd keyed by (provider, client[#slot]). Link addresses come from the sorted client index. netMux.clear has no caller.
- **Change needed:** Only primaries register rosters and splice. A non-primary copy of a provider gets no roster. Non-primary clients either get no provider leg or dedicated roster entries, which renumber links and restart the provider.
- **Compat:** Unchanged for existing tiles.

### HTTP interface bindings and grant calls

- **Paths:** `/home/magik6k/buxon/internal/broker/netfn.go:253-408`, `/home/magik6k/buxon/internal/broker/resources.go:99-123`, `/home/magik6k/buxon/internal/broker/broker.go:412-490`, `/home/magik6k/buxon/internal/server/static.go:447`, `/home/magik6k/buxon/sdk/xbin.go:189-249`
- **Today:** Consumer env and document meta point at /api/<prov>[instance path]. Calls are authorized by the binding role or grants and reach the provider's primary through the proxy.
- **Change needed:** None for the primary. A parallel fabric needs the caller's deployment in its principal and a target-deployment choice in the proxy.
- **Compat:** Env and meta shapes stay the same for primaries (docs/elements.md:105-112).

### Interface instances

- **Paths:** `/home/magik6k/buxon/internal/broker/netfn.go:1032-1131`, `/home/magik6k/buxon/internal/registry/registry.go:218-223`, `/home/magik6k/buxon/docs/protocol.md:1627-1639`
- **Today:** A provider replaces IfaceInstances[path] in the root xbin.json, and every PUT restarts all bound consumers.
- **Change needed:** Refuse or no-op non-primary PUTs, or keep separate per-deployment tables.
- **Compat:** No shipped tile uses it.

### Tile status (tile-report) and xbind build events

- **Paths:** `/home/magik6k/buxon/internal/obs/status.go:13-150`, `/home/magik6k/buxon/internal/obs/obs.go:18-30`, `/home/magik6k/buxon/sdk/xbin.go:386-418`, `/home/magik6k/buxon/internal/runner/runner.go:285-352`, `/home/magik6k/buxon/web/bx-frame.js:366-381`, `/home/magik6k/buxon/internal/boot/serve.go:178`
- **Today:** Status is held in memory by path and cleared by any build-start for that path. build-* and reload events carry only the component. bx-frame reloads or shows its overlay by component.
- **Change needed:** Either tag status and events per deployment (additive field) or suppress them for non-primary copies. obs clears status only on the primary's build-start. bx-frame ignores non-primary events.
- **Compat:** Old shells ignore unknown fields. Existing event types must not be reused with the bare path for non-primary builds.

### Push /notify (D94)

- **Paths:** `/home/magik6k/buxon/internal/push/api.go:287-400`, `/home/magik6k/buxon/internal/boot/push.go:155`, `/home/magik6k/buxon/sdk/notify.go:52-80`, `/home/magik6k/buxon/builtin-templates/agent/_backend/needs_push.go`
- **Today:** The notifying tile is taken from the principal, with a per-tile budget; the tap link is c/<tile>/.
- **Change needed:** Decide a policy for non-primary copies: drop, deliver to the developer only, or tag.
- **Compat:** Unchanged for primaries.

### Terminals and agent sessions (D73/D74)

- **Paths:** `/home/magik6k/buxon/internal/term/term.go:78-500`, `/home/magik6k/buxon/internal/term/agent.go:114-360`, `/home/magik6k/buxon/internal/term/sessions.go:85-100`, `/home/magik6k/buxon/internal/server/agentapi.go:20-66`, `/home/magik6k/buxon/internal/server/server.go:165-168,292-352`
- **Today:** Sessions belong to a tile (cwd). The terminal token is the tile's principal, and XBIN_COMPONENT is the tile. Only one live session may mount a tile's persistent layer.
- **Change needed:** Optional per-session target deployment (token claim or env) so curl and bx address the chosen deployment. The brief places the hot-reload toggle UI here.
- **Compat:** Default target is the primary, which keeps today's behaviour.

### bx CLI

- **Paths:** `/home/magik6k/buxon/cmd/bx/main.go:349-376,494-530,1122-1136`, `/home/magik6k/buxon/cmd/bx/agent.go:179,244`
- **Today:** status calls /tile-status (default $XBIN_COMPONENT). logs reads .xbin/log/<CompKey>.log directly. cron ls lists jobs. Agent sessions open on $XBIN_COMPONENT.
- **Change needed:** A deployment flag and default for status and logs. New deploy, promote and pause verbs.
- **Compat:** The CLI must stay a superset (compat.md rule 6).

### Runtime and admin APIs

- **Paths:** `/home/magik6k/buxon/internal/boot/api.go:18-118`, `/home/magik6k/buxon/internal/runner/inspect.go`, `/home/magik6k/buxon/internal/runner/stats.go`
- **Today:** /backends is a map by path. /runtime returns Backend rows with Path. /tile-status takes the first Inspect row whose Path matches.
- **Change needed:** Add a deployment field or nested per-deployment map, and a ?deployment= parameter on /tile-status.
- **Compat:** Rule 2: additive only; keys stay paths.

### Static and frontend plane

- **Paths:** `/home/magik6k/buxon/internal/server/static.go:65-180,193-219,427-470`, `/home/magik6k/buxon/internal/server/tileorigin.go:17-170`, `/home/magik6k/buxon/internal/server/native.go:50-130`, `/home/magik6k/buxon/internal/auth/assettoken.go:178-188`
- **Today:** Tile documents and files are served live from the working tree with no-store. The frame token, component meta and tile origin are all keyed by path.
- **Change needed:** Serve the primary from its pinned code when hot reload is paused or attached to another deployment. A dev frontend's address and credential are opt-in.
- **Compat:** Only one HTML transform is sanctioned (CLAUDE.md), so routing must happen at the URL or credential level, never by rewriting.

### Lifecycle gates

- **Paths:** `/home/magik6k/buxon/internal/proxy/proxy.go:145-158`, `/home/magik6k/buxon/internal/proxy/ingress.go:37-40`, `/home/magik6k/buxon/internal/runner/runner.go:191-196,272-275,734-748`, `/home/magik6k/buxon/internal/boot/boot.go:504-508`, `/home/magik6k/buxon/internal/runner/alwayson.go:50-52,126`, `/home/magik6k/buxon/internal/broker/cron.go:166-173`, `/home/magik6k/buxon/internal/broker/bussubs.go:342-344`, `/home/magik6k/buxon/internal/broker/ingressfn.go:93-96`, `/home/magik6k/buxon/internal/broker/lifecycle.go:22-117`, `/home/magik6k/buxon/internal/broker/resenc_wire.go:102-162`, `/home/magik6k/buxon/internal/registry/registry.go:347-370`
- **Today:** A per-path state in the root xbin.json gates every start path, and Stop(path) stops the one instance.
- **Change needed:** Gates apply to all deployments of a tile. Stop, Seal and Offload stop all of them. A per-deployment stop would be an optional state outside ws.Lifecycle.
- **Compat:** ws.Lifecycle semantics stay unchanged.

### Backend logs

- **Paths:** `/home/magik6k/buxon/internal/runner/runner.go:464-471`, `/home/magik6k/buxon/internal/runner/env.go:89`, `/home/magik6k/buxon/internal/obs/logs.go:38-80`, `/home/magik6k/buxon/cmd/bx/main.go:494-530`
- **Today:** One append-only log per path, shared by all generations and by the env-layer build.
- **Change needed:** A per-deployment log key, with the primary keeping the legacy file.
- **Compat:** docs/protocol.md:2320 and docs/elements.md:524 name .xbin/log/<compkey>.log, so the primary keeps that path.

## Hazards

- If a second deployment of a tile shares RunDir/CompKey(path) with its own generation counter, os.Remove(sock) before start and on stop deletes the other deployment's live g<N>.sock, and sweepTransports then drops its pooled transport (runner.go:408-413,728; proxy.go:117-126).
- A non-primary copy has the same instance principal as the primary, so its self-calls to /api/<Self()> reach the primary as admin under the self-admin rule — dev code reads and writes prod data (auth.go:589-591; broker.go:482-484; sdk/xbin.go:81-82).
- The agent template's keep-alive request targets /api/<Self()>/engine/hold, so a dev copy's hold keeps the primary alive while the dev copy is reaped mid-work after 30 idle minutes (owner.go:154-184; runner.go:798-820).
- With per-deployment databases the agent's schedule and trigger ids collide across copies, so re-registration at start rewrites the primary's sched-<id> cron jobs and trig-<id> bus subscriptions with dev values, or deletes them (agent main.go:67-68; schedule.go:166-216; triggers.go:441-470; cron.go:108; bussubs.go:158).
- The agent's takeover deletes the shared 'resume' and 'heartbeat' jobs and its shutdown registers 'resume' to fire /tick, so a dev copy removes or triggers the primary's recovery job (owner.go:220-238).
- Cron and bus-subscription registrations from any copy land on the key (path,name) and replace the previous row, and DELETE acts on the caller's own component, so a dev copy silently rewrites or removes production schedules and subscriptions (cron.go:108-144,234-236,257-270; bussubs.go:158,186-216,399-401).
- PUT /iface-instances replaces the provider's whole instance map in the root xbin.json and restarts every bound consumer on each call, so two copies re-registering at start overwrite each other and restart consumers every time (netfn.go:1102-1129).
- PUT /ingress-hosts replaces the tile's registered hostnames in the root xbin.json and reconciles listeners, so a dev copy with different data unpublishes production hosts (ingressfn.go:564-581).
- Tile status is one in-memory record per path, cleared by any build-start for that path, so a dev build clears the primary's status and dev reports overwrite it (obs/status.go:61-116,129-148; obs/obs.go:25).
- build-start, build-error, build-ok and reload events carry only the component path, so a dev build error or reload shows on every user's primary frame and in the native app (runner.go:285-352; serve.go:178; web/bx-frame.js:366-381).
- A second net-provider copy registering its roster closes the primary's client-link fds and restarts every client; two client copies splice to one provider fd, splitting packets between them while sharing one link address (netmux.go:22-32; runner.go:545-588; splice_linux.go:15-39; netfn.go:213-216,239-251).
- Two copies of an alwaysOn chat bridge open two platform connections with the same vault token, and because hello and outbox rows are keyed by X-XBin-From both copies post the same agent replies (bridge xbin.json; platform.go:29-60; outbox.go:248-305; docs/agent-inbox.md).
- A non-primary webhooks copy pushes /adapter/event with the same From as production, so it fires production agent triggers matched by source_ref (webhooks main.go:176-200; triggers.go:396).
- A second traefik copy polls the same scoped routes and renders ACME resolvers for production hostnames whose challenges reach only the primary, risking failed validations and rate limits (traefik main.go:35,87,129-139,271; ingressfn.go:624-629) — inference.
- If the vault stays per tile, every deployment gets production secrets (API keys, platform tokens), because vault access is authorized by Component == path only (broker/vault.go:151-176,188).
- A bus publish from a non-primary copy reaches every production consumer — other tiles' push subscriptions and /ws/events frontends — unless bus topics are namespaced per deployment (resources.go:410-458; bussubs.go:271-299).
- A non-primary copy calling POST /notify sends real pushes to people and spends the tile's shared budget (push/api.go:287-395).
- Two deployments building into the one .xbin/build/<CompKey>/bin overwrite each other's artifact, which under isolation is bind-mounted into the running sandbox (runner.go:366-377,642-644).
- All generations and deployments append to one .xbin/log/<CompKey>.log, which bx logs and GET /logs read unfiltered (runner.go:464-471; obs/logs.go:69; cmd/bx/main.go:511).
- Deployments would share one cgroup leaf comp-<CompKey> (one memory/pids cap, 2 GiB default), and the VM leaf is sized for exactly two guests (runner.go:480-484,602-604; vm.go:99-107; boot.go:556-563).
- Every restart path — reaper, crash watch, grant/binding change, provider nudge, alwaysOn backoff — calls Ensure, which rebuilds from the working tree; a paused deployment picks up unsaved-to-deploy code on its next restart (runner.go:199-236,347,583-585,808-812; boot.go:497-501; alwayson.go:121-136).
- node, python and cgi backends execute scripts straight from the tile directory, so even a running paused backend can load new code lazily and cgi always runs the working tree (runner.go:388-401,630; proxy.go:239-261).
- Tile documents and assets are served from the working tree with Cache-Control no-store, so pausing the reload event does not pause the frontend: the next navigation serves the new files (static.go:69,153,193-219).
- The registry holds one manifest per path from the working tree, so runtime, entry, uses, interfaces, exposes, alwaysOn, vm and setup for any deployment come from whatever xbin.json is on disk (registry.go:456-474) — inference, overlaps the git brief.
- Grant, binding and instance changes restart only the path's single state, so a dev deployment would keep stale spawn-time env such as resources, GPU, caps and interface URLs (boot.go:497-501; broker.go:648-666; netfn.go:824-846,1115-1129).
- Lifecycle disable, offload, restore and vault seal stop only runner.Stop(path), so without fan-out a dev deployment keeps running after the tile is disabled (runner.go:734-748; lifecycle.go:101-105; backup.go:439-455; resenc_wire.go:151-161).
- Callees cannot tell a non-primary caller from production because X-XBin-From, the role and the grants are identical (auth.go:196-207; proxy.go:275-305; docs/auth.md:16-26).
- A path-embedded selector would collide with legal component directory names and with the allowance grammar's @ separator (util.go:97-115; users/orgs.go:597-641).
- The instance token is revoked at process exit rather than at swap, contrary to D8's wording, so an old generation can keep registering cron, bus or status for up to the 30 s drain (runner.go:44,591-610; plans/DECISIONS.md D8).

## Open questions

- Does a non-primary instance carry its deployment as a new auth.Principal field, keeping Component = path so grants, bindings and ceilings keyed by path keep working, or does it get a distinct Component string that every grant lookup would have to map back (broker.go:418-452)?
- Should /api/<self> from a non-primary copy route to that copy? The agent's engine hold needs it. And should the self-admin rule (broker.go:482-484) cross deployments at all?
- May a non-primary copy register cron jobs, bus subscriptions, iface instances, ingress hosts and status? If so, are they keyed by (path, deployment) and dispatched there; if not, is the call an accepted no-op (keeps SDK rule 8) or an error?
- Is the vault per tile or per deployment? A per-tile vault hands dev the platform tokens that make alwaysOn bridges double-connect.
- Parallel fabric: when a dev consumer calls a provider through a binding or grant, does it reach the provider's same-named deployment (fallback primary?), always the primary, or nothing? Does the answer differ for http, stream, lan-ingress and net?
- Are bus resources namespaced per deployment? If so, how do cross-tile subscribers and frontends choose which namespace they see?
- Is alwaysOn ever honoured for non-primary deployments, and is 'keep dev running' a per-deployment switch?
- Do non-primary deployments of net-provider or lan-ingress tiles get any L3 plumbing? Adding roster entries renumbers links and restarts the provider (netfn.go:182-233).
- When hot reload is paused or attached to a non-primary deployment, what does the static plane serve for the primary (a snapshot or worktree of its code)? And what address and credential does a dev frontend get (origin label, frame-token claim)?
- If primary becomes reassignable, inbound traffic switches to the other deployment's data (resources are per deployment). Do ingress listeners, forward doors, cron, bus subscriptions, status and backups follow the primary pointer?
- Do backups include non-primary data, and under which archive key (backupKey = CompKey(path), backup.go:45)?
- Should a terminal or agent session carry a default target deployment, so curl $XBIN_URL/api/<self> and bx status/logs address dev?
- Naming: the owner calls deployments 'identities', which collides with the auth model's 'identities' — docs/auth.md is titled 'identities, roles, grants, vault'.
- Which xbin.json governs a deployment pinned to other code (branch variant)? The registry has one Component per path (registry.go:372-385,456-474).
- Resources are per scope, not per tile (util.ScopeKey; broker/resources.go:36-66). What does per-deployment data mean for a scope shared by several tiles when only one of them has a dev deployment?
- Is there a per-deployment stop state, separate from the tile-level ws.Lifecycle states in the root xbin.json?
- How do cgi tiles, which run per request from comp.Dir (proxy.go:239-261), get deployments at all — a code directory per deployment?
