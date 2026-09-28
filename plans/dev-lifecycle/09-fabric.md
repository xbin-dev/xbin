# 09 — The fabric: inbound edges, outbound edge policy, self-calls

> Status: live — how every call into, out of and within a tile with deployments is routed and authorized: inbound edges, the deployment URL, principals, self-calls, the outbound edge policy, self-scoped APIs, deliveries, primary reassignment and the later parallel fabric (part of [plans/dev-lifecycle](README.md))

Terms are [01-glossary.md](01-glossary.md)'s, used verbatim. This document
elaborates [05-model.md](05-model.md) §7 (routing), §8 (events) and §10
(authority), and the invariants P3, P7, P11, P12, P13, P17, P19–P21 and
P23–P27. The glossary and P23 fix the edge-policy values; this document
assigns them to every edge kind (§5.1) and specifies how each is enforced.

Evidence: [research/serving-fabric.md](research/serving-fabric.md) (primary),
[research/inbound-edges.md](research/inbound-edges.md),
[research/identity-resources.md](research/identity-resources.md),
[research/builder-contract.md](research/builder-contract.md),
[research/data-plane.md](research/data-plane.md),
[research/prior-art.md](research/prior-art.md) (§6 bindings, §7 inbound) and
[research/sandbox-visibility.md](research/sandbox-visibility.md). The
baseline is master: D112 is landed, and D113 is designed
([../tile-sandboxes.md](../tile-sandboxes.md)). Every `file:line` below was
re-verified against this worktree.

Owned elsewhere, and only referenced here:
- data namespaces and the physical key function: [08-data.md](08-data.md);
- runner state keys, restart fan-out, serving roots and confine binds: [07-runtime.md](07-runtime.md);
- exact wire names (parameters, fields, headers, event types): [11-contract.md](11-contract.md);
- the threat model: [06-security.md](06-security.md);
- UI copy and panels: [10-ux.md](10-ux.md);
- the full test matrix: [15-test-plan.md](15-test-plan.md).

## 0. Rules and notation

**Notation.**
- `T` is a tile. `primary(T)` is the deployment holding T's primary role:
  from T's deployment record, or `main` when T has none.
- `dep(p)` is the deployment named by principal `p`'s credential. `""` means
  `main`.
- A **tile principal** is an element principal built from an instance token,
  a frame token (or tile-origin cookie) or a terminal token: `p.Component`
  set, `Via` one of `instance`, `frame`, `terminal`. Cron and bus deliveries
  are **synthetic principals**, not tile principals.
- A **non-primary principal** is a tile principal with
  `dep(p) ≠ primary(p's tile)`.

**Rules.** Every section applies them.

| # | Rule |
|---|---|
| F1 | **The zero state takes today's code path.** For a tile with no deployment record, `dep(p)` is always `""` and `primary(T)` is `main`. Every function in this document then returns today's result, byte for byte (P5). The first check in each new function is that lookup. |
| F2 | **Credentials carry a deployment name; roles are looked up per request.** A token binds a name, fixed at mint. Whether that name is primary is read from the tile's record on every request. A role change therefore takes effect at once for every running generation and token (§3.6). |
| F3 | **Inbound edges resolve the primary at their funnel.** No edge is taught about deployments one by one. The funnels are `Runner.Ensure`, `Runner.DialInto` and the netmux. Their names are kept and keep meaning the primary; deployment-aware names sit beside them (`EnsureDeployment`, `TrackDeployment`; [14-implementation.md](14-implementation.md) NP-14-8), so every inbound caller reaches `primary(T)` whether or not it has moved yet (§1). `dialCurrent`'s read of the current generation (`internal/runner/ingress.go:57-69`) keys per deployment. The runner's other path-keyed entry points (`Stop`, `Status`, `Inspect`, the reaper, the proxy's transport pool) are [07-runtime.md](07-runtime.md)'s. |
| F4 | **Tile principals address only their own deployment.** A self-call lands on `dep(p)`. Naming another deployment of the same tile is refused, on every route (P12, §4). |
| F5 | **One function decides a non-primary deployment's outbound calls.** It is `resolveTarget` (§5.3). No other code reads the edge policy. |
| F6 | **Fail closed.** Each of these is refused with an error that names the rule or the edge: an unknown deployment, an unknown edge-policy value, an unclassified self-scoped API, a role the read clamp can't narrow. |
| F7 | **Registrations are stored, not refused.** Registering from a non-primary deployment succeeds and is stored under it. Its cron jobs and bus push subscriptions fire for it unless a tile manager switched its deliveries off; its interface instances and ingress hosts have no effect unless it is primary (P13, revised 2026-09-28; §6, §7). |
| F8 | **The inbound surface follows the primary's code.** `template`, `exposes`, `expose.roles` (with `implies`), `provides` and `chrome` are read from `primary(T)`'s code: the work tree while the primary is the live reload target, its checkpoint while pinned ([05-model.md](05-model.md) §6). Every inbound decision below (a binding's role, the read clamp's `implies`, what is exposed, whether a document is chrome) reads that view. A non-primary deployment's documents are never chrome. Tile-level fields (`uses`, `interfaces`, `deps`) stay the work tree's. |

## 1. Inbound edges: every one reaches the primary

Each row names:
- the edge and its entry point, with where the target is resolved today;
- the change;
- the unit test that pins the change. Names follow
  [15-test-plan.md](15-test-plan.md) §5.5 where it has one.

**The integration fixture.** Every row is also a step of
`TestInboundEdgesReachOnlyPrimary`. Tile `T` has two deployments:
- `main` is primary and pinned to checkpoint A, whose backend and files
  answer `A`;
- `dev` is the live reload target, and its work tree answers `B`.

Each step fires its edge and asserts that `A` answered and `B` saw nothing.
Rows marked (iso) need `--isolate` and run under `make integration`.

| Edge | Entry and today's resolution | Change | Test |
|---|---|---|---|
| `/api/<tile>/…` on the console, the gateway socket and a tile origin | `Proxy.ServeHTTP` `internal/proxy/proxy.go:128-235`: `Resolve` :130, `Policy` :160-171, `Runner.Ensure(ctx, comp)` :182, `Track(comp.Path)` :196. Front doors: `internal/server/server.go:163`, `internal/boot/serve.go:34-54`, `internal/server/tileorigin.go:173`. | `Policy` becomes `Route` (§4.1): the primary for every caller that is not the tile's own principal, and not a synthetic delivery. The proxy calls `EnsureDeployment(ctx, comp, dep)` and `TrackDeployment(path, dep)` with `Route`'s answer ([07-runtime.md](07-runtime.md)). | `TestQualifiedURLRouting`, `TestEdgePolicyNeverTouchesPrimary`: calls from another tile, an admin and the native app answer `A`. |
| `/c/<tile>/…` documents and files, all three asset planes | `handleComponentStatic` `internal/server/static.go:70-180`: owner `owningComponent` :105 (`:330-338`), RBAC :106-115, opener `openLegacy` :193. The D4 token is minted in `headInjection` :431-441. | The bare URL serves the primary's code root, per [07-runtime.md](07-runtime.md), framed by the primary's `chrome` (F8). The D4 injection mints a token with the primary's claim, absent for `main`, and only for principals §3.3 allows. | `TestQualifiedURLRouting`, `TestFrameTokenClaimInjected` |
| Native runtime document `/c/<tile>/?native=1`, and the native app | `static.go:119-121` → `serveNativeRoute` `internal/server/native.go:54`. The app maps `/c/…` to a tile by longest prefix and forwards its frame token: `native/ios/Packages/XbinCore/Sources/XbinCore/Client/TileLoading.swift:74-93`. | Server side only: served from the primary's code. The app is unchanged. It never builds a deployment URL, and it never sees non-primary activity, which rides only the `deployments` event ([05-model.md](05-model.md) §8). | `TestNativeDocumentFollowsPrimary` |
| Consumer interface bindings: backend env and frontend meta | URLs from `HTTPSlots` `internal/broker/netfn.go:341-381`; instance prefixes from `ws.IfaceInstances[prov]` :366. Env: `EnvFor` `internal/broker/resources.go:103-123`. Meta: `static.go:444-448`. All of it becomes the `/api` edge above. | URLs are unchanged. The role a binding grants comes from the provider primary's `provides` and `expose` (F8). `prov#inst` resolves against the **provider primary's active** interface instance table (§8), which is today's root map while `main` is primary. | `TestEnvForPerDeployment`; new `TestIfaceInstanceFollowsPrimary` for `prov#inst` |
| Grant-based calls: `uses` rows and same-scope auto-grants | `grantedRole` `internal/broker/broker.go:418-452`, then the `/api` edge. | None: the primary by default. | `TestEdgePolicyNeverTouchesPrimary` |
| Cron ticks (D85) | `fire` `internal/broker/cron.go:166-186` builds `xbin/cron` at :180. `DispatchViaProxy` :200-208 posts to `/api/<comp><path>`. | A job carries its owning deployment (`main`'s jobs stay in today's store). It fires unless its owner is a non-primary deployment with deliveries off, and it dispatches to its owner, never the primary (§7). A deployment's own tick is not an inbound edge from others. | `TestDormantRegistrations`, `TestDeliveriesSwitch`: `main`'s job reaches `A`; `dev`'s reaches `B`, and stays dormant while its deliveries are off. |
| Bus push deliveries (D85) | `publish` `internal/broker/bussubs.go:271-299` → `deliver` :337-362 (`xbin/bus` built at :354) → `DispatchBodyViaProxy` :127-135. | As cron. In addition, a delivery queues only when the event's data namespace equals the subscription's resolution (§5.10). | `TestDormantRegistrations`, `TestDeliveriesSwitch`; new `TestBusNamespaceMatch` |
| Event triggers (D87) | Bus triggers are `trig-<id>` subscriptions (`builtin-templates/agent/_backend/triggers.go:441-470`), i.e. the bus-push row. Push triggers are a bound tile's `/adapter/event` call, i.e. the binding row. | None beyond those two rows. | Covered by those rows. |
| Webhooks | Public `/hook/*` arrives through ingress (next row) at the webhooks tile's primary. Its pushes leave through the multi `agents` slot, `builtin-tiles/webhooks/xbin.json:17`. | Inbound: none. Outbound from a non-primary webhooks deployment: blocked, with no override, because role `channel` can't be read-clamped (P23, §5.5). | `TestUnclampableEdgeDefaults` |
| Ingress HTTP, runtime listener (ING-5) | `internal/boot/serve.go:66-72` → `IngressLookup` `internal/broker/ingressfn.go:104-146` (registered hosts :133-139) → `ForwardIngress` `internal/proxy/ingress.go:31-95` (`Ensure` :58, `Track` :65). | `ForwardIngress` targets `primary(T)`. `IngressLookup` reads the **primary's active** host set (§8). | `TestIngressForwardPrimaryOnly` (iso step) |
| Ingress through terminator forward doors | Doors: `internal/boot/boot.go:532-542`. Terminators: `IngressSources` `ingressfn.go:264-273`. The terminator's own route to its door: `IngressFwdFor` `ingressfn.go:315-317`. | As the runtime row. In addition, only the terminator's primary gets the door in its relay, and a non-primary terminator reads an empty `/ingress-routes` (§6). | `TestIngressForwardPrimaryOnly`; new `TestTerminatorDoorPrimaryOnly` |
| L4 streams and hairpin (ING-6) | `IngressStreamSpecs` `ingressfn.go:242-259`; `Streams{Dial: run.DialInto}` `boot.go:531`; `HairpinDial` `ingressfn.go:458-470` through `DialStream = run.DialInto` `boot.go:544`; `DialInto` `internal/runner/ingress.go:32-47`, whose `dialCurrent` reads the state at :57-69. | `DialInto` keeps its signature and resolves `primary(T)`; `dialCurrent` reads that deployment's state ([07-runtime.md](07-runtime.md) §1.3). | new `TestDialIntoPrimary` (iso) |
| Stream interfaces, tile to tile | Forward `stream:<prov>:<port>` from `IngressFwdFor` `ingressfn.go:318-322` → `hostDial` `internal/runner/ingress.go:115-127` → `DialInto(prov)`. | Target: the provider's primary. The dialing deployment's edge is checked at each dial (§5.7). | new `TestEdgeStreamDial` (iso) |
| lan-ingress | Provider roster `lanIngressRoster` `ingressfn.go:396-406`, registered at provider start `internal/runner/runner.go:555-561`. Client legs `NetLinksFor` `ingressfn.go:420-442`, spliced at client start `runner.go:564-580`. | Only a provider's primary registers a roster. Only a client's primary gets a leg (P23). | `TestUnclampableEdgeDefaults` (iso step) |
| Net-provider splices | `NetProviderRoster` `netfn.go:223-233`; `NetClientTarget` `netfn.go:239-251`; splice `runner.go:517-524`; `netMux.register`/`get` `internal/runner/netmux.go:22-41`; client nudge `runner.go:581-597`. | Only primaries register and splice. A non-primary deployment of a provider has no roster; a non-primary client has no splice (§5.8). | `TestNetEdgeDefault` (iso step) |
| alwaysOn and its wake points (D84) | `WakeAlwaysOn` `internal/runner/alwayson.go:45-61`; backoff `afterExit` :100-137; reaper exemption `runner.go:819`. Wakes: `boot.go:720`, `serve.go:183`, `internal/broker/broker.go:233-234` (unseal), `internal/broker/lifecycle.go:104` (enable). | Primary only. A non-primary deployment is woken only by its own alwaysOn switch (§7). The reaper exemption and backoff key per deployment. | new `TestNonPrimaryAlwaysOn` |
| Archiver calls (LC-3) | `archiveDo` `internal/broker/backup.go:379-388` calls as Owner through the proxy; the archiver comes from `archiveProvider` :35-41. | None: an Owner call without a qualifier reaches the archiver's primary. | `TestBackupCoversDeployments`, with an archiver that has a `dev` deployment |
| Notify links (D94) | The link is set to `c/<tile>/<link>` at `internal/push/api.go:368`. | None. Non-primary deployments never push (§6), so no link names a deployment URL. | `TestNotifyNotPushed` |
| Restart paths (not traffic): grant/binding changes, provider nudges | `OnGrantChange` `boot.go:508-512`; nudge `runner.go:581-597`. | Fan-out to each deployment with its own wiring ([07-runtime.md](07-runtime.md)). | In [07-runtime.md](07-runtime.md). |

The `/ws/events` feed is not an inbound edge into a backend. Its bus filter
is deployment-aware (§5.10), and its filter for non-primary facts is §6.1's.

**`/components` describes the primary** (`internal/server/api.go:130-160`).
Its deployment-level fields (`runtime`, `hasIndex`, `native`) and
`/c/<tile>/?native=1` describe the primary's code, its checkpoint when
pinned. `chrome` follows the inbound surface (F8). `manifestError`, `roles`,
`uses` and `deps` keep describing the work tree. The listing adds only the
primary summary, never a non-primary name ([05-model.md](05-model.md) §6,
§8; [11-contract.md](11-contract.md) owns the fields). The roles that
inbound calls are checked against are always the primary's (F8), whatever
the listing's work-tree `roles` say.

## 2. Reaching non-primary deployments

Exactly three paths reach a non-primary deployment ([05-model.md](05-model.md)
§7):
- its deployment URL, by humans with at least `write`;
- the tile's terminal and agent sessions that target it;
- its own self-calls.

Its own cron jobs and bus subscriptions deliver to it unless a tile manager
switched its deliveries off; a tile manager can turn on its alwaysOn, and a
terminal-level user can run one of its jobs now (§7). Nothing else reaches
it.

### 2.1 The deployment URL

`/c/<tile>+<name>/…` and `/api/<tile>+<name>/…`. The qualifier grammar is
the glossary's. Resolution follows [12-compat.md](12-compat.md)'s NP-12-1 and
[11-contract.md](11-contract.md) §2.2:
1. **Today's resolution comes first.** A component, or a path on disk, at
   the full candidate `<tile>+<name>` is served exactly as today
   (`Reg.Resolve`, `internal/registry/registry.go:519-536`). Two narrow
   refusals, for every creator (admins and `xbin:writer` elements included),
   keep a qualified URL unambiguous (glossary, Deployment URL;
   [05-model.md](05-model.md) §7):
   - no tile is created at `<P>+<N>` while `P` has deployment `N`;
   - no deployment `N` is added to `P` while a component exists at
     `<P>+<N>`.

   No other new tile name may contain `+` either: refused for every creator
   (P17, decided 2026-09-28; 11-contract §2.1).
2. **The split is tried only when that fails, and only for a tile with a
   deployment record.** For a zero-state tile, every `+<name>` URL behaves
   exactly as today, `+main` included (P5).
3. **For a tile with a record**, the name must be one of its deployments,
   else 404 `no such deployment`. `<tile>+<primary name>` is the primary, and
   every bare-URL rule applies.
4. **The qualifier is consumed.** The backend sees `/<endpoint>` exactly as
   for the bare URL (`proxy.go:209`). A qualifier is never forwarded, just as
   `?frame=` isn't (`proxy.go:199-203`).

### 2.2 The gate, per principal

| Principal | Bare URL, or `+<primary name>` | `+<name>`, where the name is non-primary |
|---|---|---|
| Admin: root token, admin user, `--no-auth` | as today | allowed at `admin` |
| Human session with ≥ `write` on the tile, including `noTerminal` accounts, which are capped at `write` (D88) | `/c/`: as today. `/api/`: as today (non-admin humans hold no role there, `broker.go:478-490`; they reach APIs through the tile's frames). | `/c/` documents and frame-token mints: allowed. `/api/`: as today (no direct role). |
| Human session with `read` only | as today | 403 `non-primary deployments need write on <tile>`. Not the D36 request-access page, which asks for read. |
| The tile's own tile principal | `/api/` bare URL: a **self-call to `dep(p)`**, not to the primary (§2.4). `/c/` bare URL: the primary's document, with a frame token only when `dep(p)` is the primary (§3.3). `+<primary name>`: only when `dep(p)` is the primary, otherwise 403 (P12). | allowed only when the name is `dep(p)`; otherwise 403 (P12, §4) |
| Another tile's principal | bare URL: the primary, with the caller's own edge policy if it is non-primary itself (§5). `+<primary name>`: 403, because a hard-coded name would break at the next reassignment (NP-11-14). | 403 `non-primary deployments receive no inbound edges` |
| Synthetic `xbin/cron`, `xbin/bus` | routed to the registration's owning deployment, never by URL (§7) | — |
| Ingress | primary only | never: no route names a deployment |

**Subresources in the legacy plane.** Credential-less subresource loads
(`tileSubresourceAuthed`, `internal/server/static.go:286`) apply to
deployment URLs under the same rule as bare URLs. Sandboxed frames send no
credential on tag loads, and the bytes are tile code, which the legacy plane
already serves to readers.

Documents, frame-token mints and API calls are always gated by the table.
The strict planes carry the deployment inside their asset tokens and
tile-origin grants (§3.4), so their per-request access checks cover
subresources too.

### 2.3 Terminal and agent targets (P24)

- **The target is server-side state of the session's token** (§3.5), never
  an env var or a header. It is either a deployment name or "follows the
  primary", which resolves to the current primary on each request
  ([11-contract.md](11-contract.md) §7.4).
- **It is chosen in the API dropdown.** The terminal window's existing
  tile-API select (`web/frame-titlebar.js:162-167`, today `🔌 tile API` /
  `⛔ no API`) gains one entry per deployment the user may reach. There is no
  separate target picker; [10-ux.md](10-ux.md) owns the entries' copy.
  "API off" is today's code-only session: no terminal token is minted
  (`internal/term/term.go:336-338`), so the session has no target, no
  self-calls and no tile API.
- **The default target**, chosen by the server at session start:
  1. the primary ("follows the primary"), unless it is protected;
  2. otherwise the live reload target, as a named target;
  3. otherwise API off (a protected primary with live reload paused).
- **Requesting a target.** A session asks for one with the session parameter
  [11-contract.md](11-contract.md) §7.4 names. Asking for the current
  primary's name stores "follows the primary". Asking for a protected
  primary, by name or by following it, is refused with 403 `the primary of
  <tile> is protected: terminal and agent sessions can't target it` (P21,
  P24; [11-contract.md](11-contract.md) §1.14). Opening already
  requires terminal level on the tile: `internal/term/term.go:229`,
  `internal/term/agent.go:135`.
- **The target is fixed for the session's life.** Changing it is a
  terminal-level act ([05-model.md](05-model.md) §10) that restarts the
  session, as today's API switch does.
- **Protecting the primary** restarts every session whose target is the
  primary, following or named, onto the default above, or ends it when the
  default is API off. The protection is read at decision time (F2), so from
  the moment it is committed such a token's self-calls, self-scoped API calls
  and frame-token mints answer 403 naming the protection, even before the
  restart lands. A reassignment while the primary is protected does the same
  for sessions whose named target becomes the primary (§8).
- **What the target governs.** `/api/<self>/…`, every self-scoped API (§6),
  `bx status`/`bx logs`, and the edge policy on its outbound calls. A session
  bound to the primary uses edges exactly as today.
- **`XBIN_DEPLOYMENT`** is set only when the target is not the primary at
  session start (glossary; 11-contract §5). It is informational: routing
  never reads env.
- **A removed target** makes the session's self-calls answer 404
  (11-contract §7.4). There is never a silent fallback to the primary: an
  agent must not start writing to the primary's data because a deployment
  disappeared.

### 2.4 Self-calls

A tile principal's call to `/api/<self>/…` routes to `dep(p)` at role
`admin`, whichever deployment is primary:
- a backend's call routes by its instance token (§3.2);
- a frontend's by its frame-token claim (§3.3);
- a session's by its target (§3.5).

`xbin.self`, `XBIN_COMPONENT` and `Self()` stay the tile path
(`web/xbin-client.js:50`, `sdk/xbin.go:82`). Tile code keeps building
`/api/${xbin.self}/…`, and xbind routes the call by credential.

This also resolves two research hazards
([research/inbound-edges.md](research/inbound-edges.md) §3 B.9, F.20):
- the agent template's keep-alive `GET /api/<Self()>/engine/hold`
  (`builtin-templates/agent/_backend/owner.go:155`) holds its own
  deployment;
- its `resume` cron job (`owner.go:225`) and its `sched-*` jobs
  (`builtin-templates/agent/_backend/schedule.go:166`), registered from a
  non-primary deployment, are that deployment's registrations. They never
  fire into the primary: they reach that deployment, unless a tile manager
  switched its deliveries off (§7).

**Sandbox-to-tile forwards.** D113's phase-3 forward from a tile-managed
sandbox to its tile's backend ([../tile-sandboxes.md](../tile-sandboxes.md) §5)
routes to the deployment that owns the sandbox. It is a self-call.

### 2.5 What a non-primary deployment never receives

A non-primary deployment never receives any of these:
- other tiles' calls (bindings, grants, stream interfaces);
- ingress: HTTP, streams, hairpin, forward doors, route reads;
- lan-ingress legs, or net-provider roster links and splices;
- archiver calls, notify-link taps, native app loads, bare-URL loads;
- another deployment's cron ticks or bus deliveries;
- alwaysOn wakes, unless its own switch is on.

Its own cron ticks and bus deliveries reach it (unless its deliveries are
off): they are its registrations, not inbound edges. Its interface instances
and ingress hosts never become active while it is non-primary, deliveries on
or not (§6, §7).

## 3. Principal changes

### 3.1 `auth.Principal.Deployment`

`internal/auth/auth.go:54-84` gains one field:

```go
// Deployment names the tile deployment this principal acts in; "" = main.
// Tile principals: set from the credential (never from a request header).
// Synthetic cron/bus principals: the deployment the registration belongs
// to, which is the delivery's target. Humans: always "" (a person names a
// deployment by URL, not by credential).
Deployment string
```

**`From()` stays the tile path** (`auth.go:196-207`). `X-XBin-From`,
grants, bindings, ceilings and ownership all keep keying on the path
(option (i) of [research/serving-fabric.md](research/serving-fabric.md) B.6).
The deployment travels beside the path, never inside it.

| Credential | Built at | `Deployment` comes from |
|---|---|---|
| Instance bearer | `auth.go:589-591`; no-auth mode `auth.go:571-572` | the instance map value (§3.2) |
| Terminal / agent bearer | `terminalPrincipal` `auth.go:498-507` | the session's named target, or the current primary's name when it follows the primary (§3.5; 11-contract §7.1). An API-off session has no bearer (§2.3). |
| Frame token (`X-XBin-Frame-Token` or `?frame=`) | `framePrincipal` `internal/auth/frametoken.go:304-320` | the token's claim; absent means `main` (§3.3) |
| Tile-origin cookie (D95) | `TilePrincipal` `internal/auth/assettoken.go:215-225` | the cookie's grant (§3.4) |
| `xbin/cron` | `cron.go:180` | the job's owning deployment |
| `xbin/bus` | `bussubs.go:354` | the subscription's owning deployment |
| Owner, session, device, app | `auth.go:584-648` | always `""` |

**Where the new code goes.** `auth.go` is at 707 of its 732-line budget
(`hack/size-budget.txt`). The instance map and the claim therefore live in a
new file of `internal/auth`.

**A removed deployment.** A credential naming a deployment that no longer
exists still verifies, but every decision point refuses it:
- routing answers 404 at resolution (11-contract §7.2);
- the self-scoped classification of §6 refuses it the same way.

Zero-state tiles have only `main` and never reach the check (F1).

### 3.2 Instance tokens: token → (path, deployment)

Today `Auth.instances` maps token → path (`auth.go:219`). It is filled by
`RegisterInstance` (`auth.go:445-449`) from `runner.go:488` and emptied at
process exit (`runner.go:619`).

The map value becomes `{component, deployment}`. A registration call that
takes the deployment sits beside today's call. `Runner.start` passes the
deployment it spawns, and `lookupInstance` (`auth.go:457-462`) returns both.

A token is minted per generation and per deployment
(`util.RandomToken(24)`, `runner.go:421`). Two deployments' generations
never share a token, a socket or a map entry.

### 3.3 The frame-token claim

The wire format today is `base64url(component)|base64url(user)|exp|gen|hmac`
(`frametoken.go:39-43`, minted at `frametoken.go:226-232`). `verifyFrame`
accepts 5 fields, or 4 for the legacy form (`frametoken.go:252-266`).

- **Non-`main` deployments get a sixth field** before the HMAC:
  `…|exp|gen|base64url(deployment)|hmac`, covered by the HMAC.
- **A 5-field or 4-field token means `main`.** Tokens for zero-state tiles,
  and for `main` in every case, keep today's exact bytes, and the pinned
  injection tests stay green.
- **An older xbind rejects 6-field tokens** in its `default` branch. That
  fails closed, and only non-`main` deployments ever mint them.
  [11-contract.md](11-contract.md) owns the exact encoding and updates the
  field-count text in `docs/protocol.md`.

**Who mints what.**
- **The D4 injection** (`static.go:431-441`) mints the claim of the
  deployment serving the document: `primary(T)`'s name at the bare URL, the
  URL's name at `+<name>`.
- **`mayMintFrameToken`** (`static.go:494-503`) gains the deployment rule,
  for **every** document, bare or qualified. It mints only for:
  - a human (`p.Component == ""`) who may open that deployment: `read` for
    the primary (today's rule), `write` for a non-primary deployment,
    re-checked on every mint and renewal; or
  - a frame or terminal/agent principal of the tile whose bound deployment
    `dep(p)` equals the document's deployment and, for a non-primary
    document, whose driving user can write the tile.

  Any other principal of the tile gets the document with an empty token
  (`content=""`). The rule covers `fetch()` as well as navigation: a `dev`
  frame, or a session targeting `dev`, that fetches `/c/<tile>/` receives the
  primary's document without the primary's token, and a `main` frame that
  fetches `/c/<tile>+dev/` receives no `dev` token. The primary's frame token
  is self-admin on the primary, so this is what keeps non-primary code off
  the primary's admin surface. No session ever targets a protected primary
  (§2.3), so no terminal or agent token can mint a protected primary's frame
  token. The same-tree exception (`static.go:501-502`) applies only between
  primaries: a non-primary principal never gets another component's token
  through it, and no principal gets a non-primary document's token through
  it. People switch deployments by URL, with their own session (P12).
- **Renewal**, `GET /api/xbin/frame-token` (`internal/server/api.go:241-252`,
  rule at :244):
  - a frame or terminal principal renews only for `dep(p)`, and the claim is
    copied as `Gen` is (`frametoken.go:218-224`);
  - a human may mint for a non-primary deployment only with `write`, through
    the deployment parameter [11-contract.md](11-contract.md) names. The
    chrome bootstrap (`web/frame-info.js:75-100`) passes it for deployment
    views.
- **Backend principals still never get a token** (`backendPrincipal`,
  `static.go:555-557`).

**A current `write` check on every request.** A request authenticated by a
user-attributed frame principal of a non-primary deployment re-checks the
user's current `write` on the tile. A token lives 15 minutes and renews itself
(`static.go:24`), so demoting a user must end their access to non-primary
deployments at once. Access is already resolved per request
(`frametoken.go:317`), so the check costs one lookup. Owner-driven frames
(`UserID == ""`) pass ([05-model.md](05-model.md) §7).

### 3.4 Tile-origin and asset credentials

- **Origins mode (D95).** The label, the ticket, the `__Host-xbin_tile`
  grant and `TilePrincipal` (`assettoken.go:215-225`) carry the deployment.
  A non-primary deployment needs an origin of its own, because a shared
  origin would share the primary's localStorage and IndexedDB
  ([research/serving-fabric.md](research/serving-fabric.md) A.8).
  `TileHostID(tile)` (`assettoken.go:182-188`) stays the same for the
  primary's label.
- **Tokens mode.** Asset tokens serve only non-document files. They carry
  the deployment so the opener picks the right root.

[07-runtime.md](07-runtime.md) and [11-contract.md](11-contract.md) own the
label scheme and the token fields. The fabric requires only one thing: every
credential that yields a frame principal sets `Principal.Deployment`.

### 3.5 Terminal targets

- **The terminal map value** `termID{component, userID}` (`auth.go:242-245`)
  gains `deployment`: the session's named target, or `""` for "follows the
  primary" (11-contract §7.4).
- **Minting.** The mint call at `term.go:338` (`MintTerminal`,
  `auth.go:471-477`) takes the target chosen by §2.3's rule. The target is
  fixed for the session's life, and changing it restarts the session (§2.3).
  API off mints nothing, as today.
- **`terminalPrincipal`** (`auth.go:498-507`) resolves "follows the
  primary" against the record on every request, so a session that follows
  the primary moves with a reassignment (§3.6).
- **Agent sessions** use the same token model (`agent.go:171-173`) and
  follow the same rules.
- **Where the code goes.** `internal/term/term.go` is at its 950-line
  budget, so the target plumbing goes in a new file.

### 3.6 The role is looked up per request

The record is held in memory as an immutable value, and every write replaces
it with one atomic pointer store. Every decision (routing, clamp, marker,
dormancy) reads the current value. Consequences:
- **Reassigning the primary** changes, for every active token at once:
  routing, `X-XBin-Deployment`, edge-policy application and registration
  activity. No generation keeps stale authority (§8).
- **A removed deployment's credentials are refused on every request**
  (§3.1): the backend is still draining, the frame is stale, or the session
  named it.
- **Returning to the zero state** leaves only `main`, whose credentials
  carry `""`. Nothing lingers (P5).

### 3.7 What the 30 s drain means with deployments

Today a replaced generation keeps serving, calling and holding its token for
up to `drainDeadline` = 30 s:
- the constant: `runner.go:45`;
- the old generation stopped in the background: `runner.go:328-330`;
- SIGTERM, then a kill at the deadline: `runner.go:728-740`;
- the token revoked only at process exit: `runner.go:619`.

D8's "instance credentials also die at swap" is therefore not what the code
does ([../DECISIONS.md](../DECISIONS.md) D8;
[research/inbound-edges.md](research/inbound-edges.md) §1).

With deployments:
1. **A draining generation of D still authenticates as D.** Its calls get D's
   authority and D's edge policy, and its self-calls land on D's current
   generation. It can never act as another deployment.
2. **A deploy, promote or roll back onto the primary** leaves the previous
   code acting with the primary's full authority for at most 30 s. That is
   what every save does today, and it is unchanged.
3. **Role changes have no drain window** (§3.6). A generation of a
   deployment that just stopped being primary is clamped on its next call.
4. **Registrations from a draining generation of D land in D's set.** Today
   a draining generation can re-register the tile's cron jobs, bus
   subscriptions and status for 30 s. That is now bounded to D.
5. **D81's flock assumption holds per deployment.** It assumes two engines
   overlap on one database for at most 30 s. Two generations of D still
   overlap for at most 30 s. Two deployments of one tile never share a
   database, because their namespaces differ ([08-data.md](08-data.md)). The
   indefinite concurrency of deployments therefore never reaches the lock.
6. **Terminal tokens have no drain.** They end with the session. Frame
   tokens expire in 15 minutes and are refused at once if their deployment
   is removed.

## 4. Self-call routing and the cross-deployment refusal

### 4.1 The proxy's decision

The broker installs a routing function into the proxy in place of
`Broker.Policy` (`broker.go:478-490`, wired at `boot.go:494`). It returns the
target deployment and the role, or a refusal. `DefaultPolicy`
(`proxy.go:52-60`) keeps its meaning for broker-less tests.

For `/api/<T>[+<N>]/…`, with the tile-level gates unchanged (template,
backend, lifecycle: `proxy.go:135-158`), the function checks these rules in
order:

1. **A synthetic delivery** (`xbin/cron`, `xbin/bus`).
   - Target: `p.Deployment`, the registration's owner.
   - Role: `p.Role`.
   - A qualifier is an error.
   - An owner that no longer exists drops the delivery.
2. **A tile principal of T** (`p.Component == T.Path`, as today's self rule).
   - A qualifier naming anything other than `dep(p)` is refused (P12).
   - Target: `dep(p)`.
   - Role: `admin`.
   - When `dep(p)` is non-primary and `p` is a user-attributed frame, the
     current `write` check of §3.3 applies.
   - A `dep(p)` that no longer exists answers 404 (§3.1).
3. **An admin** (`p.IsAdmin()`).
   - Target: the qualifier's deployment if there is one, else `primary(T)`.
   - Role: `admin`.
4. **Anyone else**, meaning another tile's principal or a non-admin human.
   - Any qualifier is refused: a non-primary name because non-primary
     deployments receive no inbound edges, and the primary's name because
     other tiles must use the bare URL (NP-11-14).
   - Target: `primary(T)`.
   - Role: `resolveTarget` (§5.3). It equals today's `grantedRole` unless the
     caller is a non-primary principal.

The proxy then:
- calls `identify` with the chosen role and the caller marker (§5.11);
- runs `EnsureDeployment(ctx, comp, target)` and
  `TrackDeployment(comp.Path, target)`;
- never lets a request reach a deployment the function didn't return.

The routing function lives in `internal/broker/edgepolicy.go` beside the
edge policy, and the proxy's side in `internal/proxy`
([14-implementation.md](14-implementation.md) WP-37, WP-47). `broker.go` is
at 743 of the 800-line unlisted cap.

### 4.2 Every self rule

Each place that grants something because `p.Component == <tile>` must also
compare deployments. The rule for all of them: **tile principals address
only `dep(p)`**, and a selector naming another deployment is refused. The
self-scoped API classes are in §6.

| Self rule | Code | With deployments |
|---|---|---|
| An element is `admin` of itself on its own API | `broker.go:482-484`; `proxy.go:56-58` | `admin` on `dep(p)` only (§4.1 rule 2) |
| Own-tile read/write pass (code visibility) | `tileLevel` `auth.go:133-135` | unchanged: code isn't deployment data; a non-primary frame may load the tile's other code |
| Vault: manage own, read values own backend only (D30) | `vaultAccess` `internal/broker/vault.go:171-174`; value gate :188 | `dep(p)`'s vault; the path stays `/vault/<tile>/…` (`sdk/xbin.go:264-317`) and xbind resolves the deployment from the credential |
| Frame-token renewal | `api.go:244` | own deployment only; the claim is copied (§3.3) |
| D4 mint for own frame or terminal | `static.go:494-503` | same deployment only (§3.3) |
| Cron and bus self-registration, list, delete | `cron.go:217-221`, `:234-236`, `:261-263`; `bussubs.go:376-380`, `:399-401`, `:440-442` | keyed `(tile, dep(p))` (§6) |
| Interface-instance self-registration | `netfn.go:1049-1054` | `dep(p)`'s table (§6) |
| Ingress-host self-registration; terminator route read | `ingressfn.go:503-508`; `:618-630` | `dep(p)`'s set; routes empty for non-primary principals (§6) |
| Status self-report | `internal/obs/status.go:73-87` | keyed `(tile, dep(p))` |
| Notify "from the tile" | `notifier` `push/api.go:287-300` | non-primary: recorded, never sent (§6) |
| Logs self | `canReadLogs` `internal/obs/logs.go:42-47` | `dep(p)`'s log |
| Tile status self | `internal/boot/api.go:90-100` | `dep(p)`'s backend row |
| Prefs self | `prefsKeys` `internal/obs/prefs.go:33-43` | keyed `(user, tile, dep(p))` |
| Code read self; PR decide self; PR author | `code.go:153`; `prs.go:109-111`; `prs.go:115-123` | tile-level, since the code plane has no deployment; PR decisions are primary-only for instance principals (P26; [05-model.md](05-model.md) §10) |
| whoami for an element | `internal/broker/whoami.go:62-67` | reports `dep(p)` (§6) |
| Tile-managed sandboxes (D113, designed) | [../tile-sandboxes.md](../tile-sandboxes.md) §3 | `dep(p)`'s sandbox set ([05-model.md](05-model.md) §12) |

### 4.3 The refusal

403, JSON through `jsonErr` (`proxy.go:307-319`) or `server.WriteError`:

```
{"error": "apps/crm's deployment \"dev\" can only call itself: this credential belongs to \"dev\", not \"main\". A tile's code never reaches another deployment of its own tile; a person switches deployments by URL, a terminal by changing its target.",
 "docs": "/docs/auth.md"}
```

[11-contract.md](11-contract.md) may add machine fields, such as the
refused deployment. `bx` reports it with exit code 3, refused
([11-contract.md](11-contract.md) §9.1). [15-test-plan.md](15-test-plan.md)'s `TestSelfCallRouting`
and `TestSelfCallsStayInDeployment` cover the proxy row. A new
`TestCrossDeploymentRefused` covers every other row of §4.2 in both
directions: a `dev` principal against `main`, and a `main` principal against
`dev`.

### 4.4 How strong the refusal is, per credential

- **Instance and frame tokens can't retarget.** For backend and frontend
  code the refusal is a real boundary. A non-primary deployment's code can't
  use the tile's credentials on the primary's admin surface. That is P12's
  rationale.
- **Terminal and agent sessions can retarget** between deployments that
  aren't a protected primary. Changing the target is a terminal-level act
  ([05-model.md](05-model.md) §10), done by restarting the session (§2.3).
  Between those deployments the refusal catches accidents, such as a stale
  URL, and is not a boundary. That is consistent with P20, since a
  non-primary deployment is an accident boundary.
- **A protected primary is a boundary for sessions too.** No session token
  ever targets it: the dropdown doesn't offer it, the server refuses the
  request, and protecting restarts the sessions that targeted it (§2.3, P24).
  A token bound to any other deployment is refused there by F4. So a
  prompt-injected agent session can't reach a protected primary's self-admin
  surface: its API, its kv, blob and vault, its cron, bus, interface-instance
  and ingress registrations, or its frame token (§3.3).

## 5. The outbound edge policy

### 5.1 Edges and their ids

An edge belongs to the tile. The primary uses every edge exactly as today
(P3). The policy applies to all of the tile's non-primary deployments
together.

| Edge kind | Edge id | Covers | Can it be read-clamped? | v1 values | Default |
|---|---|---|---|---|---|
| http interface slot, single or multi | `slot:<name>` | calls to each bound provider through `XBIN_IFACE_<SLOT>_URL`, the multi JSON list, or the `xbin-interfaces` meta | yes, per provider; a custom role only through the provider primary's `implies` (§5.5, F8) | `read`, `block`; only `block` for a provider whose role can't be clamped (P23) | `read`; blocked, with no override, where the role can't be clamped |
| stream interface slot | `slot:<name>` | dials through the slot's gateway forward | no | `block` only (P23) | blocked |
| lan-ingress interface slot | `slot:<name>` | the slot's L3 leg | no | `block` only (P23) | blocked |
| net interface slot | `slot:<name>` of the tile's `kind: net` slot (11-contract §1 "the net slot included") | egress through the tile's relay policy, in the deployment's own network namespace; never host networking or a provider splice (§5.8) | no role | `inherit`, `block`; `block` only when the tile's net resolves to host share or a provider (P23) | `inherit`; blocked under host share or a provider |
| component grant: an explicit row, or a same-scope `uses` entry (ND5) | `grant:<tile>` | calls to that tile, including same-scope siblings ([05-model.md](05-model.md) §9) | as the http slot row | as the http slot row | as the http slot row |
| resource grant in another scope, or workspace-level (kv, blob, bus, cron) | `grant:res:<scope>/<name>` | the kv/blob/bus APIs; bus WS reads and push subscriptions on it | yes | `read`, `block` | `read` |
| workspace-level filesystem or sqlite grant, on a workspace-scope tile | `grant:res:workspace/<name>` | the path env and rw bind (`resources.go:83-91`) | no: a read-only bind is not a safe read. WAL readers need `-shm` write access, and VM guest caches aren't coherent with a writer ([08-data.md](08-data.md) §5 item 6, §7) | `block` only (P23): no bind, no remap; the env var stays | blocked |
| code grant | `grant:code`, `grant:code:<tile>` | `/c/` and `/code` reads of other tiles' source; PR suggestions (`code.go:138-145`, `prs.go:99-104`) | already `reader` | `read`, `block` | `read` |
| capability grant | `grant:gpu:<n>`, `grant:cap:net-admin`, `grant:cap:containers`, `grant:cap:open-links`; later `grant:cap:sandboxes` (D113) | the deployment's own sandbox or document (`internal/broker/gpu.go:14-96`) | no role | `inherit`, `block` | `inherit`, except `gpu:*` `block` (§5.9) |
| governance grant | `grant:xbin`, `grant:xbin:*` | — | — | none: P19 keeps such tiles single-deployment, and non-primary principals never hold it (§5.3) | — |

**Not edges:**
- own-scope resources, which are deployment data (P14; [08-data.md](08-data.md));
- self-calls (§2.4);
- `@archive` pseudo-slots: xbind's own calls, `backup.go:35-41`;
- `exposes`, which are inbound (§1).

**The values per kind** are the glossary's (Edge policy):
- `read | block` for edges that can be read-clamped;
- `inherit | block` for role-less edges: the net slot
  (`internal/broker/egress.go:57-91`) and capabilities. `inherit` is D113's
  word for the same idea on tile-sandbox egress
  ([../tile-sandboxes.md](../tile-sandboxes.md) §5), and it never reaches
  host networking or a provider splice;
- `block` alone for edges that can't be read-clamped: custom roles with no
  path to `reader`, stream interfaces, lan-ingress links, net-provider
  splices, a net that resolves to host share (§5.8), and the workspace-level
  file resources above. They are blocked for non-primary deployments in v1,
  with no override (P23): loosening later is easy, tightening after the fact
  is not.

`block` is the single word for "no access" on every kind. A later read-only
value for workspace-level `filesystem` alone would reuse the remap's
read-only flag (08-data §5 item 6); it is not a v1 value.

### 5.2 Storage

- **Where.** The deployment record's `edges` map: `{"<edge id>": <value>}`
  ([05-model.md](05-model.md) §4). It is written only through the
  deployments API, by tile managers in a human session (§6; P26,
  [05-model.md](05-model.md) §10). That route
  ([11-contract.md](11-contract.md) §1.7) takes `inherit` on the net slot and
  on capability edges, where `read` has no meaning.
- **Absent keys** take the edge kind's default. Zero-state tiles have no
  record and never consult it (F1).
- **Writes are validated.**
  - The edge must currently exist on the tile: a bound slot, a grant row or
    a `uses` entry.
  - The value must be allowed for its kind. For example, `read` on a stream
    slot gets 400 `stream edges can't be read-clamped: they are blocked for
    non-primary deployments in v1`.
  - An edge P23 blocks takes no other value. `read` or `inherit` on it gets
    400, naming the role and the provider (or the stream, lan-ingress, host
    share or splice). There is no override value in v1
    (`TestUnclampableEdgesRefuseOverride`).
  - A slot's kind comes from the tile-level manifest, which lives in the
    work tree ([05-model.md](05-model.md) §6). Whether a provider's role can
    be clamped comes from the provider primary's `expose` (F8).
- **Entries for vanished edges are kept, inert.** Re-binding then keeps the
  manager's choice, and the panel lists them as "no longer present".
- **Forward-compatible reads.**
  - A value may be a string or a JSON object.
  - Any value this xbind doesn't know, including every object, reads as
    `block`.
  - A stored value that is invalid for the edge's current kind or role also
    reads as `block`. For example, a stored `read` becomes invalid when the
    provider's primary code changes its `expose` so the role no longer
    implies `reader`, and a hand-written `read` on an edge P23 blocks reads
    as `block`.
  - So a newer xbind can write `match` or `{"policy":"match","else":"read"}`
    (§9), and an older one fails closed (P27).

### 5.3 The single evaluation point

`grantedRole` (`broker.go:418-452`) and `httpBindingRole`
(`netfn.go:286-311`) stay the tile's authority, unchanged, ceiling included
(`broker.go:419`). Next to them sits the only reader of the edge policy.
This is the model's `resolveTarget(caller, callerDeployment, target, edgePolicy)`:

```go
// Decision is the outcome of one call from a tile deployment to a target.
type Decision struct {
	Deployment string   // the target's deployment ("" = main); v1: primary(target)
	Role       string   // effective role on the target; "" when refused
	Clamped    bool     // the read clamp narrowed Role
	Edges      []string // the caller's edges authorizing this call
	Deny       error    // non-nil: refused, naming the rule or the edge
}

func (b *Broker) resolveTarget(caller, callerDep, target string, edges map[string]json.RawMessage) Decision {
	role, ok := b.grantedRole(caller, target) // the tile's authority, unchanged
	d := Decision{Deployment: b.primaryOf(target)} // `match` (§9) changes only this
	switch {
	case !ok:
		d.Deny = errNotGranted(caller, target) // today's 403 and wording
	case b.isPrimary(caller, callerDep): // includes every zero-state tile (F1)
		d.Role = role
	case isGovernance(target): // xbin, xbin:* — P19's backstop
		d.Deny = errGovernance(caller, callerDep)
	default:
		d = b.applyEdgePolicy(d, caller, target, edges) // §5.4–§5.9
	}
	return d
}
```

`applyEdgePolicy` enumerates the authorizing edges with their roles:
- explicit grant rows → `grant:<target>`;
- each bound http slot → `slot:<name>`, with the role from `httpProvideFor`
  (`netfn.go:261-281`) and `provideRole` (`netfn.go:712-717`);
- same-scope `uses` entries → `grant:<target>`.

It then applies §5.4, then §5.5, or the capability rule of §5.9.

**The callers that must go through it.** Every one takes the principal (or
its tile and deployment) instead of a bare path:

| Caller | Code | Uses the decision for |
|---|---|---|
| The proxy's routing function (§4.1) | replaces `broker.go:478-490` | component calls |
| `allowRes` | `broker.go:493-509` | kv/blob (`resources.go:185`, `:308`), bus publish (`:425`); the namespace choice ([08-data.md](08-data.md), §5.10). The cron-resource check at registration (`cron.go:245`) is the exception: it applies the edge's `block` but not the clamp (§6, NP-09-18). |
| `busFilter` | `resources.go:441-458` (hooked at `broker.go:314`) | WS bus reads, clamp plus namespace match (§5.10) |
| Bus-subscription checks | `bussubs.go:422` (register), `:345` (each delivery) | today they build `Principal{Component}` without a deployment; they must pass the subscription's owner |
| `codeGrantAllows` | `code.go:138-145`, via `requireCodeRead` `:151-164`, `canReadPRs` `prs.go:99-104`, `CodeReadGrant` `broker.go:317-319` | code edges |
| Governance checks | `IsAdmin` `broker.go:466-475`; `canCreateAt` `policy.go:105-116`; `requireWriter` `tiles.go:118-128`; `canManageUsers` `usersapi.go:38-50`; `elementXbinCapable` `whoami.go:119-128` | always false for non-primary principals |
| Spawn-time hooks | `EnvFor` `resources.go:71-144`; `GPUFor`, `NetAdminFor`, `ContainersFor` `gpu.go:14-60`; `EgressFor` `egress.go:57-91`; `NetHostShare` `netfn.go:196-210`; `NetClientTarget`, `NetProviderRoster` `netfn.go:223-251`; `NetLinksFor` `ingressfn.go:420-442`; `IngressFwdFor`, `IngressNetFor` `ingressfn.go:313-356` | §5.6–§5.9; the runner passes the spawning deployment ([07-runtime.md](07-runtime.md) picks the mechanism) |
| Document-time hooks | `OpenLinksFor`/`SandboxTokensFor` `gpu.go:82-96`, via `docSandboxExtras` `internal/server/tileassets.go:78` | `cap:open-links` for a non-primary document |

`TestEdgePolicyCallers` fails when non-test code calls `grantedRole`,
`httpBindingRole` or `codeGrantAllows` outside an allow-list of the
functions above. It is a grep test in the style of `TestNoDirectExec`.
`grantedRole` has 16 call sites today.

### 5.4 Several edges to one target: `block` wins

A request can't tell which edge it used. The slot URL `http://xbin/api/<prov>`
(`netfn.go:363`) and a hand-built URL are the same request, and
`grantedRole` merges explicit rows, bindings and same-scope `uses` into one
answer (`broker.go:418-452`). The rule for a non-primary call:
- If **any** authorizing edge is `block` (by value, by default, by P23, or
  because its value is unknown or invalid), the call is refused. The error
  names that edge (P27).
- Otherwise the role is `reader`: every remaining edge is `read`, and each
  `read` edge's role can be clamped (§5.2 refuses and invalidates the rest).

The panel groups edges by target and says so: "blocking any edge to
apps/llm-gw blocks every call to it". A multi slot has one policy, and its
providers are evaluated one by one. `read` on `mcp` gives `reader` on each
MCP provider whose role can be clamped, and the slot's providers whose role
can't be clamped are blocked (P23). A multi slot whose every provider grants
a custom role with no path to `reader`, such as the agent's sandbox-manager
slot, is blocked as a whole (§5.13).

### 5.5 Read clamp semantics

The clamp never yields a role the tile's role on the provider doesn't imply,
because a deployment never widens authority (P11). It follows ND4: the
blessed order `admin ⊃ writer ⊃ reader` (`broker.go:385-388`), custom roles
only through the provider primary's `expose.implies` (F8; `broker.go:389-408`,
`internal/registry/registry.go:32-39`), and the bus aliases `subscriber` =
`reader`, `publisher` = `writer` (`broker.go:372-381`; SDK twin
`sdk/xbin.go:150-169`).

| The tile's role on the target | Clamped role under `read` | Example |
|---|---|---|
| `reader` / `subscriber` | `reader` (unchanged) | `examples/email` → `apps/calendar` and `res:apps/calendar/bus`, both reader (`examples/email/xbin.json:6-7`) |
| `writer`, `admin`, `publisher` | `reader` | the agent template's `llm` providers (`slot:llm`, `builtin-templates/agent/xbin.json:48`), whose binding grants llm-gw's `openai` provide role, `writer` (`builtin-tiles/llm-gw/xbin.json:30`): `GET /v1/models` works, `/v1/*` gets llm-gw's own 403 (§5.12) |
| custom `r`, where the provider's `implies` reaches `reader` | `reader` | a provider declaring `implies: {"editor": ["reader"]}` |
| custom `r` with no implication reaching `reader` | none: blocked in v1, with no override (P23) | webhooks → the agent's `inbox` at `channel` (`builtin-tiles/webhooks/xbin.json:17`; `builtin-templates/agent/xbin.json:63`, `:72`): a non-primary webhooks deployment can't fire the primary agent's triggers. The agent tile → its sandbox managers at `consumer` (§5.13): a non-primary agent deployment has no coding sandboxes. |

**"The provider's reader" means reader as implied by the tile's role.** A
provider that merely *declares* a `reader` role doesn't make `channel`
clampable. `channel` doesn't include what `reader` includes, so clamping to
it would widen the tile's authority.

The refusal error:
- `apps/webhooks's non-primary deployment "dev" can't use edge slot:agents:
  it grants the custom role "channel" on apps/agent, which implies no reader
  the read clamp could narrow it to, so non-primary deployments are blocked
  from it. Test this from the primary, or ask the provider to declare what
  channel implies.`

**Enforcement.**
- **xbind's own APIs** (kv, blob, bus) enforce the clamped role in
  `allowRes`.
- **Component calls** carry it as `X-XBin-Role: reader` and rely on the
  callee's guards (`sdk/xbin.go:172-187`). These are the same guards that
  protect the callee from readers today.
- **A provider that doesn't guard by role** gets no protection from the
  clamp. Its role model is its own (`/docs/auth.md`), and the accident
  boundary is as strong as the guards the provider already has.
  [06-security.md](06-security.md) records this.
- **A clamped call's response names the clamp** ([05-model.md](05-model.md)
  §7): an xbind response header naming the edge and the clamp, like
  `X-XBin-Lifecycle` (`proxy.go:149`). The caller's developer can then tell a
  provider 403 caused by the clamp from any other. It is a different header
  from NP-11-12's response `X-XBin-Deployment`, which names the answering
  deployment; [11-contract.md](11-contract.md) names this one.

### 5.6 Block semantics

**`block` fails closed at the edge.** The status is 403, or a closed
connection for raw streams. The error names:
- the deployment;
- the edge id and its target;
- the policy value;
- who can change it.

For example: `apps/crm's non-primary deployment "dev" may not use edge
grant:apps/calendar: the tile's edge policy for it is "block". A tile manager
can change it in the Deployments panel.` For an edge P23 blocks, the last
sentence instead says that v1 has no value that unblocks it and that the
call works from the primary. The refusal is counted per (deployment, edge)
in memory and shown in the panel as "blocked calls: 12, last 2 min ago"
([05-model.md](05-model.md) §7; [10-ux.md](10-ux.md)). `bx` reports a
refusal it meets with exit code 3 ([11-contract.md](11-contract.md) §9.1).

**The slot env stays present.** A blocked http slot keeps
`XBIN_IFACE_<SLOT>_URL`, its multi list and its `xbin-interfaces` meta. A
blocked stream slot keeps `XBIN_IFACE_<SLOT>_ADDR`. Resource env keeps its
canonical ids. The reasons:
- **Code paths stay identical across deployments.** Tiles often exit at
  start when a slot env is missing. They would crash-loop for a policy
  reason, with the reason hidden, and "unbound" and "blocked" would look
  the same.
- **Env is captured at spawn** (`runner.go:423-431`). Per-call evaluation
  makes policy changes apply without a restart.

Exceptions forced by the mechanics:
- **lan-ingress:** `XBIN_IFACE_<SLOT>_IP` and a provider's `XBIN_LAN_INGRESS`
  are absent, because no leg or roster exists for a non-primary deployment
  (§5.7).
- **Spawn-time edges** (net, capabilities) apply at spawn. Changing them
  restarts the tile's non-primary deployments through the grant-change
  fan-out. The primary is never restarted by an edge-policy change (NP-09-6).

### 5.7 Stream and lan-ingress edges

Neither carries a role, and both reach into the provider's primary as raw
L4/L3 traffic, which the read clamp can't narrow. In v1 they are blocked for
non-primary deployments, with no override (P23): a non-primary deployment
never writes into another tile's primary (glossary, Read clamp). `match` is
their future (§9).

**Stream slots.**
- The spawn-time forward stays in the relay (`runner.go:541-547`).
- `hostDial` (`runner/ingress.go:115-127`) checks the dialing deployment's
  edge at each dial. A refused dial closes at once and writes one line to
  that deployment's log: `stream slot db blocked by edge policy (edge
  slot:db)`.
- The relay belongs to one backend generation, so the runner builds the
  `hostDial` closure with that generation's deployment.

**lan-ingress slots.** A non-primary deployment gets no leg. Legs are spliced
at the provider's roster positions (`ingressfn.go:363-406`). A non-primary
entry would renumber the primary's links and restart the provider and every
client (`netfn.go:182-191`, `runner.go:581-597`).

### 5.8 The net edge

- **`inherit`, the default.** The deployment gets the tile's relay policy,
  because the network belongs to the tile (D54). `netBinding`
  (`netfn.go:47-114`) resolves as today: the ceiling, org and personal sets,
  and named sets. It becomes a relay policy through `EgressFor`
  (`egress.go:57-91`), applied in the deployment's own network namespace.
  `inherit` **never** reaches host networking or a provider splice (glossary,
  Edge policy). Two cases therefore give a non-primary deployment **no
  egress**: its net edge resolves as `block` (P23), and no value restores it
  in v1.
  - **Host share.** The tile's `net` resolves to host sharing: the `host`
    builtin, or an org, personal or named set whose rules say host
    (`NetHostShare`, `netfn.go:196-210`; D54, D65, D88). In the host network
    namespace, untested code could listen on host ports reachable from the
    LAN or the Internet (against P7), take the primary's fixed port while the
    primary restarts and serve its public traffic, and dial host-bound raw
    listeners of its own primary and of other tiles' primaries, which would
    bypass the stream block of §5.7. The reason is recorded like an inert net
    binding (`noteInertNet`, `netfn.go:144`): "host networking serves the
    tile's primary only; non-primary deployments get no egress". Test:
    `TestNonPrimaryNeverSharesHostNetwork` (iso): a `dev` deployment of a
    host-share tile can't bind a host port and can't reach the primary's
    `127.0.0.1` listeners.
  - **A provider-tile binding** is a splice. Splice links belong to the
    provider's roster, which is the primary's, and adding an entry renumbers
    links and restarts the provider and its clients (`netfn.go:182-191`,
    `netfn.go:212-216`, `runner.go:555-597`). Two splicers on one fd would
    also split packets (`netmux.go:22-32`). The recorded reason is "net
    provider splices serve the tile's primary only".

  In both cases the deployment still starts, as with a `none` binding, and
  the panel shows why it has no egress ([10-ux.md](10-ux.md)).
- **`block`.** The net resolves as `none` (`NetRefNone`, `egress.go:97`): a
  deny-all relay with no DNS, still present when ingress plumbing needs it
  (`runner.go:530-535`).
- **Ceilings** (D20 `net` deny) apply unchanged, because they are tile
  authority.
- **Tile-managed sandboxes (D113).** Once built, a sandbox's `inherit`
  egress started by a non-primary deployment is **that deployment's
  effective net edge**. A blocked, host-share or provider-bound net inherits
  as `none` ([research/sandbox-visibility.md](research/sandbox-visibility.md)
  §7; [../tile-sandboxes.md](../tile-sandboxes.md) §5).

### 5.9 Capabilities

Capability grants shape the deployment's **own** sandbox or document, not
another tile's data. `inherit` is exactly the tile's authority (P11), so it
is the default. The exception is shared physical devices:

| Capability | Default | Why |
|---|---|---|
| `cap:net-admin` (`gpu.go:38-41`) | `inherit` | caps inside its own netns. A non-primary deployment of a net provider has no roster anyway (§1). |
| `cap:containers` (`gpu.go:57-60`) | `inherit` | rootless podman in its own sandbox; its storage is a resource in its own namespace |
| `cap:open-links` (`gpu.go:82-96`) | `inherit` | popups from its own documents, opened by `write` users |
| `gpu:*` (`gpu.go:14-25`) | `block` | a shared device. Untested code exhausting VRAM reaches the primary, and cgroup leaves don't cover VRAM. A tile manager sets `inherit` when testing in a non-primary deployment needs the GPU ([05-model.md](05-model.md) §7). |
| `cap:sandboxes` (D113, later) | `inherit` | the deployment addresses its own sandbox set. Caps stay per tile ([05-model.md](05-model.md) §12). The sandbox-managers programme (another branch) lets only sandbox-manager tiles hold it, so this row governs a manager tile's own deployments (§5.13). |

`block` withholds the capability at spawn: no device, caps dropped, no
popup tokens. A policy change restarts the non-primary deployments (§5.6).

### 5.10 Resources and buses across scopes

- **Own scope.** `rt.Scope == caller.Scope`, and non-empty
  (`broker.go:454-460`). This is deployment data, not an edge. It resolves in
  the caller deployment's `(scope, name)` namespace, including siblings'
  shared resources (flow G, [08-data.md](08-data.md)). There is no clamp.
  Each deployment provisions the resources its own code's `scope.json`
  declares, in its own namespace, with limits defaulting to the tile's (P22;
  [05-model.md](05-model.md) §6).
- **A same-scope sibling's API is an edge, even though its data is the
  scope's.** Under `read`, `apps/a+dev` calling `apps/b` reaches `apps/b`'s
  primary. That primary serves the scope's primary namespace, which is
  `apps/a`'s own `main` data. The isolation of non-primary data holds for
  direct access only ([02-goals.md](02-goals.md), Divergence 2). The edge
  row for a same-scope sibling says "reads the scope's primary data" and
  offers `block`.
- **Another scope, or workspace-level.** An edge `grant:res:…`, clamped to
  `reader`. It resolves in **the target scope's primary namespace**
  `(R, P(R))`, where `P(R)` is the scope primary name defined in
  [08-data.md](08-data.md) §1, and in `main`'s workspace data for
  `res:workspace/*` (08-data §4.2).
  08-data keeps `P(R)` well defined by refusing to reassign the primary of
  one member of a multi-tile scope (NP-08-4). That closes the gap the model
  leaves: it keys data by scope and primaries by tile
  ([research/data-plane.md](research/data-plane.md) §6(d)).
- **Bus publish.**
  - Own scope: into the deployment's namespace. The event carries 08-data's
    additive `deployment` field when that namespace isn't `main`'s
    (08-data §4.3).
  - Another scope: refused, because `writer` is clamped away
    (`resources.go:425`).
- **Bus reads, WS and push.** Own-scope subscriptions resolve in the
  subscriber deployment's namespace, other-scope subscriptions in
  `(R, P(R))`. `busFilter` (`resources.go:441-458`) and the publish fan-out
  (`bussubs.go:271-299`) compare the event's namespace with the subscriber's
  resolution, not only topic prefixes. Consequences:
  - A zero-state frame never sees events published in a non-`main`
    namespace, though old clients' topic matching is unchanged
    (`web/xbin-client.js`).
  - Admin and owner sessions keep today's bypass
    (`internal/server/server.go:606-608`) and receive every namespace, with
    the event's `deployment` field naming it. A frame principal is never an
    admin, so a frame in an admin's browser takes the namespace check like
    any other (08-data §4.3).

### 5.11 What callees see

- **`X-XBin-Deployment: <name>`** is set by `identify`
  (`proxy.go:275-305`) only when the caller is a non-primary principal. It
  is evaluated per request (§3.6) and absent on the primary's calls, so
  nothing changes today. It is trustworthy because every inbound `X-Xbin-*`
  is stripped first (`proxy.go:276-280`).
  - **Set** on self-calls of a non-primary deployment, and on its outbound
    calls.
  - **Unset** for humans, admins, ingress and synthetic deliveries.
- **`X-XBin-From` stays the tile path.** Callees that key state on it, such
  as the agent's channels and outbox, and sandbox managers' partitions
  (§5.13), must key on `(From, Deployment)` before they accept non-primary
  callers. In v1 they don't receive any: `channel` and `consumer` can't be
  read-clamped, so those edges are blocked (P23).
- **SDK.** `CallerInfo` (`sdk/xbin.go:85-103`) gains `Deployment string`,
  read in `Caller()` (`sdk/xbin.go:107-114`). `""` means the caller is the
  primary, or not a tile. This is zero-dependency and permissive
  (compat rule 8, [/docs/compat.md](/docs/compat.md)), with a changelog
  entry. [11-contract.md](11-contract.md) adds the `docs/protocol.md` row
  beside `X-XBin-Viewed-By` (`docs/protocol.md:64-82`).

### 5.12 A consequence to state plainly: roles that mean "spend"

llm-gw's `openai` provide grants `writer` (`builtin-tiles/llm-gw/xbin.json:30`)
and guards `/v1/*` with `RoleFunc("writer")`
(`builtin-tiles/llm-gw/backend/main.go:274`). Only `GET /v1/models` is
`reader` (`:269`). Under P3's default `read`:
- **Every LLM-using tile loses completions in non-primary deployments.** The
  agent template reaches its models through its `llm` multi slot
  (`slot:llm`, `builtin-templates/agent/xbin.json:48`), whose binding grants
  llm-gw's `openai` role, `writer`. An older instance may instead hold a
  `grant:apps/llm-gw` row at writer (the by-name fallback the template's
  comment names at `:46-47`). Either edge is clamped to `reader`, so a
  non-primary deployment of the agent can list models but can't run a turn.
- **The primary is unaffected.** `block` makes the refusal explicit, but no
  v1 value restores completions.

This is what P3 means, since "a non-primary deployment never writes into
another tile's primary", and v1 ships with it as a stated limitation
([05-model.md](05-model.md) §7). [02-goals.md](02-goals.md) and
[10-ux.md](10-ux.md) say it, and the edge's panel row says "writer is
clamped to reader: /v1/* will refuse". NP-09-14 remains a proposal for a
later rung, not v1.

### 5.13 Agent tiles and sandbox managers

The sandbox-managers programme, on another branch and described here in
words, adds a service contract for sandbox managers. A manager tile provides
`{kind: "http", service: "sandbox-manager", role: "consumer"}`: a custom
role, the `channel` pattern, which keeps the manager's own admin routes out
of reach. Consumers multi-bind managers: the agent tile through a `sandboxes`
slot, and a separate `sandbox-terminal` tile.

- **`slot:sandboxes` is blocked for non-primary deployments in v1, with no
  override (P23).** `consumer` has no implication reaching `reader`, so every
  provider in the multi slot is refused (§5.4) with §5.5's error, naming role
  `consumer`. The panel shows the edge as fixed and counts its refusals
  ([10-ux.md](10-ux.md)). An implementation that finds a non-primary agent
  deployment unable to reach its managers adds no allow value.
- **What a non-primary deployment of the agent tile can do in v1:** serve its
  UI at its deployment URL; keep its own data (its conversations and memory
  live in its own namespace of the tile's `db` resource); call its own-scope
  APIs; list models; call MCP providers whose role clamps to `reader`; fire
  its own scheduled and resume jobs, for itself, unless a tile manager
  switched its deliveries off (§2.4, §7).
- **What it can't do in v1:**
  - run LLM turns (§5.12);
  - reach any sandbox manager (above), so it has no coding sandboxes;
  - receive channel messages: adapters bind the primary's `inbox`, and
    inbound edges reach only the primary (P7).

  Agent changes that need turns or sandboxes are therefore exercised on the
  primary: under parity by a terminal-level user (P4), or by a manager's
  deploy when the primary is protected.
- **Partitions must gain the deployment before any unblock.** A manager
  partitions sandboxes by `X-XBin-From`, which stays the tile path (§5.11).
  Were `consumer` ever unblocked, or were `match` to route a non-primary
  deployment to a manager, that deployment would drive the primary's
  sandboxes. Managers must key partitions on
  (`X-XBin-From`, `X-XBin-Deployment`) first (NP-09-19).
- **A manager tile's own deployments.** In that programme only manager tiles
  hold `cap:sandboxes`. [05-model.md](05-model.md) §12's rule that
  tile-managed sandboxes belong to the deployment therefore applies to a
  manager tile's deployments: a non-primary deployment of a manager drives
  its own deployment's sandbox set, keyed per deployment, never the
  primary's (§5.9).

Test: `TestSandboxManagerEdgeBlocked` (a multi `sandbox-manager` slot whose
providers grant `consumer`), also a row of `TestUnclampableEdgeDefaults` and
`TestUnclampableEdgesRefuseOverride`.

## 6. Self-scoped APIs for non-primary deployments

Every `/api/xbin/*` route has one of P26's three classes
([05-model.md](05-model.md) §10). The table's class column uses these words:
- **Deployment-scoped** routes act on `dep(p)`, in one of three ways:
  - **dormant**: the call succeeds with today's response, and the result is
    stored under the deployment. It has no effect until that deployment is
    primary;
  - **namespaced**: it reads and writes the deployment's own `(scope, name)`
    data namespace ([08-data.md](08-data.md));
  - **per-deployment**: it acts on a tile-keyed store that gains the
    deployment key. `main` keeps today's key.
- **Primary-only** routes refuse non-primary principals with an error that
  names the rule, and have no effect for them.
- **Neutral** routes have no deployment dimension, and their behaviour is
  unchanged.

Humans (tile managers, terminal-level users, admins) inspect a non-primary
deployment's stores through selectors that [11-contract.md](11-contract.md)
defines. Tile principals may pass such a selector only when it names
`dep(p)` (§4).

| API | Code | Today keyed by | Non-primary class | Behaviour |
|---|---|---|---|---|
| Cron jobs: `GET`/`PUT /cron/jobs`, `DELETE /cron/jobs/{name}` | `cron.go:212-270`; routes `broker.go:283-285` | `(component, name)`, `data/cron-jobs.json` (`cron.go:108`) | per-deployment | stored in the deployment's own file ([05-model.md](05-model.md) §3); fires for it unless its deliveries are off (§7), when the list shows its jobs with `dormant: true`. The `writer` check on a foreign cron resource (`cron.go:245`) applies the edge's `block` but not its clamp: a job only ever schedules the deployment's own handler. Under `read` it is stored; under `block` it is refused (08-data §7; NP-09-18). |
| Bus subscriptions: `GET`/`PUT /bus/subscriptions`, `DELETE …/{name}` | `bussubs.go:371-449`; routes `broker.go:273-275` | `(component, name)`, 64 per component (`bussubs.go:50`, `:194-202`) | per-deployment | as cron; the cap is 64 per `(tile, deployment)`, so `main`'s is unchanged (NP-09-16). A subscription on a foreign bus reads the scope's primary's events, like a read binding: stored under `read`, refused under `block` (08-data §7), and re-checked against the edge at every delivery (§7). |
| Interface instances: `PUT /iface-instances` | `netfn.go:1037-1131`; route `broker.go:254` | root `xbin.json` `ifaceInstances[comp]`, replaced whole | dormant | stored per deployment; no `grants` event and no consumer restarts (`netfn.go:1115-1129` skipped); active only while the deployment is primary; deliveries never activate it |
| Ingress hosts: `PUT /ingress-hosts` | `ingressfn.go:491-583`; route `broker.go:255` | root `xbin.json` `ingressHosts[comp]` | dormant | zone-validated as today (`ingressfn.go:523-563`); takes no part in conflict checks (`ingressfn.go:587-607`) and triggers no reconcile; active only while primary (NP-09-13) |
| Ingress routes: `GET /ingress-routes` | `ingressfn.go:613-639` | `Source == p.Component` | dormant | an empty list. A non-primary traefik then renders no ACME for the primary's hostnames ([research/inbound-edges.md](research/inbound-edges.md) §3 F.23; NP-09-12). |
| Bus publish: `POST /bus/publish` | `resources.go:410-437` | resource | namespaced (own scope); refused by the clamp (other scopes) | §5.10 |
| Push notify: `POST /notify`, SDK `NotifyUser` | `push/api.go:287-395`; route `internal/boot/push.go:155`; `sdk/notify.go:48-59` | tile, per-tile budget, mute, link | dormant | 202 as today; recorded as "would notify <user>: <title>" in a bounded per-deployment list, announced only in the `deployments` event to §6.1's audience; never pushed, never charged to the tile's budget (`push/api.go:356-363`) (P13) |
| Tile report: `POST`/`GET /tile-report`, SDK `Status`/`ClearStatus`/`Notify` | `internal/obs/status.go:34-143`; SDK `sdk/xbin.go:393-408` | `statuses[comp]` in memory | per-deployment | stored under `(tile, name)`. A non-primary deployment's status and toasts ride only the `deployments` event, which carries `deployment`, never `status` or notify, and never a qualified `component` on an old type ([05-model.md](05-model.md) §8; §6.1). A non-primary build clears only its own deployment's status (`status.go:129-143`). |
| kv: `/kv/{rest...}` | `resources.go:173-298`; routes `broker.go:277-279` | bucket = canonical id | namespaced | own scope in its namespace; other scopes an edge (§5.10) |
| blob: `/blob/{rest...}` | `resources.go:302-406`; routes `broker.go:280-282` | cipher dir per `(scope, name)` | namespaced | as kv |
| sqlite and filesystem (env plus binds) | `resources.go:83-91`; `internal/runner/binds.go` | path per `(scope, name)` | namespaced | the deployment's volume bound at the primary's path, so `XBIN_RES_*` stays identical ([07-runtime.md](07-runtime.md), [08-data.md](08-data.md)) |
| Vault: `/vault/{rest...}` | `vault.go:151-200`; routes `broker.go:268-270` | `data/vault/<CompKey>.json` (`vault.go:45-47`) | per-deployment | the deployment's vault ([08-data.md](08-data.md) §10), starting with key names only (P14); values readable only by the backend (D30); `main` keeps today's file |
| Logs: `GET /logs` | `internal/obs/logs.go:42-80` | `.xbin/log/<CompKey>.log` (`:69`) | per-deployment | its own log: `.xbin/deploy/<TileKey>/d/<name>/backend.log` for a non-`main` deployment; `main` keeps today's file. A tile principal reads only `dep(p)`'s log (§4.2); humans keep today's terminal-level gate (`logs.go:46`). |
| Tile status: `GET /tile-status` | `internal/boot/api.go:87-117` | the first `Inspect` row by path | per-deployment | its own backend row; a tile principal reads only `dep(p)`'s |
| Prefs: `/prefs…` | `internal/obs/prefs.go:26-48` | `(user, comp)` | per-deployment | `(user, tile, deployment)` ([05-model.md](05-model.md) §9) |
| Frame-token minting: D4 injection, `GET /frame-token` | `static.go:431-441`, `:494-503`; `api.go:241-252` | `(component, user, gen)` | per-deployment | claim = the document's or caller's deployment; renewal only for its own (§3.3) |
| whoami: `GET /whoami` | `whoami.go:36-70`; route `usersapi.go:20` | element id = path | neutral | also reports `deployment` under [11-contract.md](11-contract.md)'s role rule (§0.2): present when the principal's deployment isn't the primary |
| Sandboxes: `GET /sandboxes` (D112, admin today); D113's tile API (designed) | `internal/boot/sandboxes.go:34-50`; [../tile-sandboxes.md](../tile-sandboxes.md) §3 | admin; D113: the caller's tile | per-deployment | a non-`main` backend's registry row has ID `backend+<name>:<CompKey>:g<gen>` and `Entry.Deployment` set; `main`'s rows stay byte-identical (`backend:<CompKey>:g<gen>`, no `Deployment`, `internal/runner/sbx.go:63`), even when the tile has a record ([05-model.md](05-model.md) §12). Once D113 is built, a non-primary backend drives only its own deployment's set (§5.13), and `inherit` egress follows §5.8. |
| Code reads: `/code/*`, `/git/*` | `code.go:35-41`, `:147-164` | self, or a code grant | neutral (self); edge (other tiles) | the work tree has no deployment dimension |
| Code PRs: `POST`/`GET /code/prs`, decisions | `prs.go:77-78`, `:95-123` | target, `From.Component` | neutral; deciding is primary-only for instance principals (P26) | opening a PR to another tile is a read-level suggestion (D48) through the code edge |
| Governance: user management, tile creation from a tile, admin APIs | `usersapi.go:38-50`; `policy.go:105-116`; `tiles.go:118-128`; `broker.go:466-475` | `xbin`, `xbin:*` grants | primary-only | P19 backstop (§5.3) |
| The deployments API (this feature) | [11-contract.md](11-contract.md) §1; the plane is `internal/deployments` | — | refused for instance and frame principals of every deployment, primary included | terminal-level acts: humans with terminal level, and the tile's terminal and agent tokens while their user holds it. Manager acts need a human session: `p.Component == ""` and (`IsAdmin` or `mayManageTile`; `internal/auth/auth.go:92`, `internal/broker/orgsapi.go:427`). No element principal passes the manager gate, whatever `xbin` or `xbin:users` grants its tile holds ([05-model.md](05-model.md) §10). Onto a protected primary, only managers, naming the reviewed checkpoint ([05-model.md](05-model.md) §5, Reviewed operations). |
| The checkpoint fetch remote ([11-contract.md](11-contract.md) §1.12) | new | — | neutral; refused for instance and frame principals | served from the tile's **view repository** (`data/checkpoints/<TileKey>.view.git`), never the store: only `refs/heads/deploy/<name>` of pinned deployments, each pointing at the checkpoint's git view, plus `HEAD` naming the primary's ref; no `refs/xbin/*` is advertised. Readable by the tile's own terminal and agent sessions (any target) and humans with ≥ `write` on the tile (their current level); other tiles never. An allow-list of dumb-HTTP files opened beneath the view repository, refusing symlinks, refreshed by a confined `update-server-info`. Injected per session through `GIT_CONFIG_*` (beside `internal/term/term.go:789`) only while the tile has a record, never written into `.git/config` ([05-model.md](05-model.md) §3). A tile without a record has neither remote nor view repository, and nothing here creates a checkpoint store: it exists only after a committed opt-in, never from a dry run or a diff ([05-model.md](05-model.md) §5). |
| `/ws/events` | `server.go:592-612`; `resources.go:441-458` | reader grant (bus); every subscriber (other types) | namespaced (bus); filtered (non-primary facts) | §5.10; §6.1 |

**Enforcement: default-deny (P26).** The research's failure mode for
option (i) is that any handler left unconverted writes the primary's state,
keyed by `p.Component` ([research/serving-fabric.md](research/serving-fabric.md)
B.6). The counter:
1. **One table** maps every `RegisterAPI` pattern to its P26 class and, for
   deployment-scoped routes, to a handler aware of the deployment. The table
   is the one above plus the pure read APIs, each classed explicitly.
2. **Unclassified routes refuse non-primary principals, reads included.**
   `server.handleAPI` (`server.go:563-590`) finds the matched pattern
   through the API mux and refuses such principals when the pattern has no
   class: `403 not available to non-primary deployments yet`. An unconverted
   read of own-scope data would otherwise read the primary's namespace.
3. **A test keeps the table complete.** The route inventory already
   enumerates every `RegisterAPI` literal
   (`internal/apicheck/apicheck_test.go`). `TestDeploymentRouteClasses`
   fails when one lacks a class.
4. **Zero-state tiles never hit the check**, since they have no non-primary
   principals (F1). Audit lines (`server.go:568-580`) add the deployment for
   non-`main` principals.

### 6.1 Who sees non-primary facts: events and the reader view

**Events (05-model §8).**
- Non-primary activity never rides `reload`, `build-*`, `status` or notify.
  It rides only the `deployments` event type, which carries `deployment`.
- No qualified `component` string appears on an old event type, ever: not
  for deploys, not for reassignment (§8), not for lifecycle.
- A successful swap on the primary emits one `reload` for the bare
  component, only when the primary's code changed. A deploy's phases and
  failures ride `deployments`, never `build-start`/`build-error`.

[11-contract.md](11-contract.md) §3 owns the payload.

**The audience.** Today a non-bus event reaches every subscriber, except
`pr` (tile readers) and `term`/`session` (per user) (`server.go:592-612`).
The filter gains one rule:
- Facts about the primary go to the tile's readers, as today.
- Anything naming a non-primary deployment goes only to:
  - admins;
  - humans with ≥ `write` on the tile, at their current level;
  - that deployment's own principals (its frame and instance principals);
  - the tile's terminal and agent sessions.

  That covers its name, builds, compiler output, status, would-notify
  lines and data ops.
- The primary's frame token is minted for the tile's readers (§3.3), so it
  is **not** an own principal for non-primary facts. Neither is an instance
  token of another deployment.
- Other tiles receive none of it.

The same audience applies to every other read that names a non-primary
deployment (its deploy log, diffs, logs and status), with a tile principal
limited to `dep(p)` (§4.2). Tests: `TestDeploymentEventsFiltered`,
`TestPrimaryFrameTokenGetsNoNonPrimaryFacts`.

**What readers see.** A reader of the tile gets only primary-scoped facts:
- `GET /api/xbin/deployments` answers with live reload state as it concerns
  the primary, the primary's checkpoint and deploy state, and protection.
  It has no non-primary deployment names, and no counts that reveal them;
  it is a filtered view, not a 403.
- `/components` adds only the primary summary (§1).

[11-contract.md](11-contract.md) owns the exact shape.

## 7. Deliveries, run now and alwaysOn for non-primary deployments

**The deliveries switch.** Per non-primary deployment, an off switch set by
tile managers in a human session, **on by default** (P13, revised by the
owner 2026-09-28: "allowing cron for non-primary deployments is fine; bus is
trickier but subscribe-only would be ok — like read binds") ([05-model.md](05-model.md)
§5, §10). It is D85's term for cron and bus POSTs. In the record an absent
`deliveries` is on and `false` is off; turning it on clears the key. The M2
build stored only `true` (it omitted its default off), which reads as on, so
no record migrates.
- **Owner.** Every cron job and bus push subscription records its owning
  deployment. `main`'s stay in today's stores without the field; the others
  live in per-deployment files ([05-model.md](05-model.md) §3).
- **The active set** is every deployment's cron jobs and bus subscriptions,
  less those of each non-primary deployment whose deliveries are off. This
  is evaluated when registrations change and at each fire or publish.
- **Cron.** `fire` (`cron.go:166-186`) returns early for an inactive owner,
  beside today's lifecycle gate (`cron.go:171-173`). The dispatch principal
  carries `Deployment = owner`, and routing rule 1 (§4.1) sends it there,
  never to the primary. A non-primary deployment starts lazily on a tick; no
  alwaysOn is needed.
- **Bus.** `publish` (`bussubs.go:271-299`) queues nothing for inactive
  owners and counts them as dormant, not dropped. `deliver` (`:337-362`)
  re-checks that the owner is active and that the owner deployment can read
  (§5.3), then dispatches to the owner. Namespaces match per §5.10: an
  own-scope subscription hears its owner's (scope, name) namespace, and an
  other-scope subscription receives the target scope's primary events — a
  read, which the edge's `read` allows and `block` refuses, like a read
  binding. Publishing is unchanged (§5.10): a non-primary deployment
  publishes only into its own namespace.
- **Activation re-checks** ([08-data.md](08-data.md) §7 leaves them to this
  document). A bus subscription meets its edge at every delivery, as the
  reader check does today (`bussubs.go:345`), so a later `block` stops it.
  A cron job meets no edge when it fires, because it dispatches only to its
  own deployment (NP-09-18).
- **Toggling applies at once.** A job of a deployment switched off stays
  scheduled and its tick returns early; bus queues are gated. Interface
  instances and ingress hosts are never activated by deliveries
  ([05-model.md](05-model.md) §7).

**Run now.** One delivery of one cron job of a non-primary deployment Y.
- It is dispatched as `xbin/cron` with the job's registered role, to Y,
  whatever Y's deliveries switch says.
- One run is in flight per job at a time.
- The panel shows the status code and the first line of the body, as
  `cron.go:182-185` logs them.
- It needs terminal level on the tile ([05-model.md](05-model.md) §5, §10).
  A terminal targeting Y can already call the handler itself, and run now
  only adds the job's role and the `xbin/cron` sender
  ([11-contract.md](11-contract.md) §1.9).
- There is no run now for bus subscriptions or for the primary's jobs in v1:
  zero-state tiles gain nothing (P5).

**alwaysOn for a non-primary deployment.** A per-deployment switch, set by
tile managers in a human session, off by default. It requires that Y's own
code declares
`alwaysOn`, since that field is deployment-level
([05-model.md](05-model.md) §6).
- `WakeAlwaysOn` (`alwayson.go:45-61`) starts Y at every wake point (§1).
- The reaper exemption (`runner.go:819`), the backoff (`alwayson.go:100-137`)
  and the crash breaker key per deployment.

It is off by default because alwaysOn tiles hold exclusive external
connections: two bridges on one platform token would both post (D84, D86;
[research/inbound-edges.md](research/inbound-edges.md) §3 F.21). Because
the vault is per deployment (P14), a non-primary bridge holds no token until
a manager copies one.

## 8. Primary reassignment

**The atomic routing switch.** M2. Precondition: Y exists and is healthy
([05-model.md](05-model.md) §5).
1. **Validate first.**
   - The actor is a tile manager in a human session (§6; P26), and the
     request carries the loud confirmation that names whose data the new
     primary serves ([11-contract.md](11-contract.md) §1.7 owns the field).
     Without it, 400; `bx` without a TTY needs `--yes`, else exit 4
     ([11-contract.md](11-contract.md) §9.1).
   - While the primary is protected, the request also carries `expect`,
     naming the checkpoint Y runs, which the manager reviewed. Without it,
     400. The commit is a compare-and-set on the record's `seq`, with the
     actor's authority re-checked; a record that moved since the review
     answers 409 ([05-model.md](05-model.md) §5, Reviewed operations). With
     live reload on Y, Y is first pinned in place, since a protected primary
     is never the live reload target (P21).
   - Y's dormant ingress hosts are checked against other tiles' active
     registrations (`ingressfn.go:587-607`). Conflicting hosts stay inactive
     and are reported in the result.
   - A chrome or governance tile has no non-primary deployment to reassign
     to (P19).
   - In v1 the tile must be in the workspace scope or be the only member of
     the scope it roots, which keeps `P(S)` defined ([05-model.md](05-model.md)
     §5; [08-data.md](08-data.md) §1).
2. **Replace the record.** It is written with `fsutil.WriteFileAtomic`,
   then the in-memory value is replaced (§3.6). From that instant every
   decision in §1, §4 and §5 resolves Y. Requests already dispatched finish
   on X. Long-lived streams to X last until step 5 cuts them.
3. **Registrations.**
   - X's interface instances and ingress hosts become dormant and Y's
     become active. Each deployment's cron jobs and bus subscriptions keep
     firing for it, X's included, unless its deliveries are off (§7).
   - `HTTPSlots` (`netfn.go:366`) reads Y's interface instance table. The
     consumers bound to T restart with re-injected URLs, as
     `netfn.go:1115-1129` does today.
   - `IngressLookup` (`ingressfn.go:133`) reads Y's host set, and
     `OnIngressChange` reconciles (`ingressfn.go:578-581`).
   - Nothing is written to the root `xbin.json`. `main`'s rows stay where
     they are; its routes are dormant because of the record. An older xbind, which ignores
     the record, sees `main` as primary again ([12-compat.md](12-compat.md)).
4. **Events.**
   - The `deployments` event, to §6.1's audience; readers see only that the
     primary's code and state changed ([05-model.md](05-model.md) §8).
   - One `reload` for the bare component. Open bare-URL frames hold X's
     tokens and must re-navigate to Y, minting new claims; readers' frames
     would otherwise hit the `write` gate of §2.2.
   - No `reload`, `status` or other old event type ever names `<tile>+<X>`
     or `<tile>+<Y>` (§6.1). Frames open at a deployment URL learn of the
     change from `deployments`.
   - `grants` for T's consumers when the active interface instances changed.
5. **Restarts, for every tile.** Role decides wiring that is captured at
   spawn: the net edge (§5.8), capabilities (§5.9), stream forwards and
   doors (§5.7, §1), lan-ingress legs and rosters, netmux registration
   (`runner.go:506-597`), and the backend's `XBIN_DEPLOYMENT`
   ([11-contract.md](11-contract.md) §0.2, NP-11-1). So:
   - Y restarts through blue/green with primary wiring, on its pinned
     artifact (P9). If T provides a network, Y registers the roster, which
     closes X's link fds (`netmux.go:28-30`), and nudges the clients to
     re-splice (`runner.go:581-597`).
   - X then restarts with non-primary wiring, or is stopped if it was idle.
     Either way its long-lived inbound streams are cut, and clients
     reconnect to Y.
   - The order is Y first, then X, so a network provider's clients are
     never without links.

   [07-runtime.md](07-runtime.md) §8.8 agrees: it restarts the running
   generations of both, blue/green, new primary first.
6. **Markers flip at once** (§3.6). Y's calls lose `X-XBin-Deployment`. X's
   carry X's name (`main` in flow F) and are clamped. Y's status becomes the
   bare component's `status`; X's rides only `deployments` (§6.1).

**Data does not move** (P7). The UI requires confirming that Y's data now
serves every inbound edge (flow F). Sessions with a named target keep it,
and sessions that follow the primary move to Y (§3.5). While the primary is
protected, sessions whose named target was Y are restarted onto P24's
default (§2.3). Tests:
[15-test-plan.md](15-test-plan.md)'s
`TestReassignPrimaryAtomic` and `TestReassignPrimaryFlowF`, extended to every
step here, including a net provider with clients and a `prov#inst` consumer.

## 9. The parallel fabric (`match`, later)

`match` is deferred (M3; [05-model.md](05-model.md) §15). This section
fixes what v1 must already honour, so that adding `match` is purely
additive.

**How resolution changes.** Under `match`, `resolveTarget` looks for a
provider deployment with the caller's deployment name.
- If the provider has one and it is **not** the provider's primary, the
  target is that deployment, at the tile's **full** role. Its writes land in
  the provider's non-primary data, which is the point.
- Otherwise the fallback applies.
- A matched deployment that *is* the provider's primary (the provider
  reassigned its primary to `dev`) takes the fallback. Otherwise `match`
  would write into a primary.

**The fallback when the provider has no same-named deployment:**

| Fallback | Effect | Recommendation |
|---|---|---|
| `deny` | refused, like `block` | offered: for edges whose reads of the primary are unwanted |
| `primary-read` | the read clamp to the provider's primary, i.e. today's `read` | **the default**: `match` then strictly extends `read`. Switching an edge from `read` to `match` never loses a read that worked. |
| `primary` (full role) | writes into the provider's primary | **never offered.** It is the leak across named copies that every platform in the prior art warns about ([research/prior-art.md](research/prior-art.md) §6, §12). |

[04-options.md](04-options.md)'s NP-04-7 proposes refusing a missing name
instead, with no fallback to the primary at all. The two proposals agree on
never offering a full-role fallback. They differ only on whether the default
fallback is `deny` or `primary-read`, which is left to the owner (see
[Open questions](#open-questions)).

**Data implications.**
- A matched call's writes land in the provider deployment's namespace. For
  resources, that is `(provider scope, name)`, which exists only when that
  scope has a deployment of that name.
- Otherwise the resource takes the fallback. Workspace-level resources
  always take the fallback.
- Seeding the provider's namespace is the provider's tile managers' act
  (P14). The consumer's managers can't seed another tile's data.

**Beyond HTTP.**
- **Stream forwards** name the matched deployment at spawn
  (`stream:<prov>+<name>:<port>`), dialed through a deployment-aware dial
  beside `DialInto`, which keeps meaning the primary (F3).
- **Net and lan-ingress** get a roster **on the matched provider
  deployment**, holding only matched clients. Each provider deployment has
  its own TUN set, so the primary's link numbering is never touched. That is
  the structural reason v1 gives non-primary deployments no links at all.

**Provider-side consent.** A matched call is an inbound edge into a
non-primary deployment, a deliberate exception to P7. The provider's tile
managers opt each of their deployments in to receiving matched calls. The
precedent is D33's provider-side consent (NP-09-15). A provider that keys
state on `X-XBin-From`, such as a sandbox manager's partitions, must key it
on (`X-XBin-From`, `X-XBin-Deployment`) before it opts in (§5.13,
NP-09-19).

**UI.** The edge row offers "match (→ the provider's same-named deployment,
else read)". The panel lists each non-primary deployment's resolution per
edge: "dev → apps/calendar+dev", "qa → apps/calendar, read (no qa
deployment)" ([10-ux.md](10-ux.md)).

**Why P3's per-edge record makes it additive.**
- The key space doesn't change: edge ids.
- `match` is a new value, possibly an object, that v1 already reads as
  `block` (§5.2).
- `resolveTarget` already receives the caller's deployment. Only the
  `Deployment` line and the role path change (§5.3).
- The primary never reads edges.
- xbinds without deployments ignore the record entirely.

**The D32 precedent.** D32's allowance grammar
`iface:<svc>@<tile-glob>[#<inst-glob>]` already expresses "the dev instance
only, for this org" ([../DECISIONS.md](../DECISIONS.md) D32). Pointing a
consumer at a provider's non-primary variant is already a governed choice in
xbin. `match` generalizes that static address into a rule ("the same name as
my deployment"), per edge. An org allowance could later require `match` or
`deny` for non-primary deployments in the same grammar.

**Why interface instances are not deployments (IFACE-7):**
- **An instance is an address, not a runtime.** It is a path prefix inside
  the provider's one backend (`netfn.go:363-373`). Instances share its code,
  data, vault and process. A "dev instance" runs the primary's code on the
  primary's data.
- **Instances don't isolate authority.** The call grant ignores the
  instance; the role comes from the provide (`httpBindingRole`,
  `netfn.go:286-311`).
- **The provider owns the address book.** Instances are registered at
  runtime by the provider, replacing its whole map and restarting consumers
  (`netfn.go:1102-1129`). An operator doesn't manage them.
- **`#` is the instance separator** (`netfn.go:315-320`). Deployments use
  `+`, so `prov#dev` and `prov+dev` never mean the same thing.

The two compose: a deployment registers interface instances, which stay
dormant unless it is primary (§6). A binding to `prov#inst` resolves against
the provider primary's active table (§1).

## 10. Tests this document requires

[15-test-plan.md](15-test-plan.md) owns the matrix. It already names most
of what this document needs:
- `TestInboundEdgesReachOnlyPrimary` and the unit tests of §1;
- `TestGrantedRoleReadClamp`, `TestEdgePolicyBlock`,
  `TestUnclampableEdgeDefaults`, `TestNetEdgeDefault`,
  `TestEdgePolicyNeverTouchesPrimary`, `TestOutboundEdgePolicy` (§5);
- `TestIdentifyDeploymentHeader`, `TestIdentifyZeroState` (§5.11);
- `TestSelfCallRouting`, `TestSelfCallsStayInDeployment`,
  `TestMayMintFrameTokenNeverCrossesDeployments` (=
  `TestMintRefusesCrossDeployment`, in both directions: a `dev` frame
  fetching `/c/<tile>/` and a `main` frame fetching `/c/<tile>+dev/`),
  `TestFrameTokenDeploymentClaim`, `TestInstanceTokenCarriesDeployment`,
  `TestTerminalTokenTarget`, `TestTerminalTokenCannotMintProtectedPrimaryToken`
  (§3, §4);
- `TestSessionTarget` (the default is the primary; with the primary
  protected, the live reload target; with that paused too, API off) and
  `TestTargetProtectedPrimary` (a protected primary is never offered, and a
  request naming it gets 403; protecting the primary makes a session token
  that follows it answer 403 at once, and restarts the session onto the
  default) (§2.3);
- `TestSandboxManagerEdgeBlocked` (§5.13): a multi `sandbox-manager` slot
  whose providers grant `consumer`;
- `TestDormantRegistrations`, `TestDeliveriesSwitch`, `TestRunNow`,
  `TestNotifyNotPushed`, `TestBusPublishStaysInNamespace` (§6, §7);
- `TestDeploymentEventsFiltered` (§6.1);
- `TestReassignPrimaryAtomic`, `TestReassignPrimaryFlowF` (§8).

This document adds:
- `TestZeroStateRoute`: for tiles without a record, `Route`,
  `resolveTarget`, `allowRes` and `busFilter` equal today's `Policy`,
  `grantedRole`, `allowRes` and `busFilter`, over a generated matrix of
  grants, bindings and scopes (F1);
- `TestDeploymentURLGate` (§2.2): every principal row;
- `TestCrossDeploymentRefused` (§4.2): every self rule, both directions;
- `TestEdgeBlockWins` (§5.4) and `TestEdgeUnknownValueBlocks` (§5.2);
- `TestUnclampableEdgesRefuseOverride` (§5.2): `read` or `inherit` written to
  a stream, lan-ingress, custom-role, host-share or splice edge answers 400,
  and a hand-written `read` in the record reads as `block` (P23);
- `TestEdgeCapabilities`, `TestEdgeStreamDial`, `TestDialIntoPrimary`,
  `TestTerminatorDoorPrimaryOnly`, `TestNonPrimaryNeverSharesHostNetwork`
  (iso; §1, §5.7, §5.8, §5.9);
- `TestBusNamespaceMatch`, `TestIfaceInstanceFollowsPrimary` (§1, §5.10);
- `TestDeploymentRouteClasses` (§6, P26) and `TestEdgePolicyCallers` (§5.3):
  guard tests;
- `TestPrimaryFrameTokenGetsNoNonPrimaryFacts` (§6.1): a reader's primary
  frame token receives no `deployments` event naming `dev`, and a reader's
  `GET /api/xbin/deployments` lists no non-primary name or count;
- `TestNonPrimaryAlwaysOn` (§7);
- `TestDrainAuthority` (§3.7): a draining generation acts only as its own
  deployment, and a reassignment clamps it on its next call.

## Open questions

- `match`'s default fallback: `primary-read` (NP-09-15) or `deny`
  (NP-04-7)? Both exclude a full-role fallback to the primary.
- Is one policy per multi slot fine-grained enough? The agent's `mcp` slot
  binds several providers (`builtin-templates/agent/xbin.json:49`), and a
  per-provider key (`slot:<name>#<provider>`) would be additive later.

Resolved since the first draft: v1 ships without restoring LLM completions
(05-model §7); sessions never target a protected primary (P24); `gpu:*`
defaults to `block` (05-model §7); the audience of non-primary events is
§6.1's (05-model §8).

## Divergences from the model

1. Resolved by 05-model §7: the spine states that the read clamp takes LLM
   completions away from non-primary deployments (§5.12).
2. Resolved by P17 (05-model §13): signals are absent for the primary, and
   credentials and stored state are absent for `main`.
3. Resolved by 05-model §5's reassignment row: the new primary starts first,
   the old one restarts or stops, and `reload` is emitted for the bare
   component (§8).
4. Resolved by 05-model §7 (P12's strength per credential) and P24 (no
   session targets a protected primary; §4.4).
5. Resolved by the glossary's Edge and Edge policy definitions (capability
   and code grants are edges; `inherit` is a value).
6. **Tile-managed sandboxes and manager tiles.** [05-model.md](05-model.md)
   §12 says tile-managed sandboxes belong to the deployment and that
   `cap:sandboxes` stays the tile's, as if any tile may hold it. The
   sandbox-managers programme (another branch) lets only sandbox-manager
   tiles hold `cap:sandboxes`, and ordinary tiles reach sandboxes through a
   manager at the custom role `consumer`, which P23 blocks for non-primary
   deployments.
   - Evidence: that programme's plan (phase 2, "Only a manager tile …
     calls `/api/xbin/sandboxes`"); `builtin-templates/agent/xbin.json:42-54`
     today has no `sandboxes` slot, which that branch adds.
   - Fix: 05-model §12 says the rule applies to a manager tile's own
     deployments, and that other tiles' non-primary deployments have no
     sandboxes in v1 (§5.13).

## New proposals

- **NP-09-1** — Resolved by the glossary's Edge policy and P23.
- **NP-09-2** — Resolved by P27 (`block` wins) and P23 (a multi slot's
  providers whose role can't be clamped are blocked); §5.4 states the rule.
- **NP-09-3** — Resolved by P27.
- **NP-09-4** — Resolved by P26.
- **NP-09-5** — Resolved by 05-model §7 (`gpu:*` defaults to `block`, other
  capabilities to `inherit`).
- **NP-09-6** — `block` keeps the slot env and fails at call or dial time
  with an error naming the edge. The exceptions are lan-ingress (no leg) and
  spawn-time edges (restart on change). Env is captured at spawn
  (`runner.go:423-431`).
- **NP-09-7** — Resolved by 05-model §7 (a clamped call's response names the
  clamp); [11-contract.md](11-contract.md) names the header.
- **NP-09-8** — Resolved by 05-model §5's reassignment row;
  [07-runtime.md](07-runtime.md) §8.8 agrees.
- **NP-09-9** — Resolved by 05-model §5 and §10 (run now is terminal level,
  one run in flight per job).
- **NP-09-10** — Resolved by 05-model §7 (the panel counts refusals per
  edge).
- **NP-09-11** — Resolved by 05-model §7 (the user's current level on every
  request).
- **NP-09-12** — Non-primary terminators get no forward door and an empty
  `/ingress-routes`. Non-primary net or lan-ingress providers register no
  roster (`ingressfn.go:313-317`, `ingressfn.go:613-639`,
  `netfn.go:182-233`).
- **NP-09-13** — Dormant ingress hosts take no part in conflict checks and
  trigger no reconcile (`ingressfn.go:587-607`). Re-validation at activation
  is resolved by 05-model §5.
- **NP-09-14** — A later, manager-set edge value `full` lifts the clamp on
  one edge: the tile's own role, at the provider's primary. It is meant for
  providers whose write role means spend, not state
  (`builtin-tiles/llm-gw/backend/main.go:274`). v1 ships without it
  (05-model §7). It needs owner ratification because it relaxes P3's "a
  non-primary deployment never writes into another tile's primary"; P23 does
  not decide it, since `writer` can be read-clamped.
- **NP-09-15** — `match` falls back to `read` by default, with `deny`
  optional. A full-role fallback to the primary is never offered, and a
  match that lands on the provider's primary takes the fallback. The
  provider's managers opt each deployment in to receiving matched calls
  (D33 precedent). This differs from NP-04-7, which always refuses a
  missing name.
- **NP-09-16** — Bus push subscriptions are capped at 64 per
  `(tile, deployment)`, which keeps `main`'s cap unchanged
  (`bussubs.go:50`, `bussubs.go:194-202`).
- **NP-09-17** — Resolved by P26 (05-model §10: PR decisions are
  primary-only for instance principals).
- **NP-09-18** — Registrations on foreign resources:
  - a bus push subscription is re-checked against its edge at every
    delivery, as the reader check is today (`bussubs.go:345`);
  - a cron job on a foreign cron resource is checked against `block` when
    it registers, but not clamped (`cron.go:245`), because it only ever
    dispatches to its own deployment.

  [08-data.md](08-data.md) §7 leaves this to the fabric.
- **NP-09-19** — Before any later value unblocks the custom role
  `consumer`, or `match` routes a non-primary deployment to a sandbox
  manager, managers key their partitions on (`X-XBin-From`,
  `X-XBin-Deployment`), not on `X-XBin-From` alone. `X-XBin-From` stays the
  tile path (§5.11), so otherwise a `dev` deployment would drive the
  primary's sandboxes (§5.13). The same holds for any provider that keys
  state on the caller, such as the agent's channels and outbox.
