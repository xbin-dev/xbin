# Builder contract, SDK, CLI and compat rules

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `contract`.

## Report

## Builder contract, SDK, CLI and compat rules: what tile deployments and paused hot reload must extend without breaking

All paths are relative to /home/magik6k/buxon. Anything marked *(inference)* is my reading, not verified behaviour.

### 0. Constraints the contract imposes (read first)

1. **The tile path has to stay the only identity a tile can see.**
   - Backends: `XBIN_COMPONENT` / `xbin.Self()` (sdk/xbin.go:82).
   - Frontends: `xbin.self`, read from `<meta name="xbin-component">` (web/xbin-client.js:50; internal/server/static.go:458-464).
   - Tiles build URLs from it:
     - `/api/${xbin.self}/…` (12 hits in shipped tiles) and `selfApi()` (9 hits; web/bx-kit.js:41);
     - `res:${xbin.self}/…` bus ids, which the workspace AGENTS.md:262-263 and :305-311 tell tiles to use;
     - the vault URL `http://xbin/api/xbin/vault/<Self()>/<key>` (sdk/xbin.go:264-317);
     - frame-token renewal `?component=<self>` (xbin-client.js:83).
   - So a deployment must be a **second dimension**, carried by the credential or by new optional signals. It cannot be encoded into the path.
   - No separator is free:
     - `#` already means iface instances (`provider#id`);
     - `~` is the `/c/~<asset-token>/` plane;
     - `:` is refused in new tile names by policy (internal/broker/policy.go:162-165) but not by `util.ComponentPathOK` (internal/util/util.go:97-115);
     - `@` is a legal directory character.

2. **Every event consumer keys on `component` alone.**
   - bx-frame `_event` (web/bx-frame.js:365-394): `reload` → `_reload()`, `build-error` → overlay, `build-ok` → clear.
   - web/bx-code.js:268.
   - The status plane, which clears a tile's status on **any** `build-start` (internal/obs/status.go:129-143).
   - The shipped iOS app (native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:54-57).
   - Old shells: `reload` reloads their lists (workspace-template/shell/bx-shell.js:195).
   - Consequence: a non-primary deployment must not publish plain `reload` / `build-*` events for the tile path. A **new event type** is safe: the app ignores unknown types (`.other`, Events.swift), and compat rule 10 says receivers ignore what they do not know.

3. **Size budgets are used up exactly where the feature lands.** Rules: hack/size-budget.txt; internal/sizebudget caps unlisted Go files at 800 lines and shipped JS at 900.

   | File | Lines / budget |
   |---|---|
   | cmd/bx/main.go | 1150 / 1150 |
   | internal/term/term.go | 950 / 950 |
   | internal/runner/runner.go | 833 / 850 |
   | internal/auth/auth.go | 707 / 732 |
   | web/bx-frame.js (unlisted) | 866 / 900 |
   | workspace-template/shell/bx-shell.js | 1879 / 1892 |
   | workspace-template/apps/welcome/notes.js | 1028 / 1050 |

   The precedents are splits:
   - `moreCmds` in cmd/bx/native.go:26-32, whose comment says "main.go's switch is at its size budget";
   - web/frame-titlebar.js, frame-launcher.js and frame-info.js, all "extracted from bx-frame … the frame is at its size budget".

4. **Pause cannot be a lifecycle state or a manifest key.**
   - Old shells treat `state !== 'enabled'` as disabled and offer "Enable" (shell/menus.js:111-116).
   - The proxy answers 409 for every non-enabled state (internal/proxy/proxy.go:144-157).
   - `POST /lifecycle` rejects unknown states (internal/broker/lifecycle.go:82-85).
   - A manifest key is itself a file edit, so toggling it would reload and restart the backend (`changedComponents`, internal/boot/serve.go:162-199).
   - Naming clash: the docs already call lifecycle *disable* "pause/resume a tile" (docs/bx.md:138), and the shell menu's Disable item uses ⏸ (menus.js:114).
   - Second naming clash: "identity" is the core term for the component path ("its PATH is its identity", elements.md:5; docs/overview/05-identity.md), so calling sub-deployments "identities" would collide.

5. **"No change for tiles that don't opt in" is reachable only with signals that are absent by default.** Precedents for each:
   - Env vars injected only when relevant: `XBIN_IFACE_<SLOT>_INSTANCE` (internal/broker/resources.go:119-121).
   - Headers only when set: `X-XBin-Viewed-By` (proxy.go:304-306).
   - Head injection only in the affected documents: legacy mode's injection is "byte-for-byte unchanged" (static.go:439).
   - New event types, a new endpoint family, and `omitempty` fields (componentInfo, internal/server/api.go:130-160).
   - State kept in `data/` rather than the root `xbin.json`. The legacy fixture requires boot to leave the root `xbin.json` byte-identical (test/legacy_workspace_test.go:69-71).

### 1. The manifest (`xbin.json`, docs/elements.md:22-191; `registry.Manifest`, internal/registry/registry.go:52-99)

All keys are optional. JSONC is allowed (D5).

| Key | Semantics |
|---|---|
| `runtime` | `static` (default) \| `go` \| `node` \| `python` \| `cgi`. A backend exists only for the last four and never for a template (`HasBackend`, registry.go:391-402). |
| `entry` | Defaults: go `./backend`; node `backend/server.js`; python `backend/server.py`; cgi `backend/handler`. |
| `deps` | Paths materialised as `deps/<basename>` symlinks. Editing plane only; grants no call rights. |
| `setup` | Shell script building a cached env layer, run with net:internet. Rebuilt only when it changes; not allowed with `vm`. |
| `alwaysOn` | Started at boot, never idle-reaped, restarted with 1 s → 5 min backoff; the crash breaker still applies. |
| `vm` | `true` or `{memory, vcpus}`. Needs `--isolate`, admin VM policy and KVM (else emulated). Otherwise it fails with the reason; `VMOpt.UnmarshalJSON` errors on a bad shape (registry/vm.go:21-47). |
| `uses` | `[{target, role}]`. Targets: component paths, `res:<scope>/<name>`, `cap:open-links\|net-admin\|containers`, `gpu:all\|<idx>\|<uuid>`. Owner-approved; same-scope is auto-approved. |
| `interfaces` | Requested slots `{kind: net\|http\|stream\|lan-ingress, service, multi}`, bound by the owner; become `XBIN_IFACE_*` env. Rebinding restarts the backend (elements.md:114-115). |
| `provides` | Offered slots `{kind, service, role (default reader), instances}`. |
| `exposes` | Public endpoints: http `{paths}` or stream `{proto, port}`. Inert until bound; `ValidateExposes` turns errors into a manifest error (registry.go:137-172). |
| `expose` | `{roles: name→description (required), implies}`. `API.md` required (bx doctor). |
| `inject` | `false` serves the HTML byte-exact: no identity at all. |
| `native` | Module path or `false`. Tolerant: never a manifest error (registry/native.go:17-43). |
| `chrome` | Host-set trusted chrome: unsandboxed, acts as the signed-in human. |
| `template` | `{title, description, defaultName}`: a blueprint that doesn't run. |

Related files:
- `scope.json`: `{resources, importMap}` (registry.go:187-191).
- Workspace root `xbin.json`: `{schema, importMap, grants, resources, bindings, ifaceInstances, ingressHosts, lifecycle, lifecycleAt}` (registry.go:207-236).

**How unknown keys are treated**
- `jsonc.Unmarshal` is `json.Unmarshal(Strip(src), v)` without `DisallowUnknownFields` (internal/jsonc/jsonc.go:90-95). Unknown keys are silently ignored by this xbind and every older one; compat rule 7 promises this (compat.md:51-53).
- A *known* key with a bad value is a manifest error. The component keeps serving statically and the error shows in `bx ls` / `bx doctor` / `/components` (elements.md:189-191; registry.go:458-467).
- The tolerant precedent for a new key is `NativeOpt`: invalid values are recorded in `Invalid`, and elements.md:160-166 promises "Old xbinds ignore the key … never a manifest error".

**Rewrites**
- xbind rewrites a tile's manifest only when instantiating a template (`stripTemplateBlock`). That goes through `map[string]json.RawMessage`, so unknown keys survive and comments are lost (internal/builtins/builtins.go:377-393).
- The **workspace** `xbin.json` is re-marshalled through the `WorkspaceManifest` struct on every mutation (`MutateWorkspace`, registry.go:586-595). Any binary that doesn't know a top-level key drops it: a downgrade hazard if deployment state were stored there.

**Could the feature avoid a manifest key?** Yes, and it should *(inference)*.
- Pause state, the list of deployments, which one is primary and the branch mapping are owner/operational state, like `lifecycle` (registry.go:229-235) or bindings.
- A manifest key would travel with clone, import, `git push` and template instantiation.
- In the branch variant it would be circular: `xbin.json` differs per branch.
- Toggling it would restart the backend.
- If an author-side opt-in is ever needed, follow `native`: optional, tolerant `UnmarshalJSON`, absent = today.

### 2. What a backend receives (docs/sdk.md, sdk/, internal/runner, internal/broker)

**Env vars.** Sources: runner.go:417-425, `Broker.EnvFor` (internal/broker/resources.go:69-145), `backendEnv` (internal/runner/hostenv.go:20-46).

| Variable | Value |
|---|---|
| `XBIN_SOCKET` | Per-generation socket `g<N>.sock` under `RunDir/<CompKey>` |
| `XBIN_COMPONENT` | The tile path |
| `XBIN_GATEWAY` | `.xbin/run/gateway.sock` |
| `XBIN_TOKEN` | Per-generation instance token, `util.RandomToken(24)` at runner.go:415; dies at swap |
| `XBIN_RES_<NAME>` | Canonical `res:<scope>/<name>` for kv/blob/bus/cron; a dir path (same-scope filesystem); a `.sqlite` file (same-scope sqlite); nothing for cross-scope fs/sqlite |
| `XBIN_IFACE_<SLOT>_URL` (+`_INSTANCE`) | Single http slot; `_INSTANCE` only when bound to an instance |
| `XBIN_IFACE_<SLOT>` | JSON list, `multi` slots |
| `XBIN_IFACE_<SLOT>_ADDR` | Bound stream interface |
| `XBIN_IFACE_<SLOT>_IP` | lan-ingress leg |
| `XBIN_INGRESS_FORWARD_URL`, `XBIN_LAN_INGRESS` | Ingress terminator / net-provider tiles |

- Under isolation, only the rootfs `PATH`, `LANG`/`LC_*`/`TZ` and the proxy vars cross from the daemon.
- cgi gets `XBIN_COMPONENT`, `XBIN_FROM`, `XBIN_ROLE` plus CGI/1.1 (proxy.go:245-259).
- **`XBIN_URL` is not a backend variable.** It exists for terminals (internal/boot/boot.go:262-276; term.go:760-797) and bx. Backends reach xbind through the gateway.
- The backend env contract is stated in all of these: elements.md:533-546, protocol.md:2311-2327, sdk.md:5-24 and :156-199, sdk/xbin.go:14-17, docs/overview/16-extending.md:22-29 and :73, workspace-template/AGENTS.md:429-433.

**Headers.** Constants at proxy.go:29-44; injection in `identify` (proxy.go:275-313); documented at protocol.md:62-87.
- Injected: `X-XBin-From`, `X-XBin-Role`, `X-XBin-User`, `X-XBin-User-Level`, `X-XBin-Viewed-By` (only in view-as), `X-XBin-Ingress-Host` (ingress only).
- `identify` strips every inbound `X-Xbin-*`, xbind's cookies and any `Authorization: Bearer`. `?frame=` is consumed (proxy.go:199-202).
- Response header: `X-XBin-Lifecycle` on the 409 (proxy.go:149).
- Inbound headers xbind reads: `X-XBin-Frame-Token` (a credential) and `X-XBin-Client` (internal/server/native.go:26).

**SDK** (sdk/go.mod: go 1.24, no requires):
- `Serve`, `Self`.
- `CallerInfo{From, Role, Owner, User, UserLevel, ViewedBy}` / `Caller`, `UserCanWrite`, `Ingress`, `RoleSatisfies` / `Role` / `RoleFunc` (xbin.go:81-187).
- `Client` / `GatewayDial`, `Resource`.
- `Secret` / `SetSecret` / `DeleteSecret`, all URLs built from `Self()`.
- `Publish`, `Subscribe` / `Unsubscribe` / `BusEvent`.
- `Status` / `ClearStatus` / `Notify` (all `POST /tile-report`), `NotifyUser` / `NotifyUserWith` (notify.go).
- `KV` (kv.go: `/api/xbin/kv/` + the canonical id), `WriteJSON` / `WriteError` (http.go).
- Vault: reading a value is **backend-only** (instance token, D30); list/set/delete by the backend, the tile's own terminal, or an admin (protocol.md:1715-1727). The vault is keyed by the component path.

**How a backend could learn its deployment** (all additive):
- A new env var such as `XBIN_DEPLOYMENT`, injected only for non-primary deployments. Precedent: `_INSTANCE` names which instance.
- A `deployment` field on `/whoami` for element principals (internal/broker/whoami.go:62-67 returns `kind: element`, `id`).
- An SDK helper `xbin.Deployment()` mirroring `Self()`.
- Optionally a header such as `X-XBin-Deployment` feeding a new `CallerInfo` field. Precedents: `User`/`UserLevel` (D29) and `ViewedBy` (D64) were added the same way. SDK rule 8 allows this: permissive changes only, with a changelog line.

**What breaks if a backend doesn't know its deployment and xbind doesn't compensate server-side:**
- **(a) Identity and URLs: nothing breaks**, provided `XBIN_COMPONENT` stays the tile path. If it changed, the vault URLs (sdk/xbin.go:265, :300) and `res:${self}` ids would break.
- **(b) Resources.**
  - fs/sqlite are transparent: env carries a path.
  - kv/blob/bus/cron env carries the canonical id (resources.go:98-99), and code also builds `res:${self}/…` by hand. Separation must therefore key on the calling principal's deployment *(inference)*, or hand-built ids reach prod data.
- **(c) Vault.** Shared, unless resolved per deployment from the token.
- **(d) Cron jobs and bus push subscriptions.** Per component, idempotent by name (resources.md:166, :186-196; protocol.md:1741-1752, :1930-1937; stored in `data/cron-jobs.json` and `data/bus-subscriptions.json`). The documented pattern subscribes at every start (sdk.md:98-111), so a dev start re-registers main's name and deliveries go to "the component's backend".
- **(e) `PUT /iface-instances` and `PUT /ingress-hosts`.** Both *replace* the map or set (protocol.md:1627-1650). A dev provider registering at start clobbers main's live routing.
- **(f) Status.** `POST /tile-report` is keyed by component (protocol.md:1760-1768), so dev errors paint the tile for every user. Main's status is also cleared on a dev `build-start` (obs/status.go:129-143).
- **(g) Notifications.** `NotifyUser` / `Notify` from dev reach real people under the tile's rate budget (sdk.md:116-154).
- **(h) Outbound calls.** `X-XBin-From` is the tile path and grants are per tile, so dev calls other tiles' primaries with prod rights, and callees can't tell.
- **(i) Logs.** One file per component, `.xbin/log/<CompKey>.log` (internal/obs/logs.go:69; bx main.go:507-515).

### 3. What a tile frontend knows (web/xbin-client.js, web/bx-kit.js)

**Metas xbind injects** (static.go:431-464):
- Always: `xbin-component`, `xbin-frame-token` (empty for another tile's fetch; `mayMintFrameToken`, static.go:494-503).
- When relevant: `xbin-interfaces`, `xbin-sandbox`.
- App clients only: `xbin-ws-origin`.
- `xbin-tile-assets` / `xbin-workspace-origin` and the tokens-mode `<base data-xbin-assets>` + import-map remap (internal/server/tileassets.go:338-371).
- The native runtime document adds `xbin-native`.

**The frozen `window.xbin` object** (xbin-client.js:412) has `self`, `iface`, `fetch`, `ws`, `url`, `download`, `bus`, `events`, `dialog`, `window`, `status`, `clearStatus`, `notify`, and `native` (app runtime document only).
- `fetch` / `ws` / `url` attach the frame token (header or `?frame=`).
- Renewal: `GET /api/xbin/frame-token?component=<self>` every 10 minutes.
- Bus and events share one `/ws/events?frame=` socket; `bus.on(prefix)` filters `e.topic` starting with `res:<scope>/<name>/`.
- `publish` posts `/api/xbin/bus/publish`; `status` / `notify` post `/tile-report`.

**Addressing its own backend.** Always the absolute `/api/<self>/…`: `xbin.fetch(`/api/${xbin.self}/…`)` or `selfApi(path)` (bx-kit.js:41). In origins mode a raw `fetch('/api/<you>/x')` works too (elements.md:280-291). The frame token is opaque ("frontends … never parse it", protocol.md:36). Its wire format `b64(component)|b64(user)|exp|gen|hmac` (internal/auth/frametoken.go:35-39) already has a precedent for adding a field with a legacy-verify path.

**Dialogs and windows.**
- bx-frame re-dispatches `xbin:dialog` / `xbin:window` as `bx-spawn` with `from: this.src` (bx-frame.js:502-516).
- The **scaffold** shell builds the window src `[d.from, spec.path].join('/')` (bx-shell.js:677).
- So a deployment context needs a new field in that event, which old shells will ignore.

**Assets.**
- `/c/<tile>/` serves the tile directory live with `Cache-Control: no-store` (elements.md:195-197).
- Relative URLs are the portable form. Absolute self-imports are remapped only in tokens mode.
- *(inference)*: in the default legacy mode, a non-primary frontend's relative URLs resolve to the working tree unless xbind injects a deployment-scoped `<base>` and import-map remap, reusing tokens mode's machinery.

**Event consumers.** Tile frames can subscribe via `xbin.events.on`; chrome uses `/vendor/events-socket.js` `onEvent` (docs/frontend-kit.md table).

### 4. Resources (docs/resources.md)

- **Kinds:** filesystem, sqlite, kv (bbolt, values up to 1 MiB), blob (256 MiB per write), bus (in-memory, at-most-once), cron.
- **Declaration:** in `scope.json` `resources`, or workspace-level in the root `xbin.json` (`res:workspace/<name>`).
- **Addressing:** `res:<scope>/<name>` in `uses`, in the APIs (`/api/xbin/kv|blob/res:<scope>/<name>/…`, bus publish, cron / subscription bodies) and in bus topics `res:<scope>/<name>/<topic>`.
- **Scope rules:**
  - Same-scope is auto-granted (still declared in `uses`); cross-scope needs owner approval (elements.md:548-563).
  - filesystem/sqlite paths are handed **only** to same-scope components (resources.md:275-278; resources.go:83-96).
- **Storage:** always encrypted at rest (gocryptfs per fs/sqlite/blob resource; envelope-encrypted kv) under `data/resources-enc/<scope>/`, using the `ScopeKey` "apps~cal" (util.go:125-133).
- **Quota:** per scope, `XBIN_LIMIT_DISK` (default 50G).
- **Backup:** "source + scope data + terminal env; NOT vault/env-layer" (protocol.md:1685-1688).
- **Why it matters:** resources are **per scope and shared by every component in the scope**, while deployments are per tile. "Dev must not see prod data" therefore needs a rule for scope-shared resources (see open questions).

### 5. docs/protocol.md sections, and how an endpoint gets registered

**Sections the feature touches:**
- Identity headers: :62-94.
- `/c/`: :207-268. `?native=1` (a precedent for a query-selected variant of a tile document): :269-295. `/c/~<token>/`: :296-311.
- `/api/<component>` resolution, longest prefix: :406-419 (proxy.go:130 `Resolve` → proxy.go:182 `Runner.Ensure`).
- `/backends` (admin; a map `{path: {state, gen, error?}}`, runner.go:773-796, counted by the old shell at bx-shell.js:1326-1333): :430.
- `/runtime`: :431-460.
- `/tile-status` (self or admin; internal/boot/api.go:87-117): :488-495.
- `/logs` (admin, the tile itself, or terminal-level; `?tail`, `?follow`): :510-519.
- `/components`: :524-543. `/frame-token`: :559-568. `/whoami`: :574-606.
- Term and agent sessions: :628-800 (`POST /term/sessions` takes `net`/`api`/`gpu`/`vm`/`resume`).
- create/clone/builtins/templates: :1293-1389. The `template` git remote is a read-only dumb-HTTP server at `GET /templates/{repo}/{rest...}` (:1384-1389).
- code/git (admin or `code[:<component>]`; `/git/log` reads the component's OWN repo; `remote` = origin): :1391-1420.
- Code PRs (xbind never applies; the target runs `git am`): :1422-1458.
- iface-instances / ingress-hosts: :1627-1650.
- lifecycle (owner-gated, 409 + `X-XBin-Lifecycle`): :1669-1683.
- vault: :1715-1727. kv/blob/bus: :1729-1752. tile-report: :1754-1768. cron: :1930-1937. Bus delivery: :1939-1959.
- `/ws/term` query params `cwd`, `session`, `api`, `net`, `gpu`, `vm`; control frames where "Unknown ops are ignored on both ends": :2056-2177.
- `/ws/events` types: `reload`, `build-start` / `build-error` / `build-ok`, `grants`, `branding`, `native`, `bus`, `status`, `term`, `session`: :2179-2233. Also published but missing from that list: `pr` (internal/broker/prs.go:149, mentioned at :1427) and `users` (internal/broker/orgsapi.go:80).
- postMessage: :2270-2309. Backend contract: :2311-2327.
- Filesystem contract: `.xbin/` is "derived state — safe to delete when xbind is stopped"; `data/` is the backup unit (:2329-2350).

**Registering a new endpoint.** internal/apicheck/apicheck_test.go:119-200 enforces docs/maintenance.md:476-505.
- It is **three edits**:
  1. a `RegisterAPI("METHOD /path")` string literal, reachable via `broker.Register(srv)` / `srv.Handler()`, or inline in internal/boot/*.go (read from source);
  2. an `ep{method, path, tag, summary, capability, desc, params, body, resp}` row in internal/server/openapi.go (capability vocabulary at :61-84);
  3. a column-0 row inside a code fence in protocol.md.
- A `RegisterAPI` literal in a package the fixture doesn't mount fails the test.
- Optional queries go in the row as `?x=<y>`, never `[?x]`.
- Errors go through `server.WriteError`.
- `server.DecodeJSON` rejects unknown fields (internal/server/api.go:71-76). Adding a field to an *existing* request body is additive only toward newer servers; an older server answers 400.

### 6. docs/compat.md rules, paraphrased (compat.md:16-74)

1. **Old workspaces boot on new binaries.** Boot migrations are additive and idempotent (a second run changes nothing). The `home/` + `homes/` hard stop stays. Boot never records edited scaffold files as pristine.
2. **Old chrome works against new xbind.** Boot changes nothing in a workspace except the `tiles/organisations` backfill, so the HTTP API is additive only: no route removed or renamed, no response field removed or retyped. Shells must ignore unknown fields.
3. **Shipped URLs are frozen.**
   - Every `/vendor/<name>` stays served.
   - The import map lives in each workspace's `xbin.json`, which no upgrade rewrites, so shipped code never uses a bare specifier; imports go by absolute `/vendor/` URL.
   - A moved file leaves a re-export shim.
4. **Scaffold layouts are additive.** `bx builtin update` adds files. Entry files (`shell/bx-shell.js`, `tiles/admin/admin.js`, `index.html`) keep their names and import new siblings relatively. Nothing shipped is renamed or removed.
5. **Theme fallbacks** in `web/bx-*.js` stay, regenerated from theme.css.
6. **The CLI is a superset.** Every accepted invocation stays accepted: interleaved positionals, repeated flags, `--flag value` / `--flag=value`, `--no-x`. A newly erroring path gets a changelog line and warns for one release.
7. **The manifest schema is additive.** `expose` and `exposes` both work, new keys only, unknown keys ignored.
8. **The SDK stays zero-dependency** and changes only permissively, with a changelog entry.
9. **Every boot migration has a fixture test.** `make integration` boots an aged workspace twice. The first boot may change only listed paths and never the root `xbin.json`; the second changes nothing; a home conflict stops the daemon. New migrations extend the allowed list with a reason.
10. **The native app contract is additive:** vocab, tree/bridge messages, `/vendor/xb-native.js` exports, `xbin.native`. Breaking changes need a new major version served side by side.
11. **Enforcement.** Builder-visible changes need a changelog entry; an operator action needs a note under `docs/changes/`; a silent path warns for one release before becoming an error.

Non-promises (compat.md:141-147): `.xbin/` contents, `data/` layout and temp names may change.

**Guards in docs/maintenance.md a big feature trips:**
- **Size ratchet** (:64-74): numbers in §0. A listed file that shrinks below 90% of its budget must have its budget lowered.
- **docscheck** (:76-96):
  - Every cited decision id needs a definition, and the id regex knows only `D|ND|ING|LC|IFACE|VD|RT|ISO|BU|CM|PR` (internal/docscheck/docscheck_test.go:25, :28). A new prefix such as `DEP-1` needs the regex extended, or use D1xx+.
  - Links in docs/ and plans/ must resolve.
  - Every plans/*.md needs a `> Status:` line in its first 12 lines.
- **Embed guard** (assets_test.go:27-87): no ELF, ≤512 KB outside web/vendor, no `.git`/`node_modules`/`.claude`/`deps`, and **no `plans/` citations** anywhere in the embedded trees (web/, docs/, workspace-template/, builtin-*). Cite docs and D-ids instead.
- **js-check:**
  - node --check over every shipped script and inline module; named imports resolved.
  - Kit helpers (`api|xbinApi|selfApi|jbody|esc|deepActive|pathHas|clampBox|clampWin`) may be defined only in web/bx-kit.js (hack/check-js.mjs:88-100).
  - No `._x` member access in hack/ui-harness.
- **theme-check** (:289-296): every `var(--bx-x, literal)` fallback in web/, workspace-template/ and builtin trees must equal theme.css.
- **Legacy fixture** (test/legacy_workspace_test.go:31-97, plus its in-process twin internal/boot/boot_test.go:116-175 — D62 made boot a package so it can run in-process):
  - Allowed first-boot paths: `.xbin/`, `data/`, `homes/`, `home/`, `tiles/organisations/`, `go.work`, `.gitignore`, `AGENTS.md`, `CLAUDE.md`, `.git/`, `*/deps/`, `*/.git/`.
  - The root `xbin.json` must be byte-identical; the second boot must change nothing.
- **Route inventory** (§5).
- **TestConfigDoc:** a new flag or env var is a `boot.Config` field plus a regenerated docs/config.md (:189-196).
- **Other tests and harness rules:**
  - `TestNoDirectExec` (D78, :49-62).
  - `TestBuiltinManifestsAndRoleGuards`, `TestTileVersions` (bump `tile.json` versions), `TestShippedTilesPassStrictGating` (:245-277).
  - UI harness (:416-474): passes drive `testApi()`, and a pass cleans up the per-user `term:<tile>` / `termvm:<tile>` prefs it created.

### 7. Changelog and migration notes (repo AGENTS.md:115-159)

- **Docs-discipline table:**
  - HTTP/WS → protocol.md + openapi.go.
  - Builder-visible → docs/*.md + workspace-template/AGENTS.md + welcome notes.js.
  - A decision → plans/<area>.md + DECISIONS.md.
  - A guard → maintenance.md.
  - `bx` → docs/bx.md + usage strings.
  - Anything builder-visible → a changelog entry; BREAKING → a migration note.
- **Changelog entries:** newest first under `## YYYY-MM-DD`. The rule says one `- area: what changed` line; in practice entries are a bold lead sentence plus prose with D-ids and doc links (docs/changelog.md:14-60).
- **BREAKING:** mark the entry and add `docs/changes/YYYY-MM-DD-<slug>.md` with exactly **What changed · Who's affected · How to migrate · Why**, linked from the entry. Observed title form: `# 2026-09-24 — <summary> (D78)`.
- **Reach:** these files are embedded and served at `/docs/`. Workspace agents are told to read the changelog after upgrades (workspace AGENTS.md:15-18).
- **Grep every statement of a contract** (the instance-paths cautionary tale, AGENTS.md:132-139).

### 8. workspace-template/AGENTS.md (the root CLAUDE.md is a symlink to it)

What agents are told:
- :34-36: "Saving any file live-reloads the frontend and rebuilds/swaps the backend. There is no deploy step."
- :150-158: debug by curling `/api/apps/thing/hello`, then `bx status` / `bx logs -f` / `bx doctor`.
- :160-178, **CM-2:** commit often and unprompted inside the tile's own repo; never ask. xbind never auto-commits (DECISIONS CM-2, D2).
- :305-311: never hardcode your own path.
- :323-324: frames live-reload on save.
- :472-486: "A save = a new process"; failed after 3 fast crashes "until you save a change".
- :534-535: status resets on restart.
- :542-603: code PRs. The clone is of the target's *last commit*; `git am` lands in the target's working tree; builtin-update PRs.
- :668-673: the terminal dev sandbox.
- :883-884: "a save is visible within ~300–500 ms, Go swaps in ~1 s".
- Testing guidance is thin ("build/test what you can", :556), because save = production.

What changes once a tile can have deployments or a paused primary:
- **A save no longer necessarily goes live.** It reaches the hot-reload target (maybe dev), or nothing while paused. Agents need to know which deployment they are editing, how to test it (dev URL, logs, status), and that promotion is a separate, deliberate step.
- **The CM-2 tension.** If a deployment follows a branch or receives pushes, then "commit/push unprompted" becomes "deploy unprompted", unless deploying stays separate from committing.
- **PR and builtin-update flows** apply to the working tree. Which deployment sees them?
- **Debug recipes** hit the primary.
- **The "failed until you save" and status-reset rules** need to say which deployment they apply to.

Distribution caveat:
- `AGENTS.md` is seeded only when absent (boot.go:153-160; internal/boot/workspace.go:69-86).
- It is **not** a builtin scaffold unit: units are only directories holding `xbin.json` or `index.html` (internal/builtins/updates.go:150-175).
- So existing workspaces never receive the new text. docs/ is refreshed at every boot (extracted to `.xbin/docs`, `XBIN_DOCS`, boot.go:335-341).
- Guidance must therefore land in docs/*.md and the changelog, and the in-product surfaces (bx output, the terminal window) must be self-explanatory.
- The welcome tile's notes.js teaches save=live at lines 38-39, 81, 376 and 480-515. It is a scaffold unit, so existing workspaces keep their old copy.

### 9. bx (cmd/bx)

**How bx authenticates and connects**
- `ownerToken()` (main.go:209-237) takes `XBIN_TOKEN` first: the tile-scoped terminal token. If `XBIN_GATEWAY` is set it is inside a backend sandbox and borrows no host token. Otherwise it reads `<workspace>/.xbin/token`.
- `transport()` (main.go:239-254): `XBIN_URL` over HTTP; else the gateway socket as `http://xbin`; else 127.0.0.1:8642.
- `apiJSON` adds the hint "this terminal is scoped to <tile>" on a 403 (main.go:256-283).

**How it targets tiles**
- A positional component, defaulting to `XBIN_COMPONENT` for `status` (main.go:349-370), `code prs` and `code pr` verbs (cmd/bx/code.go:83, :159), `fix assets`, `native` and `agent run`.

**Commands relevant to the feature**
- `status [<c>] [--all]`: `/tile-status`, or `/backends` + `/status`.
- `logs [-f] <c>`: reads `.xbin/log/<CompKey>.log` **directly** (main.go:494-530; bx.md:368-369). `.xbin` is masked in isolated tile terminals (internal/term/binds.go:43-53), so *(inference)* it fails there. The HTTP twin is `GET /logs`.
- `enable|disable|hide|unhide`: `POST /lifecycle`; takes exactly one argument (main.go:858-867).
- `offload`, `backup`, `restore`.
- `code prs|pr …`: PR storage; xbind never applies.
- `template ls|new|updates` (template remote plus `git fetch template && git merge template/main`, cmd/bx/template.go).
- `builtin updates|update --replace|--merge|--pr`: replace/merge write the working tree; `pr` files a proposal (D49).
- `agent …`.
- There is no restart or reload command and no restart/reload API route.

**The CLI-superset rule in practice**
- `unknownFlag` errors except for four lenient commands; `nextArg` (cmd/bx/args.go; bx.md:371-379).
- `bx logs` and `bx status` treat *any* other argument as the component, last one wins. New flags there must stay lenient.
- New top-level commands go in `moreCmds`, because main.go has zero headroom.
- Usage is appended like `+nativeUsage` (main.go:174). Changing that line doesn't add a line.
- Update docs/bx.md.

### 10. Contract hooks (additive attach points and their precedents)

**Backend env**
- **Hook:** `XBIN_DEPLOYMENT`, injected only for non-primary deployments (or always; either is additive).
- **Precedent:** `XBIN_IFACE_<SLOT>_INSTANCE` (resources.go:119-121) and `XBIN_COMPONENT`.
- **Update:** the seven places listed in §2, plus cgi env (proxy.go:253-257).

**SDK**
- **Hook:** `xbin.Deployment()`, and optionally a `CallerInfo.Deployment` field fed by a header.
- **Precedent:** `Self()` (xbin.go:82); `ViewedBy` (D64) and `User`/`UserLevel` (D29).
- **Rule 8:** permissive change plus a changelog line.

**Injected request header**
- **Hook:** `X-XBin-Deployment`, set only when not primary.
- **Precedent:** `X-XBin-Viewed-By` and `X-XBin-Ingress-Host`. Trusted automatically, because `identify` strips inbound values (proxy.go:284-288).
- **Update:** protocol.md:62-80, docs/overview/05-identity.md:24-28, sdk.md:32-52, AGENTS.md:463-466.

**Selector consumed by xbind**
- **Hook:** a header or query parameter naming a deployment, for testing dev from a terminal or bx.
- **Precedent:** `?frame=` / `X-XBin-Frame-Token`, consumed and never forwarded (proxy.go:199-202); `X-XBin-Client` (server/native.go:26).
- **Gate like** `/logs`: self or terminal-level.

**Response header**
- **Precedent:** `X-XBin-Lifecycle` (proxy.go:149) and `X-XBin-Session` (term.go:239-241).

**Head injection**
- **Hook:** `<meta name="xbin-deployment">` plus `xbin.deployment`, only in non-primary documents.
- **Precedent:** `xbin-native` / `xbin.native` (xbin-client.js:406-412); legacy's byte-identical injection (static.go:439).

**Asset re-addressing**
- **Hook:** a deployment-scoped `<base>` plus remapping `/c/<self>/` in the import map.
- **Precedent:** tokens-mode `assetHead` (tileassets.go:338-371) and the `/c/~…` plane (protocol.md:296-311).

**Events**
- **Hook:** a new type, e.g. `{type: "deployment", component, data: {op, deployment, …}}`.
- **Precedent:** the `native`, `branding` and `pr` types added over time; the `events.Event` type list (internal/events/events.go:12).
- **Update:** the protocol.md list (:2191-2209).

**Endpoint family**
- **Hook:** a new family under `/api/xbin/…`: pause/resume, reload-now, list, deploy, promote, set-primary.
- **Precedent:** `POST /lifecycle` (owner gate) and `POST /term/sessions/{id}/restart`.
- **Register:** RegisterAPI + openapi row + protocol row (§5).

**Git**
- **Hooks:** smart-HTTP push under a `{rest...}` route, and a remote written into the tile repo.
- **Precedent:** `GET /templates/{repo}/{rest...}` (internal/broker/templates.go:30); `AddTemplateRemote` writing `http://xbin/api/xbin/templates/<name>.git` via confined git (internal/broker/templaterepo.go:139-150); the terminal's `GIT_CONFIG_*` rewrite of `http://xbin/` → `XBIN_URL` with the session bearer (term.go:783-795).

**Additive response fields**
- `/components` componentInfo `omitempty` (api.go:130-160). Precedent: `native?` / `origin?` / `sandbox?`.
- `/tile-status` (boot/api.go:110-116). Precedent: `net`, D54.
- `/runtime` Backend rows: add a `deployment` field (runner/inspect.go:26-53) rather than new `/backends` map keys.
- `/whoami` element: `deployment`.
- `/logs?deployment=`.
- `/term/sessions` rows and the `/ws/term?deployment=` param. Precedent: `?vm=1`.

**bx**
- **Hook:** a new subcommand (e.g. `bx deploy …`) via `moreCmds`, defaulting to `XBIN_COMPONENT`.
- **Hook:** lenient `--deployment` flags on logs/status, after moving those functions out of main.go.
- **Hook:** a `bx doctor` check. Precedent: the strict asset gating check.

**Terminal window (always current: `/vendor/` ships with the binary)**
- **Hook:** a pause/reload control in web/frame-titlebar.js `settings()` / `layerButtons()`. Precedent: the VM toggle (frame-titlebar.js:180-203). Update `barKey` (:61-62) so `fitBar` re-measures.
- **Hook:** a panel module `web/bx-deploy.js`. Precedent: bx-prs.js / bx-logs.js plus `_prCount`.
- **Hook:** a new `open(layout)` value (bx-frame.js:533-541).
- **Hook:** a `deployment` attribute on `<bx-frame>`. Precedent: the `height` / `no-edit` attributes.

**Manifest key (only if unavoidable)**
- **Precedent:** `native`: tolerant, absent = today.

**Owner state**
- **Hook:** `data/<x>.json` via `fsutil.WriteFileAtomic`.
- **Precedent:** `data/bus-subscriptions.json`, `data/cron-jobs.json`. Not the root `xbin.json`: struct re-marshal and the boot fixture.

**Daemon kill switch, if wanted**
- **Precedent:** the `--tile-assets` modes.
- **Update:** a `boot.Config` field plus a regenerated docs/config.md.

**Decision records**
- D1xx+ in DECISIONS.md, or a new series with the docscheck regex extended.
- A new plans/<area>.md with a `> Status:` line.
- A changelog entry.

## Surfaces this feature touches

### Manifest schema (tile xbin.json)

- **Paths:** `docs/elements.md:22-191`, `internal/registry/registry.go:52-99`, `internal/registry/registry.go:458-467`, `internal/registry/native.go:17-43`, `internal/registry/vm.go:21-47`, `internal/jsonc/jsonc.go:90-95`, `internal/builtins/builtins.go:377-393`
- **Today:** Optional JSONC keys parsed into registry.Manifest by encoding/json, so unknown keys are ignored by every xbind. A bad value for a known key becomes ManifestErr and the tile serves statically. Keyed by the component directory.
- **Change needed:** Ideally none: deployment and pause state is owner/runtime state. If an author opt-in is ever needed, add one optional key parsed tolerantly the way NativeOpt is.
- **Compat:** Rule 7: new keys only. Old xbinds ignore them. Use a never-erroring UnmarshalJSON so a stray value never becomes a manifest error. Tiles without the key behave exactly as today.

### Workspace manifest / owner state storage

- **Paths:** `internal/registry/registry.go:205-236`, `internal/registry/registry.go:586-595`, `test/legacy_workspace_test.go:31-97`, `internal/boot/boot_test.go:116-175`, `docs/protocol.md:2329-2350`
- **Today:** Root xbin.json holds grants, bindings, ifaceInstances, ingressHosts, lifecycle and lifecycleAt keyed by component path. It is re-marshalled through a Go struct on every mutation, and boot must leave it byte-identical.
- **Change needed:** Deployment registry, primary designation and paused flags need a durable home. Prefer data/<x>.json (backup unit), like bus-subscriptions.json and cron-jobs.json. A new root key would be dropped by older binaries.
- **Compat:** Absent state means today's behaviour, so no boot migration is needed. If boot ever writes anything, it must stay inside the fixture's allowed paths (data/, .xbin/) and the second boot must change nothing.

### Backend env contract

- **Paths:** `internal/runner/runner.go:407-425`, `internal/broker/resources.go:66-145`, `internal/runner/hostenv.go:20-46`, `internal/proxy/proxy.go:245-259`, `docs/elements.md:533-546`, `docs/protocol.md:2311-2327`, `docs/sdk.md:5-24`, `sdk/xbin.go:14-17`, `docs/overview/16-extending.md:22-29`, `workspace-template/AGENTS.md:429-433`
- **Today:** XBIN_SOCKET, XBIN_COMPONENT (tile path), XBIN_GATEWAY, XBIN_TOKEN (per-generation), XBIN_RES_*, XBIN_IFACE_*, XBIN_INGRESS_FORWARD_URL, XBIN_LAN_INGRESS, plus locale/TZ/proxy vars. cgi gets XBIN_COMPONENT/FROM/ROLE. Captured at spawn.
- **Change needed:** Optionally add XBIN_DEPLOYMENT for non-primary deployments. XBIN_COMPONENT must stay the tile path. XBIN_RES_* fs/sqlite paths may differ per deployment. kv/blob/bus/cron canonical ids are better kept identical, with the broker remapping by the token's deployment (inference).
- **Compat:** A new env var is invisible to tiles that ignore it. Every statement of the env contract (7+ docs) must be updated together (repo AGENTS.md: grep every statement).

### Identity headers to backends

- **Paths:** `internal/proxy/proxy.go:29-44`, `internal/proxy/proxy.go:275-313`, `internal/proxy/proxy.go:144-157`, `docs/protocol.md:62-94`, `docs/overview/05-identity.md:17-30`, `docs/sdk.md:32-52`
- **Today:** X-XBin-From/Role/User/User-Level plus the conditional Viewed-By and Ingress-Host. All inbound X-Xbin-* are stripped; ?frame= is consumed. The response header X-XBin-Lifecycle is set on 409.
- **Change needed:** Optional X-XBin-Deployment (serving deployment, or the caller's deployment in a parallel fabric). An optional inbound selector consumed by xbind for testing dev. An optional response header naming the answering deployment.
- **Compat:** Set only when non-primary, following the Viewed-By precedent, so existing tiles see identical headers. Trust comes free from identify() stripping.

### Go SDK

- **Paths:** `sdk/xbin.go:81-131`, `sdk/xbin.go:251-317`, `sdk/xbin.go:319-418`, `sdk/kv.go`, `sdk/notify.go`, `sdk/go.mod`
- **Today:** Self() reads XBIN_COMPONENT. Vault URLs are built from Self(). KV/Publish/Subscribe use the canonical ids from env. CallerInfo carries From, Role, Owner, User, UserLevel, ViewedBy. Zero dependencies.
- **Change needed:** Add xbin.Deployment() and optionally CallerInfo.Deployment. Existing functions should need no change if the server resolves deployment from the instance token.
- **Compat:** Rule 8: zero-dependency, permissive-only, with a changelog entry. New functions and fields are additive.

### Frontend client and head injection

- **Paths:** `web/xbin-client.js:44-58`, `web/xbin-client.js:81-126`, `web/xbin-client.js:147-201`, `web/xbin-client.js:243-284`, `web/xbin-client.js:338-412`, `internal/server/static.go:431-503`, `internal/server/tileassets.go:338-371`, `web/bx-kit.js:40-41`
- **Today:** xbin.self comes from the xbin-component meta. The frame token (opaque, 5 fields) is renewed via /frame-token?component=self. The backend is addressed as /api/<self>/ (xbin.fetch or selfApi). The bus uses res:<scope>/<name> ids. window.xbin is frozen. Tokens mode injects a <base> and remaps /c/<self>/.
- **Change needed:** Non-primary documents need a deployment-bound frame token (so /api/<self>/ routes to that deployment), possibly a deployment <meta> plus xbin.deployment, and deployment-scoped asset addressing (reusing the tokens-mode <base> and remap).
- **Compat:** The primary's documents must stay byte-identical (legacy injection rule, static.go:439). The token stays opaque. The 4-to-5-field precedent shows a token format can grow with a legacy verify path.

### Event stream

- **Paths:** `internal/events/events.go:10-17`, `docs/protocol.md:2179-2233`, `internal/boot/serve.go:162-199`, `web/bx-frame.js:365-394`, `internal/obs/status.go:126-143`, `native/ios/Packages/XbinCore/Sources/XbinCore/Client/Events.swift:48-84`, `web/events-socket.js`
- **Today:** reload/build-* events keyed only by component. They drive bx-frame reloads and overlays, status clearing and the iOS app. The watch loop publishes reload for every changed tile and calls runner.Changed.
- **Change needed:** New event type(s) for pause/resume/deploy/promote and for non-primary build progress. Suppress plain reload/build-* for the tile path when the change doesn't affect what primary viewers see.
- **Compat:** Old shells, bx-frame and the shipped iOS app ignore unknown types (compat rule 10). Reusing reload/build-* for dev would reload main viewers, clear main's status and overlay main frames.

### Proxy routing /api/<tile>/

- **Paths:** `internal/proxy/proxy.go:128-230`, `docs/protocol.md:406-419`, `internal/registry/registry.go:517-536`
- **Today:** Longest-prefix Resolve picks the component, then Runner.Ensure(comp) returns the one socket. Lifecycle gates with 409.
- **Change needed:** Pick the deployment from the principal (frame/instance/terminal token) or a consumed selector. Default is primary. Incoming traffic from other tiles and ingress goes to primary only.
- **Compat:** No selector means today's path. The URL space is unchanged, so every tile's /api/${self} keeps working.

### Resources / vault / cron / bus subscriptions / iface-instances

- **Paths:** `docs/resources.md:1-38`, `docs/resources.md:143-196`, `docs/protocol.md:1627-1650`, `docs/protocol.md:1715-1760`, `docs/protocol.md:1930-1959`, `internal/util/util.go:125-133`, `docs/elements.md:548-563`
- **Today:** Resources are scope-keyed (res:<scope>/<name>) and shared by all components in a scope. The vault is component-keyed. Cron/bus subscriptions are component and name keyed, idempotent. iface-instances and ingress-hosts replace per component.
- **Change needed:** Per-deployment storage for dev (resources, maybe vault). Per-deployment keys for cron jobs, subscriptions and self-registrations (or refuse them from non-primary). Per-deployment status and logs.
- **Compat:** If the primary maps to today's storage keys, no data migration is needed and existing data stays in place. Dev stores are new keys under data/ (on-disk layout is not promised by compat.md).

### HTTP API additions + registration

- **Paths:** `internal/server/openapi.go:17-90`, `internal/apicheck/apicheck_test.go:119-200`, `docs/maintenance.md:476-505`, `internal/server/api.go:71-76`, `internal/server/api.go:130-160`, `internal/boot/api.go:22-117`, `internal/broker/lifecycle.go:17-116`
- **Today:** Every route is a RegisterAPI literal plus an openapi ep row plus a protocol.md fenced row. DecodeJSON is strict. /components, /tile-status, /runtime, /whoami and /backends are keyed by component path.
- **Change needed:** A new endpoint family (list, pause/resume, reload-now, deploy, promote, set-primary). omitempty fields on /components, /tile-status, /runtime rows and /whoami. An optional deployment query on /logs.
- **Compat:** Rule 2: additive only, no new keys in the /backends map (the old shell counts them). New request fields on existing bodies are 400s on older servers.

### Terminal window UI (vendor, always current)

- **Paths:** `web/bx-frame.js:1-60`, `web/bx-frame.js:533-541`, `web/frame-titlebar.js:23-62`, `web/frame-titlebar.js:120-213`, `web/frame-launcher.js`, `web/bx-prs.js`, `web/bx-logs.js`, `docs/overview/09-terminals.md:541-547`
- **Today:** bx-frame hosts the pop-up with layouts term/code/split/logs/prs, the net/API/VM/GPU pickers and the layer buttons. open(layout) is the shell's entry point. bx-frame.js is at 866/900 lines.
- **Change needed:** Pause toggle plus a reload-now button, deployment picker/panel, maybe a deployment attribute on <bx-frame>. Build in a new sibling module (bx-deploy.js), with barKey updated for fitBar.
- **Compat:** /vendor/ ships with the binary, so old workspaces get it. Old shells simply never call the new open() value. Harness passes must use testApi. Theme fallbacks must match theme.css.

### Shell / admin scaffold (workspace-owned copies)

- **Paths:** `workspace-template/shell/menus.js:94-124`, `workspace-template/shell/bx-shell.js:195`, `workspace-template/shell/bx-shell.js:677`, `workspace-template/shell/bx-shell.js:1292-1340`, `workspace-template/tiles/admin/tabs/runtime.js:80`, `workspace-template/shell/bx-tile-admin.js`
- **Today:** Tile menu items (terminal/logs/source/proposals, lifecycle). Window spawn src built as from + '/' + path. /backends is counted for the footer. Old copies persist in adopted workspaces.
- **Change needed:** Optional: menu entries and admin tab views for deployments. xbin.window in a dev frame needs the deployment carried in the bx-spawn detail.
- **Compat:** Rule 4: add sibling files, keep entry names. bx-shell.js has 13 lines of headroom. Old shells ignore new fields, so dev windows would frame primary content.

### bx CLI

- **Paths:** `cmd/bx/main.go:26-106`, `cmd/bx/main.go:111-174`, `cmd/bx/main.go:209-283`, `cmd/bx/main.go:349-370`, `cmd/bx/main.go:494-530`, `cmd/bx/agent.go:30-46`, `cmd/bx/native.go:26-41`, `cmd/bx/args.go`, `docs/bx.md`
- **Today:** Hand-parsed commands. Auth via XBIN_TOKEN, gateway or host token. Tile defaults from XBIN_COMPONENT. bx logs reads .xbin/log directly. No restart/reload/deploy command. main.go is at its 1150-line budget.
- **Change needed:** New top-level subcommand (e.g. deploy/pause/reload/promote) via moreCmds. Lenient --deployment on logs/status (move them out of main.go first). Usage appended like nativeUsage. bx doctor check.
- **Compat:** Rule 6: every current invocation must still parse. logs/status accept any extra argument as the component today. New names are free (unknown commands currently exit 2 via usage()).

### Git remote precedent (blessed runtime-injected remote)

- **Paths:** `internal/broker/templaterepo.go:139-150`, `internal/broker/templates.go:25-31`, `internal/term/term.go:736-797`, `internal/broker/code.go:90-118`, `docs/protocol.md:1359-1420`
- **Today:** Instances get a read-only 'template' remote at http://xbin/api/xbin/templates/<name>.git, written via confined git. Terminals rewrite http://xbin/ to XBIN_URL with the session bearer through GIT_CONFIG_* env. Every component is its own repo (git init -b main).
- **Change needed:** A deploy remote such as http://xbin/api/xbin/<...>.git with receive-pack, plus branch/ref-based deployments. Server side must run in confine (D78).
- **Compat:** New routes under /api/xbin with {rest...}. The remote is added idempotently like AddTemplateRemote. Imported repos may not use 'main' as their default branch.

### Builder docs (served, always current)

- **Paths:** `docs/elements.md:356-360`, `docs/elements.md:500-546`, `docs/getting-started.md:85`, `docs/index.md:5-7`, `docs/overview/03-components.md:208-228`, `docs/overview/04-frontend.md:112-116`, `docs/overview/04-frontend.md:269-285`, `docs/overview/09-terminals.md`, `docs/overview/14-lifecycle.md`, `docs/sdk.md`, `docs/resources.md`, `docs/protocol.md`, `docs/bx.md`
- **Today:** Everything teaches save goes live: hot-reload on save, no deploy step, blue/green on every save.
- **Change needed:** Describe paused hot reload, deployments, what gets primary traffic, how resources and state split, and the new env/header/API/bx surfaces.
- **Compat:** Docs are embedded, so no plans/ citations and D-ids must resolve (docscheck). New docs should be linked from docs/index.md and the AGENTS.md 'also:' list.

### Workspace AGENTS.md + welcome notes

- **Paths:** `workspace-template/AGENTS.md:15-18`, `workspace-template/AGENTS.md:34-36`, `workspace-template/AGENTS.md:150-178`, `workspace-template/AGENTS.md:305-311`, `workspace-template/AGENTS.md:472-486`, `workspace-template/AGENTS.md:534-535`, `workspace-template/AGENTS.md:542-603`, `workspace-template/AGENTS.md:883-884`, `workspace-template/apps/welcome/notes.js`, `internal/boot/boot.go:153-160`, `internal/builtins/updates.go:150-175`
- **Today:** Tells agents: save = live, commit often and unprompted (CM-2), test from the terminal against the live tile, code PRs apply with git am. Seeded only if absent; not a scaffold unit.
- **Change needed:** Explain which deployment a save reaches, how to test dev, when to promote, and how commits and pushes relate to deploying (the CM-2 tension). Debug recipes per deployment.
- **Compat:** Existing workspaces never get the new AGENTS.md automatically, so the changelog and docs carry it. notes.js has 22 lines of headroom and is a scaffold unit (old copies persist).

### Changelog / decisions / guards

- **Paths:** `AGENTS.md:115-159`, `docs/changelog.md:1-12`, `docs/changes/`, `plans/DECISIONS.md`, `internal/docscheck/docscheck_test.go:25-33`, `hack/size-budget.txt`, `assets_test.go:27-87`, `hack/check-js.mjs:88-100`, `docs/maintenance.md:64-96`, `docs/maintenance.md:189-196`
- **Today:** Changelog entry per builder-visible change. Migration note for BREAKING changes. Decision ids limited to known prefixes. Size ratchet. Embed and js/theme checks. TestConfigDoc for flags.
- **Change needed:** Changelog entries for every new surface. D1xx+ decisions (or a new prefix added to the docscheck regex). A new plans/*.md with a Status line. Budget-driven splits before editing at-budget files.
- **Compat:** No BREAKING entry is needed if everything is opt-in. Any path that later turns into an error must warn for one release (rule 11).

## Hazards

- cmd/bx/main.go (1150/1150) and internal/term/term.go (950/950) have zero size-budget headroom, runner.go has 17 lines and bx-frame.js 34 lines, so any edit there must first move code out (hack/size-budget.txt; internal/sizebudget); raising a budget is explicitly the wrong fix (docs/maintenance.md:71-72).
- Encoding a deployment into the component path (e.g. apps/x@dev) breaks every self-derived URL: vault calls built from Self() (sdk/xbin.go:265,300), `/api/${xbin.self}`, `res:${xbin.self}` bus ids and frame-token renewal (web/xbin-client.js:83); and no separator is truly free (util.ComponentPathOK allows @, ~, # and :, and `#` is already the iface-instance separator).
- Publishing plain reload/build-* events for a non-primary deployment reloads main's frames and overlays build errors on them (web/bx-frame.js:370-378), clears main's status on build-start (internal/obs/status.go:129-143), and reloads views in shipped iOS apps (Events.swift:54-57).
- Reporting pause as a lifecycle `state` makes old shells offer 'Enable' on a running tile (workspace-template/shell/menus.js:111-116), and any non-enabled state already makes the proxy answer 409 (internal/proxy/proxy.go:144-157).
- The root xbin.json is re-marshalled through a Go struct (internal/registry/registry.go:586-595), so deployment state stored there is silently dropped by any older binary's next grant change, and boot must never rewrite it (test/legacy_workspace_test.go:69-71).
- A dev backend following the documented start-up patterns (xbin.Subscribe at every start, sdk.md:98-111; iface-instance or ingress-host registration, which REPLACE the map/set, protocol.md:1627-1650) would clobber main's subscriptions and live routing unless those stores are keyed by deployment.
- Grants and bindings stay per tile and X-XBin-From is the bare tile path (protocol.md:62-80), so without a parallel fabric a dev deployment calls other tiles' primaries with prod rights and callees can't tell, which undercuts 'dev must not see prod data'.
- Resources are scope-keyed and shared by every component in a scope (docs/elements.md:548-563; docs/resources.md:209-217,262-265), while deployments are per tile, so per-deployment resource separation needs an explicit rule for scope-shared stores.
- xbin.window sub-path windows are framed by the scaffold shell as `from + '/' + path` (workspace-template/shell/bx-shell.js:677) with no deployment; old shells will ignore any new field, so dev windows would show primary code.
- In the default legacy asset mode a non-primary frontend's relative URLs resolve to /c/<tile>/, the working tree (docs/elements.md:195-197,297-299), unless xbind injects a deployment-scoped <base> and import-map remap as tokens mode does (internal/server/tileassets.go:338-371).
- Terminology collision: docs already call lifecycle disable 'pause/resume a tile' (docs/bx.md:138; the ⏸ Disable menu item, menus.js:114), and 'identity' is the core term for the component path (docs/elements.md:5; docs/overview/05-identity.md).
- server.DecodeJSON rejects unknown fields (internal/server/api.go:71-76), so adding a deployment field to an existing request body (e.g. POST /term/sessions, POST /lifecycle) gets a 400 from older servers and lagging clients; new endpoints avoid this.
- Workspace AGENTS.md is seeded only when absent and is not a builtin scaffold unit (internal/boot/boot.go:153-160; internal/builtins/updates.go:150-175), so guidance about deployments never reaches existing workspaces' agents except through /docs/ and the changelog.
- CM-2 ('commit often and unprompted', workspace-template/AGENTS.md:160-166) turns into 'deploy unprompted' if a deployment follows a branch or accepts pushes automatically; deploying must stay a distinct, deliberate act, or the agent guidance has to change.
- bx logs reads .xbin/log/<CompKey>.log directly (cmd/bx/main.go:507-515), but .xbin is masked in isolated tile terminals (internal/term/binds.go:43-53), so (inference) it already fails there; per-deployment logs should use GET /logs (internal/obs/logs.go:51-69).
- The route inventory fails make test unless every new route has a RegisterAPI literal reachable by the fixture, an openapi.go ep row and a protocol.md fenced row (internal/apicheck/apicheck_test.go:119-200; docs/maintenance.md:491-505).
- docscheck only validates decision ids with prefixes D|ND|ING|LC|IFACE|VD|RT|ISO|BU|CM|PR (internal/docscheck/docscheck_test.go:25,28); a new series like DEP-n would go unchecked unless the regex is extended.
- Embedded trees (web/, docs/, workspace-template/, builtin-*) must not cite plans/ (assets_test.go:27-87), so design references in new builder docs and shipped JS must use D-ids and /docs/ links.
- Imported tile repos keep their upstream default branch and nested components have their own repos, while xbind-initialised repos use `main` (internal/broker/code.go:90-107), so 'the main deployment follows branch main' can't be assumed.
- The old scaffold shell counts Object.values(/backends) for its footer (workspace-template/shell/bx-shell.js:1326-1333), so extra per-deployment keys in that map would inflate counts; add fields to rows, not new keys.

## Open questions

- Which credential selects a non-primary deployment for /api/<tile>/ requests: a frame token bound to a deployment, a consumed selector header or query parameter, or a separate URL plane like /c/~…? And what gate applies (self, terminal-level as for /logs, or owner as for /lifecycle)?
- Should XBIN_DEPLOYMENT (and X-XBin-Deployment) be injected for the primary as well (additive but changes every backend's env), or only for non-primary deployments (true zero-change)?
- Should the vault be per deployment (dev must not read prod secrets) or shared? The SDK builds vault URLs from Self(), so separation would have to be server-side.
- How do scope-shared resources split: does dev of tile A get a dev copy of res:<scope>/db that dev of sibling tile B also uses (a parallel fabric per scope), or is it private to A-dev?
- Are cron jobs, bus push subscriptions, iface-instance and ingress-host registrations, tile-report status and NotifyUser allowed from non-primary deployments (keyed per deployment), or refused or suppressed?
- Does pause freeze only the backend, or also the frontend files served at /c/<tile>/? The latter needs a snapshot, because /c/ serves the working tree live with no-store.
- When main is paused and no dev deployment exists, where do saves go and what does the terminal user see? And does 'reload now' also run the build/health check and publish ordinary reload/build events?
- Is pause per tile (server state that every user's terminal window reflects) or per user? Which level may toggle it (terminal, write, or owner), and does an agent session's own token count?
- In the branch variant, which branch does the working tree hold, and how do xbind's own writes (builtin update replace/merge, restore, template seeding) and code-PR `git am` interact with deployments?
- Should deployment configuration (list, primary, branch mapping) live in data/ (xbind-owned, backed up) or in the root xbin.json next to lifecycle (older binaries drop unknown keys there)? Should it be removed as a path 'leftover' when the tile is deleted (broker/policy.go newTilePathOK)?
- What UI and wire name should the feature use, given that 'pause' already means lifecycle disable and 'identity' already means the component path?
- Do the admin console and the scaffold shell get deployment views (which reach only new or updated workspaces), or does all UI live in the /vendor/ terminal window?
- Should promotion copy or migrate data between deployments (e.g. a dev schema migration to prod), or are deployments code-only, with each keeping its own data?
