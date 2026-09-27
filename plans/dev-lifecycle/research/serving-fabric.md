# Serving identity and the outbound fabric

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `fabric`.

## Report

## Serving identity and the outbound fabric: findings (brief A + B)

All refs are relative to /home/magik6k/buxon. Lines marked "Inference:" are conclusions I drew; they are not stated in the code.

---

### A. Serving a tile from something other than its live directory

#### A.1 How `/c/<tile>/…` is served today

**Route and wrapping**
- The route is `handle("/c/", s.withAssetTokens(s.authedStatic(http.HandlerFunc(s.handleComponentStatic))))` (internal/server/server.go:155).
- The whole mux is wrapped as `logRequests(s.tileOrigins(nullOriginCORS(mux)))` (server.go:173).
- `/vendor/` is unauthenticated and served from xbind's embedded `web/` (server.go:156-160, static.go:582-607). So bx-frame.js, frame-info.js and xbin-client.js always match the running binary.
- The shell (workspace-template/shell/*) belongs to the workspace. An upgrade never rewrites it, so old copies live on (docs/compat.md intro, rules 2 and 4).

**Request pipeline in legacy mode (the default)**
1. **`authedStatic`** (server.go:246-273)
   - `auth.TileContext` drops the cookie on opaque-origin requests (auth.go:654-698).
   - Then `FromRequest` runs (auth.go:543-648). Order: owner bearer, instance bearer, terminal bearer, app bearer, then the cookie, then the frame-token header or `?frame=`.
   - A request with no credential gets through only via the legacy credential-less rule `tileSubresourceAuthed` (static.go:278-311, auth.go:347-419), and then with an empty principal. The rule needs all of:
     - GET or HEAD, and not `.html`;
     - `Sec-Fetch-Site` of cross-site or same-site;
     - `Sec-Fetch-Dest` of script, style, image, font, audio, video, track, worker or manifest;
     - a source IP that authenticated within the last hour.
   - Strict modes answer 401 instead (server.go:259-261).
2. **`handleComponentStatic`** (static.go:70-180)
   - `util.SafeJoin` rejects any `..` (util.go:78-93).
   - `pathAllowed` rejects a reserved first segment (.xbin, vendor, data, home, homes, xbin, ingress, runtime, owner; util.go:67-70) and any `.git` or `.xbin` segment (static.go:346-361).
   - `owner := owningComponent(cleaned)` is the longest registered prefix via `Reg.Resolve` (registry.go:517-536). When nothing is registered it falls back to the first segment (static.go:327-338).
   - A non-chrome owner requires `CanReadTile || codeGranted || tileSubresourceAuthed`. In strict modes it also requires `strictLiveRead`, which re-checks the driving user's live access (tileassets.go:61-71; static.go:105-115).
   - A signed-in human who fails this gets the D36 request-access page.
3. **`?native=1`** goes to `serveNativeRoute` (static.go:116-121).
4. **Opening the file**
   - Strict modes: `serveStrictStatic` (tileassets.go:166-217).
   - Legacy: `openLegacy` (static.go:182-219) tries the dev overlay first (never for xbin.json or scope.json), then `fsutil.OpenResolved(Reg.Root, cleaned, pathAllowed)`.
5. **Response**
   - A directory gets a 301 to the trailing slash, then `index.html` is served.
   - HTML gets the injection, except under `inject:false`: then the bytes are served unchanged, still with the CSP sandbox header (static.go:167-175).
   - Other files get `inertNonDocument`: `CSP: sandbox` on the workspace origin, except PDFs (static.go:231-247).
   - Every response has `no-store` and `nosniff`.

**Only the bytes come from the file. Everything else comes from the live registry:**
- `Manifest.Inject` (static.go:158);
- chrome-ness via `sandboxedFrame` (static.go:313-325);
- `c.Native` (native.go:34-42);
- whether the component exists at all. `Rescan` walks the live tree and registers a directory only if it holds xbin.json or index.html. It skips dot dirs at every level, IgnoredDirs and reserved names (registry.go:423-476, util.go:72-76).

#### A.2 The single sanctioned HTML transform (D4)
`headInjection` (static.go:423-466) emits, in order:
- `assetHead`, strict modes only; it is empty in legacy;
- the merged import map (workspace plus scope, registry.go:565-582);
- `<meta name="xbin-component" content="<compPath>">`;
- `<meta name="xbin-frame-token" content="<token or empty>">`;
- `xbin-interfaces` (the resolved http slots, netfn.go:385-410);
- `xbin-sandbox` (non-chrome only);
- `xbin-ws-origin` (only for `X-XBin-Client: app/…`, native.go:130-148);
- `<script type="module" src="/vendor/xbin-client.js">`.

How it is applied:
- `injectHTML` places the block right after `<head…>` (static.go:363-395).
- `documentHeaders` sets CSP sandbox plus any grant extras. Chrome instead gets COOP, and in origins mode `frame-ancestors 'self'` (static.go:397-421).
- compPath is the registered component. For a directory index with nothing registered it is that directory; for a bare .html file it is the file's directory (static.go:367-379).
- `inject:false` means the frontend "has no identity at all" (static.go:167-170). The native runtime document is injected regardless (native.go:90).
- Legacy output is pinned byte for byte by tests: tileassets_test.go:184-187 `TestLegacyInjectionUnchanged`, and native_test.go:433.

**The identity string is load-bearing in tile code**
- `xbin.self = meta('xbin-component')` (web/xbin-client.js:50).
- Tiles address themselves with it: ``xbin.fetch(`/api/${xbin.self}/events`)`` and ``xbin.bus.on(`res:${xbin.self}/bus/events/`)`` (examples/calendar/index.html:35,55; docs/sdk.md:207-234).
- The Go SDK builds vault URLs from `Self()`, which is `XBIN_COMPONENT` (sdk/xbin.go:82, 264-320).

#### A.3 Frame tokens: what they bind and who gets one
**Format and lifetime**
- Wire format: `base64url(component)|base64url(user)|exp|gen|hmac`, HMAC-SHA256 under `.xbin/secret` (internal/auth/frametoken.go:39-43, 226-238).
- TTL is 15 minutes (static.go:24).
- xbin-client renews every 10 minutes at `GET /api/xbin/frame-token?component=<self>` (xbin-client.js:77-93; api.go:232-252).

**What it binds**
- **component**: the tile, or an `xbin.window` sub-path whose owning component is the tile.
- **user**: a user id; "" means the owner or bootstrap token.
- **gen** (D93, the 5th field) takes one of three forms (frametoken.go:22-43, 79-189):
  - `s.<handle>`: a login session. Each use slides that session's idle window.
  - `u.<epoch>.<n>`: the user's generation; sign-out-everywhere bumps n.
  - `o.<keyed hash of the owner token>`.
- Handles persist in `.xbin/frame-gens.json`. Old 4-field tokens verify only if they expire within an hour of boot (frametoken.go:73-77, 274-276).

**Verification**
- `framePrincipal` produces `Principal{Component, UserID, Via:"frame", Gen, Impersonator}` plus the user's `Access`. A deleted or disabled user, or a dead generation, kills the token (frametoken.go:296-320).
- Renewal via `MintFrameTokenFor` copies the caller's Gen (frametoken.go:213-224).
- Frame principals are admin of their own component. Anywhere else they are clamped to the driving user's access (auth.go:117-160).

**Minting rules (D95), via `mayMintFrameToken`** (static.go:468-503)
- Never for a `backendPrincipal`, i.e. a Component whose Via is not frame or terminal (static.go:549-557).
- The principal must pass `CanReadTile(compPath)`.
- The principal must be one of:
  - a human (`p.Component == ""`);
  - the tile itself (`p.Component == compPath`, or its owning component is compPath);
  - a navigation within the same tile tree where `writersCover` holds (static.go:505-575).
- Any other tile gets `content=""`.

**Other mint paths**
- The API rule: `!backendPrincipal(p) && (p.Component == comp || (p.Component == "" && p.CanReadTile(comp)))` (api.go:244).
- bx-frame's credentialless bootstrap mints from the shell's cookie context (web/frame-info.js:72-100).

**Contract**: the token is opaque, but docs/protocol.md:24-37 describes the change from 4 to 5 fields.

#### A.4 Strict modes: asset tokens and per-tile origins (D95)
**tokens mode**
- `/c/~<tok>/…` is routed only in tokens mode (tileassets.go:251-266).
- Token format: `a1.b64(tile).b64(user).exp.b64(gen).mac`, a purpose-tagged 16-byte HMAC. TTL is 7 days, and the token is bound to the document's generation (auth/assettoken.go:13-176).
- `serveAssetToken` (tileassets.go:268-324):
  - serves only non-document files, never chrome, directories or document destinations;
  - checks `UserCanReadTile` live for both the token's tile and the loaded owner;
  - opens files via `openStrict`;
  - adds `CSP: sandbox` and `no-referrer`.
- `assetHead` (tileassets.go:326-390):
  - adds `<base href="/c/~<tok>/<doc-dir>/" data-xbin-assets>`. The directory comes from `r.URL.Path` (`tokenBase`), or from the document's own `<base>` when that points into `/c/`;
  - rewrites every `/c/` import-map value under the token;
  - adds a remap of `/c/<tile>/`.

**origins mode**
- The label is `t-` plus lowercase base32 of `HMAC(secret, "xbin-tile-host-v1"‖0‖tile)[:10]`, 18 characters in all (assettoken.go:178-188). `tileHostOf` enforces that format (tileorigin.go:99-122).
- Scheme and port come from `--external-url` (tileorigin.go:124-140). The origin is reported as `/components` `origin` (api.go:155-159, 184).
- The login exchange (tilenav.go:113-178; tilebinding.go:226-275; tileorigin.go:366-461):
  1. The workspace navigation redirects to the tile origin with `?xbin_begin=<hint>`.
  2. The tile origin sets `__Host-xbin_tstate` and redirects back with `?xbin_state`.
  3. The workspace mints a one-time ticket bound to the session reference and the state hash.
  4. The tile origin redeems the ticket for `__Host-xbin_tile` (HttpOnly, Secure, SameSite=Strict, host-only).
- After that, `tileOriginPrincipal` checks each request (tileorigin.go:273-364; assettoken.go:209-225):
  - a frame token must belong to this origin's tile;
  - a cookie-only request must come from the origin itself (`cookieRequestAllowed`);
  - the result, `TilePrincipal`, is the frame principal for (tile, user, login gen).
- A tile origin serves only `/c/`, `/api/`, `/ws/events`, `/vendor/` and `/healthz` (tileorigin.go:142-191), and only its own tile's documents (tileassets.go:219-249).
- Set-Cookie is dropped on its `/api` (tileorigin.go:234-236; proxy.go:219-227).
- Its documents get `allow-same-origin` (tileassets.go:73-84), so each tile has real localStorage and IndexedDB of its own (docs/elements.md §Asset URLs).

#### A.5 Containment, symlinks, nesting
- **Legacy** (`OpenResolved`): symlinks are followed anywhere, so cross-tile symlinks work. The resolved path must stay inside the workspace and pass `pathAllowed`, and that exact path is then opened with RESOLVE_NO_SYMLINKS. FIFOs and devices are refused (static.go:182-219; internal/fsutil/beneath.go:47-57).
- **Strict** (`OpenIn(root, owner, rel)`): the owner directory must be reached from the workspace root without following any symlink; `rel` is opened with RESOLVE_BENEATH (tileassets.go:124-164; beneath.go:36-45).
- **Nesting**
  - Any subdirectory with xbin.json or index.html is a component of its own, and `Resolve` takes the longest prefix.
  - Token issuance between nested pages uses the same-tree navigation rule above.
  - In origins mode each nested component gets its own origin, and navigating within a tree is recognized by Referer (`navWithinTree`, tilenav.go:197-222).
  - Frame reloads also target the longest mounted prefix (web/events-socket.js:33-47).

#### A.6 Native runtime document, and how tiles are addressed
**`GET /c/<tile>/?native=1[&preview=1]`**
- It is authorized exactly like index.html (static.go:116-121).
- The slashless URL gets a 301 that keeps the query. Any other directory gets a 404. While the workspace switch is off it returns 410, except with `preview=1`. Chrome has no native document (native.go:34-97).
- The body is generated: the same `headInjection(r, comp, comp.Path, nil)`, an `xbin-native` meta, and `import { boot } from '/vendor/xb-native.js'; await boot("./<entry>")` (native.go:99-128). The entry resolves relative to the document URL.

**How each client addresses a tile**

| Who | URL shape | Ref |
|---|---|---|
| bx-frame | `src` is the component path; the iframe loads `/c/<src>/`. Credentialless: adds `?frame=<bootstrap>`. Origins: the workspace URL, which redirects to the tile origin | bx-frame.js:12,524; frame-info.js:78-100 |
| frame facts | `/api/xbin/components`, longest prefix (chrome, sandbox, origin) | frame-info.js:35-70 |
| reload | re-navigates on a matching `reload` event; the longest mounted src wins | bx-frame.js:366-417; events-socket.js:33-47 |
| shell screens | `<bx-frame src=${o.path}>` (workspace-owned) | bx-canvas.js:276 |
| xbin.window | the shell builds `src = from + '/' + spec.path`, where `from` is bx-frame's verified `this.src` | bx-shell.js:677; bx-frame.js:499-514 |
| native app | `/c/<tile>/` and `/c/<tile>/?native=1`. The scheme handler maps `/c/…` to a known tile by longest prefix and forwards that tile's frame token | native/ios/…/TileLoading.swift:74-93; compat.md native contract |
| tokens mode | `/c/~<tok>/<tile>/<dir>/…` | tileassets.go:251-371 |
| origins mode | `https://t-<id>.<tiles-domain>/c/<tile>/…`, plus the `xbin_begin`/`state`/`ticket`/`retry` parameters | tileorigin.go:67-81 |

#### A.7 Serving a frozen snapshot at the default URL (pause)
Inference: what this needs, derived from A.1-A.6.
1. **A snapshot store outside every tree that is scanned, watched, served or writable by a tile.** `.xbin/…` or `data/…` both qualify:
   - Rescan skips them (registry.go:440-445), and so does the watcher (internal/watch: 55-69);
   - `pathAllowed` refuses them;
   - terminal sandboxes mask them (sandbox.go:90-107);
   - they are outside the compat promise (docs/compat.md, "What this does not promise").
   It must never be bound into a tile's sandbox.
2. **One resolver from owner to root, used by every opener.** The openers are `openLegacy` (static.go:193), `openStrict` (tileassets.go:133, also used by `serveAssetToken` at :308), the tile-origin plane (it calls `handleComponentStatic`, tileorigin.go:231-233) and the native route (its entry import is an ordinary `/c/` load).
   - The dev overlay is a precedent for serving another directory at the same URL (static.go:206-217; tileassets.go:147-158).
   - A frozen root must open with OpenBeneath semantics. Relative symlink targets resolve against the snapshot's own location, so legacy cross-tile symlinks cannot work inside one.
3. **Suppress `reload` events and restarts for a paused tile** (boot/serve.go:164-186 publishes `reload` and calls `run.Changed`). Otherwise bx-frame re-navigates (bx-frame.js:370-373) and throws away in-page state for identical bytes. Old shells also run a full `_load()` on every `reload` (bx-shell.js:195).
4. **Decide what happens to manifest-derived facts.** These are all live: inject, native entry, runtime, entry, alwaysOn, vm, interfaces, uses, and whether the component exists (registry.go:456-476). If the working tree has neither xbin.json nor index.html, the component disappears and `/api/<tile>` returns 404 (proxy.go:128-134), snapshot or not. The chrome flag must stay live.
5. **Nested components** keep resolving to their own root. A parent's snapshot must not be used for them.
6. **No auth changes.** Same URL, token, origin and identity as today.

#### A.8 A second deployment's frontend
Legacy authorizes credential-less subresources by URL path alone (static.go:278-311), and relative URLs drop the query string. So the deployment has to be in the path or in the origin.

**Candidate URL shapes**

| Shape | Assessment |
|---|---|
| `/c/<tile>@<dep>/` | '@' is legal in tile names: `ComponentPathOK` allows it (util.go:95-113) and non-admin creation bans only ':' (broker/policy.go:156-166). It is also the allowance separator. And `Resolve` would pick the wrong prefix. |
| `/c/<tile>/@<dep>/` or `/c/<tile>/.<m>/<dep>/` | Collides with a tile's own internal directories and with `xbin.window` sub-paths. |
| `/c/.<m>/<dep>/<tile>/` | No component path can start with '.' (registry.go:440; util.go:109), so this is collision-free for components. Relative URLs stay under it, and in tokens mode `tokenBase` copies `r.URL.Path` into the `<base>`. Today such URLs return 403 or 404. The only overlap: a hidden directory at the workspace root would be admin-readable through the legacy plane. |
| `/c/~…` | Taken by tokens mode (tileassets.go:251-266). |
| a new route `/d/<dep>/<tile>/` | Additive (compat rule 2), but tile origins would need to serve it too (tileorigin.go:147-191), plus apicheck, openapi.go and protocol.md updates. |
| `?deploy=<dep>` | Applies to the document only; subresources lose it. |
| origins: own label, e.g. TileHostID over tile‖NUL‖dep | A directory name can't contain NUL, and the 18-character format is kept. Paths can stay `/c/<tile>/…`, so absolute self-URLs work. Needs a reverse lookup from label to (tile, dep) everywhere code compares `TileHostID(owner) == id` (tileorigin.go:163,296,309,375,411; tilenav.go:216-220). |

**Frame identity**
- Frame tokens bind only (component, user, exp, gen). A deployment needs a new field: `framePrincipal` would set a new `Principal.Deployment`, and renewal (api.go:250) must copy it.
- `mayMintFrameToken` currently accepts any deployment of the same tile (`p.Component == compPath`). Without a new check, navigating main to dev (or dev to main) would mint the other deployment's token. It also has no notion of which user level may open dev.
- **tokens mode**: carry the deployment in the path under the token, or in `AssetGrant`.
- **origins mode**: the label, ticket, cookie and `TilePrincipal` all need the deployment, and `/components` should report an origin per deployment (additive). A separate origin is required: on a shared origin, dev would share prod's localStorage and IndexedDB.

**Making dev's /api calls reach dev's backend**
- Tile code calls `/api/${xbin.self}/…`. Keeping `xbin-component` equal to the tile path keeps that code working.
- The proxy then has to route by frame identity: a self-call (`p.Component == target.Path`) with `p.Deployment` set goes to that deployment. Today it always does `Runner.Ensure(comp)` and `Track(comp.Path)` (proxy.go:160-196).
- In origins mode the dev origin's cookie yields `TilePrincipal{tile, dep}`, and plain `fetch('/api/<tile>/…')` works.

**Import map and absolute URLs**
- Remapping `/c/<tile>/` to the deployment prefix in the import map follows the tokens-mode precedent (tileassets.go:357-365).
- In legacy and tokens mode, absolute `/c/<self>/…` URLs in markup would still load the primary's files. In origins mode they would load the deployment's files.

**Events and the shell**
- Dev events must not be published under the bare component name:
  - bx-frame matches on `e.component === this.src` (bx-frame.js:366-395);
  - the workspace-owned shell keys its status dots and toasts on `e.component` (bx-shell.js:1257-1266).
- An old shell rebuilds `xbin.window` frames from `from + '/' + path` (bx-shell.js:677), which drops a deployment carried anywhere other than `src`.
- The native app and old shells never generate dev URLs.

#### A.9 Building and running a backend from a snapshot at its canonical path
**Isolated runtime: yes.**
- `sandboxCmd` binds `{Src: c.Dir, Dst: c.Dir, RO: true}`, plus the run dir and the gateway socket (runner.go:622-633).
- `mountBind` creates Dst in the overlay and does a recursive bind of Src onto it (init_linux.go:391-423).
- Cwd is c.Dir (runner.go:671). node and python run `c.Dir/entry`; Go runs the binary bound at `/run/backend` (runner.go:388-401, 638-649).
- VM backends export the paths as the shim sees them (vm/manager.go:258-293). Inference: a remapped source works there unchanged.
- Everything below is keyed per component today and would need per-deployment keys:
  - build output `.xbin/build/<CompKey>/bin` (runner.go:366);
  - sockets (runner.go:408-412);
  - log (runner.go:464-466);
  - cgroup leaf (runner.go:480-484).

**Isolated Go build: yes, with one change to confine.**
- `buildConfined` binds GOROOT and the workspace read-only (with .xbin, data and homes masked), so `go.work` resolves unchanged. It runs `go build -buildvcs=false` with `Dir: c.Dir, ReadOnlyDir: true` (build.go:22-97).
- `confine.Cmd.binds()` always adds `{Src: Dir, Dst: Dir}` (confine.go:222-228). There is no field for a different source directory.
- Inference: adding `Bind{Src: snapshot, Dst: c.Dir, RO: true}` would shadow that bind, because the depth sort in `sortBinds` is stable and the later bind at the same depth wins (sandbox.go:115-127). `use ./<tile>` in go.work would then resolve to the snapshot.

**Non-isolated: no canonical-path view.**
- Backends exec as xbind with `cmd.Dir = c.Dir` (runner.go:446-461), and confine runs tools directly (confine.go:140-143). Running from a snapshot means Dir and entry point into the snapshot, at a different path.
- Inference: a snapshot under the workspace finds `<root>/go.work`, which `use`s only the canonical tile directories (deps.go:120-163). Go then refuses to build a module that isn't listed, unless GOWORK is overridden.
- Relative `replace` directives also break outside the canonical location, e.g. `replace …/sdk => ../../sdk` in examples/counter-go/go.mod.
- Per D78 there is no boundary in this mode anyway.
- Stream ports are dialled at `127.0.0.1:<port>` (runner/ingress.go:49-56), so two deployments that listen on the same fixed port collide.

---

### B. Outbound calls, principals, and the binding fabric

#### B.1 A backend calling another tile
1. **SDK side.** `Client()` dials `$XBIN_GATEWAY`, which is `RunDir/gateway.sock` bound into the sandbox at the same path (vsock port 1025 for VMs). It sends `Authorization: Bearer $XBIN_TOKEN` (sdk/xbin.go:205-252; runner.go:417-422, 628-633).
2. **Same handler.** The gateway socket serves the same handler as the console (boot/serve.go:34-54).
3. **Identity.** `FromRequest` maps the instance bearer to `Principal{Component, Via:"instance"}` (auth.go:584-591). The token-to-component map is filled once per generation (auth.go:219, 445-462; runner.go:415, 479, 608).
4. **Dispatch.** `handleAPI` sends `/api/xbin/*` to the core mux; governance writes are audited as `who=p.From()` (server.go:561-584). Everything else goes to the proxy.
5. **Proxy.** `Proxy.ServeHTTP` (proxy.go:128-235) runs:
   - `Resolve` (longest prefix);
   - template, no-backend and lifecycle (409) gates;
   - `broker.Policy`: admin gets admin; `p.Component == target.Path` gets admin; cron and bus principals get their bound role; otherwise `grantedRole` (broker.go:477-490);
   - `identify`;
   - `Runner.Ensure(comp)`, then a reverse proxy to `g<N>.sock`.

#### B.2 A frontend's call
- `xbin.fetch` sets `X-XBin-Frame-Token`. `xbin.ws`, `xbin.url` and `/ws/events` use `?frame=` instead (xbin-client.js:95-159).
- Server side:
  - `Origin: null` is handled by `nullOriginCORS` (server.go:176-201);
  - `authed` drops the cookie from tile-context requests (server.go:213-217);
  - a frame token alone is enough (auth.go:604-609);
  - with both a cookie and a frame token, the users must match (auth.go:634-646).
- In origins mode the tile cookie gives `TilePrincipal`.
- The proxy strips `?frame=` before forwarding (proxy.go:199-203).

#### B.3 What the callee sees
**`identify`** (proxy.go:263-305):
- strips every incoming `X-Xbin-*` header, xbind's cookies, and any Bearer;
- sets `X-XBin-From` from `p.From()`: a component path, `owner`, `user:<id>`, `xbin/cron` or `xbin/bus` (auth.go:196-207);
- sets `X-XBin-Role`;
- when a human is attributed, also sets `X-XBin-User`, `X-XBin-User-Level` (the user's level on the callee, boot.go:484-490) and `X-XBin-Viewed-By`.

**Other callers**
- Ingress traffic gets `From: ingress` plus `X-XBin-Ingress-Host` (proxy/ingress.go:40-54).
- CGI gets `XBIN_COMPONENT`, `XBIN_FROM` and `XBIN_ROLE` (proxy.go:249-259).
- The SDK exposes this as `CallerInfo{From, Role, Owner, User, UserLevel, ViewedBy}` (sdk/xbin.go:85-124).

**Gap:** nothing tells two deployments of the same caller apart.

#### B.4 How bindings resolve
**http slots**
- Stored as `bindings[comp][slot]` with refs of the form `<provider>[#<instance>]` in the root xbin.json (registry.go:213-228).
- `HTTPSlots` resolves them to `/api/<prov>`, or `/api/<prov><prefix>` for an instance (netfn.go:333-383).
- The provider must have an http provide matching the slot's service. A provide with `instances:true` must be bound as `provider#instance`. Refs that have vanished are skipped.
- Delivered at spawn as env `XBIN_IFACE_<SLOT>_URL` (plus `_INSTANCE`, or a JSON list for multi slots) (resources.go:99-123). Also delivered in the document meta for `xbin.iface` (xbin-client.js:53-58).
- The binding itself is the call grant: `httpBindingRole` (netfn.go:283-310; broker.go:434-437).
- The instance is not checked at call time (netfn.go:293-295). Instances are addresses, not an isolation boundary.

**Interface instances (D32)**
- Only providers whose provide declares `{kind:"http", instances:true}` can register them. Each maps an id to a provider-relative path prefix.
- Registration is `PUT /api/xbin/iface-instances`, by the provider's own principal or an admin (registry.go:112-116, 218-223; netfn.go:1031-1131).
- A PUT replaces the provider's whole map in the root xbin.json. It then emits a `grants` event and restarts every requester bound to that provider (via `OnGrantChange`).
- Allowances can pin an approval to a provider and instance: `iface:<svc>@<tile-glob>[#<inst-glob>]` (users/orgs.go:629-642).

**Per-slot gateway forwards**
- `IngressFwdFor` maps virtual gateway ports to targets (ingressfn.go:277-327):
  - a terminator's forward door, `unix:<socket>`;
  - one `stream:<prov>:<port>` per bound stream interface.
- These are installed in the relay (`HostFwd`, runner.go:532-538) and advertised through `XBIN_IFACE_<SLOT>_ADDR` and `XBIN_INGRESS_FORWARD_URL` (resources.go:124-141).
- Dialing ends in `DialInto(prov)`, the provider's current instance (runner/ingress.go:28-47, 104-134).

**The net slot**
- `netBinding(comp)` resolves one of: internet (optionally filtered), host, `lan:<cidr>`, `set:`, org, personal, none, or a provider tile. Ceilings and org net sets apply (netfn.go:36-110).
- The result is then turned into one of:
  - `EgressFor`, a relay policy (egress.go:57-100);
  - `NetHostShare`, the host network;
  - `NetClientTarget`, a /30 splice to a provider. The splice address comes from the client's index in the sorted client list (netfn.go:170-253).
- Provider links are created when the provider spawns, one TUN per client, registered in netmux under (provider, client) (runner.go:493-571). A provider restart makes its clients re-splice (runner.go:572-588).
- All of this is captured at spawn.

**The egress relay** is started as `relay.Start{TunFD, Allow, Resolver, AllowHost (D35), Published/HairpinDial, Gateway/HostFwd}` (runner.go:516-544).

#### B.5 Where the component path is the principal key

| Area | How the path is used | Refs |
|---|---|---|
| Identity | `Principal.Component`; `From()`; instance token maps to component; terminal token maps to (component, user); frame token's component; the `Tile` of asset tokens, tile tickets and tile cookies; `TileHostID`; the self-pass in `tileLevel` | auth.go:63,133-135,196-207,219-220,242-245,445-491; frametoken.go:226-232; assettoken.go:59-72,182-188; tilebinding.go:233-281 |
| Grants | rows `{from,target,role}`; `grantedRole`: explicit rows, then the http-binding role, then same-scope `uses` | registry.go:193-203; broker.go:412-452 |
| Self | `p.Component == target.Path` means admin | broker.go:482-484; proxy.go:52-60 |
| Capabilities | `grantedRole(p.Component, "xbin"/"xbin:users")` for IsAdmin, user management and tile creation; `gpu:`; `cap:net-admin`; `cap:containers`; `cap:open-links` | broker.go:462-475; usersapi.go:38-50; broker/policy.go:105-130; gpu.go:14-100 |
| Bindings | `bindings[comp][slot]`; net, splices, stream forwards, exposes, lan-ingress | netfn.go:36-383; ingressfn.go:72-360 |
| Ceilings | workspace rows matched against the path, plus owner-derived rows (personal policy for a user owner; org sets, policy and net sets for an org owner) keyed on `owners[path]` | broker/policy.go:22-87; users/orgs.go:1211-1263 |
| Ownership | `Users.Owner` / `OwnerOrg`; the `/components` owner field; D26 and D33 approvals | users/orgs.go:169-182; broker.go:308-313 |
| Code and PRs | self code reads; `codeGrantAllows`; `canDecidePR` self; `From.Component` | code.go:146-164; prs.go:95-130,287,368 |
| Resources | Keyed by scope, not component: `res:<scope>/<name>`; kv bucket is the resource id; files live under `data/resources[-enc]/<ScopeKey>/<name>`. Authorization uses `grantedRole(p.Component, res)` | broker.go:321-366,492-509; resources.go:24-202 |
| Bus reader checks | the WS filter and every push delivery call `allowRes(p, res, "reader")` | resources.go:439-458; bussubs.go:337-347 |
| Stores | vault `data/vault/<CompKey>.json`; prefs `data/prefs/<user>/<CompKey>.json`; cron and bus subscriptions keyed `(component,name)`; in-memory statuses; `ifaceInstances[comp]` and `ingressHosts[comp]` in the root xbin.json; logs `.xbin/log/<CompKey>.log` | vault.go:45-47; prefs.go:33-49; cron.go:72-74,108; bussubs.go:158-163; obs/status.go:17-25; registry.go:218-228; logs.go:74 |
| Budgets | push: per tile, per tile+person, mute (person, tile), link `c/<tile>/`. Disk quota is per scope | push/api.go:348-368; diskmon.go:237-247 |
| Audit | `who=p.From()`; grant approvals record `by` | server.go:568-580; broker.go:700-714 |
| whoami | `kind: element, id: p.Component` | whoami.go:64-69 |
| Runtime | runner states, sockets, builds, caches, cgroups, netmux, stats | runner.go:155,173-182,366,408-412,480-484; build.go:43-45 |
| Events | `Event.Component` | events.go:11-17 |

Scale, from grep over non-test daemon code: 103 references to `p.Component`, 16 `grantedRole` call sites, 22 ceiling calls, 45 owner lookups, 61 `Reg.Component` calls.

#### B.6 The three options compared

**(i) One principal per tile, with a deployment attribute**
- **Policy:** no policy lookup changes. Dev inherits grants, bindings, capabilities, ceilings and ownership automatically, which is what the requirement asks for.
- **Tile code:** `xbin.self`, `XBIN_COMPONENT`, `res:${xbin.self}` and `Self()` all keep working unchanged.
- **What has to be built:**
  - `Principal.Deployment`, where the zero value means primary;
  - instance map values of (component, deployment);
  - deployment in frame tokens, asset tokens and tile tickets/cookies;
  - proxy routing of self-calls;
  - a routing hint in the request context for cron and bus deliveries, since those run as `xbin/cron` or `xbin/bus` (cron.go:185-186; bussubs.go:348-349);
  - an `X-XBin-*` deployment header for non-primary callers only (trustworthy, since incoming ones are already stripped);
  - deployment in audit lines and whoami;
  - deployment-aware handling in every self-scoped API (B.7).
- **Failure mode:** any handler that isn't converted writes prod state. That fails open for isolation, not for privilege. Inference: counter this by refusing non-primary principals on all `/api/xbin/*` writes except an explicit allow-list, enforced by a test in the style of TestNoDirectExec.
- **Effort:** moderate. The risk sits in covering all ~15 self-scoped handlers.

**(ii) Deployment-qualified principals, e.g. `apps/x@dev`**
- **Upside:** every string-keyed store separates automatically.
- **Policy problem:** "grants stay per tile" means every policy lookup must canonicalize the name. Omissions fail differently by type:
  - grants, bindings and same-scope lookups fail closed (`Reg.Component` misses);
  - ceilings fail open: owner-derived org and personal rows key on `owners[path]`, which has no entry for the qualified name (users/orgs.go:1234-1263), so dev would escape org policy.
- **Visible changes:**
  - `X-XBin-From` stops being a path;
  - `/api/${xbin.self}` breaks because `Resolve` looks for a longest prefix (proxy.go:130);
  - `res:${xbin.self}` breaks because `parseRes` expects a declared scope (broker.go:337-366);
  - SDK vault URLs break because `vaultAccess` uses `Resolve` (vault.go:151-159).
- **No free separator:**
  - '@' is legal in tile names, and it is the allowance separator, cut at the first '@' (users/orgs.go:598-642);
  - '#' separates binding instances (netfn.go:315-320);
  - ':' separates grant targets.
- **Effort:** high. The risk is to privilege.

**(iii) A parallel fabric**
- **Shape:** a routing rule on top of (i). A non-primary caller's cross-tile call goes to the provider's deployment of the same name. URLs don't change; the proxy decides.
- **Fallback when the provider has no such deployment** (a choice to make):
  - use the primary, which leaks dev effects into prod;
  - refuse, which is safe;
  - let each binding choose.
- **Beyond HTTP:**
  - stream forwards need the deployment in `DialInto`;
  - net splices need one link per pair of deployments. Splice addresses shift with the sorted client index, so the provider and all its clients restart;
  - lan-ingress is affected the same way;
  - cross-scope resources and buses need namespaces per (scope, deployment);
  - ingress always goes to the primary.
- **Effort:** high on the network plane, moderate for HTTP.

#### B.7 Self-scoped APIs, and what leaks if dev runs as the same principal

| API (callers) | Key | Refs | Effect of dev on prod | Severity |
|---|---|---|---|---|
| `/api/<self>/…` (frontend, backend, terminal) | self rule; `Ensure(comp)` | broker.go:477-490; proxy.go:160-196 | dev's UI and backend drive the prod backend | HIGH |
| kv, blob (both) | resource id | resources.go:171-406 | reads and writes prod data | HIGH |
| sqlite, filesystem (backend) | `XBIN_RES_*` path plus a rw bind | resources.go:81-95; runner/binds.go | the same files | HIGH |
| vault (backend reads; terminals and admins manage; frontend refused) | `CompKey(comp)` | vault.go:45-47,151-189 | dev reads and overwrites prod secrets, which means external side effects | HIGH |
| bus publish (both) | resource | resources.go:408-437; bussubs.go:271-335 | reaches prod's WS subscribers and push subscriptions | HIGH |
| bus subscriptions PUT/DELETE | (comp,name) | bussubs.go:158,391-449 | overwrites or deletes prod's subscriptions; deliveries go to the primary | HIGH |
| cron PUT/DELETE | (comp,name) | cron.go:108,166-196,227-268 | overwrites or deletes prod's jobs; ticks go to the primary | HIGH |
| notify (backend: any reader; frontend: its own user) | tile, budget, mute, link | push/api.go:287-395 | real pushes to users as the prod tile, charged to its budget | HIGH |
| iface-instances PUT (provider) | root xbin.json | netfn.go:1037-1131 | replaces prod's instance map and restarts every requester | HIGH |
| ingress-hosts PUT (tile) | root xbin.json | ingressfn.go:491-582 | rewrites prod's public hostnames | HIGH |
| cross-tile calls through grants and bindings | the target's primary | proxy.go:128-235 | writes prod records in other tiles (e.g. a Slack adapter posting to the agent inbox, D86) | HIGH (a fabric decision) |
| xbin:* governance (admin and manager tiles) | `grantedRole(comp,"xbin…")` | broker.go:462-475; usersapi.go:38-50 | changes real users, grants and tiles | HIGH |
| tile-report status and toast | `statuses[comp]`, status event | obs/status.go:61-127 | prod's status dot and toasts, in old shells too | MEDIUM |
| build/reload events (published by the runner) | Component | runner.go:285-352; serve.go:178; obs/status.go:129-140 | dev build errors on prod frames; clears prod's status | MEDIUM |
| code PR open/state | target, `From.Component` | prs.go:95-130,287,368 | files or decides real PRs | MEDIUM |
| prefs | (user,comp) | prefs.go:33-49 | overwrites prod's per-user state | MEDIUM-LOW |
| `/ws/events` bus filter (frontend) | grants | resources.go:439-458 | dev frontend sees prod bus events | MEDIUM (isolation) |
| frame-token renewal | `p.Component==comp` | api.go:241-252 | if not converted, a dev frame turns into prod | HIGH |
| logs, tile-status, cron/bus list | component | logs.go:42-80; boot/api.go:87-118; cron.go:212; bussubs.go:371 | a shared log file; reports prod's backend; lists prod's registrations | LOW |
| whoami, code/git reads | component | whoami.go:64-69; code.go:146-164 | none | NONE |

**Outside the brief:** CGI tiles run tile code via `net/http/cgi` on the host as xbind, even under `--isolate` (proxy.go:175-177, 237-261; runner.go:614-620).

## Surfaces this feature touches

### /c/ static serving (resolution and file opening)

- **Paths:** `internal/server/static.go:70-180`, `internal/server/static.go:182-219`, `internal/server/static.go:327-361`, `internal/server/tileassets.go:124-217`, `internal/fsutil/beneath.go:34-57`, `internal/registry/registry.go:517-536`
- **Today:** URL path is cleaned (SafeJoin, pathAllowed). owner = longest registered prefix. RBAC. Bytes come from Reg.Root/<path> with the dev overlay tried first. Legacy uses OpenResolved (cross-tile symlinks allowed inside the workspace); strict uses OpenIn beneath the owner. Keyed by URL path plus the live registry.
- **Change needed:** Add a resolver from (tile, deployment) to a source root: the live dir, or an immutable snapshot under .xbin/ or data/. Consult it from every opener. Parse a deployment marker before owningComponent. A paused tile's primary resolves to its snapshot. Open frozen roots with OpenBeneath semantics.
- **Compat:** Tiles that don't opt in take the identical code path to the live dir. Legacy cross-tile symlinks can't work inside snapshots (opt-in only; document it). The marker must be collision-free; a dot-prefixed first segment is never a component.

### D4 injection (the single sanctioned transform)

- **Paths:** `internal/server/static.go:363-466`, `internal/server/tileassets.go:326-390`, `internal/server/native.go:90`, `internal/server/native.go:99-128`, `internal/server/tileassets_test.go:184-187`, `internal/server/native_test.go:433`
- **Today:** Emits the import map, xbin-component, xbin-frame-token (gated by mayMintFrameToken), xbin-interfaces, xbin-sandbox, xbin-ws-origin (app only) and xbin-client.js. Tokens mode adds a base tag and remaps /c/<tile>/ in the import map; origins mode adds metas. inject:false skips everything except the native document.
- **Change needed:** Mint the token bound to the document's deployment. Keep xbin-component as the tile path. Add a deployment meta for non-primary documents only. Remap /c/<tile>/ in the import map to the deployment prefix (tokens-mode precedent). Snapshot documents may need the snapshot's inject and native facts.
- **Compat:** Primary and unpaused documents stay byte-identical, pinned by the existing tests. New metas appear only on opted-in deployments. No new rewriting outside the existing injection block.

### Frame tokens and minting rules

- **Paths:** `internal/auth/frametoken.go:16-43`, `internal/auth/frametoken.go:205-320`, `internal/server/static.go:468-575`, `internal/server/api.go:232-252`, `web/xbin-client.js:77-100`, `web/frame-info.js:72-100`, `docs/protocol.md:24-37`
- **Today:** HMAC over component, user, exp and gen (s./u./o. generations). Renewal copies gen. Minted only for a human, the tile itself, or a same-tree navigation that passes writersCover; never for backends.
- **Change needed:** Add a deployment binding (e.g. a 6th field) that sets Principal.Deployment; renewal copies it. mayMintFrameToken binds the URL's deployment and decides cross-deployment navigation and the user level needed to open dev. /api/xbin/frame-token gains a deployment parameter.
- **Compat:** 5-field and legacy 4-field tokens keep verifying unchanged. The token is documented as opaque, but the field-count text in protocol.md needs updating.

### Tokens-mode asset plane

- **Paths:** `internal/auth/assettoken.go:13-176`, `internal/server/tileassets.go:251-371`
- **Today:** a1.<tile>.<user>.<exp>.<gen>.<mac> under /c/~<tok>/. Serves non-document files from the live tree via openStrict. The base tag's dir comes from r.URL.Path.
- **Change needed:** Carry the deployment in the path under the token (tokenBase already copies r.URL.Path) or in AssetGrant. serveAssetToken resolves the deployment's root.
- **Compat:** Existing tokens and paths are unchanged; only deployment documents mint the new form.

### Origins mode (per-tile origins, exchange, cookie)

- **Paths:** `internal/server/tileorigin.go:83-191`, `internal/server/tileorigin.go:273-461`, `internal/server/tilenav.go:113-222`, `internal/auth/tilebinding.go:180-322`, `internal/auth/assettoken.go:178-225`, `internal/server/api.go:155-159`
- **Today:** One origin per tile: t-<keyed hash of the tile>. The ticket and cookie bind (tile, user, session). TilePrincipal. frame-ancestors is self plus the workspace. /components reports the origin. Documents get allow-same-origin, i.e. real storage.
- **Change needed:** Add a per-deployment label (e.g. hash over tile, NUL, dep). Add a reverse lookup from label to (tile, dep) wherever TileHostID(owner)==id is compared. Ticket, cookie and TilePrincipal carry dep. A dev origin serves only its own deployment's documents. /components gains per-deployment origins (additive).
- **Compat:** TileHostID(tile) is unchanged for primaries, and no cookie is renamed. The separate origin keeps dev's localStorage and IndexedDB apart from prod's.

### Native runtime document

- **Paths:** `internal/server/native.go:15-128`, `docs/compat.md`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/TileLoading.swift:74-93`
- **Today:** /c/<tile>/?native=1, generated from the registry's comp.Native plus headInjection. Part of the frozen native contract.
- **Change needed:** Serve it for deployment URLs and from snapshots, resolving the native entry against the deployment's files.
- **Compat:** Shipped apps only address /c/<tile>/, so they always get the primary. Unchanged.

### bx-frame, frame-info, events-socket, terminal window (vendor, ships with xbind)

- **Paths:** `web/bx-frame.js:12`, `web/bx-frame.js:190-230`, `web/bx-frame.js:366-417`, `web/bx-frame.js:499-524`, `web/frame-info.js:35-100`, `web/events-socket.js:33-47`, `web/frame-titlebar.js:26-100`, `internal/server/static.go:582-607`
- **Today:** src is a component path; the iframe loads /c/<src>/ with a bootstrap token or via the tile origin. Events match on component. The terminal window's titlebar and tools row are built here.
- **Change needed:** Add a pause toggle and a reload-now button in the terminal window. Add deployment views (pop-out) with deployment-aware URL, token minting and origin. Filter deployment-tagged events.
- **Compat:** /vendor/* updates with the binary. bx-frame.js is at 866/900 lines (unlisted JS cap), so new logic goes in new modules.

### Workspace-owned shell (old copies persist)

- **Paths:** `workspace-template/shell/bx-shell.js:195-200`, `workspace-template/shell/bx-shell.js:677`, `workspace-template/shell/bx-shell.js:1257-1266`, `workspace-template/shell/bx-canvas.js:276`, `docs/compat.md`
- **Today:** Keys status dots and toasts on e.component. Runs a full _load on reload and grants events. Builds xbin.window spawn src from from + '/' + path. Screen tiles are bx-frame src=path.
- **Change needed:** Nothing required, provided the server never publishes dev effects under the bare component. New shells may add deployment UI.
- **Compat:** Dev status, toast and reload events must use new event types or never reuse the bare component. A deployment carried outside src is lost when an old shell spawns a window.

### Events hub

- **Paths:** `internal/events/events.go:11-17`, `internal/runner/runner.go:284-352`, `internal/runner/env.go:72`, `internal/boot/serve.go:164-206`, `internal/obs/status.go:118-140`, `internal/broker/lifecycle.go:114`, `internal/server/server.go:592-612`
- **Today:** Event{Type, Component, Text, Topic, Data}. build-*, reload and status are keyed by component. Bus events are filtered by BusAllows(p). watchStatusRestarts clears a status on build-start.
- **Change needed:** Add a deployment tag or separate event types for non-primary deployments. Suppress reload and restarts for paused tiles. The bus filter must match the deployment.
- **Compat:** An additive JSON field is fine for parsers, but old consumers key on component, so non-primary events must not reuse the bare component for status or build.

### Auth principals and credentials

- **Paths:** `internal/auth/auth.go:53-207`, `internal/auth/auth.go:209-238`, `internal/auth/auth.go:443-507`, `internal/auth/auth.go:543-648`, `internal/auth/context.go`
- **Today:** Principal{Owner, UserID, User, Access, Component, Via, DeviceID, Gen, Role, Impersonator}. Instance tokens map to a component; terminal tokens map to (component, user).
- **Change needed:** Add Principal.Deployment, where empty means primary. Instance registry values become (component, deployment). Optionally attach a deployment to terminals.
- **Compat:** The zero value is primary, so every existing principal and token behaves identically. auth.go is at 707/732 lines, so additions go in new files.

### Proxy routing and identity headers

- **Paths:** `internal/proxy/proxy.go:47-60`, `internal/proxy/proxy.go:128-235`, `internal/proxy/proxy.go:263-305`, `internal/proxy/ingress.go:31-95`, `internal/broker/cron.go:166-209`, `internal/broker/bussubs.go:125-135`
- **Today:** Resolve the target, gate on template, backend and lifecycle, apply Policy, run identify (strip X-XBin-* and credentials; set From, Role, User, User-Level, Viewed-By), then Runner.Ensure(comp): one backend per component. Cron and bus deliveries dispatch in-process to /api/<comp><path>.
- **Change needed:** Pick the deployment: the caller's own for self-calls; a context hint for cron and bus dispatch; the primary for everything else, including ingress; or the parallel fabric if chosen. Add a deployment header for non-primary callers.
- **Compat:** Primary callers see exactly today's headers. The new header appears only on dev traffic, and incoming X-XBin-* stripping keeps it trustworthy.

### Broker policy (grants, ceilings, ownership, capabilities)

- **Paths:** `internal/broker/broker.go:412-509`, `internal/broker/policy.go:14-87`, `internal/users/orgs.go:169-182`, `internal/users/orgs.go:1211-1263`, `internal/broker/gpu.go:14-100`, `internal/broker/code.go:146-164`
- **Today:** Everything is keyed by component path: explicit grants, http-binding grants, same-scope auto-grants, ceilings (workspace rows plus owner-derived org and personal rows), ownership, xbin:* and cap:* capabilities, code grants.
- **Change needed:** Option (i): nothing. Option (ii): canonicalize at every lookup. Optionally deny xbin:* governance capabilities to non-primary principals.
- **Compat:** (i) leaves policy semantics identical. (ii) fails open on owner-derived ceilings wherever canonicalization is missed.

### HTTP interface bindings and instances

- **Paths:** `internal/broker/netfn.go:255-410`, `internal/broker/netfn.go:1031-1131`, `internal/broker/resources.go:99-123`, `internal/registry/registry.go:101-117`, `internal/registry/registry.go:213-228`, `web/xbin-client.js:53-58`
- **Today:** bindings[comp][slot] holds refs provider[#inst]. URLs are resolved at env and meta build time. The binding is also the call grant; the instance is not checked at call time. The provider registers instances with a whole-map PUT into the root xbin.json, which restarts requesters.
- **Change needed:** Build dev env per deployment with the same URLs. Refuse or namespace iface-instances registration for non-primary. Route the parallel fabric at the proxy, if chosen.
- **Compat:** Primary env and meta are unchanged.

### Net slot, splices, stream forwards, egress relay

- **Paths:** `internal/broker/netfn.go:36-253`, `internal/broker/egress.go:57-100`, `internal/broker/ingressfn.go:277-360`, `internal/runner/runner.go:493-589`, `internal/runner/runner.go:676-708`, `internal/runner/netmux.go`, `internal/runner/ingress.go:28-134`
- **Today:** The net binding per component resolves to a relay policy, host network, or a splice over a provider link. Links are created at provider spawn, one TUN per client, addressed by sorted index. Stream forwards call DialInto(provider), which reaches the primary.
- **Change needed:** Dev gets the same egress policy. A splice needs a per-deployment link (provider restart) or no splice at all for dev. DialInto needs a deployment under a parallel fabric.
- **Compat:** Unchanged unless a dev client joins a provider roster; adding a roster entry shifts indices and restarts the provider and all its clients.

### Self-scoped side-effect APIs

- **Paths:** `internal/broker/cron.go:31-270`, `internal/broker/bussubs.go:56-449`, `internal/broker/resources.go:171-458`, `internal/broker/vault.go:45-200`, `internal/push/api.go:287-395`, `internal/obs/status.go:35-140`, `internal/obs/prefs.go:23-140`, `internal/obs/logs.go:39-80`, `internal/boot/api.go:87-118`, `internal/broker/ingressfn.go:491-582`, `internal/broker/netfn.go:1031-1131`, `internal/broker/prs.go:95-130`, `internal/broker/whoami.go:13-70`
- **Today:** Each is keyed by p.Component (or by resource for kv, blob and bus) and persisted in data/*.json, the root xbin.json or memory.
- **Change needed:** Make each one deployment-aware (separate keys or namespaces) or restrict it to the primary. Default-deny non-primary principals on /api/xbin writes behind an allow-list.
- **Compat:** The legacy unsuffixed keys belong to the primary (main), so there is no migration and existing tiles see no difference.

### Backend spawn and build from a snapshot

- **Paths:** `internal/runner/runner.go:357-461`, `internal/runner/runner.go:614-715`, `internal/runner/build.go:22-97`, `internal/confine/confine.go:73-84`, `internal/confine/confine.go:119-256`, `internal/sandbox/sandbox.go:90-127`, `internal/sandbox/init_linux.go:391-423`, `internal/vm/manager.go:258-293`, `internal/deps/deps.go:120-193`
- **Today:** The sandbox binds c.Dir at c.Dir, read-only. The build writes .xbin/build/<CompKey>/bin. The confined build binds the workspace RO plus Dir. Non-isolated mode runs in c.Dir as xbind.
- **Change needed:** Bind Src=snapshot at Dst=c.Dir. Add per-deployment output, socket, log and cgroup keys. confine needs a way to bind another source at Dir, e.g. a same-depth extra bind. Non-isolated mode needs cmd.Dir set to the snapshot plus a generated GOWORK.
- **Compat:** Tiles that don't opt in keep the live-dir binds unchanged. runner.go is at 833/850 lines, so new code goes in new files.

### Registry and watch loop

- **Paths:** `internal/registry/registry.go:372-476`, `internal/boot/serve.go:164-206`, `internal/watch`
- **Today:** A scan of the live tree decides which components exist and what their manifests say. Every change publishes a reload event and triggers a restart.
- **Change needed:** Pause suppresses reload and restart. Decide whether manifest-derived facts are frozen along with the files.
- **Compat:** Unpaused tiles are unchanged.

### Docs contract and drift tests

- **Paths:** `docs/protocol.md:14-95`, `docs/protocol.md:207-330`, `docs/auth.md`, `docs/elements.md:195-300`, `docs/elements.md:533-541`, `docs/sdk.md:24`, `docs/sdk.md:85-124`, `docs/compat.md`, `docs/changelog.md`, `internal/server/openapi.go`, `internal/apicheck/apicheck_test.go`, `hack/size-budget.txt`
- **Today:** Every route, header, event and env var is documented. apicheck reconciles the mux, openapi.go and protocol.md. The size ratchet caps files.
- **Change needed:** Document new routes (pause, deploy, promote, the frame-token parameter), the deployment header, event fields, URL shapes, env vars and the SDK CallerInfo field, with changelog entries.
- **Compat:** Additive only (compat rules 2, 7, 8). Nothing is removed or renamed.

## Hazards

- '@' is legal in tile names (util.go:95-113; broker/policy.go:156-166 bans only ':') and is the allowance separator, cut at the first '@' (users/orgs.go:598-642); '#' is the binding-instance separator (netfn.go:315-320). A deployment-qualified name has no free separator.
- Deployment-qualified principals fail open on ceilings: owner-derived org and personal policy rows key on owners[path] (users/orgs.go:169-173, 1234-1263), which has no entry for apps/x@dev.
- The workspace-owned shell keys status dots and toasts on e.component and can't be upgraded (bx-shell.js:1257-1266; compat.md). Any dev status published under the bare component shows on the prod tile.
- watchStatusRestarts clears a tile's status on every build-start for that component (obs/status.go:129-140), so dev builds published under the same component erase prod's status.
- Cron jobs and bus subscriptions are keyed (component, name) (cron.go:108; bussubs.go:158) and dispatch to /api/<comp> (cron.go:200-209; bussubs.go:127-135). A dev deployment's registrations overwrite prod's, and deliveries hit the primary.
- PUT /iface-instances and PUT /ingress-hosts replace the component's whole map in the root xbin.json and trigger restarts or an ingress reconcile (netfn.go:1037-1131; ingressfn.go:564-582). A dev deployment calling either rewires production.
- POST /notify uses p.Component for the tile, its budget, the mute key and the c/<tile>/ link (push/api.go:287-300, 348-368). A dev backend running as the same principal sends real pushes as the prod tile.
- Bus publish fans out to hub subscribers and push subscriptions keyed only by resource (resources.go:408-437; bussubs.go:271-335), so dev events reach prod frontends and backends, and prod events reach dev frontends.
- The vault is per component and readable by the tile's instance token (vault.go:45-47, 178-189). A dev backend reads prod secrets and can act on external accounts; alwaysOn adapters would open duplicate connections (runner/alwayson.go).
- Non-isolated mode has no mount namespace to show a snapshot at the canonical path (runner.go:446-461; confine.go:140-143). Inference: a snapshot under the workspace finds the root go.work, which doesn't list it (deps.go:120-163), so the build fails without a GOWORK override. Relative replaces such as examples/counter-go/go.mod break outside the canonical location.
- confine.Cmd always binds Dir at itself (confine.go:222-228). Building a snapshot at the canonical path needs an extra same-depth bind, which relies on sortBinds' stable ordering (sandbox.go:115-127), or a new field.
- Legacy /c/ follows cross-tile symlinks through OpenResolved against the workspace root (static.go:182-219). A snapshot stored elsewhere can't honor relative cross-tile symlinks, so snapshot serving must use OpenBeneath semantics.
- Legacy authorizes credential-less subresources by URL path only (static.go:278-311), and relative URLs drop the query, so a query-parameter deployment selector can't reach subresources.
- On its own origin a tile document is allow-same-origin with real storage (tileassets.go:73-84). Serving dev on prod's origin would share prod's localStorage and IndexedDB.
- mayMintFrameToken treats any principal of the same component as the tile itself (static.go:494-503). Without a deployment check, a main frame navigating to a dev URL (or dev to main) gets that deployment's token.
- Under option (i), if deployments ever run code not written by the tile's writers (PR previews: D48 read=suggest, prs.go:95-111), reader-authored code would get the tile's grants, vault and capabilities.
- Dev deployments of xbin-capable or chrome tiles act on real workspace governance, which has no deployment dimension. Chrome runs unsandboxed as the human (static.go:313-325); capability grants come from broker.go:462-475.
- If the working tree loses both xbin.json and index.html, the component drops out of the registry and /api/<tile> returns 404 (registry.go:456-476; proxy.go:128-134). Pause alone doesn't keep the backend reachable.
- Net-provider splices get one link per (provider, client component), created at provider spawn and addressed by sorted index (netfn.go:182-233; runner.go:545-588). A dev client has no link until the provider restarts, which renumbers and restarts the other clients.
- Non-isolated DialInto dials 127.0.0.1:<port> (runner/ingress.go:49-56), so two deployments of a tile with a fixed stream port collide on the host.
- CGI tiles run tile code via net/http/cgi on the host as xbind, even under --isolate (proxy.go:175-177, 237-261; runner.go:614-620). Pre-existing and outside the brief, but it matters if deployments serve cgi from snapshots.
- Size ratchet (hack/size-budget.txt; internal/sizebudget): runner.go is at 833/850 lines, auth.go at 707/732, bx-frame.js at 866/900 (unlisted JS cap). New logic must go in new files.
- bx-frame matches e.component === this.src (bx-frame.js:366-395), and the shell reloads its component list on every reload event (bx-shell.js:195). Deployment events need new types or tags.
- HTTP interface instances are not an isolation boundary: httpBindingRole ignores the instance (netfn.go:283-310) and the grant covers the whole provider tile, so instances can't substitute for deployments.

## Open questions

- Is a non-primary deployment an accident boundary (same writers, option i) or a trust boundary (code from PR previews or pushes by non-writers)? The answer decides principal separation and who may deploy or promote.
- Which user level may open a dev frontend (read or write)? Which may pause, reload now, deploy or promote (write or terminal)?
- Should the vault be shared, copied or separate per deployment? A shared vault means external side effects from dev using prod credentials.
- Parallel fabric: when a provider has no same-named deployment, should dev calls fall back to the primary, be refused, or follow a per-binding choice? What should dev see for cross-scope resources and buses?
- When paused, is only the file tree frozen, or also the manifest-derived record (inject, native entry, runtime, entry, alwaysOn, vm, interfaces, uses, component existence)? The chrome flag must stay live.
- Do nested components inside a deployed tile get deployments per registered component or per tile tree (topTile, as D95's same-tree navigation rule assumes)?
- What grammar do deployment names follow, and how do git branch names (which may contain '/') map to them? Names end up in URLs, env vars, file names, token fields and label hashes.
- Which URL namespace: a dot-marker inside /c/ (collision-free), a new top-level route, or origin-only? Should absolute /c/<self>/ URLs in dev markup load the primary's files, as in tokens mode?
- Are dev views only pop-outs from the terminal window (vendor code), or can they be screen tiles? The latter requires the deployment to travel inside src through old shells.
- Which self-scoped APIs are primary-only for non-primary deployments: xbin:* governance, notify, ingress-hosts, iface-instances, code-PR decisions?
- If the primary becomes assignable, do resource namespaces follow the deployment name (the legacy keys stay with main) or the primary role?
- Should non-primary deployments honor alwaysOn? Duplicate external connections are a risk.
- How do humans and tools (bx, curl, terminals) reach a dev backend: an inbound selector header, a path form, or a deployment-attached terminal token?
- Snapshot source and store: a git tree export via confine or a copy? Where does it live, how is it made immutable and garbage-collected, and what does hot reload attach to when the working tree is on a branch without a deployment?
- Where is pause and deployment state persisted: the root xbin.json (compat rule 9 forbids boot-time changes there), the users store, or .xbin/?
